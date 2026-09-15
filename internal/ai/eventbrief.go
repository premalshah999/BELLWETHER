package ai

import (
	"context"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/news"
)

// EventInput is one feed item, reduced to what a brief is written from.
type EventInput struct {
	Headline  string
	Body      string
	EventType string
	Companies []string
	Source    string
	Official  bool
	Published time.Time
}

// maxBriefBody caps how much of an item is sent.
//
// Filings run to thousands of words of boilerplate and the substance is
// almost always in the first paragraph. Sending the whole thing costs budget
// to tell the model the same thing more slowly.
const maxBriefBody = 4000

// BriefEvent writes two or three sentences on what one item means.
//
// Returns an empty string rather than an error when the model is unreachable
// or the budget is spent: a feed that fails to load because a brief could not
// be written would be a worse feed than one without briefs.
func (s *Service) BriefEvent(ctx context.Context, in EventInput) (string, string, error) {
	if _, err := s.guard(ctx); err != nil {
		return "", "", err
	}

	body := strings.TrimSpace(in.Body)
	if len(body) > maxBriefBody {
		body = body[:maxBriefBody] + "…"
	}
	if body == "" {
		// Many exchange filings carry a headline and a PDF link and nothing
		// else. The headline is still worth a sentence, and saying so beats
		// sending an empty body and letting the model invent one.
		body = "(no body text was retrieved; the headline is all that is available)"
	}

	published := "unknown"
	if !in.Published.IsZero() {
		published = in.Published.UTC().Format(time.RFC3339)
	}
	companies := strings.Join(in.Companies, ", ")
	if companies == "" {
		companies = "none identified"
	}

	prompt, err := UserPrompt(PromptEventBrief, map[string]any{
		"Headline":  in.Headline,
		"Body":      body,
		"EventType": strings.TrimSpace(in.EventType),
		"Companies": companies,
		"Source":    in.Source,
		"Official":  in.Official,
		"Published": published,
	})
	if err != nil {
		return "", "", err
	}

	resp, err := s.client.Complete(ctx, Request{
		Feature:     FeatureEventBrief,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.2,
		// Three sentences, plus headroom for a reasoning model to think first.
		MaxTokens: 1200,
	})
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(resp.Text), resp.Model, nil
}

// BriefStore is the storage a scheduled briefing run needs.
type BriefStore interface {
	UnbriefedEvents(ctx context.Context, minImportance, limit int, within time.Duration) ([]news.Event, error)
	SaveEventBrief(ctx context.Context, eventID int64, brief, model string) error
}

// BriefImportance is the threshold above which an event is briefed
// automatically.
//
// Measured over a week of this feed: importance 7 and above is about 397
// events a day, 8 and above about 101, 9 and above about 9. Seven is the
// level at which the classifier is saying "a holder would want to know", and
// at roughly 1,950 tokens a brief that is under one percent of the monthly
// budget — so the threshold is set by what is worth reading rather than by
// what is affordable.
const BriefImportance = 7

// BriefWindow is how far back a scheduled run will reach.
//
// Briefing a week-old filing nobody opened spends budget answering a question
// that stopped being asked. Anything older is still available on demand.
const BriefWindow = 12 * time.Hour

// BriefEvents writes briefs for the important events that have none.
//
// Returns how many were written. A failure on one event is logged and skipped
// rather than abandoning the run: one unparseable item should not cost the
// other ninety-nine their briefs.
func (s *Service) BriefEvents(ctx context.Context, store BriefStore, limit int) (int, error) {
	if _, err := s.guard(ctx); err != nil {
		return 0, err
	}
	pending, err := store.UnbriefedEvents(ctx, BriefImportance, limit, BriefWindow)
	if err != nil {
		return 0, err
	}

	written := 0
	for _, ev := range pending {
		// Checked every iteration rather than once: a run that starts inside
		// the budget can exhaust it partway through, and the remaining calls
		// would each fail slowly against the provider.
		if _, err := s.guard(ctx); err != nil {
			s.log.Info("stopping the briefing run early", "written", written, "reason", err)
			break
		}
		if ctx.Err() != nil {
			break
		}

		in := EventInput{
			Headline:  ev.Headline,
			Body:      ev.Summary,
			EventType: ev.Type,
			Official:  ev.Official,
			Published: ev.PublishedAt,
		}
		if in.Published.IsZero() {
			in.Published = ev.DiscoveredAt
		}

		brief, model, err := s.BriefEvent(ctx, in)
		if err != nil {
			s.log.Debug("could not brief an event", "event", ev.ID, "err", err)
			continue
		}
		if brief == "" {
			continue
		}
		if err := store.SaveEventBrief(ctx, ev.ID, brief, model); err != nil {
			s.log.Warn("could not store an event brief", "event", ev.ID, "err", err)
			continue
		}
		written++
	}
	return written, nil
}
