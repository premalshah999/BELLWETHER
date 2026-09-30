package algo

import (
	"strings"
	"testing"
)

// findError reports whether any validation error mentions the given field.
func findError(err error, field string) *ValidationError {
	errs, ok := err.(ValidationErrors)
	if !ok {
		if ve, isOne := err.(*ValidationError); isOne && ve.Field == field {
			return ve
		}
		return nil
	}
	for _, e := range errs {
		if e.Field == field {
			return e
		}
	}
	return nil
}

const validAlgo = `{
  "name": "Momentum watch",
  "symbols": ["XOM", "IBM"],
  "interval": "1d",
  "all": [
    {"indicator": "rsi", "period": 14, "op": "<", "value": 35},
    {"indicator": "close", "op": ">", "compare": {"indicator": "sma", "period": 200}},
    {"indicator": "volume", "op": ">", "compare": {"indicator": "vol_avg", "period": 20, "mult": 1.5}}
  ],
  "cooldown_hours": 24,
  "notify": {"telegram": true, "ai_context": true}
}`

func TestParseSpecExample(t *testing.T) {
	// The exact document from the product specification must parse.
	a, err := Parse([]byte(validAlgo))
	if err != nil {
		t.Fatalf("the spec's own example failed to parse: %v", err)
	}
	if a.Name != "Momentum watch" {
		t.Errorf("name = %q", a.Name)
	}
	if len(a.Symbols) != 2 || a.Symbols[0] != "XOM" {
		t.Errorf("symbols = %v", a.Symbols)
	}
	if len(a.All) != 3 {
		t.Fatalf("got %d conditions, want 3", len(a.All))
	}
	if a.CooldownHours != 24 {
		t.Errorf("cooldown = %v, want 24", a.CooldownHours)
	}
	if !a.Notify.Telegram || !a.Notify.AIContext {
		t.Errorf("notify = %+v", a.Notify)
	}
	if a.All[2].Compare.Mult != 1.5 {
		t.Errorf("multiplier = %v, want 1.5", a.All[2].Compare.Mult)
	}
}

