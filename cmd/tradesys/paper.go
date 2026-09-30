package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/ai"
	"github.com/tradesys/dashboard/internal/algo"
	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/indicators"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/paper"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// routerPrices serves the paper engine from the market-data router.
type routerPrices struct{ router *marketdata.Router }

func (p routerPrices) Quote(ctx context.Context, symbol string) (paper.Cents, time.Time, error) {
	sym, err := marketdata.ParseSymbol(symbol)
	if err != nil {
		return 0, time.Time{}, err
	}
	q, err := p.router.Quote(ctx, sym)
	if err != nil {
		return 0, time.Time{}, err
	}
	return paper.CentsOf(q.Quote.Price), q.Quote.AsOf, nil
}

func (p routerPrices) Bars(ctx context.Context, symbol string) ([]paper.Bar, error) {
	sym, err := marketdata.ParseSymbol(symbol)
	if err != nil {
		return nil, err
	}
	s, err := p.router.Candles(ctx, sym, marketdata.Interval5m, 90)
	if err != nil {
		return nil, err
	}
	out := make([]paper.Bar, len(s.Candles))
	for i, c := range s.Candles {
		out[i] = paper.Bar{At: c.Time, Open: paper.CentsOf(c.Open), High: paper.CentsOf(c.High), Low: paper.CentsOf(c.Low), Close: paper.CentsOf(c.Close)}
	}
	return out, nil
}

func (p routerPrices) Benchmark(ctx context.Context) (float64, error) {
	q, err := p.router.Quote(ctx, marketdata.MustParseSymbol("GSPC.INDEX"))
	if err != nil {
		return 0, err
	}
	return q.Quote.Price, nil
}

// paperCandidates builds what an agent may consider: its own symbols and
// watchlists, what it holds, and optionally the scanner's latest movers,
// each with its recent behaviour and headlines.
type paperCandidates struct {
	store  *postgres.DB
	router *marketdata.Router
	master *company.Master
}

const maxCandidates = 25

func (c paperCandidates) Candidates(ctx context.Context, symbols []string, watchlists []int64, useScanner, useForecast bool) ([]paper.Candidate, error) {
	signals := map[string]string{}
	seen := map[string]bool{}
	var list []string
	add := func(s string) {
		if sym, err := marketdata.ParseSymbol(s); err == nil && !seen[sym.String()] && sym.Exchange == marketdata.ExchangeUS {
			seen[sym.String()] = true
			list = append(list, sym.String())
		}
	}
	for _, s := range symbols {
		add(s)
	}
	for _, id := range watchlists {
		if syms, err := c.store.WatchlistSymbols(ctx, id); err == nil {
			for _, s := range syms {
				add(s.String())
			}
		}
	}
	if useScanner {
		if found, err := c.store.LatestScan(ctx, 15); err == nil {
			for _, f := range found {
				add(f.Symbol)
				var names []string
				for _, sg := range f.Signals {
					names = append(names, strings.ReplaceAll(string(sg), "_", " "))
				}
				signals[f.Symbol] = strings.Join(names, ", ")
			}
		}
	}
	pct := map[string]float64{}
	if _, preds, err := c.store.LatestForecast(ctx); err == nil {
		for i, p := range preds {
			pct[p.Symbol] = p.Percentile
			if useForecast && i < 12 {
				add(p.Symbol)
			}
		}
	}
	if len(list) > maxCandidates {
		list = list[:maxCandidates]
	}
	out := make([]paper.Candidate, 0, len(list))
	for _, s := range list {
		cand, ok := c.describe(ctx, s)
		if !ok {
			continue
		}
		cand.Signal = signals[s]
		if v, ok := pct[s]; ok {
			cand.ModelPercentile = &v
		}
		out = append(out, cand)
	}
	return out, nil
}

func (c paperCandidates) describe(ctx context.Context, symbol string) (paper.Candidate, bool) {
	sym := marketdata.MustParseSymbol(symbol)
	cand := paper.Candidate{Symbol: symbol}
	if c.master != nil {
		if co, ok := c.master.Lookup(symbol); ok {
			cand.Name = co.Name
		}
		cand.Sector, _ = c.master.Sector(symbol)
	}
	series, err := c.store.LoadCandles(ctx, sym, marketdata.Interval1d, 60)
	if err != nil || len(series.Candles) < 21 {
		return cand, false
	}
	bars := series.Candles
	closes := indicators.Closes(bars)
	last := closes[len(closes)-1]
	if q, err := c.router.Quote(ctx, sym); err == nil && q.Quote.Price > 0 {
		last = q.Quote.Price
		cand.ChangePct = round2(q.Quote.ChangePercent)
	}
	cand.Price = round2(last)
	cand.Change5dPct = round2((last/closes[len(closes)-6] - 1) * 100)
	cand.RSI14 = round2(indicators.RSI(closes, 14).Last())
	if sma := indicators.SMA(closes, 20).Last(); sma > 0 {
		cand.FromSMA20Pct = round2((last/sma - 1) * 100)
	}
	if avg := indicators.SMA(indicators.Volumes(bars), 20).Last(); avg > 0 {
		cand.VolumeRatio = round2(bars[len(bars)-1].Volume / avg)
	}
	if evs, err := c.store.ListEvents(ctx, postgres.EventFilter{Symbol: symbol, Since: time.Now().Add(-72 * time.Hour), Limit: 3}); err == nil {
		for _, e := range evs {
			cand.Headlines = append(cand.Headlines, fmt.Sprintf("%s (%s ago)", e.Headline, time.Since(e.DiscoveredAt).Round(time.Hour)))
		}
	}
	return cand, true
}

