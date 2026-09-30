package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/paper"
)

// Paper trading. Every function that moves value runs in one transaction
// holding the wallet row lock, so concurrent orders, fills and payments on
// one wallet are applied one at a time against fresh balances.

// ErrPaper is a paper-trading refusal a caller can show as is: not enough
// cash, a wallet closed, an order already final.
type ErrPaper struct{ Msg string }

func (e ErrPaper) Error() string { return e.Msg }

func refusal(format string, a ...any) error { return ErrPaper{fmt.Sprintf(format, a...)} }

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

const walletCols = `id, name, owner, currency, status, settings, peak_equity_cents, created_at`

func scanWallet(row interface{ Scan(...any) error }) (paper.Wallet, error) {
	var w paper.Wallet
	var raw []byte
	if err := row.Scan(&w.ID, &w.Name, &w.Owner, &w.Currency, &w.Status, &raw, &w.PeakEquity, &w.CreatedAt); err != nil {
		return w, err
	}
	_ = json.Unmarshal(raw, &w.Settings)
	w.Settings = w.Settings.Normalize()
	return w, nil
}

// CreateWallet opens a wallet with no money in it.
func (d *DB) CreateWallet(ctx context.Context, name, owner string, s paper.Settings) (paper.Wallet, error) {
	raw, _ := json.Marshal(s.Normalize())
	return scanWallet(d.db.QueryRowContext(ctx, `
INSERT INTO paper_wallets (name, owner, settings) VALUES ($1, $2, $3)
RETURNING `+walletCols, name, owner, raw))
}

