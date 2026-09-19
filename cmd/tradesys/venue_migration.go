package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/tradesys/dashboard/internal/news/company"
	"github.com/tradesys/dashboard/internal/storage/postgres"
)

// goMigrationName is the ledger entry for the venue-qualification migration.
// It must match migrations/go_migrations.manifest exactly, or postgres.Open
// refuses to start — the manifest exists so that forgetting to wire this in
// at a call site is a hard failure, not data silently left half-migrated.
const goMigrationName = "0019_venue_qualify.go"

// venueMigrationOption loads both venue masters and builds the postgres.Open
// option that registers the 0019 migration. Every call site that opens the
// database needs this, because the manifest requires it unconditionally.
func venueMigrationOption() (postgres.Option, error) {
	nse, err := company.LoadEmbedded()
	if err != nil {
		return nil, fmt.Errorf("load nse company master: %w", err)
	}
	usTickers, usListings, err := company.LoadEmbeddedUS()
	if err != nil {
		return nil, fmt.Errorf("load us company master: %w", err)
	}
	return postgres.WithGoMigration(goMigrationName, venueQualifyMigration(nse, usTickers, usListings)), nil
}

// Three small, hand-reviewed exceptions to the otherwise-automatic
// event_entities/research_conversations symbol classification below. Found
// by diffing every distinct symbol on record against both venue masters and
// reading a sample headline for each of the 149 that matched neither.
var (
	// literalSymbolRemap fixes one dirty row: a legal name ("InterGlobe
	// Aviation") stored where a ticker belongs. The correct NSE ticker is
	// INDIGO.
	literalSymbolRemap = map[string]string{
		"INTERGLOBE AVIATION": "INDIGO.NSE",
	}
	// indexSymbols were attached to events as if they were companies; they
	// are benchmark indices, which is exactly what ExchangeIndex exists for.
	indexSymbols = map[string]bool{
		"NIFTY": true, "NIFTY50": true, "NIFTY500": true,
	}
	// droppedSymbols resolve to neither the embedded NSE master nor SEC's
	// exchange-listed ticker file under this exact string (verified against
	// both, 2026-09). Guessing a venue for them would be fabrication, so the
	// entity link is dropped instead — the same rule internal/ai/events.go
	// already applies when a model-supplied ticker resolves nowhere.
	droppedSymbols = map[string]bool{
		"PERNOD": true, "ARCELORMITTAL": true, "HITACHI": true,
		"BROOKFIELD": true, "CSK": true, "EXL": true,
	}
)

// symbolClassifier decides what a bare, pre-migration symbol becomes.
type symbolClassifier struct {
	nse map[string]bool // every ticker in the NSE master (equity_l.csv)
	us  map[string]bool // every ticker in SEC's exchange-listed file
}

func newSymbolClassifier(nse *company.Master, usTickers []company.USTicker) *symbolClassifier {
	c := &symbolClassifier{nse: make(map[string]bool, nse.Len()), us: make(map[string]bool, len(usTickers))}
	for _, co := range nse.All() {
		c.nse[co.Symbol] = true
	}
	for _, t := range usTickers {
		c.us[t.Symbol] = true
	}
	return c
}

// classify returns the canonical symbol a bare pre-migration symbol becomes,
// or ok=false if the entity link should be dropped rather than rewritten.
func (c *symbolClassifier) classify(sym string) (canonical string, ok bool) {
	if v, hit := literalSymbolRemap[sym]; hit {
		return v, true
	}
	if indexSymbols[sym] {
		return sym + ".INDEX", true
	}
	if droppedSymbols[sym] {
		return "", false
	}
	if c.nse[sym] {
		return sym + ".NSE", true
	}
	if c.us[sym] {
		return sym, true // already canonical: US symbols carry no suffix
	}
	// Neither master recognizes it. Every source that ever populated
	// event_entities or a watchlist before this migration was India-scoped,
	// so the overwhelming prior is a renamed, delisted, or non-equity-series
	// NSE instrument (a REIT/InvIT unit, an SME-board listing) rather than
	// an unrecognized US one. It is recorded as an inactive NSE listing
	// below rather than silently trusted as still-current.
	return sym + ".NSE", true
}

