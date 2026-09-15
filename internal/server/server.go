// Package server exposes the REST API and serves the built frontend.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/health"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/research"
	"github.com/tradesys/dashboard/internal/scanner"
	"github.com/tradesys/dashboard/internal/storage"
	"github.com/tradesys/dashboard/internal/stream"
)

// standardRequestTimeout bounds an ordinary request: cache reads and single
// upstream calls.
const standardRequestTimeout = 30 * time.Second

// aiRequestTimeout bounds an AI-backed request. Generous, because a reasoning
// model's thinking time counts against it and a brief covering a whole
// watchlist is a genuinely long call.
const aiRequestTimeout = 5 * time.Minute

// Disclaimer is rendered in the footer of every page. It is served from the
// backend so there is exactly one copy of the wording.
const Disclaimer = "Analysis tool. Not investment advice. Data may be delayed."

// BudgetReporter is any provider that can report its own consumption, so the
// settings page and the top bar meter can show it.
type BudgetReporter interface {
	Usage(ctx context.Context) (used, limit int, err error)
}

// Deps is everything the server needs, all as interfaces so handlers can be
// tested without a database or a network.
type Deps struct {
	Config  *config.Config
	Store   storage.Store
	Router  *marketdata.Router
	Health  *health.Tracker
	Budgets map[string]BudgetReporter
	// Evaluator powers the builder's preview, which never notifies.
	Evaluator *algo.Evaluator
	// Engine runs the real alert pipeline behind "run now".
	Engine *alerts.Engine
	// Scheduler is read here only to report schedules on the settings page.
	Scheduler *alerts.Scheduler
	// AI is the feature layer. Nil when no LLM is configured, and every
	// handler must cope with that rather than assuming it exists.
	AI *ai.Service
	// NewsPoller backs the manual "collect news now" action.
	NewsPoller *news.Poller
	// Ingest is the multi-source fetch engine. Read here only to report
	// source health; the engine schedules itself.
	Ingest *news.Engine
	// Research runs on-demand multi-source searches. Nil when no scrapers
	// are configured, in which case the endpoint reports itself unavailable.
	Research *research.Engine
	// Attention is the heat tracker, read here only to report what the
	// scheduler currently considers eventful.
	Attention *news.Tracker
	// Companies is the listed master, read here to name an instrument and
	// its industry. Nil in tests that do not need it.
	Companies *company.Master
	// Processor turns collected items into events. Read here only so a
	// manual refresh can complete the whole cycle rather than collecting
	// items the page cannot yet show.
	Processor *events.Processor

	// Scanner finds instruments behaving abnormally. Optional: without a
	// price service there is nothing to scan, and the routes report that
	// rather than pretending to have scanned nothing.
	Scanner *scanner.Runner

	// Stream fans live updates out to connected clients. Optional: without
	// it the stream routes report themselves unavailable and the front end
	// falls back to polling, which is what it did before.
	// SessionSecret signs session cookies. Empty generates a random one,
	// which is secure but does not survive a restart.
	SessionSecret string

	Stream *stream.Hub
	// StreamPumps are the running sources, exposed so their health can be
	// reported. A connected-but-silent feed is the failure that matters.
	StreamPumps []*stream.Pump
	// Searcher finds instruments by name. Nil when no provider supports it,
	// in which case the UI falls back to typing an exact symbol.
	Searcher marketdata.SymbolSearcher
	Log      *slog.Logger
	Version  string
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// Server wires the API and the static frontend into one handler.
type Server struct {
	deps Deps
	mux  *chi.Mux
	// aiLimiter bounds what a loop against the model-backed routes can spend.
	aiLimiter *rateLimiter
	// sessionSecret signs session cookies.
	sessionSecret []byte
}

// New builds the server and its routes.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.Budgets == nil {
		d.Budgets = map[string]BudgetReporter{}
	}
	secret := []byte(d.SessionSecret)
	if len(secret) == 0 {
		secret = newSessionSecret()
	}
	s := &Server{
		deps:          d,
		sessionSecret: secret,
		mux:           chi.NewRouter(),
		// Twelve model-backed calls a minute per caller. A person clicking
		// through a page generates two or three; a loop generates thousands,
		// and each one costs tokens from a fixed monthly budget.
		aiLimiter: newRateLimiter(12, time.Minute, d.Log),
	}
	// Idle callers are swept so the map does not grow once per address
	// forever. Cheap enough that a plain ticker is the whole mechanism.
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			s.aiLimiter.sweep()
		}
	}()
	s.routes()
	return s
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.Use(middleware.RequestID)
	s.mux.Use(requestLogger(s.deps.Log))
	s.mux.Use(recoverer(s.deps.Log))
	// Deliberately no timeout here. A timeout applied at this level wraps
	// every route below it, and an inner, longer timeout cannot extend an
	// outer, shorter one — so the AI routes would silently keep the 30-second
	// budget no matter what they asked for. Each group sets its own instead.

	s.mux.Route("/api", func(r chi.Router) {
		// Everything below this line requires a session, except the login
		// route and the health check. The middleware is registered here, at
		// the top of the API tree, so a route added later is protected by
		// default rather than by remembering to protect it.
		r.Use(s.requireAuth)

		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)
		r.Get("/auth/status", s.handleAuthStatus)

		// The live stream sits outside every timeout group on purpose.
		//
		// A server-sent event connection is long-lived by definition: it is
		// open for as long as the browser tab is. Wrapping it in a request
		// timeout — even a generous one — kills the stream on a fixed
		// schedule and produces a reconnect storm across every client at
		// once, which looks exactly like an upstream outage and is not one.
		r.Get("/stream", s.handleStream)

		r.Group(func(r chi.Router) {
			// Everything that answers from cache or a single upstream call.
			r.Use(middleware.Timeout(standardRequestTimeout))

			r.Get("/meta", s.handleMeta)
			r.Get("/health", s.handleHealth)
			r.Get("/ingest/sources", s.handleIngestSources)
			r.Get("/ingest/stats", s.handleStorageStats)
			r.Get("/ingest/pipeline", s.handlePipeline)

			// The news and filings feed.
			r.Get("/events", s.handleEvents)
			r.Get("/events/types", s.handleEventTypes)
			r.Post("/events/{id}/brief", s.handleEventBrief)
			r.Get("/events/{id}", s.handleEvent)
			r.Get("/symbols/{symbol}/events", s.handleSymbolEvents)
			// A manual refresh reaches an upstream publisher, so it is
			// limited too — not to protect the token budget, but to keep a
			// stuck client from hammering a source on our behalf.
			r.With(s.aiLimiter.middleware).
				Post("/symbols/{symbol}/news/refresh", s.handleRefreshSymbolNews)

			r.Delete("/ai/outputs/{id}", s.handleDeleteOutput)
			r.Delete("/ai/outputs", s.handleClearOutputs)
			r.Delete("/ai/outlooks/{id}", s.handleDeleteOutlook)
			r.Get("/research/scrapers", s.handleResearchScrapers)
			r.Get("/research/conversations", s.handleConversations)
			r.Get("/research/conversations/{id}", s.handleConversation)
			r.Delete("/research/conversations/{id}", s.handleDeleteConversation)

			// Named lists. The older singular routes stay where they are so
			// nothing that used them breaks; they now operate on the first
			// list.
			r.Post("/algorithms/backtest", s.handleBacktest)
			r.Post("/algorithms/{id}/backtest", s.handleBacktestSaved)

			r.Route("/screens", func(r chi.Router) {
				// Fields before the id routes: chi would otherwise match
				// "fields" as an {id} and answer a catalogue request with
				// "that is not a screen id".
				r.Get("/fields", s.handleScreenFields)
				r.Get("/", s.handleListScreens)
				r.Post("/", s.handleCreateScreen)
				r.Post("/run", s.handleRunScreen)
				r.Put("/{id}", s.handleUpdateScreen)
				r.Delete("/{id}", s.handleDeleteScreen)
				r.Post("/{id}/run", s.handleRunSavedScreen)
			})

			r.Route("/watchlists", func(r chi.Router) {
				r.Get("/", s.handleListWatchlists)
				r.Post("/", s.handleCreateWatchlist)
				r.Put("/{id}", s.handleRenameWatchlist)
				r.Delete("/{id}", s.handleDeleteWatchlist)
				r.Get("/{id}/symbols", s.handleWatchlistItems)
				r.Post("/{id}/symbols", s.handleAddToWatchlist)
				r.Delete("/{id}/symbols/{symbol}", s.handleRemoveFromWatchlist)
				r.Post("/{id}/copy", s.handleCopyWatchlist)
			})

			r.Route("/watchlist", func(r chi.Router) {
				r.Get("/", s.handleWatchlist)
				r.Post("/", s.handleWatchlistAdd)
				r.Delete("/{symbol}", s.handleWatchlistRemove)
			})

			// Declared before the {symbol} routes so "search" and "sectors"
			// are never parsed as a ticker.
			r.Get("/symbols/search", s.handleSearchSymbols)
			r.Get("/symbols/sectors", s.handleSymbolSectors)

			r.Route("/symbols/{symbol}", func(r chi.Router) {
				r.Get("/candles", s.handleCandles)
				r.Get("/quote", s.handleQuote)
				r.Get("/outlooks", s.handleListOutlooks)
			})

			r.Route("/algorithms", func(r chi.Router) {
				// Static sub-routes are declared before the {id} routes so
				// "templates" is never parsed as an identifier.
				r.Get("/templates", s.handleAlgorithmTemplates)
				r.Get("/vocabulary", s.handleAlgorithmVocabulary)
				r.Post("/validate", s.handleValidateAlgorithm)
				r.Post("/preview", s.handlePreviewAlgorithm)

				r.Get("/", s.handleListAlgorithms)
				r.Post("/", s.handleCreateAlgorithm)
				r.Get("/{id}", s.handleGetAlgorithm)
				r.Put("/{id}", s.handleUpdateAlgorithm)
				r.Delete("/{id}", s.handleDeleteAlgorithm)
				r.Post("/{id}/run", s.handleRunAlgorithm)
				r.Get("/{id}/evaluations", s.handleAlgorithmEvaluations)
			})

			r.Route("/alerts", func(r chi.Router) {
				r.Get("/", s.handleListAlerts)
				r.Post("/read-all", s.handleMarkAllAlertsRead)
				r.Post("/{id}/read", s.handleMarkAlertRead)
			})

			r.Route("/ai", func(r chi.Router) {
				r.Get("/status", s.handleAIStatus)
				r.Get("/brief", s.handleMorningBrief)
				r.Get("/briefs", s.handleBriefArchive)
				r.Get("/calibration", s.handleCalibration)
				r.Get("/outlooks", s.handleListOutlooks)
				r.Post("/outlooks/resolve", s.handleResolveOutlooks)
			})

			r.Get("/news", s.handleListNews)

			r.Get("/symbols/{symbol}/fundamentals", s.handleFundamentals)
			r.Get("/stream/health", s.handleStreamHealth)
			r.Post("/stream/test", s.handleStreamTest)

			r.Get("/scan/latest", s.handleLatestScan)
			r.Get("/scan/symbols/{symbol}", s.handleSymbolScanHistory)
		})

		// A reasoning model thinks before it answers, and a morning brief over
		// a full watchlist legitimately takes minutes. The default 30-second
		// timeout would cut those off mid-flight and bill the tokens anyway.
		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(aiRequestTimeout))
			// Every route in this group spends tokens from a fixed monthly
			// budget. Twelve calls a minute is far more than a person
			// generates and far less than a loop would — and the limit stands
			// even behind authentication, because a signed-in operator with a
			// stuck refresh loop spends the budget just as fast as a stranger.
			r.Use(s.aiLimiter.middleware)

			r.Post("/symbols/{symbol}/explain", s.handleExplainMove)
			r.Post("/symbols/{symbol}/debrief", s.handleSymbolDebrief)
			r.Post("/symbols/{symbol}/outlook", s.handleGenerateOutlook)
			r.Post("/ai/brief/generate", s.handleGenerateBrief)
			r.Post("/ai/calc", s.handleCalcHelper)
			r.Post("/ai/digest/run", s.handleRunDigest)
			r.Post("/ai/classify-events", s.handleClassifyEvents)
			r.Post("/ai/briefs/run", s.handleRunBriefs)
			r.Post("/news/poll", s.handleRunNewsPoll)

			// The scan spends no tokens, but it fans out across seven hundred
			// instruments against somebody else's service and takes about two
			// minutes. It belongs in the limited group for courtesy rather
			// than for cost.
			r.Post("/scan/run", s.handleRunScan)

			// Ask returns as soon as the turn row exists, so it needs none of
			// this group's timeout — but it belongs here for the limiter.
			// The work it starts continues in the background and spends
			// tokens exactly like the rest, and being fast to return makes it
			// the easiest of all of them to call in a loop.
			r.Post("/research/ask", s.handleAsk)
		})

		// The SPA fallback below must never swallow an unmatched API path:
		// a typo'd endpoint has to fail as JSON, not as a 200 of HTML that
		// the frontend would try to parse.
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "No such API endpoint.")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "That method is not allowed on this endpoint.")
		})
	})

	// Anything not under /api is the single-page app.
	s.mux.NotFound(s.staticHandler())
}
