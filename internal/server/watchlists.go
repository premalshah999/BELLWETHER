package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/storage/postgres"
)

func (s *Server) handleListWatchlists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.deps.Store.Watchlists(r.Context())
	if err != nil {
		s.deps.Log.Error("could not list watchlists", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the watchlists.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"watchlists": lists,
		"max":        postgres.MaxWatchlists,
	})
}

func (s *Server) handleCreateWatchlist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body with a name.")
		return
	}
	list, err := s.deps.Store.CreateWatchlist(r.Context(), body.Name)
	if err != nil {
		// The cap and the empty name are both things a person can fix, so the
		// message says what to do rather than reporting a failure.
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRenameWatchlist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "watchlist")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body with a name.")
		return
	}
	if err := s.deps.Store.RenameWatchlist(r.Context(), id, body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteWatchlist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "watchlist")
	if !ok {
		return
	}
	if err := s.deps.Store.DeleteWatchlist(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAddToWatchlist accepts one symbol or many.
//
// Bulk is the normal case, not a special one: an operator moving to this
// system arrives with a list of forty tickers in a spreadsheet, and adding
// them one at a time is the sort of friction that keeps the list empty.
func (s *Server) handleAddToWatchlist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "watchlist")
	if !ok {
		return
	}
	var body struct {
		Symbol  string   `json:"symbol"`
		Symbols []string `json:"symbols"`
		// Text accepts a pasted block — commas, spaces or newlines — so a
		// column copied out of a spreadsheet works without reformatting.
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body.")
		return
	}

	symbols := body.Symbols
	if body.Symbol != "" {
		symbols = append(symbols, body.Symbol)
	}
	if body.Text != "" {
		// Split on line and list separators, never on spaces.
		//
		// Splitting on spaces turned a pasted line of prose into one
		// "instrument" per word, and single words pass ticker validation, so
		// "hello world" became HELLO and WORLD on the watchlist. Keeping each
		// line whole means prose fails validation as the single malformed
		// entry it is.
		symbols = append(symbols, strings.FieldsFunc(body.Text, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ';'
		})...)
	}
	if len(symbols) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Name at least one symbol.")
		return
	}

	added, skipped, rejected, err := s.deps.Store.AddToWatchlist(r.Context(), id, symbols)
	if err != nil {
		s.deps.Log.Error("could not add to watchlist", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not update the watchlist.")
		return
	}
	// Three outcomes, reported separately. "Already on the list" and "not a
	// ticker" are different things, and a caller that cannot tell them apart
	// shows the wrong message for both.
	writeJSON(w, http.StatusOK, map[string]any{
		"added":    added,
		"skipped":  skipped,
		"rejected": rejected,
	})
}

func (s *Server) handleRemoveFromWatchlist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "watchlist")
	if !ok {
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "symbol")))
	if err := s.deps.Store.RemoveFromWatchlist(r.Context(), id, symbol); err != nil {
		s.deps.Log.Error("could not remove from watchlist", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not update the watchlist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCopyWatchlist copies one list's instruments onto another.
func (s *Server) handleCopyWatchlist(w http.ResponseWriter, r *http.Request) {
	from, ok := pathID(w, r, "watchlist")
	if !ok {
		return
	}
	var body struct {
		To int64 `json:"to"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body naming the destination.")
		return
	}
	n, err := s.deps.Store.CopyWatchlist(r.Context(), from, body.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"copied": n})
}

// handleWatchlistItems is one list, priced.
//
// The rail switches between lists without a page change, so this has to be as
// cheap as the legacy single-list call: same enrichment, same concurrency cap.
func (s *Server) handleWatchlistItems(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "watchlist")
	if !ok {
		return
	}
	entries, err := s.deps.Store.WatchlistEntries(r.Context(), id)
	if err != nil {
		s.deps.Log.Error("could not read watchlist items", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read that watchlist.")
		return
	}
	items := []watchlistItem(nil)
	if r.URL.Query().Get("cached") == "1" {
		// The rail must paint immediately. A 25-name list can take several
		// seconds to refresh from the sidecar even with bounded concurrency;
		// returning the persisted snapshot first gives the browser useful rows
		// while it refreshes those prices in a second request.
		items = s.enrichWatchlistCached(r.Context(), entries)
	} else {
		items = s.enrichWatchlist(r.Context(), entries)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
