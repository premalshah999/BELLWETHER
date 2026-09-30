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
// so each has a quarter to compare against.
func (s *Syncer) SyncFunds(ctx context.Context) (int, error) {
	added := 0
	for _, f := range Funds {
		if err := s.Store.UpsertFund(ctx, f); err != nil {
			return added, err
		}
		body, err := s.SEC.Get(ctx, "https://data.sec.gov/submissions/CIK"+f.CIK+".json")
		if err != nil {
			s.Log.Warn("13f: submissions unavailable", "fund", f.Name, "err", err)
			continue
		}
		subs, err := ParseSubmissions(body)
		if err != nil {
			s.Log.Warn("13f: submissions unreadable", "fund", f.Name, "err", err)
			continue
		}
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
	}
	return added, nil
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

// resolveCUSIPs fills Symbol on each holding: from the cache first, then
// OpenFIGI for the rest, ten per request under its keyless rate limit.
func (s *Syncer) resolveCUSIPs(ctx context.Context, hs []Holding) error {
	var cusips []string
	for _, h := range hs {
		cusips = append(cusips, h.CUSIP)
	}
	known, err := s.Store.CUSIPSymbols(ctx, cusips)
	if err != nil {
		return err
	}
	var missing []string
	for _, c := range cusips {
		if _, ok := known[c]; !ok {
			missing = append(missing, c)
		}
	}
	found := map[string]string{}
	for i := 0; i < len(missing); i += 10 {
		batch := missing[i:min(i+10, len(missing))]
		got, err := s.figi(ctx, batch)
		if err != nil {
			return err
		}
		for c, sym := range got {
			found[c] = sym
			known[c] = sym
		}
		// Keyless OpenFIGI allows 25 requests a minute.
		select {
		case <-time.After(2600 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if len(found) > 0 {
		if err := s.Store.SaveCUSIPSymbols(ctx, found); err != nil {
			return err
		}
	}
	for i := range hs {
		hs[i].Symbol = known[hs[i].CUSIP]
	}
	return nil
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
