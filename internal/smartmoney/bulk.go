package smartmoney

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SEC publishes every Form 3, 4 and 5 as a quarterly structured dataset. It
// is how insider history reaches back years without reading each filing.
const insiderDatasetsPage = "https://www.sec.gov/data-research/sec-markets-data/insider-transactions-data-sets"

var insiderZipRe = regexp.MustCompile(`href="(/files/[^"]*/(\d{4}q\d)_form345\.zip)"`)

// InsiderDatasetURLs lists the quarterly datasets on SEC's index page,
// newest first.
func InsiderDatasetURLs(page []byte) []string {
	type q struct{ url, key string }
	var qs []q
	seen := map[string]bool{}
	for _, m := range insiderZipRe.FindAllSubmatch(page, -1) {
		key := string(m[2])
		if seen[key] {
			continue
		}
		seen[key] = true
		qs = append(qs, q{"https://www.sec.gov" + string(m[1]), key})
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].key > qs[j].key })
	out := make([]string, len(qs))
	for i, x := range qs {
		out[i] = x.url
	}
	return out
}

// tsv streams one file of a dataset as maps keyed by column name.
func tsv(z *zip.Reader, name string, fn func(map[string]string)) error {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		r := csv.NewReader(rc)
		r.Comma, r.LazyQuotes, r.FieldsPerRecord = '\t', true, -1
		r.ReuseRecord = true
		header, err := r.Read()
		if err != nil {
			return err
		}
		header = append([]string(nil), header...)
		for {
			rec, err := r.Read()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				continue
			}
			row := make(map[string]string, len(header))
			for i, h := range header {
				if i < len(rec) {
					row[h] = rec[i]
				}
			}
			fn(row)
		}
	}
	return fmt.Errorf("smartmoney: dataset has no %s", name)
}

func datasetDate(s string) (time.Time, bool) {
	t, err := time.Parse("02-Jan-2006", strings.TrimSpace(s))
	return t, err == nil
}

// ParseInsiderDataset reads one quarterly dataset and returns its open-market
// purchases and sales (codes P and S) on Form 4. symbolOf maps the issuer's
// reported ticker to a canonical symbol, or reports it unknown; trades in
// unknown issuers are dropped.
func ParseInsiderDataset(data []byte, symbolOf func(string) (string, bool)) ([]Trade, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("smartmoney: open dataset: %w", err)
	}
	type sub struct {
		symbol, cik, name string
		filed             time.Time
		plan              bool
	}
	subs := map[string]sub{}
	err = tsv(z, "SUBMISSION.tsv", func(r map[string]string) {
		if r["DOCUMENT_TYPE"] != "4" {
			return
		}
		sym, ok := symbolOf(strings.ToUpper(strings.TrimSpace(r["ISSUERTRADINGSYMBOL"])))
		filed, okDate := datasetDate(r["FILING_DATE"])
		if !ok || !okDate {
			return
		}
		subs[r["ACCESSION_NUMBER"]] = sub{sym, strings.TrimLeft(r["ISSUERCIK"], "0"), strings.TrimSpace(r["ISSUERNAME"]), filed, r["AFF10B5ONE"] == "true"}
	})
	if err != nil {
		return nil, err
	}

	type owner struct {
		cik, name, title          string
		director, officer, tenPct bool
	}
	owners := map[string]owner{}
	err = tsv(z, "REPORTINGOWNER.tsv", func(r map[string]string) {
		acc := r["ACCESSION_NUMBER"]
		if _, ok := subs[acc]; !ok {
			return
		}
		if _, dup := owners[acc]; dup {
			return // a joint filing: the first owner speaks for it, as in ParseForm4
		}
		rel := strings.ToLower(r["RPTOWNER_RELATIONSHIP"])
		owners[acc] = owner{
			cik: strings.TrimLeft(r["RPTOWNERCIK"], "0"), name: strings.TrimSpace(r["RPTOWNERNAME"]),
			title:    strings.TrimSpace(r["RPTOWNER_TITLE"]),
			director: strings.Contains(rel, "director"), officer: strings.Contains(rel, "officer"),
			tenPct: strings.Contains(rel, "tenpercent"),
		}
	})
	if err != nil {
		return nil, err
	}

	lines := map[string]int{}
	var out []Trade
	err = tsv(z, "NONDERIV_TRANS.tsv", func(r map[string]string) {
		acc := r["ACCESSION_NUMBER"]
		s, ok := subs[acc]
		if !ok {
			return
		}
		line := lines[acc]
		lines[acc]++
		code := strings.ToUpper(strings.TrimSpace(r["TRANS_CODE"]))
		if code != "P" && code != "S" {
			return
		}
		date, ok := datasetDate(r["TRANS_DATE"])
		shares, err := strconv.ParseFloat(r["TRANS_SHARES"], 64)
		if !ok || err != nil || shares <= 0 {
			return
		}
		o := owners[acc]
		t := Trade{
			Accession: acc, Line: line, Symbol: s.symbol, IssuerCIK: s.cik, IssuerName: s.name,
			OwnerCIK: o.cik, OwnerName: o.name, IsDirector: o.director, IsOfficer: o.officer, IsTenPct: o.tenPct,
			OfficerTitle: o.title, Security: strings.TrimSpace(r["SECURITY_TITLE"]), TxDate: date, Code: code,
			Acquired: r["TRANS_ACQUIRED_DISP_CD"] == "A", Shares: shares,
			Direct: r["DIRECT_INDIRECT_OWNERSHIP"] != "I", Plan105b1: s.plan, FiledAt: s.filed,
		}
		if p, err := strconv.ParseFloat(r["TRANS_PRICEPERSHARE"], 64); err == nil && p > 0 {
			v := p * shares
			t.Price, t.Value = &p, &v
		}
		if a, err := strconv.ParseFloat(r["SHRS_OWND_FOLWNG_TRANS"], 64); err == nil {
			t.OwnedAfter = &a
		}
		out = append(out, t)
	})
	return out, err
}

