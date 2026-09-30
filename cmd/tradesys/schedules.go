package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/fundamentals"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/scanner"
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
) *cron.Cron {
	c := cron.New(cron.WithLocation(cfg.DisplayTZ), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger), cron.Recover(cron.DefaultLogger)))

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
		// A spec carrying its own CRON_TZ= prefix (robfig/cron's per-job
		// override, used by the venue-scoped market scans below) runs in
		// that zone, not the scheduler's shared DisplayTZ -- logging
		// DisplayTZ unconditionally here would tell an operator debugging
		// scan timing the wrong zone for exactly those jobs.
		tz := cfg.DisplayTZ.String()
		if rest, ok := strings.CutPrefix(spec, "CRON_TZ="); ok {
			if zone, _, ok := strings.Cut(rest, " "); ok {
				tz = zone
			}
		} else if rest, ok := strings.CutPrefix(spec, "TZ="); ok {
			if zone, _, ok := strings.Cut(rest, " "); ok {
				tz = zone
			}
		}
		log.Info("scheduled", "job", name, "cron", spec, "tz", tz)
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

	// The market scan, run once per venue's own trading day rather than
	// once for the whole (now combined NSE+US) universe at IST-only times --
	// scanning the US half of the universe only at IST-aligned hours would
	// mean scanning it three times a day, always while its market is closed.
	// Each job carries its own CRON_TZ= prefix (robfig/cron's per-job
	// timezone override) instead of depending on the scheduler's shared
	// DisplayTZ, so this stays correct regardless of what an operator sets
	// DISPLAY_TZ to.
	//
	// The close scan is the important one in either venue: it sees the full
	// day's volume, which is the number the statistics are actually about. A
	// midday scan compares a partial session against complete historical
	// days and reads every stock as quiet — the comparison is not like for
	// like. So the intraday scans exist to catch violent moves early and are
	// understood to under-report; the close scan is the one whose output
	// should be trusted.
	if marketScanner != nil {
		usScope := func(s marketdata.Symbol) bool { return !s.IsIndian() && !s.IsIndex() }
		scan := func(scope func(marketdata.Symbol) bool) func(context.Context) {
			return func(runCtx context.Context) {
				if _, err := marketScanner.Run(runCtx, scope); err != nil {
					log.Warn("market scan failed", "err", err)
				}
			}
		}
		add("market scan (us close)", "CRON_TZ=America/New_York 15 16 * * 1-5", scan(usScope))
		add("market scan (us intraday)", "CRON_TZ=America/New_York 15,45 10-15 * * 1-5", scan(usScope))
		add("market scan (us open)", "CRON_TZ=America/New_York 45 9 * * 1-5", scan(usScope))

		// The forward calendar, once a day before the US open.
		//
		// Everything else this app stores is a record of what has happened;
		// this is the only thing it knows about what has not. Earnings dates
		// move rarely, so daily is ample -- and it runs on a long window
		// because a 2,254-symbol refresh walks the upstream one name at a
		// time rather than fanning out, which is deliberate: the scan path
		// already demonstrated what threading this sidecar does to its
		// memory.
		addWithin("forward calendar", "CRON_TZ=America/New_York 20 7 * * 1-5",
			90*time.Minute, func(runCtx context.Context) {
				if _, err := refreshCalendar(runCtx, marketScanner.Client, store, marketScanner.Universe(), log); err != nil {
					log.Warn("calendar refresh failed", "err", err)
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

	// Retention, outside market hours. Events and the evidence behind them
	// are kept indefinitely — they are the historical record everything later
	// will be tested against. What is pruned is items that produced no event.
	// Move aged news to the archive, in the quiet window between the US close
	// and the next open.
	//
	// Given an hour rather than the shared fifteen minutes: the first run on an
	// existing database has months of news to move, in batches, to a managed
	// endpoint that is not on this machine. Subsequent runs move a day's worth
	// and finish in seconds.
	if newsArchive != nil {
		addWithin("news rollover", "CRON_TZ=America/New_York 30 2 * * *", time.Hour,
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

	// Congressional PTR filings, once a day: the House Clerk posts new
	// disclosures on no fixed schedule, so there is no "close" moment to
	// chase the way there is for a market scan. Given its own two-hour
	// budget rather than the shared fifteen minutes -- the first run of a
	// fresh deploy can find several hundred filings outstanding for the
	// year, each a separate PDF fetch plus a pdftotext shell-out, and that
	// backfill should be allowed to actually finish rather than being cut
	// off partway and repeating the same early filings tomorrow.
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
	if sm, err := newSmartMoneySyncer(cfg, store, log); err != nil {
		log.Warn("smart money sync disabled", "err", err)
	} else if sm != nil {
		addWithin("insider trades sync", "CRON_TZ=America/New_York */30 6-23 * * 1-5", 25*time.Minute, func(runCtx context.Context) {
			if n, err := sm.SyncInsiders(runCtx, 4); err != nil {
				log.Warn("insider trades sync failed", "err", err)
			} else if n > 0 {
				log.Info("insider trades synced", "trades", n)
			}
		})
		addWithin("fund holdings sync", "CRON_TZ=America/New_York 20 7 * * *", time.Hour, func(runCtx context.Context) {
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
		// The morning brief, an hour before the US open.
		//
		// It used to run at 08:30 in the display timezone, which with
		// DISPLAY_TZ=Asia/Kolkata fired it at 23:00 ET -- half a day before
		// the session it is meant to precede, and against an overnight tape
		// that had not happened yet. Anchored to the venue like the market
		// scans above, for the same reason: a brief is about a trading day,
		// not about a wall clock.
		add("morning brief", "CRON_TZ=America/New_York 30 8 * * 1-5", func(runCtx context.Context) {
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
			// Sixty, not the hundred-and-twenty this first had.
			//
			// Measured: a brief takes about eleven seconds, and this job runs
			// under the shared fifteen-minute deadline — so a cap of 120 would
			// be cancelled around the eightieth and the rest of the run
			// wasted. Sixty finishes in about eleven minutes with margin, and
			// is still three times the rate at which high-importance events
			// actually arrive.
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

		// Event classification, hourly. Storage hands these back
		// most-important-first, so a capped run spends effort on what matters
		// instead of on whatever happened to arrive last.
		//
		// The limit depends on who is classifying. The text model is a slow
		// reasoning model in batches of six and 300 is what fits the
		// fifteen-minute window. Jev answers one event in well under a second,
		// eight at a time, so 3,000 fits with room -- enough to clear the
		// backlog in about a day rather than months.
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
