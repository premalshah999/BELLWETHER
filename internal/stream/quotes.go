package stream

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// QuoteSource polls the market data layer for the instruments being watched,
// which is what real time looks like without a market data licence: it reports
// its latency as the poll interval rather than implying tick resolution. It
// polls only while the market is trading.
type QuoteSource struct {
	Router *marketdata.Router
	// Symbols returns what to watch. Read on every cycle so that adding an
	// instrument to the watchlist starts streaming it without a restart.
	Symbols func() []marketdata.Symbol
	Log     *slog.Logger

	// Interval is how often to poll while a market is open.
	Interval time.Duration
	// IdleInterval is how often to check while every relevant market is shut.
	// Not zero: a market opens eventually, and the source has to notice.
	IdleInterval time.Duration

	mu   sync.Mutex
	last map[string]marketdata.Quote
}

// Name implements Source.
func (q *QuoteSource) Name() string { return "quotes:polled" }

// barResolution is the finest data this source can see.
//
// The upstream is minute bars, so a price cannot be fresher than the minute
// it belongs to however often we ask. Reporting the poll interval instead
// would overstate this by an order of magnitude.
const barResolution = time.Minute

// Latency implements Source.
//
// Reports what a caller actually gets, not how often this polls. Those are
// different numbers here and the difference matters: polling every five
// seconds against a feed that produces a bar a minute yields a price up to a
// minute old, and anything deciding whether to trust this for execution needs
// the second number, not the first.
func (q *QuoteSource) Latency() time.Duration {
	poll := q.Interval
	if poll <= 0 {
		poll = 5 * time.Second
	}
	if poll > barResolution {
		return poll
	}
	return barResolution
}

// Run implements Source.
func (q *QuoteSource) Run(ctx context.Context, out chan<- Update) error {
	interval := q.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	idle := q.IdleInterval
	if idle <= 0 {
		idle = time.Minute
	}

	q.mu.Lock()
	if q.last == nil {
		q.last = map[string]marketdata.Quote{}
	}
	q.mu.Unlock()

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}

		symbols := q.watched()
		active := q.pollOnce(ctx, symbols, out)

		next := idle
		if active {
			next = interval
		}
		timer.Reset(next)
	}
}

// pollOnce fetches every watched instrument, returning whether any venue was
// trading.
func (q *QuoteSource) pollOnce(ctx context.Context, symbols []marketdata.Symbol, out chan<- Update) bool {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		active bool
	)
	// Bounded: a watchlist of forty instruments should not open forty
	// simultaneous requests every few seconds.
	sem := make(chan struct{}, 6)

	for _, sym := range symbols {
		if !sym.Exchange.SessionActive(time.Now()) {
			continue
		}
		mu.Lock()
		active = true
		mu.Unlock()

		wg.Add(1)
		go func(sym marketdata.Symbol) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			res, err := q.Router.Quote(ctx, sym)
			if err != nil {
				// One instrument failing is not the feed failing. Logged at
				// debug because during a session this would otherwise be the
				// loudest thing in the log for a single delisted ticker.
				q.log().Debug("quote poll failed", "symbol", sym, "err", err)
				return
			}
			quote := res.Quote
			if !q.changed(sym.String(), quote) {
				// Unchanged prices are not published. The point of a stream
				// is that a message means something happened; republishing an
				// identical price every few seconds trains a reader to ignore
				// it.
				return
			}
			select {
			case out <- Update{
				Symbol:        sym.String(),
				Price:         quote.Price,
				Change:        quote.Change,
				ChangePercent: quote.ChangePercent,
				Volume:        quote.Volume,
				DayHigh:       quote.DayHigh,
				DayLow:        quote.DayLow,
				At:            quote.AsOf,
				Source:        q.Name(),
				// Carried through rather than hidden: an update the router
				// could only satisfy from a stale cache is still worth
				// sending, but must not arrive looking like a fresh tick.
				Stale: res.Stale,
			}:
			case <-ctx.Done():
			}
		}(sym)
	}
	wg.Wait()
	return active
}

// changed reports whether this quote differs from the last one published, and
// records it either way.
func (q *QuoteSource) changed(key string, quote marketdata.Quote) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	prev, seen := q.last[key]
	q.last[key] = quote
	if !seen {
		return true
	}
	return prev.Price != quote.Price || prev.Volume != quote.Volume
}

func (q *QuoteSource) watched() []marketdata.Symbol {
	if q.Symbols == nil {
		return nil
	}
	return q.Symbols()
}

func (q *QuoteSource) log() *slog.Logger {
	if q.Log != nil {
		return q.Log
	}
	return slog.Default()
}
