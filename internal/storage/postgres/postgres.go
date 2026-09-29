// Package postgres is the production storage implementation.
//
// It replaces an SQLite implementation that had become the wrong shape for the
// system built on top of it. The deciding constraint was concurrency: the
// ingestion pipeline runs fetchers, processors and AI workers at the same
// time, and SQLite serialises every writer behind one lock. That was already
// observable — a bulk reprocess had to stop the application first, because two
// processes could not write to the same file.
//
// Postgres also lets the schema carry invariants the application previously
// only intended: real timestamps instead of integers, enumerations instead of
// free strings, CHECK constraints, partial indexes on the work queues, and
// full-text search instead of a LIKE scan.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/tradesys/dashboard/internal/storage"
)

//go:embed migrations/*.sql migrations/*.manifest
var migrationFS embed.FS

// DB is the storage handle.
type DB struct {
	db  *sql.DB
	log *slog.Logger

	goMigrations []goMigration
}

// Option configures a DB.
type Option func(*DB)

// WithLogger sets the logger used for migration and maintenance reporting.
func WithLogger(l *slog.Logger) Option { return func(d *DB) { d.log = l } }

// goMigration pairs a migration name with the Go function that applies it.
type goMigration struct {
	name string
	run  func(ctx context.Context, tx *sql.Tx) error
}

// WithGoMigration registers a migration that cannot be plain SQL — one that
// needs to resolve data against something only Go code can read, such as an
// embedded company master. It runs inside the same transaction-per-migration,
// same-ledger discipline as every embedded .sql file, in the same sorted
// sequence: name should sort exactly where the change belongs, e.g.
// "0019_venue_qualify.go" runs immediately after "0018_listings.sql".
//
// Every name listed in migrations/go_migrations.manifest must be registered
// through this before Open is called, or migrate fails outright — a Go
// migration silently skipped because nobody wired it into main is worse than
// the process refusing to start.
func WithGoMigration(name string, fn func(ctx context.Context, tx *sql.Tx) error) Option {
	return func(d *DB) { d.goMigrations = append(d.goMigrations, goMigration{name: name, run: fn}) }
}

// Pool sizing.
//
// The application is one process with a bounded number of concurrent workers:
// twelve fetchers, a processor, a handful of AI workers and the HTTP handlers.
// A pool much larger than that buys nothing and costs the database a backend
// process per connection; much smaller and the pipelines queue behind each
// other. These are matched to the worker counts rather than guessed.
const (
	maxOpenConns    = 24
	maxIdleConns    = 8
	connMaxLifetime = 30 * time.Minute
	connMaxIdleTime = 5 * time.Minute
)

// Open connects and applies migrations.
func Open(ctx context.Context, dsn string, opts ...Option) (*DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres: empty DATABASE_URL")
	}

	// Adjust the DSN for the managed service behind it before connecting --
	// TLS, and the exec mode a transaction pooler needs. See prepareDSN for
	// why each of those is not safe to leave to the default.
	prepared, notes, err := prepareDSN(dsn)
	if err != nil {
		return nil, err
	}

	handle, err := sql.Open("pgx", prepared)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	handle.SetMaxOpenConns(maxOpenConns)
	handle.SetMaxIdleConns(maxIdleConns)
	handle.SetConnMaxLifetime(connMaxLifetime)
	handle.SetConnMaxIdleTime(connMaxIdleTime)

	d := &DB{db: handle, log: slog.Default()}
	for _, opt := range opts {
		opt(d)
	}
	if len(notes) > 0 {
		d.log.Info("database connection adjusted for its provider",
			"target", redactDSN(prepared), "provider", providerOf(hostOf(prepared)),
			"applied", strings.Join(notes, ", "))
	}

	// The database container may still be starting. Compose waits on its
	// health check, but a restart can still race, and failing to boot because
	// the database was two seconds late is a bad trade.
	if err := d.waitReady(ctx, 30*time.Second); err != nil {
		handle.Close()
		return nil, err
	}
	if err := d.migrate(ctx); err != nil {
		handle.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) waitReady(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var lastErr error
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		lastErr = d.db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(500+attempt*250) * time.Millisecond):
		}
	}
	return fmt.Errorf("postgres: database not ready after %s: %w", limit, lastErr)
}

