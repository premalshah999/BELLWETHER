package paper

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Candidate is one stock an agent may consider, with what is known about it.
type Candidate struct {
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name,omitempty"`
	Sector       string  `json:"sector,omitempty"`
	Price        float64 `json:"price"`
	ChangePct    float64 `json:"change_pct"`
	Change5dPct  float64 `json:"change_5d_pct"`
	RSI14        float64 `json:"rsi14"`
	VolumeRatio  float64 `json:"volume_ratio"`
	FromSMA20Pct float64 `json:"from_sma20_pct"`
	Signal       string  `json:"signal,omitempty"`
	// ModelPercentile is the forecast model's rank for the stock: 100 is the
	// most likely to outperform over the next five sessions.
	ModelPercentile *float64 `json:"model_percentile,omitempty"`
	Headlines       []string `json:"headlines,omitempty"`
}

// HoldingView is a holding as an agent sees it.
type HoldingView struct {
	Symbol        string  `json:"symbol"`
	Qty           int64   `json:"qty"`
	AvgCost       float64 `json:"avg_cost"`
	Price         float64 `json:"price"`
	UnrealizedPct float64 `json:"unrealized_pct"`
	HeldDays      int     `json:"held_days"`
}

// Snapshot is everything an agent sees when it decides.
type Snapshot struct {
	At             time.Time     `json:"at"`
	SessionOpen    bool          `json:"session_open"`
	MinutesToClose int           `json:"minutes_to_close"`
	Cash           float64       `json:"cash"`
	BuyingPower    float64       `json:"buying_power"`
	Equity         float64       `json:"equity"`
	DayChangePct   float64       `json:"day_change_pct"`
	DrawdownPct    float64       `json:"drawdown_pct"`
	Holdings       []HoldingView `json:"holdings"`
	OpenOrders     []Order       `json:"open_orders"`
	Candidates     []Candidate   `json:"candidates"`
	Limits         Settings      `json:"limits"`
	MaxPositions   int           `json:"max_positions"`
	Intraday       bool          `json:"intraday"`
	Instructions   string        `json:"instructions,omitempty"`
}

// Strategy decides what an agent wants to trade.
type Strategy interface {
	Decide(ctx context.Context, a Agent, snap Snapshot) (summary string, intents []Intent, model string, err error)
}

// Candidates builds the list of stocks an agent may consider.
type Candidates interface {
	Candidates(ctx context.Context, symbols []string, watchlists []int64, useScanner, useForecast bool) ([]Candidate, error)
}

// AgentStore is what the runner reads and writes beyond the engine's store.
type AgentStore interface {
	Agents(ctx context.Context, walletID int64) ([]Agent, error)
	OpenOrders(ctx context.Context) ([]Order, error)
	TouchAgent(ctx context.Context, agentID int64, at time.Time, haltReason string) error
	SaveDecision(ctx context.Context, d Decision) error
}

// Runner runs agents on their cadence during the session.
type Runner struct {
	Engine     *Engine
	Store      AgentStore
	Candidates Candidates
	Strategies map[AgentKind]Strategy
	Log        *slog.Logger
}

// RunDue runs every enabled agent whose interval has passed.
func (r *Runner) RunDue(ctx context.Context) {
	now := r.Engine.now()
	if !SessionOpen(now) {
		return
	}
	agents, err := r.Store.Agents(ctx, 0)
	if err != nil {
		r.Log.Warn("paper: agents unreadable", "err", err)
		return
	}
	for _, a := range agents {
		every := time.Duration(max(a.Config.EveryMinutes, 5)) * time.Minute
		closing := a.Config.Intraday && MinutesToClose(now) <= 10
		if a.LastRunAt != nil && now.Sub(*a.LastRunAt) < every && !closing {
			continue
		}
		if _, err := r.Run(ctx, a); err != nil {
			r.Log.Warn("paper: agent run failed", "agent", a.ID, "err", err)
		}
	}
}

