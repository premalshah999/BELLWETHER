package forecast_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/marketdata"
	"github.com/tradesys/dashboard/internal/news/company"
)

// TestRealUniverse runs the engine on a real database, read only, and logs
// what it found. Set FORECAST_REAL_DSN (and FORECAST_VIX_JSON, candles as
// the price service returns them) to run it.
func TestRealUniverse(t *testing.T) {
	dsn := os.Getenv("FORECAST_REAL_DSN")
	if dsn == "" {
		t.Skip("set FORECAST_REAL_DSN to run the engine on a real database")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	load := func(sym string) []marketdata.Candle {
		rows, err := db.QueryContext(ctx, `SELECT ts, open::float8, high::float8, low::float8, close::float8, volume::float8 FROM daily_history WHERE symbol = $1
			UNION ALL SELECT ts, open, high, low, close, volume FROM (
			SELECT * FROM candles WHERE symbol = $1 AND interval = '1d' ORDER BY ts DESC LIMIT 1500) x ORDER BY ts`, sym)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []marketdata.Candle
		for rows.Next() {
			var c marketdata.Candle
			if err := rows.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
	vix := load("VIX.INDEX")
	if f := os.Getenv("FORECAST_VIX_JSON"); f != "" && len(vix) == 0 {
		raw, _ := os.ReadFile(f)
		var bars []struct {
			T string  `json:"t"`
			C float64 `json:"c"`
		}
		_ = json.Unmarshal(raw, &bars)
		for _, b := range bars {
			ts, _ := time.Parse(time.RFC3339, b.T)
			vix = append(vix, marketdata.Candle{Time: ts, Close: b.C})
		}
	}
	panel := forecast.NewPanel(load("GSPC.INDEX"), vix)

	earns := map[string][]forecast.Earning{}
	rows, err := db.QueryContext(ctx, `SELECT symbol, announced_at, surprise_pct FROM earnings_history`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		var at time.Time
		var sp sql.NullFloat64
		_ = rows.Scan(&s, &at, &sp)
		v := math.NaN()
		if sp.Valid {
			v = sp.Float64
		}
		earns[s] = append(earns[s], forecast.Earning{At: at, SurprisePct: v})
	}
	rows.Close()
	ins := map[string][]forecast.Insider{}
	rows, err = db.QueryContext(ctx, `SELECT symbol, filed_at, code = 'P', sum(value) FROM insider_trades
		WHERE code IN ('P','S') AND NOT plan_10b5_1 AND value IS NOT NULL GROUP BY accession, symbol, filed_at, code`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		var f forecast.Insider
		_ = rows.Scan(&s, &f.Filed, &f.Buy, &f.Value)
		ins[s] = append(ins[s], f)
	}
	rows.Close()
	master, err := company.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range master.Universe() {
		bars := load(sym)
		if len(bars) < 300 {
			continue
		}
		sector, _ := master.Sector(sym)
		panel.AddStock(sym, sector, bars, earns[sym], ins[sym])
	}
	t.Logf("panel: %d sessions, %d stocks", panel.T(), len(panel.Stocks))
	started := time.Now()
	limit := int64(640 << 20)
	if v := os.Getenv("FORECAST_MEMLIMIT_MB"); v != "" {
		var mb int64
		_, _ = fmt.Sscan(v, &mb)
		limit = mb << 20
	}
	debug.SetMemoryLimit(limit)
	rep, preds, err := forecast.Run(panel, forecast.Config{Now: time.Now(),
		Log: func(msg string, args ...any) { t.Log(append([]any{msg}, args...)...) }})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("elapsed %s, live heap after GC %d MB, heap peak sys %d MB", time.Since(started).Round(time.Second), ms.HeapInuse>>20, ms.HeapSys>>20)
	out, _ := json.MarshalIndent(map[string]any{"alpha": rep.Alpha, "vol": rep.Vol, "dist": rep.Dist, "years": rep.Years,
		"calibration": rep.Calibration, "regime": rep.Regime, "market": rep.Market, "factors": rep.Factors,
		"turnover": rep.Turnover, "momentum_turnover": rep.MomentumTurnover, "reliability": rep.Reliability}, "", " ")
	_ = os.WriteFile(os.Getenv("FORECAST_OUT"), out, 0o644)
	sample, _ := json.MarshalIndent(preds[:min(5, len(preds))], "", " ")
	_ = os.WriteFile(os.Getenv("FORECAST_OUT")+".preds", sample, 0o644)
	all, _ := json.Marshal(preds)
	_ = os.WriteFile(os.Getenv("FORECAST_OUT")+".all", all, 0o644)
}
