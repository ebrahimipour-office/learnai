package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openAt opens a fresh database migrated only up to version v.
func openAt(t *testing.T, v int) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.db")
	w, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	w.SetMaxOpenConns(1)
	d := &DB{writer: w, path: path}
	t.Cleanup(func() { w.Close() })
	if err := d.migrateTo(v); err != nil {
		t.Fatalf("migrate to %d: %v", v, err)
	}
	return d
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestMigrate0003KeepsDataAndReferences(t *testing.T) {
	d := openAt(t, 2)
	w := d.writer
	mustExec(t, w, `INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'r', 'R', '{}')`)
	mustExec(t, w, `INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name) VALUES (7, 1, 'k', 'T', 't', 1, 't', '01-t')`)
	mustExec(t, w, `INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes, immutable) VALUES (40, 7, 'lesson', 'topics/01-t/lesson.md', 'h', 3, 0)`)
	mustExec(t, w, `INSERT INTO jobs (id, kind, scope_key, topic_id, status) VALUES (90, 'lesson', 'topic:7', 7, 'failed')`)
	mustExec(t, w, `INSERT INTO jobs (id, kind, scope_key, topic_id, status, retry_of) VALUES (91, 'lesson', 'topic:7', 7, 'running', 90)`)
	mustExec(t, w, `INSERT INTO job_steps (job_id, step_key, status) VALUES (91, 'lesson', 'running')`)
	mustExec(t, w, `INSERT INTO lesson_versions (topic_id, version, file_id, keywords, is_current, job_id) VALUES (7, 1, 40, '[]', 1, 91)`)

	if err := d.migrateTo(3); err != nil {
		t.Fatalf("migrate to 3: %v", err)
	}
	if v, _ := d.UserVersion(); v != 3 {
		t.Fatalf("user_version = %d, want 3", v)
	}
	var fk int
	if err := w.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (%v), want 1 after migration", fk, err)
	}

	// ids and references survive the table rebuilds
	var rel, step string
	var retry int64
	if err := w.QueryRow(`SELECT f.rel_path, j.retry_of, s.step_key FROM lesson_versions l
		JOIN files f ON f.id = l.file_id JOIN jobs j ON j.id = l.job_id JOIN job_steps s ON s.job_id = j.id`).Scan(&rel, &retry, &step); err != nil {
		t.Fatal(err)
	}
	if rel != "topics/01-t/lesson.md" || retry != 90 || step != "lesson" {
		t.Fatalf("got %q %d %q", rel, retry, step)
	}

	// new columns and CHECK values
	mustExec(t, w, `UPDATE import_drafts SET title = 'x' WHERE 0`)
	mustExec(t, w, `INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes) VALUES (41, 7, 'source', 'topics/01-t/source.md', 'h', 1)`)
	mustExec(t, w, `UPDATE topics SET source_start_line = 3, source_end_line = 9, source_sha256 = 'h', source_file_id = 41 WHERE id = 7`)
	mustExec(t, w, `INSERT INTO jobs (kind, scope_key, roadmap_id, status) VALUES ('chapter_detect', 'draft:abc', 1, 'queued')`)

	// the rebuilt tables keep their guards
	if _, err := w.Exec(`INSERT INTO jobs (kind, scope_key, topic_id, status) VALUES ('exercises', 'topic:7', 7, 'queued')`); err == nil ||
		!strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("second active job in one scope: err = %v, want UNIQUE violation", err)
	}
	if _, err := w.Exec(`INSERT INTO files (topic_id, kind, rel_path, sha256, size_bytes) VALUES (999, 'source', 'x', 'h', 1)`); err == nil {
		t.Fatal("file for a missing topic was accepted: foreign keys are off")
	}
	if _, err := w.Exec(`INSERT INTO files (topic_id, kind, rel_path, sha256, size_bytes) VALUES (7, 'bogus', 'y', 'h', 1)`); err == nil {
		t.Fatal("unknown files.kind was accepted")
	}
}

func TestMigrateRollsBackOnBrokenReference(t *testing.T) {
	d := openAt(t, 2)
	// a dangling reference can only exist with foreign keys off; the check must stop the next migration
	mustExec(t, d.writer, `PRAGMA foreign_keys = OFF`)
	mustExec(t, d.writer, `INSERT INTO files (topic_id, kind, rel_path, sha256, size_bytes) VALUES (999, 'lesson', 'x', 'h', 1)`)
	mustExec(t, d.writer, `PRAGMA foreign_keys = ON`)
	if err := d.migrateTo(3); err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("err = %v, want foreign_key_check failure", err)
	}
	if v, _ := d.UserVersion(); v != 2 {
		t.Fatalf("user_version = %d, want 2 after rollback", v)
	}
	var n int
	if err := d.writer.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('import_drafts') WHERE name = 'title'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("title column present after rollback (n=%d, %v)", n, err)
	}
}

// CR-01: old 'suggestion-N' keys become 16 hex; other keys are untouched.
func TestMigrate0004RewritesSuggestionKeys(t *testing.T) {
	d := openAt(t, 3)
	w := d.writer
	mustExec(t, w, `INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'r', 'R', '{}')`)
	mustExec(t, w, `INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name, origin) VALUES
		(7, 1, 'a3f9c2d41b7e8f06', 'T', 't', 1, 't', '01-t', 'roadmap'),
		(8, 1, 'suggestion-7', 'S', 's', 2, 's', '02-s', 'suggested'),
		(9, 1, 'suggestion-12', 'S2', 's2', 3, 's2', '03-s2', 'suggested')`)
	if err := d.migrateTo(4); err != nil {
		t.Fatalf("migrate to 4: %v", err)
	}
	for id, want := range map[int]string{7: "a3f9c2d41b7e8f06", 8: "0000000000000008", 9: "0000000000000009"} {
		var got string
		if err := w.QueryRow(`SELECT stable_key FROM topics WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("topic %d: stable_key = %q (%v), want %q", id, got, err, want)
		}
	}
}