func TestValidationRejects(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantField string
		wantText  string
	}{
		{
			name:      "no name",
			src:       `{"symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "name",
		},
		{
			name:      "blank name",
			src:       `{"name":"   ","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "name",
		},
		{
			name:      "no symbols",
			src:       `{"name":"t","symbols":[],"interval":"1d","all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "symbols",
		},
		{
			name:      "bad symbol",
			src:       `{"name":"t","symbols":["FOO.LSE"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "symbols[0]",
			wantText:  "LSE",
		},
		{
			name:      "duplicate symbol",
			src:       `{"name":"t","symbols":["AAPL","aapl"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "symbols[1]",
			wantText:  "more than once",
		},
		{
			name:      "missing interval",
			src:       `{"name":"t","symbols":["AAPL"],"all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "interval",
		},
		{
			name:      "bad interval",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"3mo","all":[{"indicator":"close","op":">","value":1}]}`,
			wantField: "interval",
		},
		{
			name:      "no conditions",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d"}`,
			wantField: "",
			wantText:  "at least one condition",
		},
		{
			name: "both all and any at the top level",
			src: `{"name":"t","symbols":["AAPL"],"interval":"1d",
                   "all":[{"indicator":"close","op":">","value":1}],
                   "any":[{"indicator":"close","op":"<","value":9}]}`,
			wantText: "not both",
		},
		{
			name:      "unknown indicator",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"bollinger","op":">","value":1}]}`,
			wantField: "all[0].indicator",
			wantText:  "unknown indicator",
		},
		{
			name:      "unknown operator",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":"=>","value":1}]}`,
			wantField: "all[0].op",
			wantText:  "unknown operator",
		},
		{
			name:      "missing operator",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","value":1}]}`,
			wantField: "all[0].op",
		},
		{
			name:      "both value and compare",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":1,"compare":{"indicator":"sma","period":20}}]}`,
			wantField: "all[0]",
			wantText:  "not both",
		},
		{
			name:      "neither value nor compare",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">"}]}`,
			wantField: "all[0]",
		},
		{
			name:      "sma without a period",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"sma","op":">","value":1}]}`,
			wantField: "all[0].period",
			wantText:  "requires a period",
		},
		{
			name:      "negative period",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"sma","period":-5,"op":">","value":1}]}`,
			wantField: "all[0].period",
		},
		{
			name:      "period beyond the maximum",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"sma","period":99999,"op":">","value":1}]}`,
			wantField: "all[0].period",
			wantText:  "maximum",
		},
		{
			name:      "degenerate RSI period",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"rsi","period":1,"op":">","value":1}]}`,
			wantField: "all[0].period",
			wantText:  "degenerate",
		},
		{
			name:      "period on a field that takes none",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","period":20,"op":">","value":1}]}`,
			wantField: "all[0].period",
			wantText:  "takes no period",
		},
		{
			name:      "macd with fast slower than slow",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"macd","fast":30,"slow":10,"op":">","value":0}]}`,
			wantField: "all[0]",
			wantText:  "must be shorter",
		},
		{
			name:      "negative cooldown",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}],"cooldown_hours":-1}`,
			wantField: "cooldown_hours",
		},
		{
			name: "nesting deeper than one level",
			src: `{"name":"t","symbols":["AAPL"],"interval":"1d",
                   "all":[{"any":[{"all":[{"indicator":"close","op":">","value":1}]}]}]}`,
			wantText: "one level deep",
		},
		{
			name:      "empty condition object",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{}]}`,
			wantField: "all[0]",
			wantText:  "empty",
		},
		{
			name: "a node that is both group and condition",
			src: `{"name":"t","symbols":["AAPL"],"interval":"1d",
                   "all":[{"indicator":"close","op":">","value":1,"any":[{"indicator":"close","op":"<","value":9}]}]}`,
			wantField: "all[0]",
			wantText:  "not both",
		},
		{
			name:      "unknown compare indicator",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","compare":{"indicator":"nope"}}]}`,
			wantField: "all[0].compare.indicator",
		},
		{
			name:      "negative multiplier",
			src:       `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","compare":{"indicator":"sma","period":20,"mult":-2}}]}`,
			wantField: "all[0].compare.mult",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil {
				t.Fatal("want a validation error, got nil")
			}
			if tc.wantField != "" {
				if ve := findError(err, tc.wantField); ve == nil {
					t.Errorf("no error on field %q; got: %v", tc.wantField, err)
				}
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not mention %q", err, tc.wantText)
			}
		})
	}
}

func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	// An operator fixing a hand-written rule set should see all of its
	// problems in one pass, not one per save.
	src := `{"name":"","symbols":["FOO.LSE"],"interval":"3mo",
             "all":[{"indicator":"bollinger","op":"=>","value":1}]}`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("want errors")
	}
	errs, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("error type = %T, want ValidationErrors", err)
	}
	if errs.Len() < 4 {
		t.Errorf("reported %d problems, want at least 4: %v", errs.Len(), errs)
	}
}

func TestUnknownFieldsAreRejected(t *testing.T) {
	// A typo such as "cooldown_hour" would otherwise be silently discarded,
	// and the operator would get exactly the alert spam they tried to stop.
	src := `{"name":"t","symbols":["AAPL"],"interval":"1d",
             "all":[{"indicator":"close","op":">","value":1}],
             "cooldown_hour":24}`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("want an error for the misspelled field")
	}
	if !strings.Contains(err.Error(), "cooldown_hour") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