// Wallets lists wallets, open ones first.
func (d *DB) Wallets(ctx context.Context) ([]paper.Wallet, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+walletCols+` FROM paper_wallets ORDER BY status, id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: wallets: %w", err)
	}
	defer rows.Close()
	var out []paper.Wallet
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Wallet reads one wallet.
func (d *DB) Wallet(ctx context.Context, id int64) (paper.Wallet, error) {
	w, err := scanWallet(d.db.QueryRowContext(ctx, `SELECT `+walletCols+` FROM paper_wallets WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// UpdateWallet renames a wallet and replaces its settings.
func (d *DB) UpdateWallet(ctx context.Context, id int64, name string, s paper.Settings) (paper.Wallet, error) {
	raw, _ := json.Marshal(s.Normalize())
	w, err := scanWallet(d.db.QueryRowContext(ctx, `
UPDATE paper_wallets SET name = $2, settings = $3, updated_at = now() WHERE id = $1
RETURNING `+walletCols, id, name, raw))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// CloseWallet stops a wallet taking orders or money; its history stays.
func (d *DB) CloseWallet(ctx context.Context, id int64) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockWallet(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE paper_orders SET status = 'canceled', reserved_cents = 0, reject_reason = 'wallet closed', updated_at = now()
WHERE wallet_id = $1 AND status = 'accepted'`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE paper_agents SET enabled = false, halted_reason = 'wallet closed' WHERE wallet_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE paper_wallets SET status = 'closed', updated_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func lockWallet(ctx context.Context, tx *sql.Tx, id int64) (paper.Wallet, error) {
	w, err := scanWallet(tx.QueryRowContext(ctx, `SELECT `+walletCols+` FROM paper_wallets WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	if w.Status != "active" {
		return w, refusal("this wallet is closed")
	}
	return w, nil
}

// postLedger writes one ledger transaction. Postings must sum to zero: the
// books balance by construction or the transaction is refused.
func postLedger(ctx context.Context, q querier, walletID int64, kind, refType string, refID *int64, memo string, postings []paper.Posting) (int64, error) {
	var sum paper.Cents
	for _, p := range postings {
		sum += p.AmountCents
	}
	if sum != 0 {
		return 0, fmt.Errorf("postgres: unbalanced %s ledger transaction (off by %s)", kind, sum)
	}
	var id int64
	if err := q.QueryRowContext(ctx, `
INSERT INTO paper_ledger_tx (wallet_id, kind, ref_type, ref_id, memo) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		walletID, kind, refType, refID, memo).Scan(&id); err != nil {
		return 0, fmt.Errorf("postgres: ledger tx: %w", err)
	}
	for _, p := range postings {
		if p.AmountCents == 0 {
			continue
		}
		if _, err := q.ExecContext(ctx, `
INSERT INTO paper_postings (tx_id, wallet_id, account, amount_cents) VALUES ($1, $2, $3, $4)`,
			id, walletID, p.Account, int64(p.AmountCents)); err != nil {
			return 0, fmt.Errorf("postgres: posting: %w", err)
		}
	}
	return id, nil
}

// Balances sums every account of a wallet's ledger.
func (d *DB) Balances(ctx context.Context, walletID int64) (map[string]paper.Cents, error) {
	return balances(ctx, d.db, walletID)
}

func balances(ctx context.Context, q querier, walletID int64) (map[string]paper.Cents, error) {
	rows, err := q.QueryContext(ctx, `SELECT account, COALESCE(sum(amount_cents), 0) FROM paper_postings WHERE wallet_id = $1 GROUP BY account`, walletID)
	if err != nil {
		return nil, fmt.Errorf("postgres: balances: %w", err)
	}
	defer rows.Close()
	out := map[string]paper.Cents{}
	for rows.Next() {
		var a string
		var c int64
		if err := rows.Scan(&a, &c); err != nil {
			return nil, err
		}
		out[a] = paper.Cents(c)
	}
	return out, rows.Err()
}

// Account reads what a pre-trade check needs, without prices: the engine
// marks holdings to market before checking.
func (d *DB) Account(ctx context.Context, walletID int64, now time.Time) (paper.Account, error) {
	return account(ctx, d.db, walletID, now)
}

func account(ctx context.Context, q querier, walletID int64, now time.Time) (paper.Account, error) {
	a := paper.Account{Holdings: map[string]paper.Holding{}, OpenedToday: map[string]bool{}}
	bal, err := balances(ctx, q, walletID)
	if err != nil {
		return a, err
	}
	a.Cash = bal["cash"]
	var pendingOut int64
	if err := q.QueryRowContext(ctx, `
SELECT COALESCE((SELECT sum(reserved_cents) FROM paper_orders WHERE wallet_id = $1 AND status = 'accepted'), 0),
       COALESCE((SELECT sum(amount_cents) FROM paper_payments WHERE wallet_id = $1 AND direction = 'withdrawal'
                 AND status IN ('created', 'processing')), 0)`, walletID).Scan(&a.Reserved, &pendingOut); err != nil {
		return a, fmt.Errorf("postgres: reserved: %w", err)
	}
	a.Reserved += paper.Cents(pendingOut)

	rows, err := q.QueryContext(ctx, `
SELECT p.symbol, p.qty, p.cost_cents, p.opened_at,
       COALESCE((SELECT sum(o.qty - o.filled_qty) FROM paper_orders o
                 WHERE o.wallet_id = p.wallet_id AND o.symbol = p.symbol AND o.side = 'sell' AND o.status = 'accepted'
                 AND o.parent_order_id IS NULL), 0)
FROM paper_positions p WHERE p.wallet_id = $1 AND p.qty > 0`, walletID)
	if err != nil {
		return a, fmt.Errorf("postgres: holdings: %w", err)
	}
	day := sessionDay(now)
	for rows.Next() {
		var h paper.Holding
		if err := rows.Scan(&h.Symbol, &h.Qty, &h.CostCents, &h.OpenedAt, &h.ReservedQty); err != nil {
			rows.Close()
			return a, err
		}
		a.Holdings[h.Symbol] = h
		if sessionDay(h.OpenedAt) == day {
			a.OpenedToday[h.Symbol] = true
		}
	}
	rows.Close()

	// Day trades: a symbol bought and sold in the same session, counted once
	// per symbol per session, over the last five business days.
	since := now.AddDate(0, 0, -7)
	if err := q.QueryRowContext(ctx, `
SELECT count(*) FROM (
    SELECT symbol, (filled_at AT TIME ZONE 'America/New_York')::date AS d
    FROM paper_fills WHERE wallet_id = $1 AND filled_at >= $2
    GROUP BY 1, 2 HAVING bool_or(side = 'buy') AND bool_or(side = 'sell')
      AND min(filled_at) FILTER (WHERE side = 'buy') < max(filled_at) FILTER (WHERE side = 'sell')
) t WHERE d >= ($3::date - 6)`, walletID, since, day).Scan(&a.DayTrades); err != nil {
		return a, fmt.Errorf("postgres: day trades: %w", err)
	}
	if err := q.QueryRowContext(ctx, `
SELECT count(DISTINCT order_id) FROM paper_fills
WHERE wallet_id = $1 AND (filled_at AT TIME ZONE 'America/New_York')::date = $2::date`, walletID, day).Scan(&a.TradesToday); err != nil {
		return a, err
	}
	// Equity at the last mark before today's session, and the peak.
	var dayStart sql.NullInt64
	_ = q.QueryRowContext(ctx, `
SELECT equity_cents FROM paper_equity WHERE wallet_id = $1 AND (at AT TIME ZONE 'America/New_York')::date < $2::date
ORDER BY at DESC LIMIT 1`, walletID, day).Scan(&dayStart)
	var peak int64
	_ = q.QueryRowContext(ctx, `SELECT peak_equity_cents FROM paper_wallets WHERE id = $1`, walletID).Scan(&peak)
	a.PeakEquity = paper.Cents(peak)
	if dayStart.Valid {
		a.DayStartEquity = paper.Cents(dayStart.Int64)
	}
	return a, nil
}

func sessionDay(t time.Time) string { return t.In(marketdata.Market).Format("2006-01-02") }

// ---- payments -------------------------------------------------------------

const paymentCols = `id, wallet_id, direction, amount_cents, currency, provider, provider_ref, status, idempotency_key, failure_reason, created_at, updated_at`

func scanPayment(row interface{ Scan(...any) error }) (paper.Payment, error) {
	var p paper.Payment
	err := row.Scan(&p.ID, &p.WalletID, &p.Direction, &p.AmountCents, &p.Currency, &p.Provider, &p.ProviderRef,
		&p.Status, &p.IdempotencyKey, &p.FailureReason, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreatePayment records a deposit or withdrawal in the created state. The
// idempotency key makes a retried request return the original payment rather
// than moving the money twice. A withdrawal must fit in the cash not already
// promised to open orders or other withdrawals.
func (d *DB) CreatePayment(ctx context.Context, walletID int64, direction string, amount paper.Cents, key, provider string, now time.Time) (paper.Payment, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return paper.Payment{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockWallet(ctx, tx, walletID); err != nil {
		return paper.Payment{}, false, err
	}
	if p, err := scanPayment(tx.QueryRowContext(ctx, `SELECT `+paymentCols+` FROM paper_payments WHERE wallet_id = $1 AND idempotency_key = $2`, walletID, key)); err == nil {
		if p.Direction != direction || p.AmountCents != amount {
			return p, false, refusal("that idempotency key was already used for a different payment")
		}
		return p, false, nil
	}
	if direction == "withdrawal" {
		a, err := account(ctx, tx, walletID, now)
		if err != nil {
			return paper.Payment{}, false, err
		}
		if amount > a.BuyingPower() {
			return paper.Payment{}, false, refusal("only %s can be withdrawn: the rest is invested or held for open orders", max(a.BuyingPower(), 0))
		}
	}
	p, err := scanPayment(tx.QueryRowContext(ctx, `
INSERT INTO paper_payments (wallet_id, direction, amount_cents, provider, status, idempotency_key)
VALUES ($1, $2, $3, $4, 'created', $5) RETURNING `+paymentCols, walletID, direction, int64(amount), provider, key))
	if err != nil {
		return p, false, fmt.Errorf("postgres: create payment: %w", err)
	}
	return p, true, tx.Commit()
}

// SettlePayment moves a payment to its provider's reported status. Success
// posts the ledger exactly once; a payment already final is returned as is,
// so a provider's repeated notification changes nothing.
func (d *DB) SettlePayment(ctx context.Context, paymentID int64, status paper.PaymentStatus, providerRef, reason string) (paper.Payment, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return paper.Payment{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var walletID int64
	if err := tx.QueryRowContext(ctx, `SELECT wallet_id FROM paper_payments WHERE id = $1`, paymentID).Scan(&walletID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return paper.Payment{}, ErrNotFound
		}
		return paper.Payment{}, err
	}
	if _, err := lockWallet(ctx, tx, walletID); err != nil {
		return paper.Payment{}, err
	}
	p, err := scanPayment(tx.QueryRowContext(ctx, `SELECT `+paymentCols+` FROM paper_payments WHERE id = $1 FOR UPDATE`, paymentID))
	if err != nil {
		return p, err
	}
	if p.Status.Final() {
		return p, nil
	}
	var ledgerID *int64
	if status == paper.PaymentSucceeded {
		postings := []paper.Posting{{Account: "cash", AmountCents: p.AmountCents}, {Account: "external", AmountCents: -p.AmountCents}}
		if p.Direction == "withdrawal" {
			bal, err := balances(ctx, tx, walletID)
			if err != nil {
				return p, err
			}
			if bal["cash"] < p.AmountCents {
				status, reason = paper.PaymentFailed, "insufficient cash when the withdrawal settled"
				postings = nil
			} else {
				postings = []paper.Posting{{Account: "cash", AmountCents: -p.AmountCents}, {Account: "external", AmountCents: p.AmountCents}}
			}
		}
		if postings != nil {
			id, err := postLedger(ctx, tx, walletID, p.Direction, "payment", &p.ID, p.Provider+" "+p.Direction, postings)
			if err != nil {
				return p, err
			}
			ledgerID = &id
		}
	}
	p, err = scanPayment(tx.QueryRowContext(ctx, `
UPDATE paper_payments SET status = $2, provider_ref = COALESCE(NULLIF($3, ''), provider_ref), failure_reason = $4,
       ledger_tx_id = $5, updated_at = now()
WHERE id = $1 RETURNING `+paymentCols, paymentID, string(status), providerRef, reason, ledgerID))
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// Payments lists a wallet's deposits and withdrawals, newest first.
func (d *DB) Payments(ctx context.Context, walletID int64, limit int) ([]paper.Payment, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+paymentCols+` FROM paper_payments WHERE wallet_id = $1 ORDER BY id DESC LIMIT $2`, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- orders ---------------------------------------------------------------

const orderCols = `id, wallet_id, client_order_id, symbol, side, type, qty, limit_cents, stop_cents, tif, status,
    filled_qty, avg_fill_cents, reserved_cents, source, agent_id, reason, reject_reason, stop_loss_pct, take_profit_pct,
    parent_order_id, created_at, updated_at, filled_at`

func scanOrder(row interface{ Scan(...any) error }) (paper.Order, error) {
	var o paper.Order
	var limit, stop, avg sql.NullInt64
	err := row.Scan(&o.ID, &o.WalletID, &o.ClientOrderID, &o.Symbol, &o.Side, &o.Type, &o.Qty, &limit, &stop, &o.TIF,
		&o.Status, &o.FilledQty, &avg, &o.ReservedCents, &o.Source, &o.AgentID, &o.Reason, &o.RejectReason,
		&o.StopLossPct, &o.TakeProfitPct, &o.ParentOrderID, &o.CreatedAt, &o.UpdatedAt, &o.FilledAt)
	cents := func(n sql.NullInt64) *paper.Cents {
		if !n.Valid {
			return nil
		}
		c := paper.Cents(n.Int64)
		return &c
	}
	o.LimitCents, o.StopCents, o.AvgFillCents = cents(limit), cents(stop), cents(avg)
	return o, err
}

func centsArg(p *float64) any {
	if p == nil {
		return nil
	}
	return int64(paper.CentsOf(*p))
}

// PlaceOrder checks and records an order under the wallet lock. check sees
// the account as it stands inside the transaction, so two orders placed at
// once cannot both spend the same cash. A client order id already used on
// the wallet returns the original order.
func (d *DB) PlaceOrder(ctx context.Context, walletID int64, r paper.Request, source paper.Source, agentID *int64, parent *int64,
	now time.Time, check func(paper.Account) (reserve paper.Cents, err error)) (paper.Order, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return paper.Order{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockWallet(ctx, tx, walletID); err != nil {
		return paper.Order{}, err
	}
	if r.ClientOrderID != "" {
		if o, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderCols+` FROM paper_orders WHERE wallet_id = $1 AND client_order_id = $2`, walletID, r.ClientOrderID)); err == nil {
			return o, nil
		}
	} else {
		r.ClientOrderID = fmt.Sprintf("%s-%d", source, now.UnixNano())
	}
	a, err := account(ctx, tx, walletID, now)
	if err != nil {
		return paper.Order{}, err
	}
	reserve, err := check(a)
	if err != nil {
		return paper.Order{}, refusal("%s", err.Error())
	}
	o, err := scanOrder(tx.QueryRowContext(ctx, `
INSERT INTO paper_orders (wallet_id, client_order_id, symbol, side, type, qty, limit_cents, stop_cents, tif, status,
    reserved_cents, source, agent_id, reason, stop_loss_pct, take_profit_pct, parent_order_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'accepted',$10,$11,$12,$13,$14,$15,$16)
RETURNING `+orderCols, walletID, r.ClientOrderID, r.Symbol, string(r.Side), string(r.Type), r.Qty,
		centsArg(r.LimitPrice), centsArg(r.StopPrice), string(r.TIF), int64(reserve), string(source), agentID, r.Reason,
		r.StopLossPct, r.TakeProfitPct, parent))
	if err != nil {
		return o, fmt.Errorf("postgres: place order: %w", err)
	}
	return o, tx.Commit()
}

// OpenOrders lists every accepted order across active wallets, oldest first.
func (d *DB) OpenOrders(ctx context.Context) ([]paper.Order, error) {
	return d.orders(ctx, `SELECT `+orderCols+` FROM paper_orders WHERE status = 'accepted' ORDER BY id`)
}

// Orders lists a wallet's orders, newest first; status "open" keeps accepted ones.
func (d *DB) Orders(ctx context.Context, walletID int64, status string, limit int) ([]paper.Order, error) {
	if status == "open" {
		return d.orders(ctx, `SELECT `+orderCols+` FROM paper_orders WHERE wallet_id = $1 AND status = 'accepted' ORDER BY id DESC LIMIT $2`, walletID, limit)
	}
	return d.orders(ctx, `SELECT `+orderCols+` FROM paper_orders WHERE wallet_id = $1 ORDER BY id DESC LIMIT $2`, walletID, limit)
}

func (d *DB) orders(ctx context.Context, q string, args ...any) ([]paper.Order, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: orders: %w", err)
	}
	defer rows.Close()
	var out []paper.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// EndOrder cancels, expires or rejects an open order and releases what it
// held. An order already final is left alone.
func (d *DB) EndOrder(ctx context.Context, walletID, orderID int64, status paper.Status, reason string) (paper.Order, error) {
	o, err := scanOrder(d.db.QueryRowContext(ctx, `
UPDATE paper_orders SET status = $3, reserved_cents = 0, reject_reason = $4, updated_at = now()
WHERE id = $1 AND wallet_id = $2 AND status = 'accepted' RETURNING `+orderCols, orderID, walletID, string(status), reason))
	if errors.Is(err, sql.ErrNoRows) {
		cur, err2 := scanOrder(d.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM paper_orders WHERE id = $1 AND wallet_id = $2`, orderID, walletID))
		if errors.Is(err2, sql.ErrNoRows) {
			return cur, ErrNotFound
		}
		return cur, refusal("that order is already %s", cur.Status)
	}
	return o, err
}

