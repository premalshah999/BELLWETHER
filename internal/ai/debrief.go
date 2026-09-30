package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news"
)

// DebriefSection is one part of a symbol debrief.
type DebriefSection struct {
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Events []int64 `json:"events,omitempty"`
}

// DebriefOutstanding is something knowable now that has not resolved yet.
type DebriefOutstanding struct {
	Item     string  `json:"item"`
	Expected string  `json:"expected,omitempty"`
	Events   []int64 `json:"events,omitempty"`
}

// Debrief is a written account of everything collected about one instrument.
type Debrief struct {
	Symbol      string               `json:"symbol"`
	Company     string               `json:"company,omitempty"`
	Period      string               `json:"period"`
	Headline    string               `json:"headline"`
	Sections    []DebriefSection     `json:"sections,omitempty"`
	Outstanding []DebriefOutstanding `json:"outstanding,omitempty"`
	BlindSpots  []string             `json:"blind_spots,omitempty"`

	EventCount    int       `json:"event_count"`
	OfficialCount int       `json:"official_count"`
	Model         string    `json:"model,omitempty"`
	GeneratedAt   time.Time `json:"generated_at"`
	// Events are the source rows, so every citation can be followed.
	Events []news.Event `json:"events,omitempty"`
}

// maxDebriefEvents caps how much history reaches the prompt.
//
// A debrief covering forty events is a report; one covering four hundred is a
// list nobody reads, and the tokens would be spent restating routine
// disclosures. Events arrive most recent first and are truncated, so what is
// dropped is the oldest rather than the least important.
const maxDebriefEvents = 40

// DebriefStore is what a debrief needs from storage.
type DebriefStore interface {
	EventFacts(ctx context.Context, eventID int64) (map[string]string, error)
}

// Debrief writes an account of everything collected about an instrument.
//
// The difference from research is the corpus. Research goes out to the web and
// synthesises what it finds; this reads only what this system already holds —
// filings with their stated figures, resolved companies, validated timestamps
// — and turns it into something a person can read in a minute. It is the
// answer to "I have not looked at this position in a month".
func (s *Service) Debrief(ctx context.Context, store DebriefStore, symbol, company, industry string, events []news.Event, days int) (Debrief, error) {
	if s.client == nil {
		return Debrief{}, ErrNotConfigured
	}
	out := Debrief{
		Symbol: symbol, Company: company,
		Period:      fmt.Sprintf("last %d days", days),
		EventCount:  len(events),
		GeneratedAt: s.now(),
		Events:      events,
	}
	if len(events) == 0 {
		out.Headline = "Nothing has been collected about this instrument in the period."
		return out, nil
	}

	trimmed := events
	if len(trimmed) > maxDebriefEvents {
		trimmed = trimmed[:maxDebriefEvents]
	}

	type eventView struct {
		ID         int64
		When       string
		Type       string
		Official   bool
		Importance int
		Headline   string
		Summary    string
		Facts      string
	}
	views := make([]eventView, 0, len(trimmed))
	for _, e := range trimmed {
		if e.Official {
			out.OfficialCount++
		}
		v := eventView{
			ID: e.ID, Type: strings.ToLower(strings.ReplaceAll(e.Type, "_", " ")),
			Official: e.Official, Headline: e.Headline,
			Summary: truncateWords(e.Summary, 45),
		}
		// The discovery time is the one that can be vouched for; a
		// publisher's is used only when this system judged it trustworthy.
		when := e.DiscoveredAt
		if !e.PublishedAt.IsZero() {
			when = e.PublishedAt
		}
		v.When = when.In(marketdata.Market).Format("2 Jan 15:04")
		if e.Importance != nil {
			v.Importance = *e.Importance
		}
		// The figures a filing actually stated are the whole point of having
		// extracted them: a debrief that says "declared a dividend" when the
		// filing said "$0.35 per share, record date 18 September" has thrown
		// away the useful part.
		if facts, err := store.EventFacts(ctx, e.ID); err == nil && len(facts) > 0 {
			v.Facts = formatFacts(facts)
		}
		views = append(views, v)
	}

	// The count in the header must describe what the model was actually
	// shown, not the whole history, or it will summarise forty events and
	// claim to have covered four hundred.
	officialShown := 0
	for _, v := range views {
		if v.Official {
			officialShown++
		}
	}

	prompt, err := UserPrompt(PromptSymbolDebrief, map[string]any{
		"Symbol": symbol, "Company": company, "Industry": industry,
		"Period": out.Period, "Count": len(views), "Official": officialShown,
		"Events": views,
	})
	if err != nil {
		return out, err
	}

	var payload struct {
		Headline string `json:"headline"`
		Sections []struct {
			Title  string  `json:"title"`
			Body   string  `json:"body"`
			Events []int64 `json:"events"`
		} `json:"sections"`
		Outstanding []struct {
			Item     string  `json:"item"`
			Expected string  `json:"expected"`
			Events   []int64 `json:"events"`
		} `json:"outstanding"`
		BlindSpots []string `json:"blind_spots"`
	}

	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature:     FeatureSymbolDebrief,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.2,
		MaxTokens:   9000,
	}, &payload)
	if err != nil {
		return out, err
	}

	// Citations are checked against the events actually supplied. An id the
	// model invented would render as a link to nothing, which is worse than
	// an uncited sentence because it looks verifiable.
	valid := make(map[int64]bool, len(trimmed))
	for _, e := range trimmed {
		valid[e.ID] = true
	}
	keep := func(ids []int64) []int64 {
		var ok []int64
		for _, id := range ids {
			if valid[id] {
				ok = append(ok, id)
			}
		}
		return ok
	}

	out.Headline = strings.TrimSpace(payload.Headline)
	out.Model = resp.Model
	for _, sec := range payload.Sections {
		if strings.TrimSpace(sec.Body) == "" {
			continue
		}
		out.Sections = append(out.Sections, DebriefSection{
			Title: sec.Title, Body: sec.Body, Events: keep(sec.Events),
		})
	}
	for _, o := range payload.Outstanding {
		if strings.TrimSpace(o.Item) == "" {
			continue
		}
		out.Outstanding = append(out.Outstanding, DebriefOutstanding{
			Item: o.Item, Expected: o.Expected, Events: keep(o.Events),
		})
	}
	out.BlindSpots = payload.BlindSpots
	return out, nil
}
