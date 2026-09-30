package main

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/fundamentals"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/scanner"
	"github.com/tradesys/dashboard/internal/smartmoney"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// Scheduled work: the cron jobs that keep the derived layers current.

// startAISchedules registers the periodic AI and news jobs.
//
// Every job here is optional by design. News collection needs no LLM, and
// outlook scoring needs no LLM either — which is deliberate, because the
// moment an operator most wants to know how much to trust the model is when it
// has stopped answering.
func startAISchedules(
	ctx context.Context,
	cfg *config.Config,
	log *slog.Logger,
	svc *ai.Service,
	poller *news.Poller,
	processor *events.Processor,
	store *postgres.DB,
	marketScanner *scanner.Runner,
	fundamentalsRunner *fundamentals.Runner,
	newsArchive *postgres.Archive,
	master *company.Master,
	sm *smartmoney.Syncer,
) *cron.Cron {
	c := cron.New(cron.WithLocation(marketdata.Market), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger), cron.Recover(cron.DefaultLogger)))

	// Fifteen minutes suits every job here except one, so the deadline is a
	// parameter rather than a constant. A fundamentals pass over 750 companies
	// is several upstream requests each and runs to about an hour; under the
	// shared ceiling it would be cancelled two thirds of the way through,
	// every week, having stored a partial universe and logged nothing that
	// looked like a failure.
	addWithin := func(name, spec string, limit time.Duration, job func(context.Context)) {
		if _, err := c.AddFunc(spec, func() {
			runCtx, cancel := context.WithTimeout(ctx, limit)
			defer cancel()
			job(runCtx)
		}); err != nil {
			log.Error("could not schedule an AI job", "job", name, "cron", spec, "err", err)
			return
		}
		log.Info("scheduled", "job", name, "cron", spec)
	}

	add := func(name, spec string, job func(context.Context)) {
		addWithin(name, spec, 15*time.Minute, job)
	}

	// Watchlist collection every ten minutes; this needs no AI. The multi-source
	// ingestion engine polls far more often than this on its own per-source
	// cadence; this is the watchlist-driven sweep, which is a different job.
	add("news poll", "*/10 * * * *", func(runCtx context.Context) {
		if _, err := poller.PollAll(runCtx); err != nil {
			log.Warn("scheduled news poll failed", "err", err)
		}
	})

	// The market scan. The close scan is the one to trust: it
	// sees the full day's volume, where an intraday scan compares a partial
	// session with complete days and under-reports.
	if marketScanner != nil {
		scan := func(runCtx context.Context) {
			if _, err := marketScanner.Run(runCtx); err != nil {
				log.Warn("market scan failed", "err", err)
			}
		}
		add("market scan (us close)", "15 16 * * 1-5", scan)
		add("market scan (us intraday)", "15,45 10-15 * * 1-5", scan)
		add("market scan (us open)", "45 9 * * 1-5", scan)

		// The forward calendar, once a day before the open: earnings dates
		// move rarely. It gets a long window because the refresh walks the
		// universe one name at a time, which keeps the sidecar's memory flat.
		addWithin("forward calendar", "20 7 * * 1-5",
			90*time.Minute, func(runCtx context.Context) {
				if _, err := refreshCalendar(runCtx, marketScanner.Client, store, marketScanner.Universe(), log); err != nil {
					log.Warn("calendar refresh failed", "err", err)
				}
			})

		// The forecast after the close scan has stored the day's bars.
		if master != nil {
			addWithin("forecast", "40 16 * * 1-5", 30*time.Minute, func(runCtx context.Context) {
				if err := runForecast(runCtx, store, master, log); err != nil {
					log.Warn("forecast failed", "err", err)
				}
			})
		}

		// Earnings history weekly: the last two quarters are enough to pick
		// up new results, since the full history was backfilled once.
		addWithin("earnings history", "0 5 * * 0", 3*time.Hour, func(runCtx context.Context) {
			if _, err := refreshEarnings(runCtx, marketScanner.Client, store, marketScanner.Universe(), 4, log); err != nil {
				log.Warn("earnings refresh failed", "err", err)
			}
		})
	}

	// Fundamentals overnight, when nothing else is competing for the sidecar
	// and a slow pass costs nobody anything. Saturday because results are
	// published on weekdays and a weekend pass picks up everything filed
	// during the week in one go, rather than re-reading four hundred
	// unchanged balance sheets every night.
	if fundamentalsRunner != nil {
		addWithin("fundamentals refresh", "0 2 * * 6", 3*time.Hour, func(runCtx context.Context) {
			if _, _, err := fundamentalsRunner.Refresh(runCtx); err != nil {
				log.Warn("fundamentals refresh failed", "err", err)
			}
		})
	}

	// Event processing: often, because it is cheap and the value of an event
	// decays fast. Nothing here calls a model, so this cadence costs nothing
	// but a little CPU and keeps the feed close to live.
	add("event processing", "*/2 * * * *", func(runCtx context.Context) {
		res, err := processor.ProcessBatch(runCtx, 800)
		if err != nil {
			log.Warn("event processing failed", "err", err)
			return
		}
		if res.Items > 0 {
			log.Info("events processed",
				"items", res.Items, "created", res.Created, "merged", res.Merged,
				"filtered", res.Filtered, "took", res.Duration.Round(time.Millisecond))
		}
	})

	// Move aged news to the archive in the quiet window after the close. An
	// hour rather than the shared fifteen minutes: the first run on an
	// existing database has months to move, later runs a day's worth.
	if newsArchive != nil {
		addWithin("news rollover", "30 2 * * *", time.Hour,
			func(runCtx context.Context) {
				moved, err := newsArchive.Rollover(runCtx, time.Now())
				if err != nil {
					// Partial progress is kept: the rollover copies before it
					// deletes and every insert is idempotent, so an
					// interrupted run is finished by the next one.
					log.Warn("news rollover incomplete", "moved", moved, "err", err)
					return
				}
				if moved > 0 {
					log.Info("news rolled over", "events", moved)
				}
			})
	}

	add("prune orphan items", "15 2 * * *", func(runCtx context.Context) {
		cutoff := time.Now().AddDate(0, 0, -orphanRetentionDays)
		n, err := store.PruneOrphanRawItems(runCtx, cutoff)
		if err != nil {
			log.Warn("prune failed", "err", err)
			return
		}
		log.Info("pruned orphan raw items", "deleted", n, "older_than", cutoff.Format(time.DateOnly))
	})

	// Compaction weekly, on a Sunday. VACUUM holds a write lock for its
	// duration, so it runs when nothing is expected to be writing.
	add("compact database", "45 2 * * 0", func(runCtx context.Context) {
		before, _ := store.Stats(runCtx)
		if err := store.Compact(runCtx); err != nil {
			log.Warn("compaction failed", "err", err)
			return
		}
		after, _ := store.Stats(runCtx)
		log.Info("database compacted",
			"before_mb", before.SizeBytes/(1<<20), "after_mb", after.SizeBytes/(1<<20))
	})

	// Congressional trade disclosures, once a day: the House Clerk posts on no
	// fixed schedule. Two hours, because the first run of a fresh deploy finds
	// several hundred PDFs outstanding and should finish rather than repeat.
	congressUA := cfg.SECUserAgent
	if congressUA == "" {
		// The House Clerk is not the SEC and enforces no fair-access
		// policy, so this job is not gated on SEC_USER_AGENT the way the
		// SEC filings tape is -- it still identifies itself rather than
		// riding on Go's default UA, because that costs nothing.
		congressUA = "TradeSys/1.0 (github.com/tradesys/dashboard)"
	}
	congressClient := &http.Client{Timeout: 2 * time.Minute}
	addWithin("congress filings sync", "30 6 * * *", 2*time.Hour, func(runCtx context.Context) {
		n, err := syncCongressFilings(runCtx, store, congressClient, congressUA, log)
		if err != nil {
			log.Warn("congress filings sync failed", "err", err)
			return
		}
		if n > 0 {
			log.Info("congress filings synced", "new", n)
		}
	})

	// Who's buying. Form 4s are due two business days after a trade and SEC
	// publishes the daily index through the evening, so insider trades are
	// read every half hour on weekdays (today and the previous three days,
	// which also retries anything a transient error missed). 13Fs change
	// quarterly, so the fund pass runs once a day.
	if sm != nil {
		addWithin("insider trades sync", "*/30 6-23 * * 1-5", 25*time.Minute, func(runCtx context.Context) {
			if n, err := sm.SyncInsiders(runCtx, 4); err != nil {
				log.Warn("insider trades sync failed", "err", err)
			} else if n > 0 {
				log.Info("insider trades synced", "trades", n)
			}
		})
		// SEC publishes each quarter's insider dataset a few weeks after it
		// ends; the newest one is reloaded monthly, which skips filings
		// already read one by one.
		if master != nil {
			addWithin("insider dataset", "0 4 3 * *", 2*time.Hour, func(runCtx context.Context) {
				if n, err := sm.BackfillInsiders(runCtx, 1, insiderSymbolOf(master)); err != nil {
					log.Warn("insider dataset load failed", "err", err)
				} else {
					log.Info("insider dataset loaded", "trades", n)
				}
			})
		}
		// Tickers for 13F holdings no one has resolved yet, largest first.
		addWithin("cusip resolver", "10 * * * *", 25*time.Minute, func(runCtx context.Context) {
			if n, err := sm.ResolvePendingCUSIPs(runCtx, 20*time.Minute); err != nil {
				log.Warn("cusip resolution failed", "err", err)
			} else if n > 0 {
				log.Info("cusips resolved", "count", n)
			}
		})
		addWithin("fund holdings sync", "20 7 * * *", time.Hour, func(runCtx context.Context) {
			if n, err := sm.SyncFunds(runCtx); err != nil {
				log.Warn("fund holdings sync failed", "err", err)
			} else if n > 0 {
				log.Info("fund holdings synced", "filings", n)
			}
		})
	}

	// Outlook scoring: no LLM call, so it keeps the calibration record
	// current even when the token budget is spent.
	add("outlook scoring", "45 5 * * *", func(runCtx context.Context) {
		if _, err := svc.ResolveDueOutlooks(runCtx); err != nil {
			log.Warn("scheduled outlook scoring failed", "err", err)
		}
	})

	if cfg.LLMConfigured() {
		// Outlooks are the forecasts the AI track record scores. A few
		// watchlist names each morning, those longest without one, so every
		// name is forecast about once a horizon and the record keeps growing.
		add("outlooks", "45 8 * * 1-5", func(runCtx context.Context) {
			if n := generateOutlooks(runCtx, svc, store, 4, log); n > 0 {
				log.Info("outlooks generated", "count", n)
			}
		})

		// The morning brief, an hour before the open, in New York time: a
		// brief is about a trading day, not a wall clock.
		add("morning brief", "30 8 * * 1-5", func(runCtx context.Context) {
			if _, err := svc.GenerateMorningBrief(runCtx); err != nil {
				log.Warn("scheduled morning brief failed", "err", err)
			}
		})

		// Briefs for the important items, shortly after classification has
		// decided which those are.
		//
		// Capped per run so a backlog drains steadily rather than stampeding
		// the provider, and windowed so it never reaches back into the
		// archive. Anything older, or below the threshold, is still briefed
		// on demand from the feed.
		add("event briefs", "50 * * * *", func(runCtx context.Context) {
			// Sixty: a brief takes about eleven seconds and the job has
			// fifteen minutes, so a larger cap would be cancelled partway.
			n, err := svc.BriefEvents(runCtx, store, 60)
			if err != nil {
				log.Warn("scheduled event briefing failed", "err", err)
				return
			}
			if n > 0 {
				log.Info("event briefs written", "count", n)
			}
		})

	}

	// Decisions -- classification and news scoring -- run when either model
	// can make them. They belong to Jev when a TypeSafe key is set and to the
	// text model otherwise, which is decided inside the service, so the
	// schedule only has to know that one of them exists.
	if cfg.LLMConfigured() || svc.JevConfigured() {
		// News scoring runs shortly after collection, on Jev when configured, else the text model's cheap tier.
		add("news digest", "25 * * * *", func(runCtx context.Context) {
			if _, err := svc.RunNewsDigest(runCtx, 40); err != nil {
				log.Warn("scheduled news digest failed", "err", err)
			}
		})

		// Event classification, hourly, most important first. The text model
		// fits about 300 into the window; Jev answers in under a second, so
		// 3,000 fits and clears a backlog in a day.
		classifyLimit := 300
		if svc.JevConfigured() {
			classifyLimit = 3000
		}
		add("event classification", "40 * * * *", func(runCtx context.Context) {
			n, err := svc.ClassifyEvents(runCtx, classifyLimit)
			if err != nil {
				log.Warn("event classification failed", "err", err)
				return
			}
			if n > 0 {
				log.Info("events classified", "count", n)
			}
		})
	}

	c.Start()
	return c
}

