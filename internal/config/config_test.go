package config

import (
	"log/slog"
	"path/filepath"
	"testing"
)

// setEnv sets variables for one test and restores the environment afterwards.
func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadDefaults(t *testing.T) {
	// Point at a path with no .env so only defaults apply.
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.AlphaVantageDailyLimit != 25 {
		t.Errorf("AlphaVantageDailyLimit = %d, want 25", cfg.AlphaVantageDailyLimit)
	}
	if cfg.DisplayTZID != "America/New_York" {
		t.Errorf("DisplayTZID = %q, want America/New_York", cfg.DisplayTZID)
	}
	if cfg.DisplayTZ == nil {
		t.Error("DisplayTZ was not resolved")
	}
	// yfinance leads: it has no request budget and the deepest history, so
	// the budgeted providers stand behind it.
	want := []string{"yfinance", "twelvedata", "alphavantage"}
	if len(cfg.MarketDataOrder) != len(want) {
		t.Fatalf("MarketDataOrder = %v, want %v", cfg.MarketDataOrder, want)
	}
	for i, w := range want {
		if cfg.MarketDataOrder[i] != w {
			t.Errorf("MarketDataOrder[%d] = %q, want %q", i, cfg.MarketDataOrder[i], w)
		}
	}
	if cfg.EnableSynthetic {
		t.Error("EnableSynthetic defaulted to true; generated data must be opt-in")
	}
}

func TestFeatureGates(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		check  func(*Config) bool
		want   bool
		reason string
	}{
		{
			name:   "alphavantage needs a key",
			env:    map[string]string{},
			check:  (*Config).AlphaVantageConfigured,
			want:   false,
			reason: "no key supplied",
		},
		{
			name:  "alphavantage with a key",
			env:   map[string]string{"ALPHAVANTAGE_API_KEY": "abc"},
			check: (*Config).AlphaVantageConfigured,
			want:  true,
		},
		{
			name:   "twelvedata needs a key",
			env:    map[string]string{},
			check:  (*Config).TwelveDataConfigured,
			want:   false,
			reason: "no key supplied",
		},
		{
			name:  "twelvedata with a key",
			env:   map[string]string{"TWELVEDATA_API_KEY": "abc"},
			check: (*Config).TwelveDataConfigured,
			want:  true,
		},
		{
			name:   "telegram needs both token and chat id",
			env:    map[string]string{"TELEGRAM_BOT_TOKEN": "t"},
			check:  (*Config).TelegramConfigured,
			want:   false,
			reason: "a token without a chat id cannot deliver anything",
		},
		{
			name:  "telegram fully configured",
			env:   map[string]string{"TELEGRAM_BOT_TOKEN": "t", "TELEGRAM_CHAT_ID": "1"},
			check: (*Config).TelegramConfigured,
			want:  true,
		},
		{
			name:   "llm needs url, model and key",
			env:    map[string]string{"LLM_BASE_URL": "http://x", "LLM_MODEL": "m"},
			check:  (*Config).LLMConfigured,
			want:   false,
			reason: "missing api key",
		},
		{
			name: "llm fully configured",
			env: map[string]string{
				"LLM_BASE_URL": "http://x", "LLM_MODEL": "m", "LLM_API_KEY": "k",
			},
			check: (*Config).LLMConfigured,
			want:  true,
		},
		{
			name:  "search needs either provider key",
			env:   map[string]string{"BRAVE_API_KEY": "b"},
			check: (*Config).SearchConfigured,
			want:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, tc.env)
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := tc.check(cfg); got != tc.want {
				t.Errorf("got %v, want %v (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

func TestCheapModelDefaultsToMainModel(t *testing.T) {
	setEnv(t, map[string]string{"LLM_MODEL": "big-model"})
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLMCheapModel != "big-model" {
		t.Errorf("LLMCheapModel = %q, want it to fall back to LLM_MODEL", cfg.LLMCheapModel)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "unknown provider", env: map[string]string{"MARKETDATA_ORDER": "yahoo,bloomberg"}},
		{name: "negative twelvedata limit", env: map[string]string{"TWELVEDATA_DAILY_LIMIT": "-1"}},
		{name: "empty provider list", env: map[string]string{"MARKETDATA_ORDER": ",,"}},
		{name: "unknown timezone", env: map[string]string{"DISPLAY_TZ": "Mars/Olympus"}},
		{name: "negative av limit", env: map[string]string{"ALPHAVANTAGE_DAILY_LIMIT": "-3"}},
		{name: "negative token budget", env: map[string]string{"LLM_MONTHLY_TOKEN_BUDGET": "-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, tc.env)
			if _, err := Load(""); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

func TestUnparseableNumbersFallBackToDefaults(t *testing.T) {
	// A typo in a numeric setting must not stop the server booting; it warns
	// and uses the default.
	setEnv(t, map[string]string{"ALPHAVANTAGE_DAILY_LIMIT": "twenty-five"})
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AlphaVantageDailyLimit != 25 {
		t.Errorf("AlphaVantageDailyLimit = %d, want the default 25", cfg.AlphaVantageDailyLimit)
	}
}

func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"DEBUG": slog.LevelDebug,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"info":  slog.LevelInfo,
		"":      slog.LevelInfo,
		"junk":  slog.LevelInfo,
	}
	for in, want := range tests {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMarketDataOrderIsNormalised(t *testing.T) {
	setEnv(t, map[string]string{"MARKETDATA_ORDER": " YAHOO , TwelveData , Synthetic "})
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"yahoo", "twelvedata", "synthetic"}
	for i, w := range want {
		if cfg.MarketDataOrder[i] != w {
			t.Errorf("MarketDataOrder[%d] = %q, want %q", i, cfg.MarketDataOrder[i], w)
		}
	}
}

func TestTwelveDataDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	// The documented free-tier allowance.
	if cfg.TwelveDataDailyLimit != 800 {
		t.Errorf("TwelveDataDailyLimit = %d, want 800", cfg.TwelveDataDailyLimit)
	}
	if cfg.TwelveDataConfigured() {
		t.Error("TwelveDataConfigured = true with no key set")
	}
}

func TestYFinanceDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.YFinanceConfigured() {
		t.Error("YFinanceConfigured = true with no sidecar address")
	}
	// As-traded prices by default: an adjusted series answers a different
	// question and would surprise anyone comparing against a broker screen.
	if cfg.YFinanceAdjust {
		t.Error("YFinanceAdjust defaulted to true; dividend adjustment must be opt-in")
	}
}

func TestYFinanceIsAValidProvider(t *testing.T) {
	setEnv(t, map[string]string{"MARKETDATA_ORDER": "yfinance,alphavantage"})
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("yfinance should be an accepted provider name: %v", err)
	}
	if cfg.MarketDataOrder[0] != "yfinance" {
		t.Errorf("order = %v", cfg.MarketDataOrder)
	}
}
