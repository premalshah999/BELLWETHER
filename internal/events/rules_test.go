package events

import "testing"

func TestClassifyHeadline(t *testing.T) {
	// One case per event type the keyword pass can actually produce, with US
	// fixtures. These were Indian ("L&T bags Rs 4,200 crore metro contract",
	// "SEBI bars Karvy", "RBI holds repo rate") which no longer describes
	// anything this product reads, and covered 13 of the 29 reachable types.
	cases := []struct {
		name     string
		headline string
		want     Type
	}{
		{"order win", "Caterpillar bags a $4.2 billion mining equipment contract", TypeOrderWin},
		{"contract", "Lockheed Martin signs a definitive agreement with the Air Force", TypeContract},
		{"order cancelled", "Boeing says an airline cancelled an order for 30 aircraft", TypeOrderCancelled},
		{"resignation", "Intel CFO resigns with immediate effect", TypeManagementChange},
		{"regulatory action", "The SEC bars a broker-dealer from the securities market", TypeRegulatoryAction},
		{"regulatory policy", "The Federal Reserve proposes a new capital rule for large banks", TypeRegulatoryPolicy},
		{"insolvency", "Rite Aid files for Chapter 11 bankruptcy protection", TypeInsolvency},
		{"auditor", "Deloitte resigns as auditor of a mid-cap industrial", TypeAuditorChange},
		{"earnings", "Nvidia Q3 results: net profit rises 12%", TypeEarnings},
		{"guidance", "3M cuts its full-year earnings guidance", TypeGuidance},
		{"acquisition", "Exxon acquires a majority stake in a shale producer", TypeAcquisition},
		{"merger", "Two regional banks agree to an all-stock merger of equals", TypeMerger},
		{"demerger", "Honeywell announces a spin-off of its aerospace division", TypeDemerger},
		{"stake sale", "SoftBank sells its remaining stake in a payments firm", TypeStakeSale},
		{"credit rating", "Moody's downgrades the long-term rating of a US utility", TypeCreditRating},
		{"macro", "The Federal Reserve holds the federal funds rate at 4.5%", TypeMacroEvent},
		{"commodity", "Brent crude rises 4% after the OPEC decision", TypeCommodityEvent},
		{"geopolitics", "The US announces a fresh tariff on steel imports", TypeGeopoliticalEvent},
		{"buyback", "Apple's board approves a $110 billion share buyback", TypeBuyback},
		{"dividend", "Chevron declares a quarterly dividend of $1.71 per share", TypeDividend},
		{"bonus", "The board approves a bonus issue of one share for every two held", TypeBonus},
		{"fund raise", "Rivian raises $1.5 billion in a convertible note offering", TypeFundRaise},
		{"debt", "Ford prices a $2 billion senior unsecured notes offering", TypeDebt},
		{"capex", "Micron will invest $15 billion in a new fabrication plant", TypeCapex},
		{"new product", "Apple unveils a new product line at its September event", TypeNewProduct},
		{"litigation", "A jury orders Johnson & Johnson to pay damages in a product lawsuit", TypeLitigation},
		{"tax", "The IRS assesses a tax demand against a Fortune 500 filer", TypeTaxAction},
		{"pledge", "An insider pledged shares against a personal loan", TypePledge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := ClassifyHeadline(tc.headline, "")
			if !matched {
				t.Fatalf("no rule matched %q", tc.headline)
			}
			if got.Type != tc.want {
				t.Errorf("type = %s, want %s (matched %q)", got.Type, tc.want, got.Matched)
			}
			if got.Confidence <= 0 || got.Confidence > 0.7 {
				t.Errorf("confidence = %.2f; the keyword pass should be modest about itself", got.Confidence)
			}
		})
	}
}

// TestWordBoundariesPreventFalsePositives is why the patterns are anchored.
// Without boundaries "order" matches "disorder" and "merger" matches "emerger".
func TestWordBoundariesPreventFalsePositives(t *testing.T) {
	cases := []struct{ headline string }{
		{"Sleep disorder treatment maker files for approval"},
		{"Tape recorder maker reports higher sales"},
		{"Emerger fund launches new scheme"},
		{"Company reappoints its longstanding advisor"},
	}
	for _, tc := range cases {
		got, matched := ClassifyHeadline(tc.headline, "")
		if matched && (got.Type == TypeOrderWin || got.Type == TypeMerger) {
			t.Errorf("%q wrongly classified as %s via %q", tc.headline, got.Type, got.Matched)
		}
	}
}

