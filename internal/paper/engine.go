package paper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Prices is where the engine gets the market it fills against.
type Prices interface {
	// Quote is the latest traded price and when it was observed.
	Quote(ctx context.Context, symbol string) (Cents, time.Time, error)
	// Bars are recent intraday bars, oldest first.
	Bars(ctx context.Context, symbol string) ([]Bar, error)
	// Benchmark is the S&P 500 level, for marking equity against it.
	Benchmark(ctx context.Context) (float64, error)
}

// Store is the persistence the engine needs; postgres.DB implements it.
type Store interface {
	Wallet(ctx context.Context, id int64) (Wallet, error)
	Wallets(ctx context.Context) ([]Wallet, error)
	Account(ctx context.Context, walletID int64, now time.Time) (Account, error)
	Balances(ctx context.Context, walletID int64) (map[string]Cents, error)
	CreatePayment(ctx context.Context, walletID int64, direction string, amount Cents, key, provider string, now time.Time) (Payment, bool, error)
	SettlePayment(ctx context.Context, paymentID int64, status PaymentStatus, providerRef, reason string) (Payment, error)
	PlaceOrder(ctx context.Context, walletID int64, r Request, source Source, agentID, parent *int64, now time.Time,
		check func(Account) (Cents, error)) (Order, error)
	OpenOrders(ctx context.Context) ([]Order, error)
	EndOrder(ctx context.Context, walletID, orderID int64, status Status, reason string) (Order, error)
	ApplyFill(ctx context.Context, orderID int64, price, quote, fee Cents, at time.Time, walletName string) (*Fill, error)
	MarkEquity(ctx context.Context, walletID int64, p EquityPoint) error
}

