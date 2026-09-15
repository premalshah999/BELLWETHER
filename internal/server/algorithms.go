package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/alerts"
	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/storage"
)

// maxAlgorithmBytes bounds a submitted definition. The raw JSON editor is a
// text area, and a paste accident should not become a memory problem.
const maxAlgorithmBytes = 64 << 10

// algorithmView is an algorithm plus the derived facts the UI needs but the
// stored document does not carry.
type algorithmView struct {
	*algo.Algorithm
	// RequiredBars is how much history this algorithm needs, shown in the
	// builder so an operator can see why a 200-day rule is quiet on a newly
	// added symbol.
	RequiredBars int `json:"required_bars"`
}

func view(a *algo.Algorithm) algorithmView {
	return algorithmView{Algorithm: a, RequiredBars: a.RequiredBars()}
}

func (s *Server) handleListAlgorithms(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Store.ListAlgorithms(r.Context())
	if err != nil {
		s.deps.Log.Error("could not list algorithms", "err", err)
		writeError(w, http.StatusInternalServerError, "algorithms_unavailable", "Could not read the algorithm list.")
		return
	}
	out := make([]algorithmView, 0, len(list))
	for _, a := range list {
		out = append(out, view(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"algorithms": out})
}

func (s *Server) handleGetAlgorithm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.algorithmID(w, r)
	if !ok {
		return
	}
	a, err := s.deps.Store.GetAlgorithm(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such algorithm.")
		return
	}
	if err != nil {
		s.deps.Log.Error("could not read algorithm", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "algorithms_unavailable", "Could not read that algorithm.")
		return
	}
	writeJSON(w, http.StatusOK, view(a))
}

// readAlgorithmBody decodes and validates a submitted definition, writing the
// error response itself when it fails.
//
// Validation errors are returned field by field so the builder can show each
// message inline next to the input that caused it, rather than as one opaque
// banner.
func (s *Server) readAlgorithmBody(w http.ResponseWriter, r *http.Request) (*algo.Algorithm, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAlgorithmBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That algorithm definition is too large.")
		return nil, false
	}

	a, err := algo.Parse(raw)
	if err != nil {
		writeValidationError(w, err)
		return nil, false
	}
	return a, true
}

// validationResponse carries per-field messages for inline display.
type validationResponse struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  []validationField `json:"fields,omitempty"`
	} `json:"error"`
}

type validationField struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func writeValidationError(w http.ResponseWriter, err error) {
	var resp validationResponse
	resp.Error.Code = "invalid_algorithm"

	var many algo.ValidationErrors
	var one *algo.ValidationError
	switch {
	case errors.As(err, &many):
		resp.Error.Message = "This algorithm has problems that need fixing."
		for _, e := range many {
			resp.Error.Fields = append(resp.Error.Fields, validationField{Field: e.Field, Message: e.Message})
		}
	case errors.As(err, &one):
		resp.Error.Message = one.Message
		resp.Error.Fields = []validationField{{Field: one.Field, Message: one.Message}}
	default:
		resp.Error.Message = err.Error()
	}
	writeJSON(w, http.StatusBadRequest, resp)
}

func (s *Server) handleCreateAlgorithm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.readAlgorithmBody(w, r)
	if !ok {
		return
	}
	if _, err := s.deps.Store.CreateAlgorithm(r.Context(), a); err != nil {
		writeValidationOrServerError(w, s, err, "Could not save that algorithm.")
		return
	}
	s.deps.Log.Info("algorithm created", "id", a.ID, "name", a.Name, "enabled", a.Enabled)
	writeJSON(w, http.StatusCreated, view(a))
}

func (s *Server) handleUpdateAlgorithm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.algorithmID(w, r)
	if !ok {
		return
	}
	a, ok := s.readAlgorithmBody(w, r)
	if !ok {
		return
	}
	// The path is authoritative for identity; a mismatched body id would
	// otherwise let one algorithm overwrite another.
	a.ID = id

	if err := s.deps.Store.UpdateAlgorithm(r.Context(), a); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "No such algorithm.")
			return
		}
		writeValidationOrServerError(w, s, err, "Could not save that algorithm.")
		return
	}
	s.deps.Log.Info("algorithm updated", "id", a.ID, "name", a.Name, "enabled", a.Enabled)
	writeJSON(w, http.StatusOK, view(a))
}

