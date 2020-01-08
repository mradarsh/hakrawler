package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/gocolly/colly"
	"github.com/google/uuid"
	. "github.com/logrusorgru/aurora"
	sitemap "github.com/oxffaa/gopher-parse-sitemap"
)

var (
	// regular expression from LinkFinder.
	LinkFinderRegex, _ = regexp.Compile(`(?:"|')(((?:[a-zA-Z]{1,10}://|//)[^"'/]{1,}\.[a-zA-Z]{2,}[^"']{0,})|((?:/|\.\./|\./)[^"'><,;| *()(%%$^/\\\[\]][^"'><,;|()]{1,})|([a-zA-Z0-9_\-/]{1,}/[a-zA-Z0-9_\-/]{1,}\.(?:[a-zA-Z]{1,4}|action)(?:[\?|/][^"|']{0,}|))
|([a-zA-Z0-9_\-]{1,}\.(?:php|asp|aspx|jsp|json|action|html|js|txt|xml)(?:\?[^"|']{0,}|)))(?:"|')`)
)

func banner(au Aurora) {
	fmt.Print(`Welcome`)
}

// if -plain is set, just print the message, otherwise print a coloured tag and then the message
func colorPrint(tag Value, msg string, plain bool) {
	if plain {
		fmt.Println(msg)
	} else {
		fmt.Println(tag, msg)
	}
}

func printToRandomFile(msg string, dir string) {
	uuid, _ := uuid.NewRandom()
	if dir[len(dir)-1:] != "/" {
		dir = dir + "/"
	}

	err := ioutil.WriteFile(dir+"hakrawler_"+uuid.String()+".req", []byte(msg), 0644)
	if err != nil {
		log.Fatalf("ERROR: %v\n", err)
	}
}

// determines whether the domains/urls should be printed based on the provided scope (returns true/false)
func printIfInScope(scope string, tag Value, schema string, domain string, msg string, plain bool, outdirPtr *string) bool {
	base, err := url.Parse(schema + domain)
	if err != nil {
		// Error parsing base domain
		return false
	}

	var msgSchema string

	if !strings.Contains(msg, "http://") && !strings.Contains(msg, "https://") && !strings.HasPrefix(msg, "/") {
		msgSchema = "https://"
	} else {
		msgSchema = ""
	}

	urlObj, err := url.Parse(msgSchema + msg)
	if err != nil {
		// the url can't be parsed, move on with reckless abandon
		return false
	}
	urlObj = base.ResolveReference(urlObj)

	shouldPrint := false

	switch scope {
	case "strict":
		shouldPrint = urlObj.Host == domain
	case "fuzzy":
		shouldPrint = strings.Contains(urlObj.Host, domain)
	case "subs":
		shouldPrint = strings.HasSuffix(urlObj.Host, domain)
	default:
		shouldPrint = true
	}

	if shouldPrint {
		strVal := urlObj.String()
		// Remove the schema if it was added before
		if msgSchema != "" {
			strVal = strings.Replace(strVal, msgSchema, "", 1)
		}
		colorPrint(tag, strVal, plain)
		if *outdirPtr != "" {
			printToRandomFile(rawHTTPGET(msg), *outdirPtr)
		}
	}
	return shouldPrint
}

