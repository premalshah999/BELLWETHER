package fundamentals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// vendorTicker mirrors scanner.vendorTicker: the price sidecar's dialect for
// a canonical symbol. Duplicated rather than shared because the two packages
// must not import each other, and the mapping is three lines.
func vendorTicker(sym marketdata.Symbol) (vendor string, ok bool) {
	switch sym.Exchange {
	case marketdata.ExchangeUS:
		return sym.Ticker, true
	case marketdata.ExchangeNSE:
		return sym.Ticker + ".NS", true
	case marketdata.ExchangeBSE:
		return sym.Ticker + ".BO", true
	default:
		return "", false
	}
}

// Client fetches fundamentals from the price sidecar.
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

// batchSize is how many symbols one request carries.
//
// Small because fundamentals are expensive per symbol — each is several
// upstream calls for statements and ratios, measured at roughly five seconds —
// where a price scan is one bulk download. The sidecar refuses more than forty.
const batchSize = 20

type wireResponse struct {
	Fundamentals map[string]wireCompany `json:"fundamentals"`
	Failed       []string               `json:"failed"`
	Elapsed      float64                `json:"elapsed_seconds"`
}

type wireCompany struct {
	Symbol  string         `json:"symbol"`
	AsOf    string         `json:"as_of"`
	Info    map[string]any `json:"info"`
	Periods []wirePeriod   `json:"periods"`
}

type wirePeriod struct {
	PeriodEnd  string             `json:"period_end"`
	PeriodType string             `json:"period_type"`
	ReportDate string             `json:"report_date"`
	Metrics    map[string]float64 `json:"metrics"`
	Raw        map[string]float64 `json:"raw"`
}

// Company is everything known about one company's finances.
type Company struct {
	Snapshot Snapshot
	Periods  []Period
	// RawInfo and RawPeriods keep what had no column, so a metric nobody
	// wanted today is recoverable without refetching a year of history.
	RawInfo    map[string]any
	RawPeriods map[string]map[string]float64
}

// Fetch retrieves fundamentals for a set of canonical, venue-qualified
// symbols. The returned map and failed list are keyed by that same canonical
// form, not by the vendor's dialect.
func (c *Client) Fetch(ctx context.Context, symbols []marketdata.Symbol) (map[string]Company, []string, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return nil, nil, fmt.Errorf("fundamentals: no price service configured")
	}
	out := make(map[string]Company, len(symbols))
	var failed []string

	for start := 0; start < len(symbols); start += batchSize {
		end := start + batchSize
		if end > len(symbols) {
			end = len(symbols)
		}
		batch := symbols[start:end]

		suffixed := make([]string, 0, len(batch))
		canonicalOf := make(map[string]string, len(batch))
		for _, sym := range batch {
			vendor, ok := vendorTicker(sym)
			if !ok {
				continue
			}
			suffixed = append(suffixed, vendor)
			canonicalOf[vendor] = sym.String()
		}
		if len(suffixed) == 0 {
			continue
		}

		got, bad, err := c.fetchBatch(ctx, suffixed, canonicalOf)
		if err != nil {
			return out, failed, err
		}
		for k, v := range got {
			out[k] = v
		}
		failed = append(failed, bad...)
	}
	return out, failed, nil
}

func (c *Client) fetchBatch(ctx context.Context, symbols []string, canonicalOf map[string]string) (map[string]Company, []string, error) {
	body, err := json.Marshal(map[string]any{"symbols": symbols})
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/fundamentals", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fundamentals: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("fundamentals: price service returned %s", resp.Status)
	}

	var raw wireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("fundamentals: decode: %w", err)
	}

	out := make(map[string]Company, len(raw.Fundamentals))
	for vendorSym, wc := range raw.Fundamentals {
		canonical, ok := canonicalOf[vendorSym]
		if !ok {
			// The sidecar echoed a symbol we never asked for; do not guess
			// its venue.
			continue
		}
		out[canonical] = buildCompany(canonical, wc)
	}
	failed := make([]string, 0, len(raw.Failed))
	for _, f := range raw.Failed {
		if canonical, ok := canonicalOf[f]; ok {
			failed = append(failed, canonical)
		}
	}
	return out, failed, nil
}

