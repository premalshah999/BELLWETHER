package news

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"
)

// RawItem is one fetched thing, exactly as it arrived.
//
// Nothing in the pipeline may edit a RawItem after it is stored. Every derived
// artefact — the entity mapping, the story cluster, the model's reading of it —
// is reproducible from this, and only from this. That is what makes a bad
// parser or a bad prompt a recoverable mistake instead of permanent data loss.
type RawItem struct {
	ID       int64  `json:"id"`
	SourceID string `json:"source_id"`

	URL          string `json:"url"`
	CanonicalURL string `json:"canonical_url"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Publisher    string `json:"publisher"`
	Payload      string `json:"-"` // the original item, verbatim

	// The three timestamps, kept apart on purpose. See below.
	OccurredAt   time.Time `json:"occurred_at,omitempty"`
	PublishedAt  time.Time `json:"published_at,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
	FetchedAt    time.Time `json:"fetched_at"`

	// ContentHash identifies the item's content within its source, so that
	// re-polling a feed converges instead of accumulating duplicates.
	ContentHash string `json:"-"`

	// TimestampTrust says how far PublishedAt can be believed. An aggregator
	// reports when it noticed an item, which is not when the item was
	// published and has been observed to differ by months.
	// Brief is a short "what happened and why it matters", written once by
	// the AI service and kept. Empty when none has been generated.
	Brief string `json:"brief,omitempty"`

	TimestampTrust TimestampTrust `json:"timestamp_trust,omitempty"`
	// TimestampNote records why a claimed publication time was discarded.
	TimestampNote string `json:"timestamp_note,omitempty"`
}

// KnowledgeTime is the moment this system could first have acted on the item:
// DiscoveredAt, never PublishedAt. Treating a publisher's timestamp as our own
// knowledge time claims to have read an article before fetching it, the look-
// ahead bias that makes a backtest brilliant and a live deployment lose money.
func (r RawItem) KnowledgeTime() time.Time { return r.DiscoveredAt }

// Latency reports how far behind the publisher we were. It is the metric that
// tells us whether the ingestion layer is actually fast, as opposed to
// merely claiming to be real-time.
func (r RawItem) Latency() (time.Duration, bool) {
	if r.PublishedAt.IsZero() || r.DiscoveredAt.IsZero() {
		return 0, false
	}
	// An observed timestamp is the intermediary's own sighting, so the
	// difference between it and ours measures the gap between two observers
	// rather than how far behind the publisher we are.
	if r.TimestampTrust == TimestampObserved {
		return 0, false
	}
	d := r.DiscoveredAt.Sub(r.PublishedAt)
	if d < 0 {
		// A source dated the item in our future. Its clock is wrong, or it
		// pre-dates scheduled posts; either way the figure is meaningless.
		return 0, false
	}
	return d, true
}

// Direction is what an event means for one particular instrument.
//
// It is an enumeration rather than a signed number because "unclear" is a
// real, frequent and useful answer. A CEO resigning unexpectedly is
// unambiguously important and genuinely ambiguous in direction, and a signed
// score would have to render that as zero — which instead reads as "no
// effect", a different and wrong claim.
type Direction string

const (
	DirectionPositive Direction = "positive"
	DirectionNegative Direction = "negative"
	DirectionUnclear  Direction = "unclear"
)

// Relationship is how closely an event concerns an instrument.
type Relationship string

const (
	// RelPrimary is the company the event is about.
	RelPrimary Relationship = "primary"
	// RelMentioned is named in the evidence but is not the subject.
	RelMentioned Relationship = "mentioned"
	// RelPeer is a competitor likely to be read across to.
	RelPeer Relationship = "peer"
	// RelSector is exposed through its sector rather than by name.
	RelSector Relationship = "sector"
)

// EventEntity ties an event to one instrument, with a reading specific to it.
type EventEntity struct {
	Symbol       string       `json:"symbol"`
	Relationship Relationship `json:"relationship"`

	// How the company was identified, carried through from the resolver so a
	// reviewer can tell a filing's own name from a one-word guess.
	MatchConfidence float64 `json:"match_confidence"`
	MatchMethod     string  `json:"match_method"`

	// Filled by the model, later in the pipeline.
	Direction      Direction `json:"direction,omitempty"`
	ImpactStrength float64   `json:"impact_strength,omitempty"`
	Rationale      string    `json:"rationale,omitempty"`
}

