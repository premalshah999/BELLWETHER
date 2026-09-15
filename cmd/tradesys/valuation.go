package main

import (
	"context"
	"fmt"

	"github.com/tradesys/dashboard/internal/research"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// storeValuations adapts the fundamentals store to what research needs.
//
// It lives here rather than in the research package because research must not
// import storage: storage already reaches research through the AI layer, and
// closing that loop is an import cycle. Wiring is main's job.
//
// It resolves the peer comparison into plain statements here rather than
// handing percentiles to the model. "In the most expensive quarter of its
// sector" is a fact this code can establish exactly; asking a language model
// to derive it from a percentile and a median is asking it to do arithmetic
// under a word limit, which is where it is least reliable.
type storeValuations struct {
	DB *postgres.DB
}

// Valuation implements ValuationSource.
func (v storeValuations) Valuation(ctx context.Context, symbol string) (*research.Valuation, error) {
	if v.DB == nil {
		return nil, fmt.Errorf("research: no fundamentals store")
	}
	snap, err := v.DB.LatestSnapshot(ctx, symbol)
	if err != nil {
		return nil, err
	}

	out := &research.Valuation{
		Symbol:        symbol,
		PE:            snap.PETrailing,
		PB:            snap.PriceToBook,
		EVToEBITDA:    snap.EVToEBITDA,
		ROE:           snap.ReturnOnEquity,
		OpMargin:      snap.OperatingMargin,
		DebtToEquity:  snap.DebtToEquity,
		DividendYield: snap.DividendYield,
	}

	pc, err := v.DB.ComparePeers(ctx, symbol)
	if err != nil {
		// No peer group is a normal outcome for anything outside the index.
		// The absolute ratios are still worth having.
		return out, nil
	}
	out.Industry = pc.Industry
	out.PeerCount = pc.Peers

	for _, st := range pc.Stats {
		if st.Value == nil || st.Percentile == nil || st.Median == nil {
			continue
		}
		switch st.Metric {
		case "P/E":
			out.PEMedian = st.Median
		case "Return on equity":
			out.ROEMedian = st.Median
		}
		out.Notes = append(out.Notes, peerNote(st))
	}
	return out, nil
}

// peerNote states one comparison in words.
func peerNote(st postgres.PeerStat) string {
	pct := *st.Percentile
	// Phrased by where it sits, not by the number. A reader wants "among the
	// cheapest quarter of its sector", not "27th percentile" — and the two
	// are the same fact.
	var band string
	switch {
	case pct >= 75:
		band = "the top quarter"
	case pct >= 50:
		band = "the upper half"
	case pct >= 25:
		band = "the lower half"
	default:
		band = "the bottom quarter"
	}

	quality := "higher than most peers"
	if pct < 50 {
		quality = "lower than most peers"
	}
	// For metrics where low is good, say so plainly rather than leaving the
	// reader to remember which direction each one runs.
	if !st.HigherIsBetter {
		if pct >= 50 {
			quality = "more expensive than most peers"
		} else {
			quality = "cheaper than most peers"
		}
	}

	value, median := formatPeerValue(st.Unit, *st.Value), formatPeerValue(st.Unit, *st.Median)
	return fmt.Sprintf("%s %s against an industry median of %s — %s, %s (%d peers)",
		st.Metric, value, median, band, quality, st.Peers)
}

// formatPeerValue renders a metric in the units it is actually stored in.
func formatPeerValue(unit string, v float64) string {
	switch unit {
	case "fraction":
		return fmt.Sprintf("%.1f%%", v*100)
	case "percent":
		return fmt.Sprintf("%.2f%%", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}