func writeValidationOrServerError(w http.ResponseWriter, s *Server, err error, message string) {
	var many algo.ValidationErrors
	var one *algo.ValidationError
	if errors.As(err, &many) || errors.As(err, &one) {
		writeValidationError(w, err)
		return
	}
	s.deps.Log.Error("algorithm write failed", "err", err)
	writeError(w, http.StatusInternalServerError, "algorithm_write_failed", message)
}

func (s *Server) handleDeleteAlgorithm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.algorithmID(w, r)
	if !ok {
		return
	}
	if err := s.deps.Store.DeleteAlgorithm(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "No such algorithm.")
			return
		}
		s.deps.Log.Error("could not delete algorithm", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "algorithm_write_failed", "Could not delete that algorithm.")
		return
	}
	s.deps.Log.Info("algorithm deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// handleValidateAlgorithm checks a definition without storing it, powering the
// builder's live feedback as the operator types.
func (s *Server) handleValidateAlgorithm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.readAlgorithmBody(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":         true,
		"required_bars": a.RequiredBars(),
		"algorithm":     a,
	})
}

// handlePreviewAlgorithm evaluates a draft against live data without saving it
// or sending anything.
//
// This is how an operator answers "would this have fired?" before turning a
// rule on. It never delivers notifications and never stamps a cooldown.
func (s *Server) handlePreviewAlgorithm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.readAlgorithmBody(w, r)
	if !ok {
		return
	}
	if s.deps.Evaluator == nil {
		writeError(w, http.StatusServiceUnavailable, "evaluator_unavailable", "The evaluator is not running.")
		return
	}

	type symbolResult struct {
		Symbol string       `json:"symbol"`
		Result *algo.Result `json:"result,omitempty"`
		Error  string       `json:"error,omitempty"`
		Source string       `json:"source,omitempty"`
		Stale  bool         `json:"stale"`
	}

	limit := a.RequiredBars() + 20

	// The same union the scheduled engine evaluates: symbols named on the
	// rule plus the members of every attached list. Computing it here as well
	// keeps preview and the real run honest about each other — a preview that
	// silently evaluated fewer instruments than the schedule would is worse
	// than no preview.
	targets := s.algorithmTargets(r.Context(), a)
	out := make([]symbolResult, 0, len(targets))

	for _, raw := range targets {
		sym, err := marketdataParse(raw)
		if err != nil {
			out = append(out, symbolResult{Symbol: raw, Error: err.Error()})
			continue
		}
		interval, err := marketdataInterval(a.Interval)
		if err != nil {
			out = append(out, symbolResult{Symbol: raw, Error: err.Error()})
			continue
		}
		series, err := s.deps.Router.Candles(r.Context(), sym, interval, limit)
		if err != nil {
			// One unavailable symbol must not fail the whole preview.
			out = append(out, symbolResult{Symbol: sym.String(), Error: "No price data available."})
			continue
		}
		res := s.deps.Evaluator.Evaluate(a, sym, series.Candles)
		out = append(out, symbolResult{
			Symbol: sym.String(), Result: &res, Source: series.Source, Stale: series.Stale,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"required_bars": a.RequiredBars(),
		"results":       out,
		"evaluated_at":  s.deps.Now(),
	})
}

