package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/fundamentals"
)

// SaveFundamentals records one company's snapshot and reported periods.
func (d *DB) SaveFundamentals(ctx context.Context, c fundamentals.Company) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save fundamentals: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rawInfo, _ := json.Marshal(orEmptyMap(c.RawInfo))
	s := c.Snapshot
	_, err = tx.ExecContext(ctx, `
		INSERT INTO fundamentals_snapshot (
			symbol, as_of, sector, industry, quote_currency, financial_currency,
			pe_trailing, pe_forward, price_to_book, market_cap, enterprise_value,
			ev_to_ebitda, ev_to_revenue, peg_ratio,
			return_on_equity, return_on_assets, profit_margin, operating_margin,
			gross_margin, ebitda_margin,
			debt_to_equity, current_ratio, quick_ratio, dividend_yield, payout_ratio,
			eps_trailing, eps_forward, book_value, shares_outstanding,
			revenue_growth, earnings_growth, beta, raw)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
		        $19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33)`,
		s.Symbol, s.AsOf, s.Sector, s.Industry, s.QuoteCurrency, s.FinancialCurrency,
		s.PETrailing, s.PEForward, s.PriceToBook, s.MarketCap, s.EnterpriseValue,
		s.EVToEBITDA, s.EVToRevenue, s.PEGRatio,
		s.ReturnOnEquity, s.ReturnOnAssets, s.ProfitMargin, s.OperatingMargin,
		s.GrossMargin, s.EBITDAMargin,
		s.DebtToEquity, s.CurrentRatio, s.QuickRatio, s.DividendYield, s.PayoutRatio,
		s.EPSTrailing, s.EPSForward, s.BookValue, s.SharesOutstanding,
		s.RevenueGrowth, s.EarningsGrowth, s.Beta, rawInfo)
	if err != nil {
		return fmt.Errorf("save fundamentals: snapshot %s: %w", s.Symbol, err)
	}

	for _, p := range c.Periods {
		raw, _ := json.Marshal(orEmptyFloats(c.RawPeriods[p.PeriodType+"|"+p.PeriodEnd]))
		// Upserted rather than inserted: statements are restated, and a
		// restatement should replace the figure rather than sit beside it as
		// a duplicate period. report_date is only overwritten by a known
		// value, so a later fetch that cannot determine it does not erase one
		// we already had.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO financials (
				symbol, period_end, period_type, report_date, currency,
				revenue, gross_profit, operating_income, ebitda, ebit, net_income,
				eps_basic, eps_diluted, interest_expense, tax_provision, total_expenses,
				total_assets, total_debt, net_debt, equity, cash, working_capital,
				invested_capital, tangible_book_value, shares_outstanding,
				free_cash_flow, capex, operating_cash_flow, raw)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
			        $18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)
			ON CONFLICT (symbol, period_end, period_type) DO UPDATE SET
				report_date = COALESCE(EXCLUDED.report_date, financials.report_date),
				currency = EXCLUDED.currency,
				revenue = EXCLUDED.revenue, gross_profit = EXCLUDED.gross_profit,
				operating_income = EXCLUDED.operating_income, ebitda = EXCLUDED.ebitda,
				ebit = EXCLUDED.ebit, net_income = EXCLUDED.net_income,
				eps_basic = EXCLUDED.eps_basic, eps_diluted = EXCLUDED.eps_diluted,
				interest_expense = EXCLUDED.interest_expense,
				tax_provision = EXCLUDED.tax_provision,
				total_expenses = EXCLUDED.total_expenses,
				total_assets = EXCLUDED.total_assets, total_debt = EXCLUDED.total_debt,
				net_debt = EXCLUDED.net_debt, equity = EXCLUDED.equity,
				cash = EXCLUDED.cash, working_capital = EXCLUDED.working_capital,
				invested_capital = EXCLUDED.invested_capital,
				tangible_book_value = EXCLUDED.tangible_book_value,
				shares_outstanding = EXCLUDED.shares_outstanding,
				free_cash_flow = EXCLUDED.free_cash_flow, capex = EXCLUDED.capex,
				operating_cash_flow = EXCLUDED.operating_cash_flow,
				raw = EXCLUDED.raw`,
			p.Symbol, p.PeriodEnd, p.PeriodType, p.ReportDate, p.Currency,
			p.Revenue, p.GrossProfit, p.OperatingIncome, p.EBITDA, p.EBIT, p.NetIncome,
			p.EPSBasic, p.EPSDiluted, p.InterestExpense, p.TaxProvision, p.TotalExpenses,
			p.TotalAssets, p.TotalDebt, p.NetDebt, p.Equity, p.Cash, p.WorkingCapital,
			p.InvestedCapital, p.TangibleBookValue, p.SharesOutstanding,
			p.FreeCashFlow, p.Capex, p.OperatingCashFlow, raw)
		if err != nil {
			return fmt.Errorf("save fundamentals: period %s %s: %w", p.Symbol, p.PeriodEnd, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save fundamentals: commit: %w", err)
	}
	return nil
}

// LatestSnapshot returns the most recent ratio snapshot for one symbol.
func (d *DB) LatestSnapshot(ctx context.Context, symbol string) (fundamentals.Snapshot, error) {
	row := d.db.QueryRowContext(ctx, `
		SELECT symbol, as_of, sector, industry, quote_currency, financial_currency,
		       pe_trailing, pe_forward, price_to_book, market_cap, enterprise_value,
		       ev_to_ebitda, ev_to_revenue, peg_ratio,
		       return_on_equity, return_on_assets, profit_margin, operating_margin,
		       gross_margin, ebitda_margin,
		       debt_to_equity, current_ratio, quick_ratio, dividend_yield, payout_ratio,
		       eps_trailing, eps_forward, book_value, shares_outstanding,
		       revenue_growth, earnings_growth, beta
		FROM fundamentals_snapshot WHERE symbol = $1 ORDER BY id DESC LIMIT 1`, symbol)
	return scanSnapshot(row)
}

func scanSnapshot(row rowScanner) (fundamentals.Snapshot, error) {
	var s fundamentals.Snapshot
	err := row.Scan(&s.Symbol, &s.AsOf, &s.Sector, &s.Industry,
		&s.QuoteCurrency, &s.FinancialCurrency,
		&s.PETrailing, &s.PEForward, &s.PriceToBook, &s.MarketCap, &s.EnterpriseValue,
		&s.EVToEBITDA, &s.EVToRevenue, &s.PEGRatio,
		&s.ReturnOnEquity, &s.ReturnOnAssets, &s.ProfitMargin, &s.OperatingMargin,
		&s.GrossMargin, &s.EBITDAMargin,
		&s.DebtToEquity, &s.CurrentRatio, &s.QuickRatio, &s.DividendYield, &s.PayoutRatio,
		&s.EPSTrailing, &s.EPSForward, &s.BookValue, &s.SharesOutstanding,
		&s.RevenueGrowth, &s.EarningsGrowth, &s.Beta)
	if err != nil {
		return fundamentals.Snapshot{}, err
	}
	s.AsOf = s.AsOf.UTC()
	return s, nil
}

// Financials returns reported periods for a symbol, most recent first.
//
// asOf, when set, restricts results to what was public at that time. Periods
// with no known report date are excluded rather than assumed — that is the
// whole reason the column is nullable.
func (d *DB) Financials(ctx context.Context, symbol, periodType string, limit int, asOf time.Time) ([]fundamentals.Period, error) {
	if limit <= 0 {
		limit = 12
	}
	q := `
		SELECT symbol, period_end::text, period_type, report_date, currency,
		       revenue, gross_profit, operating_income, ebitda, ebit, net_income,
		       eps_basic, eps_diluted, interest_expense, tax_provision, total_expenses,
		       total_assets, total_debt, net_debt, equity, cash, working_capital,
		       invested_capital, tangible_book_value, shares_outstanding,
		       free_cash_flow, capex, operating_cash_flow
		FROM financials
		WHERE symbol = $1 AND period_type = $2`
	args := []any{symbol, periodType}
	if !asOf.IsZero() {
		q += ` AND report_date IS NOT NULL AND report_date <= $3`
		args = append(args, asOf.UTC())
	}
	q += ` ORDER BY period_end DESC LIMIT ` + fmt.Sprint(limit)

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("financials: %w", err)
	}
	defer rows.Close()

	var out []fundamentals.Period
	for rows.Next() {
		var p fundamentals.Period
		var rd sql.NullTime
		if err := rows.Scan(&p.Symbol, &p.PeriodEnd, &p.PeriodType, &rd, &p.Currency,
			&p.Revenue, &p.GrossProfit, &p.OperatingIncome, &p.EBITDA, &p.EBIT, &p.NetIncome,
			&p.EPSBasic, &p.EPSDiluted, &p.InterestExpense, &p.TaxProvision, &p.TotalExpenses,
			&p.TotalAssets, &p.TotalDebt, &p.NetDebt, &p.Equity, &p.Cash, &p.WorkingCapital,
			&p.InvestedCapital, &p.TangibleBookValue, &p.SharesOutstanding,
			&p.FreeCashFlow, &p.Capex, &p.OperatingCashFlow); err != nil {
			return nil, fmt.Errorf("financials: scan: %w", err)
		}
		if rd.Valid {
			t := rd.Time.UTC()
			p.ReportDate = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyFloats(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}
