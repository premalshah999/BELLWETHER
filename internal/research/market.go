package research

import (
	"context"
	"math"
	"sync"
	"time"
)

// Measured market data for a research question.
//
// Kept apart from Findings for the same reason UniverseNote is: these are not
// sources and there is nothing to cite. The distinction is worth preserving in
// the type system rather than in a comment, because it is the difference
// between a fact and a claim. A six-month return computed from the price
// series is measured; a journalist writing that a stock "has surged this year"
// is reporting, and may be wrong, stale, or talking about a different window.
//
// When a question asks how something has performed, the numbers should answer
// it and the articles should explain it — not the other way round.

// Bar is one price observation. Deliberately minimal so this package depends
// on no particular market data type.
type Bar struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// ValuationSource supplies what a company is worth, as opposed to how its
// price has moved.
//
// Separate from PriceSource because the two answer different questions and
// fail independently: a company can have a full price history and no reported
// fundamentals, and a research answer should still get whichever it has.
type ValuationSource interface {
	Valuation(ctx context.Context, symbol string) (*Valuation, error)
}

// Valuation is the fundamental position of one company, already compared
// against its peers.
//
// Peer-relative rather than absolute because an absolute multiple is not
// information: a P/E of 23 means nothing until you know the sector trades at
// 12. The comparison is the finding.
type Valuation struct {
	Symbol   string `json:"symbol"`
	Industry string `json:"industry,omitempty"`

	PE            *float64 `json:"pe,omitempty"`
	PEMedian      *float64 `json:"pe_median,omitempty"`
	PB            *float64 `json:"pb,omitempty"`
	EVToEBITDA    *float64 `json:"ev_to_ebitda,omitempty"`
	ROE           *float64 `json:"roe,omitempty"`
	ROEMedian     *float64 `json:"roe_median,omitempty"`
	OpMargin      *float64 `json:"operating_margin,omitempty"`
	DebtToEquity  *float64 `json:"debt_to_equity,omitempty"`
	DividendYield *float64 `json:"dividend_yield,omitempty"`
	PeerCount     int      `json:"peer_count,omitempty"`

	// Notes are the peer comparisons stated plainly, already resolved into
	// "expensive against its sector" rather than left as percentiles for the
	// model to interpret. Interpreting a percentile is exactly the sort of
	// arithmetic a language model does unreliably, and it is trivial here.
	Notes []string `json:"notes,omitempty"`

	// Trends summarise the reported quarters.
	Trends []string `json:"trends,omitempty"`
}

// PriceSource supplies daily bars for an instrument.
type PriceSource interface {
	// DailyBars returns up to limit daily bars, oldest first.
	DailyBars(ctx context.Context, symbol string, limit int) ([]Bar, error)
}

// Horizon is a named lookback.
type Horizon struct {
	Label string `json:"label"`
	Days  int    `json:"days"`
}

// horizons are trading-day counts rather than calendar days, because the bars
// are trading days: a "1 month" return computed by stepping back 30 bars is
// six weeks ago, and the error compounds at longer windows.
var horizons = []Horizon{
	{"1w", 5}, {"1m", 21}, {"3m", 63}, {"6m", 126}, {"1y", 252},
}

// Return is a realised return over one horizon.
type Return struct {
	Horizon string  `json:"horizon"`
	Percent float64 `json:"percent"`
	// From is the close the return is measured against, so a reader can check
	// the arithmetic rather than take it on trust.
	From     float64   `json:"from"`
	FromDate time.Time `json:"from_date"`
}

// MarketStats is the measured profile of one instrument.
type MarketStats struct {
	Symbol string    `json:"symbol"`
	AsOf   time.Time `json:"as_of"`
	Close  float64   `json:"close"`

	Returns []Return `json:"returns"`

	// Volatility is the annualised standard deviation of daily returns over
	// the past year, in percent.
	Volatility float64 `json:"volatility_percent"`
	// MaxDrawdown is the worst peak-to-trough fall over the window, in
	// percent. Reported because a return alone says nothing about what an
	// investor would have had to sit through to earn it.
	MaxDrawdown float64 `json:"max_drawdown_percent"`

	High52W        float64 `json:"high_52w"`
	Low52W         float64 `json:"low_52w"`
	PctFrom52WHigh float64 `json:"pct_from_52w_high"`
	PctFrom52WLow  float64 `json:"pct_from_52w_low"`

	AvgVolume   float64 `json:"avg_volume"`
	LastVolume  float64 `json:"last_volume"`
	VolumeRatio float64 `json:"volume_ratio"`

	// Bars is the sample size every number above rests on. Stated rather than
	// implied: a "1 year volatility" from forty bars is not one, and a reader
	// cannot tell the difference from the number alone.
	Bars int `json:"bars"`
}