func TestMalformedJSON(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "empty", src: ""},
		{name: "whitespace", src: "   \n  "},
		{name: "truncated", src: `{"name":"t",`},
		{name: "not an object", src: `[1,2,3]`},
		{name: "trailing content", src: `{"name":"t","symbols":["AAPL"],"interval":"1d","all":[{"indicator":"close","op":">","value":1}]} garbage`},
		{name: "wrong type for symbols", src: `{"name":"t","symbols":"AAPL","interval":"1d","all":[]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.src)); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestDefaultsAreNormalisedInPlace(t *testing.T) {
	// The evaluator must never have to guess an omitted parameter, so
	// validation fills them in on the stored document.
	src := `{"name":"t","symbols":["aapl"],"interval":"1d",
             "all":[{"indicator":"rsi","op":"<","value":35},
                    {"indicator":"macd","op":">","compare":{"indicator":"macd_signal"}},
                    {"indicator":"atr","op":">","value":1},
                    {"indicator":"vol_avg","op":">","value":1}],
             "notify":{}}`
	a, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}

	if a.All[0].Period != 14 {
		t.Errorf("RSI period = %d, want the default 14", a.All[0].Period)
	}
	if a.All[1].Fast != 12 || a.All[1].Slow != 26 || a.All[1].Signal != 9 {
		t.Errorf("MACD = %d/%d/%d, want 12/26/9", a.All[1].Fast, a.All[1].Slow, a.All[1].Signal)
	}
	if a.All[1].Compare.Fast != 12 || a.All[1].Compare.Slow != 26 || a.All[1].Compare.Signal != 9 {
		t.Errorf("compare MACD = %d/%d/%d, want 12/26/9",
			a.All[1].Compare.Fast, a.All[1].Compare.Slow, a.All[1].Compare.Signal)
	}
	if a.All[2].Period != 14 {
		t.Errorf("ATR period = %d, want 14", a.All[2].Period)
	}
	if a.All[3].Period != 20 {
		t.Errorf("VolAvg period = %d, want 20", a.All[3].Period)
	}
	// Symbols are canonicalised so the scheduler and alert text agree.
	if a.Symbols[0] != "AAPL" {
		t.Errorf("symbol = %q, want the canonical AAPL", a.Symbols[0])
	}
}

func TestLimits(t *testing.T) {
	t.Run("too many symbols", func(t *testing.T) {
		var syms []string
		for i := 0; i < maxSymbols+1; i++ {
			syms = append(syms, "SYM"+string(rune('A'+i%26))+string(rune('A'+i/26)))
		}
		a := &Algorithm{Name: "t", Symbols: syms, Interval: "1d",
			All: []Node{{Indicator: "close", Op: ">", Value: ptr(1.0)}}}
		if err := a.Validate(); err == nil {
			t.Error("want an error for too many symbols")
		}
	})

	t.Run("too many conditions", func(t *testing.T) {
		var nodes []Node
		for i := 0; i < maxConditionsPerGroup+1; i++ {
			nodes = append(nodes, Node{Indicator: "close", Op: ">", Value: ptr(1.0)})
		}
		a := &Algorithm{Name: "t", Symbols: []string{"AAPL"}, Interval: "1d", All: nodes}
		if err := a.Validate(); err == nil {
			t.Error("want an error for too many conditions")
		}
	})
}

func TestVocabularyMatchesRegistry(t *testing.T) {
	vocab := Vocabulary()
	if len(vocab) != len(registry) {
		t.Fatalf("vocabulary has %d entries, registry has %d", len(vocab), len(registry))
	}
	// The frontend builds its dropdowns from this, so every entry must name a
	// real indicator with a legal parameter kind.
	for _, v := range vocab {
		if _, ok := registry[v.Name]; !ok {
			t.Errorf("vocabulary lists %q, which is not in the registry", v.Name)
		}
		switch v.Params {
		case "none", "period", "macd":
		default:
			t.Errorf("%s: params = %q, want none, period, or macd", v.Name, v.Params)
		}
	}
}

func TestEveryVocabularyEntryValidates(t *testing.T) {
	// Anything the builder UI offers must produce a valid algorithm when the
	// operator picks it and accepts the defaults.
	for _, v := range Vocabulary() {
		t.Run(v.Name, func(t *testing.T) {
			period := ""
			if v.Params == "period" {
				p := v.DefaultPeriod
				if p == 0 {
					p = 20
				}
				period = `,"period":` + itoaTest(p)
			}
			src := `{"name":"t","symbols":["AAPL"],"interval":"1d",
                     "all":[{"indicator":"` + v.Name + `"` + period + `,"op":">","value":0}],
                     "notify":{}}`
			if _, err := Parse([]byte(src)); err != nil {
				t.Errorf("indicator %q does not validate with its defaults: %v", v.Name, err)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
