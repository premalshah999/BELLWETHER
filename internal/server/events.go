package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/events"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
	"github.com/tradesys/dashboard/internal/storage"
)

// handleSymbolSectors reports each requested symbol's industry, already
// normalised to the same spelling an event's own Sectors carries -- so the
// Geopolitics & Policy page can find "which of my holdings does this touch"
// with a plain set membership check, no per-venue logic of its own.
func (s *Server) handleSymbolSectors(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("symbols"))
	if raw == "" {
		writeJSON(w, http.StatusOK, map[string]any{"sectors": map[string]string{}})
		return
	}
	var symbols []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			symbols = append(symbols, s)
		}
	}
	if len(symbols) > 100 {
		symbols = symbols[:100]
	}
	sectors, err := s.deps.Store.SectorsFor(r.Context(), symbols)
	if err != nil {
		s.deps.Log.Error("sector lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read sectors.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sectors": sectors})
}

// eventEnvelope is one event as the UI receives it.
type eventEnvelope struct {
	news.Event
	Sectors []string          `json:"sectors,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
	// AgeLabel saves every client reimplementing relative time formatting,
	// and keeps "3m ago" meaning the same thing everywhere it appears.
	AgeLabel string `json:"age_label"`
	// LatencySeconds is how long after publication we discovered the event.
	// It is exposed rather than hidden because a feed that claims to be fast
	// should be auditable, and because a source whose latency degrades is a
	// problem worth seeing before it becomes a gap.
	LatencySeconds *int64 `json:"latency_seconds,omitempty"`
}

type eventListResponse struct {
	Events []eventEnvelope `json:"events"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// handleEvents serves the news and filings feed.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := storage.EventFilter{
		Symbol:       strings.TrimSpace(q.Get("symbol")),
		Query:        strings.TrimSpace(q.Get("q")),
		OfficialOnly: q.Get("official") == "1" || q.Get("official") == "true",
		Limit:        min(atoiDefault(q.Get("limit"), 60), 500),
		Offset:       atoiDefault(q.Get("offset"), 0),
	}
	if t := strings.TrimSpace(q.Get("type")); t != "" {
		for _, part := range strings.Split(t, ",") {
			if part = strings.ToUpper(strings.TrimSpace(part)); part != "" {
				filter.Types = append(filter.Types, part)
			}
		}
	}
	if v := q.Get("min_importance"); v != "" {
		filter.MinImportance = atoiDefault(v, 0)
	}
	// Index constituents only by default, so the parameter turns it off. A
	// search is never narrowed: someone typing a query wants matches to it,
	// and an empty answer reads as "there is no news".
	filter.IndexOnly = q.Get("universe") != "all" && strings.TrimSpace(filter.Query) == ""

	// Newest published first unless the caller asks for arrival order, which
	// is for auditing what came in and when.
	filter.OrderByContentAge = q.Get("order") != "arrival"
	filter.MacroTypes = events.SectorScopeTypes()
	// The default window is deliberately short. This is a feed of what is
	// happening, and an unbounded query over a growing archive is both slower
	// and less useful than one over the last few days.
	hours := atoiDefault(q.Get("hours"), 72)
	if hours > 0 {
		filter.Since = s.now().Add(-time.Duration(hours) * time.Hour)
	}

	// The archive is consulted only when the window reaches past the hot
	// cutoff, which the default 72-hour feed never does. When it is not
	// configured this is the single-database path unchanged.
	var (
		list []news.Event
		err  error
	)
	if a := s.deps.NewsArchive; a != nil {
		list, err = a.ListEvents(r.Context(), filter, s.now())
	} else {
		list, err = s.deps.Store.ListEvents(r.Context(), filter)
	}
	if err != nil {
		s.deps.Log.Error("list events failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read events.")
		return
	}

	// Briefs already written are attached here, in one query for the whole
	// page. Nothing is generated: writing a brief costs a model call, and
	// doing that for every item scrolled past would spend the month's budget
	// on shareholding patterns.
	briefs := map[int64]string{}
	if len(list) > 0 {
		ids := make([]int64, 0, len(list))
		for _, e := range list {
			ids = append(ids, e.ID)
		}
		if got, err := s.deps.Store.EventBriefs(r.Context(), ids); err == nil {
			briefs = got
		} else {
			s.deps.Log.Warn("could not read event briefs", "err", err)
		}
	}

	out := eventListResponse{
		Events: make([]eventEnvelope, 0, len(list)),
		Total:  len(list), Limit: filter.Limit, Offset: filter.Offset,
	}
	now := s.now()
	for _, e := range list {
		e.Brief = briefs[e.ID]
		out.Events = append(out.Events, s.envelope(e, now))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleEvent serves one event with all its evidence.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "event")
	if !ok {
		return
	}
	e, err := s.deps.Store.GetEvent(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "No such event.")
		return
	}
	if err != nil {
		s.deps.Log.Error("get event failed", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the event.")
		return
	}

	// The brief, if one has been written. Attached here as well as on the
	// list: opening a single item is exactly when a reader wants the note,
	// and wiring it into only one of the two routes meant the feed showed a
	// brief that vanished when you clicked into it.
	if brief, err := s.deps.Store.EventBrief(r.Context(), id); err == nil {
		e.Brief = brief
	}

	env := s.envelope(e, s.now())
	if facts, err := s.deps.Store.EventFacts(r.Context(), id); err == nil && len(facts) > 0 {
		env.Facts = facts
	}
	if sectors, err := s.deps.Store.EventSectors(r.Context(), id); err == nil && len(sectors) > 0 {
		env.Sectors = sectors
	}
	writeJSON(w, http.StatusOK, env)
}

