package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// Peer comparison.
//
// A P/E of 23 is not information. A P/E of 23 against an industry median of 31,
// in the cheapest quartile of its peers, with better margins than most of them
// — that is information, and it is the question an investor actually has.
//
// Peers come from the NSE industry classification already embedded in the
// company master, so the grouping is the exchange's own rather than something
// invented here.

// PeerStat is one metric compared against a peer group.
type PeerStat struct {
	Metric string   `json:"metric"`
	Value  *float64 `json:"value,omitempty"`
	Median *float64 `json:"median,omitempty"`
	// Percentile is where this company sits among peers that report the
	// metric, 0 being the lowest value and 100 the highest.
	Percentile *float64 `json:"percentile,omitempty"`
	// Peers is how many companies the comparison rests on. Reported because a
	// percentile against three peers is not a percentile, and the reader
	// cannot otherwise tell.
	Peers int `json:"peers"`
	// HigherIsBetter records which direction is favourable, so a caller can
	// render "cheap" and "profitable" without hardcoding a metric list.
	HigherIsBetter bool `json:"higher_is_better"`
	// Unit is how the number should be read: "ratio" for a multiple such as
	// P/E, "percent" for a figure already expressed in percent, and
	// "fraction" for one that must be multiplied by a hundred first.
	//
	// Carried explicitly because the provider is not consistent and there is
	// no way to infer it. Return on equity arrives as 0.32 meaning 32 per
	// cent, while dividend yield arrives as 4.37 meaning 4.37 per cent.
	// Scaling everything by a hundred turns Infosys's yield into 437 per
	// cent; scaling nothing reports its return on equity as 0.32 per cent.
	// Both are wrong in ways a reader would not catch.
	Unit string `json:"unit"`
}

// PeerComparison is a company set against its industry.
type PeerComparison struct {
	Symbol   string     `json:"symbol"`
	Industry string     `json:"industry"`
	Peers    int        `json:"peer_count"`
	Stats    []PeerStat `json:"stats"`
}

// peerMetrics are the columns worth comparing, and which way is good.
//
// Deliberately short. A comparison across thirty metrics is a spreadsheet, and
// the reader has to work out which ones matter; these are the ones that decide
// whether something is cheap, whether it earns its capital, and whether it can
// survive its debt.
var peerMetrics = []struct {
	column         string
	label          string
	higherIsBetter bool
	unit           string
}{
	{"pe_trailing", "P/E", false, "ratio"},
	{"price_to_book", "P/B", false, "ratio"},
	{"ev_to_ebitda", "EV/EBITDA", false, "ratio"},
	{"return_on_equity", "Return on equity", true, "fraction"},
	{"operating_margin", "Operating margin", true, "fraction"},
	{"profit_margin", "Net margin", true, "fraction"},
	{"revenue_growth", "Revenue growth", true, "fraction"},
	{"debt_to_equity", "Debt to equity", false, "ratio"},
	{"dividend_yield", "Dividend yield", true, "percent"},
	// Free cash flow against what the market pays for the company.
	//
	// Derived rather than taken from the provider, and taken from the annual
	// statements rather than the quarterly ones: measured across this data,
	// 380 of 474 annual periods report cash flow and only 21 of 555 quarterly
	// ones do. Quarterly cash flow simply is not published for most Indian
	// listings, and computing a yield from the handful that have it would
	// rank twenty companies against each other and call it a sector.
	{"fcf_yield", "FCF yield", true, "fraction"},
}

// minPeersForPercentile is how many companies must report a metric before a
// percentile is offered.
//
// Below this the figure is arithmetic over a handful of companies presented
// with two significant digits, which reads as far more precise than it is.
// Five also puts the divisor safely above zero.
const minPeersForPercentile = 5

