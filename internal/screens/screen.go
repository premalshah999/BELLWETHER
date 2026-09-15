// Package screens turns an operator's question into a query over the last
// scan.
//
// The built-in scanner answers one fixed question -- "what is behaving
// abnormally today" -- with six hand-tuned signals and thresholds measured
// from this universe. That question is not the only one worth asking, and the
// thresholds that make it useful are exactly what make it useless for
// anything else: a screen for "quietly sitting near its high" is looking for
// instruments the scanner is built to ignore.
//
// A screen is therefore a list of conditions over the same measurements the
// scanner computes, combined with all or any, sorted, and capped. Nothing
// here is new arithmetic -- it is the existing metrics, filtered by hand.
package screens

import (
	"fmt"
	"sort"
	"strings"
)

// Field is a measurement a condition can be written about.
//
// A closed set, and the only thing permitted to reach the SQL text. Operator
// input picks a member of this map by name; the column it maps to is a
// constant written here. A field the map does not know is rejected at
// validation, so an unknown name can never travel further than this package.
type Field struct {
	Column string
	Label  string
	Unit   string
}

var fields = map[string]Field{
	"close":             {"m.close_price", "Price", "currency"},
	"return_1d":         {"m.return_1d", "1-day return", "percent"},
	"return_5d":         {"m.return_5d", "5-day return", "percent"},
	"return_z":          {"m.return_z", "Return z-score", "sigma"},
	"volume":            {"m.volume", "Volume", "shares"},
	"volume_ratio":      {"m.volume_ratio", "Volume vs median", "multiple"},
	"volume_z":          {"m.volume_z", "Volume z-score", "sigma"},
	"gap_percent":       {"m.gap_percent", "Gap from prior close", "percent"},
	"pct_from_52w_high": {"m.pct_from_52w_high", "Distance from 52w high", "percent"},
	"pct_from_52w_low":  {"m.pct_from_52w_low", "Distance from 52w low", "percent"},
	"bars":              {"m.bars", "Trading days measured", "count"},
}

// Fields is the catalogue, for the interface to build a field picker from.
//
// Sorted by name so the list does not reshuffle between requests: Go
// randomises map iteration, and a picker whose options move every time it
// opens is unusable.
func Fields() []map[string]string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]string, 0, len(names))
	for _, name := range names {
		f := fields[name]
		out = append(out, map[string]string{"field": name, "label": f.Label, "unit": f.Unit})
	}
	return out
}

// ops are the comparisons a condition can make.
//
// Same closed-set discipline as the fields, for the same reason.
var ops = map[string]string{
	">":  ">",
	">=": ">=",
	"<":  "<",
	"<=": "<=",
}

// Condition is one clause: a measurement, a comparison, a number.
type Condition struct {
	Field string  `json:"field"`
	Op    string  `json:"op"`
	Value float64 `json:"value"`
}

// Definition is a screen.
type Definition struct {
	// Match is "all" or "any". Defaults to all, because a screen with several
	// conditions almost always means the intersection, and an operator who
	// wanted the union will say so.
	Match string `json:"match"`

	Conditions []Condition `json:"conditions"`

	// Universe optionally narrows the sweep to the members of these
	// watchlists. Empty means all 750 constituents.
	WatchlistIDs []int64 `json:"watchlist_ids,omitempty"`

	SortBy   string `json:"sort_by"`
	SortDesc bool   `json:"sort_desc"`
	Limit    int    `json:"limit"`
}

// MaxConditions caps a screen's length.
//
// Not a storage limit -- it is that a screen with more than a dozen clauses is
// not a screen any more, it is a name, and picking the instrument directly is
// both easier to read and easier to be right about.
const MaxConditions = 12

// MaxLimit caps how many rows one screen returns.
const MaxLimit = 750

