package events

import (
	"testing"
	"time"
)

func TestStripPublisher(t *testing.T) {
	cases := []struct{ in, pub, want string }{
		{"Oil holds loss as exports slow - Bloomberg", "Bloomberg", "Oil holds loss as exports slow"},
		{"Zuckerberg Lost $8.9 Billion - thestreet.com", "", "Zuckerberg Lost $8.9 Billion"},
		{"Black Cat targets growth - australianmining.com.au", "Australian Mining", "Black Cat targets growth"},
		{"Notable Two Hundred Day Moving Average Cross - CLF", "MarketBeat", "Notable Two Hundred Day Moving Average Cross - CLF"},
		{"Apple - the stock everyone owns", "", "Apple - the stock everyone owns"},
		{"Fed holds rates | Reuters", "Reuters", "Fed holds rates"},
	}
	for _, c := range cases {
		if got := StripPublisher(c.in, c.pub); got != c.want {
			t.Errorf("StripPublisher(%q, %q) = %q, want %q", c.in, c.pub, got, c.want)
		}
	}
}

func TestIdenticalHeadlineMergesWhenOneSideHasNoCompany(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	title := "Should You Really Invest in the Stock Market Right Now?"
	cands := []Candidate{{ID: 7, Headline: title, TitleKey: TitleKey(title), DiscoveredAt: now.Add(-time.Hour)}}
	if m := FindCluster(title, []string{"SPY"}, TypeUnclassified, now, cands); !m.Matched || m.EventID != 7 {
		t.Fatalf("expected a merge into 7, got %+v", m)
	}
	cands[0].Symbols = []string{"QQQ"}
	if m := FindCluster(title, []string{"SPY"}, TypeUnclassified, now, cands); m.Matched {
		t.Fatalf("two different companies must not merge, got %+v", m)
	}
}
