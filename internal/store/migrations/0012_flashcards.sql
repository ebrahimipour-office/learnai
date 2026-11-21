-- T11.3: jobs.kind 'flashcards' and the flashcards table (review cards from a lesson's keywords; DB only, no file).
-- SQLite cannot alter a CHECK constraint, so jobs is rebuilt as in 0011 (foreign_keys is OFF while migrating).
CREATE TABLE jobs_new (
  id                 INTEGER PRIMARY KEY,
  kind               TEXT NOT NULL CHECK (kind IN ('lesson','exercises','evaluate','roadmap_analyze','chapter_detect','qa','flashcards')),
  scope_key          TEXT NOT NULL,
  topic_id           INTEGER REFERENCES topics(id),
  roadmap_id         INTEGER REFERENCES roadmaps(id),
  status             TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed','canceled')),
  cancel_requested   INTEGER NOT NULL DEFAULT 0,
  params             TEXT NOT NULL DEFAULT '{}',
  inputs_fingerprint TEXT,
  retry_of           INTEGER REFERENCES jobs(id),
  phase              TEXT,
  progress_done      INTEGER NOT NULL DEFAULT 0,
  progress_total     INTEGER NOT NULL DEFAULT 0,
  error_code         TEXT,
  error_message      TEXT,
  commit_journal     TEXT,
  result_ref         TEXT,
  adapter TEXT, model TEXT, skill TEXT, skill_version TEXT,
  usage              TEXT,
  log_dir            TEXT,
  enqueued_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  started_at         TEXT,
  heartbeat_at       TEXT,
  finished_at        TEXT,
  not_before         TEXT,
  auto_attempt       INTEGER NOT NULL DEFAULT 0
);
INSERT INTO jobs_new SELECT * FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;
CREATE UNIQUE INDEX uq_jobs_active_scope ON jobs(scope_key) WHERE status IN ('queued','running');
CREATE INDEX idx_jobs_queue ON jobs(status, enqueued_at);
CREATE INDEX idx_jobs_topic ON jobs(topic_id, enqueued_at DESC);
CREATE INDEX idx_jobs_scope ON jobs(scope_key, id);

-- Leitner box 1..5; due_at is UTC text in the same format as created_at, so it compares as a string.
CREATE TABLE flashcards (
  id                INTEGER PRIMARY KEY,
  topic_id          INTEGER NOT NULL REFERENCES topics(id),
  lesson_version_id INTEGER NOT NULL REFERENCES lesson_versions(id),
  front             TEXT NOT NULL,
  back              TEXT NOT NULL,
  box               INTEGER NOT NULL DEFAULT 1 CHECK (box BETWEEN 1 AND 5),
  due_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  job_id            INTEGER NOT NULL REFERENCES jobs(id),
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX idx_flashcards_due ON flashcards(due_at);
CREATE INDEX idx_flashcards_version ON flashcards(lesson_version_id);