// venueQualifyMigration performs the one-time rewrite from a single bare-
// ticker namespace to venue-qualified symbols everywhere. It is registered
// with postgres.WithGoMigration as "0019_venue_qualify.go" and runs inside
// the same transaction-per-migration, same-ledger discipline as every
// embedded .sql file — see postgres.go's migrate().
//
// It needs Go rather than SQL because telling an Indian symbol from a US one
// requires the embedded company master, which plain SQL cannot see. That is
// also why this lives in cmd/tradesys rather than internal/storage/postgres:
// the storage package must not import internal/news/company, so the closure
// is built here, where both are already imported, and handed to Open as a
// plain func(ctx, tx) error.
func venueQualifyMigration(nse *company.Master, usTickers []company.USTicker, usListings []company.USListing) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		classifier := newSymbolClassifier(nse, usTickers)

		if err := insertListings(ctx, tx, nse, usTickers, usListings); err != nil {
			return fmt.Errorf("insert listings: %w", err)
		}
		if err := qualifyEventEntitiesAndResearch(ctx, tx, classifier); err != nil {
			return fmt.Errorf("qualify event_entities/research_conversations: %w", err)
		}
		if err := qualifyScanUniverseTables(ctx, tx); err != nil {
			return fmt.Errorf("qualify scan-universe tables: %w", err)
		}
		if err := qualifyWatchSourceIDs(ctx, tx, classifier); err != nil {
			return fmt.Errorf("qualify watch source ids: %w", err)
		}
		if err := addVenueCheckConstraints(ctx, tx); err != nil {
			return fmt.Errorf("add venue check constraints: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `ANALYZE listings, index_constituents, scan_metrics,
			scan_findings, fundamentals_snapshot, financials, event_entities,
			research_conversations, raw_items, event_evidence, source_health`); err != nil {
			return fmt.Errorf("analyze: %w", err)
		}
		return nil
	}
}

// insertListings populates the full referenceable universe: every NSE
// listing (2,557 rows, whether or not it carries an industry classification)
// and every SEC-registered ticker (10,421 rows), with the ~750-name NSE scan
// subset and the S&P 500 supplying industry/taxonomy where they have it.
func insertListings(ctx context.Context, tx *sql.Tx, nse *company.Master, usTickers []company.USTicker, usListings []company.USListing) error {
	nseIndustry := nse.IndexConstituents() // symbol -> industry, ~750 rows

	all := nse.All()
	symbols := make([]string, 0, len(all)+len(usTickers))
	tickers := make([]string, 0, cap(symbols))
	venues := make([]string, 0, cap(symbols))
	exchanges := make([]string, 0, cap(symbols))
	names := make([]string, 0, cap(symbols))
	ciks := make([]string, 0, cap(symbols))
	isins := make([]string, 0, cap(symbols))
	taxonomies := make([]string, 0, cap(symbols))
	industries := make([]string, 0, cap(symbols))
	actives := make([]bool, 0, cap(symbols))

	for _, c := range all {
		industry, hasIndustry := nseIndustry[c.Symbol]
		taxonomy := ""
		if hasIndustry {
			taxonomy = "nse-industry"
		}
		symbols = append(symbols, c.Symbol+".NSE")
		tickers = append(tickers, c.Symbol)
		venues = append(venues, "NSE")
		exchanges = append(exchanges, "")
		names = append(names, c.Name)
		ciks = append(ciks, "")
		isins = append(isins, c.ISIN)
		taxonomies = append(taxonomies, taxonomy)
		industries = append(industries, industry)
		actives = append(actives, true)
	}

	usListingBySymbol := make(map[string]company.USListing, len(usListings))
	for _, l := range usListings {
		usListingBySymbol[l.Symbol] = l
	}
	for _, t := range usTickers {
		taxonomy, industry, exchange := "", "", t.Exchange
		if l, ok := usListingBySymbol[t.Symbol]; ok {
			taxonomy, industry = "gics", l.Sector
			if l.Exchange != "" {
				exchange = l.Exchange
			}
		}
		symbols = append(symbols, t.Symbol)
		tickers = append(tickers, t.Symbol)
		venues = append(venues, "US")
		exchanges = append(exchanges, exchange)
		names = append(names, t.Name)
		ciks = append(ciks, t.CIK)
		isins = append(isins, "")
		taxonomies = append(taxonomies, taxonomy)
		industries = append(industries, industry)
		actives = append(actives, true)
	}

	return bulkInsertListings(ctx, tx, symbols, tickers, venues, exchanges, names, ciks, isins, taxonomies, industries, actives)
}

func bulkInsertListings(ctx context.Context, tx *sql.Tx, symbols, tickers, venues, exchanges, names, ciks, isins, taxonomies, industries []string, actives []bool) error {
	if len(symbols) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO listings (symbol, ticker, venue, exchange, name, cik, isin, taxonomy, industry, active)
SELECT sym, tick, ven, NULLIF(exch, ''), nm, NULLIF(cik_, ''), NULLIF(isin_, ''), NULLIF(taxo, ''), NULLIF(indus, ''), act
FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::bool[])
    AS t(sym, tick, ven, exch, nm, cik_, isin_, taxo, indus, act)
ON CONFLICT (symbol) DO NOTHING`,
		symbols, tickers, venues, exchanges, names, ciks, isins, taxonomies, industries, actives)
	return err
}