func buildCompany(ticker string, wc wireCompany) Company {
	c := Company{
		Snapshot:   Snapshot{Symbol: ticker, AsOf: parseTime(wc.AsOf)},
		RawInfo:    wc.Info,
		RawPeriods: map[string]map[string]float64{},
	}
	if c.Snapshot.AsOf.IsZero() {
		c.Snapshot.AsOf = time.Now().UTC()
	}

	s := &c.Snapshot
	s.Sector, _ = wc.Info["sector"].(string)
	s.Industry, _ = wc.Info["industry"].(string)
	s.QuoteCurrency, _ = wc.Info["currency"].(string)
	s.FinancialCurrency, _ = wc.Info["financialCurrency"].(string)

	num := func(key string) *float64 {
		v, ok := wc.Info[key]
		if !ok {
			return nil
		}
		f, ok := v.(float64)
		if !ok {
			return nil
		}
		return &f
	}
	s.PETrailing = num("trailingPE")
	s.PEForward = num("forwardPE")
	s.PriceToBook = num("priceToBook")
	s.MarketCap = num("marketCap")
	s.EnterpriseValue = num("enterpriseValue")
	s.EVToEBITDA = num("enterpriseToEbitda")
	s.EVToRevenue = num("enterpriseToRevenue")
	s.PEGRatio = num("pegRatio")
	s.ReturnOnEquity = num("returnOnEquity")
	s.ReturnOnAssets = num("returnOnAssets")
	s.ProfitMargin = num("profitMargins")
	s.OperatingMargin = num("operatingMargins")
	s.GrossMargin = num("grossMargins")
	s.EBITDAMargin = num("ebitdaMargins")
	s.DebtToEquity = num("debtToEquity")
	s.CurrentRatio = num("currentRatio")
	s.QuickRatio = num("quickRatio")
	s.DividendYield = num("dividendYield")
	s.PayoutRatio = num("payoutRatio")
	s.EPSTrailing = num("trailingEps")
	s.EPSForward = num("forwardEps")
	s.BookValue = num("bookValue")
	s.SharesOutstanding = num("sharesOutstanding")
	s.RevenueGrowth = num("revenueGrowth")
	s.EarningsGrowth = num("earningsGrowth")
	s.Beta = num("beta")

	// Periods are parsed first because the market-cap derivation below may
	// need a share count that only the balance sheet carries.
	parsePeriods(&c, ticker, wc)

	sanitise(s)
	dropMixedCurrencyRatios(s)

	// Market cap is frequently absent for Indian listings. Derived rather
	// than left empty, because it is the denominator of every yield and a
	// screen with holes in it is one an investor stops trusting.
	//
	// Book value is per share and in the quote currency, so shares times book
	// value is equity and multiplying by price-to-book gives the market's
	// valuation of it. Checked against Reliance: 13.53bn shares at ₹668.05
	// reproduces its reported equity of ₹9.04 lakh crore exactly, and ₹17.6
	// lakh crore of market capitalisation against a real figure of about
	// ₹17.8 lakh crore.
	if s.MarketCap == nil && s.BookValue != nil && s.PriceToBook != nil {
		shares := s.SharesOutstanding
		if shares == nil {
			// The ratio block omits the share count for some listings while
			// the balance sheet carries it. Of 750 companies, 17 had no
			// market capitalisation and every one of them had shares here.
			shares = latestShares(c.Periods)
		}
		if shares != nil {
			mc := *shares * *s.BookValue * *s.PriceToBook
			if mc > 0 {
				s.MarketCap = &mc
			}
		}
	}
	// Likewise return on equity, which is EPS over book value per share when
	// both are reported and the provider's own figure is not.
	if s.ReturnOnEquity == nil && s.EPSTrailing != nil && s.BookValue != nil && *s.BookValue > 0 {
		roe := *s.EPSTrailing / *s.BookValue
		s.ReturnOnEquity = &roe
	}

	return c
}

// parsePeriods fills in the reported financial periods.
func parsePeriods(c *Company, ticker string, wc wireCompany) {
	for _, wp := range wc.Periods {
		p := Period{
			Symbol:     ticker,
			PeriodEnd:  wp.PeriodEnd,
			PeriodType: wp.PeriodType,
			// Statements carry the filing currency, which is not necessarily
			// the currency the share trades in.
			Currency: c.Snapshot.FinancialCurrency,
		}
		if wp.ReportDate != "" {
			if t := parseTime(wp.ReportDate); !t.IsZero() {
				p.ReportDate = &t
			}
		}
		m := func(key string) *float64 {
			v, ok := wp.Metrics[key]
			if !ok {
				return nil
			}
			return &v
		}
		p.Revenue = m("revenue")
		p.GrossProfit = m("gross_profit")
		p.OperatingIncome = m("operating_income")
		p.EBITDA = m("ebitda")
		p.EBIT = m("ebit")
		p.NetIncome = m("net_income")
		p.EPSBasic = m("eps_basic")
		p.EPSDiluted = m("eps_diluted")
		p.InterestExpense = m("interest_expense")
		p.TaxProvision = m("tax_provision")
		p.TotalExpenses = m("total_expenses")
		p.TotalAssets = m("total_assets")
		p.TotalDebt = m("total_debt")
		p.NetDebt = m("net_debt")
		p.Equity = m("equity")
		p.Cash = m("cash")
		p.WorkingCapital = m("working_capital")
		p.InvestedCapital = m("invested_capital")
		p.TangibleBookValue = m("tangible_book_value")
		p.SharesOutstanding = m("shares_outstanding")
		p.FreeCashFlow = m("free_cash_flow")
		p.Capex = m("capex")
		p.OperatingCashFlow = m("operating_cash_flow")

		c.Periods = append(c.Periods, p)
		c.RawPeriods[wp.PeriodType+"|"+wp.PeriodEnd] = wp.Raw
	}
}

