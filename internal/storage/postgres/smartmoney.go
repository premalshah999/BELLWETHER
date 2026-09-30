package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/smartmoney"
)

func nullF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func (d *DB) InsiderFilingSeen(ctx context.Context, accession string) (bool, error) {
	var ok bool
	err := d.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM insider_filings_seen WHERE accession = $1)`, accession).Scan(&ok)
	return ok, err
}

func (d *DB) MarkInsiderFiling(ctx context.Context, accession string, ok bool) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO insider_filings_seen (accession, ok) VALUES ($1, $2)
ON CONFLICT (accession) DO UPDATE SET ok = EXCLUDED.ok, fetched_at = now()`, accession, ok)
	return err
}

func (d *DB) SaveInsiderTrades(ctx context.Context, trades []smartmoney.Trade) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range trades {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO insider_trades (accession, line, symbol, issuer_cik, issuer_name, owner_cik, owner_name,
    is_director, is_officer, is_ten_pct, officer_title, security, tx_date, code, acquired, shares,
    price, value, owned_after, direct, plan_10b5_1, filed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
ON CONFLICT (accession, line) DO NOTHING`,
			t.Accession, t.Line, t.Symbol, t.IssuerCIK, t.IssuerName, t.OwnerCIK, t.OwnerName,
			t.IsDirector, t.IsOfficer, t.IsTenPct, t.OfficerTitle, t.Security, t.TxDate, t.Code, t.Acquired,
			t.Shares, nullF(t.Price), nullF(t.Value), nullF(t.OwnedAfter), t.Direct, t.Plan105b1, t.FiledAt); err != nil {
			return fmt.Errorf("postgres: save insider trade: %w", err)
		}
	}
	return tx.Commit()
}

func (d *DB) UpsertFund(ctx context.Context, f smartmoney.Fund) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO funds (cik, name, manager, style) VALUES ($1,$2,$3,$4)
ON CONFLICT (cik) DO UPDATE SET name = EXCLUDED.name, manager = EXCLUDED.manager, style = EXCLUDED.style`,
		f.CIK, f.Name, f.Manager, f.Style)
	return err
}

func (d *DB) FundFilingExists(ctx context.Context, accession string) (bool, error) {
	var ok bool
	err := d.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM fund_filings WHERE accession = $1)`, accession).Scan(&ok)
	return ok, err
}

func (d *DB) SaveFundFiling(ctx context.Context, f smartmoney.FundFiling, hs []smartmoney.Holding) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A newer filing for the same quarter replaces the older one.
	if _, err := tx.ExecContext(ctx, `DELETE FROM fund_filings WHERE cik = $1 AND period = $2`, f.CIK, f.Period); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO fund_filings (accession, cik, period, filed, total_value, positions) VALUES ($1,$2,$3,$4,$5,$6)`,
		f.Accession, f.CIK, f.Period, f.Filed, f.TotalValue, f.Positions); err != nil {
		return fmt.Errorf("postgres: save fund filing: %w", err)
	}
	for _, h := range hs {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO fund_holdings (accession, cusip, issuer, symbol, value, shares, put_call) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT DO NOTHING`, f.Accession, h.CUSIP, h.Issuer, h.Symbol, h.Value, h.Shares, h.PutCall); err != nil {
			return fmt.Errorf("postgres: save fund holding: %w", err)
		}
	}
	return tx.Commit()
}

func (d *DB) CUSIPSymbols(ctx context.Context, cusips []string) (map[string]string, error) {
	out := map[string]string{}
	if len(cusips) == 0 {
		return out, nil
	}
	rows, err := d.db.QueryContext(ctx, `SELECT cusip, symbol FROM cusip_symbols WHERE cusip = ANY($1::text[])`, stringArray(cusips))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, s string
		if err := rows.Scan(&c, &s); err != nil {
			return nil, err
		}
		out[c] = s
	}
	return out, rows.Err()
}

func (d *DB) SaveCUSIPSymbols(ctx context.Context, m map[string]string) error {
	for c, s := range m {
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO cusip_symbols (cusip, symbol) VALUES ($1,$2)
ON CONFLICT (cusip) DO UPDATE SET symbol = EXCLUDED.symbol, looked_up_at = now()`, c, s); err != nil {
			return err
		}
	}
	return nil
}

// ---- reads ---------------------------------------------------------------

// InsiderFilter narrows a trade listing.
type InsiderFilter struct {
	Symbol string
	Since  time.Time
	// Market limits to open-market purchases and sales.
	Market bool
	// Side is "buy", "sell" or empty for both (with Market).
	Side  string
	Limit int
}

