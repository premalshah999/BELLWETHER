package fundamentals

import (
	"context"
	"log/slog"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Store is the persistence the runner needs.
type Store interface {
	SaveFundamentals(ctx context.Context, c Company) error
}

// Runner refreshes fundamentals across a universe.
type Runner struct {
	Client   *Client
	Store    Store
	Universe func() []marketdata.Symbol
	Log      *slog.Logger

	// Batch is how many symbols one pass covers.
	//
	// Fundamentals cost roughly five seconds a symbol — several upstream
	// requests each for ratios and three statements — so a full 750-name
	// universe is around an hour. That is fine overnight and unacceptable as
	// a foreground refresh, which is why this runs on its own schedule and
	// never on a request path.
	Batch int
}

// Refresh fetches and stores fundamentals for the universe.
func (r *Runner) Refresh(ctx context.Context) (stored int, failed int, err error) {
	universe := r.Universe()
	if r.Batch > 0 && len(universe) > r.Batch {
		universe = universe[:r.Batch]
	}
	if len(universe) == 0 {
		return 0, 0, nil
	}

	started := time.Now()
	companies, bad, err := r.Client.Fetch(ctx, universe)
	if err != nil {
		return 0, len(bad), err
	}
	for _, c := range companies {
		if err := r.Store.SaveFundamentals(ctx, c); err != nil {
			r.log().Warn("could not store fundamentals", "symbol", c.Snapshot.Symbol, "err", err)
			failed++
			continue
		}
		stored++
	}
	r.log().Info("fundamentals refreshed",
		"requested", len(universe), "stored", stored, "failed", failed+len(bad),
		"elapsed", time.Since(started).Round(time.Second).String())
	return stored, failed + len(bad), nil
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
