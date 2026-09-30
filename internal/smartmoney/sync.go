package smartmoney

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Syncer fills the store from SEC.
type Syncer struct {
	Store Store
	SEC   *SEC
	HTTP  *http.Client
	Log   *slog.Logger
	// Universe maps an issuer CIK (no leading zeros) to its ticker. Only
	// filings about these companies are fetched.
	Universe map[string]string
	// FIGIKey is an optional OpenFIGI key: forty times the keyless rate for
	// turning 13F CUSIPs into tickers.
	FIGIKey string
}

// SyncInsiders reads the daily EDGAR indexes for the last `days` days and
// stores every Form 4 about a company in the universe that has not been
// read before. Returns how many trades were stored.
func (s *Syncer) SyncInsiders(ctx context.Context, days int) (int, error) {
	stored := 0
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for d := 0; d < days; d++ {
		day := today.AddDate(0, 0, -d)
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		body, err := s.SEC.Get(ctx, DailyIndexURL(day))
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			continue // holiday, or the day's index not published yet
		}
		if err != nil {
			return stored, fmt.Errorf("daily index %s: %w", day.Format("2006-01-02"), err)
		}
		seen := map[string]bool{}
		for _, e := range ParseDailyIndex(bytes.NewReader(body)) {
			if e.Form != "4" {
				continue
			}
			sym, ok := s.Universe[e.CIK]
			if !ok || seen[e.Accession()] {
				continue
			}
			seen[e.Accession()] = true
			done, err := s.Store.InsiderFilingSeen(ctx, e.Accession())
			if err != nil {
				return stored, err
			}
			if done {
				continue
			}
			n, err := s.readForm4(ctx, e, sym)
			if err != nil {
				s.Log.Warn("form 4 not read", "accession", e.Accession(), "err", err)
			}
			stored += n
			if err := s.Store.MarkInsiderFiling(ctx, e.Accession(), err == nil); err != nil {
				return stored, err
			}
		}
	}
	return stored, nil
}

func (s *Syncer) readForm4(ctx context.Context, e IndexEntry, symbol string) (int, error) {
	doc, err := s.SEC.Get(ctx, "https://www.sec.gov/Archives/"+e.Path)
	if err != nil {
		return 0, err
	}
	_, trades, err := ParseForm4(doc, e.Accession(), e.Date)
	if err != nil {
		return 0, err
	}
	for i := range trades {
		trades[i].Symbol = symbol
	}
	if len(trades) == 0 {
		return 0, nil
	}
	return len(trades), s.Store.SaveInsiderTrades(ctx, trades)
}

// SyncFunds stores the two most recent 13F filings of every followed fund,
// so each has a quarter to compare against. The shipped list is written
// first, so a new release's managers appear without any action.
func (s *Syncer) SyncFunds(ctx context.Context) (int, error) {
	for _, f := range Funds {
		f.Curated = true
		if err := s.Store.UpsertFund(ctx, f); err != nil {
			return 0, err
		}
	}
	funds, err := s.Store.FollowedFunds(ctx)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, f := range funds {
		n, err := s.SyncFund(ctx, f)
		if err != nil {
			if ctx.Err() != nil {
				return added, ctx.Err()
			}
			s.Log.Warn("13f: fund not synced", "fund", f.Name, "err", err)
		}
		added += n
	}
	return added, nil
}

// SyncFund stores one fund's two most recent 13Fs, if not already stored.
func (s *Syncer) SyncFund(ctx context.Context, f Fund) (int, error) {
	body, err := s.SEC.Get(ctx, "https://data.sec.gov/submissions/CIK"+f.CIK+".json")
	if err != nil {
		return 0, fmt.Errorf("submissions unavailable: %w", err)
	}
	subs, err := ParseSubmissions(body)
	if err != nil {
		return 0, fmt.Errorf("submissions unreadable: %w", err)
	}
	added := 0
	for _, filing := range subs.Latest13F(2) {
		exists, err := s.Store.FundFilingExists(ctx, filing.Accession)
		if err != nil {
			return added, err
		}
		if exists {
			continue
		}
		filing.CIK = f.CIK
		if err := s.readFund13F(ctx, filing); err != nil {
			s.Log.Warn("13f: filing not read", "fund", f.Name, "accession", filing.Accession, "err", err)
			continue
		}
		added++
	}
	return added, nil
}

// Filer is a 13F filer found by name.
type Filer struct {
	CIK  string `json:"cik"`
	Name string `json:"name"`
}

