package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	if moves, _, err := s.deps.Store.FundMovesWhere(ctx, postgres.MoveFilter{MinValue: 5_000_000}); err == nil {
		for _, m := range moves {
			if m.Kind == "new" || m.Kind == "added" {
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

	// Some managers hold thousands of positions, so the list is searched,
	// filtered, sorted and paged here rather than shipped whole.
	q := r.URL.Query()
	needle := strings.ToUpper(strings.TrimSpace(q.Get("q")))
	kind := q.Get("kind")
	counts := map[string]int{}
	sectors := map[string]float64{}
	var total float64
	shown := moves[:0]
	for _, m := range moves {
		counts[m.Kind]++
		if m.Kind != "exited" {
			total += m.Value
			sector := "Other"
			if s.deps.Companies != nil && m.Symbol != "" {
				if sec, ok := s.deps.Companies.Sector(m.Symbol); ok && sec != "" {
					sector = sec
				}
			}
			sectors[sector] += m.Value
		}
		if kind != "" && kind != "all" && m.Kind != kind {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToUpper(m.Issuer), needle) && !strings.Contains(m.Symbol, needle) && m.CUSIP != needle {
			continue
		}
		shown = append(shown, m)
	}
	desc := q.Get("dir") != "asc"
	key := func(m smartmoney.Move) float64 {
		switch q.Get("sort") {
		case "change":
			return m.Value - m.PrevValue
		case "change_pct":
			return m.ChangePct
		case "shares":
			return m.Shares
		default:
			return m.Value
		}
	}
	sort.SliceStable(shown, func(i, j int) bool {
		if desc {
			return key(shown[i]) > key(shown[j])
		}
		return key(shown[i]) < key(shown[j])
	})
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset = max(0, min(offset, len(shown)))
	page := shown[offset:min(offset+limit, len(shown))]

	type share struct {
		Sector string  `json:"sector"`
		Value  float64 `json:"value"`
		Pct    float64 `json:"pct"`
	}
	mix := make([]share, 0, len(sectors))
	for name, v := range sectors {
		mix = append(mix, share{name, v, v / max(total, 1) * 100})
	}
	sort.Slice(mix, func(i, j int) bool { return mix[i].Value > mix[j].Value })
	writeJSON(w, http.StatusOK, map[string]any{
		"filing": filing, "moves": page, "matched": len(shown), "offset": offset, "limit": limit,
		"counts": counts, "sectors": mix,
	})
}

// handleFunds is the directory of followed managers.
func (s *Server) handleFunds(w http.ResponseWriter, r *http.Request) {
	funds, err := s.deps.Store.FundSummaries(r.Context())
	if err != nil {
		s.deps.Log.Error("fund summaries failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not read the fund directory.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"funds": funds})
}

// handleSearchFunds finds any SEC filer by name, so a manager not on the
// shipped list can be followed.
func (s *Server) handleSearchFunds(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}
	if s.deps.SmartMoney == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Set SEC_USER_AGENT to search SEC filers.")
		return
	}
	found, err := s.deps.SmartMoney.SearchFilers(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", "SEC search is not answering right now.")
		return
	}
	followed := map[string]bool{}
	if fs, err := s.deps.Store.FollowedFunds(r.Context()); err == nil {
		for _, f := range fs {
			followed[f.CIK] = true
		}
	}
	type result struct {
		smartmoney.Filer
		Followed bool `json:"followed"`
	}
	out := make([]result, len(found))
	for i, f := range found {
		out[i] = result{f, followed[f.CIK]}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// handleFollowFund adds a 13F filer to the followed list and reads its two
// latest filings in the background.
func (s *Server) handleFollowFund(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CIK     string `json:"cik"`
		Manager string `json:"manager"`
		Style   string `json:"style"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.")
		return
	}
	digits := strings.TrimLeft(strings.TrimSpace(body.CIK), "0")
	if digits == "" || len(digits) > 10 || strings.Trim(digits, "0123456789") != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "cik must be an SEC CIK number.")
		return
	}
	if s.deps.SmartMoney == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Set SEC_USER_AGENT to follow SEC filers.")
		return
	}
	cik := fmt.Sprintf("%010s", digits)
	ok, name, err := s.deps.SmartMoney.Has13F(r.Context(), cik)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", "SEC is not answering right now.")
		return
	}
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "no_13f", name+" has never filed a 13F holdings report, so there is no portfolio to follow.")
		return
	}
	f := smartmoney.Fund{CIK: cik, Name: name, Manager: strings.TrimSpace(body.Manager), Style: strings.TrimSpace(body.Style)}
	if f.Manager == "" {
		f.Manager = name
	}
	if err := s.deps.Store.FollowFund(r.Context(), f); err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not follow this fund.")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if _, err := s.deps.SmartMoney.SyncFund(ctx, f); err != nil {
			s.deps.Log.Warn("13f: first sync of a followed fund failed", "fund", f.Name, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, f)
}

// handleUnfollowFund stops following a manager.
func (s *Server) handleUnfollowFund(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.UnfollowFund(r.Context(), chi.URLParam(r, "cik")); errors.Is(err, postgres.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "That fund is not in the directory.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "storage", "Could not unfollow this fund.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
