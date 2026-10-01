package research

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// Reading the articles, not just their headlines: an answer built from titles
// and snippets is as thin as they are. The fetcher reads a published page the
// way a person clicking through does, identifies itself, stays slow and
// honours robots.txt. The text is used to answer and cited by link; it is
// never redisplayed.

// Article is the readable text of one page.
type Article struct {
	URL   string
	Title string
	Text  string
	// Words is the extracted length, so a caller can tell a real article from
	// a consent wall without re-counting.
	Words     int
	FetchedAt time.Time
	Cached    bool
}

// fetcher limits.
const (
	// articleTimeout is per page. Generous because a slow publisher is worth
	// waiting for once, and the whole fetch runs concurrently anyway.
	articleTimeout = 20 * time.Second
	// articleConcurrency is how many pages are read at once across all hosts.
	articleConcurrency = 8
	// maxArticleBytes bounds what is read from any one page.
	maxArticleBytes = 4 << 20
	// minArticleWords is the length below which extraction is treated as
	// having failed. Consent walls, paywall interstitials and JavaScript
	// shells all return something; they just do not return an article.
	minArticleWords = 120
)

// ArticleFetcher reads published pages.
type ArticleFetcher struct {
	HTTP       *http.Client
	Limit      int
	UserAgent  string
	mu         sync.Mutex
	publishers map[string]*publisherState
	cache      map[string]articleCacheEntry
	slots      chan struct{}
	// refusals remembers publishers that turned a reader away, so the next
	// question spends its reading on pages that can be read.
	refusals map[string]*refusal
}

type refusal struct {
	count int
	since time.Time // the first refusal counted
	until time.Time // blocked until; zero while only counting
}

// blockedFor is how long a publisher that refused stays skipped.
const blockedFor = 6 * time.Hour

// noteRefusal records a failed read. A forbidden status or a robots rule
// blocks the publisher at once; pages that yield no text (a paywall, a
// script-only page) block it after the second.
func (f *ArticleFetcher) noteRefusal(rawURL string, err error) {
	u, perr := url.Parse(rawURL)
	if perr != nil || err == nil {
		return
	}
	msg := err.Error()
	hard := strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403") ||
		strings.Contains(msg, "disallows") || strings.Contains(msg, "robots access denied")
	soft := strings.Contains(msg, "words extracted")
	if !hard && !soft {
		return
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refusals == nil {
		f.refusals = map[string]*refusal{}
	}
	now := time.Now()
	r := f.refusals[host]
	if r == nil || (!r.until.IsZero() && now.After(r.until)) || (r.until.IsZero() && now.After(r.since.Add(blockedFor))) {
		r = &refusal{since: now}
		f.refusals[host] = r
	}
	r.count++
	if hard || r.count >= 2 {
		r.until = now.Add(blockedFor)
	}
}

// Blocked reports whether a URL's publisher recently refused to be read.
func (f *ArticleFetcher) Blocked(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.refusals[host]
	return r != nil && time.Now().Before(r.until)
}

func NewArticleFetcher() *ArticleFetcher {
	return &ArticleFetcher{HTTP: &http.Client{Transport: publicTransport(), Timeout: articleTimeout}, slots: make(chan struct{}, articleConcurrency)}
}

// Fetch checks every redirect and bounds queueing, transfer and extraction by
// one deadline. Cache hits do not spend a socket or a publisher request.
func (f *ArticleFetcher) Fetch(ctx context.Context, rawURL string) (Article, error) {
	ctx, cancel := context.WithTimeout(ctx, articleTimeout)
	defer cancel()
	u, err := articleURL(rawURL)
	if err != nil {
		return Article{}, err
	}
	key := u.String()
	if a, err, ok := f.cached(key); ok {
		return a, err
	}
	f.mu.Lock()
	if f.slots == nil {
		f.slots = make(chan struct{}, articleConcurrency)
	}
	slots := f.slots
	f.mu.Unlock()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return Article{}, ctx.Err()
	}
	for redirects := 0; redirects < 6; redirects++ {
		a, next, err := f.fetchPage(ctx, u)
		if next != nil {
			u = next
			continue
		}
		if ctx.Err() == nil {
			f.remember(key, a, err)
			f.noteRefusal(rawURL, err)
		}
		return a, err
	}
	return Article{}, fmt.Errorf("article: redirect limit exceeded")
}

