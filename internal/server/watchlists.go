package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/storage"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// WatchlistStore is the slice of storage these routes need.
type WatchlistStore interface {
	Watchlists(ctx context.Context) ([]postgres.Watchlist, error)
	WatchlistSymbols(ctx context.Context, id int64) ([]marketdata.Symbol, error)
	WatchlistEntries(ctx context.Context, id int64) ([]storage.WatchlistEntry, error)
	CreateWatchlist(ctx context.Context, name string) (postgres.Watchlist, error)
	RenameWatchlist(ctx context.Context, id int64, name string) error
	DeleteWatchlist(ctx context.Context, id int64) error
	// Returns what was added, what was already present, and what was not a
	// valid ticker.
	AddToWatchlist(ctx context.Context, id int64, symbols []string) (added, skipped, rejected []string, err error)
	RemoveFromWatchlist(ctx context.Context, id int64, symbol string) error
	CopyWatchlist(ctx context.Context, from, to int64) (int, error)
}

func (s *Server) watchlistStore() (WatchlistStore, bool) {
	st, ok := s.deps.Store.(WatchlistStore)
	return st, ok
}

func (s *Server) handleListWatchlists(w http.ResponseWriter, r *http.Request) {
	store, ok := s.watchlistStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Watchlists are not available.")
		return
	}
	lists, err := store.Watchlists(r.Context())
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
	store, ok := s.watchlistStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Watchlists are not available.")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send a JSON body with a name.")
		return
	}
	list, err := store.CreateWatchlist(r.Context(), body.Name)
	if err != nil {
		// The cap and the empty name are both things a person can fix, so the
		// message says what to do rather than reporting a failure.
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRenameWatchlist(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.watchlistTarget(w, r)
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
	if err := store.RenameWatchlist(r.Context(), id, body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteWatchlist(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.watchlistTarget(w, r)
	if !ok {
		return
	}
	if err := store.DeleteWatchlist(r.Context(), id); err != nil {
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
	store, id, ok := s.watchlistTarget(w, r)
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

	added, skipped, rejected, err := store.AddToWatchlist(r.Context(), id, symbols)
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
	store, id, ok := s.watchlistTarget(w, r)
	if !ok {
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "symbol")))
	if err := store.RemoveFromWatchlist(r.Context(), id, symbol); err != nil {
		s.deps.Log.Error("could not remove from watchlist", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not update the watchlist.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCopyWatchlist copies one list's instruments onto another.
func (s *Server) handleCopyWatchlist(w http.ResponseWriter, r *http.Request) {
	store, from, ok := s.watchlistTarget(w, r)
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
	n, err := store.CopyWatchlist(r.Context(), from, body.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"copied": n})
}

// watchlistTarget resolves the store and the list id, answering the request
// itself when either is unusable.
func (s *Server) watchlistTarget(w http.ResponseWriter, r *http.Request) (WatchlistStore, int64, bool) {
	store, ok := s.watchlistStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Watchlists are not available.")
		return nil, 0, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "The watchlist id must be a number.")
		return nil, 0, false
	}
	return store, id, true
}

// handleWatchlistItems is one list, priced.
//
// The rail switches between lists without a page change, so this has to be as
// cheap as the legacy single-list call: same enrichment, same concurrency cap.
func (s *Server) handleWatchlistItems(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.watchlistTarget(w, r)
	if !ok {
		return
	}
	entries, err := store.WatchlistEntries(r.Context(), id)
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