// ApplyFill executes an open order at a price, in one transaction: the
// ledger, the position, the order and the fill are written together or not
// at all. A sell that closes shares also lands in the trade journal. A fill
// that could no longer be paid for or delivered rejects the order instead.
func (d *DB) ApplyFill(ctx context.Context, orderID int64, price, quote, fee paper.Cents, at time.Time, walletName string) (*paper.Fill, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var walletID int64
	if err := tx.QueryRowContext(ctx, `SELECT wallet_id FROM paper_orders WHERE id = $1`, orderID).Scan(&walletID); err != nil {
		return nil, err
	}
	if _, err := lockWallet(ctx, tx, walletID); err != nil {
		return nil, err
	}
	o, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderCols+` FROM paper_orders WHERE id = $1 FOR UPDATE`, orderID))
	if err != nil {
		return nil, err
	}
	if o.Status != paper.Accepted {
		return nil, nil
	}
	reject := func(why string) (*paper.Fill, error) {
		if _, err := tx.ExecContext(ctx, `
UPDATE paper_orders SET status = 'rejected', reserved_cents = 0, reject_reason = $2, updated_at = now() WHERE id = $1`, orderID, why); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}

	qty := o.Qty - o.FilledQty
	gross := price * paper.Cents(qty)
	var pos struct {
		qty      int64
		cost     paper.Cents
		openedAt time.Time
		exists   bool
	}
	err = tx.QueryRowContext(ctx, `SELECT qty, cost_cents, opened_at FROM paper_positions WHERE wallet_id = $1 AND symbol = $2 FOR UPDATE`,
		walletID, o.Symbol).Scan(&pos.qty, &pos.cost, &pos.openedAt)
	switch {
	case err == nil:
		pos.exists = true
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}

	var postings []paper.Posting
	var realized *paper.Cents
	if o.Side == paper.Buy {
		bal, err := balances(ctx, tx, walletID)
		if err != nil {
			return nil, err
		}
		if bal["cash"] < gross+fee {
			return reject(fmt.Sprintf("the price moved to %s and the order would cost %s, more than the cash available", price, gross+fee))
		}
		postings = []paper.Posting{{Account: "cash", AmountCents: -(gross + fee)}, {Account: "securities", AmountCents: gross}, {Account: "fees", AmountCents: fee}}
		if pos.exists && pos.qty > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE paper_positions SET qty = qty + $3, cost_cents = cost_cents + $4, updated_at = $5 WHERE wallet_id = $1 AND symbol = $2`,
				walletID, o.Symbol, qty, int64(gross), at)
		} else {
			_, err = tx.ExecContext(ctx, `
INSERT INTO paper_positions (wallet_id, symbol, qty, cost_cents, opened_at, updated_at) VALUES ($1, $2, $3, $4, $5, $5)
ON CONFLICT (wallet_id, symbol) DO UPDATE SET qty = EXCLUDED.qty, cost_cents = EXCLUDED.cost_cents, opened_at = EXCLUDED.opened_at, updated_at = EXCLUDED.updated_at`,
				walletID, o.Symbol, qty, int64(gross), at)
		}
		if err != nil {
			return nil, err
		}
	} else {
		if !pos.exists || pos.qty < qty {
			return reject("the shares are no longer held")
		}
		basis := pos.cost
		if qty < pos.qty {
			basis = paper.Cents((int64(pos.cost)*qty + pos.qty/2) / pos.qty) // proportional, rounded
		}
		gain := gross - basis
		realized = &gain
		postings = []paper.Posting{
			{Account: "cash", AmountCents: gross - fee}, {Account: "securities", AmountCents: -basis},
			{Account: "fees", AmountCents: fee}, {Account: "realized_pnl", AmountCents: -gain},
		}
		if _, err := tx.ExecContext(ctx, `UPDATE paper_positions SET qty = qty - $3, cost_cents = cost_cents - $4, updated_at = $5 WHERE wallet_id = $1 AND symbol = $2`,
			walletID, o.Symbol, qty, int64(basis), at); err != nil {
			return nil, err
		}
		// The journal gets the closed trade, set against its news like any other.
		entry := float64(basis) / float64(qty) / 100
		if _, err := tx.ExecContext(ctx, `
INSERT INTO trades (symbol, quantity, entry_price, exit_price, opened_at, closed_at, realized_pnl, account, notes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			o.Symbol, float64(qty), entry, price.Dollars(), pos.openedAt, at, gain.Dollars()-fee.Dollars(),
			"paper: "+walletName, o.Reason); err != nil {
			return nil, err
		}
	}

	ledgerID, err := postLedger(ctx, tx, walletID, string(o.Side), "order", &o.ID, fmt.Sprintf("%s %d %s @ %s", o.Side, qty, o.Symbol, price), postings)
	if err != nil {
		return nil, err
	}
	f := &paper.Fill{OrderID: o.ID, WalletID: walletID, Symbol: o.Symbol, Side: o.Side, Qty: qty, PriceCents: price,
		FeeCents: fee, QuoteCents: quote, RealizedCents: realized, FilledAt: at}
	var realizedArg, openedArg any
	if realized != nil {
		realizedArg, openedArg = int64(*realized), pos.openedAt
	}
	if err := tx.QueryRowContext(ctx, `
INSERT INTO paper_fills (order_id, wallet_id, symbol, side, qty, price_cents, fee_cents, quote_cents, realized_cents, opened_at, ledger_tx_id, filled_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		o.ID, walletID, o.Symbol, string(o.Side), qty, int64(price), int64(fee), int64(quote), realizedArg, openedArg, ledgerID, at).Scan(&f.ID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE paper_orders SET status = 'filled', filled_qty = qty, avg_fill_cents = $2, reserved_cents = 0, filled_at = $3, updated_at = now()
WHERE id = $1`, o.ID, int64(price), at); err != nil {
		return nil, err
	}
	// One exit filling cancels its sibling: a stop and a target are one-cancels-other.
	if o.ParentOrderID != nil {
		if _, err := tx.ExecContext(ctx, `
UPDATE paper_orders SET status = 'canceled', reserved_cents = 0, reject_reason = 'the other exit filled', updated_at = now()
WHERE parent_order_id = $1 AND id <> $2 AND status = 'accepted'`, *o.ParentOrderID, o.ID); err != nil {
			return nil, err
		}
	}
	// A position that closed takes its remaining exits with it.
	if o.Side == paper.Sell && pos.qty == qty {
		if _, err := tx.ExecContext(ctx, `
UPDATE paper_orders SET status = 'canceled', reserved_cents = 0, reject_reason = 'the position closed', updated_at = now()
WHERE wallet_id = $1 AND symbol = $2 AND side = 'sell' AND status = 'accepted' AND id <> $3`, walletID, o.Symbol, o.ID); err != nil {
			return nil, err
		}
	}
	return f, tx.Commit()
}

