package research

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type discoveryHost struct {
	gate     chan struct{}
	next     time.Time
	cooldown time.Time
}

// DiscoveryTransport coordinates separate search adapters that hit the same
// upstream. Publisher-scoped Google queries are still requests to one host.
type DiscoveryTransport struct {
	Base  http.RoundTripper
	mu    sync.Mutex
	hosts map[string]*discoveryHost
}

func (d *DiscoveryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.ToLower(req.URL.Hostname())
	d.mu.Lock()
	if d.hosts == nil {
		d.hosts = map[string]*discoveryHost{}
	}
	h := d.hosts[host]
	if h == nil {
		h = &discoveryHost{gate: make(chan struct{}, 2)}
		d.hosts[host] = h
	}
	d.mu.Unlock()
	select {
	case h.gate <- struct{}{}:
		defer func() { <-h.gate }()
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	interval := 500 * time.Millisecond
	if host == "api.gdeltproject.org" {
		interval = 5500 * time.Millisecond
	}
	for {
		d.mu.Lock()
		at := h.next
		if h.cooldown.After(at) {
			at = h.cooldown
		}
		if !time.Now().Before(at) {
			h.next = time.Now().Add(interval)
			d.mu.Unlock()
			break
		}
		d.mu.Unlock()
		if err := waitUntil(req.Context(), at); err != nil {
			return nil, err
		}
	}
	base := d.Base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err == nil && (resp.StatusCode == 429 || resp.StatusCode == 503) {
		d.mu.Lock()
		at := retryAt(resp)
		if at.After(h.cooldown) {
			h.cooldown = at
		}
		d.mu.Unlock()
	}
	return resp, err
}
