package server

import (
	"net/http"
	"strings"
	"time"
)

// webResult is one headline found on the open web.
type webResult struct {
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Publisher   string     `json:"publisher"`
	Snippet     string     `json:"snippet,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Engine      string     `json:"engine"`
	Symbols     []string   `json:"symbols,omitempty"`
}

// handleWebSearch searches the open web, beyond the curated sources: the News
// page's "from the web" results, the headlines beside a chart, and the first
// place to look when a stock moves with nothing in the archive to explain it.
func (s *Server) handleWebSearch(w http.ResponseWriter, r *http.Request) {
	if s.deps.Research == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Web search is not configured.")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "news"
	}
	if q == "" || len(q) > 200 || (kind != "news" && kind != "web") {
		writeError(w, http.StatusBadRequest, "bad_request", "Give a query of up to 200 characters and a kind of news or web.")
		return
	}
	found, engines, err := s.deps.Research.Web(r.Context(), q, kind, clampInt(intParam(r, "limit", 12), 1, 40))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Web search is not available right now.")
		return
	}
	results := make([]webResult, 0, len(found))
	for _, f := range found {
		// Only web links: a result is rendered as an anchor.
		if !strings.HasPrefix(f.URL, "https://") && !strings.HasPrefix(f.URL, "http://") {
			continue
		}
		res := webResult{Title: f.Title, URL: f.URL, Publisher: f.Publisher, Snippet: f.Snippet, Engine: f.Scraper, Symbols: f.Symbols}
		if !f.PublishedAt.IsZero() {
			res.PublishedAt = &f.PublishedAt
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q, "kind": kind, "results": results, "engines": engines})
}