func rawHTTPGET(url string) string {
	// some sanity checking
	if !strings.Contains(url, "http") {
		return ""
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}
	raw, err := httputil.DumpRequest(req, true)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseSitemap(domain string, depth int, c colly.Collector, printResult bool, mainwg *sync.WaitGroup, schema string, au Aurora, plain bool, scope string, outdirPtr *string) {
	defer mainwg.Done()
	sitemapURL := schema + domain + "/sitemap.xml"
	sitemap.ParseFromSite(sitemapURL, func(e sitemap.Entry) error {
		if printResult {
			_ = printIfInScope(scope, sitemap, schema, domain, e.GetLocation(), plain, outdirPtr)
		}
		// if depth is greater than 1, add sitemap url as seed
		if depth > 1 {
			c.Visit(e.GetLocation())
		}
		return nil
	})
}

func parseRobots(domain string, depth int, c colly.Collector, printResult bool, mainwg *sync.WaitGroup, schema string, au Aurora, plain bool, scope string, outdirPtr *string) {
	defer mainwg.Done()
	var robotsurls []string
	robotsURL := schema + domain + "/robots.txt"

	resp, err := http.Get(robotsURL)
	if err != nil {
		return
	}
	if resp.StatusCode == 200 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return
		}
		lines := strings.Split(string(body), "\n")

		var re = regexp.MustCompile(".*llow: ")
		for _, line := range lines {
			if strings.Contains(line, "llow: ") {
				urlstring := re.ReplaceAllString(line, "")
				if printResult {
					_ = printIfInScope(scope, robots, schema, domain, schema+domain+urlstring, plain, outdirPtr)
				}
				//add it to a slice for parsing later
				robotsurls = append(robotsurls, schema+domain+urlstring)
			}
		}
	}
	// if depth is greater than 1, add all of the robots urls as seeds
	if depth > 1 {
		for _, robotsurl := range robotsurls {
			c.Visit(robotsurl)
		}
	}
}
func linkfinder(jsfile string, tag Value, plain *bool) {
	// skip tls verification
	client := http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := client.Get(jsfile)
	if err != nil {
		return
	}
	//  if js file exists
	if resp.StatusCode == 200 {
		res, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return
		}
		resp.Body.Close()
		found := LinkFinderRegex.FindAllString(string(res), -1)
		for _, link := range found {
			colorPrint(tag, link, *plain)
		}

	}
}
func crawl(domain string, depthPtr *int, outdirPtr *string, includeJSPtr *bool, includeSubsPtr *bool, includeURLsPtr *bool, includeFormsPtr *bool, includeRobotsPtr *bool, includeSitemapPtr *bool, includeWaybackPtr *bool, includeAllPtr *bool, cookiePtr *string, authHeaderPtr *string, scopePtr *string, schemaPtr *string, wayback *bool, runlinkfinder *bool, plain *bool, au Aurora, domainwg *sync.WaitGroup) {

	// make sure the domain has been set
	if domain == "" {
		fmt.Println("[error]", "You must set a domain, e.g. -domain=example.com")
		fmt.Println("[info]", "See hakrawler -h for commandline options")
		os.Exit(1)
	}

	// set up the schema
	schema := "http://"
	if *schemaPtr == "https" {
		schema = "https://"
	}

	// set up the bools
	if *includeJSPtr || *includeSubsPtr || *includeURLsPtr || *includeFormsPtr || *includeRobotsPtr || *includeSitemapPtr {
		*includeAllPtr = false
	}

	// these will store the discovered assets to avoid duplicates
	urls := make(map[string]struct{})
	subdomains := make(map[string]struct{})
	jsfiles := make(map[string]struct{})
	forms := make(map[string]struct{})

	// behold, the colly collector
	c := colly.NewCollector(
		colly.MaxDepth(*depthPtr),
		// this is not fooling anyone XD
		colly.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/78.0.3904.108 Safari/537.36"),
		//colly.Async(true),
	)

	// set custom headers if specified
	if *cookiePtr != "" {
		c.OnRequest(func(r *colly.Request) {
			r.Headers.Set("Cookie", *cookiePtr)
		})
	}

	if *authHeaderPtr != "" {
		c.OnRequest(func(r *colly.Request) {
			r.Headers.Set("Authorization", *authHeaderPtr)
		})
	}

	// find and visit the links
	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		var urlString string = e.Request.AbsoluteURL(e.Attr("href"))
		// if the url isn't already there, print and save it, if it's a new subdomain, print that too
		if _, ok := urls[urlString]; !ok {
			if urlString != "" {
				var urlObj, err = url.Parse(urlString)
				// ditch unparseable URLs
				if err != nil {
					fmt.Println(err)
				} else {
					if *includeURLsPtr || *includeAllPtr {
						_ = printIfInScope(*scopePtr, url, schema, domain, urlString, *plain, outdirPtr)
						urls[urlString] = struct{}{}
					}
					// if this is a new subdomain, print it
					if *includeSubsPtr || *includeAllPtr {
						if _, ok := subdomains[urlObj.Host]; !ok {
							if urlObj.Host != "" {
								_ = printIfInScope(*scopePtr, subdomain, schema, domain, urlObj.Host, *plain, outdirPtr)
								subdomains[urlObj.Host] = struct{}{}
							}
						}
					}
				}
			}
			e.Request.Visit(e.Attr("href"))
		}
	})

	// find and print all the JavaScript files
	if *includeJSPtr || *includeAllPtr {
		c.OnHTML("script[src]", func(e *colly.HTMLElement) {
			jsfile := e.Request.AbsoluteURL(e.Attr("src"))
			if _, ok := jsfiles[jsfile]; !ok {
				if jsfile != "" {
					inScope := printIfInScope(*scopePtr, javascript, schema, domain, jsfile, *plain, outdirPtr)
					if inScope {
						if *runlinkfinder {
							linkfinder(jsfile, linkfinder, plain)
						}
					}
					jsfiles[jsfile] = struct{}{}
				}
			}
		})
	}

	// find and print all the form action URLs
	if *includeFormsPtr || *includeAllPtr {
		c.OnHTML("form[action]", func(e *colly.HTMLElement) {
			form := e.Request.AbsoluteURL(e.Attr("action"))
			if _, ok := forms[form]; !ok {
				if form != "" {
					_ = printIfInScope(*scopePtr, form, schema, domain, form, *plain, outdirPtr)
					forms[form] = struct{}{}
				}
			}
		})
	}

	// figure out if the results from robots.txt should be printed
	var printRobots bool
	if *includeRobotsPtr || *includeAllPtr {
		printRobots = true
	} else {
		printRobots = false
	}

	// figure out of the results from sitemap.xml should be printed
	var printSitemap bool
	if *includeSitemapPtr || *includeAllPtr {
		printSitemap = true
	} else {
		printSitemap = false
	}

	// do all the things
	// setup a waitgroup to run all methods at the same time
	var mainwg sync.WaitGroup

	// robots.txt
	mainwg.Add(1)
	go parseRobots(domain, *depthPtr, *c, printRobots, &mainwg, schema, au, *plain, *scopePtr, outdirPtr)

	// sitemap.xml
	mainwg.Add(1)
	go parseSitemap(domain, *depthPtr, *c, printSitemap, &mainwg, schema, au, *plain, *scopePtr, outdirPtr)

	// waybackurls
	if *wayback {
		go func() {
			mainwg.Add(1)
			defer mainwg.Done()
			// get results from waybackurls
			waybackurls := WaybackURLs(domain)

			// print wayback results, if depth >1, also add them to the crawl queue
			for _, waybackurl := range waybackurls {
				if *includeWaybackPtr || *includeAllPtr {
					_ = printIfInScope(*scopePtr, wayback, schema, domain, waybackurl, *plain, outdirPtr)
				}
				// if this is a new subdomain, print it
				urlObj, err := url.Parse(waybackurl)
				if err != nil {
					continue
				}

				if *includeSubsPtr || *includeAllPtr {
					if _, ok := subdomains[urlObj.Host]; !ok {
						if urlObj.Host != "" {
							if strings.Contains(urlObj.Host, domain) {
								_ = printIfInScope(*scopePtr, subdomain, schema, domain, urlObj.Host, *plain, outdirPtr)
								subdomains[urlObj.Host] = struct{}{}
							}
						}
					}
				}
				if *depthPtr > 1 {
					c.Visit(waybackurl)
				}
			}
		}()
	}

	// colly
	mainwg.Add(1)
	go func() {
		defer mainwg.Done()
		c.Visit(schema + domain)
	}()

	mainwg.Wait()
	domainwg.Done()
}

