-- Bellwether covers US markets only. Drop what was cached for Indian listings
-- (every row here is re-fetchable market data), the four AI outputs written
-- about one, and the Indian example symbol in the seeded rules. Research
-- threads about Indian stocks are archived, not deleted. Indian news events
-- stay in the archive and are filtered out on read.
DELETE FROM catalyst_calendar     WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM fundamentals_snapshot WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM financials            WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM scan_findings         WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM scan_metrics          WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM candles               WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM quotes                WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM series_coverage       WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM ai_outputs            WHERE symbol ~ '\.(NSE|BSE)$';
DELETE FROM outlooks              WHERE symbol ~ '\.(NSE|BSE)$';

UPDATE research_conversations SET archived = true
WHERE array_to_string(symbols, ',') ~ '\.(NSE|BSE)';

UPDATE algorithms SET definition = jsonb_set(definition, '{symbols}', (
    SELECT coalesce(jsonb_agg(s), '[]'::jsonb)
    FROM jsonb_array_elements(definition->'symbols') s
    WHERE s #>> '{}' !~ '\.(NSE|BSE)$'))
WHERE jsonb_typeof(definition->'symbols') = 'array'
  AND (definition->'symbols')::text ~ '\.(NSE|BSE)';

-- The NSE/BSE listing fallback was the only thing that set these.
ALTER TABLE candles DROP COLUMN IF EXISTS resolved_symbol;
ALTER TABLE quotes  DROP COLUMN IF EXISTS resolved_symbol;
