package research

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type searchEntry struct {
	findings []Finding
	err      error
	until    time.Time
}

// Each provider has a small cache and one in-flight request. Repeated questions
// reuse discovery; failures briefly cool down the provider across queries.
type cachedScraper struct {
	Scraper
	gate     chan struct{}
	mu       sync.Mutex
	entries  map[string]searchEntry
	cooldown time.Time
	lastErr  error
}

func cacheScraper(s Scraper) Scraper {
	return &cachedScraper{Scraper: s, gate: make(chan struct{}, 1), entries: map[string]searchEntry{}}
}

func (c *cachedScraper) Search(ctx context.Context, query string, limit int) ([]Finding, error) {
	key := fmt.Sprintf("%d:%s", limit, strings.Join(strings.Fields(query), " "))
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && time.Now().Before(entry.until) {
		out := append([]Finding(nil), entry.findings...)
		c.mu.Unlock()
		return out, entry.err
	}
	if time.Now().Before(c.cooldown) {
		err := c.lastErr
		c.mu.Unlock()
		return nil, fmt.Errorf("provider cooling down: %w", err)
	}
	c.mu.Unlock()
	out, err := c.Scraper.Search(ctx, query, limit)
	if ctx.Err() != nil {
		return out, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.cooldown = time.Now().Add(time.Minute)
		c.lastErr = err
		return out, err
	}
	if len(out) > limit {
		out = out[:limit]
	}
	if len(c.entries) >= 32 {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = searchEntry{append([]Finding(nil), out...), nil, time.Now().Add(2 * time.Minute)}
	return out, nil
}

// A provider adapter failing must not crash the process from a worker goroutine.
func searchProvider(ctx context.Context, sc Scraper, query string, limit int) (findings []Finding, err error) {
	defer func() {
		if value := recover(); value != nil {
			findings = nil
			err = fmt.Errorf("provider failed unexpectedly")
		}
	}()
	return sc.Search(ctx, query, limit)
}
