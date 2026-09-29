package postgres

import (
	"fmt"
	"net/url"
	"strings"
)

// Managed Postgres providers this app knows how to connect to correctly.
//
// Both Neon and Supabase are ordinary Postgres, so the DSN would mostly work
// untouched -- but "mostly" hides two failures that are silent until they are
// not, and both are properties of the endpoint rather than choices an operator
// should have to remember.
type provider string

const (
	providerNeon     provider = "neon"
	providerSupabase provider = "supabase"
	providerOther    provider = "postgres"
)

// providerOf names the managed service behind a DSN, from its host.
func providerOf(host string) provider {
	h := strings.ToLower(host)
	switch {
	case strings.Contains(h, "neon.tech"), strings.Contains(h, "neon.build"):
		return providerNeon
	case strings.Contains(h, "supabase.co"), strings.Contains(h, "supabase.com"):
		return providerSupabase
	default:
		return providerOther
	}
}

// pooled reports whether the endpoint is a connection pooler in transaction
// mode, where a server connection is handed to a different client between
// statements.
//
// Neon spells this with a "-pooler" suffix on the compute host; Supabase uses
// a dedicated pooler host, and port 6543 for transaction mode against it
// (5432 on the same host is session mode, which is safe).
func pooled(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(host, "-pooler"):
		return true
	case strings.Contains(host, "pooler.supabase.com") && u.Port() == "6543":
		return true
	default:
		return false
	}
}

// prepareDSN adjusts a connection string for the service behind it, and
// describes what it did so the choice is visible in the log rather than
// buried.
//
// Two adjustments, both for failures that are invisible until they bite:
//
//  1. TLS. Neon and Supabase both refuse an unencrypted connection, but
//     libpq's default sslmode is "prefer", which silently falls back. A DSN
//     without sslmode therefore looks like it works and is one server-side
//     config change away from sending credentials in the clear.
//
//  2. Prepared statements against a transaction-mode pooler. pgx defaults to
//     caching prepared statements per connection; a pooler that reassigns the
//     server connection between statements makes that cache describe a
//     session the next statement is not running in, and the query fails with
//     "prepared statement does not exist" -- intermittently, under load,
//     which is the worst way to find out. pgx names QueryExecModeExec as the
//     mode for exactly this case (see its conn.go), so a pooled DSN gets it.
//
// An explicit value the operator set is never overridden: if they wrote
// sslmode or default_query_exec_mode, that is a decision and this respects it.
func prepareDSN(dsn string) (out string, notes []string, err error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return "", nil, fmt.Errorf("postgres: empty connection string")
	}
	// A key/value DSN ("host=... user=...") is left alone: it is a different
	// grammar, and an operator writing one is being explicit by definition.
	if !strings.HasPrefix(trimmed, "postgres://") && !strings.HasPrefix(trimmed, "postgresql://") {
		return trimmed, nil, nil
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", nil, fmt.Errorf("postgres: parse connection string: %w", err)
	}
	q := u.Query()
	p := providerOf(u.Hostname())

	if q.Get("sslmode") == "" && p != providerOther {
		q.Set("sslmode", "require")
		notes = append(notes, "sslmode=require")
	}
	if pooled(u) && q.Get("default_query_exec_mode") == "" {
		q.Set("default_query_exec_mode", "exec")
		notes = append(notes, "default_query_exec_mode=exec (transaction pooler)")
	}

	u.RawQuery = q.Encode()
	return u.String(), notes, nil
}

// redactDSN renders a connection string safe to log: host, port and database
// survive, the password never does.
func redactDSN(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || u.Host == "" {
		return "(unparseable connection string)"
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		db = "?"
	}
	user := ""
	if u.User != nil {
		user = u.User.Username() + "@"
	}
	return fmt.Sprintf("%s://%s%s/%s", u.Scheme, user, u.Host, db)
}

// hostOf returns a DSN's host, or "" when it cannot be read. Only for
// labelling a log line; never for a connection decision.
func hostOf(dsn string) string {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return ""
	}
	return u.Hostname()
}
