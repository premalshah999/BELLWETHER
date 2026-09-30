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

// InsiderFilingsSeen reports which of these filings have been read before.
func (d *DB) InsiderFilingsSeen(ctx context.Context, accessions []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(accessions) == 0 {
		return out, nil
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT accession FROM insider_filings_seen WHERE accession = ANY($1::text[])
UNION SELECT DISTINCT accession FROM insider_trades WHERE accession = ANY($1::text[])`, stringArray(accessions))
	if err != nil {
		return nil, fmt.Errorf("postgres: insider filings seen: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out[a] = true
	}
	return out, rows.Err()
}

// MarkInsiderFilings records filings read from a bulk dataset.
func (d *DB) MarkInsiderFilings(ctx context.Context, accessions []string) error {
	if len(accessions) == 0 {
		return nil
	}
	_, err := d.db.ExecContext(ctx, `
INSERT INTO insider_filings_seen (accession, ok)
SELECT unnest($1::text[]), true ON CONFLICT (accession) DO NOTHING`, stringArray(accessions))
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
INSERT INTO funds (cik, name, manager, style, curated) VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (cik) DO UPDATE SET name = EXCLUDED.name, manager = EXCLUDED.manager, style = EXCLUDED.style,
    curated = funds.curated OR EXCLUDED.curated`,
		f.CIK, f.Name, f.Manager, f.Style, f.Curated)
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

// fundFilingsCTE ranks each followed fund's 13Fs newest first; cur is the
// latest and prev the one before, which every move is measured against.
const fundFilingsCTE = `
WITH ranked AS (
    SELECT ff.cik, ff.accession, ff.period, ff.filed, ff.total_value, ff.positions,
           row_number() OVER (PARTITION BY ff.cik ORDER BY ff.period DESC, ff.filed DESC) AS rn
    FROM fund_filings ff JOIN funds f ON f.cik = ff.cik
    WHERE f.followed AND ($1 = '' OR ff.cik = $1)
),
cur AS (SELECT * FROM ranked WHERE rn = 1),
prev AS (SELECT * FROM ranked WHERE rn = 2)`

// resolvedSymbol is a holding's ticker: stored with it, or resolved since.
const resolvedSymbol = `COALESCE(NULLIF(h.symbol, ''), cs.symbol, '')`

// MoveFilter narrows a fund-moves query. Empty fields do not filter.
type MoveFilter struct {
	CIK    string
	Symbol string
	// MinValue keeps positions worth at least this much now or before.
	MinValue float64
}

// FundMovesWhere compares each followed fund's latest 13F with its previous
// one and returns every position that changed or was held, with the filing
// the moves are measured on per fund.
func (d *DB) FundMovesWhere(ctx context.Context, f MoveFilter) ([]smartmoney.Move, map[string]smartmoney.FundFiling, error) {
	q := fundFilingsCTE + `,
ch AS (
    SELECT cur.cik, h.cusip, h.issuer, ` + resolvedSymbol + ` AS symbol, h.value, h.shares
    FROM cur JOIN fund_holdings h ON h.accession = cur.accession AND h.put_call = ''
    LEFT JOIN cusip_symbols cs ON cs.cusip = h.cusip
),
ph AS (
    SELECT prev.cik, h.cusip, h.issuer, ` + resolvedSymbol + ` AS symbol, h.value, h.shares
    FROM prev JOIN fund_holdings h ON h.accession = prev.accession AND h.put_call = ''
    LEFT JOIN cusip_symbols cs ON cs.cusip = h.cusip
)
SELECT COALESCE(c.cik, p.cik), COALESCE(c.cusip, p.cusip), COALESCE(c.issuer, p.issuer),
       COALESCE(NULLIF(c.symbol, ''), p.symbol, ''),
       COALESCE(c.value, 0), COALESCE(c.shares, 0), COALESCE(p.value, 0), COALESCE(p.shares, 0)
FROM ch c FULL OUTER JOIN ph p ON p.cik = c.cik AND p.cusip = c.cusip
WHERE ($2 = '' OR COALESCE(NULLIF(c.symbol, ''), p.symbol) = $2)
  AND GREATEST(COALESCE(c.value, 0), COALESCE(p.value, 0)) >= $3`
	rows, err := d.db.QueryContext(ctx, q, f.CIK, f.Symbol, f.MinValue)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: fund moves: %w", err)
	}
	var out []smartmoney.Move
	for rows.Next() {
		var m smartmoney.Move
		if err := rows.Scan(&m.FundCIK, &m.CUSIP, &m.Issuer, &m.Symbol, &m.Value, &m.Shares, &m.PrevValue, &m.PrevShares); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	filings, funds, err := d.latestFundFilings(ctx, f.CIK)
	if err != nil {
		return nil, nil, err
	}
	for i := range out {
		m := &out[i]
		fl := filings[m.FundCIK]
		fund := funds[m.FundCIK]
		m.FundName, m.Manager = fund.Name, fund.Manager
		m.PeriodLabel = fmt.Sprintf("Q%d %d", (int(fl.filing.Period.Month())-1)/3+1, fl.filing.Period.Year())
		smartmoney.Classify(m, fl.hasPrev, fl.filing.TotalValue)
	}
	latest := make(map[string]smartmoney.FundFiling, len(filings))
	for cik, fl := range filings {
		latest[cik] = fl.filing
	}
	return out, latest, nil
}

type latestFiling struct {
	filing  smartmoney.FundFiling
	hasPrev bool
}

func (d *DB) latestFundFilings(ctx context.Context, cik string) (map[string]latestFiling, map[string]smartmoney.Fund, error) {
	rows, err := d.db.QueryContext(ctx, fundFilingsCTE+`
SELECT cur.cik, cur.accession, cur.period, cur.filed, cur.total_value, cur.positions, prev.accession IS NOT NULL,
       f.name, f.manager, f.style
FROM cur JOIN funds f ON f.cik = cur.cik LEFT JOIN prev ON prev.cik = cur.cik`, cik)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: latest fund filings: %w", err)
	}
	defer rows.Close()
	filings := map[string]latestFiling{}
	funds := map[string]smartmoney.Fund{}
	for rows.Next() {
		var fl latestFiling
		var fund smartmoney.Fund
		if err := rows.Scan(&fl.filing.CIK, &fl.filing.Accession, &fl.filing.Period, &fl.filing.Filed,
			&fl.filing.TotalValue, &fl.filing.Positions, &fl.hasPrev, &fund.Name, &fund.Manager, &fund.Style); err != nil {
			return nil, nil, err
		}
		fund.CIK = fl.filing.CIK
		filings[fl.filing.CIK], funds[fl.filing.CIK] = fl, fund
	}
	return filings, funds, rows.Err()
}

