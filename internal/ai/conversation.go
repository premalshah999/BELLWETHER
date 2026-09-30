package ai

import (
	"context"
	"errors"
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
var ErrResearchBusy = errors.New("research capacity is busy; wait for the current research to finish")

func (s *Service) Ask(ctx context.Context, engine *research.Engine, store research.Store, conversationID int64, question string, perProvider int) (*research.Turn, int64, error) {
	return s.AskWithMode(ctx, engine, store, conversationID, question, perProvider, false)
}

func (s *Service) AskWithMode(
	ctx context.Context,
	engine *research.Engine,
	store research.Store,
	conversationID int64,
	question string,
	perProvider int,
	evidenceOnly bool,
) (*research.Turn, int64, error) {

	question = strings.TrimSpace(question)
	if question == "" {
		return nil, 0, fmt.Errorf("ai: empty question")
	}
	if engine == nil {
		return nil, 0, fmt.Errorf("ai: no research engine configured")
	}

	s.researchMu.Lock()
	if s.researchJobs >= 2 || conversationID != 0 && s.researchActive[conversationID] {
		s.researchMu.Unlock()
		return nil, conversationID, ErrResearchBusy
	}
	s.researchJobs++
	if conversationID != 0 {
		s.researchActive[conversationID] = true
	}
	s.researchMu.Unlock()
	handedOff := false
	release := func() {
		s.researchMu.Lock()
		s.researchJobs--
		delete(s.researchActive, conversationID)
		s.researchMu.Unlock()
	}
	defer func() {
		if !handedOff {
			release()
		}
	}()
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
		s.researchMu.Lock()
		s.researchActive[id] = true
		s.researchMu.Unlock()
	}

	turn, err := store.StartTurn(ctx, conversationID, question)
	if err != nil {
		return nil, conversationID, fmt.Errorf("ai: start turn: %w", err)
	}

	// Detached from the request, but not unbounded: a run that has not
	// finished in this long has failed in a way that will not resolve, and
	// leaving it would hold a row in 'running' indefinitely.
	workerTurn := *turn
	handedOff = true
	go func() {
		defer release()
		s.runTurn(s.researchContext, engine, store, conversationID, &workerTurn, perProvider, evidenceOnly)
	}()

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
	evidenceOnly bool,
) {
	ctx, cancel := context.WithTimeout(parent, researchDeadline)
	defer cancel()

	defer func() {
		// A panic in a detached goroutine would otherwise take the process
		// down and leave the turn running forever.
		if r := recover(); r != nil {
			s.log.Error("research turn panicked", "turn", turn.ID, "panic", r)
			failResearchTurn(parent, store, turn.ID, "the request failed unexpectedly")
		}
	}()

	fail := func(stage string, err error) {
		s.log.Warn("research turn failed", "turn", turn.ID, "stage", stage, "err", err)
		failResearchTurn(parent, store, turn.ID, err.Error())
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
	if len(prior) > 0 && !evidenceOnly {
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

	searchCtx := research.WithProgress(ctx, func(stage, detail string) { _ = store.RecordProgress(ctx, turn.ID, stage, detail) })
	result, err := engine.Search(searchCtx, searchQuery, perProvider)
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
	turn.Analyses = result.Analyses
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
	case evidenceOnly:
		turn.Answer = evidenceOverview(result)
		turn.Note = "Evidence-only research. No AI calls were made."
	case len(research.EvidenceIndexes(result.Findings, maxResearchSources)) == 0:
		turn.Degraded = true
		turn.Answer = evidenceOverview(result)
		turn.Note = "No readable article text was retrieved. Links remain available to inspect; synthesis was skipped."
	case s.client == nil || !s.client.Configured():
		turn.Degraded = true
		turn.Note = "No language model is configured, so these are the retrieved sources without synthesis."
		turn.Answer = evidenceOverview(result)
	default:
		_ = store.RecordProgress(ctx, turn.ID, "synthesising",
			fmt.Sprintf("reading %d documents", min(len(result.Findings), maxResearchSources)))
		if err := s.synthesise(ctx, conv, turn, result); err != nil {
			s.log.Warn("research synthesis failed; returning sources only", "err", err)
			turn.Degraded = true
			turn.Note = "The sources below were retrieved, but the summary could not be generated: " + err.Error()
			turn.Answer = evidenceOverview(result)
		}
	}
	turn.ElapsedMS = int(s.now().Sub(start).Milliseconds())

	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer saveCancel()
	if err := store.CompleteTurn(saveCtx, turn); err != nil {
		fail("save", err)
	}
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
			"Answer":   truncateWords(turnSummary(t), 70),
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

// synthesise fills in the answer from the retrieved sources.
func (s *Service) synthesise(ctx context.Context, conv research.Conversation, turn *research.Turn, result research.Result) error {
	sources := result.Findings
	indexes := research.EvidenceIndexes(sources, maxResearchSources)
	if len(indexes) == 0 {
		return fmt.Errorf("no readable evidence for synthesis")
	}
	allowed := map[int]bool{}
	for _, i := range indexes {
		allowed[i+1] = true
	}
	citations := func(in []int) []int {
		var out []int
		seen := map[int]bool{}
		for _, n := range in {
			if allowed[n] && !seen[n] {
				out = append(out, n)
				seen[n] = true
			}
		}
		return out
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
	for _, i := range indexes {
		f := sources[i]
		views = append(views, sourceView{
			Index: i + 1, Title: f.Title, Publisher: f.Publisher,
			// The article's own text where it could be read, falling back to
			// the search snippet. Forty-five words was the right budget for a
			// list of headlines and is far too tight for a report: the
			// difference between "Suzlon wins order" and the paragraph naming
			// the counterparty, the megawatts and the delivery schedule is
			// the whole difference between a summary and research.
			Snippet: research.EvidenceExcerpt(f.Body, turn.Question, 480),
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
			"Question": t.Question, "Answer": truncateWords(turnSummary(t), 60),
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

	// Price-and-news analysis: arithmetic over the price series joined to the
	// archive, written as plain lines so the model reasons from numbers it
	// does not have to compute.
	analysed := make([]map[string]any, 0, len(result.Analyses))
	for _, a := range result.Analyses {
		var moves, reactions, notable, smart []string
		for _, m := range a.BigMoves {
			line := fmt.Sprintf("%s: %+.2f%% against the market (%+.2f%% raw, %.1fx normal volume)", m.Date, m.AbnormalPct, m.ReturnPct, m.VolumeRatio)
			if len(m.Events) == 0 {
				line += "; no news in the archive"
			}
			for _, ev := range m.Events {
				line += fmt.Sprintf("; news: \"%s\" (%s)", ev.Headline, strings.ToLower(strings.ReplaceAll(ev.Type, "_", " ")))
			}
			moves = append(moves, line)
		}
		for _, r := range a.Reactions {
			reactions = append(reactions, fmt.Sprintf("%s: %d events, next session %+.2f%% vs market on average, five sessions %+.2f%%, positive %.0f%% of the time",
				strings.ToLower(strings.ReplaceAll(r.Type, "_", " ")), r.Count, r.Day1Mean, r.Day5Mean, r.HitRate))
		}
		for _, n := range a.Notable {
			notable = append(notable, fmt.Sprintf("%s \"%s\": next session %+.2f%%, five sessions %+.2f%% vs market",
				n.Session, n.Event.Headline, n.Day1Pct, n.Day5Pct))
		}
		if sm := a.SmartMoney; sm != nil {
			smart = append(smart, fmt.Sprintf("insiders bought $%.0f and sold $%.0f on the open market in six months", sm.InsiderBuyValue, sm.InsiderSellValue))
			if len(sm.InsiderBuyers) > 0 {
				smart = append(smart, "buyers: "+strings.Join(sm.InsiderBuyers, ", "))
			}
			smart = append(smart, sm.FundMoves...)
			if sm.CongressFilings > 0 {
				smart = append(smart, fmt.Sprintf("%d House disclosures by members of Congress mention it", sm.CongressFilings))
			}
		}
		cat := ""
		if c := a.Catalyst; c != nil {
			cat = fmt.Sprintf("next %s on %s (in %d days)", strings.ReplaceAll(c.Kind, "_", " "), c.Date, c.InDays)
			if c.EPSMean != nil {
				cat += fmt.Sprintf(", analysts expect EPS %.2f", *c.EPSMean)
			}
		}
		analysed = append(analysed, map[string]any{
			"Symbol": a.Symbol, "AsOf": a.AsOf, "Bars": a.Bars, "Notes": a.Notes,
			"Moves": moves, "Reactions": reactions, "Notable": notable, "Smart": smart, "Catalyst": cat,
		})
	}

	prompt, err := UserPrompt(PromptDeepResearch, map[string]any{
		"Analysed": analysed,
		"Query":    turn.Question,
		"Symbols":  strings.Join(result.Symbols, ", "),
		"Count":    len(indexes),
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
		Summary        string `json:"summary"`
		SummarySources []int  `json:"summary_sources"`
		Sections       []struct {
			Heading  string `json:"heading"`
			Body     string `json:"body"`
			Sources  []int  `json:"sources"`
			Measured bool   `json:"measured"`
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

	req := Request{
		Feature:     FeatureDeepResearch,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.2,
		MaxTokens:   6000,
	}
	// The stronger model thinks before it writes, and thinking spends from
	// the same allowance, so it gets far more room.
	if s.researchModel != "" {
		req.Model = s.researchModel
		req.MaxTokens = 20000
		if s.researchEffort != "" {
			req.Extra = map[string]any{"reasoning_effort": s.researchEffort}
		}
	}
	resp, err := s.client.CompleteJSON(ctx, req, &out)
	if err != nil {
		return err
	}

	turn.Answer = ""
	turn.Model = resp.Model
	turn.Sections = nil
	if refs := citations(out.SummarySources); strings.TrimSpace(out.Summary) != "" && (len(refs) > 0 || len(result.Analyses) > 0) {
		turn.Sections = append(turn.Sections, research.Section{Heading: "Research brief", Body: strings.TrimSpace(out.Summary), Sources: refs})
	}
	for _, sec := range out.Sections {
		heading, body := strings.TrimSpace(sec.Heading), strings.TrimSpace(sec.Body)
		// A section rests on cited documents, or on the measured analysis;
		// the latter is arithmetic, not a claim, and has no source to cite.
		if body == "" || (len(citations(sec.Sources)) == 0 && !(sec.Measured && len(result.Analyses) > 0)) {
			continue
		}
		turn.Sections = append(turn.Sections, research.Section{
			Heading: heading,
			Body:    body,
			Sources: citations(sec.Sources),
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
		valid := citations(f.Sources)
		if len(valid) == 0 {
			continue
		}
		turn.Findings = append(turn.Findings, research.Claim{
			Claim: f.Claim, Sources: valid, Confidence: f.Confidence,
		})
	}
	knownSymbols := map[string]bool{}
	for _, symbol := range result.Symbols {
		knownSymbols[symbol] = true
	}
	for _, measured := range result.Measurements {
		knownSymbols[measured.Symbol] = true
	}
	for _, c := range out.Companies {
		sym := strings.ToUpper(strings.TrimSpace(c.Symbol))
		if sym == "" || !knownSymbols[sym] || len(citations(c.Sources)) == 0 {
			continue
		}
		turn.Companies = append(turn.Companies, research.CompanyRef{
			Symbol: sym, Relevance: c.Relevance,
			Direction: string(normalizeDirection(c.Direction)),
			Sources:   citations(c.Sources),
		})
	}
	if len(turn.Sections) == 0 && len(turn.Findings) == 0 {
		return fmt.Errorf("the model returned no claims with valid evidence citations")
	}
	return nil
}

func evidenceOverview(result research.Result) string {
	read := 0
	publishers := map[string]bool{}
	for _, f := range result.Findings {
		if f.Body != "" {
			read++
		}
		if f.Publisher != "" {
			publishers[f.Publisher] = true
		}
	}
	return fmt.Sprintf("Retrieved %d documents from %d publishers; readable text was extracted from %d. Review the sources and their publication dates below.", len(result.Findings), len(publishers), read)
}

func turnSummary(t research.Turn) string {
	if t.Answer != "" {
		return t.Answer
	}
	if len(t.Sections) > 0 {
		return t.Sections[0].Body
	}
	return ""
}

func failResearchTurn(parent context.Context, store research.Store, id int64, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	_ = store.FailTurn(ctx, id, reason)
}
