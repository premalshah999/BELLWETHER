// Package config loads every tunable from the environment. Secrets live only in
// .env or the real environment — never in code, never in the database.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is the fully resolved application configuration.
type Config struct {
	// HTTP
	Addr                 string
	AllowUnauthenticated bool

	// DatabaseURL is the Postgres database.
	DatabaseURL string

	// NewsArchiveURL is a second Postgres that holds aged news, so the archive
	// can outgrow one provider's free tier. The news graph is divided by time,
	// the one cut that keeps each event whole: recent news on the primary,
	// older news here. Empty keeps everything on the primary.
	NewsArchiveURL string
	// NewsHotWindow is how long news stays on the primary before the rollover
	// job moves it to the archive.
	NewsHotWindow time.Duration

	// LLMDailyUSDCap caps text-model spend per UTC day, in US dollars. Zero
	// disables the daily cap; the monthly token budget still applies.
	LLMDailyUSDCap float64

	// TypeSafeAPIKey enables Jev for decisions -- event classification and
	// news scoring. Empty leaves those on the text model.
	TypeSafeAPIKey string
	// JevModel pins a Jev model; empty follows jev-latest.
	JevModel string
	// JevInputPriceUSD is Jev's price per million input tokens, for spend
	// accounting. TypeSafe's API reference does not state pricing, so this is
	// configurable rather than hardcoded.
	JevInputPriceUSD float64

	// Market data
	AlphaVantageKey        string
	AlphaVantageDailyLimit int
	TwelveDataKey          string
	TwelveDataDailyLimit   int
	// YFinanceURL is the yfinance sidecar. Empty leaves the provider absent.
	YFinanceURL string
	// YFinanceAdjust returns dividend-adjusted (total-return) prices rather
	// than what the stock actually traded at.
	YFinanceAdjust  bool
	MarketDataOrder []string
	EnableSynthetic bool

	// Notifications
	TelegramBotToken string
	TelegramChatID   string
	// TelegramAPIBaseURL overrides Telegram's host, for deployments behind an
	// egress proxy and for end-to-end testing against a stand-in server.
	TelegramAPIBaseURL string

	// AI
	LLMBaseURL    string
	LLMModel      string
	LLMCheapModel string
	// LLMResearchModel writes deep-research briefs; empty uses LLMModel.
	LLMResearchModel string
	// LLMResearchEffort is the reasoning effort asked of it: low, high, max.
	LLMResearchEffort     string
	LLMAPIKey             string
	LLMMonthlyTokenBudget int
	// LLMExtraBody is merged into every chat-completions request body. It is
	// the escape hatch for vendor parameters outside the OpenAI schema.
	LLMExtraBody map[string]any

	// Search
	// SearXNGURL points at the private metasearch node. Empty disables it.
	SearXNGURL string
	// StreamInterval is the live quote poll cadence during market hours.
	StreamInterval time.Duration
	// SessionSecret signs session cookies across restarts.
	SessionSecret string

	// SECUserAgent is the contact string SEC EDGAR's fair-access policy
	// requires on every request: "AppName/1.0 (contact@example.com)". A
	// generic or empty User-Agent gets 403'd outright -- verified live
	// against this host. Left empty, SEC sources are not registered at all
	// rather than being registered and failing every request; see
	// buildSECSources' caller.
	SECUserAgent string

	LogLevel slog.Level
}