// generateOutlooks writes an outlook for up to max watched symbols, taking
// those whose last one is oldest and skipping any forecast within the
// current horizon, so the same outlook is not produced twice.
func generateOutlooks(ctx context.Context, svc *ai.Service, store *postgres.DB, max int, log *slog.Logger) int {
	watched, err := store.WatchedSymbols(ctx)
	if err != nil {
		log.Warn("outlooks: could not read the watchlist", "err", err)
		return 0
	}
	type candidate struct {
		sym  marketdata.Symbol
		last time.Time
	}
	var due []candidate
	for _, sym := range watched {
		if sym.IsIndex() {
			continue
		}
		c := candidate{sym: sym}
		if prev, err := store.ListOutlooks(ctx, sym.String(), 1); err == nil && len(prev) > 0 {
			c.last = prev[0].CreatedAt
		}
		if time.Since(c.last) > ai.DefaultHorizonDays*24*time.Hour {
			due = append(due, c)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].last.Before(due[j].last) })
	// The forecast model's three strongest picks get an outlook too, so the
	// AI's read of them is scored beside the model's.
	if _, preds, err := store.LatestForecast(ctx); err == nil {
		for _, p := range preds[:min(3, len(preds))] {
			sym, err := marketdata.ParseSymbol(p.Symbol)
			if err != nil {
				continue
			}
			if prev, err := store.ListOutlooks(ctx, p.Symbol, 1); err == nil && len(prev) > 0 && time.Since(prev[0].CreatedAt) < ai.DefaultHorizonDays*24*time.Hour {
				continue
			}
			due = append([]candidate{{sym: sym}}, due...)
			max++
		}
	}
	made := 0
	for _, c := range due[:min(max, len(due))] {
		if _, _, err := svc.GenerateOutlook(ctx, c.sym); err != nil {
			log.Warn("outlooks: could not generate", "symbol", c.sym, "err", err)
			continue
		}
		made++
	}
	return made
}
