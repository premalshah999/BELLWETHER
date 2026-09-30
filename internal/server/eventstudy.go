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
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// Benchmark indices for the event study's abnormal-return calculation:
// GSPC.INDEX (S&P 500, -> Yahoo's ^GSPC) for US-venue symbols, NSEI.INDEX
// (NIFTY 50, -> ^NSEI) for NSE ones. The index itself, not an ETF, so the
// comparison is against what the market did rather than what one fund's
// tracking and fees did to it.
const (
	usBenchmarkSymbol   = "GSPC.INDEX"
	nseBenchmarkSymbol  = "NSEI.INDEX"
	defaultHoldingDays  = 5
	maxHoldingDays      = 60
	eventStudyCandleLen = 400
)

// EventStudyReader is the part of storage an event study reads.
type EventStudyReader interface {
	EventTypeSymbolPairs(ctx context.Context, eventType string) ([]postgres.EventSymbolPair, error)
	EventTypeCounts(ctx context.Context) ([]postgres.EventTypeCount, error)
	LoadCandles(ctx context.Context, sym marketdata.Symbol, interval marketdata.Interval, limit int) (marketdata.CachedSeries, error)
}

func (s *Server) eventStudyReader() (EventStudyReader, bool) {
	r, ok := s.deps.Store.(EventStudyReader)
	return r, ok
}

// handleEventStudyTypes lists every event type the archive holds at least
// one event under, most populous first. /api/events/types (handleEventTypes
// in events.go) already lists the full ~54-type taxonomy for the News
// page's filter; this is deliberately narrower, because most of that
// taxonomy is types a given deployment has never actually populated, and a
// study page's picker should not offer choices that can only ever answer
// "zero samples".
func (s *Server) handleEventStudyTypes(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.eventStudyReader()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Event types are not available.")
		return
	}
	counts, err := reader.EventTypeCounts(r.Context())
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
	reader, ok := s.eventStudyReader()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The event study engine is not available.")
		return
	}
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
	pairs, err := reader.EventTypeSymbolPairs(ctx, eventType)
	if err != nil {
		s.deps.Log.Error("event study pairs failed", "err", err, "type", eventType)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read events of this type.")
		return
	}

	var needUS, needNSE bool
	for _, p := range pairs {
		sym, err := marketdata.ParseSymbol(p.Symbol)
		if err != nil {
			continue
		}
		if sym.IsIndian() {
			needNSE = true
		} else {
			needUS = true
		}
	}

	// The benchmark is fetched through the router -- unlike the event
	// population's own symbols below, there are at most two of these, so a
	// cold-cache fetch here costs one real request per venue actually
	// present rather than one per event.
	var usBench, nseBench []marketdata.Candle
	if needUS {
		usBench = loadBenchmark(ctx, s, usBenchmarkSymbol)
	}
	if needNSE {
		nseBench = loadBenchmark(ctx, s, nseBenchmarkSymbol)
	}

	// Each event symbol's own series comes from the store only, never a
	// live fetch: an event type can span hundreds of distinct companies,
	// and turning a read-only analytics request into hundreds of possible
	// upstream calls would make its latency unpredictable and put load on
	// a vendor for data this app already collects on its own schedule (the
	// scanner's daily pass -- see internal/scanner's Result.Series). A
	// symbol with no cached history is a real, expected gap, reported by
	// eventstudy.Run's own coverage warning rather than papered over here.
	//
	// Read in parallel, bounded: one at a time, an event type spanning a few
	// hundred companies took seconds of back-to-back round trips.
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
			series, err := reader.LoadCandles(ctx, sym, marketdata.Interval1d, eventStudyCandleLen)
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
		sym, err := marketdata.ParseSymbol(p.Symbol)
		if err != nil {
			continue
		}
		bench := usBench
		if sym.IsIndian() {
			bench = nseBench
		}
		if len(bench) == 0 {
			continue
		}
		if smp, ok := eventstudy.BuildSample(p.Symbol, p.DiscoveredAt, days, candles, bench); ok {
			samples = append(samples, smp)
		}
	}

	res := eventstudy.Run(eventType, benchmarkLabel(needUS, needNSE), days, len(pairs), samples)
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

func benchmarkLabel(needUS, needNSE bool) string {
	switch {
	case needUS && needNSE:
		return "S&P 500 (US symbols) / NIFTY 50 (NSE symbols)"
	case needNSE:
		return "NIFTY 50"
	default:
		return "S&P 500"
	}
}
