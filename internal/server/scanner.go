package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// ScanReader is the slice of storage the scan routes need. Declared here as
// an interface for the same reason as every other reader in this package: the
// handler should not be able to reach the whole store.
type ScanReader interface {
	LatestScan(ctx context.Context, limit int) ([]postgres.StoredFinding, error)
	SymbolScanHistory(ctx context.Context, symbol string, limit int) ([]postgres.StoredFinding, error)
}

func (s *Server) scanStore() (ScanReader, bool) {
	r, ok := s.deps.Store.(ScanReader)
	return r, ok
}

// handleLatestScan returns the most recent scan's findings.
func (s *Server) handleLatestScan(w http.ResponseWriter, r *http.Request) {
	db, ok := s.scanStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "scanner_unavailable", "the market scanner is not configured")
		return
	}
	limit := clampInt(intParam(r, "limit", 40), 1, 200)
	findings, err := db.LatestScan(r.Context(), limit)
	if err != nil {
		s.deps.Log.Error("could not read the latest scan", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read the latest scan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"findings": findings,
		"count":    len(findings),
	})
}

// handleSymbolScanHistory returns what the scanner has said about one
// instrument over time.
//
// This is the query that makes the archive worth keeping: it shows whether
// the volume arrived before the announcement did.
func (s *Server) handleSymbolScanHistory(w http.ResponseWriter, r *http.Request) {
	db, ok := s.scanStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "scanner_unavailable", "the market scanner is not configured")
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "symbol")))
	if symbol == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a symbol is required")
		return
	}
	limit := clampInt(intParam(r, "limit", 30), 1, 200)
	findings, err := db.SymbolScanHistory(r.Context(), symbol, limit)
	if err != nil {
		s.deps.Log.Error("could not read scan history", "symbol", symbol, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read scan history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"symbol":   symbol,
		"findings": findings,
		"count":    len(findings),
	})
}

// handleRunScan triggers a scan by hand.
//
// Rate limited alongside the AI routes despite spending no tokens, because a
// scan is a two-minute fan-out across seven hundred instruments against a
// third party's service. Letting a refresh button queue those without limit
// would be discourteous to that service and useless to the operator, since a
// second scan of the same session returns the same numbers.
func (s *Server) handleRunScan(w http.ResponseWriter, r *http.Request) {
	if s.deps.Scanner == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner_unavailable", "the market scanner is not configured")
		return
	}
	res, err := s.deps.Scanner.Run(r.Context(), nil)
	if err != nil {
		s.deps.Log.Warn("manual scan failed", "err", err)
		writeError(w, http.StatusBadGateway, "scan_failed", "the scan could not be completed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
