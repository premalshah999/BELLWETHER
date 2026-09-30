package research

import (
	"context"
	"fmt"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// RouterPrices adapts the market data router to the PriceSource this package
// needs.
//
// It goes through the router rather than to a provider directly so that
// research inherits everything already built there: the provider fallback
// chain, the session-aware cache, the shared budget, and the single-flight
// that stops one question about six companies becoming six simultaneous
// upstream requests for a series someone else is already fetching.
type RouterPrices struct {
	Router *marketdata.Router
}

// DailyBars implements PriceSource.
func (p RouterPrices) DailyBars(ctx context.Context, symbol string, limit int) ([]Bar, error) {
	if p.Router == nil {
		return nil, fmt.Errorf("research: no market data router")
	}
	// Canonical symbols: a bare ticker is a US listing, GSPC.INDEX an index.
	sym, err := marketdata.ParseSymbol(symbol)
	if err != nil {
		return nil, err
	}
	series, err := p.Router.Candles(ctx, sym, marketdata.Interval1d, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(series.Candles))
	for _, c := range series.Candles {
		out = append(out, Bar{
			Time: c.Time, Open: c.Open, High: c.High,
			Low: c.Low, Close: c.Close, Volume: c.Volume,
		})
	}
	return out, nil
}
