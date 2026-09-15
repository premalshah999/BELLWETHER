package server

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// rawItemCounter is the optional part of the store this handler can use.
type rawItemCounter interface {
	CountRawItems(ctx context.Context) (int, error)
}

// ingestSourceView is one source as the operator sees it: its configuration
// and its recent behaviour in a single row.
type ingestSourceView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Method   string `json:"method"`
	Country  string `json:"country,omitempty"`

	Trust     int    `json:"trust"`
	Usage     string `json:"usage"`
	Display   string `json:"display"`
	Official  bool   `json:"official"`
	RefreshMS int64  `json:"refresh_ms"`

	Healthy             bool       `json:"healthy"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastError           string     `json:"last_error,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	TotalAttempts       int        `json:"total_attempts"`
	TotalItems          int        `json:"total_items"`
	TotalNewItems       int        `json:"total_new_items"`
}

type ingestSummary struct {
	Sources   []ingestSourceView `json:"sources"`
	Total     int                `json:"total"`
	Healthy   int                `json:"healthy"`
	Failing   int                `json:"failing"`
	Unpolled  int                `json:"unpolled"`
	ItemsHeld int                `json:"items_held"`
}

// handleIngestSources reports the state of every configured source.
//
// This is the operational view the ingestion layer needs in order to be
// trustworthy: a source that quietly stopped delivering looks exactly like a
// quiet news day unless something is watching for it.
func (s *Server) handleIngestSources(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ingest == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ingestion is not running.")
		return
	}
	// The engine's own registry, not a fresh catalogue.
	//
	// This built DefaultRegistry() and reported on that, which is the static
	// catalogue and nothing else. Every dynamically added source — one per
	// watched instrument, plus whatever the scanner flagged as moving without
	// explanation — was therefore invisible here, health and all. On a
	// fifteen-name watchlist that is fifteen sources the operator was told
	// nothing about, on a page whose entire purpose is to show that a source
	// has stopped delivering.
	registry := s.deps.Ingest.Registry()

	byID := map[string]news.SourceHealth{}
	for _, h := range s.deps.Ingest.Health() {
		byID[h.SourceID] = h
	}

	out := ingestSummary{Sources: make([]ingestSourceView, 0, registry.Len())}
	for _, src := range registry.All() {
		h, polled := byID[src.ID]
		v := ingestSourceView{
			ID: src.ID, Name: src.Name, Category: src.Category,
			Method: string(src.Method), Country: src.Country,
			Trust: src.Trust, Usage: string(src.Usage), Display: string(src.Display),
			Official: src.Official(), RefreshMS: src.Refresh.Milliseconds(),
			ConsecutiveFailures: h.ConsecutiveFailures, LastError: h.LastError,
			TotalAttempts: h.TotalAttempts, TotalItems: h.TotalItems,
			TotalNewItems: h.TotalNewItems,
		}
		v.Healthy = polled && h.Healthy()
		if !h.LastSuccessAt.IsZero() {
			t := h.LastSuccessAt
			v.LastSuccessAt = &t
		}
		if !h.LastAttemptAt.IsZero() {
			t := h.LastAttemptAt
			v.LastAttemptAt = &t
		}
		switch {
		case !polled || h.TotalAttempts == 0:
			out.Unpolled++
		case h.Healthy():
			out.Healthy++
		default:
			out.Failing++
		}
		out.Sources = append(out.Sources, v)
	}
	out.Total = len(out.Sources)

	// Failing sources first: this page exists to surface problems, and a
	// broken source buried alphabetically among forty healthy ones is a
	// problem nobody sees.
	sort.SliceStable(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		if a.Healthy != b.Healthy {
			return !a.Healthy
		}
		if a.Trust != b.Trust {
			return a.Trust > b.Trust
		}
		return a.ID < b.ID
	})

	// The item count is an optional extra: the storage interface does not
	// require it, and a store that cannot answer simply reports zero rather
	// than failing the whole page.
	if counter, ok := s.deps.Store.(rawItemCounter); ok {
		if n, err := counter.CountRawItems(r.Context()); err == nil {
			out.ItemsHeld = n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// pipelineView is what the ingestion pipeline is currently doing.
//
// It exists because adaptive scheduling is otherwise invisible: an operator
// seeing stale news needs to distinguish "the source is broken" from "the
// scheduler decided this feed is quiet" from "it is 3am and Indian sources are
// deliberately slow". Those have very different remedies.
type pipelineView struct {
	Phase   string `json:"phase"`
	PhaseAt string `json:"phase_as_of"`

	Lanes map[string]int `json:"lanes"`

	// Hot lists what the system currently considers eventful, and why.
	HotSymbols []news.HotEntry `json:"hot_symbols"`
	HotSectors []news.HotEntry `json:"hot_sectors"`

	// Due is the next few scheduled fetches, soonest first.
	Due []pipelineDue `json:"due"`

	Sources  int `json:"sources"`
	Healthy  int `json:"healthy"`
	Failing  int `json:"failing"`
	Freshest int `json:"freshest_seconds"`
	Stalest  int `json:"stalest_seconds"`
}

type pipelineDue struct {
	SourceID   string `json:"source_id"`
	Lane       string `json:"lane"`
	InSeconds  int    `json:"in_seconds"`
	IntervalMS int64  `json:"interval_ms"`
	EmptyPolls int    `json:"empty_polls"`
}

// handlePipeline reports scheduling state.
func (s *Server) handlePipeline(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ingest == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Ingestion is not running.")
		return
	}
	now := s.now()
	out := pipelineView{
		Phase:   string(s.deps.Ingest.Phase()),
		PhaseAt: now.UTC().Format(time.RFC3339),
		Lanes:   map[string]int{},
	}

	if s.deps.Attention != nil {
		out.HotSymbols = s.deps.Attention.HotSymbols(20)
		out.HotSectors = s.deps.Attention.HotSectors(10)
	}
	// Never report a nil slice as JSON null; a client iterating it should not
	// have to special-case the empty case.
	if out.HotSymbols == nil {
		out.HotSymbols = []news.HotEntry{}
	}
	if out.HotSectors == nil {
		out.HotSectors = []news.HotEntry{}
	}

	for _, sched := range s.deps.Ingest.Schedules() {
		out.Lanes[string(sched.Lane)]++
		if len(out.Due) < 12 {
			in := int(sched.NextDue.Sub(now).Seconds())
			if in < 0 {
				in = 0
			}
			out.Due = append(out.Due, pipelineDue{
				SourceID: sched.SourceID, Lane: string(sched.Lane),
				InSeconds: in, IntervalMS: sched.IntervalMS, EmptyPolls: sched.EmptyPolls,
			})
		}
	}
	if out.Due == nil {
		out.Due = []pipelineDue{}
	}

	// Freshness across the catalog. An AI answer that says "no material news"
	// while half the sources are stale is worse than no answer, so the
	// staleness is surfaced rather than left implicit.
	freshest, stalest := -1, -1
	for _, h := range s.deps.Ingest.Health() {
		out.Sources++
		if h.Healthy() {
			out.Healthy++
		} else {
			out.Failing++
		}
		if h.LastSuccessAt.IsZero() {
			continue
		}
		age := int(now.Sub(h.LastSuccessAt).Seconds())
		if freshest < 0 || age < freshest {
			freshest = age
		}
		if age > stalest {
			stalest = age
		}
	}
	if freshest > 0 {
		out.Freshest = freshest
	}
	if stalest > 0 {
		out.Stalest = stalest
	}
	writeJSON(w, http.StatusOK, out)
}
