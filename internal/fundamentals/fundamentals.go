// Package fundamentals holds what a company is worth and how it is trading,
// as distinct from what is being said about it.
//
// The rest of this system is built around news: what happened, when we learned
// it, what it might mean. That answers "what is going on" and cannot answer
// "is this expensive", which is the question an investor asks before acting on
// anything. A stock can have excellent news flow and be priced for perfection;
// a stock can be ignored and be cheap. Neither is visible from an event feed.
package fundamentals

import (
	"math"
	"time"
)

// Snapshot is the valuation and quality picture at one moment.
//
// Every ratio is a pointer because absent and zero are different facts. A
// loss-making company has no meaningful P/E, and a zero there would sort it to
// the top of any screen for cheapness — which is exactly backwards.
type Snapshot struct {
	Symbol   string    `json:"symbol"`
	AsOf     time.Time `json:"as_of"`
	Sector   string    `json:"sector,omitempty"`
	Industry string    `json:"industry,omitempty"`

	// QuoteCurrency is what the share trades in; FinancialCurrency is what
	// the statements are filed in. They differ for some Indian companies —
	// Infosys files in USD and trades in INR — and any ratio built from one
	// of each is meaningless unless they match.
	QuoteCurrency     string `json:"quote_currency,omitempty"`
	FinancialCurrency string `json:"financial_currency,omitempty"`

	PETrailing      *float64 `json:"pe_trailing,omitempty"`
	PEForward       *float64 `json:"pe_forward,omitempty"`
	PriceToBook     *float64 `json:"price_to_book,omitempty"`
	MarketCap       *float64 `json:"market_cap,omitempty"`
	EnterpriseValue *float64 `json:"enterprise_value,omitempty"`
	EVToEBITDA      *float64 `json:"ev_to_ebitda,omitempty"`
	EVToRevenue     *float64 `json:"ev_to_revenue,omitempty"`
	PEGRatio        *float64 `json:"peg_ratio,omitempty"`

	ReturnOnEquity  *float64 `json:"return_on_equity,omitempty"`
	ReturnOnAssets  *float64 `json:"return_on_assets,omitempty"`
	ProfitMargin    *float64 `json:"profit_margin,omitempty"`
	OperatingMargin *float64 `json:"operating_margin,omitempty"`
	GrossMargin     *float64 `json:"gross_margin,omitempty"`
	EBITDAMargin    *float64 `json:"ebitda_margin,omitempty"`

	DebtToEquity  *float64 `json:"debt_to_equity,omitempty"`
	CurrentRatio  *float64 `json:"current_ratio,omitempty"`
	QuickRatio    *float64 `json:"quick_ratio,omitempty"`
	DividendYield *float64 `json:"dividend_yield,omitempty"`
	PayoutRatio   *float64 `json:"payout_ratio,omitempty"`

	EPSTrailing       *float64 `json:"eps_trailing,omitempty"`
	EPSForward        *float64 `json:"eps_forward,omitempty"`
	BookValue         *float64 `json:"book_value,omitempty"`
	SharesOutstanding *float64 `json:"shares_outstanding,omitempty"`

	RevenueGrowth  *float64 `json:"revenue_growth,omitempty"`
	EarningsGrowth *float64 `json:"earnings_growth,omitempty"`
	Beta           *float64 `json:"beta,omitempty"`
}

// MixedCurrency reports that statement figures and market figures are in
// different currencies, so ratios combining them cannot be computed.
//
// Returns false when either is unknown: an unknown currency is a reason to be
// careful, but refusing every ratio for every company whose provider omitted
// the field would remove most of the data over a field that is usually
// present and usually matches.
func (s Snapshot) MixedCurrency() bool {
	if s.QuoteCurrency == "" || s.FinancialCurrency == "" {
		return false
	}
	return s.QuoteCurrency != s.FinancialCurrency
}