func (f *ArticleFetcher) fetchPage(ctx context.Context, u *url.URL) (Article, *url.URL, error) {
	p, err := f.publisher(strings.ToLower(u.Hostname()))
	if err != nil {
		return Article{}, nil, err
	}
	defer func() { f.mu.Lock(); p.users--; f.mu.Unlock() }()
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return Article{}, nil, ctx.Err()
	}
	if a, err, ok := f.cached(u.String()); ok {
		return a, nil, err
	}
	if err := f.allowed(ctx, p, u); err != nil {
		return Article{}, nil, err
	}
	resp, err := f.request(ctx, u)
	if err != nil {
		return Article{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		next, err := resp.Location()
		if err != nil {
			return Article{}, nil, err
		}
		next, err = articleURL(next.String())
		return Article{}, next, err
	}
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		p.next = retryAt(resp)
	}
	if resp.StatusCode != http.StatusOK {
		return Article{}, nil, fmt.Errorf("article: %s returned HTTP %d", u.Hostname(), resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "text/plain") {
		return Article{}, nil, fmt.Errorf("article: unsupported content type %s", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArticleBytes+1))
	if err != nil {
		return Article{}, nil, err
	}
	if len(body) > maxArticleBytes {
		return Article{}, nil, fmt.Errorf("article: document exceeds size limit")
	}
	title, text := "", string(body)
	if !strings.Contains(ct, "text/plain") {
		doc, err := html.Parse(strings.NewReader(text))
		if err != nil {
			return Article{}, nil, err
		}
		title, text = extract(doc)
	}
	words := len(strings.Fields(text))
	if words < minArticleWords {
		return Article{}, nil, fmt.Errorf("article: only %d words extracted; full text unavailable", words)
	}
	a := Article{URL: u.String(), Title: title, Text: trimWords(text, maxBodyWords), Words: words, FetchedAt: time.Now().UTC()}
	f.remember(u.String(), a, nil)
	return a, nil, nil
}

