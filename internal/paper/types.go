package paper

import (
	"context"
	"encoding/json"
	"time"
)

// Wallet is one paper account.
type Wallet struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Owner      string    `json:"owner"`
	Currency   string    `json:"currency"`
	Status     string    `json:"status"`
	Settings   Settings  `json:"settings"`
	PeakEquity Cents     `json:"peak_equity_cents"`
	CreatedAt  time.Time `json:"created_at"`
}

// PaymentStatus is where a payment is in its provider's lifecycle.
type PaymentStatus string

const (
	PaymentCreated    PaymentStatus = "created"
	PaymentProcessing PaymentStatus = "processing"
	PaymentSucceeded  PaymentStatus = "succeeded"
	PaymentFailed     PaymentStatus = "failed"
	PaymentCanceled   PaymentStatus = "canceled"
)

// Final reports whether a payment can no longer change.
func (s PaymentStatus) Final() bool {
	return s == PaymentSucceeded || s == PaymentFailed || s == PaymentCanceled
}

// Payment moves money between the outside world and a wallet.
type Payment struct {
	ID             int64         `json:"id"`
	WalletID       int64         `json:"wallet_id"`
	Direction      string        `json:"direction"` // deposit | withdrawal
	AmountCents    Cents         `json:"amount_cents"`
	Currency       string        `json:"currency"`
	Provider       string        `json:"provider"`
	ProviderRef    string        `json:"provider_ref,omitempty"`
	Status         PaymentStatus `json:"status"`
	IdempotencyKey string        `json:"idempotency_key"`
	FailureReason  string        `json:"failure_reason,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// PaymentProvider takes a payment through its lifecycle. The simulated one
// approves at once; a card processor would return Processing and report the
// outcome later, which the store applies exactly once.
type PaymentProvider interface {
	Name() string
	// Begin starts a payment and reports its status and the provider's own
	// reference for it.
	Begin(ctx context.Context, p Payment) (PaymentStatus, string, error)
}

// Simulated is the payment provider for fictional money: every deposit and
// withdrawal succeeds immediately.
type Simulated struct{}

func (Simulated) Name() string { return "simulated" }

func (Simulated) Begin(_ context.Context, p Payment) (PaymentStatus, string, error) {
	return PaymentSucceeded, "sim_" + p.IdempotencyKey, nil
}

// Fill is one execution.
type Fill struct {
	ID            int64     `json:"id"`
	OrderID       int64     `json:"order_id"`
	WalletID      int64     `json:"wallet_id"`
	Symbol        string    `json:"symbol"`
	Side          Side      `json:"side"`
	Qty           int64     `json:"qty"`
	PriceCents    Cents     `json:"price_cents"`
	FeeCents      Cents     `json:"fee_cents"`
	QuoteCents    Cents     `json:"quote_cents"`
	RealizedCents *Cents    `json:"realized_cents,omitempty"`
	FilledAt      time.Time `json:"filled_at"`
}

// Posting is one leg of a ledger transaction.
type Posting struct {
	Account     string `json:"account"`
	AmountCents Cents  `json:"amount_cents"`
}

// LedgerTx is one balanced ledger transaction.
type LedgerTx struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	RefType   string    `json:"ref_type,omitempty"`
	RefID     *int64    `json:"ref_id,omitempty"`
	Memo      string    `json:"memo,omitempty"`
	Postings  []Posting `json:"postings"`
	CreatedAt time.Time `json:"created_at"`
}

// EquityPoint is one mark of a wallet's value, with the S&P 500 beside it.
type EquityPoint struct {
	At          time.Time `json:"at"`
	CashCents   Cents     `json:"cash_cents"`
	MarketCents Cents     `json:"market_value_cents"`
	EquityCents Cents     `json:"equity_cents"`
	Benchmark   *float64  `json:"benchmark,omitempty"`
}

// AgentKind is what drives an agent's decisions.
type AgentKind string

const (
	AgentAI        AgentKind = "ai"
	AgentAlgorithm AgentKind = "algorithm"
	AgentWebhook   AgentKind = "webhook"
)

// AgentConfig is an agent's settings. Fields apply by kind.
type AgentConfig struct {
	// Universe is where an agent may look: its symbols plus the watchlists.
	Symbols      []string `json:"symbols,omitempty"`
	WatchlistIDs []int64  `json:"watchlist_ids,omitempty"`
	// UseScanner adds the scanner's latest unusual movers to an AI agent's
	// candidates.
	UseScanner bool `json:"use_scanner"`
	// EveryMinutes is how often it decides during the session.
	EveryMinutes int `json:"every_minutes"`
	// MaxPositions bounds how many names it holds at once.
	MaxPositions int `json:"max_positions"`
	// Intraday closes every position it opened before the session ends.
	Intraday bool `json:"intraday"`
	// Instructions steer an AI agent; the risk limits bind regardless.
	Instructions string `json:"instructions,omitempty"`
	// AlgorithmID is the saved rule an algorithm agent enters on, with an
	// optional exit rule; exits otherwise come from the stop and target.
	AlgorithmID     int64   `json:"algorithm_id,omitempty"`
	ExitAlgorithmID int64   `json:"exit_algorithm_id,omitempty"`
	SizePct         float64 `json:"size_pct,omitempty"`
	// WebhookURL receives the snapshot and returns intents, signed with
	// WebhookSecret; for a strategy written elsewhere.
	WebhookURL    string `json:"webhook_url,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// Agent is one automated trader bound to a wallet.
type Agent struct {
	ID           int64       `json:"id"`
	WalletID     int64       `json:"wallet_id"`
	Kind         AgentKind   `json:"kind"`
	Name         string      `json:"name"`
	Enabled      bool        `json:"enabled"`
	Config       AgentConfig `json:"config"`
	HaltedReason string      `json:"halted_reason,omitempty"`
	LastRunAt    *time.Time  `json:"last_run_at,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
}

// Intent is one trade an agent wants, before risk checks.
type Intent struct {
	Symbol string `json:"symbol"`
	Side   Side   `json:"side"`
	Qty    int64  `json:"qty,omitempty"`
	// SizePct sizes a buy as a share of equity when Qty is zero.
	SizePct       float64  `json:"size_pct,omitempty"`
	LimitPrice    *float64 `json:"limit_price,omitempty"`
	StopLossPct   *float64 `json:"stop_loss_pct,omitempty"`
	TakeProfitPct *float64 `json:"take_profit_pct,omitempty"`
	Confidence    float64  `json:"confidence,omitempty"`
	Reason        string   `json:"reason"`
}

// Decision is one run of an agent, kept whether or not it traded.
type Decision struct {
	ID       int64           `json:"id"`
	AgentID  int64           `json:"agent_id"`
	WalletID int64           `json:"wallet_id"`
	At       time.Time       `json:"at"`
	Summary  string          `json:"summary"`
	Intents  []Intent        `json:"intents"`
	OrderIDs []int64         `json:"order_ids"`
	Rejected json.RawMessage `json:"rejected"`
	Model    string          `json:"model,omitempty"`
	Error    string          `json:"error,omitempty"`
}