// Event is the central object: something that happened, with evidence for it.
//
// Articles are not the unit of meaning. Eleven outlets reporting one
// acquisition is one event supported by eleven pieces of evidence, and a
// reader should be shown the event with its sources attached rather than
// eleven near-identical rows to reconcile by eye.
type Event struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"-"`
	Type        string `json:"event_type"`
	Headline    string `json:"headline"`
	Summary     string `json:"summary,omitempty"`

	OccurredAt   time.Time `json:"occurred_at,omitempty"`
	PublishedAt  time.Time `json:"published_at,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
	// ConfirmedAt is when an exchange or regulator corroborated the event.
	// The interval between discovery and confirmation is itself informative:
	// a report the company files against twenty minutes later is a different
	// thing from one that is never confirmed at all.
	ConfirmedAt time.Time `json:"confirmed_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Importance and direction are independent, and this is the type that
	// enforces it: importance lives here, direction lives per-entity.
	Importance  *int     `json:"importance,omitempty"` // 0-10
	Confidence  *float64 `json:"confidence,omitempty"`
	BestTrust   int      `json:"best_trust"`
	SourceCount int      `json:"source_count"`
	Official    bool     `json:"official"`

	ClassifiedAt time.Time `json:"classified_at,omitempty"`
	Model        string    `json:"model,omitempty"`

	Entities []EventEntity `json:"entities,omitempty"`
	Evidence []RawItem     `json:"evidence,omitempty"`
	// Sectors are the industries a macro or policy event reaches, filled in
	// only for the SectorScope types -- a company-specific event has none,
	// since its reach is the company itself. Mixed-taxonomy: an NSE
	// industry name ("Oil Gas & Consumable Fuels") and a GICS sector
	// ("US: Energy") can both appear on the same event, since a story about
	// crude oil genuinely touches both.
	Sectors []string `json:"sectors,omitempty"`

	// TimestampTrust says whether PublishedAt may be shown as a publication
	// time. An aggregator reports when it surfaced an item: an article from
	// March arrived stamped with the day we fetched it, and was shown at the
	// top of a watchlist as that day's news.
	// Brief is a short "what happened and why it matters", written once by
	// the AI service and kept. Empty when none has been generated.
	Brief string `json:"brief,omitempty"`

	TimestampTrust TimestampTrust `json:"timestamp_trust,omitempty"`

	// Staleness is how old the content is, as opposed to how recently it was
	// discovered. Carried to the client because a feed ordered by discovery
	// will otherwise show a five-week-old article as though it broke this
	// morning.
	Staleness Staleness `json:"staleness,omitempty"`

	// PrimaryURL is the best link among the evidence, chosen by source trust.
	// Carried on the list response so a feed row can link straight to the
	// article rather than requiring the reader to expand it first.
	PrimaryURL string `json:"primary_url,omitempty"`
	// PrimarySourceID is the catalogue source that supplied PrimaryURL, and
	// PrimaryPublisher the outlet named on the item itself. The two differ for
	// a discovery source: a Google News result's source is the query, but its
	// publisher is Reuters or the Wall Street Journal, which is what a reader
	// wants to see. Source names the row; see Source on the API envelope.
	PrimarySourceID  string `json:"primary_source_id,omitempty"`
	PrimaryPublisher string `json:"-"`
	// Source is the display name for who reported this: the catalogue name
	// for a direct feed, the item's own publisher for a discovery result.
	// Filled by the server, which holds the catalogue.
	Source string `json:"source,omitempty"`
}

// Classified reports whether the model has processed this event yet.
func (e Event) Classified() bool { return !e.ClassifiedAt.IsZero() }

// trackingParams are query parameters that identify a referral rather than a
// document. Two URLs differing only in these point at the same article, and
// leaving them in means storing the same story once per source that linked it.
var trackingParams = map[string]bool{
	"utm_source": true, "utm_medium": true, "utm_campaign": true,
	"utm_term": true, "utm_content": true, "utm_id": true, "utm_name": true,
	"fbclid": true, "gclid": true, "dclid": true, "msclkid": true, "igshid": true,
	"ref": true, "ref_src": true, "referrer": true, "source": true,
	"cmpid": true, "campaign_id": true, "mc_cid": true, "mc_eid": true,
	"__twitter_impression": true, "s": true, "spm": true, "at_medium": true,
	"at_campaign": true, "ncid": true, "smid": true, "partner": true,
	"feature": true, "yptr": true, "guccounter": true,
}

// CanonicalURL reduces a URL to a stable identity for the document it names.
//
// It is conservative: the scheme is normalised, the host lower-cased and
// stripped of "www.", tracking parameters are dropped, and the remaining query
// is sorted so parameter order cannot fork one document into two. Path case is
// left alone, because plenty of servers genuinely serve different documents
// from paths differing only in case.
func CanonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}

	if u.Scheme == "" || u.Scheme == "http" {
		u.Scheme = "https"
	}
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimPrefix(u.Host, "www.")
	u.Host = strings.TrimSuffix(u.Host, ":443")
	u.Host = strings.TrimSuffix(u.Host, ":80")

	// A fragment addresses a position within a document, not a document.
	u.Fragment = ""
	u.RawFragment = ""

	if q := u.Query(); len(q) > 0 {
		keep := url.Values{}
		for k, vs := range q {
			if trackingParams[strings.ToLower(k)] {
				continue
			}
			keep[k] = vs
		}
		if len(keep) == 0 {
			u.RawQuery = ""
		} else {
			keys := make([]string, 0, len(keep))
			for k := range keep {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			for _, k := range keys {
				vs := keep[k]
				sort.Strings(vs)
				for _, v := range vs {
					if b.Len() > 0 {
						b.WriteByte('&')
					}
					b.WriteString(url.QueryEscape(k))
					b.WriteByte('=')
					b.WriteString(url.QueryEscape(v))
				}
			}
			u.RawQuery = b.String()
		}
	}

	// A trailing slash on a non-root path is almost never meaningful.
	if len(u.Path) > 1 {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	return u.String()
}

// ContentHash identifies an item's content within one source.
//
// The canonical URL alone is not enough: exchange filings frequently carry no
// stable URL, and some feeds republish an item under a new link when its text
// is corrected. Hashing the identifying fields together means a genuine
// correction registers as a new item while an unchanged re-poll does not.
func ContentHash(canonicalURL, title, description string) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(canonicalURL))))
	h.Write([]byte{0})
	h.Write([]byte(collapseSpace(strings.ToLower(title))))
	h.Write([]byte{0})
	h.Write([]byte(collapseSpace(strings.ToLower(description))))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// MarshalJSON omits zero timestamps. omitempty has no effect on a time.Time,
// so an unset time would serialise as year 1, and a client would read
// classified_at as present. The alias avoids recursing into this method.
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	out := struct {
		alias
		OccurredAt   *time.Time `json:"occurred_at,omitempty"`
		PublishedAt  *time.Time `json:"published_at,omitempty"`
		ConfirmedAt  *time.Time `json:"confirmed_at,omitempty"`
		ClassifiedAt *time.Time `json:"classified_at,omitempty"`
	}{alias: alias(e)}

	out.OccurredAt = optionalTime(e.OccurredAt)
	out.PublishedAt = optionalTime(e.PublishedAt)
	out.ConfirmedAt = optionalTime(e.ConfirmedAt)
	out.ClassifiedAt = optionalTime(e.ClassifiedAt)
	return json.Marshal(out)
}

// MarshalJSON omits zero timestamps, for the same reason as Event's.
func (r RawItem) MarshalJSON() ([]byte, error) {
	type alias RawItem
	out := struct {
		alias
		OccurredAt  *time.Time `json:"occurred_at,omitempty"`
		PublishedAt *time.Time `json:"published_at,omitempty"`
	}{alias: alias(r)}

	out.OccurredAt = optionalTime(r.OccurredAt)
	out.PublishedAt = optionalTime(r.PublishedAt)
	return json.Marshal(out)
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
