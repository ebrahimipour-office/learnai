-- ===== هسته: Roadmap و گروه‌ها =====
CREATE TABLE IF NOT EXISTS roadmaps (
  id            INTEGER PRIMARY KEY,
  slug          TEXT NOT NULL UNIQUE,                  -- نام پوشه roadmaps/<slug>
  title         TEXT NOT NULL,
  select_rule   TEXT NOT NULL,                         -- JSON: قاعده انتخاب «موضوع» (R2.2)
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS roadmap_revisions (
  id            INTEGER PRIMARY KEY,
  roadmap_id    INTEGER NOT NULL REFERENCES roadmaps(id),
  rev_no        INTEGER NOT NULL,
  rel_path      TEXT NOT NULL,                         -- roadmaps/<slug>/rev-001.md
  sha256        TEXT NOT NULL,
  parsed_tree   TEXT NOT NULL,                         -- JSON درخت پارس‌شده
  imported_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (roadmap_id, rev_no)
);

-- پیش‌نمایش پیش از ثبت نهایی (R2.2)؛ پس از انقضا پاک می‌شود
CREATE TABLE IF NOT EXISTS import_drafts (
  id            TEXT PRIMARY KEY,                      -- توکن تصادفی
  roadmap_id    INTEGER REFERENCES roadmaps(id),       -- NULL = Roadmap جدید
  source_name   TEXT NOT NULL,
  content       TEXT NOT NULL,                         -- متن فایل (سقف ۱ مگابایت)
  content_sha   TEXT NOT NULL,
  parsed_tree   TEXT NOT NULL,
  selection     TEXT NOT NULL,                         -- JSON: قاعده + تیک‌های دستی
  diff          TEXT,                                  -- JSON گزارش تفاوت (ورود مجدد)
  ai_suggestion TEXT,                                  -- JSON پیشنهاد AI برای ساختار مبهم
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  expires_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS groups (                    -- فاز/گروه (سطوح بالاتر از موضوع)
  id            INTEGER PRIMARY KEY,
  roadmap_id    INTEGER NOT NULL REFERENCES roadmaps(id),
  parent_id     INTEGER REFERENCES groups(id),
  stable_key    TEXT NOT NULL,
  title         TEXT NOT NULL,
  level         INTEGER NOT NULL,
  ord           REAL NOT NULL,
  state         TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','archived')),
  UNIQUE (roadmap_id, stable_key)
);

-- ===== موضوع =====
CREATE TABLE IF NOT EXISTS topics (
  id            INTEGER PRIMARY KEY,
  roadmap_id    INTEGER NOT NULL REFERENCES roadmaps(id),
  group_id      INTEGER REFERENCES groups(id),
  stable_key    TEXT NOT NULL,                         -- شناسه پایدار (R2.3)
  title         TEXT NOT NULL,
  title_norm    TEXT NOT NULL,                         -- برای جست‌وجو و حدس تطبیق
  outline       TEXT,                                  -- زیرموارد لیستی همان سرفصل (کمک به Prompt)
  ord           REAL NOT NULL,                         -- ترتیب نمایش (REAL برای درج پیشنهادهای تأییدشده)
  slug          TEXT NOT NULL,
  dir_name      TEXT NOT NULL UNIQUE,                  -- 05-http-basics؛ هرگز تغییر نمی‌کند
  origin        TEXT NOT NULL DEFAULT 'roadmap' CHECK (origin IN ('roadmap','suggested')),
  state         TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','archived')),
  status        TEXT NOT NULL DEFAULT 'created'
                CHECK (status IN ('created','content_ready','exercises_ready','answered','evaluated')),
  source_rev_id INTEGER REFERENCES roadmap_revisions(id),
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (roadmap_id, stable_key)
);
CREATE INDEX IF NOT EXISTS idx_topics_group ON topics(group_id, ord);
CREATE INDEX IF NOT EXISTS idx_topics_title ON topics(title_norm);

-- ===== ثبت فایل‌ها (منبع اتصال موضوع↔فایل؛ R5.3) =====
CREATE TABLE IF NOT EXISTS files (
  id              INTEGER PRIMARY KEY,
  topic_id        INTEGER NOT NULL REFERENCES topics(id),
  kind            TEXT NOT NULL CHECK (kind IN
                  ('lesson','exercises_md','manifest','rubric','answer','report','summary','result')),
  owner_type      TEXT,                                -- 'lesson_version'|'exercise_set'|'exercise'|'attempt'|'attempt_item'
  owner_id        INTEGER,
  rel_path        TEXT NOT NULL UNIQUE,
  sha256          TEXT NOT NULL,                       -- hash لحظه ایجاد توسط نرم‌افزار
  size_bytes      INTEGER NOT NULL,
  immutable       INTEGER NOT NULL DEFAULT 0,          -- ۱ برای پاسخ/گزارش: هرگز بازنویسی نشود
  observed_sha256 TEXT,                                -- آخرین hash مشاهده‌شده روی دیسک
  observed_mtime  INTEGER,                             -- ns
  observed_at     TEXT,
  presence        TEXT NOT NULL DEFAULT 'present' CHECK (presence IN ('present','missing')),
  created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_files_topic_kind ON files(topic_id, kind);
CREATE INDEX IF NOT EXISTS idx_files_sha ON files(sha256);

-- ===== درس =====
CREATE TABLE IF NOT EXISTS lesson_versions (
  id            INTEGER PRIMARY KEY,
  topic_id      INTEGER NOT NULL REFERENCES topics(id),
  version       INTEGER NOT NULL,
  file_id       INTEGER NOT NULL REFERENCES files(id),
  keywords      TEXT NOT NULL,                         -- JSON آرایه کلیدواژه‌ها
  is_current    INTEGER NOT NULL DEFAULT 0,
  job_id        INTEGER REFERENCES jobs(id),
  skill         TEXT, skill_version TEXT, model TEXT,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (topic_id, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_lesson_current ON lesson_versions(topic_id) WHERE is_current = 1;

-- ===== مجموعه تمرین و تمرین‌ها =====
CREATE TABLE IF NOT EXISTS exercise_sets (
  id               INTEGER PRIMARY KEY,
  topic_id         INTEGER NOT NULL REFERENCES topics(id),
  version          INTEGER NOT NULL,                   -- v1, v2, ... (R3.2)
  exercises_file   INTEGER NOT NULL REFERENCES files(id),
  manifest_file    INTEGER NOT NULL REFERENCES files(id),
  rubric_file      INTEGER NOT NULL REFERENCES files(id),
  rubric_version   INTEGER NOT NULL DEFAULT 1,
  is_current       INTEGER NOT NULL DEFAULT 0,
  job_id           INTEGER REFERENCES jobs(id),
  skill TEXT, skill_version TEXT, model TEXT,
  created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (topic_id, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_set_current ON exercise_sets(topic_id) WHERE is_current = 1;

CREATE TABLE IF NOT EXISTS exercises (
  id               INTEGER PRIMARY KEY,
  set_id           INTEGER NOT NULL REFERENCES exercise_sets(id),
  ex_id            TEXT NOT NULL,                      -- q01 (شناسه ثابت در manifest و exercises.md)
  ord              INTEGER NOT NULL,
  title            TEXT NOT NULL,
  type             TEXT NOT NULL CHECK (type IN ('coding','conceptual','code_analysis','problem_solving')),
  difficulty       TEXT NOT NULL CHECK (difficulty IN ('easy','medium','hard')),
  weight           REAL NOT NULL CHECK (weight > 0),
  answer_file_id   INTEGER NOT NULL REFERENCES files(id),
  template_sha256  TEXT NOT NULL,                      -- hash قالب اولیه (تشخیص «بدون پاسخ»)
  external_deps    TEXT NOT NULL DEFAULT '[]',         -- JSON وابستگی‌های دانشی (R3.2)
  UNIQUE (set_id, ex_id)
);

-- ===== تلاش‌ها (ارزیابی) =====
CREATE TABLE IF NOT EXISTS attempts (
  id               INTEGER PRIMARY KEY,
  set_id           INTEGER NOT NULL REFERENCES exercise_sets(id),
  attempt_no       INTEGER NOT NULL,                   -- در محدوده هر مجموعه (attempt-1, attempt-2, ...)
  mode             TEXT NOT NULL CHECK (mode IN ('full','incremental')),
  job_id           INTEGER REFERENCES jobs(id),
  rubric_version   INTEGER NOT NULL,
  topic_score      REAL NOT NULL,                      -- محاسبه‌شده توسط نرم‌افزار (D9)
  answered_count   INTEGER NOT NULL,
  total_count      INTEGER NOT NULL,
  is_partial       INTEGER NOT NULL,                   -- «ناقص»
  result_file      INTEGER NOT NULL REFERENCES files(id),
  summary_file     INTEGER NOT NULL REFERENCES files(id),
  skill TEXT, skill_version TEXT, model TEXT,
  started_at       TEXT NOT NULL,
  finished_at      TEXT NOT NULL,
  UNIQUE (set_id, attempt_no)
);

CREATE TABLE IF NOT EXISTS attempt_items (
  id                    INTEGER PRIMARY KEY,
  attempt_id            INTEGER NOT NULL REFERENCES attempts(id),
  exercise_id           INTEGER NOT NULL REFERENCES exercises(id),
  status                TEXT NOT NULL CHECK (status IN ('evaluated','carried_over','unanswered')),
  answer_sha256         TEXT,                          -- hash پاسخ در لحظه ارزیابی
  score                 INTEGER NOT NULL CHECK (score BETWEEN 0 AND 100),
  ai_reported_score     INTEGER,                       -- برای عیب‌یابی (D9)
  carried_from_attempt  INTEGER REFERENCES attempts(id),
  report_file           INTEGER REFERENCES files(id),
  error_types           TEXT NOT NULL DEFAULT '[]',    -- JSON دسته‌بندی خطاها
  UNIQUE (attempt_id, exercise_id)
);

-- ===== پیشنهادهای Hybrid (R8.3) =====
CREATE TABLE IF NOT EXISTS suggestions (
  id                INTEGER PRIMARY KEY,
  topic_id          INTEGER NOT NULL REFERENCES topics(id),
  job_id            INTEGER REFERENCES jobs(id),
  kind              TEXT NOT NULL CHECK (kind IN ('prerequisite','supplementary')),
  title             TEXT NOT NULL,
  title_norm        TEXT NOT NULL,
  reason            TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','accepted','rejected')),
  accepted_topic_id INTEGER REFERENCES topics(id),
  created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  decided_at        TEXT,
  UNIQUE (topic_id, kind, title_norm)
);

-- ===== Job (R4) =====
CREATE TABLE IF NOT EXISTS jobs (
  id                 INTEGER PRIMARY KEY,
  kind               TEXT NOT NULL CHECK (kind IN ('lesson','exercises','evaluate','roadmap_analyze')),
  scope_key          TEXT NOT NULL,                    -- 'topic:12' | 'roadmap:3'
  topic_id           INTEGER REFERENCES topics(id),
  roadmap_id         INTEGER REFERENCES roadmaps(id),
  status             TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed','canceled')),
  cancel_requested   INTEGER NOT NULL DEFAULT 0,
  params             TEXT NOT NULL DEFAULT '{}',       -- JSON: mode، تأییدها، ...
  inputs_fingerprint TEXT,                             -- برای اعتبار checkpointها هنگام retry
  retry_of           INTEGER REFERENCES jobs(id),
  phase              TEXT,                             -- 'prepare'|'ai'|'validate'|'commit'
  progress_done      INTEGER NOT NULL DEFAULT 0,
  progress_total     INTEGER NOT NULL DEFAULT 0,
  error_code         TEXT,
  error_message      TEXT,
  commit_journal     TEXT,                             -- JSON فهرست فایل‌هایی که commit می‌نویسد (بازیابی)
  result_ref         TEXT,                             -- JSON: شناسه موجودیت‌های ساخته‌شده
  adapter TEXT, model TEXT, skill TEXT, skill_version TEXT,
  usage              TEXT,
  log_dir            TEXT,
  enqueued_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  started_at         TEXT,
  heartbeat_at       TEXT,
  finished_at        TEXT
);
-- ★ تضمین R4: در هر scope حداکثر یک Job فعال (D8)
CREATE UNIQUE INDEX IF NOT EXISTS uq_jobs_active_scope ON jobs(scope_key) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS idx_jobs_queue ON jobs(status, enqueued_at);
CREATE INDEX IF NOT EXISTS idx_jobs_topic ON jobs(topic_id, enqueued_at DESC);

CREATE TABLE IF NOT EXISTS job_steps (
  job_id        INTEGER NOT NULL REFERENCES jobs(id),
  step_key      TEXT NOT NULL,                         -- 'exercise:q03'
  status        TEXT NOT NULL CHECK (status IN ('pending','running','done','failed','skipped')),
  error_code    TEXT,
  error_message TEXT,
  started_at    TEXT, finished_at TEXT,
  PRIMARY KEY (job_id, step_key)
);

-- ===== همگام‌سازی و ممیزی =====
CREATE TABLE IF NOT EXISTS sync_runs (
  id            INTEGER PRIMARY KEY,
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  report        TEXT NOT NULL,                         -- JSON: missing/moved/new/modified
  applied       TEXT                                   -- JSON تصمیم‌های تأییدشده کاربر
);

CREATE TABLE IF NOT EXISTS audit_log (
  id            INTEGER PRIMARY KEY,
  ts            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  action        TEXT NOT NULL,
  target_type   TEXT, target_id INTEGER,
  detail        TEXT
);

CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
