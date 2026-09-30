// Command tradesys runs the TradeSys Dashboard: a market analysis and alerting
// server for US equities.
//
// This build contains no order-placement code path of any kind. It observes
// markets and notifies; it does not trade.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embeds the IANA timezone database: a minimal container image has none,
	// and every schedule and session boundary is kept in New York time.
	_ "time/tzdata"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/fundamentals"
	"github.com/tradesys/dashboard/internal/health"
	"github.com/tradesys/dashboard/internal/jev"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/notify"
	"github.com/tradesys/dashboard/internal/notify/telegram"
	"github.com/tradesys/dashboard/internal/scanner"
	"github.com/tradesys/dashboard/internal/server"
	"github.com/tradesys/dashboard/internal/storage/postgres"
	"github.com/tradesys/dashboard/internal/stream"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// seedSymbols populate an empty watchlist on first run: large caps across
// sectors rather than tech alone. Anything else is a search away.
var seedSymbols = []string{"AAPL", "MSFT", "NVDA", "GOOGL", "AMZN", "META", "JPM", "XOM", "JNJ", "WMT"}

func main() {
	// -reprocess rebuilds every event from the stored raw items, so a
	// corrected parser or clustering rule reaches history too. A flag rather
	// than a route: it is destructive to derived data and belongs to whoever
	// has a shell.
	reprocess := flag.Bool("reprocess", false,
		"discard all derived events and rebuild them from stored raw items, then exit")

	// The daily cron job (see startAISchedules) is the normal path; this
	// exists for an operator who wants a backfill or a resync run right
	// now rather than waiting for the schedule, without needing an HTTP
	// route for what is otherwise an unauthenticated write path.
	syncCongress := flag.Bool("sync-congress", false,
		"fetch new House Clerk PTR filings for the current year, then exit")
	backfillInsiders := flag.Int("backfill-insiders", 0,
		"load the newest N quarters of SEC's insider transaction datasets, then exit")
	runForecastFlag := flag.Bool("forecast", false,
		"validate the forecast model and store today's ranking, then exit")
	backfillHistory := flag.Bool("backfill-history", false,
		"store five years of daily bars for the whole universe, then exit")
	backfillEarnings := flag.Bool("backfill-earnings", false,
		"store past earnings announcements for the whole universe, then exit")
	refreshCal := flag.Bool("refresh-calendar", false,
		"refresh the forward corporate calendar for the whole universe, then exit")
	syncSmartMoney := flag.Int("sync-smartmoney", 0,
		"fetch insider trades for the last N days and followed funds' 13Fs, then exit")
	mergeDuplicates := flag.Int("merge-duplicates", 0,
		"clean headlines and merge duplicate events from the last N days, then exit")
	rolloverNews := flag.Bool("rollover-news", false,
		"move aged news to the archive database now, then exit")
	reclaimSpace := flag.Bool("reclaim-space", false,
		"after -rollover-news, rewrite the news tables to return freed space to disk. "+
			"Takes an exclusive lock per table: ingestion stalls and the feed errors while it runs")

	// Key management is a command-line operation and has no HTTP equivalent.
	// Issuing a key grants access to everything, so the right to do it is
	// tied to shell access on the host rather than to a role inside the
	// application: there is no endpoint to find and no path from a stolen key
	// to minting more of them.
	issueKey := flag.Bool("issue-key", false, "issue a new API key and exit")
	listKeys := flag.Bool("list-keys", false, "list every API key profile and exit")
	revokeKey := flag.String("revoke-key", "", "revoke the key with this prefix, then exit")
	keyName := flag.String("name", "", "profile name for -issue-key")
	keyRole := flag.String("role", "operator", "role for -issue-key: owner, operator or viewer")
	keyNote := flag.String("note", "", "optional note for -issue-key")
	flag.Parse()

	if *issueKey || *listKeys || *revokeKey != "" {
		cfg, err := config.Load(".env")
		if err != nil {
			slog.Error("could not load configuration", "err", err)
			os.Exit(1)
		}
		ctx := context.Background()
		switch {
		case *issueKey:
			err = runIssueKey(ctx, cfg.DatabaseURL, *keyName, *keyRole, *keyNote)
		case *listKeys:
			err = runListKeys(ctx, cfg.DatabaseURL)
		default:
			err = runRevokeKey(ctx, cfg.DatabaseURL, *revokeKey)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n  %v\n\n", err)
			os.Exit(1)
		}
		return
	}

	if *reprocess {
		if err := runReprocess(); err != nil {
			slog.Error("reprocess failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *syncCongress {
		if err := runSyncCongress(); err != nil {
			slog.Error("congress sync failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *backfillInsiders > 0 {
		if err := runBackfillInsiders(*backfillInsiders); err != nil {
			slog.Error("insider backfill failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *runForecastFlag {
		if err := runForecastCmd(); err != nil {
			slog.Error("forecast failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *backfillHistory {
		if err := runBackfillHistory(); err != nil {
			slog.Error("history backfill failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *backfillEarnings {
		if err := runBackfillEarnings(); err != nil {
			slog.Error("earnings backfill failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *refreshCal {
		if err := runRefreshCalendar(); err != nil {
			slog.Error("calendar refresh failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *syncSmartMoney > 0 {
		if err := runSyncSmartMoney(*syncSmartMoney); err != nil {
			slog.Error("smart money sync failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *mergeDuplicates > 0 {
		if err := runMergeDuplicatesCmd(*mergeDuplicates); err != nil {
			slog.Error("duplicate merge failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if *rolloverNews {
		if err := runRolloverNews(*reclaimSpace); err != nil {
			slog.Error("news rollover failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// runReprocess rebuilds the event layer from raw items.
func runReprocess() error {
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

	discarded, err := store.ResetEvents(ctx)
	if err != nil {
		return err
	}
	log.Info("discarded derived events; rebuilding from raw items", "discarded", discarded)

	master, err := company.Load()
	if err != nil {
		return fmt.Errorf("load company master: %w", err)
	}
	registry, err := buildRegistry(cfg.SECUserAgent)
	if err != nil {
		return fmt.Errorf("build source registry: %w", err)
	}
	// The watchlist sources are part of the catalog at runtime, so they must
	// be part of it here too. Rebuilding without them makes every item those
	// sources collected come from an unknown source, and the processor
	// discards it — which silently emptied the whole watchlist on reprocess.
	syncWatchlistSources(ctx, registry, store, master, nil, log)

	processor := events.NewProcessor(store, master, registry,
		events.WithProcessorLogger(log))

	var total, created, merged, filtered int
	for {
		res, err := processor.ProcessBatch(ctx, 1000)
		if err != nil {
			return fmt.Errorf("reprocess batch: %w", err)
		}
		if res.Items == 0 {
			break
		}
		total += res.Items
		created += res.Created
		merged += res.Merged
		filtered += res.Filtered
		log.Info("reprocessed batch", "items", res.Items, "created", res.Created,
			"merged", res.Merged, "filtered", res.Filtered)
	}
	log.Info("reprocess complete",
		"items", total, "events", created, "merged", merged, "filtered", filtered)
	return nil
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	log.Info("starting TradeSys Dashboard", "version", version, "addr", cfg.Addr)

	// The signal context is created before anything that can block, so a
	// Ctrl-C during a slow database connect is honoured rather than ignored
	// until after startup completes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openStore(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer store.Close()

	// The news archive, when a second database is configured for it.
	//
	// Deliberately not fatal when it cannot be opened. The primary holds
	// everything recent, which is what almost every read asks for, so an
	// unreachable archive should cost the long tail of the feed rather than
	// the whole app -- and a free-tier endpoint that is asleep or rate-limited
	// is a normal Tuesday, not an outage.
	var newsArchive *postgres.Archive
	if cfg.NewsArchiveURL != "" {
		newsArchive, err = postgres.OpenArchive(ctx, store, cfg.NewsArchiveURL, cfg.NewsHotWindow,
			postgres.WithLogger(log))
		if err != nil {
			log.Error("news archive unavailable; running on the primary alone", "err", err)
			newsArchive = nil
		} else {
			defer newsArchive.Close()
		}
	} else {
		log.Info("no news archive configured; the primary holds all news")
	}

	providers, deps, budgets, searcher := buildProviders(cfg, store, log)
	if len(providers) == 0 {
		// Deliberately not fatal. Refusing to start would also refuse access
		// to the Settings page, which is exactly where an operator goes to
		// find out what is misconfigured. The router serves whatever is
		// cached, the health dots show every provider as unconfigured, and
		// the dashboard says so plainly.
		log.Warn("no market data providers are configured; serving cached prices only",
			"fix", "set YFINANCE_URL, TWELVEDATA_API_KEY or ALPHAVANTAGE_API_KEY, or ENABLE_SYNTHETIC_FALLBACK=true")
	}

	tracker := health.New(store, deps, health.WithLogger(log))
	router := marketdata.NewRouter(store, providers,
		marketdata.WithOutcomeSink(tracker.MarketDataSink()),
		marketdata.WithLogger(log))
	log.Info("market data providers configured", "order", router.ProviderNames())

	if err := seedWatchlist(ctx, store, log); err != nil {
		// A failed seed is not worth refusing to start over.
		log.Warn("could not seed the watchlist", "err", err)
	}
	if err := seedAlgorithms(ctx, store, log); err != nil {
		log.Warn("could not seed the algorithm templates", "err", err)
	}

	// Alert pipeline.
	evaluator := algo.NewEvaluator()

	var telegramOpts []telegram.Option
	if cfg.TelegramAPIBaseURL != "" {
		log.Info("using a custom Telegram API host", "base_url", cfg.TelegramAPIBaseURL)
		telegramOpts = append(telegramOpts, telegram.WithBaseURL(cfg.TelegramAPIBaseURL))
	}
	telegramClient := telegram.New(cfg.TelegramBotToken, cfg.TelegramChatID, telegramOpts...)
	notifiers := []alerts.Notifier{
		notify.WithHealth(telegramClient, tracker.Observe),
	}
	if !cfg.TelegramConfigured() {
		log.Info("telegram is not configured; alerts will appear in the in-app feed only")
	}

	newsPoller := news.NewPoller(store, store,
		news.WithPollerLogger(log), news.WithObserver(tracker.Observe))

	// The ingestion engine runs on its own ticker, because cadence belongs to
	// each source. The attention tracker sits between the pipeline's halves:
	// the processor tells it what happened and the scheduler asks where to
	// spend effort. In memory on purpose: heat is about the last hour.
	attention := news.NewTracker(time.Now)

	companyMaster, err := company.Load()
	if err != nil {
		return fmt.Errorf("load company master: %w", err)
	}
	universe := buildScanUniverse(companyMaster)
	log.Info("scan universe ready", "symbols", len(universe.symbols))

	// The market scanner finds what is behaving abnormally from price and
	// volume alone, without waiting for somebody to write about it: the price
	// moves first and the explanation follows.
	var marketScanner *scanner.Runner
	if cfg.YFinanceURL != "" {
		marketScanner = &scanner.Runner{
			Client: &scanner.Client{
				BaseURL: cfg.YFinanceURL,
				// Generous: a 750-name scan is one request that fans out
				// inside the sidecar, and cutting it off halfway wastes the
				// work already done rather than saving anything.
				HTTP:   &http.Client{Timeout: 8 * time.Minute},
				Adjust: cfg.YFinanceAdjust,
			},
			Store:     store,
			Explainer: store,
			Observe:   attention.ObserveSignal,
			Universe:  universe.Symbols,
			Log:       log,
		}
		log.Info("market scanner ready", "universe", len(universe.symbols))
	} else {
		log.Warn("market scanner disabled: no YFINANCE_URL configured")
	}

	// The index constituent list, refreshed from the embedded master on every
	// start. It decides what the default market feed is about, so it must not
	// be allowed to drift from the company data the rest of the system uses.
	if err := store.ReplaceIndexConstituents(ctx, universe.listings); err != nil {
		// Not fatal: an empty list makes the feed show everything, which is
		// the old behaviour rather than a broken one.
		log.Error("could not record index constituents", "err", err)
	} else if n, err := store.CountIndexConstituents(ctx); err == nil {
		log.Info("index constituents recorded", "count", n)
	}

	// The live stream. One hub, several sources, and a transport that pushes
	// rather than being polled — replacing twelve independent front-end
	// timers, each of which added up to a minute of latency to data whose
	// whole value is being current.
	streamHub := stream.NewHub(log)
	var streamPumps []*stream.Pump

	if router != nil {
		quotes := &stream.QuoteSource{
			Router: router,
			Symbols: func() []marketdata.Symbol {
				// Read fresh each cycle, so adding an instrument to the
				// watchlist starts streaming it without a restart.
				syms, err := store.WatchedSymbols(ctx)
				if err != nil {
					log.Debug("could not read the watchlist for streaming", "err", err)
					return nil
				}
				return syms
			},
			Log:          log,
			Interval:     cfg.StreamInterval,
			IdleInterval: time.Minute,
		}
		pump := &stream.Pump{Hub: streamHub, Source: quotes, Log: log}
		streamPumps = append(streamPumps, pump)
		go pump.Run(ctx)
		log.Info("quote stream started", "interval", cfg.StreamInterval)
	}

	// Fundamentals: what a company is worth, as distinct from what is being
	// said about it. Refreshed on a schedule rather than on demand — a full
	// universe pass is around an hour of upstream requests, which is fine
	// overnight and impossible on a page load.
	var fundamentalsRunner *fundamentals.Runner
	if cfg.YFinanceURL != "" {
		fundamentalsRunner = &fundamentals.Runner{
			Client: &fundamentals.Client{
				BaseURL: cfg.YFinanceURL,
				HTTP:    &http.Client{Timeout: 15 * time.Minute},
			},
			Store:    store,
			Universe: universe.Symbols,
			Log:      log,
		}
	}

	ingestEngine, err := startIngestion(ctx, store, attention, companyMaster, marketScanner, cfg.SECUserAgent, log)
	if err != nil {
		return err
	}
	if marketScanner != nil {
		// Registered after ingestion because the registry it writes to is
		// built there. A scan that finds something now starts the search for
		// why now, rather than at the next five-minute sync.
		marketScanner.OnAttention = func() {
			syncWatchlistSources(ctx, ingestEngine.Registry(), store, companyMaster, marketScanner, log)
		}
		// A scan that finds an unexplained move should reach the screen as it
		// finds it, not on the next refresh.
		marketScanner.OnFinding = func(f scanner.Finding) {
			streamHub.Publish(stream.TopicScanner, f.Symbol, f)
		}
	}

	// The event processor is deterministic and needs no model. It must share
	// the ingestion engine's registry: watchlist and scanner sources are added
	// to that copy at runtime, and the processor discards items from a source
	// it does not know.
	eventProcessor := events.NewProcessor(store, companyMaster, ingestEngine.Registry(),
		events.WithProcessorLogger(log),
		events.WithAttentionSink(attention),
		// Newly created events go straight to any connected client. This is
		// the difference between a filing appearing on screen when it is
		// published and appearing when a twenty-second timer next fires.
		events.WithEventSink(func(n events.EventNotice) {
			key := ""
			if len(n.Symbols) > 0 {
				key = n.Symbols[0]
			}
			streamHub.Publish(stream.TopicEvents, key, n)
		}))
	log.Info("event processor ready", "companies", companyMaster.Len())

	llm := ai.New(ai.Config{
		BaseURL:      cfg.LLMBaseURL,
		APIKey:       cfg.LLMAPIKey,
		Model:        cfg.LLMModel,
		CheapModel:   cfg.LLMCheapModel,
		MonthlyLimit: cfg.LLMMonthlyTokenBudget,
		ExtraBody:    cfg.LLMExtraBody,
	}, store, ai.WithLogger(log), ai.WithOutcomeSink(tracker.LLMSink()),
		// A hard ceiling on text-model spend per UTC day, checked against the
		// worst case before every call. See ai.WithDailyCapUSD.
		ai.WithDailyCapUSD(cfg.LLMDailyUSDCap))

	// Jev makes the decisions -- event classification and news scoring --
	// when a TypeSafe key is set. Without one those stay on the text model.
	jevOpts := []jev.Option{
		jev.WithLogger(log),
		jev.WithOutcomeSink(tracker.JevSink()),
		jev.WithSpendRecorder(store),
		jev.WithInputPrice(cfg.JevInputPriceUSD),
	}
	if cfg.JevModel != "" {
		jevOpts = append(jevOpts, jev.WithModel(cfg.JevModel))
	}
	jevClient := jev.New(cfg.TypeSafeAPIKey, jevOpts...)
	if jevClient.Configured() {
		log.Info("decisions routed to jev", "model", jevClient.Model())
	} else {
		log.Info("no TYPESAFE_API_KEY; decisions stay on the text model")
	}

	researchEngine := buildResearchEngine(cfg, store, newsArchive, companyMaster, router, log)

	aiService := ai.NewService(llm, router, store,
		ai.WithServiceLogger(log),
		ai.WithResearchContext(ctx),
		ai.WithSearch(webNews{researchEngine}),
		ai.WithNews(store),
		ai.WithScoreStore(store),
		ai.WithWatchlist(store),
		ai.WithAlerts(store),
		ai.WithJev(jevClient),
		ai.WithResearchModel(cfg.LLMResearchModel, cfg.LLMResearchEffort))

	if !cfg.LLMConfigured() {
		log.Info("no LLM configured; AI features will report as unconfigured and everything else runs normally")
	}

	engineOpts := []alerts.EngineOption{alerts.WithLogger(log)}
	if cfg.LLMConfigured() {
		engineOpts = append(engineOpts, alerts.WithAIContext(aiService))
	}
	// Algorithms may point at named watchlists, resolved when they run so a
	// list edited today changes what every attached rule watches tomorrow.
	engineOpts = append(engineOpts, alerts.WithWatchlists(store))

	engine := alerts.NewEngine(store, store, router, evaluator, notifiers, engineOpts...)

	// A deployment reachable from the internet with no passphrase is the one
	// misconfiguration worth shouting about: every write endpoint — deleting
	// an algorithm, editing the watchlist, spending the month's token budget —
	// answers anyone who finds the host.
	if n, err := store.CountActiveKeys(ctx); err != nil {
		log.Warn("could not count API keys", "err", err)
	} else if n == 0 && cfg.AllowUnauthenticated {
		log.Warn("no API keys have been issued, so this deployment is OPEN and " +
			"every endpoint including writes answers anyone who can reach it — " +
			"issue one with: tradesys -issue-key -name \"you\" -role owner")
	} else if n == 0 {
		log.Warn("API locked until an owner key is issued: tradesys -issue-key -name owner -role owner")
	} else {
		log.Info("api keys active", "count", n)
		if cfg.SessionSecret == "" {
			log.Info("SESSION_SECRET is not set; browser sessions will not survive a restart")
		}
	}

	scheduler := alerts.NewScheduler(engine, alerts.WithSchedulerLogger(log))
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer scheduler.Stop()

	// A process that died mid-research leaves a turn in 'running' forever,
	// which the interface would show as a question permanently in progress.
	// Anything still running from before this process started cannot be ours.
	if n, err := store.ReapAbandonedTurns(ctx, 10*time.Minute); err != nil {
		log.Warn("could not reap abandoned research turns", "err", err)
	} else if n > 0 {
		log.Info("failed research turns abandoned by a previous process", "count", n)
	}

	paperEngine, paperRunner, paperWebhook := startPaper(ctx, store, router, aiServiceIfConfigured(cfg, aiService), evaluator, companyMaster, cfg.PaperWebhookAllow, log)
	smartMoney, err := newSmartMoneySyncer(cfg, store, log)
	if err != nil {
		log.Warn("smart money sync disabled", "err", err)
	}
	aiCron := startAISchedules(ctx, cfg, log, aiService, newsPoller, eventProcessor, store,
		marketScanner, fundamentalsRunner, newsArchive, companyMaster, smartMoney)
	defer func() { <-aiCron.Stop().Done() }()

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: server.New(server.Deps{
			Config:        cfg,
			Store:         store,
			Router:        router,
			Health:        tracker,
			Budgets:       budgets,
			Evaluator:     evaluator,
			Engine:        engine,
			Scheduler:     scheduler,
			Scanner:       marketScanner,
			SessionSecret: cfg.SessionSecret,
			Stream:        streamHub,
			StreamPumps:   streamPumps,
			AI:            aiService,
			NewsArchive:   newsArchive,
			NewsPoller:    newsPoller,
			Ingest:        ingestEngine,
			Attention:     attention,
			Companies:     companyMaster,
			Processor:     eventProcessor,
			Research:      researchEngine,
			SmartMoney:    smartMoney,
			Paper:         paperEngine,
			PaperRunner:   paperRunner,
			PaperWebhook:  paperWebhook,
			LuckPool:      func(ctx context.Context) map[string][]marketdata.Candle { return luckPool(ctx, store, companyMaster) },
			Searcher:      searcher,
			Log:           log,
			Version:       version,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// Must exceed the longest per-route timeout, or the connection is torn
		// down before the handler can write its response — and the AI and scan
		// routes legitimately run for minutes. Derived from the routing table
		// rather than restated, so raising a route's deadline cannot silently
		// outgrow this one.
		WriteTimeout: server.LongestRouteTimeout + 2*time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown requested, draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped cleanly")
	return nil
}

// openStore connects to Postgres and migrates it.
func openStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (*postgres.DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set; Postgres is required")
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL, postgres.WithLogger(log))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	log.Info("database ready", "engine", "postgres")
	return store, nil
}

// orphanRetentionDays is how long a raw item that produced no event is kept.
// Long enough that a parser fix can still reprocess a month of history, short
// enough that filtered noise does not accumulate indefinitely.
const orphanRetentionDays = 30
