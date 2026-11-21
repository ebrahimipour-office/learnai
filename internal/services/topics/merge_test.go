package topics_test

import (
	"context"
	"errors"
	"testing"

	"learninghub/internal/domain"
)

// TestMerge: an empty renamed topic hands its place to the archived original (RK8, T11.2).
func TestMerge(t *testing.T) {
	s, db, rs := setupRS(t, "# Go\n## Basics\n### Types\n### Loops\n")
	ctx := context.Background()
	id, err := rs.CreateReimport(ctx, 1, "go.md", []byte("# Go\n## Basics\n### Types\n### Loop\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rs.Confirm(ctx, id); err != nil {
		t.Fatal(err)
	}
	topicID := func(title string) (id int64) {
		t.Helper()
		if err := db.Writer().QueryRow(`SELECT id FROM topics WHERE title = ?`, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	loops, loop := topicID("Loops"), topicID("Loop")
	if c, err := s.MergeCandidates(ctx, loop); err != nil || len(c) != 1 || c[0].ID != loops {
		t.Fatalf("candidates: %v %v", c, err)
	}
	if c, _ := s.MergeCandidates(ctx, loops); c != nil {
		t.Errorf("archived topic must offer nothing: %v", c)
	}

	// target with generated content ⇒ refused, nothing changes
	if _, err := db.Writer().Exec(`INSERT INTO files (topic_id, kind, rel_path, sha256, size_bytes) VALUES (?, 'lesson', 'x/lesson.md', 'h', 1)`, loop); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.MergeCandidates(ctx, loop); c != nil {
		t.Errorf("topic with a lesson must offer nothing: %v", c)
	}
	if err := s.Merge(ctx, loop, loops); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("merge into topic with content: %v", err)
	}
	if _, err := db.Writer().Exec(`DELETE FROM files WHERE kind = 'lesson'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Merge(ctx, loop, topicID("Types")); !errors.Is(err, domain.ErrInvalidState) {
		t.Errorf("source not archived: %v", err)
	}

	var key string
	_ = db.Writer().QueryRow(`SELECT stable_key FROM topics WHERE id = ?`, loop).Scan(&key)
	if err := s.Merge(ctx, loop, loops); err != nil {
		t.Fatal(err)
	}
	var n int
	var gotKey, title, state, dir string
	_ = db.Writer().QueryRow(`SELECT COUNT(*) FROM topics`).Scan(&n)
	_ = db.Writer().QueryRow(`SELECT stable_key, title, state, dir_name FROM topics WHERE id = ?`, loops).Scan(&gotKey, &title, &state, &dir)
	if n != 2 || gotKey != key || title != "Loop" || state != "active" || dir != "02-loops" {
		t.Errorf("after merge: %d topics, %q %q %q %q", n, gotKey, title, state, dir)
	}
	if err := db.Writer().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'topic_merged'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows: %d %v", n, err)
	}
	// the merged topic stays matched on the next re-import
	d, err := rs.CreateReimport(ctx, 1, "go.md", []byte("# Go\n## Basics\n### Types\n### Loop\n"))
	if err != nil {
		t.Fatal(err)
	}
	draft, _ := rs.Draft(ctx, d)
	if diff, err := rs.Diff(ctx, draft); err != nil || len(diff.New)+len(diff.Gone) != 0 {
		t.Errorf("re-import after merge: %+v %v", diff, err)
	}
}
