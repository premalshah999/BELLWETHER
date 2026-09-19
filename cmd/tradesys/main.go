// Command tradesys runs the TradeSys Dashboard: a market analysis and alerting
// server for Indian and US equities.
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	// Embeds the IANA timezone database in the binary. Without it a minimal
	// container image cannot resolve DISPLAY_TZ, and every timestamp in the
	// app is meant to be shown in the operator's zone.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/fundamentals"
	"github.com/tradesys/dashboard/internal/health"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/marketdata/alphavantage"
	"github.com/tradesys/dashboard/internal/marketdata/fixture"
	"github.com/tradesys/dashboard/internal/marketdata/twelvedata"
	"github.com/tradesys/dashboard/internal/marketdata/yahoo"
	"github.com/tradesys/dashboard/internal/marketdata/yfin"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/notify"
	"github.com/tradesys/dashboard/internal/notify/telegram"
	"github.com/tradesys/dashboard/internal/research"
	"github.com/tradesys/dashboard/internal/scanner"
	"github.com/tradesys/dashboard/internal/search"
	"github.com/tradesys/dashboard/internal/search/brave"
	"github.com/tradesys/dashboard/internal/search/tavily"
	"github.com/tradesys/dashboard/internal/server"
	"github.com/tradesys/dashboard/internal/storage"
	"github.com/tradesys/dashboard/internal/storage/postgres"
	"github.com/tradesys/dashboard/internal/stream"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// seedSymbols populate an empty watchlist on first run.
//
// A fresh install opening on two tickers looks like a demo, so this is a
// working set a US-first operator would plausibly start from: US megacaps
// across sectors, plus a handful of large-cap Indian names since NSE stays a
// supported second venue. Anything else is a search away, and the whole list
// can be removed in a few clicks.
var seedSymbols = []string{
	// United States, across sectors rather than tech alone.
	"AAPL",
	"MSFT",
	"NVDA",
	"GOOGL",
	"AMZN",
	"META",
	"JPM",
	"XOM",
	"JNJ",
	"WMT",

	// India — NSE, which has deeper history than BSE on most of these.
	"RELIANCE.NSE",
	"TCS.NSE",
	"HDFCBANK.NSE",
	"INFY.NSE",
	"ICICIBANK.NSE",
}