// envelope decorates an event for transport.
func (s *Server) envelope(e news.Event, now time.Time) eventEnvelope {
	e.Staleness = news.StalenessAt(e.PublishedAt, e.TimestampTrust, now)
	e.Source = s.sourceLabel(e)

	env := eventEnvelope{Event: e, AgeLabel: relativeAge(e.DiscoveredAt, now)}
	// Latency is only meaningful against a timestamp we believe. An
	// aggregator's is its own sighting, so the difference would measure the
	// gap between two observers rather than how far behind a publisher we are.
	if !e.PublishedAt.IsZero() && !e.DiscoveredAt.IsZero() && e.TimestampTrust.Reliable() {
		if d := e.DiscoveredAt.Sub(e.PublishedAt); d >= 0 {
			secs := int64(d.Seconds())
			env.LatencySeconds = &secs
		}
	}
	return env
}

// eventTypeInfo describes the taxonomy to the UI, so filter menus are built
// from the same list the classifier uses rather than a hand-copied one.
type eventTypeInfo struct {
	Type       string `json:"type"`
	Importance int    `json:"baseline_importance"`
}

func (s *Server) handleEventTypes(w http.ResponseWriter, r *http.Request) {
	names := events.AllTypeNames()
	out := make([]eventTypeInfo, 0, len(names))
	for _, n := range names {
		out = append(out, eventTypeInfo{Type: n, Importance: events.Type(n).BaselineImportance()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

// now is the server clock, injectable so tests are deterministic.
func (s *Server) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

func relativeAge(t, now time.Time) string {
	if t.IsZero() {
		return "undated"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	default:
		return t.Format("2 Jan")
	}
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// handleSymbolDebrief writes an account of everything collected about an
// instrument.
//
// Distinct from research: research goes out to the web, this reads only what
// has already been collected and verified. It is the answer to "I have not
// looked at this position in a month" — and because it works from the event
// archive, every claim in it can be traced to a filing.
func (s *Server) handleSymbolDebrief(w http.ResponseWriter, r *http.Request) {
	if !s.requireAI(w, r) {
		return
	}
	sym, err := marketdata.ParseSymbol(chi.URLParam(r, "symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Unrecognised symbol.")
		return
	}

	days := clampInt(intParam(r, "days", 30), 1, 365)
	list, err := s.deps.Store.ListEvents(r.Context(), storage.EventFilter{
		Symbol: sym.String(),
		Since:  s.now().AddDate(0, 0, -days),
		Limit:  200,
	})
	if err != nil {
		s.deps.Log.Error("debrief events failed", "symbol", sym.String(), "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read events.")
		return
	}

	var company, industry string
	if s.deps.Companies != nil {
		if c, found := s.deps.Companies.Lookup(sym.String()); found {
			company = c.Name
		}
	}
	if sectors, err := s.deps.Store.SectorsFor(r.Context(), []string{sym.String()}); err == nil {
		industry = sectors[sym.String()]
	}

	out, err := s.deps.AI.Debrief(r.Context(), s.deps.Store, sym.String(), company, industry, list, days)
	if err != nil {
		s.deps.Log.Warn("debrief failed", "symbol", sym.String(), "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable", "The debrief failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// sourceLabel names who reported an event. A direct feed is named by its
// catalogue entry; a discovery source (a search) by the item's own publisher,
// because "Google News" would be the one label guaranteed to be wrong. Falls
// back to the publisher when the source has left the catalogue.
func (s *Server) sourceLabel(e news.Event) string {
	publisher := cleanPublisher(e.PrimaryPublisher)
	if s.deps.Ingest == nil || e.PrimarySourceID == "" {
		return publisher
	}
	src, ok := s.deps.Ingest.Registry().Get(e.PrimarySourceID)
	if !ok {
		return publisher
	}
	discovery := src.Method == news.MethodGoogleNews || src.Method == news.MethodGDELT ||
		strings.HasPrefix(src.ID, "watch-")
	if discovery && publisher != "" {
		return publisher
	}
	return src.Name
}

// cleanPublisher turns a bare domain into something a person would call the
// outlet: "www.wsj.com" and "wsj.com" both become "wsj.com", and a trailing
// ".com" on a name that is already a name ("Bloomberg.com") is dropped.
func cleanPublisher(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "www.")
	if strings.ContainsAny(p, " ") || (len(p) > 0 && p[0] >= 'A' && p[0] <= 'Z') {
		p = strings.TrimSuffix(p, ".com")
	}
	return p
}
