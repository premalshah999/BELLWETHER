package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/research"
)

// maxHistoryTurns is how much of a thread is replayed into a follow-up.
//
// Four is enough to resolve any pronoun a person actually uses and short
// enough that a long thread does not grow the prompt without bound. What a
// follow-up needs is the last few exchanges, not the whole session.
const maxHistoryTurns = 4

// Ask starts answering a question and returns immediately.
//
// The retrieval and synthesis run in the background against a context that is
// not the request's, because the request is over in milliseconds. That is the
// point: a ninety-second research cycle used to be tied to the HTTP call, so
// navigating to another tab abandoned it and the work was lost. The turn now
// exists as a row from the moment the question is asked, progresses whether or
// not anyone is watching, and is there when the reader comes back.
func (s *Service) Ask(
	ctx context.Context,
	engine *research.Engine,
	store research.Store,
	conversationID int64,
	question string,
	perProvider int,
) (*research.Turn, int64, error) {

	question = strings.TrimSpace(question)
	if question == "" {
		return nil, 0, fmt.Errorf("ai: empty question")
	}
	if engine == nil {
		return nil, 0, fmt.Errorf("ai: no research engine configured")
	}

	if conversationID == 0 {
		title := question
		if len(title) > 120 {
			title = title[:117] + "…"
		}
		id, err := store.CreateConversation(ctx, title)
		if err != nil {
			return nil, 0, fmt.Errorf("ai: create conversation: %w", err)
		}
		conversationID = id
	}

	turn, err := store.StartTurn(ctx, conversationID, question)
	if err != nil {
		return nil, conversationID, fmt.Errorf("ai: start turn: %w", err)
	}

	// Detached from the request, but not unbounded: a run that has not
	// finished in this long has failed in a way that will not resolve, and
	// leaving it would hold a row in 'running' indefinitely.
	go s.runTurn(context.WithoutCancel(ctx), engine, store, conversationID, turn, perProvider)

	return turn, conversationID, nil
}

// researchDeadline bounds one background research run.
const researchDeadline = 6 * time.Minute

