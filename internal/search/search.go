// Package search wraps web search behind one interface, so the AI features can
// cite sources without knowing which vendor supplied them.
package search

import (
	"context"
	"errors"
	"time"
)

// ErrNotConfigured is returned by a provider with no API key. It is an
// expected state, not a fault.
var ErrNotConfigured = errors.New("search: provider not configured")

// ErrNoResults is returned when a provider answered but found nothing.
var ErrNoResults = errors.New("search: no results")

// Result is one search hit, normalised across providers.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
	// Published is zero when the provider did not supply a date, which is
	// common. Callers must not present a zero time as "now".
	Published time.Time `json:"published,omitempty"`
}

// Results is a search response with its provenance.
type Results struct {
	Query     string    `json:"query"`
	Results   []Result  `json:"results"`
	Provider  string    `json:"provider"`
	FetchedAt time.Time `json:"fetched_at"`
	// Cached reports that this came from the cache rather than a live call.
	Cached bool `json:"cached"`
}

// Query narrows a search.
type Query struct {
	Text string
	// MaxResults caps the response. Providers have their own ceilings.
	MaxResults int
	// Days restricts to recent results where the provider supports it.
	Days int
}

// Provider is the seam every search vendor sits behind.
type Provider interface {
	// Name identifies the provider in health reporting and provenance.
	Name() string
	// Configured reports whether an API key was supplied.
	Configured() bool
	// Search runs a query.
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Cache persists search results. Declared at the point of use so this package
// does not depend on storage.
type Cache interface {
	LoadSearch(ctx context.Context, key string) (Results, bool, error)
	SaveSearch(ctx context.Context, key string, r Results) error
}