// qualifyEventEntitiesAndResearch resolves every distinct symbol seen in
// event_entities or research_conversations.symbols through the classifier,
// records the mapping in a temp table, then applies it: event_entities rows
// are renamed or (for the handful of dropped symbols) deleted;
// research_conversations arrays are rebuilt with dropped elements removed.
func qualifyEventEntitiesAndResearch(ctx context.Context, tx *sql.Tx, classifier *symbolClassifier) error {
	seen := map[string]bool{}
	var distinct []string
	collect := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			s = strings.TrimSpace(s)
			if s != "" && !seen[s] {
				seen[s] = true
				distinct = append(distinct, s)
			}
		}
		return rows.Err()
	}
	if err := collect(tx.QueryContext(ctx, `SELECT DISTINCT symbol FROM event_entities`)); err != nil {
		return fmt.Errorf("distinct event_entities symbols: %w", err)
	}
	if err := collect(tx.QueryContext(ctx, `SELECT DISTINCT s FROM research_conversations, unnest(symbols) AS s`)); err != nil {
		return fmt.Errorf("distinct research symbols: %w", err)
	}

	oldSyms := make([]string, len(distinct))
	newSyms := make([]string, len(distinct)) // empty string means "drop"
	var newNSEListings []string              // symbols needing a bare inactive listing row
	for i, s := range distinct {
		canonical, ok := classifier.classify(s)
		oldSyms[i] = s
		if !ok {
			continue // newSyms[i] stays "", which means drop
		}
		newSyms[i] = canonical
		if strings.HasSuffix(canonical, ".NSE") && !classifier.nse[s] && literalSymbolRemap[s] == "" {
			newNSEListings = append(newNSEListings, s)
		}
	}

	// A renamed/delisted/non-equity-series NSE instrument the master does
	// not recognize still deserves a row in listings — inactive, so it never
	// shows up as a current holding, but present so the symbol is not a
	// dangling reference to nothing.
	if len(newNSEListings) > 0 {
		tickers := newNSEListings
		symbols := make([]string, len(tickers))
		venues := make([]string, len(tickers))
		names := make([]string, len(tickers))
		blank := make([]string, len(tickers))
		actives := make([]bool, len(tickers))
		for i, t := range tickers {
			symbols[i] = t + ".NSE"
			venues[i] = "NSE"
			names[i] = t // the real legal name is not recoverable from a bare ticker alone
			actives[i] = false
		}
		if err := bulkInsertListings(ctx, tx, symbols, tickers, venues, blank, names, blank, blank, blank, blank, actives); err != nil {
			return fmt.Errorf("insert inactive nse listings: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
CREATE TEMP TABLE symbol_map (old_symbol TEXT PRIMARY KEY, new_symbol TEXT) ON COMMIT DROP`); err != nil {
		return fmt.Errorf("create symbol_map: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO symbol_map (old_symbol, new_symbol)
SELECT o, NULLIF(n, '') FROM unnest($1::text[], $2::text[]) AS t(o, n)`, oldSyms, newSyms); err != nil {
		return fmt.Errorf("populate symbol_map: %w", err)
	}

	// Drop entity links that resolve to neither venue.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM event_entities en
USING symbol_map sm
WHERE en.symbol = sm.old_symbol AND sm.new_symbol IS NULL`); err != nil {
		return fmt.Errorf("drop unresolvable event_entities: %w", err)
	}
	// Rename the rest. A collision here — two old symbols on the same event
	// mapping to the same new one — would abort the transaction rather than
	// silently lose a row; none is expected (renamed/US-vs-NSE tickers do
	// not coincide on a single event in the data reviewed for this
	// migration), and failing loudly is the right behavior if one exists.
	if _, err := tx.ExecContext(ctx, `
UPDATE event_entities en
SET symbol = sm.new_symbol
FROM symbol_map sm
WHERE en.symbol = sm.old_symbol AND sm.new_symbol IS NOT NULL`); err != nil {
		return fmt.Errorf("rename event_entities: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE research_conversations rc
SET symbols = COALESCE((
    SELECT array_agg(sm.new_symbol)
    FROM unnest(rc.symbols) AS s
    JOIN symbol_map sm ON sm.old_symbol = s
    WHERE sm.new_symbol IS NOT NULL
), ARRAY[]::text[])
WHERE symbols IS NOT NULL`); err != nil {
		return fmt.Errorf("rewrite research_conversations.symbols: %w", err)
	}
	return nil
}

