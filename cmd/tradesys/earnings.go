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
	"github.com/tradesys/dashboard/internal/marketdata/yfin"
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

// runBackfillHistory stores five years of daily bars for every universe
// member, so the event study and backtests can reach back that far. The
// scheduled scan keeps the latest year current afterwards.
func runBackfillHistory() error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if cfg.YFinanceURL == "" {
		return fmt.Errorf("history: YFINANCE_URL is not configured")
	}
	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()
	master, err := company.Load()
	if err != nil {
		return err
	}
	universe := buildScanUniverse(master)
	symbols := append(universe.Symbols(), marketdata.MustParseSymbol("SPY"))
	client := &scanner.Client{BaseURL: cfg.YFinanceURL, HTTP: &http.Client{Timeout: 10 * time.Minute},
		Adjust: cfg.YFinanceAdjust, Period: "5y"}
	saved := 0
	for start := 0; start < len(symbols); start += 100 {
		batch := symbols[start:min(start+100, len(symbols))]
		res, err := client.Scan(ctx, batch, 1)
		if err != nil {
			log.Warn("history batch failed", "from", start, "err", err)
			continue
		}
		for sym, candles := range res.Series {
			if err := store.SaveCandles(ctx, sym, marketdata.Interval1d, "yfinance", marketdata.Bars{Candles: candles}, len(candles)); err != nil {
				log.Warn("history not saved", "symbol", sym.String(), "err", err)
				continue
			}
			saved++
		}
		log.Info("history batch saved", "through", start+len(batch), "of", len(symbols))
	}
	log.Info("history backfill complete", "symbols", saved)
	return nil
}

// runBackfillDeepHistory stores ten years of daily bars, older than what the
// candles table holds, for the universe, the S&P 500 and VIX: what the
// forecast engine needs to be tested across more than one market.
func runBackfillDeepHistory() error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if cfg.YFinanceURL == "" {
		return fmt.Errorf("history: YFINANCE_URL is not configured")
	}
	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()
	master, err := company.Load()
	if err != nil {
		return err
	}
	prices := yfin.New(cfg.YFinanceURL, yfin.WithDividendAdjustment(cfg.YFinanceAdjust))
	for _, idx := range []string{"GSPC.INDEX", "VIX.INDEX"} {
		bars, err := prices.Candles(ctx, marketdata.MustParseSymbol(idx), marketdata.Interval1d, 2600)
		if err != nil {
			log.Warn("index history failed", "symbol", idx, "err", err)
			continue
		}
		n, err := store.SaveDailyHistory(ctx, idx, bars.Candles)
		log.Info("index history saved", "symbol", idx, "bars", n, "err", err)
	}
	universe := buildScanUniverse(master)
	symbols := universe.Symbols()
	client := &scanner.Client{BaseURL: cfg.YFinanceURL, HTTP: &http.Client{Timeout: 15 * time.Minute},
		Adjust: cfg.YFinanceAdjust, Period: "10y"}
	saved := 0
	for start := 0; start < len(symbols); start += 50 {
		batch := symbols[start:min(start+50, len(symbols))]
		res, err := client.Scan(ctx, batch, 1)
		if err != nil {
			log.Warn("deep history batch failed", "from", start, "err", err)
			continue
		}
		for sym, candles := range res.Series {
			n, err := store.SaveDailyHistory(ctx, sym.String(), candles)
			if err != nil {
				log.Warn("deep history not saved", "symbol", sym.String(), "err", err)
				continue
			}
			saved += n
		}
		log.Info("deep history batch saved", "through", start+len(batch), "of", len(symbols), "bars", saved)
	}
	log.Info("deep history backfill complete", "bars", saved)
	return nil
}
