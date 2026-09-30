package paper

import (
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func f(v float64) *float64 { return &v }

func TestFeesOnASaleIncludeSECAndTAF(t *testing.T) {
	s := DefaultSettings()
	// 1,000 shares at $150: proceeds $150,000. SEC: 150,000 * 27.80 / 1e6 =
	// $4.17. TAF: 1,000 * $0.000166 = $0.166, rounded up to $0.17.
	if got := Fees(Sell, 1000, CentsOf(150), s); got != CentsOf(4.17)+17 {
		t.Errorf("sell fees = %s, want $4.34", got)
	}
	if got := Fees(Buy, 1000, CentsOf(150), s); got != 0 {
		t.Errorf("buy fees = %s, want $0.00 with no commission", got)
	}
	// TAF is capped per trade.
	if got := Fees(Sell, 1_000_000, CentsOf(1), s) - CentsOf(27.80); got != 830 {
		t.Errorf("TAF on a million shares = %s, want the $8.30 cap", got)
	}
}

func TestSlippageAlwaysCostsTheTrader(t *testing.T) {
	if got := Slipped(Buy, CentsOf(100), 5); got != CentsOf(100.05) {
		t.Errorf("buy = %s", got)
	}
	if got := Slipped(Sell, CentsOf(100), 5); got != CentsOf(99.95) {
		t.Errorf("sell = %s", got)
	}
}

func bar(o, h, l, c float64) Bar {
	return Bar{Open: CentsOf(o), High: CentsOf(h), Low: CentsOf(l), Close: CentsOf(c)}
}

func TestMatch(t *testing.T) {
	s := DefaultSettings()
	s.SlippageBps = 0
	lim := CentsOf(99)
	stop := CentsOf(95)
	cases := []struct {
		name  string
		o     Order
		b     Bar
		ok    bool
		price float64
	}{
		{"limit buy untouched", Order{Side: Buy, Type: Limit, LimitCents: &lim}, bar(100, 101, 99.5, 100), false, 0},
		{"limit buy touched", Order{Side: Buy, Type: Limit, LimitCents: &lim}, bar(100, 101, 98, 100), true, 99},
		{"limit buy gaps below", Order{Side: Buy, Type: Limit, LimitCents: &lim}, bar(97, 98, 96, 97), true, 97},
		{"sell stop hit", Order{Side: Sell, Type: Stop, StopCents: &stop}, bar(97, 98, 94, 96), true, 95},
		{"sell stop gaps through", Order{Side: Sell, Type: Stop, StopCents: &stop}, bar(90, 92, 89, 91), true, 90},
		{"sell stop untouched", Order{Side: Sell, Type: Stop, StopCents: &stop}, bar(97, 98, 96, 97), false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Match(tc.o, tc.b, s)
			if ok != tc.ok || (ok && got != CentsOf(tc.price)) {
				t.Errorf("Match = %s, %v; want %.2f, %v", got, ok, tc.price, tc.ok)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	bad := []Request{
		{Symbol: "AAPL", Side: "hold", Qty: 1},
		{Symbol: "AAPL", Side: Buy, Qty: 0},
		{Symbol: "AAPL", Side: Buy, Qty: 1, Type: Limit},
		{Symbol: "AAPL", Side: Buy, Qty: 1, Type: StopLimit, StopPrice: f(10)},
		{Symbol: "AAPL", Side: Buy, Qty: 1, StopLossPct: f(150)},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v should not validate", r)
		}
	}
	r := Request{Symbol: " aapl ", Side: Buy, Qty: 3}
	if err := r.Validate(); err != nil || r.Symbol != "AAPL" || r.Type != Market || r.TIF != Day {
		t.Errorf("defaults = %+v, %v", r, err)
	}
}

func account(cash float64, holdings ...Holding) Account {
	a := Account{Cash: CentsOf(cash), Holdings: map[string]Holding{}, OpenedToday: map[string]bool{}}
	for _, h := range holdings {
		a.Holdings[h.Symbol] = h
	}
	a.DayStartEquity, a.PeakEquity = a.Equity(), a.Equity()
	return a
}

func TestCheck(t *testing.T) {
	s := DefaultSettings()
	price := CentsOf(100)
	buy := func(q int64) Request { return Request{Symbol: "AAPL", Side: Buy, Type: Market, Qty: q} }

	if err := Check(buy(99), price, account(10_000), s, SourceManual); err == nil || !strings.Contains(err.Error(), "buying power") {
		t.Errorf("a buy beyond cash = %v", err)
	}
	if err := Check(buy(90), price, account(10_000), s, SourceManual); err != nil {
		t.Errorf("a manual buy within cash = %v", err)
	}
	if err := Check(buy(90), price, account(10_000), s, SourceAgent); err == nil || !strings.Contains(err.Error(), "position limit") {
		t.Errorf("an agent buy of 90%% of the account = %v", err)
	}
	held := Holding{Symbol: "AAPL", Qty: 10, CostCents: CentsOf(900), Price: price}
	if err := Check(Request{Symbol: "AAPL", Side: Sell, Type: Market, Qty: 11}, price, account(0, held), s, SourceManual); err == nil {
		t.Error("selling more than held should fail")
	}

	pdt := account(1_000, held)
	pdt.OpenedToday["AAPL"], pdt.DayTrades = true, 3
	if err := Check(Request{Symbol: "AAPL", Side: Sell, Type: Market, Qty: 5}, price, pdt, s, SourceManual); err == nil || !strings.Contains(err.Error(), "pattern day trader") {
		t.Errorf("a fourth day trade under $25k = %v", err)
	}

	down := account(9_000)
	down.DayStartEquity = CentsOf(10_000)
	if err := Check(buy(10), price, down, s, SourceAgent); err == nil || !strings.Contains(err.Error(), "daily loss") {
		t.Errorf("a buy after a 10%% daily loss = %v", err)
	}
}

func TestDayExpiry(t *testing.T) {
	ny := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, marketdata.Market) }
	cases := map[time.Time]time.Time{
		ny(1, 10, 0): ny(1, 16, 0), // Thursday, during the session
		ny(1, 7, 0):  ny(1, 16, 0), // before the open: today's session
		ny(1, 17, 0): ny(2, 16, 0), // after the close: Friday's
		ny(2, 18, 0): ny(5, 16, 0), // Friday evening: Monday's
		ny(3, 12, 0): ny(5, 16, 0), // Saturday: Monday's
	}
	for placed, want := range cases {
		if got := DayExpiry(placed); !got.Equal(want) {
			t.Errorf("DayExpiry(%s) = %s, want %s", placed, got, want)
		}
	}
	if !SessionOpen(ny(1, 9, 30)) || SessionOpen(ny(1, 16, 0)) || SessionOpen(ny(3, 12, 0)) {
		t.Error("session hours are 9:30 to 16:00 on weekdays")
	}
}
