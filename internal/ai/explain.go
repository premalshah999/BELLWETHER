package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/indicators"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/research"
)

// Citation is one source the explanation may reference.
type Citation struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Source  string `json:"source"`
	Snippet string `json:"snippet,omitempty"`
	// Used marks a citation the model actually referenced. Listing sources it
	// ignored as if it had used them would be a small lie with a large effect
	// on how much the reader trusts the rest.
	Used bool `json:"used"`
}

// Explanation is the "explain this move" output.
type Explanation struct {
	Symbol      string     `json:"symbol"`
	GeneratedAt time.Time  `json:"generated_at"`
	Model       string     `json:"model"`
	Status      Status     `json:"status"`
	Price       float64    `json:"price"`
	ChangePct   float64    `json:"change_percent"`
	Explanation string     `json:"explanation"`
	Confidence  string     `json:"confidence"`
	Caveat      string     `json:"caveat"`
	Citations   []Citation `json:"citations"`
	// Note explains a degraded result, e.g. that search is unconfigured.
	Note string `json:"note,omitempty"`
}

type explainPayload struct {
	Explanation string `json:"explanation"`
	Confidence  string `json:"confidence"`
	Cited       []int  `json:"cited"`
	Caveat      string `json:"caveat"`
}

// ExplainMove produces a sourced explanation of a symbol's recent move.
func (s *Service) ExplainMove(ctx context.Context, sym marketdata.Symbol) (Explanation, error) {
	out := Explanation{Symbol: sym.String(), GeneratedAt: s.now()}

	if status, err := s.guard(ctx); err != nil {
		out.Status = status
		return out, err
	}

	series, err := s.market.Candles(ctx, sym, marketdata.Interval1d, 60)
	if err != nil || len(series.Candles) < 2 {
		out.Status = StatusUnavailable
		return out, fmt.Errorf("ai: no price data for %s: %w", sym, err)
	}

	candles := series.Candles
	last := candles[len(candles)-1]
	prev := candles[len(candles)-2]
	change := last.Close - prev.Close
	changePct := 0.0
	if prev.Close != 0 {
		changePct = change / prev.Close * 100
	}
	out.Price, out.ChangePct = last.Close, changePct

	// Gather sources. Search is optional: without it the model still has the
	// price data, and must say plainly that it has no catalyst.
	citations := s.gatherCitations(ctx, sym)
	if len(citations) == 0 {
		out.Note = "No sources were available, so this reading is based on price action alone."
	}

	// The same measured profile the research engine computes, so that an
	// explanation of today's move is set against the instrument's own recent
	// behaviour rather than against nothing. Without it the model is told a
	// stock is up 4% and has no way to know whether that is a normal Tuesday
	// or the largest move in a year — and the difference is the whole point
	// of asking.
	bars := make([]research.Bar, 0, len(candles))
	for _, c := range candles {
		bars = append(bars, research.Bar{
			Time: c.Time, Open: c.Open, High: c.High,
			Low: c.Low, Close: c.Close, Volume: c.Volume,
		})
	}
	var measured string
	if st := research.Measure(sym.String(), bars); st != nil {
		parts := make([]string, 0, len(st.Returns))
		for _, r := range st.Returns {
			parts = append(parts, fmt.Sprintf("%s %+.2f%%", r.Horizon, r.Percent))
		}
		measured = fmt.Sprintf(
			"returns %s; annualised volatility %.1f%%; worst drawdown %.1f%%; %.1f%% from the 52-week high; today's volume %.1fx its recent median",
			strings.Join(parts, ", "), st.Volatility, st.MaxDrawdown,
			st.PctFrom52WHigh, st.VolumeRatio)
	}

	data := map[string]any{
		"Symbol":        sym.String(),
		"Measured":      measured,
		"Price":         formatFloat(last.Close),
		"Change":        formatSigned(change),
		"ChangePercent": formatSigned(changePct) + "%",
		"DayLow":        formatFloat(last.Low),
		"DayHigh":       formatFloat(last.High),
		"Volume":        formatFloat(last.Volume),
		"VolumeNote":    volumeNote(candles),
		"TrendNote":     trendNote(candles),
		"Sources":       citations,
	}

	prompt, err := UserPrompt(PromptExplainMove, data)
	if err != nil {
		out.Status = StatusUnavailable
		return out, err
	}

	var payload explainPayload
	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature:     FeatureExplainMove,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.3,
		MaxTokens:   3000,
	}, &payload)
	if err != nil {
		out.Status = statusFor(err)
		return out, err
	}

	out.Status = StatusOK
	out.Model = resp.Model
	out.Explanation = payload.Explanation
	out.Confidence = normaliseConfidence(payload.Confidence)
	out.Caveat = payload.Caveat

	// Mark which sources the model actually cited.
	cited := map[int]bool{}
	for _, i := range payload.Cited {
		cited[i] = true
	}
	for i := range citations {
		citations[i].Used = cited[citations[i].Index]
	}
	out.Citations = citations

	// Strip citation markers pointing at sources that do not exist.
	//
	// Models cite [1] even when handed no sources at all. Left in place, that
	// marker is an assertion of evidence that was never supplied — precisely
	// the kind of invented sourcing this application must not pass on to a
	// reader who is about to risk money on it.
	cleaned, dropped := stripUnresolvedCitations(out.Explanation, len(citations))
	out.Explanation = cleaned
	if dropped > 0 {
		s.log.Warn("model cited sources that were not supplied",
			"symbol", sym, "dropped", dropped, "available", len(citations))
		out.Note = appendNote(out.Note,
			"The model referenced sources that were not supplied; those references have been removed.")
	}

	s.persist(ctx, "explain_move", sym.String(), resp, out)
	return out, nil
}

