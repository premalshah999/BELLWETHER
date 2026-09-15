package marketdata

import "time"

// Trading sessions.
//
// The cache needs these because the lifetime of a bar depends on whether it
// has finished forming. A daily bar from last week will never change again
// and can be held for a day; today's daily bar changes on every trade, and
// caching the two the same way is how a chart ends up showing the morning's
// price all afternoon.

var (
	istLocation = mustLoad("Asia/Kolkata")
	etLocation  = mustLoad("America/New_York")
)

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// A missing tzdata would silently make every session look closed,
		// which caches stale prices rather than failing visibly. UTC is
		// wrong but at least it is wrong loudly: sessions never open.
		return time.UTC
	}
	return loc
}

// sessionWindow is a venue's regular hours, in its own timezone.
type sessionWindow struct {
	loc            *time.Location
	openH, openM   int
	closeH, closeM int
}

func (e Exchange) window() sessionWindow {
	switch e {
	case ExchangeNSE, ExchangeBSE:
		// 09:15–15:30 IST.
		return sessionWindow{loc: istLocation, openH: 9, openM: 15, closeH: 15, closeM: 30}
	default:
		// 09:30–16:00 ET.
		return sessionWindow{loc: etLocation, openH: 9, openM: 30, closeH: 16, closeM: 0}
	}
}

// settlingWindow is how long after the close a venue's last bar is still
// worth refetching.
//
// Providers do not publish the settled close the instant trading stops, and a
// long cache lifetime that begins at 15:30 would freeze the day at whatever
// the final intraday poll happened to see. Half an hour is enough for the
// close to land without holding the short lifetime open all evening.
const settlingWindow = 30 * time.Minute

// SessionActive reports whether prices for this venue may still be moving, or
// have moved recently enough that the last fetch is unlikely to be final.
//
// Holidays are not modelled. Getting this wrong costs one extra request per
// minute per charted instrument on a day the market is shut, which is a far
// cheaper mistake than the opposite one.
func (e Exchange) SessionActive(now time.Time) bool {
	w := e.window()
	local := now.In(w.loc)
	switch local.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	open := time.Date(local.Year(), local.Month(), local.Day(), w.openH, w.openM, 0, 0, w.loc)
	close := time.Date(local.Year(), local.Month(), local.Day(), w.closeH, w.closeM, 0, 0, w.loc)
	return !local.Before(open) && local.Before(close.Add(settlingWindow))
}

// SessionClose returns the moment trading ended on the day a bar belongs to.
//
// Daily bars are labelled by session rather than by observation time — NSE's
// sit at midnight IST — so the label is not a usable answer to "how old is
// this price". The close is.
func (e Exchange) SessionClose(barTime time.Time) (time.Time, bool) {
	if barTime.IsZero() {
		return time.Time{}, false
	}
	w := e.window()
	local := barTime.In(w.loc)
	// A bar stamped at midnight belongs to the session that begins that
	// morning, so the date carries over directly.
	return time.Date(local.Year(), local.Month(), local.Day(), w.closeH, w.closeM, 0, 0, w.loc), true
}