// Load reads .env if present, then the environment. Values already exported in
// the environment win over .env, which is what makes docker-compose overrides
// and one-off shell overrides behave as operators expect.
func Load(envFile string) (*Config, error) {
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil && !os.IsNotExist(err) {
			// A missing .env is normal in container deployments where the
			// environment is injected directly.
			slog.Debug("no .env file loaded", "path", envFile, "err", err)
		}
	}

	c := &Config{
		Addr:                   envStr("HTTP_ADDR", ":8080"),
		AllowUnauthenticated:   envBool("ALLOW_UNAUTHENTICATED", false),
		DatabaseURL:            envStr("DATABASE_URL", ""),
		NewsArchiveURL:         envStr("NEWS_ARCHIVE_DATABASE_URL", ""),
		NewsHotWindow:          envDuration("NEWS_HOT_WINDOW", 30*24*time.Hour),
		LLMDailyUSDCap:         envFloat("LLM_DAILY_USD_CAP", 1.00),
		TypeSafeAPIKey:         envStr("TYPESAFE_API_KEY", ""),
		JevModel:               envStr("JEV_MODEL", ""),
		JevInputPriceUSD:       envFloat("JEV_INPUT_PRICE_USD", 0.042),
		AlphaVantageKey:        envStr("ALPHAVANTAGE_API_KEY", ""),
		AlphaVantageDailyLimit: envInt("ALPHAVANTAGE_DAILY_LIMIT", 25),
		TwelveDataKey:          envStr("TWELVEDATA_API_KEY", ""),
		TwelveDataDailyLimit:   envInt("TWELVEDATA_DAILY_LIMIT", 800),
		YFinanceURL:            envStr("YFINANCE_URL", ""),
		YFinanceAdjust:         envBool("YFINANCE_ADJUST", false),
		EnableSynthetic:        envBool("ENABLE_SYNTHETIC_FALLBACK", false),
		TelegramBotToken:       envStr("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:         envStr("TELEGRAM_CHAT_ID", ""),
		TelegramAPIBaseURL:     envStr("TELEGRAM_API_BASE_URL", ""),
		LLMBaseURL:             envStr("LLM_BASE_URL", ""),
		LLMModel:               envStr("LLM_MODEL", ""),
		LLMCheapModel:          envStr("LLM_CHEAP_MODEL", ""),
		LLMResearchModel:       envStr("LLM_RESEARCH_MODEL", ""),
		LLMResearchEffort:      envStr("LLM_RESEARCH_EFFORT", "high"),
		LLMAPIKey:              envStr("LLM_API_KEY", ""),
		LLMMonthlyTokenBudget:  envInt("LLM_MONTHLY_TOKEN_BUDGET", 2_000_000),
		SearXNGURL:             envStr("SEARXNG_URL", ""),
		// How often the quote stream polls while a market is open. Five
		// seconds is a compromise: fast enough that a price feels live,
		// slow enough to stay courteous to an upstream that is not a paid
		// market data feed. A licensed tick source makes this irrelevant.
		StreamInterval: envDuration("STREAM_INTERVAL", 5*time.Second),
		SessionSecret:  envStr("SESSION_SECRET", ""),
		SECUserAgent:   envStr("SEC_USER_AGENT", ""),
	}

	// The cheap tier defaults to the main model so a single-model deployment
	// needs no extra configuration.
	if c.LLMCheapModel == "" {
		c.LLMCheapModel = c.LLMModel
	}

	if raw := envStr("LLM_EXTRA_BODY", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.LLMExtraBody); err != nil {
			return nil, fmt.Errorf("config: LLM_EXTRA_BODY must be a JSON object: %w", err)
		}
	}

	order := envStr("MARKETDATA_ORDER", "yfinance,twelvedata,alphavantage")
	for _, p := range strings.Split(order, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			c.MarketDataOrder = append(c.MarketDataOrder, p)
		}
	}
	if len(c.MarketDataOrder) == 0 {
		return nil, fmt.Errorf("config: MARKETDATA_ORDER resolved to an empty provider list")
	}
	for _, p := range c.MarketDataOrder {
		switch p {
		case "yfinance", "twelvedata", "alphavantage", "synthetic":
		default:
			return nil, fmt.Errorf("config: MARKETDATA_ORDER contains unknown provider %q (want yfinance, twelvedata, alphavantage, or synthetic)", p)
		}
	}

	c.LogLevel = parseLevel(envStr("LOG_LEVEL", "info"))

	if c.AlphaVantageDailyLimit < 0 {
		return nil, fmt.Errorf("config: ALPHAVANTAGE_DAILY_LIMIT must not be negative, got %d", c.AlphaVantageDailyLimit)
	}
	if c.TwelveDataDailyLimit < 0 {
		return nil, fmt.Errorf("config: TWELVEDATA_DAILY_LIMIT must not be negative, got %d", c.TwelveDataDailyLimit)
	}
	if c.LLMMonthlyTokenBudget < 0 {
		return nil, fmt.Errorf("config: LLM_MONTHLY_TOKEN_BUDGET must not be negative, got %d", c.LLMMonthlyTokenBudget)
	}
	return c, nil
}

// AlphaVantageConfigured reports whether an Alpha Vantage key is present.
func (c *Config) AlphaVantageConfigured() bool { return c.AlphaVantageKey != "" }

// TwelveDataConfigured reports whether a Twelve Data key is present.
func (c *Config) TwelveDataConfigured() bool { return c.TwelveDataKey != "" }

// YFinanceConfigured reports whether a sidecar address was supplied.
func (c *Config) YFinanceConfigured() bool { return c.YFinanceURL != "" }

// TelegramConfigured reports whether Telegram can be used.
func (c *Config) TelegramConfigured() bool {
	return c.TelegramBotToken != "" && c.TelegramChatID != ""
}

// LLMConfigured reports whether the AI layer can be used.
func (c *Config) LLMConfigured() bool {
	return c.LLMBaseURL != "" && c.LLMModel != "" && c.LLMAPIKey != ""
}

// envDuration reads a duration such as "5s" or "250ms".
//
// A malformed value falls back to the default rather than failing startup:
// a mistyped cadence should not stop the application, and the default is
// always a working value.
func envDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("ignoring unparseable integer setting", "key", key, "value", raw, "default", def)
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("ignoring unparseable boolean setting", "key", key, "value", raw, "default", def)
		return def
	}
	return b
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return def
	}
	return f
}
