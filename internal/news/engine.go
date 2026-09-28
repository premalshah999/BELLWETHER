package news

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// RawStore persists fetched items and per-source health. Declared here, at the
// point of use, so this package does not depend on the storage package.
type RawStore interface {
	// SaveRawItems inserts items, ignoring ones already held, and reports how
	// many were new. Re-polling a feed must converge rather than accumulate.
	SaveRawItems(ctx context.Context, items []RawItem) (added int, err error)
	// LoadSourceHealth returns the stored health of every known source.
	LoadSourceHealth(ctx context.Context) ([]SourceHealth, error)
	// SaveSourceHealth records the outcome of one fetch.
	SaveSourceHealth(ctx context.Context, h SourceHealth) error
}

// SourceHealth is what the scheduler knows about one source's recent history.
type SourceHealth struct {
	SourceID string `json:"source_id"`

	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastFailureAt time.Time `json:"last_failure_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`

	ConsecutiveFailures int `json:"consecutive_failures"`
	TotalAttempts       int `json:"total_attempts"`
	TotalSuccesses      int `json:"total_successes"`
	TotalItems          int `json:"total_items"`
	TotalNewItems       int `json:"total_new_items"`

	// Conditional-request tokens. Sending these back is the difference
	// between being a well-behaved consumer of a free feed and being blocked
	// by its publisher.
	ETag         string `json:"-"`
	LastModified string `json:"-"`
}

// Healthy reports whether the source is currently delivering.
func (h SourceHealth) Healthy() bool { return h.ConsecutiveFailures == 0 }

// Engine fetches every registered source on its own cadence.
//
// The design targets a single process serving a couple of operators, so it
// keeps its scheduling state in memory and persists only what must survive a
// restart. Everything expensive is bounded: total concurrency, per-host
// concurrency, per-request time, and the rate at which a failing source is
// retried.
type Engine struct {
	registry *Registry
	store    RawStore
	http     *http.Client
	log      *slog.Logger
	now      func() time.Time
	rand     *rand.Rand

	workSlots   chan struct{}
	workers     int
	perHost     int
	istLocation *time.Location
	// heat, when set, lets the scheduler poll sources covering eventful
	// instruments more often. Optional: the engine schedules perfectly well
	// without it, just less cleverly.
	heat *Tracker

	mu     sync.Mutex
	state  map[string]*sourceState
	hosts  map[string]chan struct{}
	inWork map[string]bool
}

// sourceState is the scheduler's in-memory view of one source.
type sourceState struct {
	nextDue time.Time
	health  SourceHealth
	// emptyPolls counts consecutive successful fetches that produced nothing
	// new. It drives the back-off that lets a quiet feed drift towards its
	// lane ceiling instead of being asked at full rate forever.
	emptyPolls int
	// lastInterval is what the scheduler last chose, exposed for the health
	// page so the adaptation is visible rather than mysterious.
	lastInterval time.Duration
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithEngineLogger sets the logger.
func WithEngineLogger(l *slog.Logger) EngineOption { return func(e *Engine) { e.log = l } }

// WithEngineClock replaces the clock, for tests.
func WithEngineClock(now func() time.Time) EngineOption {
	return func(e *Engine) { e.now = now }
}

// defaultFetchClient is the HTTP client the engine fetches with.
//
// Built explicitly rather than taking http.DefaultTransport, for two reasons
// that were both costing us a source.
//
// Go's default TLS handshake timeout is ten seconds. GDELT is frequently slow
// to negotiate — measured from this host it takes about 25 seconds to answer
// at all — so every poll of it failed on the handshake and it accumulated 44
// consecutive failures without ever being the kind of failure a circuit
// breaker should act on.
//
// And the client-level Timeout is a ceiling over the per-source deadline, not
// an alternative to it. At 45 seconds it silently capped the 90 seconds the
// GDELT source asks for, so a timeout written in the catalog was not the
// timeout being used. Per-request deadlines come from the source's own
// Timeout, applied through the context; the client-level one exists only to
// stop a connection hanging forever, and so sits above every source's value.
func defaultFetchClient() *http.Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			// Thirty seconds because GDELT, specifically, is slow to accept a
			// connection at all from inside a container — not slow to answer,
			// slow to complete the TCP handshake. Every other source in the
			// catalog connects in well under a second, so this ceiling costs
			// nothing except on the one endpoint that needs it.
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: tr, Timeout: 120 * time.Second}
}