// Run makes one decision for an agent and places its orders.
func (r *Runner) Run(ctx context.Context, a Agent) (Decision, error) {
	now := r.Engine.now()
	dec := Decision{AgentID: a.ID, WalletID: a.WalletID, At: now}
	defer func() {
		if err := r.Store.SaveDecision(context.WithoutCancel(ctx), dec); err != nil {
			r.Log.Warn("paper: decision not saved", "agent", a.ID, "err", err)
		}
	}()
	w, acct, err := r.Engine.Snapshot(ctx, a.WalletID)
	if err != nil {
		dec.Error = err.Error()
		return dec, err
	}
	s := w.Settings
	equity := acct.Equity()

	// The drawdown limit halts the agent outright: past it, the strategy has
	// failed on its own terms and should not keep trading until reviewed.
	if dd := LossPct(acct.PeakEquity, equity); dd >= s.MaxDrawdownPct {
		dec.Summary = fmt.Sprintf("Halted: down %.1f%% from the peak, past the %.1f%% drawdown limit.", dd, s.MaxDrawdownPct)
		_ = r.Store.TouchAgent(ctx, a.ID, now, dec.Summary)
		return dec, nil
	}
	_ = r.Store.TouchAgent(ctx, a.ID, now, "")

	// An intraday agent is flat before the close, whatever the strategy says.
	if a.Config.Intraday && MinutesToClose(now) <= 10 {
		dec.Summary = "Closing every position before the session ends (intraday)."
		for _, h := range acct.Holdings {
			if free := h.Qty - h.ReservedQty; free > 0 {
				dec.Intents = append(dec.Intents, Intent{Symbol: h.Symbol, Side: Sell, Qty: free, Reason: "intraday: flat before the close"})
			}
		}
		r.execute(ctx, a, w, acct, &dec)
		return dec, nil
	}

	snap, err := r.snapshot(ctx, a, w, acct, now)
	if err != nil {
		dec.Error = err.Error()
		return dec, err
	}
	strat, ok := r.Strategies[a.Kind]
	if !ok {
		dec.Error = fmt.Sprintf("no %s strategy is configured on this server", a.Kind)
		return dec, fmt.Errorf("%s", dec.Error)
	}
	summary, intents, model, err := strat.Decide(ctx, a, snap)
	dec.Summary, dec.Intents, dec.Model = summary, intents, model
	if err != nil {
		dec.Error = err.Error()
		return dec, err
	}
	r.execute(ctx, a, w, acct, &dec)
	return dec, nil
}

func (r *Runner) snapshot(ctx context.Context, a Agent, w Wallet, acct Account, now time.Time) (Snapshot, error) {
	equity := acct.Equity()
	snap := Snapshot{
		At: now, SessionOpen: SessionOpen(now), MinutesToClose: MinutesToClose(now),
		Cash: acct.Cash.Dollars(), BuyingPower: acct.BuyingPower().Dollars(), Equity: equity.Dollars(),
		DrawdownPct: LossPct(acct.PeakEquity, equity), Limits: w.Settings,
		MaxPositions: max(a.Config.MaxPositions, 1), Intraday: a.Config.Intraday, Instructions: a.Config.Instructions,
	}
	if acct.DayStartEquity > 0 {
		snap.DayChangePct = math.Round((float64(equity)/float64(acct.DayStartEquity)-1)*10000) / 100
	}
	held := make([]string, 0, len(acct.Holdings))
	for _, h := range acct.Holdings {
		avg := float64(h.CostCents) / float64(max(h.Qty, 1)) / 100
		v := HoldingView{Symbol: h.Symbol, Qty: h.Qty, AvgCost: math.Round(avg*100) / 100, Price: h.Price.Dollars(),
			HeldDays: int(now.Sub(h.OpenedAt).Hours() / 24)}
		if avg > 0 {
			v.UnrealizedPct = math.Round((h.Price.Dollars()/avg-1)*10000) / 100
		}
		snap.Holdings = append(snap.Holdings, v)
		held = append(held, h.Symbol)
	}
	sort.Slice(snap.Holdings, func(i, j int) bool { return snap.Holdings[i].Symbol < snap.Holdings[j].Symbol })
	if open, err := r.Store.OpenOrders(ctx); err == nil {
		for _, o := range open {
			if o.WalletID == w.ID {
				snap.OpenOrders = append(snap.OpenOrders, o)
			}
		}
	}
	cands, err := r.Candidates.Candidates(ctx, append(append([]string{}, a.Config.Symbols...), held...), a.Config.WatchlistIDs, a.Config.UseScanner, a.Config.UseForecast)
	if err != nil {
		return snap, fmt.Errorf("candidates: %w", err)
	}
	snap.Candidates = cands
	return snap, nil
}

