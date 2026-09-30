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
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/paper"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// Paper trading: wallets of simulated money traded at real prices, by hand
// or by agents. See internal/paper for the rules it follows.

func (s *Server) paperReady(w http.ResponseWriter) bool {
	if s.deps.Paper == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Paper trading needs market data, which is not configured.")
		return false
	}
	return true
}

// paperError maps store and engine refusals to what a person can act on.
func (s *Server) paperError(w http.ResponseWriter, err error, what string) {
	var refused postgres.ErrPaper
	switch {
	case errors.As(err, &refused):
		writeError(w, http.StatusUnprocessableEntity, "refused", refused.Msg)
	case errors.Is(err, paper.ErrRefused):
		writeError(w, http.StatusUnprocessableEntity, "refused", strings.TrimPrefix(err.Error(), paper.ErrRefused.Error()+": "))
	case errors.Is(err, postgres.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "No such "+what+".")
	default:
		s.deps.Log.Error("paper: "+what+" failed", "err", err)
		writeError(w, http.StatusInternalServerError, "storage", "Could not complete that "+what+" request.")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.")
		return false
	}
	return true
}

type holdingView struct {
	Symbol          string      `json:"symbol"`
	Qty             int64       `json:"qty"`
	AvgCostCents    paper.Cents `json:"avg_cost_cents"`
	PriceCents      paper.Cents `json:"price_cents"`
	ValueCents      paper.Cents `json:"value_cents"`
	UnrealizedCents paper.Cents `json:"unrealized_cents"`
	UnrealizedPct   float64     `json:"unrealized_pct"`
	WeightPct       float64     `json:"weight_pct"`
	OpenedAt        time.Time   `json:"opened_at"`
	ReservedQty     int64       `json:"reserved_qty"`
}

type walletView struct {
	paper.Wallet
	CashCents        paper.Cents   `json:"cash_cents"`
	ReservedCents    paper.Cents   `json:"reserved_cents"`
	BuyingPowerCents paper.Cents   `json:"buying_power_cents"`
	MarketValueCents paper.Cents   `json:"market_value_cents"`
	EquityCents      paper.Cents   `json:"equity_cents"`
	NetDepositsCents paper.Cents   `json:"net_deposits_cents"`
	FeesCents        paper.Cents   `json:"fees_cents"`
	RealizedCents    paper.Cents   `json:"realized_cents"`
	UnrealizedCents  paper.Cents   `json:"unrealized_cents"`
	TotalReturnPct   float64       `json:"total_return_pct"`
	DayChangePct     *float64      `json:"day_change_pct,omitempty"`
	DrawdownPct      float64       `json:"drawdown_pct"`
	DayTrades        int           `json:"day_trades"`
	TradesToday      int           `json:"trades_today"`
	SessionOpen      bool          `json:"session_open"`
	MinutesToClose   int           `json:"minutes_to_close"`
	Holdings         []holdingView `json:"holdings"`
}

func (s *Server) walletView(ctx context.Context, id int64) (walletView, error) {
	wl, a, err := s.deps.Paper.Snapshot(ctx, id)
	if err != nil {
		return walletView{}, err
	}
	bal, err := s.deps.Store.Balances(ctx, id)
	if err != nil {
		return walletView{}, err
	}
	now := s.deps.Now()
	v := walletView{Wallet: wl, CashCents: a.Cash, ReservedCents: a.Reserved, BuyingPowerCents: a.BuyingPower(),
		EquityCents: a.Equity(), NetDepositsCents: -bal["external"], FeesCents: bal["fees"], RealizedCents: -bal["realized_pnl"],
		DrawdownPct: paper.LossPct(max(a.PeakEquity, a.Equity()), a.Equity()), DayTrades: a.DayTrades, TradesToday: a.TradesToday,
		SessionOpen: paper.SessionOpen(now), MinutesToClose: paper.MinutesToClose(now), Holdings: []holdingView{}}
	v.MarketValueCents = v.EquityCents - v.CashCents
	if v.NetDepositsCents > 0 {
		v.TotalReturnPct = float64(v.EquityCents-v.NetDepositsCents) / float64(v.NetDepositsCents) * 100
	}
	if a.DayStartEquity > 0 {
		d := (float64(a.Equity())/float64(a.DayStartEquity) - 1) * 100
		v.DayChangePct = &d
	}
	for _, h := range a.Holdings {
		hv := holdingView{Symbol: h.Symbol, Qty: h.Qty, PriceCents: h.Price, ValueCents: h.Value(),
			UnrealizedCents: h.Value() - h.CostCents, OpenedAt: h.OpenedAt, ReservedQty: h.ReservedQty}
		if h.Qty > 0 {
			hv.AvgCostCents = paper.Cents(int64(h.CostCents) / h.Qty)
		}
		if h.CostCents > 0 {
			hv.UnrealizedPct = float64(hv.UnrealizedCents) / float64(h.CostCents) * 100
		}
		if v.EquityCents > 0 {
			hv.WeightPct = float64(hv.ValueCents) / float64(v.EquityCents) * 100
		}
		v.UnrealizedCents += hv.UnrealizedCents
		v.Holdings = append(v.Holdings, hv)
	}
	sort.Slice(v.Holdings, func(i, j int) bool { return v.Holdings[i].ValueCents > v.Holdings[j].ValueCents })
	return v, nil
}

