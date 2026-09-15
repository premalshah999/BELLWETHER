package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tradesys/dashboard/internal/storage"
)

// OutputStore is the deletion surface for generated artefacts.
type OutputStore interface {
	DeleteOutput(ctx context.Context, id int64) error
	DeleteOutputsOfKind(ctx context.Context, kind string) (int, error)
	DeleteOutlook(ctx context.Context, id int64) error
}

// handleDeleteOutput removes one generated artefact.
//
// Generated output is disposable: a morning brief describes a morning, and a
// stale or wrong one is clutter. What it was drawn from — events, prices — is
// untouched, so anything deleted here can be regenerated.
func (s *Server) handleDeleteOutput(w http.ResponseWriter, r *http.Request) {
	store, ok := s.deps.Store.(OutputStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Output storage is unavailable.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Output id must be a number.")
		return
	}
	if err := store.DeleteOutput(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "No such output.")
			return
		}
		s.deps.Log.Error("delete output failed", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not delete it.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleClearOutputs removes every artefact of one kind.
func (s *Server) handleClearOutputs(w http.ResponseWriter, r *http.Request) {
	store, ok := s.deps.Store.(OutputStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Output storage is unavailable.")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		// Refusing a kindless bulk delete is deliberate. "Delete everything
		// the AI has ever produced" should require saying so explicitly, and
		// an empty query parameter is not saying so.
		writeError(w, http.StatusBadRequest, "bad_request",
			"Specify which kind to clear, for example ?kind=morning_brief.")
		return
	}
	n, err := store.DeleteOutputsOfKind(r.Context(), kind)
	if err != nil {
		s.deps.Log.Error("clear outputs failed", "kind", kind, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not clear them.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": kind, "deleted": n})
}

// handleDeleteOutlook removes an unresolved forecast.
//
// A resolved one is refused. The calibration page is only honest if the record
// it averages cannot be pruned of its failures, and being able to delete a
// forecast after seeing how it turned out would make every number on that page
// meaningless.
func (s *Server) handleDeleteOutlook(w http.ResponseWriter, r *http.Request) {
	store, ok := s.deps.Store.(OutputStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Output storage is unavailable.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Outlook id must be a number.")
		return
	}
	if err := store.DeleteOutlook(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "No such outlook.")
			return
		}
		// A scored forecast is refused on principle rather than by accident,
		// so the reason is returned rather than a generic failure.
		writeError(w, http.StatusConflict, "immutable", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
