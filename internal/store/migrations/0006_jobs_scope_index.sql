-- CR-I6: index on (scope_key, id) for Runner.Latest and scope queries
CREATE INDEX IF NOT EXISTS idx_jobs_scope ON jobs(scope_key, id);