// Engine places, fills and marks paper orders.
type Engine struct {
	Store    Store
	Prices   Prices
	Payments PaymentProvider
	Log      *slog.Logger
	Now      func() time.Time
	// tick serialises fills, so a manual fill and the loop never race.
	tick sync.Mutex
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Session hours: 9:30 to 16:00 New York, weekdays. Market holidays are not
// modelled; on one, a market order waits for prices that do not come and
// fills on the next open.
const (
	openMinute  = 9*60 + 30
	closeMinute = 16 * 60
)

// SessionOpen reports whether the regular session is trading at t.
func SessionOpen(t time.Time) bool {
	et := t.In(marketdata.Market)
	if wd := et.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	m := et.Hour()*60 + et.Minute()
	return m >= openMinute && m < closeMinute
}

// MinutesToClose is how long the session has left; zero when closed.
func MinutesToClose(t time.Time) int {
	if !SessionOpen(t) {
		return 0
	}
	et := t.In(marketdata.Market)
	return closeMinute - (et.Hour()*60 + et.Minute())
}

// DayExpiry is when a day order placed at t lapses: the close of the session
// it was placed in, or of the next one when placed after a close or on a
// weekend.
func DayExpiry(t time.Time) time.Time {
	et := t.In(marketdata.Market)
	close := time.Date(et.Year(), et.Month(), et.Day(), 16, 0, 0, 0, marketdata.Market)
	if !et.Before(close) {
		close = close.AddDate(0, 0, 1)
	}
	for close.Weekday() == time.Saturday || close.Weekday() == time.Sunday {
		close = close.AddDate(0, 0, 1)
	}
	return close
}

// ErrRefused wraps a refusal the caller can show verbatim.
var ErrRefused = errors.New("order refused")

// Deposit adds simulated money. The key makes a retried request safe.
func (e *Engine) Deposit(ctx context.Context, walletID int64, amount Cents, key string) (Payment, error) {
	return e.pay(ctx, walletID, "deposit", amount, key)
}

// Withdraw takes money out, if it is not invested or held for an order.
func (e *Engine) Withdraw(ctx context.Context, walletID int64, amount Cents, key string) (Payment, error) {
	return e.pay(ctx, walletID, "withdrawal", amount, key)
}

// maxPayment bounds a single simulated payment: large enough for any real
// account, small enough that a typo is not a trillion dollars.
const maxPayment = Cents(100_000_000_00)

func (e *Engine) pay(ctx context.Context, walletID int64, direction string, amount Cents, key string) (Payment, error) {
	if amount <= 0 || amount > maxPayment {
		return Payment{}, fmt.Errorf("%w: an amount between $0.01 and $100,000,000 is required", ErrRefused)
	}
	if key == "" {
		return Payment{}, fmt.Errorf("%w: an idempotency key is required", ErrRefused)
	}
	p, created, err := e.Store.CreatePayment(ctx, walletID, direction, amount, key, e.Payments.Name(), e.now())
	if err != nil || !created || p.Status.Final() {
		return p, err
	}
	status, ref, err := e.Payments.Begin(ctx, p)
	if err != nil {
		return e.Store.SettlePayment(ctx, p.ID, PaymentFailed, "", err.Error())
	}
	if status == PaymentProcessing {
		return p, nil // the provider reports the outcome later
	}
	return e.Store.SettlePayment(ctx, p.ID, status, ref, "")
}

// Place checks an order against the account and the wallet's limits, records
// it, and fills a market order at once while the session is open.
func (e *Engine) Place(ctx context.Context, walletID int64, r Request, source Source, agentID *int64) (Order, *Fill, error) {
	if err := r.Validate(); err != nil {
		return Order{}, nil, fmt.Errorf("%w: %s", ErrRefused, err.Error())
	}
	sym, err := marketdata.ParseSymbol(r.Symbol)
	if err != nil || sym.Exchange != marketdata.ExchangeUS {
		return Order{}, nil, fmt.Errorf("%w: %s is not a US listing this system can price", ErrRefused, r.Symbol)
	}
	r.Symbol = sym.String()
	w, err := e.Store.Wallet(ctx, walletID)
	if err != nil {
		return Order{}, nil, err
	}
	price, _, err := e.Prices.Quote(ctx, r.Symbol)
	if err != nil || price <= 0 {
		return Order{}, nil, fmt.Errorf("%w: no current price for %s", ErrRefused, r.Symbol)
	}
	marks, err := e.marks(ctx, walletID)
	if err != nil {
		return Order{}, nil, err
	}
	marks[r.Symbol] = price
	s := w.Settings
	o, err := e.Store.PlaceOrder(ctx, walletID, r, source, agentID, nil, e.now(), func(a Account) (Cents, error) {
		markAccount(&a, marks)
		return ReserveFor(r, price, s), Check(r, price, a, s, source)
	})
	if err != nil {
		return o, nil, err
	}
	if o.Status != Accepted || o.Type != Market || !SessionOpen(e.now()) {
		return o, nil, nil
	}
	e.tick.Lock()
	defer e.tick.Unlock()
	fill, err := e.fill(ctx, w, o, Slipped(o.Side, price, s.SlippageBps), price)
	return o, fill, err
}

// Cancel ends an open order.
func (e *Engine) Cancel(ctx context.Context, walletID, orderID int64) (Order, error) {
	return e.Store.EndOrder(ctx, walletID, orderID, Canceled, "canceled")
}

// marks prices every holding of a wallet.
func (e *Engine) marks(ctx context.Context, walletID int64) (map[string]Cents, error) {
	a, err := e.Store.Account(ctx, walletID, e.now())
	if err != nil {
		return nil, err
	}
	out := make(map[string]Cents, len(a.Holdings))
	for sym, h := range a.Holdings {
		if p, _, err := e.Prices.Quote(ctx, sym); err == nil && p > 0 {
			out[sym] = p
		} else {
			out[sym] = Cents(int64(h.CostCents) / max(h.Qty, 1)) // no price: carried at cost
		}
	}
	return out, nil
}

func markAccount(a *Account, marks map[string]Cents) {
	for sym, h := range a.Holdings {
		h.Price = marks[sym]
		a.Holdings[sym] = h
	}
}

// Snapshot is a wallet marked to market.
func (e *Engine) Snapshot(ctx context.Context, walletID int64) (Wallet, Account, error) {
	w, err := e.Store.Wallet(ctx, walletID)
	if err != nil {
		return w, Account{}, err
	}
	a, err := e.Store.Account(ctx, walletID, e.now())
	if err != nil {
		return w, a, err
	}
	marks, err := e.marks(ctx, walletID)
	if err != nil {
		return w, a, err
	}
	markAccount(&a, marks)
	return w, a, nil
}

// fill applies one execution, then places the exits a buy asked for.
func (e *Engine) fill(ctx context.Context, w Wallet, o Order, price, quote Cents) (*Fill, error) {
	fee := Fees(o.Side, o.Qty-o.FilledQty, price, w.Settings)
	f, err := e.Store.ApplyFill(ctx, o.ID, price, quote, fee, e.now(), w.Name)
	if err != nil || f == nil {
		return f, err
	}
	e.Log.Info("paper fill", "wallet", w.ID, "symbol", f.Symbol, "side", f.Side, "qty", f.Qty, "price", f.PriceCents.String())
	if o.Side == Buy && (o.StopLossPct != nil || o.TakeProfitPct != nil) {
		e.placeExits(ctx, w, o, f)
	}
	return f, nil
}

// placeExits attaches a protective stop and a profit target to a filled buy.
// They are one-cancels-other: when one fills, the other is canceled.
func (e *Engine) placeExits(ctx context.Context, w Wallet, parent Order, f *Fill) {
	exit := func(kind OrderType, pct float64, sign float64) {
		level := f.PriceCents.Dollars() * (1 + sign*pct/100)
		r := Request{Symbol: f.Symbol, Side: Sell, Type: kind, Qty: f.Qty, TIF: GTC,
			ClientOrderID: fmt.Sprintf("exit-%d-%s", parent.ID, kind),
			Reason:        fmt.Sprintf("%s for order %d at %.2f%%", map[OrderType]string{Stop: "stop loss", Limit: "take profit"}[kind], parent.ID, pct)}
		if kind == Stop {
			r.StopPrice = &level
		} else {
			r.LimitPrice = &level
		}
		if err := r.Validate(); err != nil {
			return
		}
		pid := parent.ID
		if _, err := e.Store.PlaceOrder(ctx, w.ID, r, SourceProtective, parent.AgentID, &pid, e.now(),
			func(Account) (Cents, error) { return 0, nil }); err != nil {
			e.Log.Warn("paper: exit not placed", "order", parent.ID, "err", err)
		}
	}
	if parent.StopLossPct != nil {
		exit(Stop, *parent.StopLossPct, -1)
	}
	if parent.TakeProfitPct != nil {
		exit(Limit, *parent.TakeProfitPct, 1)
	}
}

// Tick works every open order: fills what the market has reached, expires
// day orders after the close and good-till-canceled ones after ninety days.
func (e *Engine) Tick(ctx context.Context) (int, error) {
	e.tick.Lock()
	defer e.tick.Unlock()
	orders, err := e.Store.OpenOrders(ctx)
	if err != nil {
		return 0, err
	}
	now := e.now()
	wallets := map[int64]Wallet{}
	bySymbol := map[string][]Order{}
	filled := 0
	for _, o := range orders {
		switch {
		case o.TIF == Day && now.After(DayExpiry(o.CreatedAt)):
			_, _ = e.Store.EndOrder(ctx, o.WalletID, o.ID, Expired, "the session ended")
			continue
		case o.TIF == GTC && now.Sub(o.CreatedAt) > 90*24*time.Hour:
			_, _ = e.Store.EndOrder(ctx, o.WalletID, o.ID, Expired, "good-till-canceled orders last ninety days")
			continue
		}
		bySymbol[o.Symbol] = append(bySymbol[o.Symbol], o)
	}
	if !SessionOpen(now) {
		return 0, nil
	}
	symbols := make([]string, 0, len(bySymbol))
	for s := range bySymbol {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	for _, sym := range symbols {
		bars, err := e.Prices.Bars(ctx, sym)
		if err != nil || len(bars) == 0 {
			continue
		}
		quote, _, qerr := e.Prices.Quote(ctx, sym)
		for _, o := range bySymbol[sym] {
			w, ok := wallets[o.WalletID]
			if !ok {
				if w, err = e.Store.Wallet(ctx, o.WalletID); err != nil {
					continue
				}
				wallets[o.WalletID] = w
			}
			price, ref, hit := e.match(o, bars, quote, qerr == nil, now, w.Settings)
			if !hit {
				continue
			}
			if f, err := e.fill(ctx, w, o, price, ref); err != nil {
				e.Log.Warn("paper: fill failed", "order", o.ID, "err", err)
			} else if f != nil {
				filled++
			}
		}
	}
	return filled, nil
}

// match finds the first bar since an order was placed that fills it. A
// market order placed while the market was closed fills at the next open.
func (e *Engine) match(o Order, bars []Bar, quote Cents, haveQuote bool, now time.Time, s Settings) (price, ref Cents, ok bool) {
	for _, b := range bars {
		if b.At.Before(o.CreatedAt.Truncate(time.Minute)) || !SessionOpen(b.At) {
			continue
		}
		if o.Type == Market {
			if SessionOpen(o.CreatedAt) && haveQuote {
				return Slipped(o.Side, quote, s.SlippageBps), quote, true
			}
			return Slipped(o.Side, b.Open, s.SlippageBps), b.Open, true
		}
		if p, hit := Match(o, b, s); hit {
			return p, b.Close, true
		}
	}
	if o.Type == Market && haveQuote && SessionOpen(now) {
		return Slipped(o.Side, quote, s.SlippageBps), quote, true
	}
	return 0, 0, false
}

// MarkAll records every active wallet's equity beside the S&P 500.
func (e *Engine) MarkAll(ctx context.Context) error {
	wallets, err := e.Store.Wallets(ctx)
	if err != nil {
		return err
	}
	var bench *float64
	if b, err := e.Prices.Benchmark(ctx); err == nil && b > 0 {
		bench = &b
	}
	now := e.now().Truncate(time.Minute)
	for _, w := range wallets {
		if w.Status != "active" {
			continue
		}
		_, a, err := e.Snapshot(ctx, w.ID)
		if err != nil {
			continue
		}
		mv := a.Equity() - a.Cash
		if err := e.Store.MarkEquity(ctx, w.ID, EquityPoint{At: now, CashCents: a.Cash, MarketCents: mv, EquityCents: a.Equity(), Benchmark: bench}); err != nil {
			e.Log.Warn("paper: equity mark failed", "wallet", w.ID, "err", err)
		}
	}
	return nil
}
