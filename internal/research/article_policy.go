package research

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/temoto/robotstxt"
)

const researchAgent = "BellwetherResearch/1.0"

type articleCacheEntry struct {
	article Article
	err     error
	expires time.Time
}
type publisherState struct {
	users int

	gate         chan struct{}
	robots       *robotstxt.RobotsData
	robotsUntil  time.Time
	robotsOrigin string
	next         time.Time
}

func (f *ArticleFetcher) publisher(host string) (*publisherState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishers == nil {
		f.publishers = make(map[string]*publisherState)
	}
	if p := f.publishers[host]; p != nil {
		p.users++
		return p, nil
	}
	// Do not grow indefinitely on arbitrary search-result hosts. Old idle
	// states may be removed only after their robots/cache interval expires.
	if len(f.publishers) >= 256 {
		for h, p := range f.publishers {
			if p.users > 0 {
				continue
			}
			select {
			case p.gate <- struct{}{}:
				if time.Now().After(p.robotsUntil) && time.Now().After(p.next) {
					delete(f.publishers, h)
				}
				<-p.gate
			default:
			}
		}
		if len(f.publishers) >= 256 {
			return nil, fmt.Errorf("article: publisher capacity reached; retry later")
		}
	}
	p := &publisherState{users: 1, gate: make(chan struct{}, 1)}
	f.publishers[host] = p
	return p, nil
}

func (f *ArticleFetcher) cached(raw string) (Article, error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[raw]
	if !ok || time.Now().After(e.expires) {
		return Article{}, nil, false
	}
	a := e.article
	a.Cached = true
	return a, e.err, true
}

func (f *ArticleFetcher) remember(raw string, a Article, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cache == nil {
		f.cache = make(map[string]articleCacheEntry)
	}
	if len(f.cache) >= 256 {
		oldestKey := ""
		var oldest time.Time
		for k, e := range f.cache {
			if oldestKey == "" || e.expires.Before(oldest) {
				oldestKey, oldest = k, e.expires
			}
		}
		delete(f.cache, oldestKey)
	}
	ttl := 15 * time.Minute
	if err != nil {
		ttl = time.Minute
	}
	f.cache[raw] = articleCacheEntry{a, err, time.Now().Add(ttl)}
}

func waitUntil(ctx context.Context, at time.Time) error {
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return ctx.Err()
}

func (f *ArticleFetcher) request(ctx context.Context, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	agent := researchAgent
	if f.UserAgent != "" && (u.Hostname() == "sec.gov" || strings.HasSuffix(u.Hostname(), ".sec.gov")) {
		agent += " " + f.UserAgent
	}
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	client := f.HTTP
	if client == nil {
		client = &http.Client{Transport: publicTransport(), Timeout: articleTimeout}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c.Do(req)
}

// Called with the publisher gate held. Every article redirect returns through
// this policy, including the destination's robots rules and pacing.
func (f *ArticleFetcher) allowed(ctx context.Context, p *publisherState, u *url.URL) error {
	if err := waitUntil(ctx, p.next); err != nil {
		return err
	}
	origin := u.Scheme + "://" + u.Host
	if time.Now().After(p.robotsUntil) || p.robotsOrigin != origin {
		ru := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}
		var robots *robotstxt.RobotsData
		for n := 0; n < 6; n++ {
			resp, err := f.request(ctx, ru)
			if err != nil {
				return fmt.Errorf("article: robots unavailable: %w", err)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, (512<<10)+1))
			resp.Body.Close()
			if err != nil {
				return err
			}
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				next, err := resp.Location()
				if err != nil {
					return err
				}
				ru, err = articleURL(next.String())
				if err != nil {
					return err
				}
				continue
			}
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				p.next = retryAt(resp)
				return fmt.Errorf("article: robots temporarily unavailable (%d)", resp.StatusCode)
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return fmt.Errorf("article: robots access denied")
			}
			if len(body) > 512<<10 {
				return fmt.Errorf("article: robots exceeds size limit")
			}
			if resp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
				return fmt.Errorf("article: robots endpoint returned HTML instead of rules")
			}
			robots, err = robotstxt.FromStatusAndBytes(resp.StatusCode, body)
			if err != nil {
				return fmt.Errorf("article: robots could not be parsed: %w", err)
			}
			break
		}
		if robots == nil {
			return fmt.Errorf("article: robots redirect limit exceeded")
		}
		p.robots, p.robotsUntil = robots, time.Now().Add(time.Hour)
		p.robotsOrigin = origin
	}
	group := p.robots.FindGroup("BellwetherResearch")
	if !group.Test(u.RequestURI()) {
		return fmt.Errorf("article: publisher disallows automated reading")
	}
	if err := waitUntil(ctx, p.next); err != nil {
		return err
	}
	p.next = time.Now().Add(max(time.Second, group.CrawlDelay))
	return nil
}

func retryAt(resp *http.Response) time.Time {
	now := time.Now()
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if t, err := http.ParseTime(value); err == nil && t.After(now) {
		return t
	}
	return now.Add(5 * time.Minute)
}