// execute turns intents into orders, sizing them and attaching exits, and
// records what was placed and what the risk checks refused.
func (r *Runner) execute(ctx context.Context, a Agent, w Wallet, acct Account, dec *Decision) {
	type refusal struct {
		Symbol string `json:"symbol"`
		Side   Side   `json:"side"`
		Reason string `json:"reason"`
	}
	var refused []refusal
	positions := len(acct.Holdings)
	equity := acct.Equity()
	for i, in := range dec.Intents {
		req := Request{
			Symbol: strings.ToUpper(strings.TrimSpace(in.Symbol)), Side: in.Side, Qty: in.Qty, Type: Market, TIF: Day,
			ClientOrderID: fmt.Sprintf("agent-%d-%d-%d", a.ID, dec.At.Unix(), i), Reason: in.Reason,
		}
		if in.LimitPrice != nil && *in.LimitPrice > 0 {
			req.Type, req.LimitPrice = Limit, in.LimitPrice
		}
		switch in.Side {
		case Buy:
			if _, held := acct.Holdings[req.Symbol]; !held && positions >= max(a.Config.MaxPositions, 1) {
				refused = append(refused, refusal{req.Symbol, in.Side, fmt.Sprintf("already holding the maximum of %d positions", a.Config.MaxPositions)})
				continue
			}
			if req.Qty <= 0 {
				pct := in.SizePct
				if pct <= 0 {
					pct = math.Min(w.Settings.MaxPositionPct, 100/float64(max(a.Config.MaxPositions, 1)))
				}
				price, _, err := r.Engine.Prices.Quote(ctx, req.Symbol)
				if err != nil || price <= 0 {
					refused = append(refused, refusal{req.Symbol, in.Side, "no current price"})
					continue
				}
				budget := math.Min(float64(equity)*pct/100, float64(acct.BuyingPower())*0.98)
				req.Qty = int64(budget / float64(price))
				if req.Qty <= 0 {
					refused = append(refused, refusal{req.Symbol, in.Side, "the position size rounds to zero shares"})
					continue
				}
			}
			sl, tp := in.StopLossPct, in.TakeProfitPct
			if sl == nil && w.Settings.StopLossPct > 0 {
				v := w.Settings.StopLossPct
				sl = &v
			}
			if tp == nil && w.Settings.TakeProfitPct > 0 {
				v := w.Settings.TakeProfitPct
				tp = &v
			}
			req.StopLossPct, req.TakeProfitPct = sl, tp
		case Sell:
			h, held := acct.Holdings[req.Symbol]
			if !held {
				refused = append(refused, refusal{req.Symbol, in.Side, "not held"})
				continue
			}
			if free := h.Qty - h.ReservedQty; req.Qty <= 0 || req.Qty > free {
				req.Qty = free
			}
			if req.Qty <= 0 {
				// Everything is promised to exits; cancel them so the sell can stand.
				for _, o := range openExits(ctx, r.Store, w.ID, req.Symbol) {
					_, _ = r.Engine.Cancel(ctx, w.ID, o.ID)
				}
				req.Qty = h.Qty
			}
		default:
			refused = append(refused, refusal{req.Symbol, in.Side, "side must be buy or sell"})
			continue
		}
		id := a.ID
		o, _, err := r.Engine.Place(ctx, w.ID, req, SourceAgent, &id)
		if err != nil {
			refused = append(refused, refusal{req.Symbol, in.Side, err.Error()})
			continue
		}
		dec.OrderIDs = append(dec.OrderIDs, o.ID)
		if in.Side == Buy {
			positions++
		}
	}
	if len(refused) > 0 {
		dec.Rejected, _ = json.Marshal(refused)
	}
}

