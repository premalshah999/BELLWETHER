// Package sqlite implements the storage ports on top of a single SQLite file.
// It uses modernc.org/sqlite, a pure-Go driver, so the binary needs no cgo and
// no system SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB is the SQLite-backed Store.
type DB struct {
	db *sql.DB
}

// Open connects to the SQLite file at path, applying the pragmas we need for a
// long-running server: WAL so readers never block the writer, a busy timeout so
// concurrent writers retry instead of erroring, and foreign keys on.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("sqlite: empty database path")
	}
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("sqlite: resolve path: %w", err)
		}
		path = abs
	}
	dsn := "file:" + path + "?" + url.Values{
		"_pragma": {
			"journal_mode(WAL)",
			"busy_timeout(5000)",
			"foreign_keys(1)",
			"synchronous(NORMAL)",
			// Page cache, per connection, expressed as kibibytes rather than
			// pages so the ceiling is legible. The default is 2MB and there
			// are eight connections, so the cache alone could reach 16MB
			// before any query result is materialised. 4MB total is ample
			// for a working set of recent events and keeps the whole process
			// comfortably inside a small container.
			"cache_size(-512)",
			// Memory-mapped reads avoid copying pages from the OS cache into
			// the process heap. The mapping is virtual address space, not
			// resident memory, so this lowers heap pressure on read-heavy
			// queries rather than raising it.
			"mmap_size(67108864)",
			// Without a bound the write-ahead log grows for as long as a read
			// transaction is open, and an ingestion pass writing thousands of
			// rows can leave a WAL far larger than the database.
			"wal_autocheckpoint(400)",
		},
	}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	// SQLite serialises writes anyway; a small pool avoids lock churn while
	// still letting WAL readers run concurrently.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}

	s := &DB{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory returns an isolated in-memory database. Each call gets its own
// namespace so parallel tests cannot see each other's rows.
func OpenMemory(name string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", url.PathEscape(name))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open memory: %w", err)
	}
	// A shared-cache in-memory database lives only as long as one connection
	// remains open, so the pool is pinned to a single connection.
	db.SetMaxOpenConns(1)

	s := &DB{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *DB) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    name       TEXT PRIMARY KEY,
    applied_at INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("sqlite: create migrations table: %w", err)
	}

	for _, m := range migrations {
		var seen int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, m.Name).Scan(&seen)
		if err != nil {
			return fmt.Errorf("sqlite: check migration %s: %w", m.Name, err)
		}
		if seen > 0 {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: apply migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			m.Name, time.Now().UTC().Unix()); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: record migration %s: %w", m.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("sqlite: commit migration %s: %w", m.Name, err)
		}
	}
	return nil
}

// Close releases the database handle.
func (s *DB) Close() error { return s.db.Close() }

// SQL exposes the underlying handle to the one-off data-migration command,
// which reads from this database and writes to Postgres. Nothing in the
// running application uses it; this package exists now only so that history
// can be carried across.
func (s *DB) SQL() *sql.DB { return s.db }
