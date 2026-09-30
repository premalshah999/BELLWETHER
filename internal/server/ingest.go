package server

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

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

	Healthy bool `json:"healthy"`
	// Chronic marks a source that has failed too many times in a row, with no
	// success recent enough to call it an outage. It is not a claim about the
	// cause -- only that this one will not fix itself.
	Chronic             bool       `json:"chronic"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastError           string     `json:"last_error,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	TotalAttempts       int        `json:"total_attempts"`
	TotalItems          int        `json:"total_items"`
	TotalNewItems       int        `json:"total_new_items"`
}

type ingestSummary struct {
	Sources []ingestSourceView `json:"sources"`
	Total   int                `json:"total"`
	Healthy int                `json:"healthy"`
	// Failing counts sources that are down right now; Chronic counts the
	// subset of those that have been down long enough to need a person. They
	// were one number, which is how a source that had 403'd 282 times in a row
	// sat next to a feed that had been rate-limited twice.
	Failing   int `json:"failing"`
	Chronic   int `json:"chronic"`
	Unpolled  int `json:"unpolled"`
	ItemsHeld int `json:"items_held"`
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
	// The engine's own registry, which includes the watchlist and scanner
	// sources added at runtime; a fresh catalogue would hide them.
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
		v.Chronic = polled && h.Chronic(s.now())
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
			if v.Chronic {
				out.Chronic++
			}
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
		if a.Chronic != b.Chronic {
			return a.Chronic
		}
		if a.Trust != b.Trust {
			return a.Trust > b.Trust
		}
		return a.ID < b.ID
	})

	if n, err := s.deps.Store.CountRawItems(r.Context()); err == nil {
		out.ItemsHeld = n
	}
	writeJSON(w, http.StatusOK, out)
}

// sourceLatencyView is one source's leaderboard row, with the display name
// the registry knows it by rather than its bare id.
type sourceLatencyView struct {
	SourceID       string   `json:"source_id"`
	Name           string   `json:"name"`
	Participated   int      `json:"participated"`
	TimesFirst     int      `json:"times_first"`
	WinRatePct     float64  `json:"win_rate_pct"`
	AvgLeadSeconds *float64 `json:"avg_lead_seconds,omitempty"`
}

// handleSourceLatency ranks sources by how often they were first to carry a
// story that at least one other source also carried -- "which feeds are
// actually worth the slot in the catalog", not "which feeds publish the
// most". See postgres.SourceLatencyLeaderboard's doc for the method.
func (s *Server) handleSourceLatency(w http.ResponseWriter, r *http.Request) {

	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	since := time.Now().AddDate(0, 0, -days)

	// Five is a floor, not a target: fewer appearances than that and a win
	// rate is describing a coin flip, not a pattern.
	board, err := s.deps.Store.SourceLatencyLeaderboard(r.Context(), since, 5)
	if err != nil {
		s.deps.Log.Error("source latency leaderboard failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the source leaderboard.")
		return
	}

	names := map[string]string{}
	if s.deps.Ingest != nil {
		for _, src := range s.deps.Ingest.Registry().All() {
			names[src.ID] = src.Name
		}
	}

	out := make([]sourceLatencyView, 0, len(board))
	for _, l := range board {
		name := names[l.SourceID]
		if name == "" {
			name = l.SourceID
		}
		v := sourceLatencyView{
			SourceID: l.SourceID, Name: name,
			Participated: l.Participated, TimesFirst: l.TimesFirst,
			AvgLeadSeconds: l.AvgLeadSeconds,
		}
		if l.Participated > 0 {
			v.WinRatePct = round1(float64(l.TimesFirst) / float64(l.Participated) * 100)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out, "days": days})
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