// FundMoves is one fund's moves and the filing they are measured on.
func (d *DB) FundMoves(ctx context.Context, cik string) ([]smartmoney.Move, *smartmoney.FundFiling, error) {
	moves, filings, err := d.FundMovesWhere(ctx, MoveFilter{CIK: cik})
	if err != nil {
		return nil, nil, err
	}
	f, ok := filings[cik]
	if !ok {
		return nil, nil, nil
	}
	return moves, &f, nil
}

// SymbolFundMoves lists every followed fund's latest move in one stock.
func (d *DB) SymbolFundMoves(ctx context.Context, symbol string) ([]smartmoney.Move, error) {
	moves, _, err := d.FundMovesWhere(ctx, MoveFilter{Symbol: symbol})
	return moves, err
}

// FundSummaries lists every followed fund with its latest filing, how many
// positions it opened and closed, and its five largest holdings. Funds with
// no filing stored yet are listed too, so a newly followed one is visible
// while its first sync runs.
func (d *DB) FundSummaries(ctx context.Context) ([]smartmoney.FundSummary, error) {
	rows, err := d.db.QueryContext(ctx, fundFilingsCTE+`
SELECT f.cik, f.name, f.manager, f.style, f.curated, cur.period, cur.filed,
       COALESCE(cur.total_value, 0), COALESCE(cur.positions, 0),
       CASE WHEN prev.accession IS NULL THEN 0 ELSE (
           SELECT count(*) FROM fund_holdings h WHERE h.accession = cur.accession AND h.put_call = ''
           AND NOT EXISTS (SELECT 1 FROM fund_holdings q WHERE q.accession = prev.accession AND q.cusip = h.cusip AND q.put_call = '')) END,
       CASE WHEN prev.accession IS NULL THEN 0 ELSE (
           SELECT count(*) FROM fund_holdings h WHERE h.accession = prev.accession AND h.put_call = ''
           AND NOT EXISTS (SELECT 1 FROM fund_holdings q WHERE q.accession = cur.accession AND q.cusip = h.cusip AND q.put_call = '')) END
FROM funds f LEFT JOIN cur ON cur.cik = f.cik LEFT JOIN prev ON prev.cik = f.cik
WHERE f.followed`, "")
	if err != nil {
		return nil, fmt.Errorf("postgres: fund summaries: %w", err)
	}
	var out []smartmoney.FundSummary
	index := map[string]int{}
	for rows.Next() {
		var s smartmoney.FundSummary
		if err := rows.Scan(&s.CIK, &s.Name, &s.Manager, &s.Style, &s.Curated, &s.Period, &s.Filed,
			&s.TotalValue, &s.Positions, &s.New, &s.Exited); err != nil {
			rows.Close()
			return nil, err
		}
		index[s.CIK] = len(out)
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	top, err := d.db.QueryContext(ctx, fundFilingsCTE+`
SELECT cur.cik, h.cusip, h.issuer, `+resolvedSymbol+`, h.value, h.shares, COALESCE(p.shares, 0), COALESCE(p.value, 0),
       prev.accession IS NOT NULL, cur.total_value
FROM cur
CROSS JOIN LATERAL (SELECT * FROM fund_holdings x WHERE x.accession = cur.accession AND x.put_call = ''
                    ORDER BY x.value DESC LIMIT 5) h
LEFT JOIN cusip_symbols cs ON cs.cusip = h.cusip
LEFT JOIN prev ON prev.cik = cur.cik
LEFT JOIN fund_holdings p ON p.accession = prev.accession AND p.cusip = h.cusip AND p.put_call = ''
ORDER BY cur.cik, h.value DESC`, "")
	if err != nil {
		return nil, fmt.Errorf("postgres: fund top holdings: %w", err)
	}
	defer top.Close()
	for top.Next() {
		var m smartmoney.Move
		var hasPrev bool
		var total float64
		if err := top.Scan(&m.FundCIK, &m.CUSIP, &m.Issuer, &m.Symbol, &m.Value, &m.Shares, &m.PrevShares, &m.PrevValue, &hasPrev, &total); err != nil {
			return nil, err
		}
		smartmoney.Classify(&m, hasPrev, total)
		if i, ok := index[m.FundCIK]; ok {
			m.FundName, m.Manager = out[i].Name, out[i].Manager
			out[i].Top = append(out[i].Top, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalValue > out[j].TotalValue })
	return out, top.Err()
}

// FollowFund adds a manager to the followed list, or brings one back.
func (d *DB) FollowFund(ctx context.Context, f smartmoney.Fund) error {
	_, err := d.db.ExecContext(ctx, `
INSERT INTO funds (cik, name, manager, style, followed) VALUES ($1, $2, $3, $4, true)
ON CONFLICT (cik) DO UPDATE SET followed = true,
    manager = CASE WHEN EXCLUDED.manager <> '' THEN EXCLUDED.manager ELSE funds.manager END,
    style = CASE WHEN EXCLUDED.style <> '' THEN EXCLUDED.style ELSE funds.style END`,
		f.CIK, f.Name, f.Manager, f.Style)
	return err
}

// UnfollowFund stops following a manager. Its filings stay, so following it
// again is instant.
func (d *DB) UnfollowFund(ctx context.Context, cik string) error {
	res, err := d.db.ExecContext(ctx, `UPDATE funds SET followed = false WHERE cik = $1`, cik)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FollowedFunds lists the managers the sync reads.
func (d *DB) FollowedFunds(ctx context.Context) ([]smartmoney.Fund, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT cik, name, manager, style, curated FROM funds WHERE followed ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []smartmoney.Fund
	for rows.Next() {
		var f smartmoney.Fund
		if err := rows.Scan(&f.CIK, &f.Name, &f.Manager, &f.Style, &f.Curated); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UnresolvedCUSIPs lists holdings' CUSIPs never looked up, most valuable first.
func (d *DB) UnresolvedCUSIPs(ctx context.Context, limit int) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT h.cusip FROM fund_holdings h
WHERE h.symbol = '' AND NOT EXISTS (SELECT 1 FROM cusip_symbols c WHERE c.cusip = h.cusip)
GROUP BY h.cusip ORDER BY max(h.value) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsiderStudyEvent is one filing's open-market trades, for the event study.
type InsiderStudyEvent struct {
	Symbol  string
	FiledAt time.Time
	Role    string
	Value   float64
}

// InsiderStudyEvents returns one event per Form 4 filing whose open-market
// trades of the given code (P or S) total at least minValue. Pre-arranged
// 10b5-1 sales are left out: they say nothing about a view.
func (d *DB) InsiderStudyEvents(ctx context.Context, code string, minValue float64) ([]InsiderStudyEvent, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT symbol, filed_at,
       CASE WHEN bool_or(officer_title ~* '(chief executive|ceo|chief financial|cfo|president)') THEN 'CEO, CFO or president'
            WHEN bool_or(is_officer) THEN 'other officer'
            WHEN bool_or(is_director) THEN 'director'
            WHEN bool_or(is_ten_pct) THEN '10% owner'
            ELSE 'other' END,
       sum(value)
FROM insider_trades
WHERE code = $1 AND NOT plan_10b5_1 AND value IS NOT NULL
GROUP BY accession, symbol, filed_at
HAVING sum(value) >= $2`, code, minValue)
	if err != nil {
		return nil, fmt.Errorf("postgres: insider study events: %w", err)
	}
	defer rows.Close()
	var out []InsiderStudyEvent
	for rows.Next() {
		var e InsiderStudyEvent
		if err := rows.Scan(&e.Symbol, &e.FiledAt, &e.Role, &e.Value); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// InsiderStudyCounts is how many qualifying filings each insider study has.
func (d *DB) InsiderStudyCounts(ctx context.Context) (buys, sells int, err error) {
	err = d.db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE code = 'P' AND total >= 50000), count(*) FILTER (WHERE code = 'S' AND total >= 250000)
FROM (SELECT accession, code, sum(value) AS total FROM insider_trades
      WHERE code IN ('P','S') AND NOT plan_10b5_1 AND value IS NOT NULL GROUP BY accession, code) t`).Scan(&buys, &sells)
	return
}
