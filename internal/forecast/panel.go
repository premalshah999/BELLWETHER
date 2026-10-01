package forecast

import (
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Panel is every series the engine reads, aligned on the market's own
// sessions: the S&P 500's daily bars define the calendar, and each stock and
// VIX are placed on it by New York trading date.
//
// Prices are float32: five years of five fields for 1,500 stocks is 45 MB
// instead of 90, inside a container capped at 512 MB, and a 24-bit mantissa
// resolves a price to one part in sixteen million.
type Panel struct {
	Days   []time.Time // sessions, midnight New York, oldest first
	closes []time.Time // 4pm New York on each session
	index  map[string]int
	Market *Series
	// VIX is the index's close on each session, NaN where missing.
	VIX    []float32
	Stocks []*Series
}

// Series is one instrument on the panel's calendar.
type Series struct {
	Symbol string
	Sector string
	// O, H, L, C, V are the session's bar. Before First they are NaN; a
	// session missing after First carries the previous close with zero
	// volume, so returns across a gap are measured once, on reopening.
	O, H, L, C, V []float32
	First         int
	Earnings      []EarningEvent
	Insiders      []Insider

	// Derived on demand by derive.
	r, rv   []float32 // log return and range-based variance per session
	earnDay []bool    // the session traded the first reaction to earnings
}

// Earning is a past earnings announcement as the store records it.
type Earning struct {
	At          time.Time
	SurprisePct float64
}

// Insider is one Form 4 filing's open-market total.
type Insider struct {
	Filed time.Time
	Value float64
	Buy   bool
}

// EarningEvent is an announcement placed on the calendar: Session is the
// first session to close after it, the first price that could react.
type EarningEvent struct {
	At          time.Time
	Session     int
	SurprisePct float64
}

func dayKey(t time.Time) string { return t.In(marketdata.Market).Format("2006-01-02") }

func sessionCloseOf(day time.Time) time.Time {
	y, m, d := day.In(marketdata.Market).Date()
	return time.Date(y, m, d, 16, 0, 0, 0, marketdata.Market)
}

// NewPanel builds the calendar from the benchmark's bars.
func NewPanel(market, vix []marketdata.Candle) *Panel {
	p := &Panel{index: map[string]int{}}
	for _, b := range market {
		k := dayKey(b.Time)
		if _, dup := p.index[k]; dup {
			continue
		}
		y, m, d := b.Time.In(marketdata.Market).Date()
		day := time.Date(y, m, d, 0, 0, 0, 0, marketdata.Market)
		p.index[k] = len(p.Days)
		p.Days = append(p.Days, day)
		p.closes = append(p.closes, sessionCloseOf(day))
	}
	p.Market = p.align("GSPC.INDEX", "", market)
	p.VIX = nanSlice(len(p.Days))
	for _, b := range vix {
		if i, ok := p.index[dayKey(b.Time)]; ok {
			p.VIX[i] = float32(b.Close)
		}
	}
	return p
}

// T is the number of sessions.
func (p *Panel) T() int { return len(p.Days) }

// SessionAfter is the first session whose close is after t, or T() if none.
func (p *Panel) SessionAfter(t time.Time) int {
	return sort.Search(len(p.closes), func(i int) bool { return p.closes[i].After(t) })
}

// AddStock places a stock on the calendar with its filings.
func (p *Panel) AddStock(symbol, sector string, bars []marketdata.Candle, earns []Earning, ins []Insider) *Series {
	s := p.align(symbol, sector, bars)
	if s == nil {
		return nil
	}
	for _, e := range earns {
		i := p.SessionAfter(e.At)
		if i <= s.First || i >= p.T()+30 {
			continue
		}
		s.Earnings = append(s.Earnings, EarningEvent{At: e.At, Session: i, SurprisePct: e.SurprisePct})
	}
	sort.Slice(s.Earnings, func(a, b int) bool { return s.Earnings[a].Session < s.Earnings[b].Session })
	s.Insiders = ins
	p.Stocks = append(p.Stocks, s)
	return s
}

func nanSlice(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(math.NaN())
	}
	return out
}

func (p *Panel) align(symbol, sector string, bars []marketdata.Candle) *Series {
	T := p.T()
	s := &Series{Symbol: symbol, Sector: sector, O: nanSlice(T), H: nanSlice(T), L: nanSlice(T), C: nanSlice(T), V: make([]float32, T), First: -1}
	for _, b := range bars {
		i, ok := p.index[dayKey(b.Time)]
		if !ok || b.Close <= 0 {
			continue
		}
		s.O[i], s.H[i], s.L[i], s.C[i], s.V[i] = float32(b.Open), float32(b.High), float32(b.Low), float32(b.Close), float32(b.Volume)
		if s.First < 0 || i < s.First {
			s.First = i
		}
	}
	if s.First < 0 {
		return nil
	}
	for i := s.First; i < T; i++ {
		if isNaN32(s.C[i]) {
			c := s.C[i-1]
			s.O[i], s.H[i], s.L[i], s.C[i], s.V[i] = c, c, c, c, 0
		}
		// A bar with a missing or impossible range is treated as flat
		// rather than allowed to produce a negative variance.
		if !(s.O[i] > 0) || isNaN32(s.O[i]) {
			s.O[i] = s.C[i]
		}
		if !(s.H[i] >= s.L[i]) || !(s.L[i] > 0) {
			s.H[i], s.L[i] = max(s.O[i], s.C[i]), min(s.O[i], s.C[i])
		}
	}
	return s
}

func isNaN32(x float32) bool { return x != x }

// derive computes the session log returns, the range-based variance and the
// earnings-day mask once per series.
//
// The daily variance proxy is the squared overnight return plus the
// Garman-Klass estimate from the session's open, high, low and close, which
// is several times more efficient than the squared close-to-close return
// (Garman and Klass, 1980).
func (s *Series) derive() {
	if s.r != nil {
		return
	}
	T := len(s.C)
	s.r = make([]float32, T)
	s.rv = make([]float32, T)
	s.earnDay = make([]bool, T)
	k := 2*math.Ln2 - 1
	for i := s.First + 1; i < T; i++ {
		c0, o, h, l, c := float64(s.C[i-1]), float64(s.O[i]), float64(s.H[i]), float64(s.L[i]), float64(s.C[i])
		s.r[i] = float32(math.Log(c / c0))
		on := math.Log(o / c0)
		hl := math.Log(h / l)
		co := math.Log(c / o)
		v := on*on + 0.5*hl*hl - k*co*co
		if v < 0 || math.IsNaN(v) {
			v = 0
		}
		s.rv[i] = float32(v)
	}
	for _, e := range s.Earnings {
		if e.Session > s.First && e.Session < T {
			s.earnDay[e.Session] = true
		}
	}
	// The low is read only here; nothing after needs it.
	s.L = nil
}

// ok reports whether the series has bars from at least `need` sessions
// before t through t.
func (s *Series) ok(t, need int) bool { return s.First >= 0 && t-need >= s.First && t < len(s.C) }

// nextEarnings is the first earnings reaction session after t, or -1.
func (s *Series) nextEarnings(t int) int {
	i := sort.Search(len(s.Earnings), func(k int) bool { return s.Earnings[k].Session > t })
	if i < len(s.Earnings) {
		return s.Earnings[i].Session
	}
	return -1
}
