package news

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Method is how a source is fetched. It selects the adapter, not the transport:
// every method below ultimately speaks HTTP, but each yields a different
// document shape and needs different parsing.
type Method string

const (
	// MethodRSS is a publisher's own RSS or Atom feed.
	MethodRSS Method = "rss"
	// MethodGoogleNews is a Google News RSS *search*, used to discover
	// articles from publishers whose own feeds we cannot fetch. It returns
	// headlines and links that point at the original publisher.
	MethodGoogleNews Method = "google_news"
	// MethodGDELT is the GDELT DOC API, used as a coverage-expansion layer.
	MethodGDELT Method = "gdelt"
	// MethodSECFiling is an SEC EDGAR "current filings" Atom feed (8-K,
	// Form 4, 13F). Same generic Atom shape as MethodRSS -- SEC embeds its
	// filer, accession number and (for 8-K) item codes as HTML inside the
	// <summary> element rather than as distinct fields, so the structured
	// reading happens in events.ParseSECFiling, exactly where NSE's
	// pipe-delimited facts are read from an otherwise-generic feed item.
	MethodSECFiling Method = "sec_filing"
	// MethodFederalRegister is the Federal Register's documents.json search
	// API -- JSON, not a feed, like MethodGDELT. The issuing agency is
	// structured metadata the API returns directly, encoded into the raw
	// item's description in NSE's own "|KEY: VALUE" convention so
	// interpret() can read it back with the same parsePipeFacts every
	// NSE-sourced filing already uses.
	MethodFederalRegister Method = "federal_register"
)

// UsageClass records what we are permitted to do with a source's content.
//
// This exists because compliance is not a document that lives beside the code —
// it is a property of each source that the pipeline must be able to enforce.
// A feed being publicly fetchable says nothing about whether its contents may
// be redistributed in a commercial product, and several Indian publishers
// state explicitly that their feeds are for personal, non-commercial use.
type UsageClass string

const (
	// UsageOfficial is a regulator or exchange disclosure. Public by statute
	// and the whole point of publication is dissemination.
	UsageOfficial UsageClass = "official"
	// UsageLicensed is covered by a commercial agreement we hold.
	UsageLicensed UsageClass = "licensed"
	// UsagePublicReviewed is a public feed whose terms permit our use.
	UsagePublicReviewed UsageClass = "public_terms_reviewed"
	// UsageNonCommercial is a feed whose publisher restricts it to personal,
	// non-commercial use.
	UsageNonCommercial UsageClass = "noncommercial_only"
	// UsageDiscoveryOnly means we may use it to learn that a story exists,
	// but must send the reader to the publisher rather than reproduce it.
	UsageDiscoveryOnly UsageClass = "discovery_only"
	// UsageLegalReview marks a source in use but not yet cleared. It is a
	// deliberate, visible admission rather than a silent assumption.
	UsageLegalReview UsageClass = "legal_review"
	// UsageDisabled excludes a source from fetching entirely.
	UsageDisabled UsageClass = "disabled"
)

// DisplayPolicy is what may be shown to a user for a given source.
type DisplayPolicy string

const (
	// DisplayFull permits headline, snippet, publisher and link.
	DisplayFull DisplayPolicy = "full"
	// DisplayLinkOnly permits headline, publisher and link, but no snippet.
	// This is the conservative default for anything we did not fetch from
	// the publisher under terms that clearly allow reproduction.
	DisplayLinkOnly DisplayPolicy = "link_metadata_only"
	// DisplayInternal keeps an item out of the user-facing API entirely. It
	// may still corroborate a story and raise that story's confidence — it
	// simply never appears as a citation.
	DisplayInternal DisplayPolicy = "internal_only"
)

// Trust tiers, expressed on a 0-100 scale so ranking can do arithmetic with
// them directly. An exchange filing is ground truth; a wire service is very
// nearly so; an aggregator we cannot identify is barely evidence at all.
const (
	TrustOfficial   = 100 // SEC, federal agencies: said on the record, by the source itself
	TrustWire       = 95  // Reuters, AP, Bloomberg
	TrustCompanyIR  = 90  // the company's own investor-relations release
	TrustMajorFin   = 80  // WSJ, FT, CNBC, Barron's
	TrustSpecialist = 70  // narrower trade publications
	TrustGeneric    = 50  // general press with no financial desk
	TrustAggregator = 20  // unattributed aggregators and content farms
)

// Source is one configured origin of news.
//
// Sources are data, not code. Adding a publisher must never mean editing the
// fetch loop, and every knob the loop needs — cadence, timeout, trust,
// permissions — travels with the source itself.
type Source struct {
	ID   string // stable identifier, used as a cache and health key
	Name string // human-readable, shown in the source-health UI
	URL  string

	Method   Method
	Category string // markets, companies, economy, filings, ...
	Country  string // ISO-3166 alpha-2; "" for global
	Language string // ISO-639-1

	// UserAgent overrides the engine's default (deliberately empty) header
	// for this one source. SEC enforces its fair-access policy by refusing
	// any request that does not declare a real contact in the User-Agent --
	// an empty header, which gets every other source in the catalog through,
	// gets SEC sources a 403. Left empty, a source gets the default.
	UserAgent string

	Trust   int
	Refresh time.Duration
	Timeout time.Duration

	Usage   UsageClass
	Display DisplayPolicy

	// Symbols restricts an item to a fixed set of instruments. It is set for
	// per-company discovery feeds, where the query already names the company,
	// and left empty for broad feeds whose items must be entity-resolved.
	Symbols []string

	Enabled bool
}

