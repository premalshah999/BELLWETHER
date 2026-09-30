// Package postgres is the storage layer: one Postgres database, migrated on
// open from the embedded SQL files.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB is the storage handle.
type DB struct {
	db  *sql.DB
	log *slog.Logger
}

// Option configures a DB.
type Option func(*DB)

// WithLogger sets the logger used for migration and maintenance reporting.
func WithLogger(l *slog.Logger) Option { return func(d *DB) { d.log = l } }

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

	// fs.ReadDir returns names sorted, and the numbered names are the apply order.
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		var applied bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("postgres: check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("postgres: read migration %s: %w", name, err)
		}
		// One transaction per file: Postgres DDL is transactional, so a
		// failed migration leaves nothing behind.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("postgres: begin migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("postgres: apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("postgres: record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("postgres: commit migration %s: %w", name, err)
		}
		d.log.Info("applied migration", "name", name)
	}
	return nil
}

// Close releases the pool.
func (d *DB) Close() error { return d.db.Close() }

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

// OpenScratch opens a migrated, empty schema of its own inside dsn's
// database, so tests can run in parallel without seeing each other's rows.
// drop removes the schema and closes both connections.
func OpenScratch(ctx context.Context, dsn string) (db *DB, drop func(), err error) {
	admin, err := Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	schema := fmt.Sprintf("test_%d_%d", time.Now().UnixNano(), os.Getpid())
	if _, err := admin.db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		return nil, nil, err
	}
	drop = func() {
		if db != nil {
			db.Close()
		}
		_, _ = admin.db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	if db, err = Open(ctx, dsn+sep+"search_path="+schema); err != nil {
		drop()
		return nil, nil, err
	}
	return db, drop, nil
}
