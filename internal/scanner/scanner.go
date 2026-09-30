// Package scanner finds the instruments worth looking at from price and volume
// alone. The price moves first and the explanation follows, so rather than
// asking "is anything happening" across the whole universe, search answers
// "why is this moving" for the handful the scan flags.
package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// vendorTicker is the price sidecar's spelling of a symbol; indices are not
// scanned.
func vendorTicker(sym marketdata.Symbol) (vendor string, ok bool) {
	return sym.Ticker, sym.Exchange == marketdata.ExchangeUS
}

// Metrics are one instrument's behaviour relative to its own recent normal.
// Absolute thresholds would surface the same volatile names every day and miss
// the quiet one that just woke up.
type Metrics struct {
	Symbol string `json:"symbol"`

	Close    float64 `json:"close"`
	Return1D float64 `json:"return_1d"`
	Return5D float64 `json:"return_5d"`
	// ReturnZ is today's move in standard deviations of this instrument's own
	// daily returns.
	ReturnZ float64 `json:"return_z"`

	Volume float64 `json:"volume"`
	// VolumeRatio is today's volume against the median of its recent days.
	// Against the median rather than the mean, because the mean of a
	// right-skewed series sits above its own typical day.
	VolumeRatio float64 `json:"volume_ratio"`
	// VolumeZ is today's volume against its own recent normal, measured in
	// log space against a median and a MAD. Volume is strongly right-skewed
	// -- across this universe its skewness runs from 0.9 to 6.4, where a
	// normal distribution is 0 -- so an ordinary z-score on raw volume
	// reports the shape of the stock's distribution rather than the
	// unusualness of today. One instrument read 41.8 by that method and 5.6
	// by this one.
	VolumeZ float64 `json:"volume_z"`

	GapPercent     float64 `json:"gap_percent"`
	PctFrom52WHigh float64 `json:"pct_from_52w_high"`
	PctFrom52WLow  float64 `json:"pct_from_52w_low"`
	Bars           int     `json:"bars"`
}

// Signal is a reason an instrument is worth attention.
type Signal string

const (
	SignalVolumeSpike Signal = "volume_spike"
	SignalPriceMove   Signal = "price_move"
	SignalGap         Signal = "gap"
	SignalNear52WHigh Signal = "near_52w_high"
	SignalNear52WLow  Signal = "near_52w_low"
	// SignalSilentVolume is volume without price — accumulation or
	// distribution that has not shown up in the price yet, and often the
	// earliest thing a scanner can see.
	SignalSilentVolume Signal = "volume_without_price"
)

// Thresholds, in units of the robust z-scores the price service computes, set
// from the measured distribution rather than by intuition: 2.5 selects roughly
// the top three percent, about one day in thirty for any instrument. These are
// not normal distributions, and the 2.0 a normal one suggests picks up ten
// percent of the universe.
const (
	volumeSpikeZ    = 2.5
	priceMoveZ      = 2.5
	gapPercent      = 3.0
	near52WHighPct  = -2.0
	near52WLowPct   = 3.0
	silentVolumeZ   = 3.0
	silentPriceZMax = 1.0
)

// Finding is one instrument the scanner considers abnormal.
type Finding struct {
	Metrics
	Signals []Signal `json:"signals"`
	// Score ranks findings against each other. It is a heuristic for
	// ordering attention, not a measure of anything in the world.
	Score float64 `json:"score"`

	// Explained is whether the archive already accounted for this move when
	// the scan ran. Nil means the question was not asked; false is a real
	// result and the more interesting one, because it says we looked and
	// found nothing. Collapsing the two would quietly turn the scanner's
	// best output into its default.
	Explained *bool `json:"explained,omitempty"`
}

// HasDiscoverableCause reports whether a finding's signals are of a kind that
// usually has a findable reason behind them, as opposed to drift.
//
// Distinct from the Explained field, which records whether we actually found
// one: this is about whether it is worth looking.
func (f Finding) HasDiscoverableCause() bool {
	for _, s := range f.Signals {
		if s == SignalVolumeSpike || s == SignalGap || s == SignalPriceMove {
			return true
		}
	}
	return false
}

