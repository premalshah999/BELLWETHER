package server

import "net/http"

// What the app says about itself: its configuration and the state of every
// external dependency it relies on.

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	c := s.deps.Config
	writeJSON(w, http.StatusOK, metaResponse{
		App:        "TradeSys",
		Version:    s.deps.Version,
		Disclaimer: Disclaimer,
		ServerTime: s.deps.Now(),
		Features: metaFeatures{
			AI:           c.LLMConfigured(),
			Telegram:     c.TelegramConfigured(),
			Search:       s.deps.Research != nil,
			AlphaVantage: c.AlphaVantageConfigured(),
		},
		Providers: s.deps.Router.ProviderNames(),
		Intervals: []string{"1m", "5m", "15m", "1h", "1d", "1wk"},
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	degraded := s.deps.Health.DegradedProviders()
	// Anonymous callers (uptime checks) learn only whether the app is up; the
	// provider inventory and budgets are for signed-in users.
	if _, signedIn := s.authenticate(r); !signedIn && !s.openAccess(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "degraded": len(degraded) > 0})
		return
	}
	if degraded == nil {
		degraded = []string{}
	}
	resp := healthResponse{
		Providers:          s.deps.Health.Snapshot(),
		MarketDataDegraded: s.deps.Health.MarketDataDegraded(),
		DegradedProviders:  degraded,
		Budgets:            map[string]budgetView{},
		CheckedAt:          s.deps.Now(),
	}
	for name, b := range s.deps.Budgets {
		used, limit, err := b.Usage(r.Context())
		if err != nil {
			// A budget we cannot read is not worth failing the health check
			// over — the health check is what the operator looks at when
			// things are already going wrong.
			s.deps.Log.Warn("could not read provider budget", "provider", name, "err", err)
			continue
		}
		resp.Budgets[name] = budgetView{Used: used, Limit: limit, Exhausted: limit > 0 && used >= limit}
	}
	writeJSON(w, http.StatusOK, resp)
}
