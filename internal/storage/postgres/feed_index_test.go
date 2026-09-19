package postgres

import (
	"context"
	"strings"
	"testing"
)

// The default feed filters and orders on contentAgeExpr, and migration 0022
// indexes that expression so the scan can stop at the requested page. Postgres
// matches an expression index by its parsed tree, so if the constant and the
// migration ever drift apart nothing fails -- the planner just quietly stops
// using the index and reads the whole archive again. That regression cost
// 904 ms cold and 255 ms warm on 93,402 events, and it grows with the table.
//
// enable_seqscan is disabled so the assertion is about whether the index is
// *usable* for this expression, not about what the planner happens to prefer
// on a small test schema.
func TestContentAgeExpressionMatchesItsIndex(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.db.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	plan := explain(t, db, `SELECT e.id FROM events e
		WHERE `+contentAgeExpr+` >= now() - interval '72 hours'
		ORDER BY `+contentAgeExpr+` DESC, e.id DESC
		LIMIT 50`)

	if !strings.Contains(plan, "events_content_age_idx") {
		t.Fatalf("contentAgeExpr no longer matches events_content_age_idx;\n"+
			"the feed has fallen back to scanning every event.\nPlan:\n%s", plan)
	}
}

// The feed also excludes events whose only evidence came from a per-symbol
// watchlist search. The partial index in 0022 stores exactly the rows that
// qualify; if its predicate stops matching the query's, this returns to a
// sequential scan of every evidence row.
func TestAttributableEvidencePredicateMatchesItsIndex(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	if _, err := db.db.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	plan := explain(t, db, `SELECT e.id FROM events e WHERE EXISTS (
		SELECT 1 FROM event_evidence ev
		WHERE ev.event_id = e.id AND ev.source_id NOT LIKE 'watch-%')`)

	if !strings.Contains(plan, "event_evidence_attributable_idx") {
		t.Fatalf("the attributable-evidence filter no longer matches its partial index.\nPlan:\n%s", plan)
	}
}

func explain(t *testing.T, db *DB, query string) string {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(), "EXPLAIN "+query)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return b.String()
}
