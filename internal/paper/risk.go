package paper

import (
	"fmt"
	"math"
	"time"
)

// Holding is one position with its current price.
type Holding struct {
	Symbol string `json:"symbol"`
	Qty    int64  `json:"qty"`
	// CostCents is the total cost basis of the shares held.
	CostCents Cents     `json:"cost_cents"`
	Price     Cents     `json:"price_cents"`
	OpenedAt  time.Time `json:"opened_at"`
	// ReservedQty is already promised to open sell orders.
	ReservedQty int64 `json:"reserved_qty"`
}

// Value is the holding at its current price.
func (h Holding) Value() Cents { return h.Price * Cents(h.Qty) }

// Account is everything a pre-trade check needs to know.
type Account struct {
	Cash     Cents
	Reserved Cents // cash held back for open buys
	Holdings map[string]Holding
	// DayStartEquity is equity at the previous close, for the daily loss
	// limit; PeakEquity is the highest equity marked, for the drawdown limit.
	DayStartEquity Cents
	PeakEquity     Cents
	// DayTrades is how many day trades closed in the last five business days;
	// TradesToday how many orders filled today; OpenedToday which symbols
	// were first bought today, so selling them would be a day trade.
	DayTrades   int
	TradesToday int
	OpenedToday map[string]bool
}

// Equity is cash plus the market value of every holding.
func (a Account) Equity() Cents {
	e := a.Cash
	for _, h := range a.Holdings {
		e += h.Value()
	}
	return e
}

// BuyingPower is cash not already promised to an open buy.
func (a Account) BuyingPower() Cents { return a.Cash - a.Reserved }

// patternDayTraderEquity is FINRA's $25,000 minimum for unlimited day trades.
const patternDayTraderEquity = Cents(25_000_00)

// Check decides whether an order may be placed. Every order is held to the
// account's own cash and shares and to the day-trading rule; an agent's is
// also held to the wallet's risk limits.
func Check(r Request, price Cents, a Account, s Settings, source Source) error {
	equity := a.Equity()
	switch r.Side {
	case Buy:
		need := ReserveFor(r, price, s)
		if need > a.BuyingPower() {
			return fmt.Errorf("insufficient buying power: the order needs %s and %s is available", need, a.BuyingPower())
		}
	case Sell:
		h := a.Holdings[r.Symbol]
		if free := h.Qty - h.ReservedQty; r.Qty > free {
			return fmt.Errorf("you can sell at most %d shares of %s (short selling is not supported)", max(free, 0), r.Symbol)
		}
		if s.PatternDayTrader && equity < patternDayTraderEquity && a.OpenedToday[r.Symbol] && a.DayTrades >= 3 {
			return fmt.Errorf("pattern day trader rule: an account under $25,000 may make only 3 day trades in 5 business days, and this would be the 4th")
		}
	}
	if source == SourceManual {
		return nil
	}

	// Agent limits.
	if s.MinPrice > 0 && price.Dollars() < s.MinPrice {
		return fmt.Errorf("%s is under the $%.2f minimum price for agent trades", r.Symbol, s.MinPrice)
	}
	if a.TradesToday >= s.MaxTradesPerDay {
		return fmt.Errorf("the %d-trades-a-day limit is reached", s.MaxTradesPerDay)
	}
	if lost := LossPct(a.DayStartEquity, equity); r.Side == Buy && lost >= s.MaxDailyLossPct {
		return fmt.Errorf("down %.1f%% today, past the %.1f%% daily loss limit: no new positions until tomorrow", lost, s.MaxDailyLossPct)
	}
	if dd := LossPct(a.PeakEquity, equity); r.Side == Buy && dd >= s.MaxDrawdownPct {
		return fmt.Errorf("down %.1f%% from the peak, past the %.1f%% drawdown limit: new positions are halted", dd, s.MaxDrawdownPct)
	}
	if r.Side == Buy && equity > 0 {
		h := a.Holdings[r.Symbol]
		after := h.Value() + price*Cents(r.Qty)
		if pct := float64(after) / float64(equity) * 100; pct > s.MaxPositionPct {
			return fmt.Errorf("%s would be %.0f%% of the account, over the %.0f%% position limit", r.Symbol, pct, s.MaxPositionPct)
		}
	}
	return nil
}

// LossPct is how far below `from` the value `to` sits, in percent; zero when
// it is not below.
func LossPct(from, to Cents) float64 {
	if from <= 0 || to >= from {
		return 0
	}
	return math.Round(float64(from-to)/float64(from)*10000) / 100
}