const insiderCols = `accession, symbol, issuer_cik, issuer_name, owner_cik, owner_name, is_director, is_officer,
    is_ten_pct, officer_title, security, tx_date, code, acquired, shares, price, value, owned_after, direct,
    plan_10b5_1, filed_at`

func scanTrade(rows *sql.Rows) (smartmoney.Trade, error) {
	var (
		t                   smartmoney.Trade
		price, value, owned sql.NullFloat64
	)
	err := rows.Scan(&t.Accession, &t.Symbol, &t.IssuerCIK, &t.IssuerName, &t.OwnerCIK, &t.OwnerName,
		&t.IsDirector, &t.IsOfficer, &t.IsTenPct, &t.OfficerTitle, &t.Security, &t.TxDate, &t.Code, &t.Acquired,
		&t.Shares, &price, &value, &owned, &t.Direct, &t.Plan105b1, &t.FiledAt)
	if price.Valid {
		t.Price = &price.Float64
	}
	if value.Valid {
		t.Value = &value.Float64
	}
	if owned.Valid {
		t.OwnedAfter = &owned.Float64
	}
	return t, err
}

func (d *DB) ListInsiderTrades(ctx context.Context, f InsiderFilter) ([]smartmoney.Trade, error) {
	where := []string{"tx_date >= $1"}
	args := []any{f.Since}
	if f.Symbol != "" {
		args = append(args, f.Symbol)
		where = append(where, fmt.Sprintf("symbol = $%d", len(args)))
	}
	switch {
	case f.Market && f.Side == "buy":
		where = append(where, "code = 'P'")
	case f.Market && f.Side == "sell":
		where = append(where, "code = 'S'")
	case f.Market:
		where = append(where, "code IN ('P','S')")
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	args = append(args, limit)
	q := `SELECT ` + insiderCols + ` FROM insider_trades WHERE ` + joinAnd(where) +
		fmt.Sprintf(` ORDER BY tx_date DESC, value DESC NULLS LAST LIMIT $%d`, len(args))
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list insider trades: %w", err)
	}
	defer rows.Close()
	var out []smartmoney.Trade
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

// InsiderLeaders ranks companies by open-market insider activity since a date.
func (d *DB) InsiderLeaders(ctx context.Context, since time.Time, limit int) ([]smartmoney.Leader, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT symbol, max(issuer_name),
       COALESCE(sum(value) FILTER (WHERE code = 'P'), 0),
       COALESCE(sum(value) FILTER (WHERE code = 'S'), 0),
       count(DISTINCT owner_cik) FILTER (WHERE code = 'P'),
       count(DISTINCT owner_cik) FILTER (WHERE code = 'S'),
       count(*) FILTER (WHERE code = 'P'),
       count(*) FILTER (WHERE code = 'S'),
       max(tx_date),
       COALESCE(array_agg(DISTINCT owner_name) FILTER (WHERE code = 'P'), '{}')
FROM insider_trades
WHERE tx_date >= $1 AND code IN ('P','S')
GROUP BY symbol
ORDER BY COALESCE(sum(value) FILTER (WHERE code = 'P'), 0) - COALESCE(sum(value) FILTER (WHERE code = 'S'), 0) DESC
LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: insider leaders: %w", err)
	}
	defer rows.Close()
	var out []smartmoney.Leader
	for rows.Next() {
		var l smartmoney.Leader
		if err := rows.Scan(&l.Symbol, &l.Issuer, &l.BuyValue, &l.SellValue, &l.Buyers, &l.Sellers,
			&l.Buys, &l.Sells, &l.LastTrade, (*stringArray)(&l.BuyerNames)); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// FundSummaries lists followed funds with their latest filing and its moves.
func (d *DB) FundSummaries(ctx context.Context) ([]smartmoney.FundSummary, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT cik, name, manager, style FROM funds ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var funds []smartmoney.Fund
	for rows.Next() {
		var f smartmoney.Fund
		if err := rows.Scan(&f.CIK, &f.Name, &f.Manager, &f.Style); err != nil {
			rows.Close()
			return nil, err
		}
		funds = append(funds, f)
	}
	rows.Close()
	out := make([]smartmoney.FundSummary, 0, len(funds))
	for _, f := range funds {
		moves, latest, err := d.FundMoves(ctx, f.CIK)
		if err != nil {
			return nil, err
		}
		s := smartmoney.FundSummary{Fund: f}
		if latest != nil {
			s.Period, s.Filed, s.TotalValue, s.Positions = &latest.Period, &latest.Filed, latest.TotalValue, latest.Positions
		}
		for _, m := range moves {
			switch m.Kind {
			case "new":
				s.New++
			case "exited":
				s.Exited++
			}
		}
		held := make([]smartmoney.Move, 0, len(moves))
		for _, m := range moves {
			if m.Kind != "exited" {
				held = append(held, m)
			}
		}
		sort.Slice(held, func(i, j int) bool { return held[i].Value > held[j].Value })
		if len(held) > 5 {
			held = held[:5]
		}
		s.Top = held
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalValue > out[j].TotalValue })
	return out, nil
}

// FundMoves compares a fund's latest 13F with the one before it.
func (d *DB) FundMoves(ctx context.Context, cik string) ([]smartmoney.Move, *smartmoney.FundFiling, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT accession, period, filed, total_value, positions FROM fund_filings WHERE cik = $1 ORDER BY period DESC LIMIT 2`, cik)
	if err != nil {
		return nil, nil, err
	}
	var filings []smartmoney.FundFiling
	for rows.Next() {
		var f smartmoney.FundFiling
		if err := rows.Scan(&f.Accession, &f.Period, &f.Filed, &f.TotalValue, &f.Positions); err != nil {
			rows.Close()
			return nil, nil, err
		}
		f.CIK = cik
		filings = append(filings, f)
	}
	rows.Close()
	if len(filings) == 0 {
		return nil, nil, nil
	}
	latest := filings[0]
	prevAcc := ""
	if len(filings) > 1 {
		prevAcc = filings[1].Accession
	}
	q := `
WITH cur AS (SELECT cusip, put_call, issuer, symbol, value, shares FROM fund_holdings WHERE accession = $1),
     prev AS (SELECT cusip, put_call, issuer, symbol, value, shares FROM fund_holdings WHERE accession = $2)
SELECT COALESCE(c.cusip, p.cusip), COALESCE(c.issuer, p.issuer), COALESCE(NULLIF(c.symbol,''), p.symbol, ''),
       COALESCE(c.value, 0), COALESCE(c.shares, 0), COALESCE(p.value, 0), COALESCE(p.shares, 0),
       f.name, f.manager
FROM cur c FULL OUTER JOIN prev p ON p.cusip = c.cusip AND p.put_call = c.put_call
CROSS JOIN (SELECT name, manager FROM funds WHERE cik = $3) f
WHERE COALESCE(c.put_call, p.put_call) = ''`
	rows, err = d.db.QueryContext(ctx, q, latest.Accession, prevAcc, cik)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: fund moves: %w", err)
	}
	defer rows.Close()
	var out []smartmoney.Move
	q4 := (int(latest.Period.Month())-1)/3 + 1
	label := fmt.Sprintf("Q%d %d", q4, latest.Period.Year())
	for rows.Next() {
		var m smartmoney.Move
		if err := rows.Scan(&m.CUSIP, &m.Issuer, &m.Symbol, &m.Value, &m.Shares, &m.PrevValue, &m.PrevShares,
			&m.FundName, &m.Manager); err != nil {
			return nil, nil, err
		}
		m.FundCIK, m.PeriodLabel = cik, label
		switch {
		case prevAcc == "":
			m.Kind = "held"
		case m.PrevShares == 0 && m.Shares > 0:
			m.Kind = "new"
		case m.Shares == 0 && m.PrevShares > 0:
			m.Kind = "exited"
		case m.Shares > m.PrevShares*1.02:
			m.Kind = "added"
		case m.Shares < m.PrevShares*0.98:
			m.Kind = "trimmed"
		default:
			m.Kind = "held"
		}
		if m.PrevShares > 0 {
			m.ChangePct = (m.Shares - m.PrevShares) / m.PrevShares * 100
		}
		if latest.TotalValue > 0 {
			m.WeightPct = m.Value / latest.TotalValue * 100
		}
		out = append(out, m)
	}
	return out, &latest, rows.Err()
}

// SymbolFundMoves lists every followed fund's latest move in one stock.
func (d *DB) SymbolFundMoves(ctx context.Context, symbol string) ([]smartmoney.Move, error) {
	funds, err := d.FundSummaries(ctx)
	if err != nil {
		return nil, err
	}
	var out []smartmoney.Move
	for _, f := range funds {
		moves, _, err := d.FundMoves(ctx, f.CIK)
		if err != nil {
			return nil, err
		}
		for _, m := range moves {
			if m.Symbol == symbol {
				out = append(out, m)
			}
		}
	}
	return out, nil
}
