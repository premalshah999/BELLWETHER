package screens

import (
	"strings"
	"testing"
)

// A field name is chosen from a closed map, never concatenated. This is the
// property the whole package rests on, so it is worth an explicit test rather
// than trusting a reading of SQL().
func TestUnknownFieldIsRejected(t *testing.T) {
	for _, name := range []string{
		"close; DROP TABLE scans--",
		"m.close_price",
		"1=1",
		"",
	} {
		d := Definition{Conditions: []Condition{{Field: name, Op: ">", Value: 1}}}
		errs := d.Validate()
		if len(errs) == 0 {
			t.Errorf("field %q was accepted", name)
		}
	}
}

func TestUnknownOperatorIsRejected(t *testing.T) {
	for _, op := range []string{"=", "<>", "LIKE", "; --", ""} {
		d := Definition{Conditions: []Condition{{Field: "close", Op: op, Value: 1}}}
		if errs := d.Validate(); len(errs) == 0 {
			t.Errorf("operator %q was accepted", op)
		}
	}
}

// Every value must arrive as a placeholder, so the rendered statement should
// contain no digits from the operator's numbers at all.
func TestValuesAreParameterised(t *testing.T) {
	d := Definition{
		Conditions: []Condition{
			{Field: "return_z", Op: "<=", Value: -2.5},
			{Field: "volume_ratio", Op: ">=", Value: 314159},
		},
		SortBy: "volume_z", SortDesc: true, Limit: 25,
	}
	if errs := d.Validate(); len(errs) > 0 {
		t.Fatalf("valid definition rejected: %v", errs)
	}
	q, args := d.SQL()
	if strings.Contains(q, "314159") || strings.Contains(q, "2.5") {
		t.Errorf("a value was interpolated into the statement:\n%s", q)
	}
	if len(args) != 3 { // two conditions plus the limit
		t.Errorf("got %d args, want 3: %v", len(args), args)
	}
	if args[0] != -2.5 || args[1] != float64(314159) || args[2] != 25 {
		t.Errorf("args in the wrong order: %v", args)
	}
}

func TestMatchAnyOrsConditionsButAndsTheWatchlist(t *testing.T) {
	d := Definition{
		Match: "any",
		Conditions: []Condition{
			{Field: "return_z", Op: ">", Value: 2},
			{Field: "gap_percent", Op: ">", Value: 3},
		},
		WatchlistIDs: []int64{2, 3},
	}
	if errs := d.Validate(); len(errs) > 0 {
		t.Fatalf("valid definition rejected: %v", errs)
	}
	q, args := d.SQL()
	if !strings.Contains(q, "OR") {
		t.Error(`match "any" did not OR the conditions`)
	}
	// The list restriction narrows the universe; it must not become one more
	// alternative that lets non-matching instruments through.
	if !strings.Contains(q, "AND m.symbol IN") {
		t.Errorf("watchlist restriction was not ANDed on:\n%s", q)
	}
	if len(args) != 5 { // two conditions, two watchlists, the limit
		t.Errorf("got %d args, want 5: %v", len(args), args)
	}
}

func TestDefaultsAreFilledIn(t *testing.T) {
	d := Definition{Conditions: []Condition{{Field: "close", Op: ">", Value: 100}}}
	if errs := d.Validate(); len(errs) > 0 {
		t.Fatalf("valid definition rejected: %v", errs)
	}
	if d.Match != "all" {
		t.Errorf("match = %q, want all", d.Match)
	}
	if d.SortBy != "volume_z" || d.Limit != 50 {
		t.Errorf("sort/limit not defaulted: %q %d", d.SortBy, d.Limit)
	}
}

// An over-large limit is clamped rather than refused: the operator asked for
// everything, and the universe is only 750 wide.
func TestLimitIsClamped(t *testing.T) {
	d := Definition{Conditions: []Condition{{Field: "close", Op: ">", Value: 1}}, Limit: 100000}
	if errs := d.Validate(); len(errs) > 0 {
		t.Fatalf("valid definition rejected: %v", errs)
	}
	if d.Limit != MaxLimit {
		t.Errorf("limit = %d, want %d", d.Limit, MaxLimit)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	d := Definition{
		Match: "sometimes",
		Conditions: []Condition{
			{Field: "nope", Op: "??", Value: 1},
		},
		SortBy: "also_nope",
	}
	errs := d.Validate()
	// match, field, op, sort_by
	if len(errs) != 4 {
		t.Errorf("got %d errors, want 4: %+v", len(errs), errs)
	}
}
