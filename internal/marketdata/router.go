package marketdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Router is the only market data entry point the rest of the app uses. It
// turns a list of unreliable providers plus a cache into a source that answers
// something useful almost always.
//
// The policy, in order:
//
//  1. If the cache holds data younger than the interval's TTL, serve it and
//     make no network call at all. This is what keeps Alpha Vantage inside a
//     25-request day.
//  2. Otherwise try each provider in priority order. The first usable answer
//     is written through to the cache and returned.
//  3. If every provider declines or fails, serve whatever the cache has, no
//     matter how old, flagged Stale so the UI can say so.
//  4. Only when there is nothing cached at all does a request fail.
//
// Step 3 is the reason the dashboard degrades to a banner instead of a white
// screen when Yahoo starts rate-limiting.
type Router struct {
	providers []Provider
	cache     Cache
	sink      OutcomeSink
	log       *slog.Logger
	now       func() time.Time
	ttl       map[Interval]time.Duration

	// single-flights concurrent fetches of the same key so that N watchlist
	// tiles refreshing at once cost one upstream call, not N.
	mu     sync.Mutex
	flight map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val Series
	err error
}

// RouterOption configures a Router.
type RouterOption func(*Router)

// WithOutcomeSink registers a receiver for per-provider attempt outcomes.
func WithOutcomeSink(s OutcomeSink) RouterOption { return func(r *Router) { r.sink = s } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) RouterOption { return func(r *Router) { r.log = l } }

// WithClock replaces the time source, used by tests to age the cache.
func WithClock(now func() time.Time) RouterOption { return func(r *Router) { r.now = now } }

// WithTTL overrides the cache lifetime for one interval.
func WithTTL(iv Interval, d time.Duration) RouterOption {
	return func(r *Router) { r.ttl[iv] = d }
}

// defaultTTLs balance freshness against provider budgets.
//
// These are the lifetimes for a *finished* bar. While a venue is trading, the
// newest bar of every interval is still forming and none of these apply — see
// liveTTL and ttlForSymbol. Treating a forming daily bar as immutable for six
// hours is what made a chart loaded at the open still show the opening price
// at three in the afternoon, with less than half the day's real volume.
func defaultTTLs() map[Interval]time.Duration {
	return map[Interval]time.Duration{
		// Minute bars are the live quote path during a session, so this is
		// the real ceiling on how fresh a streamed price can be. Kept just
		// under the bar interval: a full minute would mean the newest bar is
		// always one refresh behind.
		Interval1m:  45 * time.Second,
		Interval5m:  5 * time.Minute,
		Interval15m: 10 * time.Minute,
		Interval1h:  30 * time.Minute,
		Interval1d:  6 * time.Hour,
		Interval1wk: 24 * time.Hour,
	}
}

// liveTTL is the cache lifetime for any series whose newest bar is still
// forming, regardless of interval.
//
// Sixty seconds is a deliberate ceiling rather than a tuning knob: the
// front end polls a visible chart every sixty seconds, so anything longer
// means the poll returns the same bytes and the chart visibly stops moving
// while the market does not.
const liveTTL = time.Minute

// QuoteTTL is how long a cached quote is considered live. Quotes are the most
// visible number on the dashboard, so this is short.
const QuoteTTL = 2 * time.Minute

// liveQuoteTTL applies while the venue is trading.
//
// Set below the front end's sixty-second poll on purpose. At two minutes every
// second poll returns the same bytes, so a watchlist price sits visibly
// motionless for a minute at a time while the market moves — which reads as a
// broken app, and during a session is indistinguishable from one.
const liveQuoteTTL = 45 * time.Second