// extract pulls the title and the readable body out of a parsed document.
//
// Deliberately structural rather than statistical. A full readability port
// scores every node by text density and link ratio; this walks for the
// elements publishers actually use for body copy and falls back to the whole
// document. It is less clever and much easier to reason about when a
// particular publisher comes out wrong.
func extract(doc *html.Node) (title, text string) {
	var (
		best     *html.Node
		bestLen  int
		titleTxt string
	)

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "nav", "header", "footer", "aside", "form", "svg":
				return
			case "title":
				if titleTxt == "" {
					titleTxt = strings.TrimSpace(textOf(n))
				}
			case "article", "main":
				if l := len(textOf(n)); l > bestLen {
					best, bestLen = n, l
				}
			case "div", "section":
				// Publishers overwhelmingly mark body copy with a class or id
				// containing one of these words.
				if attrHints(n) {
					if l := len(textOf(n)); l > bestLen {
						best, bestLen = n, l
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if best == nil {
		best = doc
	}
	return titleTxt, paragraphs(best)
}

var bodyHints = []string{
	"article-body", "articlebody", "story-body", "storybody", "story-content",
	"post-content", "entry-content", "content-body", "article__body",
	"articlecontent", "story_content", "arttextstyle", "contentwrapper",
}

func attrHints(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key != "class" && a.Key != "id" && a.Key != "itemprop" {
			continue
		}
		v := strings.ToLower(a.Val)
		if a.Key == "itemprop" && strings.Contains(v, "articlebody") {
			return true
		}
		for _, h := range bodyHints {
			if strings.Contains(v, h) {
				return true
			}
		}
	}
	return false
}

// paragraphs renders a subtree as text, one paragraph per line.
//
// Block structure is preserved because it carries meaning the model uses: a
// list of order values reads as a list, not as one run-on sentence.
func paragraphs(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "nav", "header", "footer", "aside", "form", "svg", "figure":
				return
			}
		}
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t != "" {
				b.WriteString(t)
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "br", "li", "h1", "h2", "h3", "h4", "div", "tr":
				b.WriteByte('\n')
			}
		}
	}
	walk(n)
	return tidy(b.String())
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// tidy collapses the whitespace that HTML extraction always produces, without
// losing the paragraph breaks that carry structure.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(collapseSpaces(l))
		// One- and two-word lines are navigation furniture, not prose.
		if len(strings.Fields(l)) < 3 {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// bodyFetchLimit is how many pages one question reads.
//
// Generous, because depth is the point and the fetches run concurrently: the
// wall-clock cost of thirty pages is roughly the cost of the slowest four.
const bodyFetchLimit = 30

// maxBodyWords is how much of any one article is kept.
//
// Long enough for a full news article or a decent feature, short enough that
// one rambling opinion piece cannot crowd out nine other sources.
const maxBodyWords = 1800

// unreadableHosts are hosts whose links cannot be resolved to an article. A
// Google News link is an opaque token behind a JavaScript redirect, so
// fetching it yields nothing; its headline still counts as a finding.
var unreadableHosts = map[string]bool{
	"news.google.com":    true,
	"consent.google.com": true,
}

// readBodies fills in the text of as many findings as it can.
//
// Best-effort throughout: a paywall, a consent wall or a slow publisher costs
// that one source its body and nothing else. Findings keep their headline
// either way. When the first pass reads too little, because the most
// relevant pages sat behind paywalls, a second pass reads further down.
func (e *Engine) readBodies(ctx context.Context, findings []Finding) {
	if e.articles == nil || len(findings) == 0 {
		return
	}
	hostCounts := map[string]int{}
	tried := map[int]bool{}
	pass := func(limit int) {
		// Bounded so a question does not open thirty sockets at once.
		sem := make(chan struct{}, articleConcurrency)
		var wg sync.WaitGroup
		attempted := 0
		for i := range findings {
			if attempted >= limit {
				break
			}
			if tried[i] {
				continue
			}
			u, err := url.Parse(findings[i].URL)
			if findings[i].Body != "" {
				findings[i].ReadStatus = "read"
				continue
			}
			findings[i].ReadStatus = "not_read"
			if err != nil || u.Host == "" || unreadableHosts[strings.ToLower(u.Host)] {
				continue
			}
			// Reading is spent on the sources about the question; ranking has
			// already put them first, and an off-topic page read in full is a
			// slot a relevant one did not get.
			if r := findings[i].Relevance; r > 0 && r < 10 {
				continue
			}
			if e.articles.Blocked(findings[i].URL) {
				tried[i] = true
				findings[i].ReadStatus = "unavailable"
				findings[i].ReadError = "article: publisher recently refused automated reading"
				continue
			}
			if hostCounts[u.Hostname()] >= 3 {
				continue
			}
			hostCounts[u.Hostname()]++
			attempted++
			tried[i] = true
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()

				a, err := e.articles.Fetch(ctx, findings[i].URL)
				if err != nil {
					findings[i].ReadStatus = "unavailable"
					findings[i].ReadError = err.Error()
					e.log.Debug("could not read article", "url", findings[i].URL, "err", err)
					return
				}
				findings[i].Body = trimWords(a.Text, maxBodyWords)
				findings[i].Words = len(strings.Fields(findings[i].Body))
				findings[i].ReadStatus = "read"
				findings[i].FetchedAt = a.FetchedAt
				findings[i].Cached = a.Cached
			}(i)
		}
		wg.Wait()
	}
	pass(bodyFetchLimit)
	read := 0
	for _, f := range findings {
		if f.Body != "" {
			read++
		}
	}
	if read < wantRead && ctx.Err() == nil {
		pass(bodyFetchLimit / 2)
	}
}

// wantRead is how many readable sources make a first pass enough.
const wantRead = 15

func trimWords(s string, max int) string {
	f := strings.Fields(s)
	if len(f) <= max {
		return s
	}
	// Cut on a word boundary and say so, rather than ending mid-sentence and
	// letting the model treat a truncation as the end of the article.
	return strings.Join(f[:max], " ") + " […truncated]"
}