func round2(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*100) / 100
}

// ruleCheck evaluates a saved algorithm on a symbol's daily bars.
func ruleCheck(store *postgres.DB, evaluator *algo.Evaluator) paper.RuleCheck {
	return func(ctx context.Context, id int64, symbol string) (bool, string, error) {
		all, err := store.ListAlgorithms(ctx)
		if err != nil {
			return false, "", err
		}
		var rule *algo.Algorithm
		for _, a := range all {
			if a.ID == id {
				rule = a
			}
		}
		if rule == nil {
			return false, "", fmt.Errorf("algorithm %d no longer exists", id)
		}
		sym, err := marketdata.ParseSymbol(symbol)
		if err != nil {
			return false, "", err
		}
		iv, err := marketdata.ParseInterval(rule.Interval)
		if err != nil {
			return false, "", err
		}
		series, err := store.LoadCandles(ctx, sym, iv, max(rule.RequiredBars()+5, 60))
		if err != nil {
			return false, "", err
		}
		res := evaluator.Evaluate(rule, sym, series.Candles)
		return res.Triggered(), rule.Name, nil
	}
}

// startPaper builds the paper-trading engine and runner and starts their loop:
// fills every thirty seconds in the session, agents on their cadence, equity
// marked every fifteen minutes and at the close.
func startPaper(ctx context.Context, store *postgres.DB, router *marketdata.Router, svc *ai.Service,
	evaluator *algo.Evaluator, master *company.Master, allow []string, log *slog.Logger) (*paper.Engine, *paper.Runner, paper.Webhook) {
	engine := &paper.Engine{Store: store, Prices: routerPrices{router}, Payments: paper.Simulated{}, Log: log}
	webhook := paper.Webhook{Allow: allow}
	strategies := map[paper.AgentKind]paper.Strategy{
		paper.AgentAlgorithm: paper.Rules{Check: ruleCheck(store, evaluator)},
		paper.AgentWebhook:   webhook,
	}
	if svc != nil {
		strategies[paper.AgentAI] = ai.TradeStrategy{Service: svc}
	}
	runner := &paper.Runner{Engine: engine, Store: store, Candidates: paperCandidates{store, router, master},
		Strategies: strategies, Log: log}

	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		var lastMark time.Time
		closeMarked := ""
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				if n, err := engine.Tick(ctx); err != nil {
					log.Warn("paper tick failed", "err", err)
				} else if n > 0 {
					log.Info("paper orders filled", "count", n)
				}
				if paper.SessionOpen(now) {
					runner.RunDue(ctx)
					if now.Sub(lastMark) >= 15*time.Minute {
						_ = engine.MarkAll(ctx)
						lastMark = now
					}
				}
				// One mark after each close, the day's settled value.
				et := now.In(marketdata.Market)
				day := et.Format("2006-01-02")
				if wd := et.Weekday(); wd != time.Saturday && wd != time.Sunday && et.Hour() == 16 && et.Minute() >= 5 && closeMarked != day {
					_ = engine.MarkAll(ctx)
					closeMarked = day
				}
			}
		}
	}()
	return engine, runner, webhook
}

// luckPool loads daily bars for a spread of the universe, the stocks the
// luck test's random traders pick from.
func luckPool(ctx context.Context, store *postgres.DB, master *company.Master) map[string][]marketdata.Candle {
	var syms []string
	for _, sector := range []string{"Information Technology", "Health Care", "Financials", "Consumer Discretionary",
		"Communication Services", "Industrials", "Consumer Staples", "Energy", "Utilities", "Real Estate", "Materials"} {
		list := master.SymbolsInSector(sector)
		sort.Strings(list)
		for i := 0; i < len(list) && i < 8; i++ {
			syms = append(syms, list[i*len(list)/8])
		}
	}
	pool := map[string][]marketdata.Candle{}
	for _, s := range syms {
		sym, err := marketdata.ParseSymbol(s)
		if err != nil {
			continue
		}
		if series, err := store.LoadCandles(ctx, sym, marketdata.Interval1d, 500); err == nil && len(series.Candles) > 50 {
			pool[s] = series.Candles
		}
	}
	return pool
}

// aiServiceIfConfigured is the AI service when a text model is configured,
// so AI agents are offered only where they can run.
func aiServiceIfConfigured(cfg *config.Config, svc *ai.Service) *ai.Service {
	if svc == nil || !cfg.LLMConfigured() {
		return nil
	}
	return svc
}