func openExits(ctx context.Context, s AgentStore, walletID int64, symbol string) []Order {
	all, err := s.OpenOrders(ctx)
	if err != nil {
		return nil
	}
	var out []Order
	for _, o := range all {
		if o.WalletID == walletID && o.Symbol == symbol && o.Side == Sell {
			out = append(out, o)
		}
	}
	return out
}

// Webhook asks a strategy running elsewhere: the snapshot is POSTed as JSON,
// signed with HMAC-SHA256 over the body in X-Bellwether-Signature, and the
// reply is {"summary": "...", "intents": [...]}. It is how an algorithm
// written in any language trades a wallet.
type Webhook struct {
	HTTP *http.Client
	// Allow lists host:port pairs reachable over plain HTTP or on private
	// addresses, for a strategy running beside the app. Anything else must be
	// HTTPS to a public address.
	Allow []string
}

// CheckURL validates a webhook target before it is saved or called.
func (wh Webhook) CheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("the webhook must be a full URL")
	}
	for _, h := range wh.Allow {
		if strings.EqualFold(u.Host, strings.TrimSpace(h)) && (u.Scheme == "http" || u.Scheme == "https") {
			return nil
		}
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the webhook must use https (or be listed in PAPER_WEBHOOK_ALLOW)")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("the webhook host does not resolve")
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("the webhook resolves to a private address (list it in PAPER_WEBHOOK_ALLOW to allow it)")
		}
	}
	return nil
}

// Decide posts the snapshot and reads back intents.
func (wh Webhook) Decide(ctx context.Context, a Agent, snap Snapshot) (string, []Intent, string, error) {
	if err := wh.CheckURL(a.Config.WebhookURL); err != nil {
		return "", nil, "", err
	}
	body, err := json.Marshal(map[string]any{"agent_id": a.ID, "wallet_id": a.WalletID, "snapshot": snap})
	if err != nil {
		return "", nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Config.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return "", nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.Config.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(a.Config.WebhookSecret))
		mac.Write(body)
		req.Header.Set("X-Bellwether-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := wh.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	// Never follow a redirect: it could lead anywhere CheckURL did not look.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", nil, "", fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, "", fmt.Errorf("webhook answered %d", resp.StatusCode)
	}
	var out struct {
		Summary string   `json:"summary"`
		Intents []Intent `json:"intents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", nil, "", fmt.Errorf("webhook reply is not valid JSON: %w", err)
	}
	return out.Summary, out.Intents, "webhook", nil
}

// RuleCheck evaluates a saved algorithm on one symbol's latest closed bar.
type RuleCheck func(ctx context.Context, algorithmID int64, symbol string) (met bool, why string, err error)

// Rules trades a saved algorithm: buy when the entry rule holds on a symbol
// not held, sell when the exit rule holds on one that is. The stop and
// target on every buy are the other exits.
type Rules struct{ Check RuleCheck }

// Decide evaluates the rules over the agent's universe.
func (ru Rules) Decide(ctx context.Context, a Agent, snap Snapshot) (string, []Intent, string, error) {
	if a.Config.AlgorithmID == 0 {
		return "", nil, "", fmt.Errorf("this agent has no algorithm")
	}
	held := map[string]bool{}
	for _, h := range snap.Holdings {
		held[h.Symbol] = true
	}
	var intents []Intent
	entries, exits := 0, 0
	for _, c := range snap.Candidates {
		if held[c.Symbol] {
			if a.Config.ExitAlgorithmID == 0 {
				continue
			}
			met, why, err := ru.Check(ctx, a.Config.ExitAlgorithmID, c.Symbol)
			if err == nil && met {
				intents = append(intents, Intent{Symbol: c.Symbol, Side: Sell, Reason: "exit rule: " + why})
				exits++
			}
			continue
		}
		met, why, err := ru.Check(ctx, a.Config.AlgorithmID, c.Symbol)
		if err == nil && met {
			intents = append(intents, Intent{Symbol: c.Symbol, Side: Buy, SizePct: a.Config.SizePct, Reason: "entry rule: " + why})
			entries++
		}
	}
	return fmt.Sprintf("Checked %d symbols: %d entry signals, %d exit signals.", len(snap.Candidates), entries, exits), intents, "rules", nil
}
