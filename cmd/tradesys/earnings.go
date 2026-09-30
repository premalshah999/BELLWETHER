package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/scanner"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// earningsBatch is how many symbols go to the sidecar per request; it walks
// them one at a time, so a batch is about a minute.
const earningsBatch = 100

// refreshEarnings stores past earnings announcements for the universe. perSymbol
// bounds how far back: 28 quarters is seven years, beyond the daily history
// the event study can price.
func refreshEarnings(ctx context.Context, client *scanner.Client, store *postgres.DB, symbols []marketdata.Symbol, perSymbol int, log *slog.Logger) (int, error) {
	if len(symbols) == 0 {
		return 0, fmt.Errorf("earnings: empty universe")
	}
	saved, failed := 0, 0
	for start := 0; start < len(symbols); start += earningsBatch {
		end := min(start+earningsBatch, len(symbols))
		rows, bad, err := client.EarningsHistory(ctx, symbols[start:end], perSymbol)
		if err != nil {
			if ctx.Err() != nil {
				return saved, ctx.Err()
			}
			log.Warn("earnings batch failed", "from", start, "to", end, "err", err)
			failed += end - start
			continue
		}
		out := make([]postgres.Earnings, len(rows))
		for i, r := range rows {
			out[i] = postgres.Earnings{Symbol: r.Symbol.String(), AnnouncedAt: r.AnnouncedAt,
				EPSEstimate: r.EPSEstimate, EPSActual: r.EPSActual, SurprisePct: r.SurprisePct}
		}
		n, err := store.SaveEarnings(ctx, out)
		if err != nil {
			log.Warn("could not save earnings batch", "from", start, "err", err)
			continue
		}
		saved += n
		failed += len(bad)
		log.Info("earnings batch saved", "through", end, "of", len(symbols), "rows", n)
	}
	log.Info("earnings refresh complete", "saved", saved, "failed_symbols", failed)
	return saved, nil
}

// runBackfillEarnings is the one-off form of the weekly job.
func runBackfillEarnings() error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if cfg.YFinanceURL == "" {
		return fmt.Errorf("earnings: YFINANCE_URL is not configured")
	}
	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()
	master, err := company.Load()
	if err != nil {
		return fmt.Errorf("earnings: load listings: %w", err)
	}
	client := &scanner.Client{BaseURL: cfg.YFinanceURL, HTTP: &http.Client{Timeout: 10 * time.Minute}}
	universe := buildScanUniverse(master)
	_, err = refreshEarnings(ctx, client, store, universe.Symbols(), 28, log)
	return err
}