func main() {
	// -reprocess rebuilds every event from the raw items already held, then
	// exits. It exists because the derived layer is disposable by design:
	// when a parser or a clustering rule is corrected, history should get the
	// correction too rather than carrying the old bug forever.
	//
	// It is a command-line flag rather than an HTTP route on purpose. The
	// operation is destructive to derived data, and this build has no
	// authentication, so it must not be reachable from the internet.
	reprocess := flag.Bool("reprocess", false,
		"discard all derived events and rebuild them from stored raw items, then exit")
	migrateFrom := flag.String("migrate-from", "",
		"copy an existing SQLite database at this path into Postgres, then exit")

	// The daily cron job (see startAISchedules) is the normal path; this
	// exists for an operator who wants a backfill or a resync run right
	// now rather than waiting for the schedule, without needing an HTTP
	// route for what is otherwise an unauthenticated write path.
	syncCongress := flag.Bool("sync-congress", false,
		"fetch new House Clerk PTR filings for the current year, then exit")

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

	if *migrateFrom != "" {
		cfg, err := config.Load(".env")
		if err != nil {
			slog.Error("migration failed", "err", err)
			os.Exit(1)
		}
		log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
		if err := runDataMigration(context.Background(), log, *migrateFrom, cfg.DatabaseURL); err != nil {
			log.Error("migration failed", "err", err)
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

	// The US universe is merged into the master, not kept beside it: the
	// resolver has to be able to name a US company from free text, or every
	// US publisher's item arrives entity-free and the relevance gate
	// discards it (see company.MergeUS for the measured damage that did).
	master, err := company.LoadEmbeddedWithUS()
	if err != nil {
		return fmt.Errorf("load company master: %w", err)
	}
	usTickers, _, err := company.LoadEmbeddedUS()
	if err != nil {
		return fmt.Errorf("load us company master: %w", err)
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
		events.WithProcessorLogger(log), events.WithUSCIKIndex(buildUSCIKIndex(usTickers)))

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

	providers, deps, budgets, searcher := buildProviders(cfg, store, log)
	if len(providers) == 0 {
		// Deliberately not fatal. Refusing to start would also refuse access
		// to the Settings page, which is exactly where an operator goes to
		// find out what is misconfigured. The router serves whatever is
		// cached, the health dots show every provider as unconfigured, and
		// the dashboard says so plainly.
		log.Warn("no market data providers are configured; serving cached prices only",
			"fix", "set TWELVEDATA_API_KEY or ALPHAVANTAGE_API_KEY, keep yahoo in MARKETDATA_ORDER, or set ENABLE_SYNTHETIC_FALLBACK=true")
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
	evaluator := algo.NewEvaluator(cfg.DisplayTZ)

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

	// Search, news, and the AI feature layer.
	searchRouter := search.NewRouter(store, buildSearchProviders(cfg),
		search.WithLogger(log), search.WithObserver(tracker.Observe))

	newsPoller := news.NewPoller(store, store,
		news.WithPollerLogger(log), news.WithObserver(tracker.Observe))

	// The ingestion engine: the collection half of the intelligence layer.
	//
	// It runs on its own ticker rather than on the cron, because cadence is a
	// property of each source. An exchange filing feed is worth polling every
	// minute; a weekly shareholding disclosure is not, and a single cron
	// expression cannot express both. The engine ticks often and asks each
	// source whether it is due.
	// The attention tracker sits between the two halves of the pipeline: the
	// processor tells it what happened, and the scheduler asks it where to
	// spend effort. It is deliberately in-memory — heat is a scheduling
	// heuristic about the last hour, not a fact worth surviving a restart,
	// and rebuilding it costs one polling cycle.
	attention := news.NewTracker(time.Now)

	companyMaster, err := company.LoadEmbeddedWithUS()
	if err != nil {
		return fmt.Errorf("load company master: %w", err)
	}
	usTickers, usListings, err := company.LoadEmbeddedUS()
	if err != nil {
		return fmt.Errorf("load us company master: %w", err)
	}
	universe := buildScanUniverse(companyMaster, usListings)
	usByCIK := buildUSCIKIndex(usTickers)
	log.Info("scan universe ready", "nse", len(companyMaster.IndexConstituents()), "us", len(usListings))

	// The market scanner: the half of the intelligence layer that does not
	// wait to be told.
	//
	// Everything above this point is reactive — it polls sources and reacts
	// to what arrives, which means an instrument becomes interesting only
	// once somebody has written about it. The scanner inverts that. It reads
	// price and volume across the index universe, finds what is behaving
	// abnormally, and raises that instrument's attention directly. The price
	// moves first; the explanation follows, sometimes by hours.
	var marketScanner *scanner.Runner
	if cfg.YFinanceURL != "" {
		marketScanner = &scanner.Runner{
			Client: &scanner.Client{
				BaseURL: cfg.YFinanceURL,
				// Generous: a 750-name scan is one request that fans out
				// inside the sidecar, and cutting it off halfway wastes the
				// work already done rather than saving anything.
				HTTP: &http.Client{Timeout: 8 * time.Minute},
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

	// The event processor: the deterministic half of the intelligence layer.
	// It needs no LLM, so it runs whether or not one is configured.
	//
	// It shares the ingestion engine's registry rather than building its own.
	// A second DefaultRegistry looks harmless — same catalog, same sources —
	// but the watchlist and scanner sources are added at runtime to the
	// engine's copy only. The processor discards any item whose source it
	// does not recognise, so with a private registry every targeted search
	// this system runs was collected, stored, and then silently dropped.
	// Watchlist events only ever appeared after a -reprocess, which builds
	// its registry with the watchlist included.
	eventProcessor := events.NewProcessor(store, companyMaster, ingestEngine.Registry(),
		events.WithProcessorLogger(log),
		events.WithAttentionSink(attention),
		events.WithUSCIKIndex(usByCIK),
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
	}, store, ai.WithLogger(log), ai.WithOutcomeSink(tracker.LLMSink()))

	aiService := ai.NewService(llm, router, store, cfg.DisplayTZ,
		ai.WithServiceLogger(log),
		ai.WithSearch(searchRouter),
		ai.WithNews(store),
		ai.WithScoreStore(store),
		ai.WithWatchlist(store),
		ai.WithAlerts(store))

	if !cfg.LLMConfigured() {
		log.Info("no LLM configured; AI features will report as unconfigured and everything else runs normally")
	}
	if !cfg.SearchConfigured() {
		log.Info("no search provider configured; explanations will fall back to collected news")
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
	} else if n == 0 {
		log.Warn("no API keys have been issued, so this deployment is OPEN and " +
			"every endpoint including writes answers anyone who can reach it — " +
			"issue one with: tradesys -issue-key -name \"you\" -role owner")
	} else {
		log.Info("api keys active", "count", n)
		if cfg.SessionSecret == "" {
			log.Info("SESSION_SECRET is not set; browser sessions will not survive a restart")
		}
	}

	scheduler := alerts.NewScheduler(engine, cfg.DisplayTZ, alerts.WithSchedulerLogger(log))
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer scheduler.Stop()

	researchEngine := buildResearchEngine(cfg, store, companyMaster, router, log)

	// A process that died mid-research leaves a turn in 'running' forever,
	// which the interface would show as a question permanently in progress.
	// Anything still running from before this process started cannot be ours.
	if n, err := store.ReapAbandonedTurns(ctx, 10*time.Minute); err != nil {
		log.Warn("could not reap abandoned research turns", "err", err)
	} else if n > 0 {
		log.Info("failed research turns abandoned by a previous process", "count", n)
	}

	aiCron := startAISchedules(ctx, cfg, log, aiService, newsPoller, eventProcessor, store,
		marketScanner, fundamentalsRunner)
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
			NewsPoller:    newsPoller,
			Ingest:        ingestEngine,
			Attention:     attention,
			Companies:     companyMaster,
			Processor:     eventProcessor,
			Research:      researchEngine,
			Searcher:      searcher,
			Log:           log,
			Version:       version,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// Must exceed the longest per-route timeout, or the connection is torn
		// down before the handler can write its response — and the AI routes
		// legitimately run for minutes.
		WriteTimeout: 6 * time.Minute,
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

// buildProviders assembles the provider chain in the configured order. This is
// the only place in the program that names a concrete adapter.
func buildProviders(cfg *config.Config, store storage.Store, log *slog.Logger) (
	[]marketdata.Provider, []health.Dep, map[string]server.BudgetReporter, marketdata.SymbolSearcher,
) {
	var (
		providers []marketdata.Provider
		deps      []health.Dep
		budgets   = map[string]server.BudgetReporter{}
		searcher  marketdata.SymbolSearcher
	)

	for _, name := range cfg.MarketDataOrder {
		switch name {
		case "yahoo":
			providers = append(providers, yahoo.New())
			// Yahoo needs no credentials, so it counts as configured always.
			deps = append(deps, health.Dep{Provider: "yahoo", Kind: health.KindMarketData, Configured: true})

		case "alphavantage":
			av := alphavantage.New(cfg.AlphaVantageKey, cfg.AlphaVantageDailyLimit, store)
			deps = append(deps, health.Dep{
				Provider:   alphavantage.ProviderName,
				Kind:       health.KindMarketData,
				Configured: cfg.AlphaVantageConfigured(),
			})
			budgets[alphavantage.ProviderName] = av
			if !cfg.AlphaVantageConfigured() {
				// Registered for health and budget reporting, but left out of
				// the fetch chain so every request does not pay for a
				// guaranteed failure.
				log.Info("alphavantage has no API key; skipping it in the provider chain")
				continue
			}
			providers = append(providers, av)

		case "yfinance":
			deps = append(deps, health.Dep{
				Provider:   yfin.ProviderName,
				Kind:       health.KindMarketData,
				Configured: cfg.YFinanceConfigured(),
			})
			if !cfg.YFinanceConfigured() {
				log.Info("no yfinance sidecar configured; skipping it in the provider chain")
				continue
			}
			client := yfin.New(cfg.YFinanceURL, yfin.WithDividendAdjustment(cfg.YFinanceAdjust))
			providers = append(providers, client)
			// The same client backs symbol discovery.
			searcher = client

		case "twelvedata":
			td := twelvedata.New(cfg.TwelveDataKey, cfg.TwelveDataDailyLimit, store)
			deps = append(deps, health.Dep{
				Provider:   twelvedata.ProviderName,
				Kind:       health.KindMarketData,
				Configured: cfg.TwelveDataConfigured(),
			})
			budgets[twelvedata.ProviderName] = td
			if !cfg.TwelveDataConfigured() {
				log.Info("twelvedata has no API key; skipping it in the provider chain")
				continue
			}
			providers = append(providers, td)

		case "synthetic":
			providers = append(providers, fixture.New())
			deps = append(deps, health.Dep{Provider: fixture.ProviderName, Kind: health.KindMarketData, Configured: true})
		}
	}

	// The synthetic provider is the last resort so a fresh install with no
	// keys and no network still renders a working dashboard. Everything it
	// produces is labelled "synthetic" all the way to the UI.
	if cfg.EnableSynthetic && !containsProvider(providers, fixture.ProviderName) {
		log.Warn("synthetic market data fallback is enabled: charts may show generated, non-market data")
		providers = append(providers, fixture.New())
		deps = append(deps, health.Dep{Provider: fixture.ProviderName, Kind: health.KindMarketData, Configured: true})
	}

	// Declare the dependencies later milestones own, so their dots render in a
	// truthful state from the first run.
	deps = append(deps,
		health.Dep{Provider: health.ProviderLLM, Kind: health.KindLLM, Configured: cfg.LLMConfigured()},
		health.Dep{Provider: tavily.Name, Kind: health.KindSearch, Configured: cfg.TavilyAPIKey != ""},
		health.Dep{Provider: brave.Name, Kind: health.KindSearch, Configured: cfg.BraveAPIKey != ""},
		// News needs no credentials, so it is always "configured" and its dot
		// reflects whether the feeds are actually reachable.
		health.Dep{Provider: "news", Kind: health.KindNews, Configured: true},
		health.Dep{Provider: "telegram", Kind: health.KindNotify, Configured: cfg.TelegramConfigured()},
	)
	return providers, deps, budgets, searcher
}

func containsProvider(ps []marketdata.Provider, name string) bool {
	for _, p := range ps {
		if p.Name() == name {
			return true
		}
	}
	return false
}

// seedWatchlist populates an empty watchlist on first run.
func seedWatchlist(ctx context.Context, store storage.Store, log *slog.Logger) error {
	existing, err := store.ListWatchlist(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	for _, raw := range seedSymbols {
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			return fmt.Errorf("seed symbol %q: %w", raw, err)
		}
		if err := store.AddWatchlist(ctx, sym, ""); err != nil {
			return err
		}
	}
	log.Info("seeded an empty watchlist", "symbols", seedSymbols)
	return nil
}

// seedAlgorithms installs the prebuilt templates on first run.
//
// They arrive disabled. A fresh installation must not start sending
// notifications nobody has looked at yet, but the builder should have real
// examples to open rather than a blank editor.
func seedAlgorithms(ctx context.Context, store storage.Store, log *slog.Logger) error {
	existing, err := store.ListAlgorithms(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	var names []string
	for _, tpl := range algo.Templates() {
		a := *tpl.Algorithm
		if _, err := store.CreateAlgorithm(ctx, &a); err != nil {
			return fmt.Errorf("seed template %q: %w", tpl.Key, err)
		}
		names = append(names, a.Name)
	}
	log.Info("seeded algorithm templates (all disabled)", "algorithms", names)
	return nil
}

// buildSearchProviders orders the search vendors by the operator's preference,
// so the configured favourite is tried first and the other is the fallback.
func buildSearchProviders(cfg *config.Config) []search.Provider {
	tav := tavily.New(cfg.TavilyAPIKey)
	brv := brave.New(cfg.BraveAPIKey)
	if cfg.SearchProvider == "brave" {
		return []search.Provider{brv, tav}
	}
	return []search.Provider{tav, brv}
}

// openStore connects to Postgres.
//
// There is no fallback to SQLite. A silent fallback is worse than a failure
// here: it would let the application start against an empty local file when
// the database was merely unreachable, and the first anyone would know of it
// is an empty dashboard that looks like a data problem rather than a
// connectivity one.
func openStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (*postgres.DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set; Postgres is required")
	}
	migrationOpt, err := venueMigrationOption()
	if err != nil {
		return nil, fmt.Errorf("prepare venue migration: %w", err)
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL, postgres.WithLogger(log), migrationOpt)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	log.Info("database ready", "engine", "postgres")
	return store, nil
}

// buildResearchEngine assembles the on-demand scrapers.
//
// These are separate from the scheduled catalog on purpose. The catalog exists
// to never miss anything on a fixed beat; this exists to answer a question
// somebody just typed, so it favours breadth and accepts that any individual
// scraper may be slow or rate-limited. Every one of them degrades
// independently.
func buildResearchEngine(cfg *config.Config, store *postgres.DB, master *company.Master, router *marketdata.Router, log *slog.Logger) *research.Engine {
	// The same transport reasoning as the ingestion engine: Go's default TLS
	// handshake timeout of ten seconds is shorter than some of these
	// endpoints take to negotiate at all. GDELT measured about 25 seconds
	// from this host and failed every single research call on the handshake,
	// which reads as "GDELT is down" rather than "our client gives up early".
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 45 * time.Second,
			ExpectContinueTimeout: 2 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   4,
			ForceAttemptHTTP2:     true,
		},
	}

	scrapers := []research.Scraper{
		// Our own archive first. It is the only source built for this
		// domain — filings resolved to NSE symbols, deduplicated, classified
		// — and the only one that costs nothing and answers in milliseconds.
		&research.LocalScraper{Store: store, Window: 45 * 24 * time.Hour},
		&research.GoogleNewsScraper{Client: client, HL: "en-IN", GL: "IN"},
		&research.GoogleNewsScraper{Client: client, HL: "en-US", GL: "US"},
		&research.GDELTScraper{Client: client, Country: "india", Timespan: "7d"},
		// GDELT's sourcecountry takes a country name, not a two-letter
		// code, as one token with no internal space -- see the comment on
		// gdelt-us-business in internal/news/catalog.go for what was and
		// was not confirmed live.
		&research.GDELTScraper{Client: client, Country: "unitedstates", Timespan: "7d"},
		// The publishers worth reading on Indian markets that will not serve
		// a search endpoint directly, reached by scoping discovery to their
		// domain. What comes back is a headline and a link to them.
		&research.PublisherScraper{Client: client, Domain: "reuters.com", Label: "reuters", TrustLevel: news.TrustWire},
		&research.PublisherScraper{Client: client, Domain: "bloomberg.com", Label: "bloomberg", TrustLevel: news.TrustWire},
		&research.PublisherScraper{Client: client, Domain: "business-standard.com", Label: "business_standard", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "moneycontrol.com", Label: "moneycontrol", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "livemint.com", Label: "mint", TrustLevel: news.TrustMajorFin},
		// The US publishers worth reading the same way: CNBC and MarketWatch
		// already have direct feeds in the ingestion catalog, but a research
		// question needs their archive, not just what they published this
		// week, which is what scoping discovery to their domain reaches.
		&research.PublisherScraper{Client: client, Domain: "cnbc.com", Label: "cnbc", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "marketwatch.com", Label: "marketwatch", TrustLevel: news.TrustMajorFin},
		&research.PublisherScraper{Client: client, Domain: "barrons.com", Label: "barrons", TrustLevel: news.TrustMajorFin},
	}

	// The Federal Register: every proposed and final rule, executive order
	// and agency notice the US government publishes, free and keyless --
	// unrelated to SEC and so not gated on SEC_USER_AGENT.
	scrapers = append(scrapers, &research.FederalRegisterScraper{Client: client})

	// SEC's own text, searched directly -- not a headline about a filing,
	// the filing itself. Gated on the same declared contact every other SEC
	// endpoint in this app requires; omitted rather than registered to fail
	// every call when SEC_USER_AGENT is unset.
	if cfg.SECUserAgent != "" {
		scrapers = append(scrapers, &research.SECFullTextScraper{Client: client, UserAgent: cfg.SECUserAgent})
	} else {
		log.Warn("SEC_USER_AGENT is not set; the SEC full-text research scraper is disabled")
	}

	// The private metasearch node, when one is running. Two configurations
	// of it rather than one: the news category is narrow and fresh, the
	// general web reaches sector reports, regulator pages and primary
	// documents that never appear in a news index. Asking both is cheap —
	// there is no per-query cost — and they return materially different
	// material for the same question.
	if u := cfg.SearXNGURL; u != "" {
		scrapers = append(scrapers,
			// Paged more deeply than the other scrapers, because these are
			// the results that can actually be read. SearXNG returns the
			// publisher's own URL; Google News returns an opaque token that
			// resolves to a JavaScript shim, so a research question that
			// leans on Google News gets forty headlines and no article text.
			&research.SearXNGScraper{
				Client: client, BaseURL: u, Label: "searxng_news",
				Categories: "news", TimeRange: "month", Language: "en", Pages: 5,
			},
			&research.SearXNGScraper{
				Client: client, BaseURL: u, Label: "searxng_web",
				Language: "en", Pages: 5,
			},
			// The same index without the recency filter. A question about a
			// company's history, a past regulatory action or a multi-year
			// trend is answered by material the month-bounded news query
			// cannot see at all.
			&research.SearXNGScraper{
				Client: client, BaseURL: u, Label: "searxng_archive",
				Categories: "news", Language: "en", Pages: 4,
			},
		)
	}

	// Commercial search, when configured. These index independently of the
	// feed-based scrapers, so what they add is coverage the curated catalog
	// cannot reach — and two providers agreeing on a fact is corroboration in
	// a way that two feeds carrying the same wire copy is not.
	if key := cfg.TavilyAPIKey; key != "" {
		scrapers = append(scrapers, &research.TavilyScraper{
			Client: client, APIKey: key, Depth: "basic",
		})
	}
	if key := cfg.BraveAPIKey; key != "" {
		scrapers = append(scrapers, &research.BraveScraper{
			Client: client, APIKey: key, Country: "IN", SearchLang: "en", Freshness: "pw",
		})
	}

	// Entity resolution runs over every finding, so a research result says
	// which listed companies it concerns rather than leaving the reader to
	// spot them. The threshold is high: a wrong symbol on a research answer
	// is a wrong answer.
	resolve := func(text string) []string {
		matches := master.ResolveAbove(text, 0.9)
		out := make([]string, 0, len(matches))
		for _, m := range matches {
			out = append(out, m.Symbol)
		}
		return out
	}

	// A question naming an industry gets the listed universe for it. No news
	// article enumerates the cement companies on the exchange; the industry
	// mapping does, for 752 of them, and without it a question about "which
	// companies" can only be answered by whichever ones happened to be in the
	// retrieved articles.
	universe := func(query string) []research.UniverseNote {
		var out []research.UniverseNote
		for _, industry := range master.IndustriesMentioned(query) {
			symbols := master.SymbolsInIndustry(industry)
			if len(symbols) == 0 {
				continue
			}
			// Capped: an industry with 121 members would otherwise crowd the
			// prompt out with a list nobody reads.
			if len(symbols) > 40 {
				symbols = symbols[:40]
			}
			out = append(out, research.UniverseNote{Industry: industry, Symbols: symbols})
		}
		return out
	}

	engine := research.NewEngine(scrapers,
		research.WithLogger(log),
		research.WithSymbolResolver(resolve),
		research.WithUniverseLookup(universe),
		// Historical and statistical questions are answered with arithmetic
		// over the price series rather than from prose about it. Routed
		// through the market data router so research inherits its provider
		// fallback, cache and single-flight rather than opening a second path
		// to the same upstreams.
		research.WithPrices(research.RouterPrices{
			Router: router, Exchange: marketdata.ExchangeNSE,
		}),
		// Reads the pages behind the results rather than working from their
		// headlines. This is the difference between a report and a list of
		// links: without it the model sees a title and a forty-word snippet,
		// and for Google News results the snippet is the title again.
		research.WithArticleFetcher(research.NewArticleFetcher()),
		// Fundamentals, already compared against the peer group. A question
		// about a company is incomplete without whether it is expensive, and
		// no amount of news coverage answers that.
		research.WithValuations(storeValuations{DB: store}))
	log.Info("research engine ready", "scrapers", len(scrapers), "prices", router != nil)
	return engine
}

// orphanRetentionDays is how long a raw item that produced no event is kept.
// Long enough that a parser fix can still reprocess a month of history, short
// enough that filtered noise does not accumulate indefinitely.
const orphanRetentionDays = 30

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

// buildRegistry assembles the static catalog plus the SEC filings tape, when
// SEC_USER_AGENT is configured. Left unset, SEC sources are omitted entirely
// rather than added and left to fail every request with a 403 -- SEC's
// fair-access policy requires a real contact string, not a generic one.
func buildRegistry(secUserAgent string) (*news.Registry, error) {
	sources := news.DefaultSources()
	if secUserAgent != "" {
		sources = append(sources, news.SECSources(secUserAgent)...)
	} else {
		slog.Warn("SEC_USER_AGENT is not set; the SEC filings tape (8-K/Form 4/13F) is disabled")
	}
	return news.NewRegistry(sources...)
}

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
) *cron.Cron {
	c := cron.New(cron.WithLocation(cfg.DisplayTZ))

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

	// News collection: hourly, and it needs no AI at all. The multi-source
	// ingestion engine polls far more often than this on its own per-source
	// cadence; this is the watchlist-driven sweep, which is a different job.
	add("news poll", "7 * * * *", func(runCtx context.Context) {
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
		nseScope := func(s marketdata.Symbol) bool { return s.IsIndian() }
		usScope := func(s marketdata.Symbol) bool { return !s.IsIndian() && !s.IsIndex() }
		scan := func(scope func(marketdata.Symbol) bool) func(context.Context) {
			return func(runCtx context.Context) {
				if _, err := marketScanner.Run(runCtx, scope); err != nil {
					log.Warn("market scan failed", "err", err)
				}
			}
		}
		add("market scan (nse close)", "CRON_TZ=Asia/Kolkata 45 15 * * 1-5", scan(nseScope))
		add("market scan (nse midday)", "CRON_TZ=Asia/Kolkata 30 12 * * 1-5", scan(nseScope))
		add("market scan (nse open)", "CRON_TZ=Asia/Kolkata 45 9 * * 1-5", scan(nseScope))
		add("market scan (us close)", "CRON_TZ=America/New_York 45 15 * * 1-5", scan(usScope))
		add("market scan (us midday)", "CRON_TZ=America/New_York 30 12 * * 1-5", scan(usScope))
		add("market scan (us open)", "CRON_TZ=America/New_York 45 9 * * 1-5", scan(usScope))
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

		// News scoring runs shortly after collection, on the cheap tier.
		add("news digest", "25 * * * *", func(runCtx context.Context) {
			if _, err := svc.RunNewsDigest(runCtx, 40); err != nil {
				log.Warn("scheduled news digest failed", "err", err)
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

		// Event classification, hourly. Storage hands these back
		// most-important-first, so a capped run spends the budget on what
		// matters instead of on whatever happened to arrive last.
		add("event classification", "40 * * * *", func(runCtx context.Context) {
			// Sized to the fifteen-minute job budget with three batches in
			// flight: enough to clear a day's collection in one pass rather
			// than falling permanently behind it.
			n, err := svc.ClassifyEvents(runCtx, 300)
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
