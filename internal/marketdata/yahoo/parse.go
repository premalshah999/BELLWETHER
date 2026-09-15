package yahoo

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// chartEnvelope mirrors the shape of /v8/finance/chart. Every numeric field in
// the indicator arrays is a pointer because Yahoo emits JSON null for bars it
// has no data for — holidays, halts, and the not-yet-formed current bar. A null
// must skip the bar; it must never become a zero price.
type chartEnvelope struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency             string   `json:"currency"`
				Symbol               string   `json:"symbol"`
				RegularMarketPrice   *float64 `json:"regularMarketPrice"`
				ChartPreviousClose   *float64 `json:"chartPreviousClose"`
				PreviousClose        *float64 `json:"previousClose"`
				RegularMarketDayHigh *float64 `json:"regularMarketDayHigh"`
				RegularMarketDayLow  *float64 `json:"regularMarketDayLow"`
				RegularMarketVolume  *float64 `json:"regularMarketVolume"`
				RegularMarketTime    *int64   `json:"regularMarketTime"`
				ExchangeTimezoneName string   `json:"exchangeTimezoneName"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*float64 `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// ChartMeta is the subset of Yahoo's metadata block we rely on.
type ChartMeta struct {
	Currency  string
	Symbol    string
	Price     float64
	PrevClose float64
	DayHigh   float64
	DayLow    float64
	Volume    float64
	AsOf      time.Time
	// HasPrice is false when Yahoo omitted regularMarketPrice, which happens
	// for delisted symbols and occasionally mid-session.
	HasPrice bool
}

// ParseChart turns a raw chart response into candles and metadata.
//
// This is the single place that knows Yahoo's JSON shape. It is pure, so tests
// drive it directly from recorded payloads, and it returns descriptive errors
// rather than partial nonsense when the shape changes.
func ParseChart(body []byte) ([]marketdata.Candle, ChartMeta, error) {
	var env chartEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, ChartMeta{}, fmt.Errorf("yahoo: decode chart json: %w", err)
	}
	if env.Chart.Error != nil {
		return nil, ChartMeta{}, fmt.Errorf("yahoo: upstream error %s: %s",
			env.Chart.Error.Code, env.Chart.Error.Description)
	}
	if len(env.Chart.Result) == 0 {
		return nil, ChartMeta{}, fmt.Errorf("%w: yahoo chart.result empty", marketdata.ErrNoData)
	}
	r := env.Chart.Result[0]

	meta := ChartMeta{
		Currency: r.Meta.Currency,
		Symbol:   r.Meta.Symbol,
	}
	if r.Meta.RegularMarketPrice != nil {
		meta.Price = *r.Meta.RegularMarketPrice
		meta.HasPrice = true
	}
	// Yahoo supplies the previous close under two different keys depending on
	// the range requested; prefer the chart-relative one and fall back.
	switch {
	case r.Meta.ChartPreviousClose != nil:
		meta.PrevClose = *r.Meta.ChartPreviousClose
	case r.Meta.PreviousClose != nil:
		meta.PrevClose = *r.Meta.PreviousClose
	}
	if r.Meta.RegularMarketDayHigh != nil {
		meta.DayHigh = *r.Meta.RegularMarketDayHigh
	}
	if r.Meta.RegularMarketDayLow != nil {
		meta.DayLow = *r.Meta.RegularMarketDayLow
	}
	if r.Meta.RegularMarketVolume != nil {
		meta.Volume = *r.Meta.RegularMarketVolume
	}
	if r.Meta.RegularMarketTime != nil {
		meta.AsOf = time.Unix(*r.Meta.RegularMarketTime, 0).UTC()
	}

	if len(r.Indicators.Quote) == 0 {
		// Metadata without an OHLC block is a valid response for some
		// symbols; hand back what we have and let the caller decide.
		return nil, meta, nil
	}
	q := r.Indicators.Quote[0]

	n := len(r.Timestamp)
	// Trust the shortest array. A ragged response is a shape change, and
	// indexing past the end of any of these would panic.
	for _, arr := range [][]*float64{q.Open, q.High, q.Low, q.Close, q.Volume} {
		if len(arr) < n {
			n = len(arr)
		}
	}

	candles := make([]marketdata.Candle, 0, n)
	for i := 0; i < n; i++ {
		o, h, l, c, v := q.Open[i], q.High[i], q.Low[i], q.Close[i], q.Volume[i]
		// A bar is only usable if it has a full OHLC. Volume may legitimately
		// be null on indices, so it degrades to zero rather than dropping the
		// bar; price fields never do.
		if o == nil || h == nil || l == nil || c == nil {
			continue
		}
		if !finite(*o) || !finite(*h) || !finite(*l) || !finite(*c) {
			continue
		}
		var vol float64
		if v != nil && finite(*v) {
			vol = *v
		}
		candles = append(candles, marketdata.Candle{
			Time:   time.Unix(r.Timestamp[i], 0).UTC(),
			Open:   *o,
			High:   *h,
			Low:    *l,
			Close:  *c,
			Volume: vol,
		})
	}
	return marketdata.SortCandles(candles), meta, nil
}

// quoteFromMeta builds a quote, falling back to the candle tail when Yahoo's
// metadata block is incomplete.
func quoteFromMeta(sym marketdata.Symbol, meta ChartMeta, candles []marketdata.Candle) (marketdata.Quote, error) {
	q := marketdata.Quote{
		Symbol:    sym,
		Price:     meta.Price,
		PrevClose: meta.PrevClose,
		DayHigh:   meta.DayHigh,
		DayLow:    meta.DayLow,
		Volume:    meta.Volume,
		Currency:  meta.Currency,
		AsOf:      meta.AsOf,
	}
	if q.Currency == "" {
		q.Currency = sym.Currency()
	}
	if !meta.HasPrice {
		if len(candles) == 0 {
			return marketdata.Quote{}, fmt.Errorf("%w: yahoo gave neither price nor bars for %s", marketdata.ErrNoData, sym)
		}
		last := candles[len(candles)-1]
		q.Price = last.Close
		if q.AsOf.IsZero() {
			q.AsOf = last.Time
		}
		if q.Volume == 0 {
			q.Volume = last.Volume
		}
	}
	if q.PrevClose == 0 && len(candles) >= 2 {
		q.PrevClose = candles[len(candles)-2].Close
	}
	if q.AsOf.IsZero() {
		q.AsOf = time.Now().UTC()
	}
	if q.PrevClose != 0 {
		q.Change = q.Price - q.PrevClose
		q.ChangePercent = q.Change / q.PrevClose * 100
	}
	return q, nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