func main() {
	// define and parse command line flags
	domainPtr := flag.String("domain", "", "The domain that you wish to crawl (for example, google.com)")
	depthPtr := flag.Int("depth", 1, "Maximum depth to crawl, the default is 1. Anything above 1 will include URLs from robots, sitemap, waybackurls and the initial crawler as a seed. Higher numbers take longer but yield more results.")
	outdirPtr := flag.String("outdir", "", "Directory to save discovered raw HTTP requests")
	cookiePtr := flag.String("cookie", "", "The value of this will be included as a Cookie header")
	authHeaderPtr := flag.String("auth", "", "The value of this will be included as a Authorization header")
	scopePtr := flag.String("scope", "subs", "Scope to include:\nstrict = specified domain only\nsubs = specified domain and subdomains\nfuzzy = anything containing the supplied domain\nyolo = everything")
	schemaPtr := flag.String("schema", "http", "Schema, http or https")
	wayback := flag.Bool("usewayback", false, "Query wayback machine for URLs and add them as seeds for the crawler")
	plain := flag.Bool("plain", false, "Don't use colours or print the banners to allow for easier parsing")
	runlinkfinder := flag.Bool("linkfinder", false, "Run linkfinder on javascript files.")

	// which data to include in output?
	includeJSPtr := flag.Bool("js", false, "Include links to utilised JavaScript files")
	includeSubsPtr := flag.Bool("subs", false, "Include subdomains in output")
	includeURLsPtr := flag.Bool("urls", false, "Include URLs in output")
	includeFormsPtr := flag.Bool("forms", false, "Include form actions in output")
	includeRobotsPtr := flag.Bool("robots", false, "Include robots.txt entries in output")
	includeSitemapPtr := flag.Bool("sitemap", false, "Include sitemap.xml entries in output")
	includeWaybackPtr := flag.Bool("wayback", false, "Include wayback machine entries in output")
	includeAllPtr := flag.Bool("all", true, "Include everything in output - this is the default, so this option is superfluous")
	flag.Parse()

	au := NewAurora(!*plain)

	// print the banner
	if !*plain {
		banner(au)
	}

	// decide whether to use -domain or stdin
	var domainwg sync.WaitGroup
	if *domainPtr != "" {
		// in this case, the waitgroup is not necessary as there is only 1 domain
		// I added it anyway because the function is expecting a wg pointer
		// There's a better way to do this
		domain := *domainPtr
		domainwg.Add(1)
		go crawl(domain, depthPtr, outdirPtr, includeJSPtr, includeSubsPtr, includeURLsPtr, includeFormsPtr, includeRobotsPtr, includeSitemapPtr, includeWaybackPtr, includeAllPtr, cookiePtr, authHeaderPtr, scopePtr, schemaPtr, wayback, runlinkfinder, plain, au, &domainwg)
	} else {
		// get domains from stdin
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			domainwg.Add(1)
			domain := strings.ToLower(sc.Text())
			go crawl(domain, depthPtr, outdirPtr, includeJSPtr, includeSubsPtr, includeURLsPtr, includeFormsPtr, includeRobotsPtr, includeSitemapPtr, includeWaybackPtr, includeAllPtr, cookiePtr, authHeaderPtr, scopePtr, schemaPtr, wayback, runlinkfinder, plain, au, &domainwg)
		}
	}
	domainwg.Wait()
}
