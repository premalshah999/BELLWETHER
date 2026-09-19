-- Scheduled corporate events, ahead of time.
--
-- Everything else in this archive is a record of what has already happened.
-- This is the one table about what has not: the next earnings date, the next
-- ex-dividend date, and the analyst EPS range around them.
--
-- It exists so the app can answer "what is coming up for what I hold, and
-- what has this kind of event done to this kind of company before" -- the
-- second half of which the event study engine already computes from
-- discovered_at. A calendar alone is a commodity; a calendar with a base rate
-- attached is not.
--
-- One row per symbol rather than one per event: upstream gives the *next*
-- occurrence of each kind, not a schedule, and storing a single next-date
-- honestly is better than implying a calendar we do not have.
CREATE TABLE IF NOT EXISTS catalyst_calendar (
    symbol                 TEXT PRIMARY KEY,
    earnings_date          DATE,
    ex_dividend_date       DATE,
    dividend_date          DATE,
    -- The analyst EPS range for the upcoming quarter. Kept because the width
    -- of the range is itself information: a wide spread is a disagreement,
    -- and a disagreement before a print is what makes the print interesting.
    eps_low                DOUBLE PRECISION,
    eps_high               DOUBLE PRECISION,
    eps_average            DOUBLE PRECISION,
    refreshed_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A row with no upcoming date carries nothing. The writer already drops
    -- those; this makes it impossible to store one by another path.
    CONSTRAINT catalyst_calendar_has_a_date CHECK (
        earnings_date IS NOT NULL
        OR ex_dividend_date IS NOT NULL
        OR dividend_date IS NOT NULL
    ),
    -- The same venue rule every other symbol column in this schema carries,
    -- so Yahoo's vendor suffixes can never reach storage.
    CONSTRAINT catalyst_calendar_no_vendor_suffix CHECK (symbol !~ '\.(NS|BO)$')
);

-- The calendar is read as "what is coming up", so the ordering column is the
-- date, not the symbol.
CREATE INDEX IF NOT EXISTS catalyst_calendar_earnings_idx
    ON catalyst_calendar (earnings_date) WHERE earnings_date IS NOT NULL;
CREATE INDEX IF NOT EXISTS catalyst_calendar_exdiv_idx
    ON catalyst_calendar (ex_dividend_date) WHERE ex_dividend_date IS NOT NULL;
