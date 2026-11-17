package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"learninghub/internal/store"
)

func TestDB_OpenAndMigrate(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// 1. First open: runs migration 0001
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}

	version, err := db.UserVersion()
	if err != nil {
		t.Fatalf("Failed to get user_version: %v", err)
	}
	if version != 8 {
		t.Errorf("Expected user_version 8, got %d", version)
	}
	db.Close()

	// 2. Second open: should safely recognize version 6 without re-running
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to re-open store: %v", err)
	}
	defer db2.Close()

	version2, err := db2.UserVersion()
	if err != nil {
		t.Fatalf("Failed to get user_version: %v", err)
	}
	if version2 != 8 {
		t.Errorf("Expected user_version 8, got %d", version2)
	}
}

// CR-39: an existing database whose user_version is newer than the binary
// must be refused by Open so older binaries do not operate on an unknown schema.
func TestDB_OpenRejectsNewerSchema(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "newer.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	// Bump user_version to 99 to simulate a database written by a future version.
	if _, err := db.Writer().Exec("PRAGMA user_version = 99;"); err != nil {
		t.Fatalf("Failed to set user_version: %v", err)
	}
	db.Close()

	// Opening again must fail because 99 > len(migrations).
	_, err = store.Open(dbPath)
	if err == nil {
		t.Fatal("Open succeeded on a newer database, expected error")
	}
	if !strings.Contains(err.Error(), "database schema v99 is newer than this hub") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestDB_Constraints(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "constraints.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// 1. Foreign Key Enforcement: inserting topic without existing roadmap should fail
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO topics (roadmap_id, stable_key, title, title_norm, ord, slug, dir_name)
			VALUES (9999, 'key1', 'Title', 'title', 1.0, 'slug', '01-slug')
		`)
		return err
	})
	if err == nil {
		t.Errorf("Expected foreign key violation when inserting topic without roadmap, got nil")
	}

	// 2. Insert valid roadmap
	var roadmapID int64
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO roadmaps (slug, title, select_rule)
			VALUES ('go-test', 'Go Test Roadmap', '{}')
		`)
		if err != nil {
			return err
		}
		roadmapID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert roadmap: %v", err)
	}

	// Insert valid topic
	var topicID int64
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO topics (roadmap_id, stable_key, title, title_norm, ord, slug, dir_name)
			VALUES (?, 'key1', 'Topic 1', 'topic 1', 1.0, 'topic-1', '01-topic-1')
		`, roadmapID)
		if err != nil {
			return err
		}
		topicID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert topic: %v", err)
	}

	// 3. Test Single Active Job per Scope Constraint (uq_jobs_active_scope, D8 & R4)
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO jobs (kind, scope_key, topic_id, status)
			VALUES ('lesson', 'topic:1', ?, 'running')
		`, topicID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert first running job: %v", err)
	}

	// Second running job with same scope MUST fail with UNIQUE constraint
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO jobs (kind, scope_key, topic_id, status)
			VALUES ('exercises', 'topic:1', ?, 'queued')
		`, topicID)
		return err
	})
	if err == nil {
		t.Errorf("Expected unique constraint violation for concurrent active job in same scope, got nil")
	}

	// But a completed/failed job in same scope should be allowed
	err = db.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO jobs (kind, scope_key, topic_id, status)
			VALUES ('lesson', 'topic:1', ?, 'succeeded')
		`, topicID)
		return err
	})
	if err != nil {
		t.Errorf("Completed job should be allowed in same scope: %v", err)
	}
}

// CR-14: database paths containing '#', '%', or '?' must be escaped in SQLite DSN
// so they open at the exact intended location without creating stray files in parent directories.
func TestDB_EscapedPath(t *testing.T) {
	cases := []struct {
		name    string
		subDir  string
		skipWin bool
	}{
		{name: "hash in path", subDir: "a#b"},
		{name: "percent in path", subDir: "p%41"},
		{name: "question mark in path", subDir: "q?x", skipWin: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipWin && runtime.GOOS == "windows" {
				t.Skip("skipping filename with '?' on Windows")
			}
			tempDir := t.TempDir()
			dir := filepath.Join(tempDir, tc.subDir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("failed to create subdir: %v", err)
			}
			dbPath := filepath.Join(dir, "app.db")
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("failed to open database at %q: %v", dbPath, err)
			}
			defer db.Close()

			// Verify the database file was created at the exact path
			if _, err := os.Stat(dbPath); err != nil {
				t.Errorf("database file does not exist at expected path %q: %v", dbPath, err)
			}

			// Verify nothing stray was created in tempDir (parent of subDir)
			entries, err := os.ReadDir(tempDir)
			if err != nil {
				t.Fatalf("reading tempDir: %v", err)
			}
			if len(entries) != 1 || entries[0].Name() != tc.subDir {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("unexpected files created in parent tempDir: got %v, want [%s]", names, tc.subDir)
			}
		})
	}
}

func TestDB_OpenReadOnly(t *testing.T) {
	tempDir := t.TempDir()
	nonExistent := filepath.Join(tempDir, "missing.db")

	// 1. Non-existent file should fail to open read-only.
	if _, err := store.OpenReadOnly(nonExistent); err == nil {
		t.Fatal("expected OpenReadOnly to fail on missing file, but it succeeded")
	}

	// 2. Open existing database read-write and populate.
	dbPath := filepath.Join(tempDir, "app.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO meta (key, value) VALUES ('adapter_kind', 'claude')`); err != nil {
		t.Fatalf("failed to insert meta: %v", err)
	}
	db.Close()

	// 3. Open existing database read-only.
	roDB, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly failed on existing db: %v", err)
	}
	defer roDB.Close()

	// Reading should succeed.
	var val string
	if err := roDB.Writer().QueryRow(`SELECT value FROM meta WHERE key = 'adapter_kind'`).Scan(&val); err != nil {
		t.Fatalf("querying read-only db failed: %v", err)
	}
	if val != "claude" {
		t.Fatalf("want 'claude', got %q", val)
	}

	// Writing should fail.
	if _, err := roDB.Writer().Exec(`INSERT INTO meta (key, value) VALUES ('test', 'val')`); err == nil {
		t.Fatal("expected write to fail on read-only db, but it succeeded")
	}
}

// CR-I6: index idx_jobs_scope exists on jobs(scope_key, id).
func TestDB_JobsScopeIndex(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "scope_idx.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	var name string
	err = db.Writer().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_jobs_scope'`).Scan(&name)
	if err != nil {
		t.Fatalf("index idx_jobs_scope not found: %v", err)
	}
	if name != "idx_jobs_scope" {
		t.Errorf("expected idx_jobs_scope, got %q", name)
	}
}
