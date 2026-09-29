package ai

import (
	"strings"
	"time"
)

// modelPrice is a model's list price at peak, in USD per million tokens.
type modelPrice struct {
	cacheHit  float64 // input served from DeepSeek's context cache
	cacheMiss float64 // input that was not
	output    float64
}

// deepseekPeak is DeepSeek's published price list at peak, from
// https://api-docs.deepseek.com/quick_start/pricing. Off-peak is half of these.
//
// Cache hits cost a fiftieth of cache misses, which is why the split matters:
// a prompt that reuses the same long system text across calls is mostly hits,
// and pricing it as all-miss would overstate its cost by an order of magnitude.
// That overstatement is still safe -- it can only make the cap trip early --
// and it is what happens when the response does not report the split.
var deepseekPeak = map[string]modelPrice{
	"deepseek-flash":  {cacheHit: 0.006, cacheMiss: 0.30, output: 1.20},
	"deepseek-v4-pro": {cacheHit: 0.044, cacheMiss: 1.32, output: 3.96},
}

// priceFor finds a model's price. The response may name a versioned model, so
// a known name contained in it counts.
//
// An unknown model is priced at the most expensive known rate rather than at
// zero. A cap that silently stops counting when the model name changes is not
// a cap; one that over-counts after a rename is merely cautious.
func priceFor(model string) (modelPrice, bool) {
	m := strings.ToLower(model)
	if p, ok := deepseekPeak[m]; ok {
		return p, true
	}
	// Longest match first, so "deepseek-v4-pro" is not read as a shorter name.
	best := ""
	for name := range deepseekPeak {
		if strings.Contains(m, name) && len(name) > len(best) {
			best = name
		}
	}
	if best != "" {
		return deepseekPeak[best], true
	}
	var dearest modelPrice
	for _, p := range deepseekPeak {
		if p.output > dearest.output {
			dearest = p
		}
	}
	return dearest, false
}

// isPeak reports whether DeepSeek bills a moment at the peak rate: 01:00-04:00
// and 06:00-10:00 UTC, Monday to Friday.
//
// DeepSeek also discounts Chinese public holidays. Those are treated as peak
// here, which overstates cost on a handful of days a year -- the safe side to
// be wrong on, for something whose job is to stop spending.
func isPeak(t time.Time) bool {
	u := t.UTC()
	if u.Weekday() == time.Saturday || u.Weekday() == time.Sunday {
		return false
	}
	h := u.Hour()
	return (h >= 1 && h < 4) || (h >= 6 && h < 10)
}

// CostUSD prices a completed call at the rate in force when it was made.
//
// Input is split into cache hits and misses when the response reported the
// split. When it did not, all of it is priced as a miss.
func (u Usage) CostUSD(model string, at time.Time) float64 {
	p, _ := priceFor(model)
	scale := 1.0
	if !isPeak(at) {
		scale = 0.5
	}

	hit, miss := u.CacheHitTokens, u.CacheMissTokens
	if hit+miss == 0 {
		// No split reported: price everything as a miss.
		miss = u.PromptTokens
	}
	cost := float64(hit)*p.cacheHit + float64(miss)*p.cacheMiss + float64(u.CompletionTokens)*p.output
	return cost * scale / 1e6
}

// estimateCostUSD is the most a call could cost before it is made.
//
// Deliberately the ceiling rather than the likely figure: peak rate, every
// input token a cache miss, and the full output allowance spent. The cap is
// checked against this, so the cap cannot be crossed by a call that turns out
// more expensive than expected -- the only way to be surprised is downward.
func estimateCostUSD(model string, promptTokens, maxOutput int) float64 {
	p, _ := priceFor(model)
	return (float64(promptTokens)*p.cacheMiss + float64(max(0, maxOutput))*p.output) / 1e6
}

// startOfDay is the UTC midnight a moment falls in. The cap resets at UTC
// midnight because DeepSeek's own peak hours are defined in UTC; a cap on a
// different clock would split one billing day across two cap days.
func startOfDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
