package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"time"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/marketdata/yfin"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// forecastBars is how many recent daily bars the engine reads per stock
// from the candles table; deeper history comes from daily_history.
const forecastBars = 1500

// forecastMemory caps the Go heap while the engine runs, so its garbage is
// collected before it can push the process past the container's limit. Ten
// years of 1,500 stocks ran in the same five minutes under this cap as
// under none, at a peak of 440 MB resident.
const forecastMemory = 420 << 20

// deepSeries is a symbol's deep history joined to its recent candles.
func deepSeries(ctx context.Context, store *postgres.DB, sym marketdata.Symbol) ([]marketdata.Candle, error) {
	recent, err := store.LoadCandles(ctx, sym, marketdata.Interval1d, forecastBars)
	if err != nil {
		return nil, err
	}
	old, err := store.LoadDailyHistory(ctx, sym.String())
	if err != nil {
		return nil, err
	}
	return joinBars(old, recent.Candles), nil
}

// joinBars appends the newer series to the older, dropping any older bars
// on or after the newer series' first.
func joinBars(older, newer []marketdata.Candle) []marketdata.Candle {
	if len(newer) == 0 {
		return older
	}
	cut := len(older)
	for cut > 0 && !older[cut-1].Time.Before(newer[0].Time) {
		cut--
	}
	return append(append([]marketdata.Candle(nil), older[:cut]...), newer...)
}

// runForecast loads the universe onto one calendar, validates the engine
// walk-forward and stores today's forecasts.
func runForecast(ctx context.Context, cfg *config.Config, store *postgres.DB, master *company.Master, log *slog.Logger) error {
	started := time.Now()
	gspc, err := deepSeries(ctx, store, marketdata.MustParseSymbol("GSPC.INDEX"))
	if err != nil || len(gspc) < 600 {
		return fmt.Errorf("forecast: S&P 500 history: %d bars, %v", len(gspc), err)
	}
	// VIX: the stored deep history, then the latest read fresh from the
	// price service. Without it the volatility models fall back on the
	// market's own realised variance.
	vix, _ := store.LoadDailyHistory(ctx, "VIX.INDEX")
	if cfg.YFinanceURL != "" {
		bars, err := yfin.New(cfg.YFinanceURL).Candles(ctx, marketdata.MustParseSymbol("VIX.INDEX"), marketdata.Interval1d, 300)
		if err != nil {
			log.Warn("forecast: latest VIX unavailable", "err", err)
		}
		vix = joinBars(vix, bars.Candles)
	}
	panel := forecast.NewPanel(gspc, vix)

	earnRows, err := store.EarningsHistory(ctx)
	if err != nil {
		return err
	}
	earns := map[string][]forecast.Earning{}
	for _, e := range earnRows {
		s := math.NaN()
		if e.SurprisePct != nil {
			s = *e.SurprisePct
		}
		earns[e.Symbol] = append(earns[e.Symbol], forecast.Earning{At: e.AnnouncedAt, SurprisePct: s})
	}
	insiders, err := store.InsiderFlows(ctx)
	if err != nil {
		return err
	}
	universe := buildScanUniverse(master)
	for _, sym := range universe.Symbols() {
		bars, err := deepSeries(ctx, store, sym)
		if err != nil || len(bars) < 300 {
			continue
		}
		sector, _ := master.Sector(sym.String())
		panel.AddStock(sym.String(), sector, bars, earns[sym.String()], insiders[sym.String()])
	}

	// Scheduled reports the history does not hold yet. The calendar stores
	// a date; it is read as that date in New York.
	upcoming := map[string]time.Time{}
	if cats, err := store.UpcomingCatalysts(ctx, 45*24*time.Hour, nil, 1000); err == nil {
		for _, c := range cats {
			if c.EarningsDate != nil {
				d := c.EarningsDate.UTC()
				upcoming[c.CanonicalSymbol] = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, marketdata.Market)
			}
		}
	}
	prev := debug.SetMemoryLimit(forecastMemory)
	defer debug.SetMemoryLimit(prev)
	rep, preds, err := forecast.Run(panel, forecast.Config{Now: time.Now(), Upcoming: upcoming,
		Log: func(msg string, args ...any) { log.Info(msg, args...) }})
	if err != nil {
		return err
	}
	if _, err := store.SaveForecast(ctx, rep, preds); err != nil {
		return err
	}
	a, d := rep.Alpha[5], rep.Dist[5]
	log.Info("forecast stored", "as_of", rep.AsOf.Format("2006-01-02"), "stocks", len(preds),
		"ic_5d", a.IC, "t", a.T, "crps_skill_pct", d.SkillNormal, "coverage_80", d.CalCoverage[1],
		"elapsed", time.Since(started).Round(time.Second))
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
	return runForecast(ctx, cfg, store, master, log)
}