// Validate reports whether a source is coherent enough to schedule.
//
// A misconfigured source is worse than a missing one: it burns a worker slot
// on every cycle and reports a failure that looks like a publisher outage.
func (s Source) Validate() error {
	switch {
	case strings.TrimSpace(s.ID) == "":
		return fmt.Errorf("news: source has no ID")
	case strings.TrimSpace(s.URL) == "":
		return fmt.Errorf("news: source %q has no URL", s.ID)
	case s.Method == "":
		return fmt.Errorf("news: source %q has no method", s.ID)
	case s.Usage == "":
		return fmt.Errorf("news: source %q has no usage class", s.ID)
	case s.Display == "":
		return fmt.Errorf("news: source %q has no display policy", s.ID)
	case s.Trust < 0 || s.Trust > 100:
		return fmt.Errorf("news: source %q trust %d out of range 0-100", s.ID, s.Trust)
	case s.Refresh <= 0:
		return fmt.Errorf("news: source %q has no refresh interval", s.ID)
	}
	return nil
}

// Fetchable reports whether the scheduler should poll this source at all.
func (s Source) Fetchable() bool {
	return s.Enabled && s.Usage != UsageDisabled
}

// Publishable reports whether items from this source may be surfaced to a
// user. Sources we may read but not reproduce still contribute corroboration.
func (s Source) Publishable() bool {
	return s.Display != DisplayInternal
}

// AllowsSnippet reports whether an item's description may be shown.
func (s Source) AllowsSnippet() bool {
	return s.Display == DisplayFull
}

// Official reports whether this source is an exchange or regulator, which
// ranking treats as ground truth rather than as reporting.
func (s Source) Official() bool { return s.Usage == UsageOfficial }

// Registry is the set of configured sources.
//
// It owns lookup and filtering so no other package needs to know how sources
// are stored, and it rejects duplicates at construction: two sources sharing
// an ID would silently share cache and health state.
type Registry struct {
	// Guards the maps. The watchlist portion is rebuilt at runtime while the
	// scheduler is reading, so this is not merely defensive.
	mu    sync.RWMutex
	byID  map[string]Source
	order []string
}

// NewRegistry validates and indexes a set of sources.
func NewRegistry(sources ...Source) (*Registry, error) {
	r := &Registry{byID: make(map[string]Source, len(sources))}
	for _, s := range sources {
		if err := s.Validate(); err != nil {
			return nil, err
		}
		if _, dup := r.byID[s.ID]; dup {
			return nil, fmt.Errorf("news: duplicate source ID %q", s.ID)
		}
		r.byID[s.ID] = s
		r.order = append(r.order, s.ID)
	}
	return r, nil
}

// Get returns a source by ID.
func (r *Registry) Get(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	return s, ok
}

// Replace swaps the dynamic portion of the catalog.
//
// The curated sources are fixed at build time, but watchlist sources are not:
// adding an instrument must start following it without a restart. Only sources
// in the named category are replaced, so a rebuild cannot accidentally drop
// the exchange feeds.
//
// Returns the ids that were added and removed, which the caller logs — a
// watchlist change silently altering what is polled would be hard to explain
// later.
func (r *Registry) Replace(category string, sources []Source) (added, removed []string, err error) {
	for _, s := range sources {
		if err := s.Validate(); err != nil {
			return nil, nil, err
		}
		if s.Category != category {
			return nil, nil, fmt.Errorf("news: source %q is category %q, not %q", s.ID, s.Category, category)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	incoming := make(map[string]Source, len(sources))
	for _, s := range sources {
		incoming[s.ID] = s
	}

	kept := make([]string, 0, len(r.order))
	for _, id := range r.order {
		existing := r.byID[id]
		if existing.Category != category {
			kept = append(kept, id)
			continue
		}
		if _, still := incoming[id]; !still {
			delete(r.byID, id)
			removed = append(removed, id)
			continue
		}
		kept = append(kept, id)
	}

	for _, s := range sources {
		if _, existed := r.byID[s.ID]; !existed {
			kept = append(kept, s.ID)
			added = append(added, s.ID)
		}
		r.byID[s.ID] = s
	}
	r.order = kept
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, nil
}

// Len reports how many sources are registered, enabled or not.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}

// All returns every source in registration order.
func (r *Registry) All() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Source, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

// Fetchable returns the sources the scheduler should poll, most frequently
// refreshed first so that the highest-cadence work is queued earliest.
func (r *Registry) Fetchable() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Source
	for _, id := range r.order {
		if s := r.byID[id]; s.Fetchable() {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Refresh < out[j].Refresh })
	return out
}

// ByCategory returns fetchable sources in one category.
func (r *Registry) ByCategory(category string) []Source {
	var out []Source
	for _, s := range r.Fetchable() {
		if s.Category == category {
			out = append(out, s)
		}
	}
	return out
}
