package scanner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// Earnings is one past earnings announcement as the price sidecar reports it.
type Earnings struct {
	Symbol      marketdata.Symbol
	AnnouncedAt time.Time
	EPSEstimate *float64
	EPSActual   float64
	SurprisePct *float64
}

// EarningsHistory fetches past earnings announcements for a batch of symbols,
// up to limit per symbol (about three years per twelve).
func (c *Client) EarningsHistory(ctx context.Context, symbols []marketdata.Symbol, limit int) ([]Earnings, []marketdata.Symbol, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return nil, nil, fmt.Errorf("scanner: no price service configured")
	}
	symbolOf := map[string]marketdata.Symbol{}
	var vendors []string
	for _, sym := range symbols {
		if v, ok := vendorTicker(sym); ok {
			vendors = append(vendors, v)
			symbolOf[v] = sym
		}
	}
	if len(vendors) == 0 {
		return nil, nil, fmt.Errorf("scanner: no symbols this provider can quote")
	}
	var parsed struct {
		Earnings map[string][]struct {
			AnnouncedAt string   `json:"announced_at"`
			EPSEstimate *float64 `json:"eps_estimate"`
			EPSActual   float64  `json:"eps_actual"`
			SurprisePct *float64 `json:"surprise_pct"`
		} `json:"earnings"`
		Failed []string `json:"failed"`
	}
	if err := c.post(ctx, "/earnings", map[string]any{"symbols": vendors, "limit": limit}, 32<<20, &parsed); err != nil {
		return nil, nil, err
	}
	var out []Earnings
	for vendor, rows := range parsed.Earnings {
		sym, ok := symbolOf[vendor]
		if !ok {
			continue
		}
		for _, r := range rows {
			at, err := time.Parse(time.RFC3339, r.AnnouncedAt)
			if err != nil {
				continue
			}
			out = append(out, Earnings{Symbol: sym, AnnouncedAt: at.UTC(), EPSEstimate: r.EPSEstimate, EPSActual: r.EPSActual, SurprisePct: r.SurprisePct})
		}
	}
	var failed []marketdata.Symbol
	for _, v := range parsed.Failed {
		if sym, ok := symbolOf[v]; ok {
			failed = append(failed, sym)
		}
	}
	return out, failed, nil
}
