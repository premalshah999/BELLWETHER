package ai

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// Prices from DeepSeek's published list at peak, per million tokens. A change
// on their side should fail this test rather than silently mis-price the cap.
func TestPricesMatchDeepSeeksPublishedList(t *testing.T) {
	flash, ok := priceFor("deepseek-flash")
	if !ok || !near(flash.cacheHit, 0.006) || !near(flash.cacheMiss, 0.30) || !near(flash.output, 1.20) {
		t.Errorf("deepseek-flash = %+v", flash)
	}
	pro, ok := priceFor("deepseek-v4-pro")
	if !ok || !near(pro.cacheHit, 0.044) || !near(pro.cacheMiss, 1.32) || !near(pro.output, 3.96) {
		t.Errorf("deepseek-v4-pro = %+v", pro)
	}
}

// A versioned name returned by the API still prices as its base model, and
// the longer name wins so v4-pro is never read as something cheaper.
func TestVersionedModelNamesResolve(t *testing.T) {
	if p, ok := priceFor("deepseek-flash-2026-09"); !ok || !near(p.output, 1.20) {
		t.Errorf("versioned flash = %+v ok=%v", p, ok)
	}
	if p, ok := priceFor("deepseek-v4-pro-latest"); !ok || !near(p.output, 3.96) {
		t.Errorf("versioned pro = %+v ok=%v", p, ok)
	}
}

// A cap that stops counting when the model is renamed is not a cap. An unknown
// model is priced at the most expensive known rate.
func TestUnknownModelsArePricedAtTheDearestRate(t *testing.T) {
	p, known := priceFor("some-new-model")
	if known {
		t.Error("an unknown model must be reported as unknown")
	}
	if !near(p.output, 3.96) {
		t.Errorf("unknown model priced at %+v, want the dearest known rate", p)
	}
}

// Peak is 01:00-04:00 and 06:00-10:00 UTC on weekdays; everything else is off.
func TestPeakHours(t *testing.T) {
	mon := func(h int) time.Time { return time.Date(2026, 9, 28, h, 30, 0, 0, time.UTC) } // a Monday
	for h, want := range map[int]bool{0: false, 1: true, 3: true, 4: false, 5: false, 6: true, 9: true, 10: false, 15: false} {
		if got := isPeak(mon(h)); got != want {
			t.Errorf("Monday %02d:30 UTC peak = %v, want %v", h, got, want)
		}
	}
	sat := time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC)
	if isPeak(sat) {
		t.Error("weekends are off-peak")
	}
}

// The two cases that matter most: off-peak halves the bill, and an unreported
// cache split is priced as all misses -- the safe side for a cap.
func TestCostUsesTheRateInForceAndTheCacheSplit(t *testing.T) {
	peak := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	off := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	split := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000,
		CacheHitTokens: 800_000, CacheMissTokens: 200_000}
	// 0.8*0.006 + 0.2*0.30 + 1*1.20 = 1.2648 at peak
	if got := split.CostUSD("deepseek-flash", peak); !near(got, 1.2648) {
		t.Errorf("peak with split = %v, want 1.2648", got)
	}
	if got := split.CostUSD("deepseek-flash", off); !near(got, 0.6324) {
		t.Errorf("off-peak with split = %v, want half of peak, 0.6324", got)
	}

	unsplit := Usage{PromptTokens: 1_000_000, CompletionTokens: 0}
	if got := unsplit.CostUSD("deepseek-flash", peak); !near(got, 0.30) {
		t.Errorf("no split reported = %v, want every token priced as a miss, 0.30", got)
	}
}