// FieldError names a rejected field and says why.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Validate reports every problem with a definition rather than the first.
//
// A form that surfaces one error per submission makes the operator play
// twenty questions with the validator.
func (d *Definition) Validate() []FieldError {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{field, msg}) }

	switch d.Match {
	case "":
		d.Match = "all"
	case "all", "any":
	default:
		add("match", `match must be "all" or "any"`)
	}

	switch {
	case len(d.Conditions) == 0:
		add("conditions", "name at least one condition")
	case len(d.Conditions) > MaxConditions:
		add("conditions", fmt.Sprintf("a screen takes at most %d conditions", MaxConditions))
	}

	for i, c := range d.Conditions {
		where := fmt.Sprintf("conditions[%d]", i)
		if _, ok := fields[c.Field]; !ok {
			add(where+".field", fmt.Sprintf("%q is not a measurement this scanner computes", c.Field))
		}
		if _, ok := ops[c.Op]; !ok {
			add(where+".op", fmt.Sprintf("%q is not a comparison", c.Op))
		}
		// NaN and infinity survive JSON round-trips through some encoders and
		// compare false against everything, which would silently return an
		// empty screen rather than an error.
		if c.Value != c.Value || c.Value > 1e308 || c.Value < -1e308 {
			add(where+".value", "value must be a real number")
		}
	}

	if d.SortBy == "" {
		d.SortBy = "volume_z"
	}
	if _, ok := fields[d.SortBy]; !ok {
		add("sort_by", fmt.Sprintf("%q is not a measurement this scanner computes", d.SortBy))
	}

	switch {
	case d.Limit == 0:
		d.Limit = 50
	case d.Limit < 0:
		add("limit", "limit cannot be negative")
	case d.Limit > MaxLimit:
		d.Limit = MaxLimit
	}
	return errs
}

// SQL renders the definition as a query over the newest scan.
//
// Every fragment that reaches the statement text is a constant from the maps
// above; every operator-supplied number is a placeholder. The definition has
// been through Validate by the time this runs, so an unknown field here would
// be a programming error rather than untrusted input -- it still cannot reach
// the string, because the lookup simply fails and the clause is dropped.
func (d *Definition) SQL() (string, []any) {
	var (
		clauses []string
		args    []any
	)
	for _, c := range d.Conditions {
		f, ok := fields[c.Field]
		if !ok {
			continue
		}
		op, ok := ops[c.Op]
		if !ok {
			continue
		}
		args = append(args, c.Value)
		clauses = append(clauses, fmt.Sprintf("%s %s $%d", f.Column, op, len(args)))
	}

	join := " AND "
	if d.Match == "any" {
		join = " OR "
	}
	where := "TRUE"
	if len(clauses) > 0 {
		where = "(" + strings.Join(clauses, join) + ")"
	}

	// Restricting to watchlists is a second, independent filter: it narrows
	// which instruments are considered, never which conditions apply. So it
	// is always ANDed on, even when the conditions themselves are ORed.
	if len(d.WatchlistIDs) > 0 {
		// One placeholder per id rather than an array parameter: passing a
		// slice would mean importing the driver's array type into a package
		// that otherwise knows nothing about the database, and the list is
		// at most five long.
		marks := make([]string, 0, len(d.WatchlistIDs))
		for _, id := range d.WatchlistIDs {
			args = append(args, id)
			marks = append(marks, fmt.Sprintf("$%d", len(args)))
		}
		// Watchlists store fully qualified symbols (RELIANCE.NSE) and the
		// scanner stores bare NSE tickers, so the comparison has to strip the
		// suffix or nothing ever matches.
		where += fmt.Sprintf(`
			AND m.symbol IN (
				SELECT split_part(i.symbol, '.', 1)
				FROM watchlist_items i
				WHERE i.watchlist_id IN (%s)
			)`, strings.Join(marks, ","))
	}

	dir := "ASC"
	if d.SortDesc {
		dir = "DESC"
	}
	args = append(args, d.Limit)

	// Ties break on symbol so a screen run twice in a row returns the same
	// order. Without it Postgres is free to reshuffle equal scores, and a
	// list that reorders on refresh reads as though the market moved.
	q := fmt.Sprintf(`
		SELECT m.symbol, c.industry,
		       m.close_price, m.return_1d, m.return_5d, m.return_z,
		       m.volume, m.volume_ratio, m.volume_z, m.gap_percent,
		       m.pct_from_52w_high, m.pct_from_52w_low, m.bars,
		       COALESCE(f.signals, '{}') AS signals
		FROM scan_metrics m
		LEFT JOIN index_constituents c ON c.symbol = m.symbol
		LEFT JOIN scan_findings f ON f.scan_id = m.scan_id AND f.symbol = m.symbol
		WHERE m.scan_id = (SELECT id FROM scans ORDER BY scanned_at DESC LIMIT 1)
		  AND %s
		ORDER BY %s %s NULLS LAST, m.symbol ASC
		LIMIT $%d`,
		where, fields[d.SortBy].Column, dir, len(args))
	return q, args
}
