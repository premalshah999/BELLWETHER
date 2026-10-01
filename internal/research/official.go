package research

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
)

// personalFinance matches questions whose answer turns on rules an agency
// sets: contribution limits, penalties, deadlines, benefit formulas.
var personalFinance = regexp.MustCompile(`(?i)\b(401\s?\(?k\)?|403\s?\(?b\)?|457\s?\(?b\)?|ira|iras|roth|hsa|529|retire|retirement|pension|annuit\w*|social security|medicare|rmds?|rollover|contribution limits?|capital gains?|tax(es|ed|ation)?|deduction|brokerage account|index funds?|target[- ]date)\b`)

var workplacePlan = regexp.MustCompile(`401|403|457|pension|retire`)

var accountKinds = regexp.MustCompile(`(?i)\b(401\s?\(?k\)?|403\s?\(?b\)?|457\s?\(?b\)?|roth ira|ira|hsa)\b`)

// accountKind is the tax-advantaged account a question names, written the
// way the IRS writes it, or "" when it names none.
func accountKind(query string) string {
	m := accountKinds.FindString(query)
	if m == "" {
		return ""
	}
	m = strings.ToLower(strings.NewReplacer(" ", "", "(", "", ")", "").Replace(m))
	switch {
	case strings.HasPrefix(m, "401"), strings.HasPrefix(m, "403"), strings.HasPrefix(m, "457"):
		return m[:3] + "(" + m[3:] + ")"
	case m == "rothira":
		return "Roth IRA"
	}
	return strings.ToUpper(m)
}

// officialSites names, per topic, the agency whose own pages settle it.
func officialSites(query string) []string {
	q := strings.ToLower(query)
	sites := []string{"irs.gov"}
	switch {
	case strings.Contains(q, "social security") || strings.Contains(q, "medicare"):
		sites = append(sites, "ssa.gov")
	case workplacePlan.MatchString(q):
		sites = append(sites, "dol.gov")
	default:
		sites = append(sites, "investor.gov")
	}
	return sites
}

// officialFindings searches the agencies' own sites for a personal-finance
// question. Publisher advice disagrees about this year's limits and rules;
// the IRS page does not.
func (e *Engine) officialFindings(ctx context.Context, query string) ([]Finding, *ScraperReport) {
	if !personalFinance.MatchString(query) {
		return nil, nil
	}
	var web Scraper
	for _, sc := range e.scrapers {
		if sc.Name() == "searxng_web" {
			web = sc
		}
	}
	if web == nil {
		return nil, nil
	}
	terms := strings.Join(QueryTerms(query), " ")
	searches := make([]string, 0, 3)
	for _, site := range officialSites(query) {
		searches = append(searches, terms+" site:"+site)
	}
	// The limits change every year and are what advice most often gets
	// wrong, so the current year's announcement is looked up by name.
	if plan := accountKind(query); plan != "" {
		searches = append(searches, fmt.Sprintf("%s contribution limit %d site:irs.gov", plan, time.Now().In(marketdata.Market).Year()))
	}
	rep := &ScraperReport{Name: "official_sites"}
	var out []Finding
	at := map[string]int{}
	for n, search := range searches {
		site := search[strings.LastIndex(search, "site:")+len("site:"):]
		limitSearch := n == len(searches)-1 && accountKind(query) != ""
		got, err := web.Search(ctx, search, 8)
		if err != nil {
			rep.Error = err.Error()
			continue
		}
		first := true
		for _, f := range got {
			if h := strings.ToLower(hostOf(f.URL)); h != site && !strings.HasSuffix(h, "."+site) {
				continue
			}
			i, seen := at[f.URL]
			if !seen {
				f.Scraper, f.Trust = rep.Name, news.TrustOfficial
				f.Publisher = site
				i = len(out)
				at[f.URL] = i
				out = append(out, f)
			}
			// The top answer to "<account> contribution limit <year>" is
			// the announcement itself.
			if limitSearch && first {
				out[i].Pinned = true
			}
			first = false
		}
	}
	rep.Count = len(out)
	return out, rep
}
