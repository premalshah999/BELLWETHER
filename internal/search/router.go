package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DefaultTTL is how long a search result stays usable.
//
// One hour, per the specification. News moves, but not so fast that repeating
// the same query five times in an afternoon is worth the API quota.
const DefaultTTL = time.Hour

// Router tries providers in order and caches what it gets.
type Router struct {
	providers []Provider
	cache     Cache
	ttl       time.Duration
	log       *slog.Logger
	now       func() time.Time
	observe   func(ctx context.Context, provider string, ok, skipped bool, err error)
}

// RouterOption configures a Router.
type RouterOption func(*Router)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) RouterOption { return func(r *Router) { r.log = l } }

// WithTTL overrides the cache lifetime.
func WithTTL(d time.Duration) RouterOption { return func(r *Router) { r.ttl = d } }

// WithClock replaces the time source.
func WithClock(now func() time.Time) RouterOption { return func(r *Router) { r.now = now } }

// WithObserver registers a health reporter, matching the tracker's Observe.
func WithObserver(f func(ctx context.Context, provider string, ok, skipped bool, err error)) RouterOption {
	return func(r *Router) { r.observe = f }
}

// NewRouter builds a search router. Providers are tried in the order given,
// which is how the configured preference falls back to the other vendor.
func NewRouter(cache Cache, providers []Provider, opts ...RouterOption) *Router {
	r := &Router{
		providers: providers,
		cache:     cache,
		ttl:       DefaultTTL,
		log:       slog.Default(),
		now:       func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Configured reports whether any provider has credentials.
func (r *Router) Configured() bool {
	for _, p := range r.providers {
		if p.Configured() {
			return true
		}
	}
	return false
}

// ProviderNames lists the configured providers in preference order.
func (r *Router) ProviderNames() []string {
	out := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		if p.Configured() {
			out = append(out, p.Name())
		}
	}
	return out
}

// cacheKey identifies a query. It hashes the normalised text so that
// punctuation and casing differences do not fragment the cache.
func cacheKey(q Query) string {
	normalised := strings.Join(strings.Fields(strings.ToLower(q.Text)), " ")
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", normalised, q.MaxResults, q.Days)))
	return hex.EncodeToString(sum[:16])
}

// Search runs a query, preferring fresh cache and falling back across
// providers.
func (r *Router) Search(ctx context.Context, q Query) (Results, error) {
	if strings.TrimSpace(q.Text) == "" {
		return Results{}, fmt.Errorf("search: empty query")
	}
	if q.MaxResults <= 0 {
		q.MaxResults = 5
	}

	key := cacheKey(q)
	if r.cache != nil {
		cached, ok, err := r.cache.LoadSearch(ctx, key)
		if err != nil {
			r.log.Warn("search cache read failed", "err", err)
		} else if ok && r.now().Sub(cached.FetchedAt) < r.ttl {
			cached.Cached = true
			return cached, nil
		}
	}

	var errs []error
	for _, p := range r.providers {
		if !p.Configured() {
			continue
		}
		results, err := p.Search(ctx, q)
		if err != nil {
			skipped := errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrNoResults)
			r.report(ctx, p.Name(), false, skipped, err)
			if !skipped {
				r.log.Warn("search provider failed", "provider", p.Name(), "err", err)
			}
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		if len(results) == 0 {
			r.report(ctx, p.Name(), true, false, nil)
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), ErrNoResults))
			continue
		}

		r.report(ctx, p.Name(), true, false, nil)
		out := Results{
			Query: q.Text, Results: results,
			Provider: p.Name(), FetchedAt: r.now(),
		}
		if r.cache != nil {
			if err := r.cache.SaveSearch(ctx, key, out); err != nil {
				r.log.Warn("search cache write failed", "err", err)
			}
		}
		return out, nil
	}

	// Every provider declined. Stale cache beats nothing for a feature that
	// only decorates an answer.
	if r.cache != nil {
		if cached, ok, err := r.cache.LoadSearch(ctx, key); err == nil && ok && len(cached.Results) > 0 {
			cached.Cached = true
			r.log.Info("serving stale search results", "query", q.Text)
			return cached, nil
		}
	}
	if len(errs) == 0 {
		return Results{}, ErrNotConfigured
	}
	return Results{}, fmt.Errorf("search failed for %q: %w", q.Text, errors.Join(errs...))
}

func (r *Router) report(ctx context.Context, provider string, ok, skipped bool, err error) {
	if r.observe != nil {
		r.observe(ctx, provider, ok, skipped, err)
	}
}
