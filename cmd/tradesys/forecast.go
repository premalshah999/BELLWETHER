package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// forecastStep keeps every fifth session for training: consecutive days'
// five-day labels overlap, so the rest add rows, not information.
const forecastStep = 5

// runForecast builds the model's rows for the universe, validates it walk-
// forward and stores today's ranking.
func runForecast(ctx context.Context, store *postgres.DB, master *company.Master, log *slog.Logger) error {
	started := time.Now()
	earnRows, err := store.EarningsHistory(ctx)
	if err != nil {
		return err
	}
	earns := map[string][]forecast.Earning{}
	for _, e := range earnRows {
		earns[e.Symbol] = append(earns[e.Symbol], forecast.Earning{At: e.AnnouncedAt, SurprisePct: *e.SurprisePct})
	}
	insiders, err := store.InsiderFlows(ctx)
	if err != nil {
		return err
	}
	universe := buildScanUniverse(master)
	var rows []forecast.Row
	for _, sym := range universe.Symbols() {
		series, err := store.LoadCandles(ctx, sym, marketdata.Interval1d, 1400)
		if err != nil || len(series.Candles) < 300 {
			continue
		}
		rows = append(rows, forecast.Features(sym.String(), series.Candles, earns[sym.String()], insiders[sym.String()], forecastStep)...)
	}
	rep, preds, err := forecast.Run(rows, time.Now())
	if err != nil {
		return err
	}
	if _, err := store.SaveForecast(ctx, rep, preds); err != nil {
		return err
	}
	log.Info("forecast stored", "as_of", rep.AsOf.Format("2006-01-02"), "stocks", len(preds), "rank_ic", rep.Overall.RankIC,
		"t", rep.Overall.T, "rows", len(rows), "elapsed", time.Since(started).Round(time.Second))
	return nil
}

// runForecastCmd is the -forecast one-off.
func runForecastCmd() error {
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
	master, err := company.Load()
	if err != nil {
		return fmt.Errorf("forecast: load listings: %w", err)
	}
	return runForecast(ctx, store, master, log)
}