// runTurn performs the work behind an already-created turn.
func (s *Service) runTurn(
	parent context.Context,
	engine *research.Engine,
	store research.Store,
	conversationID int64,
	turn *research.Turn,
	perProvider int,
) {
	ctx, cancel := context.WithTimeout(parent, researchDeadline)
	defer cancel()

	defer func() {
		// A panic in a detached goroutine would otherwise take the process
		// down and leave the turn running forever.
		if r := recover(); r != nil {
			s.log.Error("research turn panicked", "turn", turn.ID, "panic", r)
			_ = store.FailTurn(context.WithoutCancel(parent), turn.ID,
				"the request failed unexpectedly")
		}
	}()

	fail := func(stage string, err error) {
		s.log.Warn("research turn failed", "turn", turn.ID, "stage", stage, "err", err)
		_ = store.FailTurn(context.WithoutCancel(parent), turn.ID, err.Error())
	}

	conv, err := store.GetConversation(ctx, conversationID)
	if err != nil {
		fail("load", err)
		return
	}
	// The turn just created is in this list; earlier ones are the history.
	var prior []research.Turn
	for _, t := range conv.Turns {
		if t.ID != turn.ID && t.Status == research.StatusDone {
			prior = append(prior, t)
		}
	}
	conv.Turns = prior

	start := s.now()
	searchQuery := turn.Question
	if len(prior) > 0 {
		_ = store.RecordProgress(ctx, turn.ID, "rewriting",
			"resolving the question against the thread")
		searchQuery = s.rewriteFollowup(ctx, conv, turn.Question)
		if searchQuery != turn.Question {
			_ = store.RecordProgress(ctx, turn.ID, "rewriting", "searching for: "+searchQuery)
		}
	}
	turn.SearchQuery = searchQuery

	_ = store.RecordProgress(ctx, turn.ID, "searching",
		fmt.Sprintf("querying %d providers", len(engine.Scrapers())))

	result, err := engine.Search(ctx, searchQuery, perProvider)
	if err != nil {
		fail("search", err)
		return
	}

	// What each provider actually returned, so a slow or empty one is
	// visible rather than being averaged into a single number.
	var parts []string
	for _, r := range result.Scrapers {
		if r.Error != "" {
			parts = append(parts, r.Name+" unavailable")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d", r.Name, r.Count))
	}
	_ = store.RecordProgress(ctx, turn.ID, "searching", strings.Join(parts, " · "))

	turn.Sources = result.Findings
	// Recorded before synthesis is attempted, so they survive it failing.
	// Arithmetic over the price series does not depend on the model, and a
	// question about how something performed is very often answered by
	// exactly these numbers — losing them because a token budget ran out
	// would throw away the half of the answer that was never at risk.
	turn.Measurements = result.Measurements
	turn.Providers = nil
	for _, r := range result.Scrapers {
		turn.Providers = append(turn.Providers, research.ProviderReport{
			Name: r.Name, Count: r.Count, Error: r.Error,
		})
	}

	switch {
	case len(result.Findings) == 0 && len(result.Measurements) == 0:
		turn.Degraded = true
		turn.Note = "No sources were retrieved for this query."
	case s.client == nil:
		turn.Degraded = true
		turn.Note = "No language model is configured, so these are the retrieved sources without synthesis."
	default:
		_ = store.RecordProgress(ctx, turn.ID, "synthesising",
			fmt.Sprintf("reading %d documents", min(len(result.Findings), maxResearchSources)))
		if err := s.synthesise(ctx, conv, turn, result); err != nil {
			s.log.Warn("research synthesis failed; returning sources only", "err", err)
			turn.Degraded = true
			turn.Note = "The sources below were retrieved, but the summary could not be generated: " + err.Error()
		}
	}
	turn.ElapsedMS = int(s.now().Sub(start).Milliseconds())

	if err := store.CompleteTurn(context.WithoutCancel(parent), turn); err != nil {
		fail("save", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// rewriteFollowup turns a context-dependent question into a standalone query.
//
// Failure here is not fatal: the original question is used instead. A slightly
// worse search is a much better outcome than refusing to answer, and for
// questions that were already standalone the rewrite would have been a no-op
// anyway.
func (s *Service) rewriteFollowup(ctx context.Context, conv research.Conversation, question string) string {
	if s.client == nil {
		return question
	}
	history := conv.History(maxHistoryTurns)
	views := make([]map[string]string, 0, len(history))
	for _, t := range history {
		views = append(views, map[string]string{
			"Question": t.Question,
			"Answer":   truncateWords(t.Answer, 70),
		})
	}

	prompt, err := UserPrompt(PromptResearchFollowup, map[string]any{
		"History":  views,
		"Question": question,
		"Symbols":  strings.Join(conv.SymbolsMentioned(), ", "),
	})
	if err != nil {
		return question
	}

	var out struct {
		Query     string `json:"query"`
		Reasoning string `json:"reasoning"`
	}
	// Short and cheap: this is one sentence of rewriting, not analysis.
	rewriteCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	if _, err := s.client.CompleteJSON(rewriteCtx, Request{
		Feature:     FeatureResearchRewrite,
		Cheap:       true,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0,
		MaxTokens:   400,
	}, &out); err != nil {
		s.log.Debug("follow-up rewrite failed; searching the question as written", "err", err)
		return question
	}

	rewritten := strings.TrimSpace(out.Query)
	if rewritten == "" {
		return question
	}
	// A rewrite that balloons the question has usually gone wrong — it has
	// added ranking words or restated the whole thread. The original is
	// safer than a query that no longer asks what was asked.
	if len(rewritten) > len(question)*6+120 {
		s.log.Debug("follow-up rewrite was implausibly long; using the question as written",
			"question", question, "rewrite", rewritten)
		return question
	}
	return rewritten
}

// sourceText is what the model reads for one source.
//
// The article body where it could be fetched, the search snippet otherwise.
// Ordering matters: a snippet is a description of an article, and a body is
// the article, so there is never a reason to prefer the former.
func sourceText(f research.Finding) string {
	if body := strings.TrimSpace(f.Body); body != "" {
		return body
	}
	return truncateWords(f.Snippet, 60)
}

// synthesise fills in the answer from the retrieved sources.
func (s *Service) synthesise(ctx context.Context, conv research.Conversation, turn *research.Turn, result research.Result) error {
	sources := result.Findings
	if len(sources) > maxResearchSources {
		sources = sources[:maxResearchSources]
	}

	type sourceView struct {
		Index            int
		Title, Publisher string
		Snippet, Age     string
		Trust            int
		// Words is the extracted length, zero when only a headline was
		// available. Shown to the model so it can weigh a source it can
		// actually read above one it can only see the title of.
		Words int
	}
	views := make([]sourceView, 0, len(sources))
	for i, f := range sources {
		views = append(views, sourceView{
			Index: i + 1, Title: f.Title, Publisher: f.Publisher,
			// The article's own text where it could be read, falling back to
			// the search snippet. Forty-five words was the right budget for a
			// list of headlines and is far too tight for a report: the
			// difference between "Suzlon wins order" and the paragraph naming
			// the counterparty, the megawatts and the delivery schedule is
			// the whole difference between a summary and research.
			Snippet: sourceText(f),
			Words:   f.Words,
			Age:     relativeAgeShort(f.PublishedAt, s.now()),
			Trust:   f.Trust,
		})
	}
	var providerNames []string
	for _, r := range result.Scrapers {
		if r.Count > 0 {
			providerNames = append(providerNames, r.Name)
		}
	}

	// Earlier turns are supplied so the answer does not repeat what the
	// thread already established.
	history := conv.History(maxHistoryTurns)
	priorViews := make([]map[string]string, 0, len(history))
	for _, t := range history {
		priorViews = append(priorViews, map[string]string{
			"Question": t.Question, "Answer": truncateWords(t.Answer, 60),
		})
	}

	// The listed universe for any industry the question named. This is the
	// half of "which companies are exposed to X" that the web cannot answer.
	universe := make([]map[string]string, 0, len(result.Universe))
	for _, u := range result.Universe {
		universe = append(universe, map[string]string{
			"Industry": u.Industry,
			"Symbols":  strings.Join(u.Symbols, ", "),
		})
	}

	// Measured statistics, kept separate from the sources in the prompt as
	// well as in the type. The model must not treat "the price series says
	// -12% over six months" as one more claim to be weighed against an
	// article asserting a rally: one is arithmetic and the other is a report.
	measured := make([]map[string]string, 0, len(result.Measurements))
	for _, m := range result.Measurements {
		parts := make([]string, 0, len(m.Returns))
		for _, r := range m.Returns {
			parts = append(parts, fmt.Sprintf("%s %+.2f%%", r.Horizon, r.Percent))
		}
		measured = append(measured, map[string]string{
			"Symbol":  m.Symbol,
			"Close":   fmt.Sprintf("%.2f", m.Close),
			"AsOf":    m.AsOf.Format("2006-01-02"),
			"Returns": strings.Join(parts, ", "),
			"Risk": fmt.Sprintf("annualised volatility %.1f%%, worst drawdown %.1f%%",
				m.Volatility, m.MaxDrawdown),
			"Range": fmt.Sprintf("52w high %.2f (%.1f%% away), 52w low %.2f (%+.1f%%)",
				m.High52W, m.PctFrom52WHigh, m.Low52W, m.PctFrom52WLow),
			"Volume": fmt.Sprintf("last %.0f against a %.0f average (%.2fx)",
				m.LastVolume, m.AvgVolume, m.VolumeRatio),
			"Bars": fmt.Sprintf("%d", m.Bars),
		})
	}

	// Valuation, already resolved into plain comparisons by the store. The
	// model is given "P/E 23.52 against an industry median of 11.62 — the top
	// quarter, more expensive than most peers" rather than a percentile to
	// interpret, because that interpretation is exact arithmetic and this is
	// not the component to do arithmetic in.
	valued := make([]map[string]any, 0, len(result.Valuations))
	for _, v := range result.Valuations {
		valued = append(valued, map[string]any{
			"Symbol":   v.Symbol,
			"Industry": v.Industry,
			"Peers":    v.PeerCount,
			"Notes":    v.Notes,
		})
	}

	prompt, err := UserPrompt(PromptDeepResearch, map[string]any{
		"Query":    turn.Question,
		"Symbols":  strings.Join(result.Symbols, ", "),
		"Count":    len(sources),
		"Scrapers": strings.Join(providerNames, ", "),
		"Sources":  views,
		"History":  priorViews,
		"Universe": universe,
		"Measured": measured,
		"Valued":   valued,
	})
	if err != nil {
		return err
	}

	var out struct {
		Summary  string `json:"summary"`
		Sections []struct {
			Heading string `json:"heading"`
			Body    string `json:"body"`
			Sources []int  `json:"sources"`
		} `json:"sections"`
		Findings []struct {
			Claim      string `json:"claim"`
			Sources    []int  `json:"sources"`
			Confidence string `json:"confidence"`
		} `json:"findings"`
		Companies []struct {
			Symbol    string `json:"symbol"`
			Relevance string `json:"relevance"`
			Direction string `json:"direction"`
			Sources   []int  `json:"sources"`
		} `json:"companies"`
		Gaps      []string `json:"gaps"`
		Followups []string `json:"followups"`
	}

	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature:  FeatureDeepResearch,
		Messages: []Message{SystemMessage(), prompt},
		// Deliberately generous. A researched answer over two dozen sources
		// runs to a summary, six to ten cited findings, a company list and a
		// gaps section; at 3,500 it was being cut off mid-JSON and the whole
		// turn degraded to "sources only" — which is how this limit was
		// found. The model here also spends part of its budget reasoning
		// before it emits anything.
		Temperature: 0.2,
		// Generous on purpose. A report over forty full-text sources cannot be
		// written in a few thousand tokens, and truncation here is not a
		// graceful degradation — it produces a JSON object that fails to
		// parse, costing the entire call.
		MaxTokens: 32000,
	}, &out)
	if err != nil {
		return err
	}

	turn.Answer = strings.TrimSpace(out.Summary)
	turn.Model = resp.Model
	turn.Sections = nil
	for _, sec := range out.Sections {
		heading, body := strings.TrimSpace(sec.Heading), strings.TrimSpace(sec.Body)
		if body == "" {
			continue
		}
		turn.Sections = append(turn.Sections, research.Section{
			Heading: heading,
			Body:    body,
			Sources: validCitations(sec.Sources, len(sources)),
		})
	}
	turn.Gaps = out.Gaps
	turn.Followups = out.Followups

	// Citations are validated against the sources actually supplied. A model
	// citing source 51 when 48 were given produces a claim that looks
	// sourced and is not, which is worse than an unsourced claim.
	for _, f := range out.Findings {
		if strings.TrimSpace(f.Claim) == "" {
			continue
		}
		valid := validCitations(f.Sources, len(sources))
		if len(valid) == 0 {
			continue
		}
		turn.Findings = append(turn.Findings, research.Claim{
			Claim: f.Claim, Sources: valid, Confidence: f.Confidence,
		})
	}
	for _, c := range out.Companies {
		sym := strings.ToUpper(strings.TrimSpace(c.Symbol))
		if sym == "" {
			continue
		}
		turn.Companies = append(turn.Companies, research.CompanyRef{
			Symbol: sym, Relevance: c.Relevance,
			Direction: string(normalizeDirection(c.Direction)),
			Sources:   validCitations(c.Sources, len(sources)),
		})
	}
	return nil
}

func validCitations(in []int, max int) []int {
	var out []int
	for _, n := range in {
		if n >= 1 && n <= max {
			out = append(out, n)
		}
	}
	return out
}
