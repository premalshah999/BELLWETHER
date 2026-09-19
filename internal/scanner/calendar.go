package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// CalendarEntry is one symbol's next scheduled corporate events, as the price
// sidecar reports them.
type CalendarEntry struct {
	Symbol         marketdata.Symbol
	EarningsDate   *time.Time
	ExDividendDate *time.Time
	DividendDate   *time.Time
	EPSLow         *float64
	EPSHigh        *float64
	EPSAverage     *float64
}

type calendarWire struct {
	EarningsDate   string   `json:"earnings_date"`
	ExDividendDate string   `json:"ex_dividend_date"`
	DividendDate   string   `json:"dividend_date"`
	EPSLow         *float64 `json:"eps_low"`
	EPSHigh        *float64 `json:"eps_high"`
	EPSAverage     *float64 `json:"eps_average"`
}

type calendarResponse struct {
	Calendar map[string]calendarWire `json:"calendar"`
	Failed   []string                `json:"failed"`
	AsOf     string                  `json:"as_of"`
	Elapsed  float64                 `json:"elapsed_seconds"`
}

// CalendarResult is a batch of upcoming events plus what could not be read.
type CalendarResult struct {
	Entries []CalendarEntry
	Failed  []marketdata.Symbol
	Elapsed time.Duration
}

// calendarDecodeLimit bounds the response body. A calendar row is a handful of
// dates, so even the whole universe is small -- two orders of magnitude under
// the scan path, which carries a year of bars per symbol.
const calendarDecodeLimit = 8 << 20

// Calendar fetches the next scheduled corporate events for a universe.
//
// Symbols cross the wire in the vendor's spelling and come back re-tagged
// with the canonical one, exactly as Scan does, so no caller outside this
// package ever handles a ".NS" suffix.
func (c *Client) Calendar(ctx context.Context, symbols []marketdata.Symbol) (CalendarResult, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return CalendarResult{}, fmt.Errorf("scanner: no price service configured")
	}
	if len(symbols) == 0 {
		return CalendarResult{}, fmt.Errorf("scanner: empty universe")
	}

	vendors := make([]string, 0, len(symbols))
	symbolOf := make(map[string]marketdata.Symbol, len(symbols))
	for _, sym := range symbols {
		vendor, ok := vendorTicker(sym)
		if !ok {
			continue
		}
		vendors = append(vendors, vendor)
		symbolOf[vendor] = sym
	}
	if len(vendors) == 0 {
		return CalendarResult{}, fmt.Errorf("scanner: no symbols this provider can quote")
	}

	body, err := json.Marshal(map[string]any{"symbols": vendors})
	if err != nil {
		return CalendarResult{}, fmt.Errorf("scanner: encode calendar request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/calendar", bytes.NewReader(body))
	if err != nil {
		return CalendarResult{}, fmt.Errorf("scanner: build calendar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return CalendarResult{}, fmt.Errorf("scanner: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return CalendarResult{}, fmt.Errorf("scanner: calendar http %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var parsed calendarResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, calendarDecodeLimit)).Decode(&parsed); err != nil {
		return CalendarResult{}, fmt.Errorf("scanner: decode calendar: %w", err)
	}

	out := CalendarResult{Elapsed: time.Duration(parsed.Elapsed * float64(time.Second))}
	for vendor, w := range parsed.Calendar {
		sym, ok := symbolOf[vendor]
		if !ok {
			// A symbol we did not ask about. Dropped rather than stored under
			// a guess at what it canonicalises to.
			continue
		}
		entry := CalendarEntry{
			Symbol: sym,
			EPSLow: w.EPSLow, EPSHigh: w.EPSHigh, EPSAverage: w.EPSAverage,
		}
		entry.EarningsDate = parseCalendarDate(w.EarningsDate)
		entry.ExDividendDate = parseCalendarDate(w.ExDividendDate)
		entry.DividendDate = parseCalendarDate(w.DividendDate)
		if entry.EarningsDate == nil && entry.ExDividendDate == nil && entry.DividendDate == nil {
			continue
		}
		out.Entries = append(out.Entries, entry)
	}
	for _, vendor := range parsed.Failed {
		if sym, ok := symbolOf[vendor]; ok {
			out.Failed = append(out.Failed, sym)
		}
	}
	return out, nil
}

// parseCalendarDate reads a plain YYYY-MM-DD. An unparseable or empty value
// is absence, not an error: one malformed date must not cost the whole batch.
func parseCalendarDate(s string) *time.Time {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return nil
	}
	return &t
}
