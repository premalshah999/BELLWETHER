package algo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func TestTemplatesAreValid(t *testing.T) {
	templates := Templates()
	if len(templates) != 4 {
		t.Fatalf("got %d templates, want 4", len(templates))
	}

	keys := map[string]bool{}
	for _, tpl := range templates {
		t.Run(tpl.Key, func(t *testing.T) {
			if tpl.Key == "" || tpl.Title == "" || tpl.Description == "" || tpl.Rationale == "" {
				t.Error("a template needs a key, title, description, and rationale")
			}
			if keys[tpl.Key] {
				t.Errorf("duplicate template key %q", tpl.Key)
			}
			keys[tpl.Key] = true

			if tpl.Algorithm == nil {
				t.Fatal("template has no algorithm")
			}
			if err := tpl.Algorithm.Validate(); err != nil {
				t.Fatalf("template does not validate: %v", err)
			}
			// Templates arrive disabled: seeding an installation must not
			// start sending notifications nobody asked for.
			if tpl.Algorithm.Enabled {
				t.Error("templates must be seeded disabled")
			}
			if tpl.Algorithm.CooldownHours <= 0 {
				t.Error("a template without a cooldown would spam the operator")
			}
		})
	}
}

func TestTemplatesSurviveJSONRoundTrip(t *testing.T) {
	// Templates are stored as JSON and re-parsed on load, so the encoding must
	// be lossless through the same path the API uses.
	for _, tpl := range Templates() {
		t.Run(tpl.Key, func(t *testing.T) {
			encoded, err := json.Marshal(tpl.Algorithm)
			if err != nil {
				t.Fatal(err)
			}
			reparsed, err := Parse(encoded)
			if err != nil {
				t.Fatalf("re-parsing the encoded template failed: %v\n%s", err, encoded)
			}
			again, err := json.Marshal(reparsed)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != string(again) {
				t.Errorf("round trip is not stable:\nfirst:  %s\nsecond: %s", encoded, again)
			}
		})
	}
}

func TestTemplatesEvaluateWithoutError(t *testing.T) {
	// Every template must run to a definite status against a long, realistic
	// series — not report insufficient data on 400 bars, and not panic.
	closes := make([]float64, 400)
	volumes := make([]float64, 400)
	price := 100.0
	for i := range closes {
		// A gentle sine-ish wander so trends form and cross.
		price += float64((i*7)%13) / 10.0
		price -= float64((i*11)%17) / 12.0
		closes[i] = price
		volumes[i] = 1000 + float64((i*23)%700)
	}

	e := NewEvaluator(time.UTC)
	sym := marketdata.MustParseSymbol("RELIANCE.BSE")

	for _, tpl := range Templates() {
		t.Run(tpl.Key, func(t *testing.T) {
			need := tpl.Algorithm.RequiredBars()
			if need > len(closes) {
				t.Fatalf("template needs %d bars, more than the %d this test supplies", need, len(closes))
			}
			res := e.Evaluate(tpl.Algorithm, sym, barsWithVolume(closes, volumes))
			if res.Status == StatusInsufficientData {
				t.Errorf("status = insufficient_data on %d bars: %s", len(closes), res.Reason)
			}
			if res.Status == StatusError {
				t.Errorf("status = error: %s", res.Reason)
			}
			if len(res.Conditions) == 0 {
				t.Error("no conditions were recorded")
			}
			if res.Summary == "" {
				t.Error("no summary was produced")
			}
		})
	}
}

func TestBreakoutTemplateCanActuallyTrigger(t *testing.T) {
	// The point of shift:1 on the 52-week high. Without it this rule could
	// never fire, because a close cannot exceed its own bar's high.
	tpl, err := TemplateByKey("52w_breakout")
	if err != nil {
		t.Fatal(err)
	}

	// 300 flat bars, then a decisive breakout on heavy volume.
	closes := make([]float64, 300)
	volumes := make([]float64, 300)
	for i := range closes {
		closes[i] = 100
		volumes[i] = 1000
	}
	closes[299] = 150
	volumes[299] = 5000

	res := NewEvaluator(time.UTC).Evaluate(tpl.Algorithm,
		marketdata.MustParseSymbol("AAPL"), barsWithVolume(closes, volumes))

	if res.Status != StatusTriggered {
		t.Errorf("status = %q, want triggered (summary: %s, reason: %s)", res.Status, res.Summary, res.Reason)
	}
}

func TestGoldenCrossTemplateFiresOnceOnTheCross(t *testing.T) {
	tpl, err := TemplateByKey("golden_cross")
	if err != nil {
		t.Fatal(err)
	}

	// A long decline followed by a sustained rise produces exactly one
	// upward crossing of the 200-bar average by the 50-bar average.
	closes := make([]float64, 0, 600)
	for i := 0; i < 300; i++ {
		closes = append(closes, 200-float64(i)*0.4)
	}
	for i := 0; i < 300; i++ {
		closes = append(closes, 80+float64(i)*0.6)
	}
	volumes := make([]float64, len(closes))
	for i := range volumes {
		volumes[i] = 1000
	}

	e := NewEvaluator(time.UTC)
	sym := marketdata.MustParseSymbol("AAPL")

	fires := 0
	for i := tpl.Algorithm.RequiredBars(); i <= len(closes); i++ {
		res := e.Evaluate(tpl.Algorithm, sym, barsWithVolume(closes[:i], volumes[:i]))
		if res.Triggered() {
			fires++
		}
	}
	if fires != 1 {
		t.Errorf("the golden cross fired on %d bars, want exactly 1", fires)
	}
}

func TestTemplateByKeyUnknown(t *testing.T) {
	if _, err := TemplateByKey("nope"); err == nil {
		t.Error("want an error for an unknown template key")
	}
}
