package indicators

import (
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// TypicalPrice is (high + low + close) / 3, the price VWAP weights by volume.
func TypicalPrice(c marketdata.Candle) float64 { return (c.High + c.Low + c.Close) / 3 }

// VolAvg is the average volume over a trailing window. It is the basis for
// "volume is 1.5x its average" conditions.
func VolAvg(candles []marketdata.Candle, period int) Series {
	return SMA(Volumes(candles), period)
}

// SessionVWAP is the volume-weighted average price, reset at the start of each
// trading session (the calendar date in loc, the exchange's zone). It only
// makes sense on intraday bars; daily callers want RollingVWAP.
func SessionVWAP(candles []marketdata.Candle, loc *time.Location) Series {
	out := newSeries(len(candles))
	if loc == nil {
		loc = time.UTC
	}

	var (
		cumPV, cumVol float64
		currentDay    string
	)
	for i, c := range candles {
		day := c.Time.In(loc).Format("2006-01-02")
		if day != currentDay {
			cumPV, cumVol = 0, 0
			currentDay = day
		}
		if !IsDefined(c.High) || !IsDefined(c.Low) || !IsDefined(c.Close) || !IsDefined(c.Volume) {
			continue
		}
		cumPV += TypicalPrice(c) * c.Volume
		cumVol += c.Volume
		if cumVol > 0 {
			out[i] = cumPV / cumVol
		}
	}
	return out
}

// RollingVWAP is the volume-weighted average price over a trailing window of
// bars, ignoring session boundaries. It is the meaningful form of VWAP on
// daily data.
func RollingVWAP(candles []marketdata.Candle, period int) Series {
	out := newSeries(len(candles))
	if period <= 0 || len(candles) < period {
		return out
	}
	for i := period - 1; i < len(candles); i++ {
		var pv, vol float64
		ok := true
		for j := i - period + 1; j <= i; j++ {
			c := candles[j]
			if !IsDefined(c.High) || !IsDefined(c.Low) || !IsDefined(c.Close) || !IsDefined(c.Volume) {
				ok = false
				break
			}
			pv += TypicalPrice(c) * c.Volume
			vol += c.Volume
		}
		if ok && vol > 0 {
			out[i] = pv / vol
		}
	}
	return out
}
