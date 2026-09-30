// Package paper is a paper-trading account: simulated money, real prices.
//
// It exists to answer one question honestly: can a strategy, a rule or an
// AI make money, or is it gambling? So it is built to be as unforgiving as a
// real brokerage account. Money is whole cents, every movement is a balanced
// ledger transaction, fills pay slippage and the fees US brokers pass on
// (SEC Section 31 and FINRA TAF on sales), orders fill only during the
// session at prices the market actually traded, the pattern-day-trader rule
// applies to small accounts, and agents trade inside hard risk limits.
//
// No real order is ever placed. There is no code path to a broker.
package paper

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Cents is money in whole cents. Floating-point dollars never touch a
// balance: they are converted at the edge and rounded once.
type Cents int64

// CentsOf converts dollars to cents, rounding to the nearest cent.
func CentsOf(dollars float64) Cents { return Cents(math.Round(dollars * 100)) }

// Dollars is the amount as a float, for display and ratios only.
func (c Cents) Dollars() float64 { return float64(c) / 100 }

func (c Cents) String() string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

// Side is buy or sell.
type Side string

const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// OrderType is how an order is priced.
type OrderType string

const (
	Market    OrderType = "market"
	Limit     OrderType = "limit"
	Stop      OrderType = "stop"
	StopLimit OrderType = "stop_limit"
)

// TIF is how long an order lives: the session, or until canceled (capped at
// ninety days, as brokers do).
type TIF string

const (
	Day TIF = "day"
	GTC TIF = "gtc"
)

// Status is an order's state. Accepted is open; the rest are final.
type Status string

const (
	Accepted Status = "accepted"
	Filled   Status = "filled"
	Canceled Status = "canceled"
	Rejected Status = "rejected"
	Expired  Status = "expired"
)

// Source says who placed an order.
type Source string

const (
	SourceManual     Source = "manual"
	SourceAgent      Source = "agent"
	SourceProtective Source = "protective" // a stop or target placed by a fill
)

