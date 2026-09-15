package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// apiError is the single error shape the frontend has to understand.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The dashboard polls; nothing here is cacheable by an intermediary.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already written, so this can only be logged.
		slog.Error("could not encode response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var e apiError
	e.Error.Code = code
	e.Error.Message = message
	writeJSON(w, status, e)
}
