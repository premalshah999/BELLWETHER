package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/tradesys/dashboard/internal/forecast"
	"github.com/tradesys/dashboard/internal/marketdata"
)

// SaveForecast stores a run and its predictions, replacing an earlier run for
// the same session.
func (d *DB) SaveForecast(ctx context.Context, rep forecast.Report, preds []forecast.Prediction) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	raw, _ := json.Marshal(rep)
	asOf := rep.AsOf.Format("2006-01-02")
	if _, err := tx.ExecContext(ctx, `DELETE FROM forecast_runs WHERE as_of = $1 AND horizon = $2`, asOf, rep.Horizon); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO forecast_runs (at, as_of, horizon, report) VALUES ($1, $2, $3, $4) RETURNING id`,
		rep.At, asOf, rep.Horizon, raw).Scan(&id); err != nil {
		return 0, fmt.Errorf("postgres: save forecast: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO forecast_predictions (run_id, symbol, score, percentile, drivers) VALUES ($1,$2,$3,$4,$5)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, p := range preds {
		drivers, _ := json.Marshal(p.Drivers)
		if _, err := stmt.ExecContext(ctx, id, p.Symbol, p.Score, p.Percentile, drivers); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// LatestForecast returns the newest run's report and its predictions, best first.
func (d *DB) LatestForecast(ctx context.Context) (*forecast.Report, []forecast.Prediction, error) {
	var id int64
	var raw []byte
	err := d.db.QueryRowContext(ctx, `SELECT id, report FROM forecast_runs ORDER BY as_of DESC, id DESC LIMIT 1`).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var rep forecast.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT symbol, score, percentile, drivers FROM forecast_predictions WHERE run_id = $1 ORDER BY score DESC`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []forecast.Prediction
	for rows.Next() {
		var p forecast.Prediction
		var drivers []byte
		if err := rows.Scan(&p.Symbol, &p.Score, &p.Percentile, &drivers); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal(drivers, &p.Drivers)
		out = append(out, p)
	}
	return &rep, out, rows.Err()
}

// SymbolForecasts is one stock's scores, newest first.
type SymbolForecast struct {
	AsOf time.Time `json:"as_of"`
	forecast.Prediction
}

