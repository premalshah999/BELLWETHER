package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// MorningBrief is the daily summary.
type MorningBrief struct {
	Date        string    `json:"date"`
	GeneratedAt time.Time `json:"generated_at"`
	Model       string    `json:"model"`
	Status      Status    `json:"status"`
	Headline    string    `json:"headline"`
	Bullets     []string  `json:"bullets"`
	WatchToday  []string  `json:"watch_today"`
	// Coverage records what the brief was actually built from, so a thin
	// morning is visibly thin rather than looking like a thin analysis.
	Coverage BriefCoverage `json:"coverage"`
}

// BriefCoverage is the input inventory behind a brief.
type BriefCoverage struct {
	Symbols  int `json:"symbols"`
	Articles int `json:"articles"`
	Alerts   int `json:"alerts"`
}

// RecentAlert is the alert summary the brief includes. It is a plain struct
// rather than the alerts.Alert type so this package does not depend on the
// alert pipeline.
type RecentAlert struct {
	AlgorithmName string
	Symbol        string
	Summary       string
}

// AlertSource supplies alerts that fired since the previous brief.
type AlertSource interface {
	RecentAlertSummaries(ctx context.Context, since time.Time, limit int) ([]RecentAlert, error)
}

// WithAlerts attaches the alert source used by the morning brief.
func WithAlerts(src AlertSource) ServiceOption {
	return func(s *Service) { s.alerts = src }
}

type briefPayload struct {
	Headline   string   `json:"headline"`
	Bullets    []string `json:"bullets"`
	WatchToday []string `json:"watch_today"`
}

type briefSymbol struct {
	Symbol        string
	Price         string
	ChangePercent string
	Note          string
}

// GenerateMorningBrief builds the daily brief across the watchlist.
func (s *Service) GenerateMorningBrief(ctx context.Context) (MorningBrief, error) {
	now := s.now()
	brief := MorningBrief{
		Date:        now.In(s.loc).Format("2 Jan 2006"),
		GeneratedAt: now,
	}

	if status, err := s.guard(ctx); err != nil {
		brief.Status = status
		return brief, err
	}
	if s.watchlist == nil {
		brief.Status = StatusUnavailable
		return brief, fmt.Errorf("ai: no watchlist source configured")
	}

	symbols, err := s.watchlist.WatchedSymbols(ctx)
	if err != nil {
		brief.Status = StatusUnavailable
		return brief, fmt.Errorf("ai: read watchlist: %w", err)
	}
	if len(symbols) == 0 {
		brief.Status = StatusUnavailable
		return brief, fmt.Errorf("ai: the watchlist is empty, so there is nothing to brief on")
	}

	rows := make([]briefSymbol, 0, len(symbols))
	for _, sym := range symbols {
		series, err := s.market.Candles(ctx, sym, marketdata.Interval1d, 60)
		if err != nil || len(series.Candles) < 2 {
			// A symbol without data is simply left out; inventing a row would
			// put a fabricated price in front of the operator.
			s.log.Debug("skipping a symbol in the brief", "symbol", sym, "err", err)
			continue
		}
		candles := series.Candles
		last, prev := candles[len(candles)-1], candles[len(candles)-2]
		changePct := 0.0
		if prev.Close != 0 {
			changePct = (last.Close - prev.Close) / prev.Close * 100
		}
		rows = append(rows, briefSymbol{
			Symbol:        sym.String(),
			Price:         formatFloat(last.Close),
			ChangePercent: formatSigned(changePct) + "%",
			Note:          trendNote(candles),
		})
	}
	brief.Coverage.Symbols = len(rows)

	// News across the whole watchlist, most relevant first where scored.
	var articles []newsItem
	if s.news != nil {
		for _, sym := range symbols {
			stored, err := s.news.ListArticles(ctx, sym.String(), 3)
			if err != nil {
				continue
			}
			for _, a := range stored {
				// Skip articles the digest judged irrelevant, so the brief is
				// not padded with index-level noise.
				if a.Relevance != nil && *a.Relevance < 0.3 {
					continue
				}
				articles = append(articles, newsItem{
					Symbol: a.Symbol, Title: a.Title, Source: a.Source, Age: a.Age(now),
				})
			}
		}
	}
	brief.Coverage.Articles = len(articles)

	var recentAlerts []RecentAlert
	if s.alerts != nil {
		since := now.Add(-24 * time.Hour)
		recentAlerts, err = s.alerts.RecentAlertSummaries(ctx, since, 10)
		if err != nil {
			s.log.Debug("could not read recent alerts for the brief", "err", err)
		}
	}
	brief.Coverage.Alerts = len(recentAlerts)

	prompt, err := UserPrompt(PromptMorningBrief, map[string]any{
		"Date":    brief.Date,
		"TZ":      s.loc.String(),
		"Symbols": rows,
		"News":    articles,
		"Alerts":  recentAlerts,
	})
	if err != nil {
		brief.Status = StatusUnavailable
		return brief, err
	}

	var payload briefPayload
	resp, err := s.client.CompleteJSON(ctx, Request{
		Feature:     FeatureMorningBrief,
		Messages:    []Message{SystemMessage(), prompt},
		Temperature: 0.4,
		MaxTokens:   4000,
	}, &payload)
	if err != nil {
		brief.Status = statusFor(err)
		return brief, err
	}

	brief.Status = StatusOK
	brief.Model = resp.Model
	brief.Headline = payload.Headline
	brief.Bullets = capSlice(payload.Bullets, 5)
	brief.WatchToday = capSlice(payload.WatchToday, 3)

	s.persist(ctx, "morning_brief", "", resp, brief)
	return brief, nil
}

// capSlice enforces the limits the prompt asks for, in case the model exceeds
// them.
func capSlice(in []string, max int) []string {
	if len(in) <= max {
		return in
	}
	return in[:max]
}
