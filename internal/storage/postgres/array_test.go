package postgres

import (
	"reflect"
	"testing"
)

// TestStringArrayRoundTrip covers the encoding that database/sql does not do
// for us. Getting the quoting wrong is silent: a symbol containing a comma
// would split into two, and nothing would report an error.
func TestStringArrayRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"empty", []string{}},
		{"simple", []string{"RELIANCE", "TCS", "INFY"}},
		{"ampersand symbol", []string{"M&M", "L&T"}},
		{"embedded comma", []string{"Metals & Mining", "Oil Gas & Consumable Fuels"}},
		{"quotes", []string{`say "hello"`, `back\slash`}},
		{"braces", []string{"{weird}", "a,b"}},
		{"empty element", []string{"", "AFTER"}},
		{"whitespace", []string{"has space", "has\ttab"}},
		{"literal null word", []string{"NULL", "REAL"}},
		{"unicode", []string{"रिलायंस", "Bharti Airtel"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := stringArray(tc.in).Value()
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			var out stringArray
			if err := out.Scan(encoded); err != nil {
				t.Fatalf("Scan(%q): %v", encoded, err)
			}
			if !reflect.DeepEqual([]string(out), tc.in) {
				t.Errorf("round trip changed the value:\n  in  %q\n  wire %q\n  out %q", tc.in, encoded, []string(out))
			}
		})
	}
}

func TestStringArrayNil(t *testing.T) {
	v, err := stringArray(nil).Value()
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("nil slice = %v, want SQL NULL", v)
	}
	var out stringArray
	if err := out.Scan(nil); err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("scanned NULL = %v, want nil", out)
	}
}

// TestStringArrayScansPostgresLiterals uses wire formats Postgres actually
// emits, rather than only what this code produces.
func TestStringArrayScansPostgresLiterals(t *testing.T) {
	cases := []struct {
		literal string
		want    []string
	}{
		{"{}", []string{}},
		{"{RELIANCE,TCS}", []string{"RELIANCE", "TCS"}},
		{`{"M&M",LT}`, []string{"M&M", "LT"}},
		{`{"Metals & Mining"}`, []string{"Metals & Mining"}},
		{`{"a,b",c}`, []string{"a,b", "c"}},
		{`{"say \"hi\""}`, []string{`say "hi"`}},
		// An unquoted NULL is a null element, not the four characters.
		{`{A,NULL,B}`, []string{"A", "B"}},
		{`{"NULL"}`, []string{"NULL"}},
	}
	for _, tc := range cases {
		var out stringArray
		if err := out.Scan(tc.literal); err != nil {
			t.Errorf("Scan(%q): %v", tc.literal, err)
			continue
		}
		if !reflect.DeepEqual([]string(out), tc.want) {
			t.Errorf("Scan(%q) = %q, want %q", tc.literal, []string(out), tc.want)
		}
	}
}

func TestStringArrayRejectsMalformed(t *testing.T) {
	var out stringArray
	if err := out.Scan("not an array"); err == nil {
		t.Error("expected a malformed literal to be rejected")
	}
	if err := out.Scan(42); err == nil {
		t.Error("expected a non-string source to be rejected")
	}
}