func (s *Server) handleWallets(w http.ResponseWriter, r *http.Request) {
	if !s.paperReady(w) {
		return
	}
	wallets, err := s.deps.Store.Wallets(r.Context())
	if err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	out := make([]walletView, 0, len(wallets))
	for _, wl := range wallets {
		if wl.Status != "active" {
			out = append(out, walletView{Wallet: wl, Holdings: []holdingView{}})
			continue
		}
		if v, err := s.walletView(r.Context(), wl.ID); err == nil {
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"wallets": out, "defaults": paper.DefaultSettings()})
}

func (s *Server) handleCreateWallet(w http.ResponseWriter, r *http.Request) {
	if !s.paperReady(w) {
		return
	}
	var body struct {
		Name     string          `json:"name"`
		Settings *paper.Settings `json:"settings"`
		Deposit  float64         `json:"deposit"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 60 {
		writeError(w, http.StatusBadRequest, "invalid_request", "A wallet needs a name of up to 60 characters.")
		return
	}
	settings := paper.DefaultSettings()
	if body.Settings != nil {
		settings = *body.Settings
	}
	owner := ""
	if p, ok := profileFrom(r.Context()); ok {
		owner = p.Name
	}
	wl, err := s.deps.Store.CreateWallet(r.Context(), name, owner, settings)
	if err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	if body.Deposit > 0 {
		if _, err := s.deps.Paper.Deposit(r.Context(), wl.ID, paper.CentsOf(body.Deposit), fmt.Sprintf("open-%d", wl.ID)); err != nil {
			s.paperError(w, err, "deposit")
			return
		}
	}
	v, err := s.walletView(r.Context(), wl.ID)
	if err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	v, err := s.walletView(r.Context(), id)
	if err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleUpdateWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	var body struct {
		Name     string         `json:"name"`
		Settings paper.Settings `json:"settings"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "A wallet needs a name.")
		return
	}
	wl, err := s.deps.Store.UpdateWallet(r.Context(), id, strings.TrimSpace(body.Name), body.Settings)
	if err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	writeJSON(w, http.StatusOK, wl)
}

