package ai

import (
	"strings"
	"testing"
)

func TestEveryPromptParsesAndRenders(t *testing.T) {
	// A malformed template must never be discovered at 08:30 inside a
	// cron job, so every prompt is exercised here with representative data.
	cases := map[string]any{
		PromptSystem: nil,
		PromptAlertContext: map[string]any{
			"AlgorithmName": "Momentum watch",
			"Symbol":        "XOM",
			"Summary":       "RSI(14)=32.10 < 35",
			"Price":         "2,431.00",
			"News": []map[string]string{
				{"Title": "Exxon falls", "Source": "Reuters", "Age": "2h ago"},
			},
		},
		PromptEventBrief: map[string]any{
			"Headline":  "Hexcel Corporation: securing order of $250 million",
			"Body":      "The company has secured an order worth $250 million for composite structures, to be executed over eighteen months.",
			"EventType": "CONTRACT",
			"Companies": "TIMETECHNO",
			"Source":    "SEC 8-K filings",
			"Official":  true,
			"Published": "2026-08-31T12:01:00Z",
		},
		PromptMorningBrief: map[string]any{
			"Date": "24 Aug 2026", "TZ": "America/New_York",
			"Symbols": []map[string]string{
				{"Symbol": "AAPL", "Price": "310.34", "ChangePercent": "+0.61%", "Note": "above SMA20"},
			},
			"News":   []map[string]string{{"Symbol": "AAPL", "Title": "T", "Source": "S", "Age": "1h ago"}},
			"Alerts": []map[string]string{{"AlgorithmName": "A", "Symbol": "AAPL", "Summary": "fired"}},
		},
		PromptEventClassify: map[string]any{
			"Types": "ORDER_WIN, EARNINGS, DIVIDEND",
			"Events": []map[string]any{{
				"ID": "12", "Type": "ORDER_WIN", "Importance": "7",
				"Headline": "Fluor: receipt of an order worth $4.2 billion",
				"Summary":  "receipt of an order", "Facts": "PURPOSE=ORDER",
				"Companies": "LT", "SourceCount": 3, "Official": true,
			}},
		},
		PromptDeepResearch: map[string]any{
			"Query": "Exxon Pioneer merger", "Symbols": "XOM",
			"History":  []map[string]string{{"Question": "How is Exxon doing?", "Answer": "Refining margins improved."}},
			"Universe": []map[string]string{{"Industry": "Construction Materials", "Symbols": "ULTRACEMCO, ACC"}},
			"Count":    2, "Scrapers": "google_news, gdelt",
			"Sources": []map[string]any{{
				"Index": 1, "Title": "Exxon weighs retail demerger",
				"Publisher": "Reuters", "Snippet": "People familiar said…",
				"Age": "2h ago", "Trust": 95,
			}},
		},
		PromptResearchFollowup: map[string]any{
			"Question": "what about their debt?", "Symbols": "GEV",
			"History": []map[string]string{{"Question": "How is GE Vernova doing?", "Answer": "It won orders."}},
		},
		PromptSymbolDebrief: map[string]any{
			"Symbol": "GEV", "Company": "GE Vernova Inc.",
			"Period": "last 30 days", "Count": 12, "Official": 9,
			"Industry": "Industrials",
			"Events": []map[string]any{{
				"ID": 4, "When": "25 Aug 14:32", "Type": "ORDER_WIN", "Official": true,
				"Importance": 7, "Headline": "GE Vernova wins 250 MW turbine order",
				"Summary": "from NextEra Energy", "Facts": "PURPOSE=ORDER",
			}},
		},
		PromptNewsDigest: map[string]any{
			"Symbol": "XOM", "Company": "Exxon Mobil",
			"Articles": []map[string]string{{"ID": "a1", "Title": "T", "Source": "S", "Age": "1h ago"}},
		},
		PromptExplainMove: map[string]any{
			"Symbol": "AAPL", "Price": "310.34", "Change": "+1.88", "ChangePercent": "+0.61%",
			"DayLow": "308", "DayHigh": "312", "Volume": "27.8M", "VolumeNote": "0.5x average",
			"TrendNote": "below its 20-day average",
			"Sources": []map[string]any{
				{"Index": 1, "Title": "T", "URL": "https://example.com", "Snippet": "S"},
			},
		},
		PromptTradeAgent: map[string]any{
			"At": "Wed 30 Sep 10:15", "SessionOpen": true, "MinutesToClose": 345, "Intraday": false,
			"Equity": "10000.00", "Cash": "4000.00", "BuyingPower": "4000.00", "DayChangePct": 0.4, "DrawdownPct": 1.2,
			"MaxPositions": 5, "MaxPositionPct": 25.0, "StopLossPct": 4.0, "TakeProfitPct": 8.0,
			"Holdings": []map[string]any{{"Symbol": "AAPL", "Qty": 10, "AvgCost": 300.0, "Price": 310.0, "UnrealizedPct": 3.3, "HeldDays": 2}},
			"Candidates": []map[string]any{{"Symbol": "NVDA", "Name": "NVIDIA", "Sector": "Information Technology", "Price": 180.0,
				"ChangePct": 1.2, "Change5dPct": 3.4, "RSI14": 58.0, "VolumeRatio": 1.3, "FromSMA20Pct": 2.1, "Signal": "volume spike",
				"Headlines": []string{"Nvidia unveils a new chip (Reuters, 2h ago)"}}},
			"Instructions": "Prefer large caps.",
		},
		PromptOutlook: map[string]any{
			"Symbol": "AAPL", "HorizonDays": 10, "Price": "310.34", "Source": "the engine",
			"Q05": "-7.1%", "Q25": "-2.0%", "Q50": "+0.3%", "Q75": "+2.6%", "Q95": "+7.4%",
			"PUp": "54%", "PBeat": "51%", "Threshold": "±4.2%",
			"Bull": "17%", "Base": "68%", "Bear": "15%", "BullMove": "+6.8%", "BaseMove": "+0.2%", "BearMove": "-6.9%",
			"Vol": "24%", "NormalVol": "28%", "Earnings": "a report on session 3",
			"Drivers": "12-month momentum (toward outperforming)", "Regime": "calm", "Record": "80% ranges held 81%",
			"News": []map[string]string{{"Title": "T", "Source": "S", "Age": "1h ago"}},
		},
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := RenderPrompt(name, data)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if strings.TrimSpace(got) == "" {
				t.Fatal("rendered to nothing")
			}
			// An unfilled placeholder means a field name changed without the
			// template being updated.
			if strings.Contains(got, "<no value>") {
				t.Errorf("template has an unfilled placeholder:\n%s", got)
			}
			if strings.Contains(got, "{{") {
				t.Errorf("template left raw syntax in the output:\n%s", got)
			}
		})
	}
}