// Measure computes the statistical profile of one instrument.
//
// Returns nil when there is too little history to say anything honest. A
// profile built from a handful of bars is worse than none, because it looks
// exactly like a real one.
func Measure(symbol string, bars []Bar) *MarketStats {
	const minBars = 30
	if len(bars) < minBars {
		return nil
	}
	last := bars[len(bars)-1]

	st := &MarketStats{
		Symbol: symbol,
		AsOf:   last.Time,
		Close:  last.Close,
		Bars:   len(bars),
	}

	for _, h := range horizons {
		idx := len(bars) - 1 - h.Days
		if idx < 0 {
			// Not enough history for this horizon. Omitted rather than
			// computed from the oldest available bar and labelled "1y",
			// which would silently answer a different question.
			continue
		}
		from := bars[idx]
		if from.Close <= 0 {
			continue
		}
		st.Returns = append(st.Returns, Return{
			Horizon:  h.Label,
			Percent:  round2((last.Close/from.Close - 1) * 100),
			From:     from.Close,
			FromDate: from.Time,
		})
	}

	// Volatility over the last year of bars, annualised by the usual root of
	// 252 trading days.
	window := bars
	if len(window) > 253 {
		window = window[len(window)-253:]
	}
	var rets []float64
	for i := 1; i < len(window); i++ {
		if window[i-1].Close > 0 {
			rets = append(rets, window[i].Close/window[i-1].Close-1)
		}
	}
	if len(rets) > 2 {
		var sum float64
		for _, r := range rets {
			sum += r
		}
		mean := sum / float64(len(rets))
		var sq float64
		for _, r := range rets {
			sq += (r - mean) * (r - mean)
		}
		sd := math.Sqrt(sq / float64(len(rets)-1))
		st.Volatility = round2(sd * math.Sqrt(252) * 100)
	}

	// Peak-to-trough over the same window.
	peak := window[0].Close
	worst := 0.0
	for _, b := range window {
		if b.Close > peak {
			peak = b.Close
		}
		if peak > 0 {
			if dd := (b.Close/peak - 1) * 100; dd < worst {
				worst = dd
			}
		}
	}
	st.MaxDrawdown = round2(worst)

	// 52-week extremes from the same window.
	st.High52W, st.Low52W = window[0].High, window[0].Low
	var volSum float64
	for _, b := range window {
		if b.High > st.High52W {
			st.High52W = b.High
		}
		if b.Low > 0 && b.Low < st.Low52W {
			st.Low52W = b.Low
		}
		volSum += b.Volume
	}
	if st.High52W > 0 {
		st.PctFrom52WHigh = round2((last.Close/st.High52W - 1) * 100)
	}
	if st.Low52W > 0 {
		st.PctFrom52WLow = round2((last.Close/st.Low52W - 1) * 100)
	}
	if n := len(window); n > 0 {
		st.AvgVolume = math.Round(volSum / float64(n))
		st.LastVolume = last.Volume
		if st.AvgVolume > 0 {
			st.VolumeRatio = round2(last.Volume / st.AvgVolume)
		}
	}
	return st
}

// maxMeasured caps how many instruments one question may price.
//
// A question naming an entire sector could otherwise fan out to every
// constituent, which is a lot of upstream requests for a reader who asked one
// thing. The cap is on the question, not the sector.
const maxMeasured = 6

// rankForMeasurement puts the question's own subjects first.
//
// Symbols named in the question are what the reader asked about. Symbols that
// merely appear in retrieved articles are context, and are worth measuring
// only once the subject is covered.
// valuations fetches fundamentals for the instruments a question is about.
func (e *Engine) valuations(ctx context.Context, symbols []string) []Valuation {
	if e.valuation == nil || len(symbols) == 0 {
		return nil
	}
	if len(symbols) > maxMeasured {
		symbols = symbols[:maxMeasured]
	}
	out := make([]Valuation, 0, len(symbols))
	for _, sym := range symbols {
		v, err := e.valuation.Valuation(ctx, sym)
		if err != nil || v == nil {
			continue
		}
		out = append(out, *v)
	}
	return out
}

func (e *Engine) rankForMeasurement(query string, fromFindings []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(list []string) {
		for _, s := range list {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	if e.resolve != nil {
		add(e.resolve(query))
	}
	add(fromFindings)
	return out
}

// measure prices the instruments a question is about.
//
// Failures are silent by design: market data is an enrichment here, and a
// research answer that already has ten sources should not fail because one
// price lookup timed out.
func (e *Engine) measure(ctx context.Context, symbols []string) []MarketStats {
	if e.prices == nil || len(symbols) == 0 {
		return nil
	}
	if len(symbols) > maxMeasured {
		symbols = symbols[:maxMeasured]
	}

	// Indexed by position so the ranked order survives concurrency. Sorting
	// the results alphabetically afterwards would undo the ranking that put
	// the question's own subject first, and quietly bury the one company the
	// reader asked about beneath five they did not.
	measured := make([]*MarketStats, len(symbols))
	var wg sync.WaitGroup
	for i, sym := range symbols {
		wg.Add(1)
		go func(i int, sym string) {
			defer wg.Done()
			bars, err := e.prices.DailyBars(ctx, sym, 400)
			if err != nil {
				e.log.Debug("no price history for research", "symbol", sym, "err", err)
				return
			}
			measured[i] = Measure(sym, bars)
		}(i, sym)
	}
	wg.Wait()

	out := make([]MarketStats, 0, len(symbols))
	for _, m := range measured {
		if m != nil {
			out = append(out, *m)
		}
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
