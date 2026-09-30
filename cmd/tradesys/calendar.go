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

// calendarBatch is how many symbols go to the sidecar per request. It walks
// them one at a time (about twelve a second), so a batch is roughly twenty
// seconds: small enough that a failure costs one batch.
const calendarBatch = 250

// refreshCalendar reloads the forward calendar for the whole universe.
//
// Failures are per batch, not per run: a rate limit part way through should
// leave the batches already written in place. The calendar is upserted, so a
// partial refresh is yesterday's dates for the symbols not reached and
// today's for the ones that were, which is strictly better than nothing.
func refreshCalendar(
	ctx context.Context,
	client *scanner.Client,
	store *postgres.DB,
	symbols []marketdata.Symbol,
	log *slog.Logger,
) (int, error) {
	if len(symbols) == 0 {
		return 0, fmt.Errorf("calendar: empty universe")
	}

	saved, failed := 0, 0
	for start := 0; start < len(symbols); start += calendarBatch {
		end := min(start+calendarBatch, len(symbols))

		res, err := client.Calendar(ctx, symbols[start:end])
		if err != nil {
			// Cancellation is the operator or a shutdown, not a fault worth
			// carrying on through.
			if ctx.Err() != nil {
				return saved, ctx.Err()
			}
			log.Warn("calendar batch failed", "from", start, "to", end, "err", err)
			failed += end - start
			continue
		}

		entries := make([]postgres.CalendarEntry, 0, len(res.Entries))
		for _, e := range res.Entries {
			entries = append(entries, postgres.CalendarEntry{
				Symbol:         e.Symbol,
				EarningsDate:   e.EarningsDate,
				ExDividendDate: e.ExDividendDate,
				DividendDate:   e.DividendDate,
				EPSLow:         e.EPSLow,
				EPSHigh:        e.EPSHigh,
				EPSAverage:     e.EPSAverage,
			})
		}
		n, err := store.SaveCalendar(ctx, entries)
		if err != nil {
			log.Warn("could not save calendar batch", "from", start, "err", err)
			continue
		}
		saved += n
		failed += len(res.Failed)
	}

	// Rows whose every date has passed are nobody's upcoming event. Pruned
	// after writing so a symbol that just moved to a new quarter keeps its
	// row rather than being dropped and re-added.
	if pruned, err := store.PruneCalendar(ctx); err != nil {
		log.Warn("could not prune calendar", "err", err)
	} else if pruned > 0 {
		log.Info("calendar pruned", "expired", pruned)
	}

	log.Info("calendar refreshed", "saved", saved, "failed", failed, "universe", len(symbols))
	return saved, nil
}

// runRefreshCalendar populates the forward calendar now rather than waiting
// for the schedule -- the same reason -sync-congress exists, and with the
// same property: it is a write path reachable only from shell access on the
// host, not from an HTTP route.
func runRefreshCalendar() error {
	ctx := context.Background()
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if cfg.YFinanceURL == "" {
		return fmt.Errorf("calendar: YFINANCE_URL is not configured")
	}

	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()

	master, err := company.Load()
	if err != nil {
		return fmt.Errorf("calendar: load listings: %w", err)
	}
	universe := buildScanUniverse(master)

	client := &scanner.Client{
		BaseURL: cfg.YFinanceURL,
		HTTP:    &http.Client{Timeout: 8 * time.Minute},
	}

	n, err := refreshCalendar(ctx, client, store, universe.Symbols(), log)
	if err != nil {
		return err
	}
	log.Info("calendar refresh complete", "saved", n)
	return nil
}
