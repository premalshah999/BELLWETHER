-- The sample size behind each scan finding.
--
-- Every statistic on a finding — the volume surprise, the return surprise, the
-- distance from the yearly extremes — is computed over some number of daily
-- bars, and how many changes what the number means. A 5-sigma reading from 30
-- bars and one from 250 are not the same claim. The column existed on the
-- computed metrics and was dropped on the way into storage.
ALTER TABLE scan_findings ADD COLUMN IF NOT EXISTS bars INTEGER NOT NULL DEFAULT 0;