// WithEngineHTTPClient replaces the HTTP client.
func WithEngineHTTPClient(c *http.Client) EngineOption {
	return func(e *Engine) { e.http = c }
}

// WithWorkers bounds how many sources are fetched at once.
func WithWorkers(n int) EngineOption {
	return func(e *Engine) {
		if n > 0 {
			e.workers = n
		}
	}
}

// WithHeatTracker supplies the attention tracker that promotes sources
// covering instruments something is currently happening to.
func WithHeatTracker(t *Tracker) EngineOption {
	return func(e *Engine) { e.heat = t }
}

// WithPerHostLimit bounds concurrent requests to any single host.
func WithPerHostLimit(n int) EngineOption {
	return func(e *Engine) {
		if n > 0 {
			e.perHost = n
		}
	}
}

// Engine defaults. Twelve concurrent fetches is comfortably enough to clear a
// catalog of this size within one scheduling tick, and two per host keeps us
// from looking like a burst of traffic to any one publisher.
const (
	defaultWorkers = 12
	defaultPerHost = 2
)

// NewEngine builds an engine over a registry.
// Registry returns the source registry this engine schedules from.
//
// Exposed so that whatever decides a company needs watching can add a source
// for it and have the engine pick it up on the next tick.
func (e *Engine) Registry() *Registry { return e.registry }

func NewEngine(registry *Registry, store RawStore, opts ...EngineOption) *Engine {
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		ist = time.FixedZone("IST", 5*3600+1800)
	}
	e := &Engine{
		registry:    registry,
		store:       store,
		http:        defaultFetchClient(),
		log:         slog.Default(),
		now:         time.Now,
		rand:        rand.New(rand.NewSource(time.Now().UnixNano())),
		workers:     defaultWorkers,
		perHost:     defaultPerHost,
		istLocation: ist,
		state:       map[string]*sourceState{},
		hosts:       map[string]chan struct{}{},
		inWork:      map[string]bool{},
	}
	for _, opt := range opts {
		opt(e)
	}
	e.workSlots = make(chan struct{}, e.workers)
	return e
}

// Prime loads stored health so a restart does not re-hammer sources that were
// failing, and does not lose the conditional-request tokens that keep our
// traffic cheap for publishers.
func (e *Engine) Prime(ctx context.Context) error {
	stored, err := e.store.LoadSourceHealth(ctx)
	if err != nil {
		return fmt.Errorf("news: load source health: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, h := range stored {
		src, known := e.registry.Get(h.SourceID)
		if !known {
			continue // a source that has since been removed from the catalog
		}
		st := &sourceState{health: h}
		// A source that was failing when we shut down resumes inside its
		// backoff rather than being retried immediately on boot.
		if h.ConsecutiveFailures > 0 && !h.LastFailureAt.IsZero() {
			st.nextDue = h.LastFailureAt.Add(e.backoff(h.ConsecutiveFailures, src.Refresh))
		}
		e.state[h.SourceID] = st
	}
	return nil
}

// RunResult summarises one pass over the due sources.
type RunResult struct {
	Attempted int
	Succeeded int
	Failed    int
	Items     int
	NewItems  int
	Skipped   int // not yet due, or held open by the circuit breaker
	Duration  time.Duration
}

// RunOnce fetches every source that is currently due.
//
// It returns after all of them settle. Failures are recorded and reported but
// never returned as an error: one publisher being down is an expected
// condition of aggregating hundreds of them, not a fault in the run.
func (e *Engine) RunOnce(ctx context.Context) RunResult {
	start := e.now()
	due, skipped := e.dueSources()

	result := RunResult{Skipped: skipped, Attempted: len(due)}
	if len(due) == 0 {
		result.Duration = e.now().Sub(start)
		return result
	}

	var (
		mu   sync.Mutex
		jobs = make(chan Source)
		wg   sync.WaitGroup
	)
	workers := e.workers
	if workers > len(due) {
		workers = len(due)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for src := range jobs {
				items, added, err := e.fetchSource(ctx, src)
				mu.Lock()
				if err != nil {
					result.Failed++
				} else {
					result.Succeeded++
					result.Items += items
					result.NewItems += added
				}
				mu.Unlock()
			}
		}()
	}
	for i, src := range due {
		select {
		case <-ctx.Done():
			e.mu.Lock()
			for _, pending := range due[i:] {
				delete(e.inWork, pending.ID)
			}
			e.mu.Unlock()
		case jobs <- src:
			continue
		}
		break
	}
	close(jobs)
	wg.Wait()

	result.Duration = e.now().Sub(start)
	return result
}

