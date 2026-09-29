package postgres

import (
	"net/url"
	"strings"
	"testing"
)

func query(t *testing.T, dsn string) url.Values {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %q: %v", dsn, err)
	}
	return u.Query()
}

// Neon and Supabase both refuse an unencrypted connection, but libpq's default
// sslmode is "prefer", which falls back silently rather than failing. A DSN
// without sslmode therefore looks like it works and is one server-side change
// away from sending credentials in the clear.
func TestManagedProvidersGetTLS(t *testing.T) {
	for name, dsn := range map[string]string{
		"neon":     "postgres://u:p@ep-cool-fog-123.us-east-2.aws.neon.tech/news",
		"supabase": "postgres://u:p@db.abcdefghijkl.supabase.co:5432/postgres",
	} {
		t.Run(name, func(t *testing.T) {
			got, notes, err := prepareDSN(dsn)
			if err != nil {
				t.Fatalf("prepareDSN: %v", err)
			}
			if query(t, got).Get("sslmode") != "require" {
				t.Errorf("sslmode = %q, want require: %s", query(t, got).Get("sslmode"), got)
			}
			if len(notes) == 0 {
				t.Error("the adjustment must be reported, so it shows in the log")
			}
		})
	}
}

// A transaction-mode pooler hands the server connection to a different client
// between statements, so pgx's cached prepared statements describe a session
// the next statement is not running in. That fails as "prepared statement does
// not exist", intermittently and under load. pgx names QueryExecModeExec for
// this case, so a pooled endpoint gets it.
func TestPooledEndpointsDisableStatementCaching(t *testing.T) {
	for name, dsn := range map[string]string{
		"neon pooler":                 "postgres://u:p@ep-cool-fog-123-pooler.us-east-2.aws.neon.tech/news",
		"supabase transaction pooler": "postgres://u:p@aws-0-us-east-1.pooler.supabase.com:6543/postgres",
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := prepareDSN(dsn)
			if err != nil {
				t.Fatalf("prepareDSN: %v", err)
			}
			if mode := query(t, got).Get("default_query_exec_mode"); mode != "exec" {
				t.Errorf("default_query_exec_mode = %q, want exec: %s", mode, got)
			}
		})
	}
}

// Supabase's session-mode port on the same pooler host keeps one server
// connection per client, so prepared statements are safe there. Treating it
// like transaction mode would cost the statement cache for no reason.
func TestSupabaseSessionModeKeepsTheStatementCache(t *testing.T) {
	got, _, err := prepareDSN("postgres://u:p@aws-0-us-east-1.pooler.supabase.com:5432/postgres")
	if err != nil {
		t.Fatalf("prepareDSN: %v", err)
	}
	if mode := query(t, got).Get("default_query_exec_mode"); mode != "" {
		t.Errorf("default_query_exec_mode = %q on session mode; it should be left alone", mode)
	}
}

// An operator who wrote a value made a decision. Overriding it would mean the
// DSN in .env is not the DSN in use, which is the kind of gap that costs an
// afternoon.
func TestExplicitSettingsAreNeverOverridden(t *testing.T) {
	dsn := "postgres://u:p@ep-x-pooler.aws.neon.tech/news?sslmode=verify-full&default_query_exec_mode=simple_protocol"
	got, notes, err := prepareDSN(dsn)
	if err != nil {
		t.Fatalf("prepareDSN: %v", err)
	}
	q := query(t, got)
	if q.Get("sslmode") != "verify-full" {
		t.Errorf("sslmode = %q, want the operator's verify-full", q.Get("sslmode"))
	}
	if q.Get("default_query_exec_mode") != "simple_protocol" {
		t.Errorf("exec mode = %q, want the operator's simple_protocol", q.Get("default_query_exec_mode"))
	}
	if len(notes) != 0 {
		t.Errorf("nothing was changed, so nothing should be reported: %v", notes)
	}
}

// A plain local Postgres is not a managed service and gets no TLS forced on
// it: the local container does not serve TLS at all, so adding sslmode=require
// here would break the deployment this app already runs on.
func TestLocalPostgresIsLeftAlone(t *testing.T) {
	got, notes, err := prepareDSN("postgres://tradesys:pw@postgres:5432/tradesys?sslmode=disable")
	if err != nil {
		t.Fatalf("prepareDSN: %v", err)
	}
	if query(t, got).Get("sslmode") != "disable" {
		t.Error("a local DSN must keep its own sslmode")
	}
	if len(notes) != 0 {
		t.Errorf("nothing to adjust locally, got %v", notes)
	}
}

// A key/value DSN is a different grammar; rewriting it as a URL would corrupt
// it. An operator writing one is being explicit anyway.
func TestKeyValueDSNPassesThrough(t *testing.T) {
	const dsn = "host=db.abc.supabase.co user=postgres password=pw dbname=postgres"
	got, notes, err := prepareDSN(dsn)
	if err != nil {
		t.Fatalf("prepareDSN: %v", err)
	}
	if got != dsn || len(notes) != 0 {
		t.Errorf("key/value DSN was modified: %q, notes %v", got, notes)
	}
}

// A password must never reach a log line.
func TestRedactDSNDropsThePassword(t *testing.T) {
	const secret = "sup3r-s3cret"
	got := redactDSN("postgres://tradesys:" + secret + "@ep-x.aws.neon.tech:5432/news?sslmode=require")
	if strings.Contains(got, secret) {
		t.Fatalf("password leaked: %q", got)
	}
	for _, want := range []string{"ep-x.aws.neon.tech", "news", "tradesys@"} {
		if !strings.Contains(got, want) {
			t.Errorf("redacted DSN lost %q: %q", want, got)
		}
	}
}
