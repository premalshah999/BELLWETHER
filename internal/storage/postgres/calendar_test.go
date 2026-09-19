package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/tradesys/dashboard/internal/marketdata"
)

func day(t *testing.T, s string) *time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}
	return &v
}

func usSym(ticker string) marketdata.Symbol {
	return marketdata.Symbol{Ticker: ticker, Exchange: marketdata.ExchangeUS}
}

// A row enters a horizon on whichever of its dates is soonest, which is not
// always the earnings date. Without NextKind a fourteen-day window can return
// a row whose only visible date is two months out, and the list reads as
// though the filter were ignored.
func TestUpcomingCatalystsNamesTheDateThatQualified(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	today := time.Now().UTC()
	soon := today.AddDate(0, 0, 3).Format("2006-01-02")
	later := today.AddDate(0, 0, 60).Format("2006-01-02")

	if _, err := db.SaveCalendar(ctx, []CalendarEntry{{
		Symbol:         usSym("TESTCAL"),
		EarningsDate:   day(t, later),
		ExDividendDate: day(t, soon),
	}}); err != nil {
		t.Fatalf("SaveCalendar: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.db.ExecContext(context.Background(),
			`DELETE FROM catalyst_calendar WHERE symbol = 'TESTCAL'`)
	})

	rows, err := db.UpcomingCatalysts(ctx, 14*24*time.Hour, []string{"TESTCAL"}, 10)
	if err != nil {
		t.Fatalf("UpcomingCatalysts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want the row inside a 14-day window via its ex-dividend date, got %d", len(rows))
	}
	got := rows[0]
	if got.NextKind != "ex-dividend" {
		t.Errorf("NextKind = %q, want \"ex-dividend\" (the date that put it in the window)", got.NextKind)
	}
	if got.NextDate == nil || got.NextDate.Format("2006-01-02") != soon {
		t.Errorf("NextDate = %v, want %s", got.NextDate, soon)
	}
	if got.EarningsInDays == nil || *got.EarningsInDays < 55 {
		t.Errorf("EarningsInDays = %v, want the real earnings distance (~60), not the one that qualified", got.EarningsInDays)
	}
}

// A refresh that fails halfway must leave the previous dates in place rather
// than emptying the calendar, so the write is an upsert.
func TestSaveCalendarUpserts(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.db.ExecContext(context.Background(),
			`DELETE FROM catalyst_calendar WHERE symbol = 'TESTUPS'`)
	})

	first := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	second := time.Now().UTC().AddDate(0, 0, 20).Format("2006-01-02")

	for _, d := range []string{first, second} {
		if _, err := db.SaveCalendar(ctx, []CalendarEntry{{
			Symbol: usSym("TESTUPS"), EarningsDate: day(t, d),
		}}); err != nil {
			t.Fatalf("SaveCalendar(%s): %v", d, err)
		}
	}

	rows, err := db.UpcomingCatalysts(ctx, 30*24*time.Hour, []string{"TESTUPS"}, 10)
	if err != nil {
		t.Fatalf("UpcomingCatalysts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly one row after two writes, got %d", len(rows))
	}
	if rows[0].EarningsDate.Format("2006-01-02") != second {
		t.Errorf("earnings date = %v, want the second write %s", rows[0].EarningsDate, second)
	}
}

// A row whose every date has passed is nobody's upcoming event.
func TestPruneCalendarDropsFullyExpiredRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.db.ExecContext(context.Background(),
			`DELETE FROM catalyst_calendar WHERE symbol IN ('TESTOLD','TESTMIX')`)
	})

	past := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
	future := time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02")

	if _, err := db.SaveCalendar(ctx, []CalendarEntry{
		{Symbol: usSym("TESTOLD"), EarningsDate: day(t, past)},
		// One stale date and one live one: the row is still upcoming.
		{Symbol: usSym("TESTMIX"), EarningsDate: day(t, past), ExDividendDate: day(t, future)},
	}); err != nil {
		t.Fatalf("SaveCalendar: %v", err)
	}

	if _, err := db.PruneCalendar(ctx); err != nil {
		t.Fatalf("PruneCalendar: %v", err)
	}

	var remaining []string
	rows, err := db.db.QueryContext(ctx,
		`SELECT symbol FROM catalyst_calendar WHERE symbol IN ('TESTOLD','TESTMIX') ORDER BY symbol`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		remaining = append(remaining, s)
	}
	if len(remaining) != 1 || remaining[0] != "TESTMIX" {
		t.Errorf("remaining = %v, want only TESTMIX (a row with one live date survives)", remaining)
	}
}
