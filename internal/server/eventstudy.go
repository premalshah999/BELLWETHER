package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/eventstudy"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// The event study measures abnormal returns against the S&P 500 index itself,
// not an ETF, so the comparison is with the market rather than one fund's
// tracking and fees.
const (
	benchmarkSymbol     = "GSPC.INDEX"
	defaultHoldingDays  = 5
	maxHoldingDays      = 60
	eventStudyCandleLen = 400
)

// handleEventStudyTypes lists every event type the archive holds at least
// one event under, most populous first. /api/events/types (handleEventTypes
// in events.go) already lists the full ~54-type taxonomy for the News
// page's filter; this is deliberately narrower, because most of that
// taxonomy is types a given deployment has never actually populated, and a
// study page's picker should not offer choices that can only ever answer
// "zero samples".
func (s *Server) handleEventStudyTypes(w http.ResponseWriter, r *http.Request) {
	counts, err := s.deps.Store.EventTypeCounts(r.Context())
	if err != nil {
		s.deps.Log.Error("event type counts failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read event types.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": counts})
}

// handleEventStudy answers "does this event type actually move the stocks
// it names, historically" -- see internal/eventstudy's package doc for the
// method and why discovered_at is the anchor.
func (s *Server) handleEventStudy(w http.ResponseWriter, r *http.Request) {
	if s.deps.Router == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "No market data router is configured.")
		return
	}

	eventType := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("type")))
	if eventType == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "type is required.")
		return
	}
	days := defaultHoldingDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxHoldingDays {
			writeError(w, http.StatusBadRequest, "invalid_request",
				fmt.Sprintf("days must be a whole number between 1 and %d.", maxHoldingDays))
			return
		}
		days = n
	}

	key := fmt.Sprintf("%s/%d", eventType, days)
	if res, ok := s.studies.get(key, s.deps.Now()); ok {
		writeJSON(w, http.StatusOK, res)
		return
	}

	ctx := r.Context()
	pairs, err := s.deps.Store.EventTypeSymbolPairs(ctx, eventType)
	if err != nil {
		s.deps.Log.Error("event study pairs failed", "err", err, "type", eventType)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read events of this type.")
		return
	}

	// Only symbols that parse: the archive still holds events on retired
	// Indian listings, which the parser refuses and which would otherwise
	// quietly change the numbers.
	us := pairs[:0]
	for _, p := range pairs {
		if _, err := marketdata.ParseSymbol(p.Symbol); err == nil {
			us = append(us, p)
		}
	}
	pairs = us

	// The benchmark goes through the router (one fetch at most); the event
	// symbols below come from the store only.
	bench := loadBenchmark(ctx, s, benchmarkSymbol)

	// Each symbol's series comes from the store, never a live fetch: an event
	// type can span hundreds of companies. A symbol with no stored history is
	// an expected gap, reported by the study's coverage warning. Read in
	// parallel, bounded.
	candlesBySymbol := map[string][]marketdata.Candle{}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 8)
		seen = map[string]bool{}
	)
	for _, p := range pairs {
		if seen[p.Symbol] {
			continue
		}
		seen[p.Symbol] = true
		sym, err := marketdata.ParseSymbol(p.Symbol)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(key string, sym marketdata.Symbol) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			series, err := s.deps.Store.LoadCandles(ctx, sym, marketdata.Interval1d, eventStudyCandleLen)
			if err != nil || len(series.Candles) == 0 {
				return
			}
			mu.Lock()
			candlesBySymbol[key] = series.Candles
			mu.Unlock()
		}(p.Symbol, sym)
	}
	wg.Wait()

	var samples []eventstudy.Sample
	for _, p := range pairs {
		candles := candlesBySymbol[p.Symbol]
		if len(candles) == 0 {
			continue
		}
		if len(bench) == 0 {
			continue
		}
		if smp, ok := eventstudy.BuildSample(p.Symbol, p.DiscoveredAt, days, candles, bench); ok {
			samples = append(samples, smp)
		}
	}

	res := eventstudy.Run(eventType, "S&P 500", days, len(pairs), samples)
	s.studies.put(key, res, s.deps.Now())
	writeJSON(w, http.StatusOK, res)
}

// studyTTL is how long a computed study is served before it is recomputed.
// Its inputs -- the events of a type and the persisted daily bars -- change
// when the scanner writes new bars a few times a day, so a quarter of an
// hour costs no accuracy anyone could notice, and it turns a page that asks
// for the same study on every visit from seconds into a map lookup.
const studyTTL = 15 * time.Minute

// studyCache holds computed studies by type and holding period. The zero
// value is ready to use.
type studyCache struct {
	mu sync.Mutex
	m  map[string]studyEntry
}

type studyEntry struct {
	at  time.Time
	res eventstudy.Result
}

func (c *studyCache) get(key string, now time.Time) (eventstudy.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) > studyTTL {
		return eventstudy.Result{}, false
	}
	return e.res, true
}

func (c *studyCache) put(key string, res eventstudy.Result, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]studyEntry{}
	}
	c.m[key] = studyEntry{at: now, res: res}
}

func loadBenchmark(ctx context.Context, s *Server, symbol string) []marketdata.Candle {
	sym, err := marketdata.ParseSymbol(symbol)
	if err != nil {
		return nil
	}
	series, err := s.deps.Router.Candles(ctx, sym, marketdata.Interval1d, eventStudyCandleLen)
	if err != nil {
		s.deps.Log.Warn("event study: could not load benchmark", "symbol", symbol, "err", err)
		return nil
	}
	return series.Candles
}