// Run fetches on a ticker until the context is cancelled.
// Run keeps scheduling while slow sources finish. The shared worker gate
// bounds all batches together; per-source inWork prevents duplicate requests.
func (e *Engine) Run(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = 15 * time.Second
	}
	timer := time.NewTicker(tick)
	defer timer.Stop()
	var batches sync.WaitGroup
	launch := func() {
		batches.Add(1)
		go func() {
			defer batches.Done()
			res := e.RunOnce(ctx)
			if res.Attempted > 0 {
				e.log.Info("news ingest pass", "attempted", res.Attempted, "ok", res.Succeeded, "failed", res.Failed, "new", res.NewItems, "took", res.Duration.Round(time.Millisecond))
			}
		}()
	}
	launch()
	for {
		select {
		case <-ctx.Done():
			batches.Wait()
			return
		case <-timer.C:
			launch()
		}
	}
}

// dueSources returns the sources whose cadence has come round, most overdue
// first so a backlog drains in the order it accumulated.
func (e *Engine) dueSources() (due []Source, skipped int) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()

	type candidate struct {
		src  Source
		when time.Time
	}
	var cands []candidate
	for _, src := range e.registry.Fetchable() {
		st, ok := e.state[src.ID]
		if !ok {
			st = &sourceState{health: SourceHealth{SourceID: src.ID}}
			e.state[src.ID] = st
		}
		if e.inWork[src.ID] {
			// Already being fetched. Two concurrent fetches of one source
			// would race on its conditional-request tokens and could store
			// the same item twice, so the second is simply skipped.
			skipped++
			continue
		}
		if !st.nextDue.IsZero() && now.Before(st.nextDue) {
			skipped++
			continue
		}
		cands = append(cands, candidate{src, st.nextDue})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].when.Before(cands[j].when) })
	for _, c := range cands {
		e.inWork[c.src.ID] = true
		due = append(due, c.src)
	}
	return due, skipped
}

// fetchSource performs one fetch and records its outcome.
func (e *Engine) fetchSource(ctx context.Context, src Source) (items, added int, err error) {
	defer func() {
		e.mu.Lock()
		delete(e.inWork, src.ID)
		e.mu.Unlock()
	}()

	select {
	case e.workSlots <- struct{}{}:
		defer func() { <-e.workSlots }()
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	}
	e.mu.Lock()
	st := e.state[src.ID]
	etag, lastMod := st.health.ETag, st.health.LastModified
	e.mu.Unlock()

	timeout := src.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, newETag, newLastMod, notModified, err := e.get(reqCtx, src, etag, lastMod)
	now := e.now()

	if err != nil {
		e.recordFailure(src, now, err)
		e.log.Debug("source fetch failed", "source", src.ID, "err", err)
		return 0, 0, err
	}

	// 304 is a success that saved everyone the bandwidth. Nothing changed,
	// so there is nothing to parse and nothing to store.
	if notModified {
		e.recordSuccess(src, now, 0, 0, newETag, newLastMod)
		return 0, 0, nil
	}

	parsed, err := e.parse(src, body, now)
	if err != nil {
		e.recordFailure(src, now, err)
		e.log.Debug("source parse failed", "source", src.ID, "err", err)
		return 0, 0, err
	}

	added, err = e.store.SaveRawItems(ctx, parsed)
	if err != nil {
		e.recordFailure(src, now, err)
		return len(parsed), 0, err
	}
	e.recordSuccess(src, now, len(parsed), added, newETag, newLastMod)
	return len(parsed), added, nil
}