// migrate applies every embedded migration exactly once.
//
// Migrations run inside an advisory lock so that two instances starting
// together cannot both apply the same file. That is not hypothetical during a
// rolling restart, and a half-applied DDL is far more painful to undo than to
// prevent.
func (d *DB) migrate(ctx context.Context) error {
	const lockID = 8_274_413_009_115_223 // arbitrary, but stable

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("postgres: acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID)
	}()

	if _, err := conn.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    name       TEXT        PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("postgres: create migrations table: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}

	// migrationStep is one schema change, whether it came from an embedded
	// .sql file or a registered Go function. Both are applied and ledgered
	// identically from this point on.
	type migrationStep struct {
		name string
		sql  string                                      // empty for a Go step
		run  func(ctx context.Context, tx *sql.Tx) error // nil for a SQL step
	}

	steps := make([]migrationStep, 0, len(entries)+len(d.goMigrations))
	var required []string
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".sql"):
			body, err := migrationFS.ReadFile("migrations/" + e.Name())
			if err != nil {
				return fmt.Errorf("postgres: read migration %s: %w", e.Name(), err)
			}
			steps = append(steps, migrationStep{name: e.Name(), sql: string(body)})
		case strings.HasSuffix(e.Name(), ".manifest"):
			body, err := migrationFS.ReadFile("migrations/" + e.Name())
			if err != nil {
				return fmt.Errorf("postgres: read manifest %s: %w", e.Name(), err)
			}
			for _, line := range strings.Split(string(body), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				required = append(required, line)
			}
		}
	}
	for _, g := range d.goMigrations {
		steps = append(steps, migrationStep{name: g.name, run: g.run})
	}
	for _, name := range required {
		found := false
		for _, g := range d.goMigrations {
			if g.name == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("postgres: migration %q is required by the manifest but was never registered via WithGoMigration", name)
		}
	}
	// Lexical order is the apply order, which is why the files (and the Go
	// migration names, which follow the same numbering) are numbered.
	sort.Slice(steps, func(i, j int) bool { return steps[i].name < steps[j].name })

	for _, st := range steps {
		var applied bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, st.name).Scan(&applied); err != nil {
			return fmt.Errorf("postgres: check migration %s: %w", st.name, err)
		}
		if applied {
			continue
		}

		// Each migration is one transaction. Postgres supports transactional
		// DDL, so a migration that fails halfway leaves nothing behind —
		// which is the single biggest practical reason to prefer it here.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("postgres: begin migration %s: %w", st.name, err)
		}
		if st.run != nil {
			if err := st.run(ctx, tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("postgres: apply migration %s: %w", st.name, err)
			}
		} else if _, err := tx.ExecContext(ctx, st.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("postgres: apply migration %s: %w", st.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name) VALUES ($1)`, st.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("postgres: record migration %s: %w", st.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("postgres: commit migration %s: %w", st.name, err)
		}
		d.log.Info("applied migration", "name", st.name)
	}
	return nil
}

// Close releases the pool.
func (d *DB) Close() error { return d.db.Close() }

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// SQL exposes the underlying handle for the data-migration command, which
// needs to read from one database and write to another. Nothing in the
// application should use it.
func (d *DB) SQL() *sql.DB { return d.db }

// nullTime renders a zero time as SQL NULL.
//
// The distinction is not cosmetic. A publisher that omits a timestamp must be
// recorded as unknown, because storing the epoch would make an undated item
// sort as the oldest row in the database and quietly rewrite the ordering that
// every later analysis depends on.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func timeOrZero(t sql.NullTime) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Compile-time assertion that this implementation satisfies every port the
// application depends on. It is the cheapest possible guarantee that a method
// was not missed during the port, and it fails at build time rather than when
// a page is first opened.
var _ storage.Store = (*DB)(nil)
