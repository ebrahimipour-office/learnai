-- T10.8: an automatic retry (rate_limited/interrupted) is queued with not_before = now + jobs.retry_delay
-- and is not claimed earlier; auto_attempt counts automatic retries in a row (0 = user-started).
ALTER TABLE jobs ADD COLUMN not_before TEXT;
ALTER TABLE jobs ADD COLUMN auto_attempt INTEGER NOT NULL DEFAULT 0;