// SymbolForecasts lists one stock's recent predictions.
func (d *DB) SymbolForecasts(ctx context.Context, symbol string, limit int) ([]SymbolForecast, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT r.as_of, p.symbol, p.score, p.percentile, p.drivers
FROM forecast_predictions p JOIN forecast_runs r ON r.id = p.run_id
WHERE p.symbol = $1 ORDER BY r.as_of DESC LIMIT $2`, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolForecast
	for rows.Next() {
		var f SymbolForecast
		var drivers []byte
		if err := rows.Scan(&f.AsOf, &f.Symbol, &f.Score, &f.Percentile, &drivers); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(drivers, &f.Drivers)
		out = append(out, f)
	}
	return out, rows.Err()
}

// LiveRun is one past run scored against what then happened.
type LiveRun struct {
	AsOf   time.Time `json:"as_of"`
	Stocks int       `json:"stocks"`
	RankIC float64   `json:"rank_ic"`
	// TopPct and AvgPct are the top tenth's and every stock's return over the
	// horizon; the difference is what following the model would have added.
	TopPct float64 `json:"top_pct"`
	AvgPct float64 `json:"avg_pct"`
}

// LiveForecasts scores every run whose horizon has passed, from the stored
// closes at the run's session and `horizon` sessions later.
func (d *DB) LiveForecasts(ctx context.Context, horizon int, since time.Time) ([]LiveRun, error) {
	// The sessions, from the benchmark's own bars.
	rows, err := d.db.QueryContext(ctx, `SELECT ts FROM candles WHERE symbol = 'GSPC.INDEX' AND interval = '1d' AND ts >= $1 ORDER BY ts`, since.AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	var sessions []time.Time
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		sessions = append(sessions, t)
	}
	rows.Close()
	index := map[string]int{}
	for i, t := range sessions {
		index[t.In(marketdata.Market).Format("2006-01-02")] = i
	}

	runs, err := d.db.QueryContext(ctx, `SELECT id, as_of FROM forecast_runs WHERE horizon = $1 AND as_of >= $2 ORDER BY as_of`, horizon, since)
	if err != nil {
		return nil, err
	}
	type run struct {
		id   int64
		asOf time.Time
	}
	var list []run
	for runs.Next() {
		var r run
		if err := runs.Scan(&r.id, &r.asOf); err != nil {
			runs.Close()
			return nil, err
		}
		list = append(list, r)
	}
	runs.Close()

	var out []LiveRun
	for _, r := range list {
		i, ok := index[r.asOf.Format("2006-01-02")]
		if !ok || i+horizon >= len(sessions) {
			continue
		}
		pr, err := d.db.QueryContext(ctx, `
SELECT p.score, e.close, x.close
FROM forecast_predictions p
JOIN candles e ON e.symbol = p.symbol AND e.interval = '1d' AND e.ts = $2
JOIN candles x ON x.symbol = p.symbol AND x.interval = '1d' AND x.ts = $3
WHERE p.run_id = $1 AND e.close > 0`, r.id, sessions[i], sessions[i+horizon])
		if err != nil {
			return nil, err
		}
		var scores, rets []float64
		for pr.Next() {
			var s, e, x float64
			if err := pr.Scan(&s, &e, &x); err != nil {
				pr.Close()
				return nil, err
			}
			scores, rets = append(scores, s), append(rets, x/e-1)
		}
		pr.Close()
		if len(scores) < 30 {
			continue
		}
		out = append(out, liveRun(r.asOf, scores, rets))
	}
	return out, nil
}

func liveRun(asOf time.Time, scores, rets []float64) LiveRun {
	idx := make([]int, len(scores))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	n := max(1, len(idx)/10)
	var top, all float64
	for k, i := range idx {
		if k < n {
			top += rets[i]
		}
		all += rets[i]
	}
	return LiveRun{AsOf: asOf, Stocks: len(scores), RankIC: math.Round(spearman(scores, rets)*10000) / 10000,
		TopPct: math.Round(top/float64(n)*10000) / 100, AvgPct: math.Round(all/float64(len(idx))*10000) / 100}
}

func spearman(a, b []float64) float64 {
	ra, rb := ranks(a), ranks(b)
	n := float64(len(a))
	var ma, mb float64
	for i := range ra {
		ma, mb = ma+ra[i], mb+rb[i]
	}
	ma, mb = ma/n, mb/n
	var sab, saa, sbb float64
	for i := range ra {
		sab += (ra[i] - ma) * (rb[i] - mb)
		saa += (ra[i] - ma) * (ra[i] - ma)
		sbb += (rb[i] - mb) * (rb[i] - mb)
	}
	if saa == 0 || sbb == 0 {
		return 0
	}
	return sab / math.Sqrt(saa*sbb)
}

func ranks(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, len(v))
	for r, i := range idx {
		out[i] = float64(r)
	}
	return out
}

// InsiderFlows returns every opportunistic open-market filing, for the model:
// buys of any size and sales not made under a 10b5-1 plan.
func (d *DB) InsiderFlows(ctx context.Context) (map[string][]forecast.Insider, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT symbol, filed_at, code = 'P', sum(value)
FROM insider_trades WHERE code IN ('P','S') AND NOT plan_10b5_1 AND value IS NOT NULL
GROUP BY accession, symbol, filed_at, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]forecast.Insider{}
	for rows.Next() {
		var sym string
		var f forecast.Insider
		var filed time.Time
		if err := rows.Scan(&sym, &filed, &f.Buy, &f.Value); err != nil {
			return nil, err
		}
		y, m, day := filed.Date()
		f.Filed = time.Date(y, m, day, 20, 0, 0, 0, marketdata.Market)
		out[sym] = append(out[sym], f)
	}
	return out, rows.Err()
}