func (s *Server) handleCloseWallet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	if err := s.deps.Store.CloseWallet(r.Context(), id); err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePayment moves simulated money in or out. The idempotency key makes a
// retried request safe: it returns the first payment instead of a second.
func (s *Server) handlePayment(direction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "wallet")
		if !ok || !s.paperReady(w) {
			return
		}
		var body struct {
			Amount         float64 `json:"amount"`
			IdempotencyKey string  `json:"idempotency_key"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		key := strings.TrimSpace(body.IdempotencyKey)
		if key == "" || len(key) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_request", "idempotency_key is required (up to 80 characters).")
			return
		}
		pay := s.deps.Paper.Deposit
		if direction == "withdrawal" {
			pay = s.deps.Paper.Withdraw
		}
		p, err := pay(r.Context(), id, paper.CentsOf(body.Amount), key)
		if err != nil {
			s.paperError(w, err, direction)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	}
}

func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	list, err := s.deps.Store.Payments(r.Context(), id, 200)
	if err != nil {
		s.paperError(w, err, "payment")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": list})
}

func (s *Server) handlePaperOrders(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	list, err := s.deps.Store.Orders(r.Context(), id, r.URL.Query().Get("status"), 300)
	if err != nil {
		s.paperError(w, err, "order")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": list})
}

func (s *Server) handlePlacePaperOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	var req paper.Request
	if !decodeBody(w, r, &req) {
		return
	}
	o, fill, err := s.deps.Paper.Place(r.Context(), id, req, paper.SourceManual, nil)
	if err != nil {
		s.paperError(w, err, "order")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"order": o, "fill": fill})
}

func (s *Server) handleCancelPaperOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	oid, err := strconv.ParseInt(chi.URLParam(r, "oid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "That is not an order id.")
		return
	}
	o, err := s.deps.Paper.Cancel(r.Context(), id, oid)
	if err != nil {
		s.paperError(w, err, "order")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handlePaperActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	fills, err := s.deps.Store.Fills(r.Context(), id, 300)
	if err != nil {
		s.paperError(w, err, "fill")
		return
	}
	ledger, err := s.deps.Store.Ledger(r.Context(), id, 300)
	if err != nil {
		s.paperError(w, err, "ledger")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fills": fills, "ledger": ledger})
}

// luckPools caches the random traders' stock pool for an hour.
type luckPools struct {
	mu   sync.Mutex
	at   time.Time
	pool map[string][]marketdata.Candle
}

func (s *Server) handlePaperPerformance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	ctx := r.Context()
	wl, err := s.deps.Store.Wallet(ctx, id)
	if err != nil {
		s.paperError(w, err, "wallet")
		return
	}
	marks, err := s.deps.Store.EquityCurve(ctx, id, wl.CreatedAt.Add(-time.Minute))
	if err != nil {
		s.paperError(w, err, "equity")
		return
	}
	fills, _ := s.deps.Store.Fills(ctx, id, 5000)
	trips, _ := s.deps.Store.RoundTrips(ctx, id)
	var pool map[string][]marketdata.Candle
	if len(trips) >= 10 && s.deps.LuckPool != nil {
		s.luck.mu.Lock()
		if s.luck.pool == nil || s.deps.Now().Sub(s.luck.at) > time.Hour {
			s.luck.pool, s.luck.at = s.deps.LuckPool(ctx), s.deps.Now()
		}
		pool = s.luck.pool
		s.luck.mu.Unlock()
	}
	perf := paper.Analyze(marks, fills, trips, wl.Settings, pool, id)
	if marks == nil {
		marks = []paper.EquityPoint{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"performance": perf, "equity": marks})
}

// ---- agents -----------------------------------------------------------------

func (s *Server) handlePaperAgents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	agents, err := s.deps.Store.Agents(r.Context(), id)
	if err != nil {
		s.paperError(w, err, "agent")
		return
	}
	if agents == nil {
		agents = []paper.Agent{}
	}
	for i := range agents {
		if agents[i].Config.WebhookSecret != "" {
			agents[i].Config.WebhookSecret = "••••••"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "ai_available": s.deps.AI != nil && s.deps.Config.LLMConfigured()})
}

func (s *Server) handleSavePaperAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	var a paper.Agent
	if !decodeBody(w, r, &a) {
		return
	}
	if raw := chi.URLParam(r, "aid"); raw != "" {
		aid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "That is not an agent id.")
			return
		}
		a.ID = aid
		if a.Config.WebhookSecret == "••••••" {
			if all, err := s.deps.Store.Agents(r.Context(), id); err == nil {
				for _, old := range all {
					if old.ID == aid {
						a.Config.WebhookSecret = old.Config.WebhookSecret
					}
				}
			}
		}
	}
	a.WalletID = id
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "An agent needs a name.")
		return
	}
	switch a.Kind {
	case paper.AgentAI:
	case paper.AgentAlgorithm:
		if a.Config.AlgorithmID == 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "Choose the saved algorithm this agent enters on.")
			return
		}
	case paper.AgentWebhook:
		if err := s.deps.PaperWebhook.CheckURL(a.Config.WebhookURL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "kind must be ai, algorithm or webhook.")
		return
	}
	if len(a.Config.Symbols) == 0 && len(a.Config.WatchlistIDs) == 0 && !a.Config.UseScanner {
		writeError(w, http.StatusBadRequest, "invalid_request", "Give the agent somewhere to look: symbols, a watchlist, or the scanner's movers.")
		return
	}
	a.Config.EveryMinutes = min(max(a.Config.EveryMinutes, 5), 390)
	a.Config.MaxPositions = min(max(a.Config.MaxPositions, 1), 50)
	if len(a.Config.Instructions) > 2000 {
		a.Config.Instructions = a.Config.Instructions[:2000]
	}
	saved, err := s.deps.Store.SaveAgent(r.Context(), a)
	if err != nil {
		s.paperError(w, err, "agent")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeletePaperAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	aid, err := strconv.ParseInt(chi.URLParam(r, "aid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "That is not an agent id.")
		return
	}
	if err := s.deps.Store.DeleteAgent(r.Context(), id, aid); err != nil {
		s.paperError(w, err, "agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunPaperAgent runs an agent once, now, whatever its schedule.
func (s *Server) handleRunPaperAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) || s.deps.PaperRunner == nil {
		return
	}
	aid, err := strconv.ParseInt(chi.URLParam(r, "aid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "That is not an agent id.")
		return
	}
	agents, err := s.deps.Store.Agents(r.Context(), id)
	if err != nil {
		s.paperError(w, err, "agent")
		return
	}
	for _, a := range agents {
		if a.ID == aid {
			dec, err := s.deps.PaperRunner.Run(r.Context(), a)
			if err != nil && dec.Error == "" {
				dec.Error = err.Error()
			}
			writeJSON(w, http.StatusOK, dec)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "No such agent.")
}

func (s *Server) handlePaperDecisions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "wallet")
	if !ok || !s.paperReady(w) {
		return
	}
	aid, _ := strconv.ParseInt(r.URL.Query().Get("agent"), 10, 64)
	list, err := s.deps.Store.Decisions(r.Context(), id, aid, 100)
	if err != nil {
		s.paperError(w, err, "decision")
		return
	}
	if list == nil {
		list = []paper.Decision{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": list})
}
