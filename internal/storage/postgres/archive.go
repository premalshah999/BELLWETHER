package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// Archive is a second Postgres holding aged news. Postgres cannot join across
// servers, so the news graph is divided by time, the one cut that keeps an
// event and everything hanging off it on one server. Both carry the same
// schema; a read spanning the cutoff is two identical queries merged, which is
// why the cutoff sits well outside the feed's usual window.
type Archive struct {
	hot    *DB
	cold   *DB
	window time.Duration
	log    *slog.Logger
}

// OpenArchive connects the archive and brings its schema up to date. It
// carries the same migrations as the primary, because a shard that cannot
// answer the same query as the other shard is not a shard.
func OpenArchive(ctx context.Context, hot *DB, dsn string, window time.Duration, opts ...Option) (*Archive, error) {
	if hot == nil {
		return nil, fmt.Errorf("postgres: archive needs a primary to roll over from")
	}
	if window <= 0 {
		return nil, fmt.Errorf("postgres: archive window must be positive, got %s", window)
	}
	cold, err := Open(ctx, dsn, opts...)
	if err != nil {
		return nil, fmt.Errorf("postgres: open news archive: %w", err)
	}
	a := &Archive{hot: hot, cold: cold, window: window, log: hot.log}
	a.log.Info("news archive ready",
		"target", redactDSN(dsn), "hot_window", window)
	return a, nil
}

// Close releases the archive connection. The primary is not ours to close.
func (a *Archive) Close() error { return a.cold.Close() }

// DB exposes the archive's own handle, for the few callers that need to query
// it directly rather than through a fan-out.
func (a *Archive) DB() *DB { return a.cold }

// cutoff is the boundary between hot and archived, measured the same way the
// feed measures an item's age.
func (a *Archive) cutoff(now time.Time) time.Time { return now.Add(-a.window) }

// contentAge is the Go equivalent of contentAgeExpr: publication time, clamped
// to discovery when that is missing or implausibly later.
//
// It has to agree with the SQL exactly. If it did not, an event could be
// archived while the hot shard's own window still claimed it, and the merge
// would return it twice or not at all.
func contentAge(e news.Event) time.Time {
	if e.PublishedAt.IsZero() || e.PublishedAt.After(e.DiscoveredAt) {
		return e.DiscoveredAt
	}
	return e.PublishedAt
}

// SpansArchive reports whether a filter reaches past the cutoff, and so needs
// the archive consulted at all.
//
// A zero Since means "no lower bound", which reaches back forever.
func (a *Archive) SpansArchive(f EventFilter, now time.Time) bool {
	return f.Since.IsZero() || f.Since.Before(a.cutoff(now))
}

// ListEvents answers from the primary alone when the window allows, and
// otherwise merges both shards.
//
// The merge takes Limit+Offset from each side rather than Limit, because the
// first Limit rows of the union can all come from either shard. That is the
// unavoidable cost of paging across two servers, and it is why the cutoff is
// set outside the feed's ordinary window.
func (a *Archive) ListEvents(ctx context.Context, f EventFilter, now time.Time) ([]news.Event, error) {
	if !a.SpansArchive(f, now) {
		return a.hot.ListEvents(ctx, f)
	}

	want := f.Limit
	if want <= 0 {
		want = 100
	}
	// Each shard is asked for everything the merged page could need from it.
	reach := f
	reach.Limit = want + f.Offset
	reach.Offset = 0

	hot, err := a.hot.ListEvents(ctx, reach)
	if err != nil {
		return nil, fmt.Errorf("postgres: list events (primary): %w", err)
	}
	cold, err := a.cold.ListEvents(ctx, reach)
	if err != nil {
		// A cold shard that is asleep or rate-limited must not take the feed
		// down with it. The primary holds everything recent, which is what a
		// reader is almost always asking for, so this degrades to that and
		// says so rather than returning an error for a page it could mostly
		// have served.
		a.log.Warn("news archive unavailable; serving the primary alone",
			"err", err, "since", f.Since)
		return trim(hot, f.Offset, want), nil
	}

	return trim(mergeEvents(hot, cold, f.OrderByContentAge), f.Offset, want), nil
}

// mergeEvents interleaves two already-sorted pages into one.
//
// Both shards ordered by the same key, so this is a merge rather than a sort:
// content age descending when the caller asked for it, discovery descending
// otherwise, with the id breaking ties exactly as the SQL does.
func mergeEvents(hot, cold []news.Event, byContentAge bool) []news.Event {
	out := make([]news.Event, 0, len(hot)+len(cold))
	out = append(out, hot...)
	out = append(out, cold...)

	key := func(e news.Event) time.Time { return e.DiscoveredAt }
	if byContentAge {
		key = contentAge
	}
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := key(out[i]), key(out[j])
		if !ki.Equal(kj) {
			return ki.After(kj)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func trim[T any](rows []T, offset, limit int) []T {
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}