// get performs the HTTP request, honouring conditional-request tokens.
func (e *Engine) get(ctx context.Context, src Source, etag, lastMod string) (body []byte, newETag, newLastMod string, notModified bool, err error) {
	release, err := e.acquireHost(ctx, src.URL)
	if err != nil {
		return nil, "", "", false, err
	}
	defer release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("news: build request for %s: %w", src.ID, err)
	}
	// No User-Agent is sent, and that is a considered choice rather than an
	// oversight.
	//
	// Measured against the live catalog, three header strategies behave very
	// differently. Go's default "Go-http-client" and any self-identifying
	// string are refused outright by NSE, which drops the connection, and by
	// Business Standard, which answers 403 — those publishers block on known
	// bot patterns. A copied Chrome string gets through everywhere, but it is
	// a claim to be software we are not.
	//
	// Sending nothing works against every source in the catalog and asserts
	// nothing untrue. We stay a good citizen where it actually counts:
	// conditional requests, bounded per-host concurrency, cadences matched to
	// how often a feed really changes, and a circuit breaker that backs off
	// rather than retrying a struggling host.
	req.Header.Set("User-Agent", src.UserAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, application/json;q=0.9, */*;q=0.8")
	// Follows the source rather than being fixed at en-IN, which is what it
	// was when every publisher in the catalog was Indian. A publisher that
	// varies content or edition by locale should be asked in the locale it
	// actually serves.
	req.Header.Set("Accept-Language", acceptLanguageFor(src))
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("news: fetch %s: %w", src.ID, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, lastMod, true, nil
	}
	// Being rate-limited is a distinct condition from being broken. It says
	// the source is healthy and we are asking too often, so it is reported as
	// its own error and earns a much longer wait than an ordinary failure.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", "", false, &RateLimitedError{
			SourceID:   src.ID,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), e.now()),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", false, fmt.Errorf("news: %s returned %s", src.ID, resp.Status)
	}

	// Feeds are small; a runaway response is a bug or an attack, not content.
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return nil, "", "", false, fmt.Errorf("news: read %s: %w", src.ID, err)
	}
	if len(body) == 0 {
		return nil, "", "", false, fmt.Errorf("news: %s returned an empty body", src.ID)
	}
	return body, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), false, nil
}

const (
	// maxFeedBytes caps one response. NSE's announcements feed is the largest
	// we carry at roughly 600KB, so this leaves an order of magnitude spare.
	maxFeedBytes = 8 << 20
)

// acquireHost bounds concurrency per upstream host.
func (e *Engine) acquireHost(ctx context.Context, rawURL string) (func(), error) {
	host := hostOf(rawURL)
	if host == "" {
		return func() {}, nil
	}
	e.mu.Lock()
	sem, ok := e.hosts[host]
	if !ok {
		sem = make(chan struct{}, e.perHost)
		e.hosts[host] = sem
	}
	e.mu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// parse turns a response body into raw items, dispatching on the source's
// method. Every item is stamped with the same discovery time: they became
// knowable to this system at the moment the response arrived, not at the
// moment the parser reached them.
func (e *Engine) parse(src Source, body []byte, discoveredAt time.Time) ([]RawItem, error) {
	switch src.Method {
	case MethodRSS, MethodGoogleNews, MethodSECFiling:
		return e.parseFeedItems(src, body, discoveredAt)
	case MethodGDELT:
		return e.parseGDELT(src, body, discoveredAt)
	case MethodFederalRegister:
		return e.parseFederalRegister(src, body, discoveredAt)
	default:
		return nil, fmt.Errorf("news: source %s has unsupported method %q", src.ID, src.Method)
	}
}

func (e *Engine) parseFeedItems(src Source, body []byte, discoveredAt time.Time) ([]RawItem, error) {
	parsed, feedTitle, err := ParseFeed(body)
	if err != nil {
		return nil, err
	}
	// Every feed now either carries an explicit zone or is close enough to
	// UTC not to matter. The one that did not was NSE, which stamped its
	// filings in IST without saying so; that special case went with the
	// exchange.
	loc := time.UTC

	trust := src.TimestampTrust()
	out := make([]RawItem, 0, len(parsed))
	for _, p := range parsed {
		// Always reparse against the source's own timezone rather than
		// accepting the parser's UTC reading. A zoneless stamp parses
		// *successfully* as UTC, so treating that as good enough would mean
		// the IST correction never runs for the one source that needs it.
		// Formats carrying an explicit offset are unaffected: parseFeedTimeIn
		// tries those first and the location is then ignored.
		published := parseFeedTimeIn(p.RawDate, loc)
		if published.IsZero() {
			published = p.Published
		}
		// A claimed publication time is checked before it is stored. One
		// that postdates our own fetch is impossible, and storing it would
		// put a months-old article at the top of a feed sorted by date —
		// which is exactly what happened before this existed.
		published, note := ValidatePublished(published, discoveredAt, trust)
		if note != "" {
			e.log.Debug("discarded an untrustworthy publication time",
				"source", src.ID, "reason", note, "title", p.Title)
		}

		canonical := CanonicalURL(p.URL)
		publisher := p.Source
		if publisher == "" {
			publisher = feedTitle
		}
		out = append(out, RawItem{
			SourceID:       src.ID,
			URL:            p.URL,
			CanonicalURL:   canonical,
			Title:          p.Title,
			Description:    p.Description,
			Publisher:      publisher,
			PublishedAt:    published,
			DiscoveredAt:   discoveredAt,
			FetchedAt:      discoveredAt,
			ContentHash:    ContentHash(canonical, p.Title, p.Description),
			TimestampTrust: trust,
			TimestampNote:  note,
		})
	}
	return out, nil
}

// recordSuccess clears the failure streak and schedules the next fetch.
func (e *Engine) recordSuccess(src Source, now time.Time, items, added int, etag, lastMod string) {
	e.mu.Lock()
	st := e.state[src.ID]
	h := st.health
	h.SourceID = src.ID
	h.LastAttemptAt, h.LastSuccessAt = now, now
	h.ConsecutiveFailures = 0
	h.LastError = ""
	h.TotalAttempts++
	h.TotalSuccesses++
	h.TotalItems += items
	h.TotalNewItems += added
	if etag != "" {
		h.ETag = etag
	}
	if lastMod != "" {
		h.LastModified = lastMod
	}
	if added > 0 {
		st.emptyPolls = 0
	} else {
		st.emptyPolls++
	}
	st.health = h
	interval := e.nextInterval(src, st.emptyPolls, now)
	st.lastInterval = interval
	st.nextDue = now.Add(interval)
	e.mu.Unlock()

	e.persist(h)
}

// nextInterval decides when to poll a source again.
//
// Three influences compose, in this order: the lane and configured cadence
// set the baseline, recent productivity stretches or compresses it, the market
// phase scales it for the time of day, and current attention compresses it
// further for instruments something is happening to. The result is clamped to
// the lane's bounds so no combination of them can turn a filings feed hourly
// or hammer a quarterly disclosure.
//
// Must be called with e.mu held.
func (e *Engine) nextInterval(src Source, emptyPolls int, now time.Time) time.Duration {
	phase := PhaseAt(now, e.istLocation)
	interval := adaptiveInterval(src, emptyPolls, phase)

	if e.heat != nil && len(src.Symbols) > 0 {
		// A per-company discovery feed inherits the heat of the company it
		// covers. Broad feeds are left alone: promoting a market-wide feed
		// because one of its many subjects is eventful would promote it
		// almost permanently.
		hottest := HeatNormal
		for _, sym := range src.Symbols {
			if h := e.heat.SymbolHeat(sym); h == HeatHot {
				hottest = HeatHot
				break
			} else if h == HeatWarm {
				hottest = HeatWarm
			}
		}
		if m := hottest.Multiplier(); m < 1 {
			interval = time.Duration(float64(interval) * m)
			if lo, _ := src.Lane().Bounds(); interval < lo {
				interval = lo
			}
		}
	}
	return e.jitter(interval)
}

// recordFailure applies the circuit breaker.
//
// Repeated failure earns exponentially longer silence rather than a retry on
// the next tick. That protects the publisher from us and protects our worker
// pool from spending itself on a host that is down, which is the negative
// caching the architecture calls for expressed as a schedule rather than as a
// separate cache.
func (e *Engine) recordFailure(src Source, now time.Time, cause error) {
	e.mu.Lock()
	st := e.state[src.ID]
	h := st.health
	h.SourceID = src.ID
	h.LastAttemptAt, h.LastFailureAt = now, now
	h.ConsecutiveFailures++
	h.LastError = truncateError(cause)
	h.TotalAttempts++
	// A failed conditional request may mean our tokens are stale; drop them
	// so the retry asks for the document unconditionally.
	if h.ConsecutiveFailures >= 2 {
		h.ETag, h.LastModified = "", ""
	}
	st.health = h

	// A rate limit is honoured on the source's own terms where it states
	// them, and otherwise earns a deliberately long wait. Backing off by the
	// ordinary schedule would keep us knocking at a door that has explicitly
	// asked us to stop.
	var limited *RateLimitedError
	switch {
	case errors.As(cause, &limited) && limited.RetryAfter > 0:
		st.nextDue = now.Add(limited.RetryAfter)
	case errors.As(cause, &limited):
		st.nextDue = now.Add(maxRateLimitWait)
	default:
		st.nextDue = now.Add(e.backoff(h.ConsecutiveFailures, src.Refresh))
	}
	e.mu.Unlock()

	e.persist(h)
}

// maxRateLimitWait is how long we wait out a rate limit that came with no
// Retry-After. It is generous on purpose: guessing short is how a temporary
// throttle becomes a permanent block.
const maxRateLimitWait = 30 * time.Minute

// RateLimitedError reports that a source refused us for asking too often.
type RateLimitedError struct {
	SourceID   string
	RetryAfter time.Duration // zero when the source did not say
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("news: %s rate-limited us; retry after %s", e.SourceID, e.RetryAfter)
	}
	return fmt.Sprintf("news: %s rate-limited us", e.SourceID)
}

// parseRetryAfter reads a Retry-After header in either of its two forms: a
// count of seconds, or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// backoff grows the wait after each consecutive failure, capped so a source
// that recovers overnight is retried within the hour rather than never.
// acceptLanguageFor maps a source's country to the locale to ask it in.
// Unknown or absent country falls back to en-US, the default venue.
func acceptLanguageFor(src Source) string {
	switch src.Country {
	case "IN":
		return "en-IN,en;q=0.9"
	case "GB":
		return "en-GB,en;q=0.9"
	default:
		return "en-US,en;q=0.9"
	}
}

func (e *Engine) backoff(failures int, base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Minute
	}
	const maxBackoff = 30 * time.Minute
	if failures < 1 {
		failures = 1
	}
	if failures > 8 {
		failures = 8 // 2^8 already exceeds the cap; stop the shift growing
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(failures)))
	if d > maxBackoff {
		d = maxBackoff
	}
	return e.jitter(d)
}

// jitter spreads scheduled fetches so that sources sharing a cadence do not
// synchronise into a burst every interval.
//
// It must be called with e.mu held: the source of randomness is not safe for
// concurrent use, and every caller already holds the lock while updating the
// schedule it feeds.
func (e *Engine) jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Minute
	}
	spread := float64(d) * 0.15
	return d + time.Duration(e.rand.Float64()*spread)
}

// persist writes health outside the lock. A storage failure here must not
// stop ingestion: the in-memory schedule is authoritative for this process,
// and the stored copy is an optimisation for the next one.
func (e *Engine) persist(h SourceHealth) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.store.SaveSourceHealth(ctx, h); err != nil && !errors.Is(err, context.Canceled) {
		e.log.Debug("persist source health failed", "source", h.SourceID, "err", err)
	}
}

// Schedule reports what the scheduler currently intends for each source, so
// the adaptation is inspectable rather than a black box.
type Schedule struct {
	SourceID   string        `json:"source_id"`
	Lane       Lane          `json:"lane"`
	NextDue    time.Time     `json:"next_due"`
	Interval   time.Duration `json:"-"`
	IntervalMS int64         `json:"interval_ms"`
	EmptyPolls int           `json:"empty_polls"`
}

// Schedules returns the current plan, soonest first.
func (e *Engine) Schedules() []Schedule {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]Schedule, 0, len(e.state))
	for id, st := range e.state {
		src, ok := e.registry.Get(id)
		if !ok {
			continue
		}
		out = append(out, Schedule{
			SourceID: id, Lane: src.Lane(), NextDue: st.nextDue.UTC(),
			Interval: st.lastInterval, IntervalMS: st.lastInterval.Milliseconds(),
			EmptyPolls: st.emptyPolls,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextDue.Before(out[j].NextDue) })
	return out
}

// Phase reports the current market phase, which drives cadence.
func (e *Engine) Phase() MarketPhase { return PhaseAt(e.now(), e.istLocation) }

// Health returns the current health of every registered source.
func (e *Engine) Health() []SourceHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]SourceHealth, 0, len(e.state))
	for _, st := range e.state {
		out = append(out, st.health)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	const max = 300
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// gdeltResponse is the shape of a GDELT DOC API ArtList reply.
type gdeltResponse struct {
	Articles []struct {
		URL           string `json:"url"`
		Title         string `json:"title"`
		SeenDate      string `json:"seendate"`
		Domain        string `json:"domain"`
		Language      string `json:"language"`
		SourceCountry string `json:"sourcecountry"`
	} `json:"articles"`
}

// parseGDELT reads a GDELT article list.
//
// GDELT reports a "seen" date — when its crawler observed the article — which
// is not the publisher's timestamp and is generally later. It is recorded as
// PublishedAt because it is the best upper bound available, and the honest
// consequence is that ingestion latency measured against GDELT understates
// how far behind the original publication we really are.
func (e *Engine) parseGDELT(src Source, body []byte, discoveredAt time.Time) ([]RawItem, error) {
	// An empty body means the window held no matching coverage. GDELT answers
	// that with nothing at all rather than an empty JSON array, so decoding it
	// raises a parse error and a quiet hour looks like a broken source.
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}

	var doc gdeltResponse
	if err := json.Unmarshal(body, &doc); err != nil {
		// GDELT answers both rate-limit violations and malformed queries with
		// prose rather than JSON, so a decode failure here is an API-level
		// complaint and the body is the message. Two were hiding behind a TLS
		// timeout until the transport was fixed: a timespan of 30 minutes,
		// which it rejects as too short, and its one-request-per-five-seconds
		// limit.
		return nil, fmt.Errorf("news: parse GDELT response for %s: %w (body starts %q)",
			src.ID, err, snippet(body))
	}
	trust := src.TimestampTrust()
	out := make([]RawItem, 0, len(doc.Articles))
	for _, a := range doc.Articles {
		title := cleanText(a.Title)
		if title == "" || a.URL == "" {
			continue
		}
		published, note := ValidatePublished(parseGDELTDate(a.SeenDate), discoveredAt, trust)
		canonical := CanonicalURL(a.URL)
		out = append(out, RawItem{
			SourceID:       src.ID,
			URL:            a.URL,
			CanonicalURL:   canonical,
			Title:          title,
			Publisher:      a.Domain,
			PublishedAt:    published,
			DiscoveredAt:   discoveredAt,
			FetchedAt:      discoveredAt,
			ContentHash:    ContentHash(canonical, title, ""),
			TimestampTrust: trust,
			TimestampNote:  note,
		})
	}
	return out, nil
}

type federalRegisterResponse struct {
	Results []struct {
		Title           string `json:"title"`
		Type            string `json:"type"`
		Abstract        string `json:"abstract"`
		DocumentNumber  string `json:"document_number"`
		HTMLURL         string `json:"html_url"`
		PublicationDate string `json:"publication_date"`
		Agencies        []struct {
			Name string `json:"name"`
		} `json:"agencies"`
	} `json:"results"`
}

// parseFederalRegister reads a documents.json search response.
//
// The issuing agency is the strongest classification signal this source
// carries -- stronger than any word in the title -- so it travels with the
// item as a fact rather than being left for a keyword pass to rediscover.
// Encoded in the description using NSE's own "|KEY: VALUE" convention
// (parsePipeFacts, in internal/events) so interpret() reads it back with the
// same reader every NSE filing already uses, rather than a second parser for
// one more shape of embedded fact.
func (e *Engine) parseFederalRegister(src Source, body []byte, discoveredAt time.Time) ([]RawItem, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var doc federalRegisterResponse
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("news: parse Federal Register response for %s: %w (body starts %q)",
			src.ID, err, snippet(body))
	}
	trust := src.TimestampTrust()
	out := make([]RawItem, 0, len(doc.Results))
	for _, r := range doc.Results {
		title := cleanText(r.Title)
		if title == "" || r.HTMLURL == "" {
			continue
		}
		agency := ""
		if len(r.Agencies) > 0 {
			agency = cleanText(r.Agencies[0].Name)
		}
		desc := cleanText(r.Abstract)
		if agency != "" {
			desc += " |AGENCY: " + agency
		}
		if r.Type != "" {
			desc += " |DOCTYPE: " + r.Type
		}
		published, note := ValidatePublished(parseFeedTimeIn(r.PublicationDate, time.UTC), discoveredAt, trust)
		canonical := CanonicalURL(r.HTMLURL)
		out = append(out, RawItem{
			SourceID:       src.ID,
			URL:            r.HTMLURL,
			CanonicalURL:   canonical,
			Title:          title,
			Description:    strings.TrimSpace(desc),
			Publisher:      firstNonEmpty(agency, "Federal Register"),
			PublishedAt:    published,
			DiscoveredAt:   discoveredAt,
			FetchedAt:      discoveredAt,
			ContentHash:    ContentHash(canonical, title, ""),
			TimestampTrust: trust,
			TimestampNote:  note,
		})
	}
	return out, nil
}

// parseGDELTDate reads GDELT's compact UTC stamp, "20260825T120000Z".
func parseGDELTDate(s string) time.Time {
	t, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// FetchNow polls one source immediately, ignoring its schedule.
//
// The scheduler exists so that nobody has to think about when a source was
// last read, and for the most part nobody should. But an operator looking at a
// company's page and expecting news that has not arrived wants to know now
// rather than at the next cadence, and telling them to wait four minutes is a
// worse answer than spending one request.
//
// It respects the circuit breaker and the in-flight guard: a failing source
// stays failing, and a source already being fetched is not fetched twice.
func (e *Engine) FetchNow(ctx context.Context, sourceID string) (items, added int, err error) {
	src, known := e.registry.Get(sourceID)
	if !known {
		return 0, 0, fmt.Errorf("news: no source %q", sourceID)
	}
	if !src.Fetchable() {
		return 0, 0, fmt.Errorf("news: source %q is not enabled", sourceID)
	}

	e.mu.Lock()
	if e.inWork[sourceID] {
		e.mu.Unlock()
		// Already running. Reporting that honestly beats queueing a second
		// fetch that would race the first for the conditional-request token.
		return 0, 0, nil
	}
	st, ok := e.state[sourceID]
	if !ok {
		st = &sourceState{health: SourceHealth{SourceID: sourceID}}
		e.state[sourceID] = st
	}
	if st.health.ConsecutiveFailures > 0 && e.now().Before(st.nextDue) {
		e.mu.Unlock()
		return 0, 0, fmt.Errorf("news: source %q is backing off after %d failures: %s",
			sourceID, st.health.ConsecutiveFailures, st.health.LastError)
	}
	e.inWork[sourceID] = true
	e.mu.Unlock()

	return e.fetchSource(ctx, src)
}

// SourceIDFor returns the watchlist source that follows an instrument, if one
// is registered. The id is keyed by the full canonical symbol, not the bare
// ticker: "watch-reliance.nse" and "watch-aapl" are different sources, and a
// bare "watch-infy" would be ambiguous between the NSE constituent and its
// NYSE-listed namesake.
func (e *Engine) SourceIDFor(sym marketdata.Symbol) (string, bool) {
	id := "watch-" + strings.ToLower(sym.String())
	if _, ok := e.registry.Get(id); ok {
		return id, true
	}
	return "", false
}
