package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// briefMaxAge is how long a stored morning brief is served before the UI
// offers to regenerate it. Briefs are expensive and dated by nature; showing
// yesterday's without saying so would be the worst option.
const briefMaxAge = 20 * time.Hour

// aiStatusResponse tells the UI what the AI layer can currently do.
type aiStatusResponse struct {
	Status Status         `json:"status"`
	Budget ai.BudgetState `json:"budget"`
	// Model names are shown on the settings page so an operator can confirm
	// which endpoint they are actually pointed at.
	Model      string `json:"model,omitempty"`
	CheapModel string `json:"cheap_model,omitempty"`
	// Search reports whether sourced explanations are possible.
	Search bool `json:"search"`
}

// Status mirrors ai.Status for the API surface.
type Status = ai.Status

func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	resp := aiStatusResponse{Status: ai.StatusUnconfigured}
	if s.deps.AI == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	client := s.deps.AI.Client()
	resp.Status = s.deps.AI.Available(r.Context())
	resp.Model = client.Model()
	resp.CheapModel = client.CheapModel()
	resp.Search = s.deps.Research != nil

	budget, err := client.Budget(r.Context())
	if err != nil {
		s.deps.Log.Warn("could not read the AI budget", "err", err)
	} else {
		resp.Budget = budget
	}
	writeJSON(w, http.StatusOK, resp)
}

// aiUnavailable writes the standard response for a feature that cannot run,
// distinguishing "not set up" and "budget spent" from a genuine fault so the
// UI can say something useful instead of "error".
func (s *Server) aiUnavailable(w http.ResponseWriter, status ai.Status) bool {
	switch status {
	case ai.StatusUnconfigured:
		writeError(w, http.StatusServiceUnavailable, "ai_unconfigured",
			"The AI layer is not configured. Set LLM_BASE_URL, LLM_MODEL, and LLM_API_KEY.")
		return true
	case ai.StatusBudgetReached:
		writeError(w, http.StatusServiceUnavailable, "ai_budget_reached",
			"The monthly AI token budget cannot cover this request. Alerts, prices, and algorithms continue unaffected.")
		return true
	}
	return false
}

func (s *Server) requireAI(w http.ResponseWriter, r *http.Request) bool {
	if s.deps.AI == nil {
		s.aiUnavailable(w, ai.StatusUnconfigured)
		return false
	}
	return !s.aiUnavailable(w, s.deps.AI.Available(r.Context()))
}

// handleMorningBrief serves the stored brief, generating one only when asked.
func (s *Server) handleMorningBrief(w http.ResponseWriter, r *http.Request) {
	if s.deps.AI == nil {
		s.aiUnavailable(w, ai.StatusUnconfigured)
		return
	}

	stored, ok, err := s.deps.Store.LatestOutput(r.Context(), "morning_brief", "")
	if err != nil {
		s.deps.Log.Error("could not read the brief archive", "err", err)
	}
	if ok {
		var brief ai.MorningBrief
		if err := json.Unmarshal(stored.Content, &brief); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"brief": brief,
				"stale": s.deps.Now().Sub(stored.CreatedAt) > briefMaxAge,
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"brief": nil,
		"stale": false,
		"note":  "No brief has been generated yet. One is produced each morning, or you can generate one now.",
	})
}

func (s *Server) handleGenerateBrief(w http.ResponseWriter, r *http.Request) {
	if !s.requireAI(w, r) {
		return
	}
	brief, err := s.deps.AI.GenerateMorningBrief(r.Context())
	if err != nil {
		if s.aiUnavailable(w, brief.Status) {
			return
		}
		s.deps.Log.Warn("could not generate the morning brief", "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable", "The brief could not be generated: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"brief": brief, "stale": false})
}

func (s *Server) handleExplainMove(w http.ResponseWriter, r *http.Request) {
	sym, ok := s.symbolParam(w, r)
	if !ok {
		return
	}
	if !s.requireAI(w, r) {
		return
	}

	explanation, err := s.deps.AI.ExplainMove(r.Context(), sym)
	if err != nil {
		if s.aiUnavailable(w, explanation.Status) {
			return
		}
		s.deps.Log.Warn("could not explain a move", "symbol", sym, "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable",
			"An explanation could not be produced right now.")
		return
	}
	writeJSON(w, http.StatusOK, explanation)
}

func (s *Server) handleGenerateOutlook(w http.ResponseWriter, r *http.Request) {
	sym, ok := s.symbolParam(w, r)
	if !ok {
		return
	}
	if !s.requireAI(w, r) {
		return
	}

	outlook, status, err := s.deps.AI.GenerateOutlook(r.Context(), sym)
	if err != nil {
		if s.aiUnavailable(w, status) {
			return
		}
		s.deps.Log.Warn("could not generate an outlook", "symbol", sym, "err", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable",
			"An outlook could not be produced right now.")
		return
	}
	writeJSON(w, http.StatusOK, outlook)
}

func (s *Server) handleListOutlooks(w http.ResponseWriter, r *http.Request) {
	symbol := ""
	if raw := chi.URLParam(r, "symbol"); raw != "" {
		sym, err := marketdata.ParseSymbol(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_symbol", err.Error())
			return
		}
		symbol = sym.String()
	}

	outlooks, err := s.deps.Store.ListOutlooks(r.Context(), symbol, clampInt(intParam(r, "limit", 50), 1, 500))
	if err != nil {
		s.deps.Log.Error("could not list outlooks", "err", err)
		writeError(w, http.StatusInternalServerError, "ai_archive_unavailable", "Could not read outlook history.")
		return
	}
	if outlooks == nil {
		outlooks = []ai.Outlook{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"outlooks": outlooks})
}

// handleCalibration serves the model's measured track record.
//
// This endpoint needs no LLM call, so it keeps working when the budget is
// spent — which matters, because the moment an operator most wants to know how
// much to trust the model is when it has stopped answering.
func (s *Server) handleCalibration(w http.ResponseWriter, r *http.Request) {
	if s.deps.AI == nil {
		writeJSON(w, http.StatusOK, ai.Calibration{GeneratedAt: s.deps.Now()})
		return
	}
	cal, err := s.deps.AI.Calibrate(r.Context())
	if err != nil {
		s.deps.Log.Error("could not compute calibration", "err", err)
		writeError(w, http.StatusInternalServerError, "calibration_unavailable",
			"Could not compute the calibration record.")
		return
	}
	writeJSON(w, http.StatusOK, cal)
}

func (s *Server) symbolParam(w http.ResponseWriter, r *http.Request) (marketdata.Symbol, bool) {
	sym, err := marketdata.ParseSymbol(chi.URLParam(r, "symbol"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_symbol", err.Error())
		return marketdata.Symbol{}, false
	}
	return sym, true
}
