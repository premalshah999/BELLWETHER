package twelvedata

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// timeSeriesResponse mirrors the /time_series payload.
//
// Every numeric field arrives as a string, and an error arrives in the same
// envelope with a status of "error" — sometimes under an HTTP 200. Inspecting
// the body rather than the status code is therefore the only reliable read.
type timeSeriesResponse struct {
	Meta struct {
		Symbol           string `json:"symbol"`
		Interval         string `json:"interval"`
		Currency         string `json:"currency"`
		ExchangeTimezone string `json:"exchange_timezone"`
		Exchange         string `json:"exchange"`
		MICCode          string `json:"mic_code"`
		Type             string `json:"type"`
	} `json:"meta"`
	Values []struct {
		Datetime string `json:"datetime"`
		Open     string `json:"open"`
		High     string `json:"high"`
		Low      string `json:"low"`
		Close    string `json:"close"`
		Volume   string `json:"volume"`
	} `json:"values"`
	Status string `json:"status"`

	// Error envelope.
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ParseTimeSeries turns a /time_series response into candles, oldest first.
//
// This is the only place that knows Twelve Data's wire format. It is pure, so
// tests drive it from recorded payloads.
func ParseTimeSeries(body []byte) ([]marketdata.Candle, error) {
	var resp timeSeriesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("twelvedata: decode json: %w", err)
	}

	if resp.Status == "error" || resp.Code >= 400 {
		return nil, classify(resp.Code, resp.Message)
	}
	if len(resp.Values) == 0 {
		return nil, fmt.Errorf("%w: twelvedata returned an empty series", marketdata.ErrNoData)
	}

	candles := make([]marketdata.Candle, 0, len(resp.Values))
	for _, v := range resp.Values {
		t, err := parseTimestamp(v.Datetime)
		if err != nil {
			// One malformed row must not discard the series.
			continue
		}
		o, ok1 := parseFloat(v.Open)
		h, ok2 := parseFloat(v.High)
		l, ok3 := parseFloat(v.Low)
		c, ok4 := parseFloat(v.Close)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			// A bar is dropped rather than zero-filled: a zero price reads as
			// a catastrophic crash to every indicator downstream.
			continue
		}
		// Volume is legitimately absent for some instruments, so zero is the
		// honest value there — unlike for prices.
		vol, _ := parseFloat(v.Volume)

		candles = append(candles, marketdata.Candle{
			Time: t, Open: o, High: h, Low: l, Close: c, Volume: vol,
		})
	}
	if len(candles) == 0 {
		return nil, fmt.Errorf("twelvedata: %d rows returned but none parsed", len(resp.Values))
	}
	// Twelve Data returns newest first; SortCandles flips to the oldest-first
	// order every consumer expects.
	return marketdata.SortCandles(candles), nil
}

// classify maps Twelve Data's error codes onto the errors the router knows how
// to route around.
func classify(code int, message string) error {
	msg := strings.TrimSpace(message)
	switch code {
	case 429:
		// Out of credits. Treating this as budget exhaustion makes the router
		// fall through to another provider rather than marking this one down.
		return fmt.Errorf("%w: twelvedata: %s", marketdata.ErrBudgetExhausted, msg)
	case 404:
		return fmt.Errorf("%w: twelvedata: %s", marketdata.ErrNoData, msg)
	case 400:
		return fmt.Errorf("%w: twelvedata rejected the call: %s", marketdata.ErrNoData, msg)
	case 401, 403:
		// A bad or absent key is a configuration problem, not a fault the
		// provider can recover from by being retried.
		return fmt.Errorf("%w: twelvedata: %s", marketdata.ErrNotSupported, msg)
	default:
		return fmt.Errorf("twelvedata: error %d: %s", code, msg)
	}
}

// parseTimestamp accepts both the date-only form used for daily and weekly
// bars and the date-time form used intraday. Requests set timezone=UTC, so
// these are already UTC.
func parseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("twelvedata: unrecognised timestamp %q", s)
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
