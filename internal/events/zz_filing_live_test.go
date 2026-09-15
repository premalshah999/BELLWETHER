package events

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	itemRe  = regexp.MustCompile(`(?s)<item[ >](.*?)</item>`)
	fieldRe = func(t string) *regexp.Regexp { return regexp.MustCompile(`(?s)<` + t + `>(.*?)</` + t + `>`) }
)

func tagOf(s, t string) string {
	m := fieldRe(t).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	v := strings.TrimSpace(m[1])
	v = strings.ReplaceAll(v, "&amp;", "&")
	return v
}

var feedToSource = map[string]string{
	"Online_announcements": "nse-announcements",
	"Corporate_action":     "nse-corp-actions",
	"Financial_Results":    "nse-results",
	"Board_Meetings":       "nse-board-meetings",
	"Insider_Trading":      "nse-insider",
	"Shareholding_Pattern": "nse-shareholding",
	"Annual_Reports":       "nse-annual-reports",
	"Circulars":            "nse-circulars",
}

func TestFilingParserOnLiveFeeds(t *testing.T) {
	files, _ := filepath.Glob("/tmp/nse_*.xml")
	if len(files) == 0 {
		t.Skip("no NSE fixtures")
	}
	ist, _ := time.LoadLocation("Asia/Kolkata")

	byType := map[Type]int{}
	var unclassified []string
	var symbolHits, symbolMisses, total int
	var samples []string

	for _, path := range files {
		feed := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "nse_"), ".xml")
		sourceID, ok := feedToSource[feed]
		if !ok {
			continue
		}
		b, _ := os.ReadFile(path)
		for _, it := range itemRe.FindAllStringSubmatch(string(b), -1) {
			title, desc, link := tagOf(it[1], "title"), tagOf(it[1], "description"), tagOf(it[1], "link")
			if title == "" {
				continue
			}
			total++
			f := ParseFiling(sourceID, title, desc, link, ist)
			byType[f.Type]++
			if f.Type == TypeUnclassified && len(unclassified) < 12 {
				unclassified = append(unclassified, sourceID+" | "+f.Subject)
			}
			if f.Symbol != "" {
				symbolHits++
			} else {
				symbolMisses++
			}
			// Collect examples of extracted structured facts.
			if len(samples) < 14 {
				if d, ok := f.DividendPerShare(); ok {
					samples = append(samples, "DIVIDEND  "+f.Company[:min(28, len(f.Company))]+"  Rs "+ftoa(d)+"/share  ex="+f.OccurredAt.In(ist).Format("02-Jan"))
				} else if p, ok := f.PromoterHolding(); ok {
					pub, _ := f.PublicHolding()
					samples = append(samples, "HOLDING   "+f.Company[:min(28, len(f.Company))]+"  promoter="+ftoa(p)+"% public="+ftoa(pub)+"%")
				} else if f.Type == TypeEarnings && f.Summary != "" {
					samples = append(samples, "RESULTS   "+f.Company[:min(28, len(f.Company))]+"  "+f.Summary[:min(46, len(f.Summary))])
				} else if f.Type == TypeOrderWin {
					samples = append(samples, "ORDER_WIN "+f.Company[:min(28, len(f.Company))]+"  "+f.Summary[:min(46, len(f.Summary))])
				}
			}
		}
	}

	t.Logf("parsed %d filings; symbol from path: %d (%.0f%%), absent: %d",
		total, symbolHits, 100*float64(symbolHits)/float64(total), symbolMisses)
	type kv struct {
		t Type
		n int
	}
	var list []kv
	for k, v := range byType {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	t.Log("--- classification ---")
	for _, e := range list {
		t.Logf("  %5d  %-24s importance=%d equity=%v", e.n, e.t, e.t.BaselineImportance(), e.t.Equity())
	}
	if len(unclassified) > 0 {
		t.Log("--- unrecognised subjects ---")
		for _, u := range unclassified {
			t.Logf("  %s", u)
		}
	}
	t.Log("--- structured facts extracted without a model ---")
	for _, s := range samples {
		t.Logf("  %s", s)
	}
}

func ftoa(f float64) string {
	s := strings.TrimRight(strings.TrimRight(formatFloat(f), "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}
func formatFloat(f float64) string { return fmt.Sprintf("%.2f", f) }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
