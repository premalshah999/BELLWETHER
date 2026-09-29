package main

import (
	"log/slog"

	"github.com/tradesys/dashboard/internal/config"
	"github.com/tradesys/dashboard/internal/health"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/marketdata/alphavantage"
	"github.com/tradesys/dashboard/internal/marketdata/fixture"
	"github.com/tradesys/dashboard/internal/marketdata/twelvedata"
	"github.com/tradesys/dashboard/internal/marketdata/yahoo"
	"github.com/tradesys/dashboard/internal/marketdata/yfin"
	"github.com/tradesys/dashboard/internal/search"
	"github.com/tradesys/dashboard/internal/search/brave"
	"github.com/tradesys/dashboard/internal/search/tavily"
	"github.com/tradesys/dashboard/internal/server"
	"github.com/tradesys/dashboard/internal/storage"
)

// Market-data and search provider assembly, from configuration.

// buildProviders assembles the provider chain in the configured order. This is
// the only place in the program that names a concrete adapter.
func buildProviders(cfg *config.Config, store storage.Store, log *slog.Logger) (
	[]marketdata.Provider, []health.Dep, map[string]server.BudgetReporter, marketdata.SymbolSearcher,
) {
	var (
		providers []marketdata.Provider
		deps      []health.Dep
		budgets   = map[string]server.BudgetReporter{}
		searcher  marketdata.SymbolSearcher
	)

	for _, name := range cfg.MarketDataOrder {
		switch name {
		case "yahoo":
			providers = append(providers, yahoo.New())
			// Yahoo needs no credentials, so it counts as configured always.
			deps = append(deps, health.Dep{Provider: "yahoo", Kind: health.KindMarketData, Configured: true})

		case "alphavantage":
			av := alphavantage.New(cfg.AlphaVantageKey, cfg.AlphaVantageDailyLimit, store)
			deps = append(deps, health.Dep{
				Provider:   alphavantage.ProviderName,
				Kind:       health.KindMarketData,
				Configured: cfg.AlphaVantageConfigured(),
			})
			budgets[alphavantage.ProviderName] = av
			if !cfg.AlphaVantageConfigured() {
				// Registered for health and budget reporting, but left out of
				// the fetch chain so every request does not pay for a
				// guaranteed failure.
				log.Info("alphavantage has no API key; skipping it in the provider chain")
				continue
			}
			providers = append(providers, av)

		case "yfinance":
			deps = append(deps, health.Dep{
				Provider:   yfin.ProviderName,
				Kind:       health.KindMarketData,
				Configured: cfg.YFinanceConfigured(),
			})
			if !cfg.YFinanceConfigured() {
				log.Info("no yfinance sidecar configured; skipping it in the provider chain")
				continue
			}
			client := yfin.New(cfg.YFinanceURL, yfin.WithDividendAdjustment(cfg.YFinanceAdjust))
			providers = append(providers, client)
			// The same client backs symbol discovery.
			searcher = client

		case "twelvedata":
			td := twelvedata.New(cfg.TwelveDataKey, cfg.TwelveDataDailyLimit, store)
			deps = append(deps, health.Dep{
				Provider:   twelvedata.ProviderName,
				Kind:       health.KindMarketData,
				Configured: cfg.TwelveDataConfigured(),
			})
			budgets[twelvedata.ProviderName] = td
			if !cfg.TwelveDataConfigured() {
				log.Info("twelvedata has no API key; skipping it in the provider chain")
				continue
			}
			providers = append(providers, td)

		case "synthetic":
			providers = append(providers, fixture.New())
			deps = append(deps, health.Dep{Provider: fixture.ProviderName, Kind: health.KindMarketData, Configured: true})
		}
	}

	// The synthetic provider is the last resort so a fresh install with no
	// keys and no network still renders a working dashboard. Everything it
	// produces is labelled "synthetic" all the way to the UI.
	if cfg.EnableSynthetic && !containsProvider(providers, fixture.ProviderName) {
		log.Warn("synthetic market data fallback is enabled: charts may show generated, non-market data")
		providers = append(providers, fixture.New())
		deps = append(deps, health.Dep{Provider: fixture.ProviderName, Kind: health.KindMarketData, Configured: true})
	}

	// Declare the dependencies later milestones own, so their dots render in a
	// truthful state from the first run.
	deps = append(deps,
		health.Dep{Provider: health.ProviderLLM, Kind: health.KindLLM, Configured: cfg.LLMConfigured()},
		health.Dep{Provider: health.ProviderJev, Kind: health.KindLLM, Configured: cfg.TypeSafeAPIKey != ""},
		health.Dep{Provider: tavily.Name, Kind: health.KindSearch, Configured: cfg.TavilyAPIKey != ""},
		health.Dep{Provider: brave.Name, Kind: health.KindSearch, Configured: cfg.BraveAPIKey != ""},
		// News needs no credentials, so it is always "configured" and its dot
		// reflects whether the feeds are actually reachable.
		health.Dep{Provider: "news", Kind: health.KindNews, Configured: true},
		health.Dep{Provider: "telegram", Kind: health.KindNotify, Configured: cfg.TelegramConfigured()},
	)
	return providers, deps, budgets, searcher
}

// buildSearchProviders orders the search vendors by the operator's preference,
// so the configured favourite is tried first and the other is the fallback.
func buildSearchProviders(cfg *config.Config) []search.Provider {
	tav := tavily.New(cfg.TavilyAPIKey)
	brv := brave.New(cfg.BraveAPIKey)
	if cfg.SearchProvider == "brave" {
		return []search.Provider{brv, tav}
	}
	return []search.Provider{tav, brv}
}

func containsProvider(ps []marketdata.Provider, name string) bool {
	for _, p := range ps {
		if p.Name() == name {
			return true
		}
	}
	return false
}
