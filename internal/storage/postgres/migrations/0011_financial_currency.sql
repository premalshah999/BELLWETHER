-- The currency financial statements are reported in.
--
-- Not the same as the currency the share trades in, and for some Indian
-- companies not even close. Infosys files in US dollars while its shares trade
-- in rupees: the provider reports financialCurrency=USD and currency=INR, and
-- says so plainly in a field that was simply not being read.
--
-- The consequence was silent and severe. Free cash flow of $3.73bn divided by
-- a market capitalisation of ₹4.53 lakh crore gave a free cash flow yield of
-- 0.1% for one of the most cash-generative companies in the index, where the
-- real figure is around 5%. The same mismatch made its EV/EBITDA read 1011x.
-- Neither looked like a bug; both looked like a number.
--
-- Recorded so that any ratio combining a statement figure with a market figure
-- can check first and decline rather than produce a plausible-looking answer
-- in mixed units.
ALTER TABLE fundamentals_snapshot
    ADD COLUMN IF NOT EXISTS quote_currency TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS financial_currency TEXT NOT NULL DEFAULT '';

ALTER TABLE financials
    ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT '';
