-- T7.2 (design §12.6): chapter source ranges, editable roadmap title, files.kind 'source', jobs.kind 'chapter_detect'.
-- Runs with foreign_keys=OFF and a foreign_key_check before commit (store.migrate): files and jobs are
-- rebuilt because SQLite cannot ALTER a CHECK constraint. Column order is unchanged, so SELECT * copies ids as-is.
ALTER TABLE topics ADD COLUMN source_start_line INTEGER;          -- NULL = no source text
ALTER TABLE topics ADD COLUMN source_end_line   INTEGER;
ALTER TABLE topics ADD COLUMN source_sha256     TEXT;             -- hash of the slice last written to source.md
ALTER TABLE topics ADD COLUMN source_file_id    INTEGER REFERENCES files(id);
ALTER TABLE import_drafts ADD COLUMN title TEXT;                  -- NULL = default (new: first H1 or file name; re-import: keep current)

CREATE TABLE files_new (
  id              INTEGER PRIMARY KEY,
  topic_id        INTEGER NOT NULL REFERENCES topics(id),
  kind            TEXT NOT NULL CHECK (kind IN
                  ('lesson','exercises_md','manifest','rubric','answer','report','summary','result','source')),
  owner_type      TEXT,
  owner_id        INTEGER,
  rel_path        TEXT NOT NULL UNIQUE,
  sha256          TEXT NOT NULL,
  size_bytes      INTEGER NOT NULL,
  immutable       INTEGER NOT NULL DEFAULT 0,
  observed_sha256 TEXT,
  observed_mtime  INTEGER,
  observed_at     TEXT,
  presence        TEXT NOT NULL DEFAULT 'present' CHECK (presence IN ('present','missing')),
  created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO files_new SELECT * FROM files;
DROP TABLE files;
ALTER TABLE files_new RENAME TO files;
CREATE INDEX idx_files_topic_kind ON files(topic_id, kind);
CREATE INDEX idx_files_sha ON files(sha256);

CREATE TABLE jobs_new (
  id                 INTEGER PRIMARY KEY,
  kind               TEXT NOT NULL CHECK (kind IN ('lesson','exercises','evaluate','roadmap_analyze','chapter_detect')),
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
  finished_at        TEXT
);
INSERT INTO jobs_new SELECT * FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;
CREATE UNIQUE INDEX uq_jobs_active_scope ON jobs(scope_key) WHERE status IN ('queued','running');
CREATE INDEX idx_jobs_queue ON jobs(status, enqueued_at);
CREATE INDEX idx_jobs_topic ON jobs(topic_id, enqueued_at DESC);