// BackfillInsiders loads the newest `quarters` datasets. Filings already read
// one by one are skipped, so the two sources never double-count a trade.
func (s *Syncer) BackfillInsiders(ctx context.Context, quarters int, symbolOf func(string) (string, bool)) (int, error) {
	page, err := s.SEC.Get(ctx, insiderDatasetsPage)
	if err != nil {
		return 0, fmt.Errorf("insider datasets index: %w", err)
	}
	urls := InsiderDatasetURLs(page)
	if len(urls) == 0 {
		return 0, fmt.Errorf("insider datasets index lists no quarters")
	}
	stored := 0
	for _, u := range urls[:min(quarters, len(urls))] {
		data, err := s.SEC.Get(ctx, u)
		if err != nil {
			s.Log.Warn("insider dataset not downloaded", "url", u, "err", err)
			continue
		}
		trades, err := ParseInsiderDataset(data, symbolOf)
		if err != nil {
			s.Log.Warn("insider dataset unreadable", "url", u, "err", err)
			continue
		}
		accs := map[string]bool{}
		for _, t := range trades {
			accs[t.Accession] = true
		}
		list := make([]string, 0, len(accs))
		for a := range accs {
			list = append(list, a)
		}
		seen, err := s.Store.InsiderFilingsSeen(ctx, list)
		if err != nil {
			return stored, err
		}
		fresh := trades[:0]
		for _, t := range trades {
			if !seen[t.Accession] {
				fresh = append(fresh, t)
			}
		}
		for i := 0; i < len(fresh); i += 2000 {
			if err := s.Store.SaveInsiderTrades(ctx, fresh[i:min(i+2000, len(fresh))]); err != nil {
				return stored, err
			}
		}
		var newAccs []string
		for _, a := range list {
			if !seen[a] {
				newAccs = append(newAccs, a)
			}
		}
		if err := s.Store.MarkInsiderFilings(ctx, newAccs); err != nil {
			return stored, err
		}
		stored += len(fresh)
		s.Log.Info("insider dataset loaded", "url", u, "trades", len(fresh), "skipped_filings", len(list)-len(newAccs))
	}
	return stored, nil
}
