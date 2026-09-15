package alphavantage

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Alpha Vantage answers HTTP 200 for every soft failure and signals the actual
// problem in one of these keys. Detecting them is the difference between
// "degrade to Yahoo" and "silently chart an empty series".
const (
	keyErrorMessage = "Error Message" // malformed call, unknown symbol
	keyNote         = "Note"          // per-minute throttle
	keyInformation  = "Information"   // daily cap reached, or demo-key notice
)

// apiCondition inspects a response for Alpha Vantage's soft-error keys.
// It returns a non-nil error when the payload is not real data.
func apiCondition(raw map[string]json.RawMessage) error {
	for _, key := range []string{keyErrorMessage, keyNote, keyInformation} {
		msg, ok := raw[key]
		if !ok {
			continue
		}
		var text string
		if err := json.Unmarshal(msg, &text); err != nil {
			text = string(msg)
		}
		text = strings.TrimSpace(text)
		switch key {
		case keyErrorMessage:
			return fmt.Errorf("%w: alphavantage rejected the call: %s", marketdata.ErrNoData, text)
		case keyNote, keyInformation:
			// Both mean "you have been throttled". Surfacing this as budget
			// exhaustion makes the router fall through to Yahoo and cache
			// instead of marking the provider hard-down.
			return fmt.Errorf("%w: alphavantage: %s", marketdata.ErrBudgetExhausted, text)
		}
	}
	return nil
}

// bar is one OHLCV entry. Alpha Vantage numbers every field name and delivers
// all values as strings.
type bar struct {
	Open   string `json:"1. open"`
	High   string `json:"2. high"`
	Low    string `json:"3. low"`
	Close  string `json:"4. close"`
	Volume string `json:"5. volume"`
}

// ParseTimeSeries turns a TIME_SERIES_* response into candles, oldest first.
//
// This is the only place that knows Alpha Vantage's JSON shape. It is pure so
// tests can drive it from recorded payloads.
func ParseTimeSeries(body []byte, seriesKey string) ([]marketdata.Candle, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("alphavantage: decode json: %w", err)
	}
	if err := apiCondition(raw); err != nil {
		return nil, err
	}
	seriesRaw, ok := raw[seriesKey]
	if !ok {
		return nil, fmt.Errorf("alphavantage: response has no %q block (keys: %s)", seriesKey, strings.Join(keysOf(raw), ", "))
	}
	var series map[string]bar
	if err := json.Unmarshal(seriesRaw, &series); err != nil {
		return nil, fmt.Errorf("alphavantage: decode %q: %w", seriesKey, err)
	}

	// Intraday keys carry a time component; daily and weekly keys do not.
	// Alpha Vantage stamps both in the exchange's local time, and supplies the
	// zone in the metadata block — but for daily bars the date alone is the
	// canonical identity, so we anchor those at UTC midnight to keep one bar
	// per trading day regardless of venue.
	candles := make([]marketdata.Candle, 0, len(series))
	for ts, b := range series {
		t, err := parseTimestamp(ts)
		if err != nil {
			// One malformed key should not discard the whole series.
			continue
		}
		c, ok := b.toCandle(t)
		if !ok {
			continue
		}
		candles = append(candles, c)
	}
	if len(candles) == 0 && len(series) > 0 {
		return nil, fmt.Errorf("alphavantage: %q had %d entries but none parsed", seriesKey, len(series))
	}
	return marketdata.SortCandles(candles), nil
}

func parseTimestamp(ts string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02 15:04:05", ts); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", ts); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("alphavantage: unrecognised timestamp %q", ts)
}

// toCandle converts a string bar. It reports false when any price field is
// missing or unparseable — a bar is dropped rather than zero-filled, because a
// zero price would read as a catastrophic crash to every indicator downstream.
func (b bar) toCandle(t time.Time) (marketdata.Candle, bool) {
	o, ok1 := parseFloat(b.Open)
	h, ok2 := parseFloat(b.High)
	l, ok3 := parseFloat(b.Low)
	c, ok4 := parseFloat(b.Close)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return marketdata.Candle{}, false
	}
	// Volume is genuinely absent for some instruments; zero is the honest
	// value there, unlike for prices.
	v, _ := parseFloat(b.Volume)
	return marketdata.Candle{Time: t, Open: o, High: h, Low: l, Close: c, Volume: v}, true
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// globalQuote mirrors the GLOBAL_QUOTE payload.
type globalQuote struct {
	Symbol        string `json:"01. symbol"`
	Open          string `json:"02. open"`
	High          string `json:"03. high"`
	Low           string `json:"04. low"`
	Price         string `json:"05. price"`
	Volume        string `json:"06. volume"`
	LatestDay     string `json:"07. latest trading day"`
	PreviousClose string `json:"08. previous close"`
	Change        string `json:"09. change"`
	ChangePercent string `json:"10. change percent"`
}

// ParseGlobalQuote turns a GLOBAL_QUOTE response into a Quote.
func ParseGlobalQuote(body []byte, sym marketdata.Symbol) (marketdata.Quote, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return marketdata.Quote{}, fmt.Errorf("alphavantage: decode json: %w", err)
	}
	if err := apiCondition(raw); err != nil {
		return marketdata.Quote{}, err
	}
	blockRaw, ok := raw["Global Quote"]
	if !ok {
		return marketdata.Quote{}, fmt.Errorf("alphavantage: response has no \"Global Quote\" block (keys: %s)", strings.Join(keysOf(raw), ", "))
	}
	var gq globalQuote
	if err := json.Unmarshal(blockRaw, &gq); err != nil {
		return marketdata.Quote{}, fmt.Errorf("alphavantage: decode global quote: %w", err)
	}

	price, ok := parseFloat(gq.Price)
	if !ok {
		// An empty Global Quote block is how Alpha Vantage reports an unknown
		// symbol on some plans.
		return marketdata.Quote{}, fmt.Errorf("%w: alphavantage global quote for %s has no price", marketdata.ErrNoData, sym)
	}
	q := marketdata.Quote{
		Symbol:   sym,
		Price:    price,
		Currency: sym.Currency(),
		AsOf:     time.Now().UTC(),
	}
	q.PrevClose, _ = parseFloat(gq.PreviousClose)
	q.DayHigh, _ = parseFloat(gq.High)
	q.DayLow, _ = parseFloat(gq.Low)
	q.Volume, _ = parseFloat(gq.Volume)
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(gq.LatestDay)); err == nil {
		q.AsOf = t.UTC()
	}
	// Derive change from prices rather than trusting the pre-formatted string,
	// which arrives with a trailing percent sign and varying precision.
	if q.PrevClose != 0 {
		q.Change = q.Price - q.PrevClose
		q.ChangePercent = q.Change / q.PrevClose * 100
	}
	return q, nil
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