// TestUnmatchedStaysUnclassified: a confident wrong label is worse than none.
func TestUnmatchedStaysUnclassified(t *testing.T) {
	for _, headline := range []string{
		"Rupee leads Asian currencies higher",
		"Markets end flat in a listless session",
		"A profile of the country's oldest textile mill",
	} {
		got, matched := ClassifyHeadline(headline, "")
		if matched {
			t.Logf("note: %q matched %s via %q", headline, got.Type, got.Matched)
		}
		if got.Type != TypeUnclassified && !matched {
			t.Errorf("%q returned %s without matching", headline, got.Type)
		}
	}
}

// TestHeadlineOutranksSummary: a match in the headline is stronger evidence.
func TestHeadlineOutranksSummary(t *testing.T) {
	inHeadline, _ := ClassifyHeadline("Company bags large order", "Some background text.")
	inSummary, _ := ClassifyHeadline("Company publishes annual update", "The company bags a large order this quarter.")

	if inHeadline.Confidence <= inSummary.Confidence {
		t.Errorf("headline confidence %.2f should exceed summary confidence %.2f",
			inHeadline.Confidence, inSummary.Confidence)
	}
}

// TestTroublePrecedesCommerce pins the ordering: a story that is both is
// primarily the regulatory one.
func TestTroublePrecedesCommerce(t *testing.T) {
	got, matched := ClassifyHeadline("Company wins order after the SEC bars a rival bidder", "")
	if !matched {
		t.Fatal("expected a match")
	}
	if got.Type != TypeRegulatoryAction {
		t.Errorf("type = %s, want REGULATORY_ACTION: trouble outranks commerce", got.Type)
	}
}

// TestMeasuredKeywordGaps covers wordings found missing against live data.
func TestMeasuredKeywordGaps(t *testing.T) {
	cases := []struct {
		headline string
		want     Type
	}{
		{"Ircon fined ₹9.66 lakh each by NSE, BSE for board non-compliance", TypeRegulatoryAction},
		{"Company penalised for disclosure lapse", TypeRegulatoryAction},
		{"Emerson Lands 13-Year Equinor Deal to Optimize Operations", TypeOrderWin},
		{"Company receives order worth Rs 500 crore", TypeOrderWin},
	}
	for _, tc := range cases {
		got, matched := ClassifyHeadline(tc.headline, "")
		if !matched {
			t.Errorf("no rule matched %q", tc.headline)
			continue
		}
		if got.Type != tc.want {
			t.Errorf("%q = %s, want %s (matched %q)", tc.headline, got.Type, tc.want, got.Matched)
		}
	}
}

// Entity-free items are dropped only when they are another market's session
// or retail-signal noise; US market and macro coverage stays.
func TestUnactionable(t *testing.T) {
	for _, h := range []string{
		"FTSE 100 closes lower as miners slide",
		"Nikkei hits record on weak yen",
		"RSI Alert: Polestar Automotive Now Oversold",
		"Danaos Corp stock hits 52-week high at 152.63 USD",
		"Amazon.com Trades at Significant Discount to Intrinsic Value",
	} {
		if !Unactionable(h, "") {
			t.Errorf("%q should be filtered as unactionable", h)
		}
	}
	for _, h := range []string{
		"Wall Street slides as the Fed signals higher rates for longer",
		"S&P 500 notches a record close on jobs data",
		"Crude oil prices slide 4% as markets look past Iran sanctions",
		"Canada slaps retaliatory tariffs on US goods worth $20 billion",
	} {
		if Unactionable(h, "") {
			t.Errorf("%q should be kept", h)
		}
	}
}

func TestNotReadableHere(t *testing.T) {
	cases := []struct {
		headline string
		want     bool
		why      string
	}{
		{"आईसीएटी ने 15-मीटर लंबी मल्टी-एक्सल स्लीपर बस के लिए पहला अनुपालन प्रमाण-पत्र", true,
			"a wholly Devanagari headline, which topped an English feed at importance 6"},
		{"Reliance Industries posts record quarterly profit", false, "plain English"},
		{"HUL Q1 net profit rises 4% to ₹2,472 crore", false, "a rupee sign does not make it unreadable"},
		{"Tata Motors' ₹18,000 crore capex plan for FY27", false, "currency and digits carry no script"},
		{"Adani Ports (अदाणी पोर्ट्स) wins Colombo terminal deal", false,
			"a parenthetical in another script inside an English headline"},
		{"", false, "nothing to judge"},
		{"2026-08-26 15:30", false, "digits and punctuation alone are not another script"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			if got := NotReadableHere(tc.headline); got != tc.want {
				t.Errorf("NotReadableHere(%q) = %v, want %v", tc.headline, got, tc.want)
			}
		})
	}
}
