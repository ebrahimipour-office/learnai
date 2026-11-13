package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
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
	if version != 5 {
		t.Errorf("Expected user_version 5, got %d", version)
	}
	db.Close()

	// 2. Second open: should safely recognize version 3 without re-running
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to re-open store: %v", err)
	}
	defer db2.Close()

	version2, err := db2.UserVersion()
	if err != nil {
		t.Fatalf("Failed to get user_version: %v", err)
	}
	if version2 != 5 {
		t.Errorf("Expected user_version 5, got %d", version2)
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