// gatherCitations collects sources from the search provider, falling back to
// stored news articles.
func (s *Service) gatherCitations(ctx context.Context, sym marketdata.Symbol) []Citation {
	var citations []Citation
	index := 1

	if s.search != nil {
		results, err := s.search.News(ctx, sym.Ticker+" stock news", 5)
		if err != nil {
			s.log.Debug("search unavailable for explanation", "symbol", sym, "err", err)
		} else {
			for _, r := range results {
				citations = append(citations, Citation{
					Index: index, Title: r.Title, URL: r.URL,
					Source: r.Source, Snippet: r.Snippet,
				})
				index++
			}
		}
	}

	// Collected RSS articles cover the case where no search provider is set.
	if len(citations) == 0 && s.news != nil {
		articles, err := s.news.ListArticles(ctx, sym.String(), 5)
		if err != nil {
			s.log.Debug("no stored articles for explanation", "symbol", sym, "err", err)
		} else {
			for _, a := range articles {
				citations = append(citations, Citation{
					Index: index, Title: a.Title, URL: a.URL,
					Source: a.Source, Snippet: a.OneLine,
				})
				index++
			}
		}
	}
	return citations
}

// volumeNote describes the latest bar's volume against its recent average.
func volumeNote(candles []marketdata.Candle) string {
	avg := indicators.VolAvg(candles, 20).Last()
	if !indicators.IsDefined(avg) || avg == 0 {
		return ""
	}
	ratio := candles[len(candles)-1].Volume / avg
	return fmt.Sprintf("%.1fx its 20-day average", ratio)
}

// trendNote describes where price sits relative to its moving averages.
func trendNote(candles []marketdata.Candle) string {
	closes := indicators.Closes(candles)
	last := closes[len(closes)-1]

	sma20 := indicators.SMA(closes, 20).Last()
	sma50 := indicators.SMA(closes, 50).Last()

	switch {
	case indicators.IsDefined(sma20) && indicators.IsDefined(sma50):
		return fmt.Sprintf("%s its 20-day average (%s) and %s its 50-day (%s)",
			aboveBelow(last, sma20), formatFloat(sma20),
			aboveBelow(last, sma50), formatFloat(sma50))
	case indicators.IsDefined(sma20):
		return fmt.Sprintf("%s its 20-day average (%s)", aboveBelow(last, sma20), formatFloat(sma20))
	default:
		return "not enough history to describe a trend"
	}
}

// citationMarker matches a bracketed source reference such as [1] or [2].
var citationMarker = regexp.MustCompile(`\s*\[(\d+)\]`)

// stripUnresolvedCitations removes markers referring to sources outside the
// supplied range, returning the cleaned text and how many were dropped.
func stripUnresolvedCitations(text string, available int) (string, int) {
	dropped := 0
	cleaned := citationMarker.ReplaceAllStringFunc(text, func(match string) string {
		digits := citationMarker.FindStringSubmatch(match)
		if len(digits) < 2 {
			return match
		}
		n, err := strconv.Atoi(digits[1])
		if err != nil {
			return match
		}
		// Sources are numbered from 1.
		if n >= 1 && n <= available {
			return match
		}
		dropped++
		return ""
	})
	if dropped > 0 {
		// Removing a marker can leave a doubled space before punctuation.
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
		cleaned = strings.TrimSpace(cleaned)
	}
	return cleaned, dropped
}

// appendNote joins note fragments without duplicating punctuation.
func appendNote(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + " " + addition
}

func aboveBelow(a, b float64) string {
	if a >= b {
		return "above"
	}
	return "below"
}

// normaliseConfidence constrains the model's free text to the three values the
// UI knows how to render.
func normaliseConfidence(s string) string {
	lowered := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(lowered, "high"):
		return "high"
	case strings.Contains(lowered, "low"):
		return "low"
	case strings.Contains(lowered, "medium"), strings.Contains(lowered, "moderate"):
		return "medium"
	default:
		// An unrecognised value is treated as low confidence. Reading an
		// unparseable answer as "high" would overstate what the model said.
		return "low"
	}
}

func formatFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "n/a"
	}
	if math.Abs(v) >= 1e6 {
		return fmt.Sprintf("%.2fM", v/1e6)
	}
	return fmt.Sprintf("%.2f", v)
}

func formatSigned(v float64) string {
	if v > 0 {
		return "+" + formatFloat(v)
	}
	return formatFloat(v)
}

// statusFor maps an error to the status the UI shows.
func statusFor(err error) Status {
	switch {
	case errors.Is(err, ErrBudgetExhausted):
		return StatusBudgetReached
	case errors.Is(err, ErrNotConfigured):
		return StatusUnconfigured
	default:
		return StatusUnavailable
	}
}

// persist stores an AI output for the archive. A storage failure is logged but
// never fails the request the operator is waiting on.
func (s *Service) persist(ctx context.Context, kind, symbol string, resp Response, payload any) {
	if s.store == nil {
		return
	}
	content, err := json.Marshal(payload)
	if err != nil {
		s.log.Warn("could not encode AI output for storage", "kind", kind, "err", err)
		return
	}
	out := &Output{
		Kind: kind, Symbol: symbol, CreatedAt: s.now(),
		Model: resp.Model, Tokens: resp.Usage.TotalTokens, Content: content,
	}
	if _, err := s.store.SaveOutput(context.WithoutCancel(ctx), out); err != nil {
		s.log.Warn("could not store AI output", "kind", kind, "err", err)
	}
}