// ComparePeers ranks one company against the other listed companies in its
// NSE industry.
func (d *DB) ComparePeers(ctx context.Context, symbol string) (PeerComparison, error) {
	var industry, taxonomy string
	err := d.db.QueryRowContext(ctx,
		`SELECT industry, COALESCE(taxonomy, '') FROM index_constituents WHERE symbol = $1`,
		symbol).Scan(&industry, &taxonomy)
	if err == sql.ErrNoRows || industry == "" {
		return PeerComparison{}, fmt.Errorf("compare peers: %s has no industry classification", symbol)
	}
	if err != nil {
		return PeerComparison{}, fmt.Errorf("compare peers: %w", err)
	}

	out := PeerComparison{Symbol: symbol, Industry: industry}

	// The latest snapshot per symbol in this industry. DISTINCT ON is the
	// cheap way to say "most recent row per group" in Postgres, and the
	// snapshot table is append-only so there is always more than one.
	// The latest ratio snapshot per symbol, widened with the most recent
	// annual free cash flow so that a yield can be computed against a market
	// capitalisation that moves daily. The two live in different tables
	// because they change on different clocks; they are joined here rather
	// than stored together for the same reason.
	//
	// Scoped by taxonomy as well as industry label: NSE's classification and
	// GICS both use ordinary English sector names, and "Information
	// Technology" means one thing in each. Matching on the label alone would
	// silently pool an NSE company's peer group with US ones any time the
	// two taxonomies happened to spell a sector the same way.
	const base = `
WITH snapshots AS (
    SELECT DISTINCT ON (f.symbol) f.*
    FROM fundamentals_snapshot f
    JOIN index_constituents ic ON ic.symbol = f.symbol
    WHERE ic.industry = $1 AND ic.taxonomy = $2
    ORDER BY f.symbol, f.id DESC
),
fcf AS (
    SELECT DISTINCT ON (symbol) symbol, free_cash_flow, currency
    FROM financials
    WHERE period_type = 'annual' AND free_cash_flow IS NOT NULL
    ORDER BY symbol, period_end DESC
),
latest AS (
    SELECT l.*,
           CASE
               WHEN l.market_cap IS NULL OR l.market_cap <= 0 THEN NULL
               -- A statement figure over a market figure is only a ratio if
               -- both are in the same money. Infosys files in dollars and
               -- trades in rupees, which turned a roughly 5% free cash flow
               -- yield into 0.1% — a number that looked entirely ordinary.
               WHEN fcf.currency <> '' AND l.quote_currency <> ''
                    AND fcf.currency <> l.quote_currency THEN NULL
               ELSE fcf.free_cash_flow / l.market_cap
           END AS fcf_yield
    FROM snapshots l
    LEFT JOIN fcf ON fcf.symbol = l.symbol
)`

	// $2 (taxonomy) must actually appear in base's own text for Postgres to
	// infer its type -- a positional gap (passing a dummy value for an
	// unreferenced placeholder) fails with "could not determine data type
	// of parameter $n" rather than being silently ignored, which is why
	// industry and taxonomy are $1/$2 here and symbol is pushed to $3 below
	// instead of sitting in the middle.
	if err := d.db.QueryRowContext(ctx, base+` SELECT count(*) FROM latest`, industry, taxonomy).
		Scan(&out.Peers); err != nil {
		return out, fmt.Errorf("compare peers: count: %w", err)
	}

	for _, m := range peerMetrics {
		var st PeerStat
		st.Metric = m.label
		st.HigherIsBetter = m.higherIsBetter
		st.Unit = m.unit

		// Percentile is computed only over peers that actually report the
		// metric. Counting a company that does not report a P/E as though it
		// had one would move everyone else's rank for no reason.
		q := fmt.Sprintf(`%s
SELECT
    (SELECT %[2]s FROM latest WHERE symbol = $3),
    (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY %[2]s) FROM latest WHERE %[2]s IS NOT NULL),
    (SELECT count(*) FROM latest WHERE %[2]s IS NOT NULL),
    (SELECT count(*) FROM latest WHERE %[2]s IS NOT NULL
        AND %[2]s < (SELECT %[2]s FROM latest WHERE symbol = $3))`, base, m.column)

		var value, median sql.NullFloat64
		var reporting, below int
		if err := d.db.QueryRowContext(ctx, q, industry, taxonomy, symbol).
			Scan(&value, &median, &reporting, &below); err != nil {
			return out, fmt.Errorf("compare peers: %s: %w", m.label, err)
		}
		if value.Valid {
			v := value.Float64
			st.Value = &v
		}
		if median.Valid {
			v := median.Float64
			st.Median = &v
		}
		st.Peers = reporting
		// A percentile needs both a value to place and enough peers to place
		// it among. Five is the floor: below that the answer is arithmetic on
		// a handful of companies and reads as far more precise than it is.
		if value.Valid && reporting >= minPeersForPercentile {
			// Rank among peers that report the metric, where the lowest value
			// scores 0 and the highest 100. The company itself is one of the
			// `reporting` rows, so the divisor excludes it. Ties share a
			// percentile, which is the correct reading of a tie.
			p := float64(below) / float64(reporting-1) * 100
			st.Percentile = &p
		}
		out.Stats = append(out.Stats, st)
	}
	return out, nil
}