// Positions lists a wallet's open holdings, without prices.
func (d *DB) Positions(ctx context.Context, walletID int64) ([]paper.Holding, error) {
	a, err := account(ctx, d.db, walletID, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]paper.Holding, 0, len(a.Holdings))
	for _, h := range a.Holdings {
		out = append(out, h)
	}
	return out, nil
}

// Fills lists a wallet's executions, newest first.
func (d *DB) Fills(ctx context.Context, walletID int64, limit int) ([]paper.Fill, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, order_id, wallet_id, symbol, side, qty, price_cents, fee_cents, quote_cents, realized_cents, filled_at
FROM paper_fills WHERE wallet_id = $1 ORDER BY filled_at DESC, id DESC LIMIT $2`, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.Fill
	for rows.Next() {
		var f paper.Fill
		var realized sql.NullInt64
		if err := rows.Scan(&f.ID, &f.OrderID, &f.WalletID, &f.Symbol, &f.Side, &f.Qty, &f.PriceCents, &f.FeeCents, &f.QuoteCents, &realized, &f.FilledAt); err != nil {
			return nil, err
		}
		if realized.Valid {
			c := paper.Cents(realized.Int64)
			f.RealizedCents = &c
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Ledger lists a wallet's ledger transactions with their postings, newest first.
func (d *DB) Ledger(ctx context.Context, walletID int64, limit int) ([]paper.LedgerTx, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT t.id, t.kind, t.ref_type, t.ref_id, t.memo, t.created_at, p.account, p.amount_cents
FROM (SELECT * FROM paper_ledger_tx WHERE wallet_id = $1 ORDER BY id DESC LIMIT $2) t
JOIN paper_postings p ON p.tx_id = t.id ORDER BY t.id DESC, p.account`, walletID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.LedgerTx
	for rows.Next() {
		var t paper.LedgerTx
		var p paper.Posting
		if err := rows.Scan(&t.ID, &t.Kind, &t.RefType, &t.RefID, &t.Memo, &t.CreatedAt, &p.Account, &p.AmountCents); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == t.ID {
			out[n-1].Postings = append(out[n-1].Postings, p)
			continue
		}
		t.Postings = []paper.Posting{p}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- equity -----------------------------------------------------------------

// MarkEquity records a wallet's value and raises its peak.
func (d *DB) MarkEquity(ctx context.Context, walletID int64, p paper.EquityPoint) error {
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO paper_equity (wallet_id, at, cash_cents, market_value_cents, equity_cents, benchmark) VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (wallet_id, at) DO UPDATE SET cash_cents = EXCLUDED.cash_cents, market_value_cents = EXCLUDED.market_value_cents,
    equity_cents = EXCLUDED.equity_cents, benchmark = EXCLUDED.benchmark`,
		walletID, p.At, int64(p.CashCents), int64(p.MarketCents), int64(p.EquityCents), p.Benchmark); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `UPDATE paper_wallets SET peak_equity_cents = GREATEST(peak_equity_cents, $2) WHERE id = $1`, walletID, int64(p.EquityCents))
	return err
}

// EquityCurve returns a wallet's marks since a time, oldest first.
func (d *DB) EquityCurve(ctx context.Context, walletID int64, since time.Time) ([]paper.EquityPoint, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT at, cash_cents, market_value_cents, equity_cents, benchmark FROM paper_equity
WHERE wallet_id = $1 AND at >= $2 ORDER BY at`, walletID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.EquityPoint
	for rows.Next() {
		var p paper.EquityPoint
		if err := rows.Scan(&p.At, &p.CashCents, &p.MarketCents, &p.EquityCents, &p.Benchmark); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- agents -----------------------------------------------------------------

const agentCols = `id, wallet_id, kind, name, enabled, config, halted_reason, last_run_at, created_at`

func scanAgent(row interface{ Scan(...any) error }) (paper.Agent, error) {
	var a paper.Agent
	var raw []byte
	if err := row.Scan(&a.ID, &a.WalletID, &a.Kind, &a.Name, &a.Enabled, &raw, &a.HaltedReason, &a.LastRunAt, &a.CreatedAt); err != nil {
		return a, err
	}
	_ = json.Unmarshal(raw, &a.Config)
	return a, nil
}

// SaveAgent creates an agent, or updates it when it has an id.
func (d *DB) SaveAgent(ctx context.Context, a paper.Agent) (paper.Agent, error) {
	raw, _ := json.Marshal(a.Config)
	if a.ID == 0 {
		return scanAgent(d.db.QueryRowContext(ctx, `
INSERT INTO paper_agents (wallet_id, kind, name, enabled, config) VALUES ($1,$2,$3,$4,$5) RETURNING `+agentCols,
			a.WalletID, string(a.Kind), a.Name, a.Enabled, raw))
	}
	out, err := scanAgent(d.db.QueryRowContext(ctx, `
UPDATE paper_agents SET name = $3, enabled = $4, config = $5, halted_reason = CASE WHEN $4 THEN '' ELSE halted_reason END, updated_at = now()
WHERE id = $1 AND wallet_id = $2 RETURNING `+agentCols, a.ID, a.WalletID, a.Name, a.Enabled, raw))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

// Agents lists agents; walletID 0 lists every enabled one.
func (d *DB) Agents(ctx context.Context, walletID int64) ([]paper.Agent, error) {
	q, args := `SELECT `+agentCols+` FROM paper_agents WHERE wallet_id = $1 ORDER BY id`, []any{walletID}
	if walletID == 0 {
		q, args = `SELECT a.id, a.wallet_id, a.kind, a.name, a.enabled, a.config, a.halted_reason, a.last_run_at, a.created_at
FROM paper_agents a JOIN paper_wallets w ON w.id = a.wallet_id WHERE a.enabled AND w.status = 'active' ORDER BY a.id`, nil
	}
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAgent removes an agent and its decision log.
func (d *DB) DeleteAgent(ctx context.Context, walletID, agentID int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM paper_agents WHERE id = $1 AND wallet_id = $2`, agentID, walletID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAgent records when an agent ran, and halts it when given a reason.
func (d *DB) TouchAgent(ctx context.Context, agentID int64, at time.Time, haltReason string) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE paper_agents SET last_run_at = $2,
    enabled = CASE WHEN $3 <> '' THEN false ELSE enabled END,
    halted_reason = CASE WHEN $3 <> '' THEN $3 ELSE halted_reason END
WHERE id = $1`, agentID, at, haltReason)
	return err
}

// SaveDecision records one agent run.
func (d *DB) SaveDecision(ctx context.Context, dec paper.Decision) error {
	intents, _ := json.Marshal(dec.Intents)
	rejected := dec.Rejected
	if len(rejected) == 0 {
		rejected = json.RawMessage("[]")
	}
	ids := make([]string, len(dec.OrderIDs))
	for i, id := range dec.OrderIDs {
		ids[i] = fmt.Sprint(id)
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO paper_decisions (agent_id, wallet_id, at, summary, intents, order_ids, rejected, model, error)
VALUES ($1,$2,$3,$4,$5,$6::bigint[],$7,$8,$9)`,
		dec.AgentID, dec.WalletID, dec.At, dec.Summary, intents, stringArray(ids), []byte(rejected), dec.Model, dec.Error)
	return err
}

// Decisions lists an agent's runs, newest first.
func (d *DB) Decisions(ctx context.Context, walletID, agentID int64, limit int) ([]paper.Decision, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT id, agent_id, wallet_id, at, summary, intents, order_ids::text[], rejected, model, error
FROM paper_decisions WHERE wallet_id = $1 AND ($2 = 0 OR agent_id = $2) ORDER BY at DESC LIMIT $3`, walletID, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.Decision
	for rows.Next() {
		var dec paper.Decision
		var intents, rejected []byte
		var ids []string
		if err := rows.Scan(&dec.ID, &dec.AgentID, &dec.WalletID, &dec.At, &dec.Summary, &intents, (*stringArray)(&ids), &rejected, &dec.Model, &dec.Error); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(intents, &dec.Intents)
		dec.Rejected = rejected
		for _, s := range ids {
			var id int64
			if _, err := fmt.Sscan(s, &id); err == nil {
				dec.OrderIDs = append(dec.OrderIDs, id)
			}
		}
		out = append(out, dec)
	}
	return out, rows.Err()
}

// RoundTrips lists a wallet's closed trades: each sale with when its
// position was opened and the return on the cost it closed.
func (d *DB) RoundTrips(ctx context.Context, walletID int64) ([]paper.RoundTrip, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT symbol, opened_at, filled_at, realized_cents, price_cents * qty - realized_cents
FROM paper_fills WHERE wallet_id = $1 AND side = 'sell' AND realized_cents IS NOT NULL AND opened_at IS NOT NULL
ORDER BY filled_at`, walletID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []paper.RoundTrip
	for rows.Next() {
		var t paper.RoundTrip
		var realized, basis int64
		if err := rows.Scan(&t.Symbol, &t.Opened, &t.Closed, &realized, &basis); err != nil {
			return nil, err
		}
		if basis > 0 {
			t.ReturnPct = float64(realized) / float64(basis) * 100
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
