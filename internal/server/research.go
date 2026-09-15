package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tradesys/dashboard/internal/research"
	"github.com/tradesys/dashboard/internal/storage"
)

// ResearchStore is the persistence the conversation endpoints need.
type ResearchStore interface {
	research.Store
}

func (s *Server) researchStore() (research.Store, bool) {
	st, ok := s.deps.Store.(research.Store)
	return st, ok
}

// askRequest is the body of a research call.
type askRequest struct {
	// ConversationID continues an existing thread. Zero starts a new one.
	ConversationID int64  `json:"conversation_id,omitempty"`
	Question       string `json:"question"`
	// PerProvider bounds how many results each provider contributes.
	PerProvider int `json:"per_provider,omitempty"`
}

// handleAsk answers a question, in a new or existing conversation.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if s.deps.Research == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable",
			"Research is not configured on this deployment.")
		return
	}
	store, ok := s.researchStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Conversation storage is unavailable.")
		return
	}
	if s.deps.AI == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The AI service is not available.")
		return
	}

	var req askRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Could not read the request body.")
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "A question is required.")
		return
	}
	if len(question) > 500 {
		writeError(w, http.StatusBadRequest, "bad_request", "That question is too long.")
		return
	}

	turn, conversationID, err := s.deps.AI.Ask(
		r.Context(), s.deps.Research, store, req.ConversationID, question, req.PerProvider)
	if err != nil {
		s.deps.Log.Error("research ask failed", "question", question, "err", err)
		writeError(w, http.StatusBadGateway, "research", "The research request failed: "+err.Error())
		return
	}
	// 202: the work has been accepted and is running. The client polls the
	// conversation for progress rather than holding a connection open for the
	// minute or two this takes.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"conversation_id": conversationID,
		"turn":            turn,
	})
}

// handleConversations lists research threads.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	store, ok := s.researchStore()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"conversations": []any{}})
		return
	}
	includeArchived := r.URL.Query().Get("archived") == "1"
	list, err := store.ListConversations(r.Context(), atoiDefault(r.URL.Query().Get("limit"), 50), includeArchived)
	if err != nil {
		s.deps.Log.Error("list conversations failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read conversations.")
		return
	}
	if list == nil {
		list = []research.Conversation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

// handleConversation returns one thread with every turn.
func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	store, ok := s.researchStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Conversation storage is unavailable.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Conversation id must be a number.")
		return
	}
	conv, err := store.GetConversation(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "No such conversation.")
		return
	}
	if err != nil {
		s.deps.Log.Error("get conversation failed", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the conversation.")
		return
	}
	writeJSON(w, http.StatusOK, conv)
}

// handleDeleteConversation removes a thread.
func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	store, ok := s.researchStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Conversation storage is unavailable.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Conversation id must be a number.")
		return
	}
	if err := store.DeleteConversation(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "No such conversation.")
			return
		}
		writeError(w, http.StatusInternalServerError, "storage", "Could not delete the conversation.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResearchScrapers reports which scrapers are configured, so the UI can
// say what a search will actually cover before it is run.
func (s *Server) handleResearchScrapers(w http.ResponseWriter, r *http.Request) {
	if s.deps.Research == nil {
		writeJSON(w, http.StatusOK, map[string]any{"scrapers": []string{}, "available": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scrapers":  s.deps.Research.Scrapers(),
		"available": true,
		"checked":   time.Now().UTC(),
	})
}
