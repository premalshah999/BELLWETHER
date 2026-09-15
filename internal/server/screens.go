package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tradesys/dashboard/internal/screens"
	"github.com/tradesys/dashboard/internal/storage"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// ScreenStore is the slice of storage these routes need.
type ScreenStore interface {
	RunScreen(ctx context.Context, def screens.Definition) (postgres.ScreenResult, error)
	Screens(ctx context.Context) ([]postgres.Screen, error)
	Screen(ctx context.Context, id int64) (postgres.Screen, error)
	CreateScreen(ctx context.Context, name, description string, def screens.Definition) (postgres.Screen, error)
	UpdateScreen(ctx context.Context, id int64, name, description string, def screens.Definition) (postgres.Screen, error)
	DeleteScreen(ctx context.Context, id int64) error
	TouchScreen(ctx context.Context, id int64) error
}

func (s *Server) screenStore() (ScreenStore, bool) {
	st, ok := s.deps.Store.(ScreenStore)
	return st, ok
}

// handleScreenFields is the catalogue a screen can be built from.
//
// Served rather than duplicated in the interface, so a measurement added to
// the scanner shows up in the builder without a matching frontend change --
// and, more to the point, so the builder cannot offer a field the query
// rejects.
func (s *Server) handleScreenFields(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"fields": screens.Fields(),
		"ops": []map[string]string{
			{"op": ">", "label": "is above"},
			{"op": ">=", "label": "is at least"},
			{"op": "<", "label": "is below"},
			{"op": "<=", "label": "is at most"},
		},
		"max_conditions": screens.MaxConditions,
		"max_limit":      screens.MaxLimit,
	})
}

// handleRunScreen evaluates a definition without saving it.
//
// The builder runs on every edit, so this is the hot path and deliberately has
// no side effects: an operator dragging a threshold around should not be
// writing rows.
func (s *Server) handleRunScreen(w http.ResponseWriter, r *http.Request) {
	store, ok := s.screenStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Screens are not available.")
		return
	}
	var def screens.Definition
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&def); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "That screen could not be read.")
		return
	}
	if errs := def.Validate(); len(errs) > 0 {
		writeFieldErrors(w, errs)
		return
	}
	res, err := store.RunScreen(r.Context(), def)
	if err != nil {
		s.deps.Log.Error("could not run screen", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not run that screen.")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListScreens(w http.ResponseWriter, r *http.Request) {
	store, ok := s.screenStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Screens are not available.")
		return
	}
	list, err := store.Screens(r.Context())
	if err != nil {
		s.deps.Log.Error("could not list screens", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the screens.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"screens": list})
}

// screenBody is the wire shape for creating and updating.
type screenBody struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Definition  screens.Definition `json:"definition"`
}

// read validates the whole body and reports every problem at once.
func (b *screenBody) read(w http.ResponseWriter, r *http.Request) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(b); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "That screen could not be read.")
		return false
	}
	b.Name = strings.TrimSpace(b.Name)
	b.Description = strings.TrimSpace(b.Description)

	errs := b.Definition.Validate()
	switch {
	case b.Name == "":
		errs = append(errs, screens.FieldError{Field: "name", Message: "give the screen a name"})
	case len(b.Name) > 80:
		errs = append(errs, screens.FieldError{Field: "name", Message: "keep the name under 80 characters"})
	}
	if len(errs) > 0 {
		writeFieldErrors(w, errs)
		return false
	}
	return true
}

func (s *Server) handleCreateScreen(w http.ResponseWriter, r *http.Request) {
	store, ok := s.screenStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Screens are not available.")
		return
	}
	var body screenBody
	if !body.read(w, r) {
		return
	}
	sc, err := store.CreateScreen(r.Context(), body.Name, body.Description, body.Definition)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "duplicate_name", "A screen with that name already exists.")
			return
		}
		s.deps.Log.Error("could not create screen", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not save that screen.")
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) handleUpdateScreen(w http.ResponseWriter, r *http.Request) {
	store, ok := s.screenStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Screens are not available.")
		return
	}
	id, ok := screenID(w, r)
	if !ok {
		return
	}
	var body screenBody
	if !body.read(w, r) {
		return
	}
	sc, err := store.UpdateScreen(r.Context(), id, body.Name, body.Description, body.Definition)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such screen.")
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "duplicate_name", "A screen with that name already exists.")
			return
		}
		s.deps.Log.Error("could not update screen", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not save that screen.")
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleDeleteScreen(w http.ResponseWriter, r *http.Request) {
	store, ok := s.screenStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Screens are not available.")
		return
	}
	id, ok := screenID(w, r)
	if !ok {
		return
	}
	err := store.DeleteScreen(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such screen.")
		return
	}
	if err != nil {
		s.deps.Log.Error("could not delete screen", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not delete that screen.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunSavedScreen runs a stored screen by id.
//
// Separate from the ad-hoc run because this one is a deliberate act rather
// than a keystroke, so it is worth recording when it last happened.
func (s *Server) handleRunSavedScreen(w http.ResponseWriter, r *http.Request) {
	store, ok := s.screenStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Screens are not available.")
		return
	}
	id, ok := screenID(w, r)
	if !ok {
		return
	}
	sc, err := store.Screen(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such screen.")
		return
	}
	if err != nil {
		s.deps.Log.Error("could not read screen", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read that screen.")
		return
	}
	res, err := store.RunScreen(r.Context(), sc.Definition)
	if err != nil {
		s.deps.Log.Error("could not run screen", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not run that screen.")
		return
	}
	// Best effort: the run succeeded, and failing the request because a
	// bookkeeping column did not update would throw away the answer.
	if err := store.TouchScreen(r.Context(), id); err != nil {
		s.deps.Log.Warn("could not record screen run", "id", id, "err", err)
	}
	writeJSON(w, http.StatusOK, res)
}

// isUniqueViolation reports whether an error is Postgres rejecting a
// duplicate.
//
// A screen whose name is already taken is the operator's mistake to fix, not a
// server fault, so it has to be told apart from a genuine storage failure and
// answered with a 409 rather than a 500.
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func screenID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "That is not a screen id.")
		return 0, false
	}
	return id, true
}

// writeFieldErrors reports a rejected definition.
//
// Every problem in one response, keyed by field, so the builder can mark the
// offending rows rather than showing one message at the top.
func writeFieldErrors(w http.ResponseWriter, errs []screens.FieldError) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error":   "invalid_screen",
		"message": "That screen is not valid.",
		"fields":  errs,
	})
}