// SearchFilers finds SEC filers by name through EDGAR's entity search.
func (s *Syncer) SearchFilers(ctx context.Context, query string) ([]Filer, error) {
	body, err := s.SEC.Get(ctx, "https://efts.sec.gov/LATEST/search-index?keysTyped="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	var res struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					Entity string `json:"entity"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	var out []Filer
	for _, h := range res.Hits.Hits {
		cik := fmt.Sprintf("%010s", strings.TrimLeft(h.ID, "0"))
		out = append(out, Filer{CIK: cik, Name: strings.TrimSpace(h.Source.Entity)})
	}
	return out, nil
}

// Has13F reports whether a filer has filed a 13F holdings report, and its
// registered name.
func (s *Syncer) Has13F(ctx context.Context, cik string) (bool, string, error) {
	body, err := s.SEC.Get(ctx, "https://data.sec.gov/submissions/CIK"+cik+".json")
	if err != nil {
		return false, "", err
	}
	subs, err := ParseSubmissions(body)
	if err != nil {
		return false, "", err
	}
	return len(subs.Latest13F(1)) > 0, subs.Name, nil
}

func (s *Syncer) readFund13F(ctx context.Context, f FundFiling) error {
	dir := fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s/",
		strings.TrimLeft(f.CIK, "0"), strings.ReplaceAll(f.Accession, "-", ""))
	body, err := s.SEC.Get(ctx, dir+"index.json")
	if err != nil {
		return err
	}
	var idx FilingIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return err
	}
	name := idx.InfoTableName()
	if name == "" {
		return fmt.Errorf("no information table in %s", f.Accession)
	}
	table, err := s.SEC.Get(ctx, dir+name)
	if err != nil {
		return err
	}
	holdings, err := ParseInfoTable(table)
	if err != nil {
		return err
	}
	if err := s.resolveCUSIPs(ctx, holdings); err != nil {
		s.Log.Warn("13f: cusip lookup incomplete", "err", err)
	}
	for _, h := range holdings {
		f.TotalValue += h.Value
	}
	f.Positions = len(holdings)
	return s.Store.SaveFundFiling(ctx, f, holdings)
}

// inlineCUSIPBudget bounds how many unknown CUSIPs a filing resolves before
// it is stored; the rest are resolved in the background, so a manager with
// thousands of positions is readable at once, by issuer name, until then.
const inlineCUSIPBudget = 50

// resolveCUSIPs fills Symbol on each holding it can: from the cache, then
// OpenFIGI for up to inlineCUSIPBudget of the rest, largest positions first.
func (s *Syncer) resolveCUSIPs(ctx context.Context, hs []Holding) error {
	var cusips []string
	for _, h := range hs {
		cusips = append(cusips, h.CUSIP)
	}
	known, err := s.Store.CUSIPSymbols(ctx, cusips)
	if err != nil {
		return err
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Value > hs[j].Value })
	var missing []string
	for _, h := range hs {
		if _, ok := known[h.CUSIP]; !ok {
			missing = append(missing, h.CUSIP)
		}
	}
	found, err := s.lookupCUSIPs(ctx, missing[:min(len(missing), inlineCUSIPBudget)])
	for c, sym := range found {
		known[c] = sym
	}
	for i := range hs {
		hs[i].Symbol = known[hs[i].CUSIP]
	}
	return err
}

// ResolvePendingCUSIPs looks up stored holdings' CUSIPs that were never
// resolved, for up to the time budget. Holdings read their ticker through
// the CUSIP table, so each one found shows at once.
func (s *Syncer) ResolvePendingCUSIPs(ctx context.Context, budget time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	total := 0
	for {
		pending, err := s.Store.UnresolvedCUSIPs(ctx, 500)
		if err != nil || len(pending) == 0 {
			return total, err
		}
		found, err := s.lookupCUSIPs(ctx, pending)
		total += len(found)
		if err != nil {
			if ctx.Err() != nil {
				return total, nil
			}
			return total, err
		}
	}
}

// lookupCUSIPs asks OpenFIGI for tickers, saving each batch as it arrives so
// an interrupted run keeps its progress.
func (s *Syncer) lookupCUSIPs(ctx context.Context, cusips []string) (map[string]string, error) {
	batch, gap := 10, 2600*time.Millisecond // keyless: 25 requests a minute, 10 each
	if s.FIGIKey != "" {
		batch, gap = 100, 250*time.Millisecond // keyed: 25 requests per 6 seconds, 100 each
	}
	found := map[string]string{}
	for i := 0; i < len(cusips); i += batch {
		got, err := s.figi(ctx, cusips[i:min(i+batch, len(cusips))])
		if err != nil {
			return found, err
		}
		if err := s.Store.SaveCUSIPSymbols(ctx, got); err != nil {
			return found, err
		}
		for c, sym := range got {
			found[c] = sym
		}
		select {
		case <-time.After(gap):
		case <-ctx.Done():
			return found, ctx.Err()
		}
	}
	return found, nil
}

func (s *Syncer) figi(ctx context.Context, cusips []string) (map[string]string, error) {
	type job struct {
		IDType  string `json:"idType"`
		IDValue string `json:"idValue"`
		Exch    string `json:"exchCode"`
	}
	jobs := make([]job, len(cusips))
	for i, c := range cusips {
		jobs[i] = job{"ID_CUSIP", c, "US"}
	}
	payload, _ := json.Marshal(jobs)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openfigi.com/v3/mapping", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.FIGIKey != "" {
		req.Header.Set("X-OPENFIGI-APIKEY", s.FIGIKey)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("openfigi: %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var results []struct {
		Data []struct {
			Ticker string `json:"ticker"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(cusips))
	for i, r := range results {
		if i >= len(cusips) {
			break
		}
		sym := ""
		if len(r.Data) > 0 {
			sym = strings.ReplaceAll(strings.ToUpper(r.Data[0].Ticker), "/", "-")
		}
		// Recorded even when empty, so an unmappable CUSIP is not asked again.
		out[cusips[i]] = sym
	}
	return out, nil
}
