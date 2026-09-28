package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/scanner"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// The ingestion side: the source registry, the polling engine, and keeping
// per-symbol watch sources in step with the watchlist.

func startIngestion(ctx context.Context, store *postgres.DB, attention *news.Tracker, master *company.Master, marketScanner *scanner.Runner, secUserAgent string, log *slog.Logger) (*news.Engine, error) {
	registry, err := buildRegistry(secUserAgent)
	if err != nil {
		return nil, fmt.Errorf("build news source registry: %w", err)
	}
	engine := news.NewEngine(registry, store,
		news.WithEngineLogger(log),
		news.WithHeatTracker(attention))

	// Restore per-source schedules and conditional-request tokens so a restart
	// does not re-hammer publishers or refetch what we already hold.
	if err := engine.Prime(ctx); err != nil {
		log.Warn("could not restore source health; starting from cold", "err", err)
	}

	// The scheduler ticks far more often than any source's cadence. Ticking
	// slowly would round every interval up to the tick, which for a
	// thirty-second filings feed would double its latency.
	go engine.Run(ctx, 10*time.Second)

	// Follow the watchlist. Without this the catalog is broad-market only:
	// against a real archive, 15 of 4,308 collected items mentioned Reliance
	// and most were mutual funds sharing the name. A watchlist is a statement
	// about which companies matter, so each gets its own query.
	syncWatchlistSources(ctx, registry, store, master, marketScanner, log)
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				syncWatchlistSources(ctx, registry, store, master, marketScanner, log)
			}
		}
	}()

	// Cold attention entries are swept periodically. Not needed for
	// correctness — heat decays on read — but without it the map accumulates
	// every symbol ever mentioned.
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				attention.Sweep()
			}
		}
	}()

	lanes := map[news.Lane]int{}
	for _, s := range registry.Fetchable() {
		lanes[s.Lane()]++
	}
	log.Info("news ingestion started",
		"sources", len(registry.Fetchable()),
		"fast", lanes[news.LaneFast], "normal", lanes[news.LaneNormal], "slow", lanes[news.LaneSlow])
	return engine, nil
}

// buildRegistry assembles the static catalog plus the SEC filings tape, when
// SEC_USER_AGENT is configured. Left unset, SEC sources are omitted entirely
// rather than added and left to fail every request with a 403 -- SEC's
// fair-access policy requires a real contact string, not a generic one.
func buildRegistry(secUserAgent string) (*news.Registry, error) {
	sources := news.DefaultSources()
	if secUserAgent != "" {
		sources = append(sources, news.SECSources(secUserAgent)...)
		// The same declared contact unlocks the official feeds that demand
		// one but have nothing to do with SEC -- BLS, whose CPI and payroll
		// releases 403 without it. DefaultSources registers only the keyless
		// ones, so these are additive rather than duplicated.
		sources = append(sources, news.ContactGatedOfficialSources(secUserAgent)...)
	} else {
		slog.Warn("SEC_USER_AGENT is not set; the SEC filings tape (8-K/Form 4/13F) " +
			"and the BLS release feed are disabled")
	}
	return news.NewRegistry(sources...)
}

// startIngestion builds the source registry and starts the fetch engine.
//
// Collection is deliberately independent of everything above it: it needs no
// LLM, no market-data provider and no watchlist. If every other subsystem is
// misconfigured, the app still accumulates the filings and headlines that
// later phases will be built on — and an hour of collection that did not
// happen cannot be recovered later.
// syncWatchlistSources rebuilds the per-instrument sources from the watchlist.
//
// Polled rather than event-driven: a watchlist change is rare and a few
// minutes of latency on it is invisible, whereas wiring a notification through
// the storage layer for this alone would be machinery with one caller.
func syncWatchlistSources(
	ctx context.Context,
	registry *news.Registry,
	store *postgres.DB,
	master *company.Master,
	marketScanner *scanner.Runner,
	log *slog.Logger,
) {
	symbols, err := store.WatchedSymbols(ctx)
	if err != nil {
		log.Warn("could not read the watchlist for news sources", "err", err)
		return
	}

	// The two reasons to run a targeted search for a company are that an
	// operator asked to follow it, and that the market just did something
	// with it that nothing explains. They produce the same kind of source, so
	// they are merged here rather than kept in separate categories: one
	// writer for the category means the periodic watchlist sync cannot
	// silently drop the scanner's additions on its next pass.
	watched := make(map[string]bool, len(symbols))
	items := make([]news.Watched, 0, len(symbols))
	for _, sym := range symbols {
		watched[sym.String()] = true
		w := news.Watched{Ticker: sym.Ticker, Venue: sym.Exchange}
		// The registered name makes a far better query than a bare ticker,
		// and the master has it for every NSE-listed instrument. There is no
		// equivalent US text master yet (Phase 2), so a US company name is
		// left blank and the query falls back to the ticker plus market
		// words -- still correct, just less precise.
		if sym.Exchange != marketdata.ExchangeUS {
			if c, found := master.Lookup(sym.Ticker); found {
				w.Company = c.Name
			}
		}
		items = append(items, w)
	}

	var fromScanner int
	if marketScanner != nil {
		for _, canonical := range marketScanner.AttentionSymbols() {
			sym, err := marketdata.ParseSymbol(canonical)
			if err != nil {
				continue
			}
			if watched[sym.String()] {
				continue
			}
			w := news.Watched{Ticker: sym.Ticker, Venue: sym.Exchange, Attention: true}
			if sym.Exchange != marketdata.ExchangeUS {
				if c, found := master.Lookup(sym.Ticker); found {
					w.Company = c.Name
				}
			}
			items = append(items, w)
			fromScanner++
		}
	}

	added, removed, err := registry.Replace("watchlist", news.WatchlistSources(items))
	if err != nil {
		log.Warn("could not update watchlist news sources", "err", err)
		return
	}
	if len(added) > 0 || len(removed) > 0 {
		log.Info("watchlist news sources updated",
			"following", len(items), "from_scanner", fromScanner,
			"added", added, "removed", removed)
	}
}