// Classify turns metrics into signals.
//
// Deliberately conjunctive where it matters: a price move on ordinary volume
// is usually drift, and volume without price is a different phenomenon worth
// its own signal rather than being folded into the first.
func Classify(m Metrics) []Signal {
	var out []Signal

	bigVolume := m.VolumeZ >= volumeSpikeZ
	bigMove := math.Abs(m.ReturnZ) >= priceMoveZ

	if bigVolume {
		out = append(out, SignalVolumeSpike)
	}
	if bigMove {
		out = append(out, SignalPriceMove)
	}
	if math.Abs(m.GapPercent) >= gapPercent {
		out = append(out, SignalGap)
	}
	// Volume arriving without a price response is not the same event as a
	// move on heavy volume, and conflating them loses the more interesting
	// of the two: somebody is transacting size and the price has not
	// reacted yet.
	if m.VolumeZ >= silentVolumeZ && math.Abs(m.ReturnZ) < silentPriceZMax {
		out = append(out, SignalSilentVolume)
	}
	// Proximity to an extreme only counts alongside participation. Without
	// it, every stock in a quiet uptrend sits near its high indefinitely and
	// would be reported every single day.
	if m.PctFrom52WHigh >= near52WHighPct && (bigVolume || bigMove) {
		out = append(out, SignalNear52WHigh)
	}
	if m.PctFrom52WLow <= near52WLowPct && (bigVolume || bigMove) {
		out = append(out, SignalNear52WLow)
	}
	return out
}

// score ranks a finding for attention.
//
// Volume leads because it is the signal least likely to be noise: a price can
// move on nothing, but sustained volume means somebody transacted. Everything
// else adjusts that.
func score(m Metrics, signals []Signal) float64 {
	s := math.Max(m.VolumeZ, 0)*1.5 + math.Abs(m.ReturnZ)
	s += math.Abs(m.GapPercent) / 3
	for _, sig := range signals {
		switch sig {
		case SignalSilentVolume:
			// Worth more than it looks: an unexplained transaction is a
			// better lead than a move everyone can already see.
			s += 1.5
		case SignalNear52WHigh, SignalNear52WLow:
			s += 1.0
		}
	}
	return math.Round(s*100) / 100
}

// Result is one scan.
type Result struct {
	AsOf     time.Time `json:"as_of"`
	Universe int       `json:"universe"`
	Scanned  int       `json:"scanned"`
	Failed   int       `json:"failed"`
	Findings []Finding `json:"findings"`
	// All is every instrument measured, including the great majority that
	// tripped no signal. The Scanner page never looks at it; custom screens
	// are nothing but a query over it.
	All     []Metrics `json:"-"`
	Elapsed string    `json:"elapsed"`

	// Series is the same year of daily bars the sidecar fetched to compute
	// All, keyed by the symbol it belongs to. Nothing in this package reads
	// it -- it exists so Runner.Run can persist it, which is the event
	// study engine's whole price-history prerequisite: the scan already
	// pays for this fetch on every pass, and it used to be discarded the
	// moment the metrics were reduced from it.
	Series map[marketdata.Symbol][]marketdata.Candle `json:"-"`
}

// Client talks to the price sidecar.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	// Adjust asks for dividend-adjusted prices, matching YFINANCE_ADJUST.
	Adjust bool
	// Period is how much daily history a scan fetches: "1y" unless set.
	Period string
}

type scanResponse struct {
	Metrics map[string]Metrics    `json:"metrics"`
	Series  map[string]scanSeries `json:"series"`
	Failed  []string              `json:"failed"`
	AsOf    string                `json:"as_of"`
	Elapsed float64               `json:"elapsed_seconds"`
}

// scanSeries is one symbol's daily bars in the sidecar's columnar wire
// format -- one array per field rather than one object per bar. Across the
// full scan universe (1,000+ symbols, ~250 trading days each) that is the
// difference between a response comfortably under the client's decode limit
// and one pressed right up against it; the single-symbol chart endpoint
// (yfin.go) has no such volume problem and keeps the more legible
// one-object-per-bar shape.
type scanSeries struct {
	T []int64   `json:"t"` // YYYYMMDD
	O []float64 `json:"o"`
	H []float64 `json:"h"`
	L []float64 `json:"l"`
	C []float64 `json:"c"`
	V []float64 `json:"v"`
}

