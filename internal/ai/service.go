package ai

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/jev"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/search"
)

// MarketSource supplies prices to the AI features. The market data router
// satisfies it, so AI features read through the same cache as everything else
// and never blow the provider budget on their own.
type MarketSource interface {
	Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Series, error)
	Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.QuoteResult, error)
}

// NewsSource supplies collected articles.
type NewsSource interface {
	ListArticles(ctx context.Context, symbol string, limit int) ([]news.Article, error)
}

// SearchSource supplies web results for sourced explanations.
type SearchSource interface {
	Configured() bool
	Search(ctx context.Context, q search.Query) (search.Results, error)
}

// WatchlistSource supplies the symbols the morning brief covers.
type WatchlistSource interface {
	WatchedSymbols(ctx context.Context) ([]marketdata.Symbol, error)
}

// Service is the AI feature layer: everything built on top of the client.
type Service struct {
	researchMu      sync.Mutex
	researchActive  map[int64]bool
	researchJobs    int
	researchContext context.Context

	client *Client
	// jev makes decisions when configured: event classification goes to it
	// instead of the text model. Nil or unconfigured leaves the text model
	// doing both, as before.
	jev       *jev.Client
	market    MarketSource
	news      NewsSource
	search    SearchSource
	watchlist WatchlistSource
	alerts    AlertSource
	scores    ScoreStore
	store     OutputStore
	loc       *time.Location
	log       *slog.Logger
	now       func() time.Time

	// researchModel and researchEffort write deep-research briefs.
	researchModel  string
	researchEffort string
}

// ServiceOption configures a Service.
type ServiceOption func(*Service)

// WithResearchModel sets the model and reasoning effort for deep research.
func WithResearchModel(model, effort string) ServiceOption {
	return func(s *Service) { s.researchModel, s.researchEffort = model, effort }
}

// WithServiceLogger sets the logger.
func WithServiceLogger(l *slog.Logger) ServiceOption { return func(s *Service) { s.log = l } }

// WithServiceClock replaces the time source.
func WithServiceClock(now func() time.Time) ServiceOption {
	return func(s *Service) { s.now = now }
}

// WithSearch attaches a search provider for sourced explanations.
func WithSearch(src SearchSource) ServiceOption { return func(s *Service) { s.search = src } }

// WithNews attaches the collected-article store.
func WithNews(src NewsSource) ServiceOption { return func(s *Service) { s.news = src } }

// WithWatchlist attaches the watchlist for the morning brief.
func WithWatchlist(src WatchlistSource) ServiceOption {
	return func(s *Service) { s.watchlist = src }
}

func WithResearchContext(ctx context.Context) ServiceOption {
	return func(s *Service) { s.researchContext = ctx }
}

// NewService builds the AI feature layer.
func NewService(client *Client, market MarketSource, store OutputStore, loc *time.Location, opts ...ServiceOption) *Service {
	if loc == nil {
		loc = time.UTC
	}
	s := &Service{
		researchActive: map[int64]bool{}, researchContext: context.Background(),
		client: client,
		market: market,
		store:  store,
		loc:    loc,
		log:    slog.Default(),
		now:    func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Client exposes the underlying LLM client for status and budget reporting.
func (s *Service) Client() *Client { return s.client }

// Available reports whether AI features can run right now, and why not.
func (s *Service) Available(ctx context.Context) Status {
	if s.client == nil {
		return StatusUnconfigured
	}
	return s.client.Status(ctx)
}

// guard is the check every feature runs first. It converts an unavailable AI
// layer into a Status the UI can render, rather than an error that looks like
// a bug.
func (s *Service) guard(ctx context.Context) (Status, error) {
	switch status := s.Available(ctx); status {
	case StatusOK:
		return StatusOK, nil
	case StatusUnconfigured:
		return status, ErrNotConfigured
	case StatusBudgetReached:
		return status, ErrBudgetExhausted
	default:
		return status, ErrNotConfigured
	}
}