func TestAllPromptFilesAreCovered(t *testing.T) {
	// Every file in prompts/ must be exercised by the test above, or a
	// template could rot unnoticed.
	covered := map[string]bool{
		PromptSystem: true, PromptAlertContext: true, PromptMorningBrief: true,
		PromptNewsDigest: true, PromptExplainMove: true, PromptOutlook: true,
		PromptEventClassify: true, PromptDeepResearch: true,
		PromptResearchFollowup: true, PromptSymbolDebrief: true,
		PromptEventBrief: true, PromptTradeAgent: true,
	}
	for name := range templates {
		if !covered[name] {
			t.Errorf("prompt %q has no coverage in TestEveryPromptParsesAndRenders", name)
		}
	}
	if len(templates) != len(covered) {
		t.Errorf("found %d prompts, expected %d", len(templates), len(covered))
	}
}

func TestPromptsRenderWithEmptyCollections(t *testing.T) {
	// The common degraded case: search returned nothing, no news was
	// collected, no algorithms fired. The prompt must still make sense and
	// must tell the model that the section is genuinely empty, so it does not
	// invent entries.
	tests := []struct {
		name     string
		prompt   string
		data     any
		wantText string
	}{
		{
			name:   "alert context without news",
			prompt: PromptAlertContext,
			data: map[string]any{
				"AlgorithmName": "A", "Symbol": "S", "Summary": "x", "Price": "1", "News": nil,
			},
			wantText: "No recent news",
		},
		{
			name:   "explain move without sources",
			prompt: PromptExplainMove,
			data: map[string]any{
				"Symbol": "S", "Price": "1", "Change": "0", "ChangePercent": "0%",
				"DayLow": "1", "DayHigh": "1", "Volume": "1", "TrendNote": "flat", "Sources": nil,
			},
			wantText: "No sources were retrieved",
		},
		{
			name:   "morning brief with nothing to report",
			prompt: PromptMorningBrief,
			data: map[string]any{
				"Date": "d", "TZ": "t", "Symbols": nil, "News": nil, "Alerts": nil,
			},
			wantText: "No algorithms fired",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderPrompt(tc.prompt, tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tc.wantText) {
				t.Errorf("rendered prompt does not say %q:\n%s", tc.wantText, got)
			}
		})
	}
}

func TestSystemPromptForbidsAdvice(t *testing.T) {
	// This application is explicitly not an advice tool, and the system
	// prompt is where that is enforced for every feature at once.
	// Whitespace is normalised before matching. These phrases are prose, and
	// prose gets rewrapped: an earlier edit put a line break between "price"
	// and "targets", which broke this test without weakening the prompt at
	// all. A test that fails on reflowing invites being weakened to make it
	// pass, and this is the one guarantee that should never be weakened.
	sys := strings.Join(strings.Fields(SystemMessage().Content), " ")
	for _, phrase := range []string{"recommendation", "price target", "Never invent"} {
		if !strings.Contains(sys, phrase) {
			t.Errorf("the system prompt does not forbid %q", phrase)
		}
	}
	if SystemMessage().Role != RoleSystem {
		t.Error("SystemMessage has the wrong role")
	}
}

func TestUnknownPrompt(t *testing.T) {
	if _, err := RenderPrompt("no_such_prompt", nil); err == nil {
		t.Error("want an error for an unknown prompt name")
	}
	if _, err := UserPrompt("no_such_prompt", nil); err == nil {
		t.Error("want an error from UserPrompt too")
	}
}