// Period is one reported financial period.
type Period struct {
	Symbol     string `json:"symbol"`
	PeriodEnd  string `json:"period_end"`
	PeriodType string `json:"period_type"`

	// ReportDate is when these figures became public.
	//
	// Currency is what these figures are denominated in.
	Currency string `json:"currency,omitempty"`

	// Nil means unknown, which is a different thing from the period end and
	// must never be silently replaced by it. An Indian company reports six to
	// eight weeks after a quarter closes; anything that reads the period end as
	// the moment the numbers existed is looking into the future.
	ReportDate *time.Time `json:"report_date,omitempty"`

	Revenue         *float64 `json:"revenue,omitempty"`
	GrossProfit     *float64 `json:"gross_profit,omitempty"`
	OperatingIncome *float64 `json:"operating_income,omitempty"`
	EBITDA          *float64 `json:"ebitda,omitempty"`
	EBIT            *float64 `json:"ebit,omitempty"`
	NetIncome       *float64 `json:"net_income,omitempty"`
	EPSBasic        *float64 `json:"eps_basic,omitempty"`
	EPSDiluted      *float64 `json:"eps_diluted,omitempty"`
	InterestExpense *float64 `json:"interest_expense,omitempty"`
	TaxProvision    *float64 `json:"tax_provision,omitempty"`
	TotalExpenses   *float64 `json:"total_expenses,omitempty"`

	TotalAssets       *float64 `json:"total_assets,omitempty"`
	TotalDebt         *float64 `json:"total_debt,omitempty"`
	NetDebt           *float64 `json:"net_debt,omitempty"`
	Equity            *float64 `json:"equity,omitempty"`
	Cash              *float64 `json:"cash,omitempty"`
	WorkingCapital    *float64 `json:"working_capital,omitempty"`
	InvestedCapital   *float64 `json:"invested_capital,omitempty"`
	TangibleBookValue *float64 `json:"tangible_book_value,omitempty"`
	SharesOutstanding *float64 `json:"shares_outstanding,omitempty"`

	FreeCashFlow      *float64 `json:"free_cash_flow,omitempty"`
	Capex             *float64 `json:"capex,omitempty"`
	OperatingCashFlow *float64 `json:"operating_cash_flow,omitempty"`
}

// Margin computes a margin from two optional figures.
//
// Returns nil rather than zero when either side is missing or revenue is zero,
// so a company with no reported revenue does not appear to have a margin of
// exactly nothing.
func Margin(numerator, revenue *float64) *float64 {
	if numerator == nil || revenue == nil || *revenue == 0 {
		return nil
	}
	m := *numerator / *revenue * 100
	if math.IsNaN(m) || math.IsInf(m, 0) {
		return nil
	}
	return &m
}

// Trend describes how a metric has moved across reported periods.
type Trend struct {
	Metric string    `json:"metric"`
	Points []float64 `json:"points"`
	// From and To are the periods the trend actually spans, which is not
	// necessarily every period requested. Providers report some quarters
	// sparsely, so a "last eight quarters" request can legitimately produce a
	// five-point trend — and a reader shown five points labelled as eight
	// quarters would draw the wrong conclusion about the gradient.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Gaps counts periods in range that did not report this metric.
	Gaps int `json:"gaps,omitempty"`
	// ChangePercent is first to last, when that is meaningful.
	ChangePercent *float64 `json:"change_percent,omitempty"`
	// Direction is the plain-language reading: improving, deteriorating, or
	// flat. Computed rather than left to the reader because the whole point of
	// a trend is the direction, and eight numbers do not state one.
	//
	// "Improving" means good for an owner, not "went up". Debt rising by a
	// third is not an improvement, and labelling it one — which this did until
	// the direction of each metric was made explicit — inverts the meaning of
	// the single word the reader is most likely to act on.
	Direction string `json:"direction"`
	// Rising records what actually happened to the number, separately from
	// whether that is good. Both are worth stating: a reader scanning for
	// "deteriorating" still wants to know which way the line went.
	Rising bool `json:"rising"`
}

// flatBand is how much a metric may drift and still count as flat, in percent.
//
// Margins wobble by a few tenths of a point every quarter for reasons nobody
// can attribute. Calling that "deteriorating" turns ordinary noise into a
// finding, which is worse than saying nothing.
const flatBand = 5.0

// minTrendPoints is the fewest reported periods a trend may rest on.
//
// Two points is a line between two numbers, and one weak quarter would read as
// a collapse. Four is enough that a direction means something.
const minTrendPoints = 4

// BuildTrend summarises a series of period values, oldest first.
//
// higherIsBetter says which way is favourable for this metric. Revenue and
// margins improve by rising; debt improves by falling.
func BuildTrend(metric string, points []float64, higherIsBetter bool) *Trend {
	if len(points) < minTrendPoints {
		return nil
	}
	t := &Trend{Metric: metric, Points: points, Direction: "flat"}
	first, last := points[0], points[len(points)-1]
	if first > 0 {
		c := (last/first - 1) * 100
		if !math.IsNaN(c) && !math.IsInf(c, 0) {
			t.ChangePercent = &c
			t.Rising = c > 0
			if math.Abs(c) > flatBand {
				improving := c > 0
				if !higherIsBetter {
					improving = c < 0
				}
				if improving {
					t.Direction = "improving"
				} else {
					t.Direction = "deteriorating"
				}
			}
		}
	}
	return t
}
