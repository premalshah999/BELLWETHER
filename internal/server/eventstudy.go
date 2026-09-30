package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/eventstudy"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// The event study measures abnormal returns against the S&P 500 index itself,
// not an ETF, so the comparison is with the market rather than one fund's
// tracking and fees.
const (
	benchmarkSymbol    = "GSPC.INDEX"
	defaultHoldingDays = 5
	maxHoldingDays     = 60
	// studyBars is how much daily history a study reads per symbol: all of
	// what is stored, about five years.
	studyBars = 1500
	// earningsKind is the study of earnings announcements with their EPS
	// surprise, drawn from the backfilled history rather than the news archive.
	earningsKind = "EARNINGS_SURPRISE"
)

// surpriseGroups orders the earnings study's groups from best to worst.
var surpriseGroups = []string{"big beat (10%+)", "beat (2-10%)", "in line (±2%)", "miss (2-10%)", "big miss (10%+)"}

func surpriseGroup(pct float64) string {
	switch {
	case pct >= 10:
		return surpriseGroups[0]
	case pct >= 2:
		return surpriseGroups[1]
	case pct > -2:
		return surpriseGroups[2]
	case pct > -10:
		return surpriseGroups[3]
	default:
		return surpriseGroups[4]
	}
}

// directionGroups is how the classifier judged an event for its company.
var directionGroups = []string{"positive", "negative", "unclear"}

// handleEventStudyTypes lists what can be studied: the earnings history when
// it has been backfilled, then every event type the archive holds, most
// populous first.
func (s *Server) handleEventStudyTypes(w http.ResponseWriter, r *http.Request) {
	counts, err := s.deps.Store.EventTypeCounts(r.Context())
	if err != nil {
		s.deps.Log.Error("event type counts failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read event types.")
		return
	}
	if a := s.deps.NewsArchive; a != nil {
		if cold, err := a.DB().EventTypeCounts(r.Context()); err == nil {
			merged := map[string]int{}
			for _, c := range append(counts, cold...) {
				merged[c.EventType] += c.Count
			}
			counts = counts[:0]
			for t, n := range merged {
				counts = append(counts, postgres.EventTypeCount{EventType: t, Count: n})
			}
			sort.Slice(counts, func(i, j int) bool {
				if counts[i].Count != counts[j].Count {
					return counts[i].Count > counts[j].Count
				}
				return counts[i].EventType < counts[j].EventType
			})
		}
	}
	if n, err := s.deps.Store.EarningsCount(r.Context()); err == nil && n > 0 {
		counts = append([]postgres.EventTypeCount{{EventType: earningsKind, Count: n}}, counts...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": counts})
}

// studyEvent is one event to measure: a company, the moment it became
// knowable, and the group it falls in.
type studyEvent struct {
	at    time.Time
	group string
}

// handleEventStudy answers "does this kind of event move the stocks it
// names" -- see internal/eventstudy for the method.
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
	events, total, order, err := s.studyEvents(ctx, eventType)
	if err != nil {
		s.deps.Log.Error("event study read failed", "err", err, "type", eventType)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read events of this type.")
		return
	}
	bench := loadBenchmark(ctx, s, benchmarkSymbol)

	// One symbol at a time per worker: its bars are read, its events measured,
	// and the bars dropped, so five years of history for a thousand companies
	// never sits in memory at once.
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8)
		samples []eventstudy.Sample
	)
	if len(bench) > 0 {
		for symbol, evs := range events {
			sym, err := marketdata.ParseSymbol(symbol)
			if err != nil {
				continue
			}
			wg.Add(1)
			go func(sym marketdata.Symbol, evs []studyEvent) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				series, err := s.deps.Store.LoadCandles(ctx, sym, marketdata.Interval1d, studyBars)
				if err != nil || len(series.Candles) == 0 {
					return
				}
				var mine []eventstudy.Sample
				for _, ev := range evs {
					if smp, ok := eventstudy.BuildSample(sym.String(), ev.at, days, series.Candles, bench); ok {
						smp.Group = ev.group
						mine = append(mine, smp)
					}
				}
				mu.Lock()
				samples = append(samples, mine...)
				mu.Unlock()
			}(sym, evs)
		}
		wg.Wait()
	}

	res := eventstudy.Run(eventType, "S&P 500", days, total, samples, order...)
	s.studies.put(key, res, s.deps.Now())
	writeJSON(w, http.StatusOK, res)
}

// studyEvents gathers the events of one kind by symbol, with how many there
// were before any coverage filter and the order their groups report in.
func (s *Server) studyEvents(ctx context.Context, kind string) (map[string][]studyEvent, int, []string, error) {
	out := map[string][]studyEvent{}
	if kind == earningsKind {
		rows, err := s.deps.Store.EarningsHistory(ctx)
		if err != nil {
			return nil, 0, nil, err
		}
		for _, e := range rows {
			out[e.Symbol] = append(out[e.Symbol], studyEvent{at: e.AnnouncedAt, group: surpriseGroup(*e.SurprisePct)})
		}
		return out, len(rows), surpriseGroups, nil
	}

	pairs, err := s.deps.Store.EventTypeSymbolPairs(ctx, kind)
	if err != nil {
		return nil, 0, nil, err
	}
	if a := s.deps.NewsArchive; a != nil {
		if cold, err := a.DB().EventTypeSymbolPairs(ctx, kind); err == nil {
			pairs = append(pairs, cold...)
		} else {
			s.deps.Log.Warn("event study: archive unavailable", "err", err)
		}
	}
	total := 0
	for _, p := range pairs {
		// Only symbols that parse: the archive still holds events on listings
		// the app no longer covers.
		if _, err := marketdata.ParseSymbol(p.Symbol); err != nil {
			continue
		}
		group := p.Direction
		if group == "" {
			group = "unclear"
		}
		out[p.Symbol] = append(out[p.Symbol], studyEvent{at: p.DiscoveredAt, group: group})
		total++
	}
	return out, total, directionGroups, nil
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
	series, err := s.deps.Router.Candles(ctx, sym, marketdata.Interval1d, studyBars)
	if err != nil {
		s.deps.Log.Warn("event study: could not load benchmark", "symbol", symbol, "err", err)
		return nil
	}
	return series.Candles
}