// NewRouter builds a Router. Providers are tried in the order given.
func NewRouter(cache Cache, providers []Provider, opts ...RouterOption) *Router {
	r := &Router{
		providers: providers,
		cache:     cache,
		log:       slog.Default(),
		now:       func() time.Time { return time.Now().UTC() },
		ttl:       defaultTTLs(),
		flight:    map[string]*call{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ProviderNames lists the configured providers in priority order.
func (r *Router) ProviderNames() []string {
	out := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Name())
	}
	return out
}

// provisional reports that a cached series was captured while its newest bar
// was still being written, and that the session has since ended.
//
// A lifetime alone cannot express this. Whatever was cached at midday is a
// snapshot of a half-finished day: the wrong close, the wrong high and low,
// and roughly half the real volume. Once trading stops that bar is not merely
// stale, it is wrong, and serving it for the rest of the interval's lifetime
// writes a mid-session guess into the permanent daily record.
func (r *Router) provisional(sym Symbol, fetchedAt time.Time) bool {
	if fetchedAt.IsZero() {
		return false
	}
	return sym.Exchange.SessionActive(fetchedAt) && !sym.Exchange.SessionActive(r.now())
}

// quoteTTL is how long this venue's last quote may be reused.
func (r *Router) quoteTTL(sym Symbol) time.Duration {
	if sym.Exchange.SessionActive(r.now()) {
		return liveQuoteTTL
	}
	return QuoteTTL
}

// ttlForSymbol is the cache lifetime for one venue's series.
//
// While that venue is trading, the newest bar is incomplete and the interval's
// own lifetime is meaningless — a bar that is still being written cannot be
// "cached until it changes", because it is changing.
func (r *Router) ttlForSymbol(sym Symbol, iv Interval) time.Duration {
	base := r.ttlFor(iv)
	if base <= liveTTL {
		return base
	}
	if sym.Exchange.SessionActive(r.now()) {
		return liveTTL
	}
	return base
}

func (r *Router) ttlFor(iv Interval) time.Duration {
	if d, ok := r.ttl[iv]; ok {
		return d
	}
	return time.Hour
}

func (r *Router) report(ctx context.Context, o Outcome) {
	if r.sink != nil {
		r.sink(ctx, o)
	}
}

// isRoutineDecline reports whether a provider's error means "not me" rather
// than "I am broken".
//
// The distinction drives health reporting, and getting it wrong is visible:
// Twelve Data's free tier does not carry Indian listings, so every RELIANCE
// lookup returns a 404. That is the provider working correctly and saying it
// has nothing — not a fault — and counting it as one turns its status dot red
// for an operator whose only mistake was watching an Indian stock.
func isRoutineDecline(err error) bool {
	return errors.Is(err, ErrNotSupported) ||
		errors.Is(err, ErrBudgetExhausted) ||
		errors.Is(err, ErrNoData)
}

// Candles returns a series for a symbol, preferring fresh cache, then live
// providers, then stale cache.
func (r *Router) Candles(ctx context.Context, sym Symbol, iv Interval, limit int) (Series, error) {
	if limit <= 0 {
		limit = 200
	}
	key := fmt.Sprintf("candles|%s|%s|%d", sym, iv, limit)

	r.mu.Lock()
	if c, ok := r.flight[key]; ok {
		r.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call{}
	c.wg.Add(1)
	r.flight[key] = c
	r.mu.Unlock()

	c.val, c.err = r.candles(ctx, sym, iv, limit)

	r.mu.Lock()
	delete(r.flight, key)
	r.mu.Unlock()
	c.wg.Done()

	return c.val, c.err
}

func (r *Router) candles(ctx context.Context, sym Symbol, iv Interval, limit int) (Series, error) {
	cached, err := r.cache.LoadCandles(ctx, sym, iv, limit)
	if err != nil {
		// A broken cache must not stop us from going to the network.
		r.log.Warn("cache read failed", "symbol", sym, "interval", iv, "err", err)
	}
	// A cache hit needs two things: the data must be fresh, and it must go
	// back far enough to answer this request. Coverage is satisfied either by
	// holding at least as many bars as were asked for, or by a previous fetch
	// having already asked for at least this many and been given fewer —
	// which means the provider has no more to give.
	fresh := len(cached.Candles) > 0 &&
		r.now().Sub(cached.FetchedAt) < r.ttlForSymbol(sym, iv) &&
		!r.provisional(sym, cached.FetchedAt)
	covers := len(cached.Candles) >= limit || cached.RequestedLimit >= limit
	if fresh && covers {
		return Series{
			Symbol: sym, Interval: iv, Candles: cached.Candles,
			Source: cached.Source, ResolvedSymbol: cached.ResolvedSymbol,
			FetchedAt: cached.FetchedAt,
		}, nil
	}

	var errs []error
	for _, p := range r.providers {
		bars, perr := p.Candles(ctx, sym, iv, limit)
		if perr != nil {
			skipped := isRoutineDecline(perr)
			r.report(ctx, Outcome{Provider: p.Name(), Skipped: skipped, Err: perr})
			if skipped {
				r.log.Debug("provider declined", "provider", p.Name(), "symbol", sym, "reason", perr)
			} else {
				r.log.Warn("provider failed", "provider", p.Name(), "symbol", sym, "err", perr)
			}
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), perr))
			continue
		}
		if len(bars.Candles) == 0 {
			r.report(ctx, Outcome{Provider: p.Name(), Skipped: true, Err: ErrNoData})
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), ErrNoData))
			continue
		}

		r.report(ctx, Outcome{Provider: p.Name(), OK: true})
		if bars.ResolvedSymbol != "" {
			r.log.Info("provider substituted a listing",
				"provider", p.Name(), "requested", sym, "served", bars.ResolvedSymbol)
		}
		if err := r.cache.SaveCandles(ctx, sym, iv, p.Name(), bars, limit); err != nil {
			// Losing the write-through is bad for the next request but must
			// not spoil this one.
			r.log.Error("cache write failed", "symbol", sym, "interval", iv, "err", err)
		}
		return Series{
			Symbol: sym, Interval: iv, Candles: bars.Candles,
			Source: p.Name(), ResolvedSymbol: bars.ResolvedSymbol, FetchedAt: r.now(),
		}, nil
	}

	// Everything upstream declined. Stale data beats no data, as long as we
	// say it is stale.
	if len(cached.Candles) > 0 {
		r.log.Warn("serving stale candles", "symbol", sym, "interval", iv,
			"age", r.now().Sub(cached.FetchedAt).Truncate(time.Second), "errors", errors.Join(errs...))
		return Series{
			Symbol: sym, Interval: iv, Candles: cached.Candles,
			Source: cached.Source, ResolvedSymbol: cached.ResolvedSymbol,
			FetchedAt: cached.FetchedAt, Stale: true,
		}, nil
	}
	if len(errs) == 0 {
		return Series{}, fmt.Errorf("%w: no market data providers are configured", ErrNoData)
	}
	return Series{}, fmt.Errorf("no data for %s at %s: %w", sym, iv, errors.Join(errs...))
}

