// Package fixture provides a deterministic marketdata.Provider.
//
// It exists for two reasons. Tests need a provider whose failures they can
// script, and operators need to be able to bring the app up and see it work
// before they have gone and collected API keys. The data it generates is a
// seeded random walk — plausible in shape, entirely fictional in value — and
// anything served from it is labelled as such all the way to the UI.
package fixture

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// ProviderName is the identifier that appears in health and provenance. It is
// deliberately blunt: nobody should mistake this for market data.
const ProviderName = "synthetic"

// Provider generates reproducible candles for any symbol.
type Provider struct {
	name string
	now  func() time.Time

	mu sync.Mutex
	// failures scripts errors for tests: the next call for a symbol returns
	// the queued error instead of data.
	failures map[string][]error
	// calls counts requests per symbol so tests can assert on fallback order.
	calls map[string]int
}

// Option configures a Provider.
type Option func(*Provider)

// WithName overrides the provider name, letting a test stand up several
// distinguishable providers.
func WithName(n string) Option { return func(p *Provider) { p.name = n } }

// WithClock replaces the time source.
func WithClock(now func() time.Time) Option { return func(p *Provider) { p.now = now } }

// New builds a synthetic provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		name:     ProviderName,
		now:      func() time.Time { return time.Now().UTC() },
		failures: map[string][]error{},
		calls:    map[string]int{},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name identifies this provider.
func (p *Provider) Name() string { return p.name }

// FailNext queues errors to be returned by the next calls for a symbol, in
// order. Used by tests to script provider outages.
func (p *Provider) FailNext(sym marketdata.Symbol, errs ...error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures[sym.String()] = append(p.failures[sym.String()], errs...)
}

// FailAlways makes every subsequent call for a symbol return err.
func (p *Provider) FailAlways(sym marketdata.Symbol, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A large queue is simpler than a separate always-fail flag and is more
	// than any test will exhaust.
	for i := 0; i < 1024; i++ {
		p.failures[sym.String()] = append(p.failures[sym.String()], err)
	}
}

// Calls reports how many requests this provider has received for a symbol.
func (p *Provider) Calls(sym marketdata.Symbol) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[sym.String()]
}

func (p *Provider) take(sym marketdata.Symbol) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := sym.String()
	p.calls[k]++
	if q := p.failures[k]; len(q) > 0 {
		err := q[0]
		p.failures[k] = q[1:]
		return err
	}
	return nil
}

// seedFor derives a stable seed from the symbol so the same symbol always
// produces the same series, across processes and restarts.
func seedFor(sym marketdata.Symbol) int64 {
	var h int64 = 1469598103934665603
	for _, b := range []byte(sym.String()) {
		h ^= int64(b)
		h *= 1099511628211
	}
	if h < 0 {
		h = -h
	}
	return h
}

// basePrice is a plausible dollar price, so charts look right at a glance.
func basePrice(sym marketdata.Symbol) float64 { return 40 + float64(seedFor(sym)%400) }

// Candles generates a seeded random walk ending at the most recent bar
// boundary.
func (p *Provider) Candles(ctx context.Context, sym marketdata.Symbol, iv marketdata.Interval, limit int) (marketdata.Bars, error) {
	if err := p.take(sym); err != nil {
		return marketdata.Bars{}, err
	}
	if err := ctx.Err(); err != nil {
		return marketdata.Bars{}, err
	}
	if limit <= 0 {
		limit = 200
	}

	rng := rand.New(rand.NewSource(seedFor(sym)))
	price := basePrice(sym)
	step := iv.Duration()
	// Anchor to a bar boundary so repeated calls line up rather than drifting.
	end := p.now().UTC().Truncate(step)

	out := make([]marketdata.Candle, 0, limit)
	for i := limit - 1; i >= 0; i-- {
		// Daily volatility of roughly 1.5%, scaled by interval length.
		vol := 0.015 * math.Sqrt(step.Hours()/24)
		drift := (rng.Float64() - 0.48) * vol * price
		open := price
		closeP := math.Max(0.01, open+drift)
		high := math.Max(open, closeP) * (1 + rng.Float64()*vol*0.6)
		low := math.Min(open, closeP) * (1 - rng.Float64()*vol*0.6)
		volume := math.Round(1e6 * (0.6 + rng.Float64()))

		out = append(out, marketdata.Candle{
			Time:   end.Add(-time.Duration(i) * step),
			Open:   round2(open),
			High:   round2(high),
			Low:    round2(low),
			Close:  round2(closeP),
			Volume: volume,
		})
		price = closeP
	}
	return marketdata.Bars{Candles: marketdata.SortCandles(out)}, nil
}

// Quote derives the latest price from the generated series so the quote and
// the chart always agree.
func (p *Provider) Quote(ctx context.Context, sym marketdata.Symbol) (marketdata.Quote, error) {
	bars, err := p.Candles(ctx, sym, marketdata.Interval1d, 2)
	if err != nil {
		return marketdata.Quote{}, err
	}
	candles := bars.Candles
	if len(candles) < 2 {
		return marketdata.Quote{}, fmt.Errorf("%w: synthetic series too short for %s", marketdata.ErrNoData, sym)
	}
	last, prev := candles[len(candles)-1], candles[len(candles)-2]
	q := marketdata.Quote{
		Symbol:    sym,
		Price:     last.Close,
		PrevClose: prev.Close,
		DayHigh:   last.High,
		DayLow:    last.Low,
		Volume:    last.Volume,
		Currency:  sym.Currency(),
		AsOf:      p.now().UTC(),
	}
	q.Change = q.Price - q.PrevClose
	if q.PrevClose != 0 {
		q.ChangePercent = q.Change / q.PrevClose * 100
	}
	return q, nil
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

var _ marketdata.Provider = (*Provider)(nil)