// The response carries the cache split in either of two places depending on
// which version of the API answered. Both must be read.
func TestCacheSplitIsReadFromEitherLayout(t *testing.T) {
	for name, body := range map[string]string{
		"top level": `{"model":"deepseek-flash","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,
			"prompt_cache_hit_tokens":70,"prompt_cache_miss_tokens":30}}`,
		"nested": `{"model":"deepseek-flash","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,
			"prompt_tokens_details":{"prompt_cache_hit_tokens":70,"prompt_cache_miss_tokens":30}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, body)
			}))
			defer srv.Close()
			budget := newMemBudget()
			c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "deepseek-flash", MonthlyLimit: 1e9},
				budget, quiet())
			if _, err := c.Complete(context.Background(), simpleRequest("hi")); err != nil {
				t.Fatal(err)
			}
			rec := budget.records[len(budget.records)-1]
			if rec.Usage.CacheHitTokens != 70 || rec.Usage.CacheMissTokens != 30 {
				t.Errorf("split = %d hit / %d miss, want 70/30", rec.Usage.CacheHitTokens, rec.Usage.CacheMissTokens)
			}
			if rec.CostUSD <= 0 {
				t.Error("a completed call must record a cost")
			}
		})
	}
}

// The cap refuses a call that could take the day past it, before sending it.
func TestDailyCapRefusesBeforeSpending(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		io.WriteString(w, `{"model":"deepseek-flash","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`)
	}))
	defer srv.Close()

	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	budget := newMemBudget()
	// Already $0.999 spent today.
	budget.records = append(budget.records, UsageRecord{At: now.Add(-time.Hour), CostUSD: 0.999})

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "deepseek-flash", MonthlyLimit: 1e9},
		budget, quiet(), WithDailyCapUSD(1.00), WithClock(func() time.Time { return now }))

	req := simpleRequest("hi")
	req.MaxTokens = 4000 // worst case 4000 * $1.20/M = $0.0048, which crosses the cap
	_, err := c.Complete(context.Background(), req)
	if !errors.Is(err, ErrDailyCapReached) {
		t.Fatalf("want ErrDailyCapReached, got %v", err)
	}
	if calls != 0 {
		t.Errorf("the provider was called %d times; a capped call must not be sent", calls)
	}
}

// Yesterday's spend does not count against today: the cap resets at UTC midnight.
func TestDailyCapResetsAtUTCMidnight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"model":"deepseek-flash","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`)
	}))
	defer srv.Close()

	now := time.Date(2026, 9, 28, 0, 5, 0, 0, time.UTC) // five past midnight
	budget := newMemBudget()
	budget.records = append(budget.records, UsageRecord{At: now.Add(-10 * time.Minute), CostUSD: 5.00})

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "deepseek-flash", MonthlyLimit: 1e9},
		budget, quiet(), WithDailyCapUSD(1.00), WithClock(func() time.Time { return now }))
	if _, err := c.Complete(context.Background(), simpleRequest("hi")); err != nil {
		t.Fatalf("yesterday's $5 must not block today: %v", err)
	}
}

// The property the reservation exists for. Twenty concurrent calls, each able
// to cost up to $0.12, against a $1 cap: without reserving under the same lock
// as the check, every one of them would read "$0 spent" and all twenty would
// go out. With it, at most eight can be in flight at once.
func TestDailyCapHoldsUnderConcurrency(t *testing.T) {
	release := make(chan struct{})
	var inFlight, sent int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&sent, 1)
		atomic.AddInt32(&inFlight, 1)
		<-release
		io.WriteString(w, `{"model":"deepseek-flash","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "deepseek-flash", MonthlyLimit: 1e9},
		newMemBudget(), quiet(), WithDailyCapUSD(1.00))

	req := simpleRequest("hi")
	req.MaxTokens = 100_000 // worst case $0.12 each

	var wg sync.WaitGroup
	var refused int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Complete(context.Background(), req); errors.Is(err, ErrDailyCapReached) {
				atomic.AddInt32(&refused, 1)
			}
		}()
	}
	// Let every goroutine reach the check before any call returns.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&refused)+atomic.LoadInt32(&inFlight) < 20 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	if sent > 8 {
		t.Errorf("%d calls went out against a $1 cap at $0.12 each; at most 8 fit", sent)
	}
	if refused == 0 {
		t.Error("no call was refused; the reservation is not holding")
	}
}