// QuoteResult is a quote plus its provenance.
type QuoteResult struct {
	Quote     Quote     `json:"quote"`
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetched_at"`
	Stale     bool      `json:"stale"`
}

// Quote returns the latest price, following the same cache-then-providers-then-
// stale-cache policy as Candles.
func (r *Router) Quote(ctx context.Context, sym Symbol) (QuoteResult, error) {
	cached, err := r.cache.LoadQuote(ctx, sym)
	if err != nil {
		r.log.Warn("quote cache read failed", "symbol", sym, "err", err)
	}
	if !cached.FetchedAt.IsZero() && r.now().Sub(cached.FetchedAt) < r.quoteTTL(sym) {
		return QuoteResult{Quote: cached.Quote, Source: cached.Source, FetchedAt: cached.FetchedAt}, nil
	}

	var errs []error
	for _, p := range r.providers {
		q, perr := p.Quote(ctx, sym)
		if perr != nil {
			r.report(ctx, Outcome{Provider: p.Name(), Skipped: isRoutineDecline(perr), Err: perr})
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), perr))
			continue
		}
		q.Symbol = sym
		if q.Currency == "" {
			q.Currency = sym.Currency()
		}
		r.report(ctx, Outcome{Provider: p.Name(), OK: true})
		if err := r.cache.SaveQuote(ctx, p.Name(), q); err != nil {
			r.log.Error("quote cache write failed", "symbol", sym, "err", err)
		}
		return QuoteResult{Quote: q, Source: p.Name(), FetchedAt: r.now()}, nil
	}

	if !cached.FetchedAt.IsZero() {
		return QuoteResult{Quote: cached.Quote, Source: cached.Source, FetchedAt: cached.FetchedAt, Stale: true}, nil
	}
	if len(errs) == 0 {
		return QuoteResult{}, fmt.Errorf("%w: no market data providers are configured", ErrNoData)
	}
	return QuoteResult{}, fmt.Errorf("no quote for %s: %w", sym, errors.Join(errs...))
}
