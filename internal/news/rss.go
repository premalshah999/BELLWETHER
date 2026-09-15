package news

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"strings"
	"time"
)

// feedDocument covers both RSS 2.0 and Atom.
//
// Rather than sniff the format first, both shapes are decoded from the same
// struct and whichever populated wins. Feed publishers are not consistent, and
// several emit hybrids.
type feedDocument struct {
	XMLName xml.Name `xml:"-"`

	// RSS 2.0
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			PubDate     string `xml:"pubDate"`
			Description string `xml:"description"`
			GUID        string `xml:"guid"`
			Source      struct {
				Text string `xml:",chardata"`
				URL  string `xml:"url,attr"`
			} `xml:"source"`
		} `xml:"item"`
	} `xml:"channel"`

	// Atom
	Title   string `xml:"title"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
		Summary   string `xml:"summary"`
		ID        string `xml:"id"`
	} `xml:"entry"`
}

// parsedItem is one feed entry before it becomes an Article.
type parsedItem struct {
	Title       string
	URL         string
	Source      string
	Description string
	Published   time.Time
	// GUID is the publisher's own identifier where one is given. It is the
	// most reliable dedupe key a feed offers, being stable across the
	// cosmetic URL changes that publishers make constantly.
	GUID string
	// RawDate is the timestamp exactly as the feed wrote it, kept so a caller
	// that knows the source's timezone can reparse a zoneless stamp correctly.
	RawDate string
}

// ParseFeed extracts items from an RSS or Atom document.
//
// This is the only place that knows feed XML. It is pure, so tests drive it
// from recorded documents, and it is tolerant: a single malformed item is
// skipped rather than discarding the feed.
func ParseFeed(body []byte) ([]parsedItem, string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))

	// Real feeds are not well-formed XML. Google News embeds HTML entities
	// such as &nbsp; in descriptions, which XML does not define and a strict
	// decoder rejects outright — rejecting the entire feed over an entity in
	// a field we do not even read.
	dec.Entity = xml.HTMLEntity
	dec.Strict = false

	// Some publishers declare a non-UTF-8 charset. Passing the bytes through
	// yields mojibake in the worst case, which is still better than dropping
	// every article from that publisher.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}

	var doc feedDocument
	if err := dec.Decode(&doc); err != nil {
		return nil, "", fmt.Errorf("news: parse feed: %w", err)
	}

	feedTitle := firstNonEmpty(doc.Channel.Title, doc.Title)
	var out []parsedItem

	for _, item := range doc.Channel.Items {
		title := cleanText(item.Title)
		description := cleanText(item.Description)
		// A link is normally required, because without one there is nothing
		// to send a reader to. NSE is the exception: it publishes some of its
		// most valuable items with an empty <link/> element — the exchange
		// asking a company to explain a price movement, a volume spurt, or a
		// news report it has seen. Those carry their whole substance in the
		// description, and requiring a URL discarded every one of them.
		//
		// Identity does not depend on the URL either: ContentHash covers the
		// title and description too, so a link-less item still deduplicates
		// correctly across polls.
		if title == "" || (item.Link == "" && description == "") {
			continue
		}
		out = append(out, parsedItem{
			Title:       title,
			URL:         strings.TrimSpace(item.Link),
			Source:      sourceFor(item.Source.Text, item.Link, feedTitle),
			Description: description,
			Published:   parseFeedTime(item.PubDate),
			GUID:        strings.TrimSpace(item.GUID),
			RawDate:     strings.TrimSpace(item.PubDate),
		})
	}

	for _, entry := range doc.Entries {
		title := cleanText(entry.Title)
		link := ""
		for _, l := range entry.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		if title == "" || link == "" {
			continue
		}
		out = append(out, parsedItem{
			Title:       title,
			URL:         strings.TrimSpace(link),
			Source:      sourceFor("", link, feedTitle),
			Description: cleanText(entry.Summary),
			Published:   parseFeedTime(firstNonEmpty(entry.Published, entry.Updated)),
			GUID:        strings.TrimSpace(entry.ID),
			RawDate:     strings.TrimSpace(firstNonEmpty(entry.Published, entry.Updated)),
		})
	}

	return out, feedTitle, nil
}

// sourceFor picks the most informative publisher name available.
//
// Google News supplies a <source> element naming the original publisher, which
// is far more useful than "Google News" — an operator wants to know a story
// came from Reuters.
func sourceFor(explicit, link, feedTitle string) string {
	if s := cleanText(explicit); s != "" {
		return s
	}
	if h := hostOf(link); h != "" && !strings.Contains(h, "news.google.com") {
		return h
	}
	if feedTitle != "" {
		// Google News titles its feeds "X - Google News"; keep just the query.
		if i := strings.Index(feedTitle, " - Google News"); i > 0 {
			return "Google News"
		}
		return cleanText(feedTitle)
	}
	return "unknown"
}

// parseFeedTime accepts the many date formats feeds emit in practice. An
// unparseable date yields the zero time, never "now".
func parseFeedTime(s string) time.Time { return parseFeedTimeIn(s, time.UTC) }

// parseFeedTimeIn parses a feed timestamp, using loc for formats that carry no
// zone of their own.
//
// The default matters more than it looks. A zoneless "25-Aug-2026 19:28:37"
// from NSE is Indian Standard Time, and reading it as UTC would date every
// filing 5h30m before it was published — enough to reorder a day's events and
// to make the ingestion latency metric report negative numbers.
func parseFeedTimeIn(s string, loc *time.Location) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}
	// Formats carrying an explicit zone are parsed first, so an explicit
	// offset always wins over the caller's default.
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC3339,
		"Mon, 02 Jan 2006 15:04:05 MST",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}

	// Zoneless formats, interpreted in loc.
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"02-Jan-2006 15:04:05", // NSE corporate filings
		"02-Jan-2006 15:04",
		"02-Jan-2006",
		"02 Jan 2006 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// cleanText unescapes entities, strips tags, and collapses whitespace. Feed
// titles routinely arrive with HTML in them.
func cleanText(s string) string {
	s = html.UnescapeString(s)

	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func hostOf(rawURL string) string {
	s := rawURL
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexByte(s, '/'); i > 0 {
		s = s[:i]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// FeedItem is one parsed feed entry, exported for callers outside this package.
type FeedItem struct {
	Title       string
	URL         string
	Source      string
	Description string
	Published   time.Time
	GUID        string
}

// ParseFeedItems parses a feed and returns its entries.
//
// It is the exported face of ParseFeed. The internal parsed form stays
// unexported so that the parser can gain fields without becoming an API, and
// this projects the stable subset that other packages actually need.
func ParseFeedItems(body []byte) ([]FeedItem, string, error) {
	items, feedTitle, err := ParseFeed(body)
	if err != nil {
		return nil, "", err
	}
	out := make([]FeedItem, 0, len(items))
	for _, it := range items {
		out = append(out, FeedItem{
			Title: it.Title, URL: it.URL, Source: it.Source,
			Description: it.Description, Published: it.Published, GUID: it.GUID,
		})
	}
	return out, feedTitle, nil
}
