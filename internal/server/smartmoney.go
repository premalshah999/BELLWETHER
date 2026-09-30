package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/smartmoney"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

type tradeEnvelope struct {
	smartmoney.Trade
	Role string `json:"role"`
}

func wrapTrades(ts []smartmoney.Trade) []tradeEnvelope {
	out := make([]tradeEnvelope, len(ts))
	for i, t := range ts {
		out[i] = tradeEnvelope{Trade: t, Role: t.Role()}
	}
	return out
}

func daysParam(r *http.Request, def, max int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && n > 0 {
		if n > max {
			return max
		}
		return n
	}
	return def
}

// handleSmartMoneyOverview answers "who with inside knowledge or a famous
// track record is buying what", in one response.
func (s *Server) handleSmartMoneyOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := daysParam(r, 90, 365)
	since := s.deps.Now().AddDate(0, 0, -days)

	leaders, err := s.deps.Store.InsiderLeaders(ctx, since, 400)
	if err != nil {
		s.deps.Log.Error("insider leaders failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read insider trades.")
		return
	}
	var buys, sells, clusters []smartmoney.Leader
	for _, l := range leaders {
		if l.BuyValue > 0 {
			buys = append(buys, l)
		}
		if l.SellValue > 0 {
			sells = append(sells, l)
		}
		// Several insiders buying on the open market in the same window is
		// the strongest insider signal there is: one can be personal, three
		// is a view.
		if l.Buyers >= 2 {
			clusters = append(clusters, l)
		}
	}
	sort.Slice(buys, func(i, j int) bool { return buys[i].BuyValue > buys[j].BuyValue })
	sort.Slice(sells, func(i, j int) bool { return sells[i].SellValue > sells[j].SellValue })
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Buyers > clusters[j].Buyers })

	big, err := s.deps.Store.ListInsiderTrades(ctx, postgres.InsiderFilter{Since: since, Market: true, Side: "buy", Limit: 60})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not read insider trades.")
		return
	}
	funds, err := s.deps.Store.FundSummaries(ctx)
	if err != nil {
		s.deps.Log.Error("fund summaries failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read fund holdings.")
		return
	}

	// Across every followed fund, the positions opened or grown last quarter.
	var fundBuys []smartmoney.Move
	for _, f := range funds {
		moves, _, err := s.deps.Store.FundMoves(ctx, f.CIK)
		if err != nil {
			continue
		}
		for _, m := range moves {
			if (m.Kind == "new" || m.Kind == "added") && m.Value >= 5_000_000 {
				fundBuys = append(fundBuys, m)
			}
		}
	}
	sort.Slice(fundBuys, func(i, j int) bool {
		return fundBuys[i].Value-fundBuys[i].PrevValue > fundBuys[j].Value-fundBuys[j].PrevValue
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"days":          days,
		"top_buys":      head(buys, 20),
		"top_sells":     head(sells, 20),
		"cluster_buys":  head(clusters, 20),
		"largest_buys":  wrapTrades(head(big, 40)),
		"funds":         funds,
		"fund_buys":     head(fundBuys, 40),
		"insider_count": len(leaders),
	})
}

func head[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	if s == nil {
		return []T{}
	}
	return s
}

func (s *Server) handleInsiderTrades(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := postgres.InsiderFilter{
		Symbol: strings.ToUpper(strings.TrimSpace(q.Get("symbol"))),
		Since:  s.deps.Now().AddDate(0, 0, -daysParam(r, 90, 730)),
		Market: q.Get("all") != "1",
		Side:   q.Get("side"),
		Limit:  500,
	}
	trades, err := s.deps.Store.ListInsiderTrades(r.Context(), f)
	if err != nil {
		s.deps.Log.Error("insider trades failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read insider trades.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trades": wrapTrades(trades)})
}

func (s *Server) handleFund(w http.ResponseWriter, r *http.Request) {
	cik := chi.URLParam(r, "cik")
	moves, filing, err := s.deps.Store.FundMoves(r.Context(), cik)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not read this fund's holdings.")
		return
	}
	if filing == nil {
		writeError(w, http.StatusNotFound, "not_found", "No 13F filing is stored for this fund yet.")
		return
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].Value > moves[j].Value })
	writeJSON(w, http.StatusOK, map[string]any{"filing": filing, "moves": moves})
}

// handleSymbolSmartMoney is everything known about who trades one stock.
func (s *Server) handleSymbolSmartMoney(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(chi.URLParam(r, "symbol"))
	ctx := r.Context()
	trades, err := s.deps.Store.ListInsiderTrades(ctx, postgres.InsiderFilter{
		Symbol: symbol, Since: s.deps.Now().AddDate(-1, 0, 0), Market: true, Limit: 300,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not read insider trades.")
		return
	}
	moves, err := s.deps.Store.SymbolFundMoves(ctx, symbol)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not read fund holdings.")
		return
	}
	var congressFilings any = []any{}
	if list, err := s.deps.Store.ListCongressFilings(ctx, postgres.CongressFilingFilter{Symbol: symbol, Limit: 50}); err == nil {
		congressFilings = list
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"symbol":   symbol,
		"trades":   wrapTrades(trades),
		"funds":    moves,
		"congress": congressFilings,
	})
}
