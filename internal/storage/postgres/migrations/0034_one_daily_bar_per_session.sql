-- The scanner stored daily bars stamped at midnight UTC, which is the previous
-- evening in New York; every other writer stamps a session at midnight New
-- York. So one session was two rows, filed under two different days, and
-- every reader of daily history saw a doubled, misaligned series. Bars that
-- duplicate one already stored for the session go; the rest move to the
-- session's own stamp.
DELETE FROM candles c
USING candles d
WHERE c.interval = '1d' AND d.interval = '1d' AND c.symbol = d.symbol
  AND extract(hour FROM c.ts AT TIME ZONE 'UTC') = 0
  AND extract(minute FROM c.ts AT TIME ZONE 'UTC') = 0
  AND d.ts = ((c.ts AT TIME ZONE 'UTC')::date::timestamp AT TIME ZONE 'America/New_York')
  AND d.ts <> c.ts;

UPDATE candles
SET ts = ((ts AT TIME ZONE 'UTC')::date::timestamp AT TIME ZONE 'America/New_York')
WHERE interval = '1d'
  AND extract(hour FROM ts AT TIME ZONE 'UTC') = 0
  AND extract(minute FROM ts AT TIME ZONE 'UTC') = 0;
