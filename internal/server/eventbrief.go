package server

import (
	"net/http"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/news"
)

// handleEventBrief writes, or returns, one item's brief.
//
// Generated on demand rather than for every item that arrives. The feed takes
// several thousand items a day and almost all of them are procedural; briefing
// all of them would spend the month's budget on shareholding patterns. A brief
// is written when someone asks for one, and kept so the second person to look
// does not pay for it again.
func (s *Server) handleEventBrief(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "event")
	if !ok {
		return
	}

	// Already written: hand it back rather than paying for it twice.
	if existing, err := s.deps.Store.EventBrief(r.Context(), id); err == nil && existing != "" {
		writeJSON(w, http.StatusOK, map[string]any{"brief": existing, "cached": true})
		return
	}

	if s.deps.AI == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The AI service is not available.")
		return
	}

	ev, err := s.deps.Store.GetEvent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such event.")
		return
	}

	brief, model, err := s.deps.AI.BriefEvent(r.Context(), briefInput(&ev))
	if err != nil {
		s.deps.Log.Warn("could not brief an event", "event", id, "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable",
			"A brief could not be written: "+err.Error())
		return
	}
	if brief == "" {
		writeError(w, http.StatusBadGateway, "ai_unavailable", "The model returned nothing.")
		return
	}
	if err := s.deps.Store.SaveEventBrief(r.Context(), id, brief, model); err != nil {
		// The brief is worth returning even if it could not be kept; the only
		// cost is that the next s.deps.Store pays for it again.
		s.deps.Log.Warn("could not s.deps.Store an event brief", "event", id, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"brief": brief, "cached": false})
}

// handleRunBriefs writes briefs for the important items that have none.
//
// The same work the hourly job does, on demand. It sits alongside the other
// model-backed triggers for the same reason they do: after changing a prompt
// or a threshold, waiting up to an hour to see the effect is how a change
// ships unverified.
func (s *Server) handleRunBriefs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAI(w, r) {
		return
	}
	// Smaller than the hourly job's cap: this one runs inside a request
	// timeout, and the scheduled pass is where a backlog is actually cleared.
	limit := clampInt(intParam(r, "limit", 20), 1, 60)
	n, err := s.deps.AI.BriefEvents(r.Context(), s.deps.Store, limit)
	if err != nil {
		s.deps.Log.Warn("briefing run failed", "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable", "Briefing failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"written": n, "requested": limit})
}

// briefInput reduces an event to what the prompt is written from.
func briefInput(ev *news.Event) ai.EventInput {
	in := ai.EventInput{
		Headline:  ev.Headline,
		Body:      ev.Summary,
		EventType: ev.Type,
		Official:  ev.Official,
		Published: ev.PublishedAt,
	}
	if in.Published.IsZero() {
		in.Published = ev.DiscoveredAt
	}
	for _, e := range ev.Entities {
		in.Companies = append(in.Companies, e.Symbol)
	}
	// The evidence carries the fuller text where a summary was never written.
	// A filing's description is often the only prose it has.
	if in.Body == "" {
		for _, raw := range ev.Evidence {
			if raw.Description != "" {
				in.Body = raw.Description
				in.Source = raw.Publisher
				break
			}
		}
	}
	if in.Source == "" && len(ev.Evidence) > 0 {
		in.Source = ev.Evidence[0].Publisher
		if in.Source == "" {
			in.Source = ev.Evidence[0].SourceID
		}
	}
	return in
}
