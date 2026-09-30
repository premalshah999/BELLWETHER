package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/paper"
)

func placeAt(t *testing.T, db *DB, w int64, r paper.Request, price paper.Cents, s paper.Settings) paper.Order {
	t.Helper()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	o, err := db.PlaceOrder(context.Background(), w, r, paper.SourceManual, nil, nil, time.Now(), func(a paper.Account) (paper.Cents, error) {
		for sym, h := range a.Holdings {
			h.Price = price
			a.Holdings[sym] = h
		}
		return paper.ReserveFor(r, price, s), paper.Check(r, price, a, s, paper.SourceManual)
	})
	if err != nil {
		t.Fatalf("place %+v: %v", r, err)
	}
	return o
}

// The whole money path: fund, buy, sell, withdraw. The books must balance at
// every step, fees must land where they belong, and the closed trade must
// reach the journal.
func TestPaperMoneyPath(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	s := paper.DefaultSettings()
	w, err := db.CreateWallet(ctx, "test", "me", s)
	if err != nil {
		t.Fatal(err)
	}

	dep, created, err := db.CreatePayment(ctx, w.ID, "deposit", paper.CentsOf(10_000), "k1", "simulated", time.Now())
	if err != nil || !created {
		t.Fatalf("deposit: %v %v", created, err)
	}
	if _, err := db.SettlePayment(ctx, dep.ID, paper.PaymentSucceeded, "sim", ""); err != nil {
		t.Fatal(err)
	}
	// Settling twice, and retrying the request, must not move money twice.
	if _, err := db.SettlePayment(ctx, dep.ID, paper.PaymentSucceeded, "sim", ""); err != nil {
		t.Fatal(err)
	}
	if again, created, _ := db.CreatePayment(ctx, w.ID, "deposit", paper.CentsOf(10_000), "k1", "simulated", time.Now()); created || again.ID != dep.ID {
		t.Errorf("a retried deposit made a new payment: created=%v id=%d", created, again.ID)
	}
	if bal, _ := db.Balances(ctx, w.ID); bal["cash"] != paper.CentsOf(10_000) {
		t.Fatalf("cash after deposit = %s", bal["cash"])
	}

	buy := placeAt(t, db, w.ID, paper.Request{Symbol: "AAPL", Side: paper.Buy, Qty: 10}, paper.CentsOf(100), s)
	if buy.ReservedCents <= paper.CentsOf(1000) {
		t.Errorf("reserved = %s, want the cost plus a margin", buy.ReservedCents)
	}
	if _, err := db.ApplyFill(ctx, buy.ID, paper.CentsOf(100.05), paper.CentsOf(100), 0, time.Now(), "test"); err != nil {
		t.Fatal(err)
	}
	sell := placeAt(t, db, w.ID, paper.Request{Symbol: "AAPL", Side: paper.Sell, Qty: 10}, paper.CentsOf(110), s)
	fee := paper.Fees(paper.Sell, 10, paper.CentsOf(110), s)
	fill, err := db.ApplyFill(ctx, sell.ID, paper.CentsOf(110), paper.CentsOf(110), fee, time.Now(), "test")
	if err != nil || fill == nil {
		t.Fatalf("sell fill: %v %v", fill, err)
	}
	if *fill.RealizedCents != paper.CentsOf(99.50) {
		t.Errorf("realized = %s, want $99.50 (1,100.00 - 1,000.50)", *fill.RealizedCents)
	}

	bal, _ := db.Balances(ctx, w.ID)
	var sum paper.Cents
	for _, c := range bal {
		sum += c
	}
	if sum != 0 {
		t.Errorf("the books do not balance: %v", bal)
	}
	wantCash := paper.CentsOf(10_000) - paper.CentsOf(1000.50) + paper.CentsOf(1100) - fee
	if bal["cash"] != wantCash || bal["securities"] != 0 || bal["fees"] != fee {
		t.Errorf("balances = %v, want cash %s, securities 0, fees %s", bal, wantCash, fee)
	}
	if n := countRows(t, db, `SELECT count(*) FROM trades WHERE account = 'paper: test'`); n != 1 {
		t.Errorf("journal rows = %d, want the closed trade", n)
	}

	// Withdrawals cannot take more than the free cash.
	if _, _, err := db.CreatePayment(ctx, w.ID, "withdrawal", wantCash+1, "w1", "simulated", time.Now()); err == nil || !strings.Contains(err.Error(), "can be withdrawn") {
		t.Errorf("an over-withdrawal = %v", err)
	}
	wd, _, err := db.CreatePayment(ctx, w.ID, "withdrawal", paper.CentsOf(500), "w2", "simulated", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SettlePayment(ctx, wd.ID, paper.PaymentSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	if bal, _ := db.Balances(ctx, w.ID); bal["cash"] != wantCash-paper.CentsOf(500) {
		t.Errorf("cash after withdrawal = %s", bal["cash"])
	}
}

// Two buys that each fit the cash alone must not both be accepted.
func TestPaperOrdersCannotSpendTheSameCashTwice(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	s := paper.DefaultSettings()
	w, _ := db.CreateWallet(ctx, "t", "", s)
	p, _, _ := db.CreatePayment(ctx, w.ID, "deposit", paper.CentsOf(1000), "k", "simulated", time.Now())
	_, _ = db.SettlePayment(ctx, p.ID, paper.PaymentSucceeded, "", "")

	placeAt(t, db, w.ID, paper.Request{Symbol: "AAPL", Side: paper.Buy, Qty: 6, ClientOrderID: "a"}, paper.CentsOf(100), s)
	r := paper.Request{Symbol: "MSFT", Side: paper.Buy, Qty: 6, ClientOrderID: "b"}
	_ = r.Validate()
	_, err := db.PlaceOrder(ctx, w.ID, r, paper.SourceManual, nil, nil, time.Now(), func(a paper.Account) (paper.Cents, error) {
		return paper.ReserveFor(r, paper.CentsOf(100), s), paper.Check(r, paper.CentsOf(100), a, s, paper.SourceManual)
	})
	var refused ErrPaper
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "buying power") {
		t.Errorf("second buy = %v, want refused for buying power", err)
	}
	// The same client order id returns the original order.
	again := placeAt(t, db, w.ID, paper.Request{Symbol: "AAPL", Side: paper.Buy, Qty: 6, ClientOrderID: "a"}, paper.CentsOf(100), s)
	if n := countRows(t, db, `SELECT count(*) FROM paper_orders`); n != 1 || again.Qty != 6 {
		t.Errorf("orders = %d after a retried order", n)
	}
}

func countRows(t *testing.T, db *DB, q string) int {
	t.Helper()
	var n int
	if err := db.db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
