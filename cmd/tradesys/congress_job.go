package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/congress"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// runSyncCongress is the -sync-congress one-off command: load config, open
// the store, run one sync pass, exit. Deliberately reuses the same
// syncCongressFilings the daily cron calls rather than a separate path, so
// a manual run and a scheduled run can never behave differently.
func runSyncCongress() error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()

	userAgent := cfg.SECUserAgent
	if userAgent == "" {
		userAgent = "TradeSys/1.0 (github.com/tradesys/dashboard)"
	}
	client := &http.Client{Timeout: 2 * time.Minute}

	n, err := syncCongressFilings(ctx, store, client, userAgent, log)
	if err != nil {
		return err
	}
	log.Info("congress filings synced", "new", n)
	return nil
}

// pdfFetchPause is a small courtesy delay between PDF downloads from the
// House Clerk's site -- unlike SEC's hosts, it publishes no documented rate
// limit, and the first run of this job can find several hundred PTRs
// already outstanding for the year. A fixed pause keeps that initial
// backfill from looking like a burst against a government host that has
// given us no reason to think it can take one.
const pdfFetchPause = 300 * time.Millisecond

// syncCongressFilings fetches this year's House Clerk disclosure index,
// finds Periodic Transaction Reports not already stored, and reads each new
// one's PDF for the tickers and earliest transaction date it names.
//
// Only PTRs are fetched -- the index's other filing-type codes (annual,
// candidate, extension, and so on) carry no trade data, so downloading
// their PDFs would spend real requests against a government host for
// nothing this feature surfaces. See internal/congress's package doc for
// why the extraction itself reads at document level, not row level.
func syncCongressFilings(ctx context.Context, store *postgres.DB, client *http.Client, userAgent string, log *slog.Logger) (int, error) {
	year := time.Now().Year()
	filings, err := congress.FetchHouseIndex(ctx, client, userAgent, year)
	if err != nil {
		return 0, fmt.Errorf("fetch house index: %w", err)
	}

	existing, err := store.ExistingCongressFilingDocIDs(ctx, year)
	if err != nil {
		return 0, fmt.Errorf("load existing doc ids: %w", err)
	}

	var saved int
	for _, f := range filings {
		if !f.IsPTR() || existing[f.DocID] {
			continue
		}

		pdf, err := fetchHouseDoc(ctx, client, userAgent, f.DocURL())
		if err != nil {
			log.Warn("congress: could not fetch PTR pdf", "doc_id", f.DocID, "err", err)
			continue
		}
		tickers, earliest, err := congress.ExtractPTR(ctx, pdf)
		if err != nil {
			log.Warn("congress: could not extract PTR", "doc_id", f.DocID, "err", err)
			continue
		}

		symbols, unresolved := resolveCongressTickers(tickers)
		stored := congress.StoredFiling{
			Filing:                  f,
			Chamber:                 "house",
			Symbols:                 symbols,
			UnresolvedTickers:       unresolved,
			EarliestTransactionDate: earliest,
			DiscoveredAt:            time.Now().UTC(),
		}
		if !earliest.IsZero() {
			delay := int(f.FilingDate.Sub(earliest).Hours() / 24)
			stored.DisclosureDelayDays = &delay
		}

		if err := store.SaveCongressFiling(ctx, stored); err != nil {
			log.Warn("congress: could not save filing", "doc_id", f.DocID, "err", err)
			continue
		}
		saved++

		select {
		case <-ctx.Done():
			return saved, ctx.Err()
		case <-time.After(pdfFetchPause):
		}
	}
	return saved, nil
}

// resolveCongressTickers keeps the tickers that name a real US listing. The
// rest stay visible as unresolved, so a reviewer sees what the extractor
// found and could not place.
func resolveCongressTickers(tickers []string) (symbols, unresolved []string) {
	for _, t := range tickers {
		if company.IsUSTicker(t) {
			symbols = append(symbols, strings.ToUpper(t))
		} else {
			unresolved = append(unresolved, t)
		}
	}
	return symbols, unresolved
}

func fetchHouseDoc(ctx context.Context, client *http.Client, userAgent, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}