// plausible bounds for provider ratios.
//
// The upstream data for Indian listings contains figures that are not merely
// imprecise but impossible, and they arrive with no error attached. Measured
// across 66 companies, the provider's EV/EBITDA ranged from -141 to 1257
// against a median of 17.8: for Infosys it reported EBITDA of about ₹448
// crore, roughly a hundredth of the real figure, giving a multiple of 1011.
//
// A screen showing that is worse than a screen showing nothing, because the
// reader has no way to tell which numbers to trust and will discount all of
// them. So an implausible value is discarded and reported as absent, which is
// the honest description of what we know.
//
// The bounds are deliberately wide. They are here to catch numbers that cannot
// be right, not to enforce a view about what is expensive.
var ratioBounds = map[string]struct{ lo, hi float64 }{
	"pe":             {0, 500},
	"pb":             {0, 100},
	"ev_ebitda":      {0, 200},
	"debt_to_equity": {0, 2000},
	"roe":            {-10, 10},
	"margin":         {-10, 10},
	"yield":          {0, 100},
}

func clampRatio(v *float64, kind string) *float64 {
	if v == nil {
		return nil
	}
	b, ok := ratioBounds[kind]
	if !ok {
		return v
	}
	if *v < b.lo || *v > b.hi || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	return v
}

// dropMixedCurrencyRatios removes the ratios that combine a market figure
// with a statement figure when the two are in different currencies.
//
// The provider makes this mistake itself, so the bad values arrive already
// computed. Within its `info` block the per-share and price-derived figures
// are in the quote currency while the absolute aggregates come from the
// statements: for Infosys, EPS of 77.56 and book value of 226.41 are rupees
// and correct, while revenue of 20.3bn and EBITDA of 4.48bn are dollars.
//
// So P/E, P/B and dividend yield are sound, and so are the pure statement
// ratios like margins and return on equity, because both of their terms come
// from the same filing. It is only the enterprise-value multiples that mix
// the two — and those were wrong by a factor of about 85, which the
// plausibility bounds happened to catch for Infosys and would not catch for a
// company whose currencies are closer in magnitude.
func dropMixedCurrencyRatios(s *Snapshot) {
	if !s.MixedCurrency() {
		return
	}
	s.EVToEBITDA = nil
	s.EVToRevenue = nil
}

// sanitise drops provider figures that cannot be true.
func sanitise(s *Snapshot) {
	s.PETrailing = clampRatio(s.PETrailing, "pe")
	s.PEForward = clampRatio(s.PEForward, "pe")
	s.PriceToBook = clampRatio(s.PriceToBook, "pb")
	s.EVToEBITDA = clampRatio(s.EVToEBITDA, "ev_ebitda")
	s.EVToRevenue = clampRatio(s.EVToRevenue, "ev_ebitda")
	s.PEGRatio = clampRatio(s.PEGRatio, "pe")
	s.DebtToEquity = clampRatio(s.DebtToEquity, "debt_to_equity")
	s.ReturnOnEquity = clampRatio(s.ReturnOnEquity, "roe")
	s.ReturnOnAssets = clampRatio(s.ReturnOnAssets, "roe")
	s.ProfitMargin = clampRatio(s.ProfitMargin, "margin")
	s.OperatingMargin = clampRatio(s.OperatingMargin, "margin")
	s.GrossMargin = clampRatio(s.GrossMargin, "margin")
	s.EBITDAMargin = clampRatio(s.EBITDAMargin, "margin")
	s.DividendYield = clampRatio(s.DividendYield, "yield")
}

// latestShares returns the most recent reported share count.
//
// Periods arrive newest first from the sidecar, but that is the sidecar's
// ordering rather than a guarantee, so the newest is chosen explicitly.
func latestShares(periods []Period) *float64 {
	var best *float64
	var bestEnd string
	for i := range periods {
		p := periods[i]
		if p.SharesOutstanding == nil || *p.SharesOutstanding <= 0 {
			continue
		}
		if best == nil || p.PeriodEnd > bestEnd {
			best, bestEnd = p.SharesOutstanding, p.PeriodEnd
		}
	}
	return best
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
