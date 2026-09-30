// Package storage holds the types shared between the Postgres store and the
// packages that read it, so those packages need not import the store.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

// WatchlistEntry is one row of a watchlist.
type WatchlistEntry struct {
	Symbol   marketdata.Symbol
	Note     string
	Position int
	AddedAt  time.Time
}

// ErrNotFound is returned when an addressed row does not exist; handlers map
// it to a 404.
var ErrNotFound = errors.New("storage: not found")

// Stats is what the database holds, for the data-sources page.
type Stats struct {
	RawItems     int   `json:"raw_items"`
	PendingItems int   `json:"pending_items"`
	Events       int   `json:"events"`
	Classified   int   `json:"classified_events"`
	Evidence     int   `json:"evidence_links"`
	Entities     int   `json:"entity_links"`
	SizeBytes    int64 `json:"size_bytes"`
}

// EventFilter narrows an event query.
type EventFilter struct {
	Symbol        string
	Types         []string
	Sectors       []string
	MinImportance int
	Since         time.Time
	Query         string // free text over headline and summary
	Limit         int
	Offset        int
	OfficialOnly  bool
	// IndexOnly keeps events about index constituents, plus the MacroTypes,
	// which name no company by nature. It is the default market view.
	IndexOnly  bool
	MacroTypes []string
	// IncludeUnattributedWatchlist admits items a per-symbol search found
	// that resolved to no company: right on that symbol's page, noise on the
	// market feed.
	IncludeUnattributedWatchlist bool
	// OrderByContentAge ranks by publication time where known. A symbol page
	// wants the latest story; the market feed stays in arrival order so one
	// publisher's skewed clock cannot reorder it.
	OrderByContentAge bool
}

// HealthStatus is the traffic light shown per dependency.
type HealthStatus string

const (
	HealthOK           HealthStatus = "ok"
	HealthDegraded     HealthStatus = "degraded"     // failing, but served from cache or a fallback
	HealthDown         HealthStatus = "down"         // failing, nothing covering it
	HealthUnconfigured HealthStatus = "unconfigured" // no credentials, never contacted
)

// ProviderHealth is the persisted health record for one dependency.
type ProviderHealth struct {
	Provider    string       `json:"provider"`
	Kind        string       `json:"kind"`
	Status      HealthStatus `json:"status"`
	Message     string       `json:"message"`
	LastOKAt    *time.Time   `json:"last_ok_at"`
	LastErrorAt *time.Time   `json:"last_error_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// HealthStore persists dependency health across restarts.
type HealthStore interface {
	RecordHealth(ctx context.Context, h ProviderHealth) error
}