// handleRunAlgorithm evaluates a stored algorithm immediately, delivering any
// alerts it produces. Unlike preview, this is the real pipeline.
func (s *Server) handleRunAlgorithm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.algorithmID(w, r)
	if !ok {
		return
	}
	if s.deps.Engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine_unavailable", "The alert engine is not running.")
		return
	}

	a, err := s.deps.Store.GetAlgorithm(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such algorithm.")
		return
	}
	if err != nil {
		s.deps.Log.Error("could not read algorithm", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "algorithms_unavailable", "Could not read that algorithm.")
		return
	}

	summary := s.deps.Engine.RunAlgorithm(r.Context(), a)
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleAlgorithmEvaluations(w http.ResponseWriter, r *http.Request) {
	id, ok := s.algorithmID(w, r)
	if !ok {
		return
	}
	records, err := s.deps.Store.LastEvaluations(r.Context(), id)
	if err != nil {
		s.deps.Log.Error("could not read evaluations", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "evaluations_unavailable", "Could not read evaluation history.")
		return
	}

	type evalView struct {
		Symbol  string    `json:"symbol"`
		At      time.Time `json:"at"`
		Status  string    `json:"status"`
		Reason  string    `json:"reason,omitempty"`
		Summary string    `json:"summary,omitempty"`
	}
	out := make([]evalView, 0, len(records))
	for _, rec := range records {
		out = append(out, evalView{
			Symbol: rec.Symbol, At: rec.At, Status: string(rec.Status),
			Reason: rec.Reason, Summary: rec.Summary,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"evaluations": out})
}

// handleAlgorithmVocabulary tells the builder what the backend can actually
// evaluate, so the dropdowns cannot drift out of sync with the engine.
func (s *Server) handleAlgorithmVocabulary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"indicators": algo.Vocabulary(),
		"operators":  algo.Operators(),
		"intervals":  []string{"1m", "5m", "15m", "1h", "1d", "1wk"},
	})
}

func (s *Server) handleAlgorithmTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"templates": algo.Templates()})
}

func (s *Server) algorithmID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id", "That is not a valid algorithm id.")
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// Alerts
// ---------------------------------------------------------------------------

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	f := alerts.Filter{
		Symbol:     r.URL.Query().Get("symbol"),
		UnreadOnly: r.URL.Query().Get("unread") == "true",
		Limit:      clampInt(intParam(r, "limit", 50), 1, 500),
	}
	if raw := r.URL.Query().Get("algorithm_id"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			f.AlgorithmID = id
		}
	}
	if raw := r.URL.Query().Get("before"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.Before = t
		}
	}

	list, err := s.deps.Store.ListAlerts(r.Context(), f)
	if err != nil {
		s.deps.Log.Error("could not list alerts", "err", err)
		writeError(w, http.StatusInternalServerError, "alerts_unavailable", "Could not read the alert feed.")
		return
	}
	unread, err := s.deps.Store.UnreadAlertCount(r.Context())
	if err != nil {
		s.deps.Log.Warn("could not count unread alerts", "err", err)
	}
	if list == nil {
		list = []alerts.Alert{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": list, "unread": unread})
}

func (s *Server) handleMarkAlertRead(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id", "That is not a valid alert id.")
		return
	}
	if err := s.deps.Store.MarkAlertRead(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "No such alert.")
			return
		}
		s.deps.Log.Error("could not mark alert read", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "alert_write_failed", "Could not update that alert.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMarkAllAlertsRead(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.MarkAllAlertsRead(r.Context()); err != nil {
		s.deps.Log.Error("could not mark all alerts read", "err", err)
		writeError(w, http.StatusInternalServerError, "alert_write_failed", "Could not update the alert feed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON is a small helper for endpoints with simple bodies.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
}

// algorithmTargets is the set of instruments an algorithm evaluates.
//
// Symbols named directly, plus every member of every attached watchlist,
// deduplicated. A list that cannot be read is skipped rather than failing the
// request: a rule that also names symbols should still report on those.
func (s *Server) algorithmTargets(ctx context.Context, a *algo.Algorithm) []string {
	seen := make(map[string]bool, len(a.Symbols))
	out := make([]string, 0, len(a.Symbols))
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range a.Symbols {
		add(v)
	}

	store, ok := s.watchlistStore()
	if !ok {
		return out
	}
	for _, id := range a.WatchlistIDs {
		syms, err := store.WatchlistSymbols(ctx, id)
		if err != nil {
			s.deps.Log.Warn("could not read a watchlist for an algorithm",
				"algorithm", a.Name, "watchlist", id, "err", err)
			continue
		}
		for _, sym := range syms {
			add(sym.String())
		}
	}
	return out
}