// qualifyScanUniverseTables appends .NSE to the five tables that are, in
// their entirety, derived from the NSE scan universe today. A plain suffix
// append can never collide: no row in any of these tables carries a dot
// today (checked directly against the live database before writing this
// migration), so appending the same fixed suffix to every value of an
// already-unique column keeps it unique.
func qualifyScanUniverseTables(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`UPDATE index_constituents SET symbol = symbol || '.NSE', venue = 'NSE', taxonomy = 'nse-industry'
		 WHERE symbol NOT LIKE '%.%'`,
		`UPDATE scan_metrics SET symbol = symbol || '.NSE' WHERE symbol NOT LIKE '%.%'`,
		`UPDATE scan_findings SET symbol = symbol || '.NSE' WHERE symbol NOT LIKE '%.%'`,
		`UPDATE fundamentals_snapshot SET symbol = symbol || '.NSE' WHERE symbol NOT LIKE '%.%'`,
		`UPDATE financials SET symbol = symbol || '.NSE' WHERE symbol NOT LIKE '%.%'`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// qualifyWatchSourceIDs renames watch-<ticker> source ids to watch-<ticker>.nse
// wherever the ticker is an NSE instrument, across every table that carries
// one: raw_items, event_evidence and source_health. Classification uses the
// same venue masters as event_entities, not current watchlist membership —
// a ticker removed from the watchlist since the id was created must still
// resolve correctly.
func qualifyWatchSourceIDs(ctx context.Context, tx *sql.Tx, classifier *symbolClassifier) error {
	rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT source_id FROM raw_items WHERE source_id LIKE 'watch-%'
UNION
SELECT DISTINCT source_id FROM event_evidence WHERE source_id LIKE 'watch-%'
UNION
SELECT DISTINCT source_id FROM source_health WHERE source_id LIKE 'watch-%'`)
	if err != nil {
		return fmt.Errorf("distinct watch ids: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	var oldIDs, newIDs []string
	for _, id := range ids {
		ticker := strings.ToUpper(strings.TrimPrefix(id, "watch-"))
		if classifier.nse[ticker] {
			oldIDs = append(oldIDs, id)
			newIDs = append(newIDs, id+".nse")
		}
		// US tickers, and anything unclassifiable, keep their id unchanged.
	}
	if len(oldIDs) == 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `
CREATE TEMP TABLE watch_id_map (old_id TEXT PRIMARY KEY, new_id TEXT NOT NULL) ON COMMIT DROP`); err != nil {
		return fmt.Errorf("create watch_id_map: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO watch_id_map (old_id, new_id) SELECT o, n FROM unnest($1::text[], $2::text[]) AS t(o, n)`,
		oldIDs, newIDs); err != nil {
		return fmt.Errorf("populate watch_id_map: %w", err)
	}

	renames := []struct{ table, column string }{
		{"raw_items", "source_id"},
		{"event_evidence", "source_id"},
		{"source_health", "source_id"},
	}
	for _, r := range renames {
		q := fmt.Sprintf(`UPDATE %s t SET %s = m.new_id FROM watch_id_map m WHERE t.%s = m.old_id`,
			r.table, r.column, r.column)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("rename %s.%s: %w", r.table, r.column, err)
		}
	}
	return nil
}

// addVenueCheckConstraints stops Yahoo's vendor suffixes from ever leaking
// back into storage now that the venue lives in the symbol itself.
func addVenueCheckConstraints(ctx context.Context, tx *sql.Tx) error {
	tables := []string{
		"index_constituents", "scan_metrics", "scan_findings",
		"fundamentals_snapshot", "financials", "event_entities",
	}
	for _, t := range tables {
		q := fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s_no_vendor_suffix CHECK (symbol !~ '\.(NS|BO)$')`, t, t)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("add check to %s: %w", t, err)
		}
	}
	return nil
}