// Settings are one wallet's execution realism and risk limits.
type Settings struct {
	// Commission per order, in cents. Most US brokers charge zero.
	CommissionCents int64 `json:"commission_cents"`
	// SlippageBps is paid on every market and stop fill: the spread and the
	// price moving while an order routes. Five basis points is typical for a
	// liquid large cap; thin names are worse.
	SlippageBps float64 `json:"slippage_bps"`
	// SEC Section 31 fee on sale proceeds, dollars per million.
	SECFeePerMillion float64 `json:"sec_fee_per_million"`
	// FINRA Trading Activity Fee on shares sold, per share, capped per trade.
	TAFPerShare float64 `json:"taf_per_share"`
	TAFMaxCents int64   `json:"taf_max_cents"`
	// PatternDayTrader applies FINRA rule 4210: an account under $25,000 may
	// make at most three day trades in five business days.
	PatternDayTrader bool `json:"pattern_day_trader"`

	// Limits that bind agents. Manual orders are only held to the account's
	// own cash and shares.
	MaxPositionPct  float64 `json:"max_position_pct"`
	MaxDailyLossPct float64 `json:"max_daily_loss_pct"`
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`
	MaxTradesPerDay int     `json:"max_trades_per_day"`
	MinPrice        float64 `json:"min_price"`
	// StopLossPct and TakeProfitPct are attached to an agent's buy when it
	// names none: every agent position has an exit.
	StopLossPct   float64 `json:"stop_loss_pct"`
	TakeProfitPct float64 `json:"take_profit_pct"`
	// FlattenAtClose sells every agent-opened position before the close, for
	// an intraday account that must not hold overnight.
	FlattenAtClose bool `json:"flatten_at_close"`

	// WeeklyGoalPct is the return the operator is aiming for. It is tracked
	// and reported against; nothing trades harder to reach it.
	WeeklyGoalPct float64 `json:"weekly_goal_pct"`
}

// DefaultSettings are a realistic retail account.
func DefaultSettings() Settings {
	return Settings{
		SlippageBps:      5,
		SECFeePerMillion: 27.80,
		TAFPerShare:      0.000166,
		TAFMaxCents:      830,
		PatternDayTrader: true,
		MaxPositionPct:   25,
		MaxDailyLossPct:  3,
		MaxDrawdownPct:   15,
		MaxTradesPerDay:  20,
		MinPrice:         5,
		StopLossPct:      4,
		TakeProfitPct:    8,
		WeeklyGoalPct:    10,
	}
}

// Normalize fills zero values from the defaults and clamps the rest, so a
// partial settings document from the UI is always usable.
func (s Settings) Normalize() Settings {
	d := DefaultSettings()
	if s.SlippageBps <= 0 {
		s.SlippageBps = d.SlippageBps
	}
	if s.SECFeePerMillion <= 0 {
		s.SECFeePerMillion = d.SECFeePerMillion
	}
	if s.TAFPerShare <= 0 {
		s.TAFPerShare = d.TAFPerShare
	}
	if s.TAFMaxCents <= 0 {
		s.TAFMaxCents = d.TAFMaxCents
	}
	if s.MaxPositionPct <= 0 || s.MaxPositionPct > 100 {
		s.MaxPositionPct = d.MaxPositionPct
	}
	if s.MaxDailyLossPct <= 0 {
		s.MaxDailyLossPct = d.MaxDailyLossPct
	}
	if s.MaxDrawdownPct <= 0 {
		s.MaxDrawdownPct = d.MaxDrawdownPct
	}
	if s.MaxTradesPerDay <= 0 {
		s.MaxTradesPerDay = d.MaxTradesPerDay
	}
	if s.MinPrice < 0 {
		s.MinPrice = 0
	}
	if s.WeeklyGoalPct <= 0 {
		s.WeeklyGoalPct = d.WeeklyGoalPct
	}
	s.SlippageBps = math.Min(s.SlippageBps, 500)
	return s
}

// Order is one paper order.
type Order struct {
	ID            int64      `json:"id"`
	WalletID      int64      `json:"wallet_id"`
	ClientOrderID string     `json:"client_order_id"`
	Symbol        string     `json:"symbol"`
	Side          Side       `json:"side"`
	Type          OrderType  `json:"type"`
	Qty           int64      `json:"qty"`
	LimitCents    *Cents     `json:"limit_cents,omitempty"`
	StopCents     *Cents     `json:"stop_cents,omitempty"`
	TIF           TIF        `json:"tif"`
	Status        Status     `json:"status"`
	FilledQty     int64      `json:"filled_qty"`
	AvgFillCents  *Cents     `json:"avg_fill_cents,omitempty"`
	ReservedCents Cents      `json:"reserved_cents"`
	Source        Source     `json:"source"`
	AgentID       *int64     `json:"agent_id,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	RejectReason  string     `json:"reject_reason,omitempty"`
	StopLossPct   *float64   `json:"stop_loss_pct,omitempty"`
	TakeProfitPct *float64   `json:"take_profit_pct,omitempty"`
	ParentOrderID *int64     `json:"parent_order_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	FilledAt      *time.Time `json:"filled_at,omitempty"`
}

// Request is an order as asked for, before it is checked and priced.
type Request struct {
	ClientOrderID string    `json:"client_order_id"`
	Symbol        string    `json:"symbol"`
	Side          Side      `json:"side"`
	Type          OrderType `json:"type"`
	Qty           int64     `json:"qty"`
	LimitPrice    *float64  `json:"limit_price,omitempty"`
	StopPrice     *float64  `json:"stop_price,omitempty"`
	TIF           TIF       `json:"tif"`
	StopLossPct   *float64  `json:"stop_loss_pct,omitempty"`
	TakeProfitPct *float64  `json:"take_profit_pct,omitempty"`
	Reason        string    `json:"reason,omitempty"`
}

// Validate checks a request's own shape, before any account is consulted.
func (r *Request) Validate() error {
	r.Symbol = strings.ToUpper(strings.TrimSpace(r.Symbol))
	if r.Type == "" {
		r.Type = Market
	}
	if r.TIF == "" {
		r.TIF = Day
	}
	switch {
	case r.Symbol == "":
		return fmt.Errorf("a symbol is required")
	case r.Side != Buy && r.Side != Sell:
		return fmt.Errorf("side must be buy or sell")
	case r.Qty <= 0:
		return fmt.Errorf("quantity must be a positive whole number of shares")
	case r.Qty > 10_000_000:
		return fmt.Errorf("quantity is implausibly large")
	case r.TIF != Day && r.TIF != GTC:
		return fmt.Errorf("time in force must be day or gtc")
	}
	positive := func(p *float64) bool { return p != nil && *p > 0 && !math.IsInf(*p, 0) }
	switch r.Type {
	case Market:
		r.LimitPrice, r.StopPrice = nil, nil
	case Limit:
		if !positive(r.LimitPrice) {
			return fmt.Errorf("a limit order needs a positive limit price")
		}
		r.StopPrice = nil
	case Stop:
		if !positive(r.StopPrice) {
			return fmt.Errorf("a stop order needs a positive stop price")
		}
		r.LimitPrice = nil
	case StopLimit:
		if !positive(r.StopPrice) || !positive(r.LimitPrice) {
			return fmt.Errorf("a stop-limit order needs positive stop and limit prices")
		}
	default:
		return fmt.Errorf("type must be market, limit, stop or stop_limit")
	}
	for _, p := range []*float64{r.StopLossPct, r.TakeProfitPct} {
		if p != nil && (*p <= 0 || *p >= 100) {
			return fmt.Errorf("stop loss and take profit are percentages between 0 and 100")
		}
	}
	if r.Side == Sell {
		r.StopLossPct, r.TakeProfitPct = nil, nil
	}
	return nil
}

// Fees is what a fill costs beyond the price: commission on every order,
// and on sales the SEC Section 31 fee (rounded up to the cent, as the SEC
// does) and FINRA's TAF (per share, capped).
func Fees(side Side, qty int64, price Cents, s Settings) Cents {
	fee := Cents(s.CommissionCents)
	if side == Sell {
		proceeds := float64(price) * float64(qty) / 100 // dollars
		fee += Cents(math.Ceil(proceeds * s.SECFeePerMillion / 1e6 * 100))
		taf := Cents(math.Ceil(float64(qty) * s.TAFPerShare * 100))
		fee += min(taf, Cents(s.TAFMaxCents))
	}
	return fee
}

// Slipped is the price a market or stop order actually gets: worse than the
// quote by the slippage, rounded against the trader.
func Slipped(side Side, quote Cents, bps float64) Cents {
	adj := float64(quote) * bps / 1e4
	if side == Buy {
		return quote + Cents(math.Ceil(adj))
	}
	return max(1, quote-Cents(math.Ceil(adj)))
}

// Bar is a slice of trading: the prices an order can be matched against.
type Bar struct {
	At                     time.Time
	Open, High, Low, Close Cents
}

// Match decides whether an open order fills during a bar, and at what price
// before fees. Limits fill at the limit or better when the bar trades
// through them; stops trigger when touched and then fill like a market
// order, paying slippage, and if the bar opened through the stop they fill
// at the open, as a gap does to a real stop.
func Match(o Order, bar Bar, s Settings) (Cents, bool) {
	switch o.Type {
	case Market:
		return Slipped(o.Side, bar.Close, s.SlippageBps), true
	case Limit:
		return matchLimit(o.Side, *o.LimitCents, bar)
	case Stop, StopLimit:
		stop := *o.StopCents
		var triggered bool
		var at Cents
		if o.Side == Buy {
			triggered, at = bar.High >= stop, max(stop, bar.Open)
		} else {
			triggered, at = bar.Low <= stop, min(stop, bar.Open)
		}
		if !triggered {
			return 0, false
		}
		if o.Type == Stop {
			return Slipped(o.Side, at, s.SlippageBps), true
		}
		return matchLimit(o.Side, *o.LimitCents, bar)
	}
	return 0, false
}

func matchLimit(side Side, limit Cents, bar Bar) (Cents, bool) {
	if side == Buy {
		if bar.Low > limit {
			return 0, false
		}
		return min(limit, bar.Open), true
	}
	if bar.High < limit {
		return 0, false
	}
	return max(limit, bar.Open), true
}

// ReserveFor is how much cash an open buy holds back: its worst-case cost at
// the reference price plus a margin for the price moving before it fills.
func ReserveFor(r Request, ref Cents, s Settings) Cents {
	if r.Side != Buy {
		return 0
	}
	price := ref
	if r.LimitPrice != nil {
		price = CentsOf(*r.LimitPrice)
	} else if r.StopPrice != nil {
		price = CentsOf(*r.StopPrice)
	}
	if r.Type == Market || r.Type == Stop {
		price = Cents(math.Ceil(float64(price) * 1.02)) // room to move while it routes
	}
	return price*Cents(r.Qty) + Fees(Buy, r.Qty, price, s)
}