// candles decodes the columnar form into ordinary bars, skipping any row
// that does not carry every field (a truncated array from a chunk that
// partly failed) or that fails the same OHLC sanity check candles' own
// storage-layer CHECK constraint enforces -- filtered here rather than left
// for Postgres to reject, because SaveCandles writes a symbol's whole batch
// in one transaction and a single bad row would otherwise cost that
// symbol's entire day of history for this pass, not just the one row.
func (s scanSeries) candles() []marketdata.Candle {
	n := len(s.T)
	if len(s.O) != n || len(s.H) != n || len(s.L) != n || len(s.C) != n || len(s.V) != n {
		return nil
	}
	out := make([]marketdata.Candle, 0, n)
	for i := 0; i < n; i++ {
		o, h, l, c, v := s.O[i], s.H[i], s.L[i], s.C[i], s.V[i]
		if !(h >= l && h >= o && h >= c && l <= o && l <= c) || v < 0 {
			continue
		}
		y, m, d := int(s.T[i]/10000), int(s.T[i]/100%100), int(s.T[i]%100)
		if y == 0 || m == 0 || d == 0 {
			continue
		}
		out = append(out, marketdata.Candle{
			// Midnight New York, the session's own date: the stamp every
			// other writer of daily bars uses, so the same session is one row.
			Time: time.Date(y, time.Month(m), d, 0, 0, 0, 0, marketdata.Market),
			Open: o, High: h, Low: l, Close: c, Volume: v,
		})
	}
	return out
}

// Scan fetches a universe and returns what is behaving abnormally.
//
// symbols are canonical (AAPL, GSPC.INDEX). The spelling the price source
// expects is translated
// here so no caller has to know about it, and every Metrics that comes back
// is re-tagged with the same canonical form it was requested under.
func (c *Client) Scan(ctx context.Context, symbols []marketdata.Symbol, maxFindings int) (Result, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return Result{}, fmt.Errorf("scanner: no price service configured")
	}
	if len(symbols) == 0 {
		return Result{}, fmt.Errorf("scanner: empty universe")
	}
	if maxFindings <= 0 {
		maxFindings = 40
	}

	suffixed := make([]string, 0, len(symbols))
	canonicalOf := make(map[string]string, len(symbols))
	symbolOf := make(map[string]marketdata.Symbol, len(symbols))
	for _, sym := range symbols {
		vendor, ok := vendorTicker(sym)
		if !ok {
			continue
		}
		suffixed = append(suffixed, vendor)
		canonicalOf[vendor] = sym.String()
		symbolOf[vendor] = sym
	}

	body, err := json.Marshal(map[string]any{"symbols": suffixed, "adjust": c.Adjust, "period": c.Period})
	if err != nil {
		return Result{}, fmt.Errorf("scanner: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/scan", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("scanner: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("scanner: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("scanner: price service returned %s", resp.Status)
	}

	var raw scanResponse
	// 96MB: the metrics payload is small, but the columnar series for a
	// full-year, ~1,250-symbol universe measures in the tens of megabytes on
	// its own (see scanSeries's doc), and this must have real headroom above
	// that rather than sit right at the edge of it.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 96<<20)).Decode(&raw); err != nil {
		return Result{}, fmt.Errorf("scanner: decode response: %w", err)
	}

	out := Result{
		Universe: len(suffixed),
		Scanned:  len(raw.Metrics),
		Failed:   len(raw.Failed),
		Elapsed:  fmt.Sprintf("%.1fs", raw.Elapsed),
	}
	if t, err := time.Parse(time.RFC3339, raw.AsOf); err == nil {
		out.AsOf = t.UTC()
	} else {
		out.AsOf = time.Now().UTC()
	}

	for vendorSym, m := range raw.Metrics {
		canonical, ok := canonicalOf[vendorSym]
		if !ok {
			// The sidecar echoed a symbol we never asked for. Silently
			// mapping it to a guess would risk writing one instrument's
			// metrics under another's symbol, so it is dropped.
			continue
		}
		m.Symbol = canonical
		out.All = append(out.All, m)
		signals := Classify(m)
		if len(signals) == 0 {
			continue
		}
		out.Findings = append(out.Findings, Finding{
			Metrics: m, Signals: signals, Score: score(m, signals),
		})
	}

	if len(raw.Series) > 0 {
		out.Series = make(map[marketdata.Symbol][]marketdata.Candle, len(raw.Series))
		for vendorSym, s := range raw.Series {
			sym, ok := symbolOf[vendorSym]
			if !ok {
				continue
			}
			if candles := s.candles(); len(candles) > 0 {
				out.Series[sym] = candles
			}
		}
	}

	sort.Slice(out.All, func(i, j int) bool { return out.All[i].Symbol < out.All[j].Symbol })
	sort.Slice(out.Findings, func(i, j int) bool {
		if out.Findings[i].Score != out.Findings[j].Score {
			return out.Findings[i].Score > out.Findings[j].Score
		}
		return out.Findings[i].Symbol < out.Findings[j].Symbol
	})
	if len(out.Findings) > maxFindings {
		out.Findings = out.Findings[:maxFindings]
	}
	return out, nil
}
