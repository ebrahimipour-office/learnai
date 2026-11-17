package topics_test

import (
	"context"
	"errors"
	"testing"

	"learninghub/internal/domain"
	"learninghub/internal/services/topics"
)

func TestPanelNotFound(t *testing.T) {
	s, _ := setup(t, md)
	ctx := context.Background()

	_, err := s.Panel(ctx, 99999)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestScope(t *testing.T) {
	if got := topics.Scope(42); got != "topic:42" {
		t.Fatalf("expected topic:42, got %q", got)
	}
}

func TestPanelBasics(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()

	var topicID int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'کتابخانه استاندارد'`).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	p, err := s.Panel(ctx, topicID)
	if err != nil {
		t.Fatalf("Panel failed: %v", err)
	}
	if p.ID != topicID {
		t.Fatalf("expected ID %d, got %d", topicID, p.ID)
	}
	if p.Title != "کتابخانه استاندارد" {
		t.Fatalf("expected title 'کتابخانه استاندارد', got %q", p.Title)
	}
	if p.Busy {
		t.Fatalf("expected topic not to be busy")
	}

	// Insert a running job for this topic -> busy must become true
	if _, err := w.Exec(`INSERT INTO jobs (id, kind, scope_key, topic_id, adapter, status)
		VALUES (1001, 'lesson', ?, ?, 'fake', 'running')`,
		topics.Scope(topicID), topicID); err != nil {
		t.Fatal(err)
	}

	p, err = s.Panel(ctx, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Busy {
		t.Fatalf("expected topic to be busy when a job is running")
	}

	// Add lesson versions
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (101, ?, 'topics/02-std/lesson.md', 'lesson', 'abc', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO lesson_versions (topic_id, version, file_id, keywords, is_current, model, created_at)
		VALUES (?, 1, 101, '["go"]', 1, 'gemini', '2026-11-15T12:05:00Z')`, topicID); err != nil {
		t.Fatal(err)
	}

	p, err = s.Panel(ctx, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lessons) != 1 {
		t.Fatalf("expected 1 lesson, got %d", len(p.Lessons))
	}
	if p.Lessons[0].Version != 1 || !p.Lessons[0].Current || p.Lessons[0].Model != "gemini" || p.Lessons[0].RelPath != "topics/02-std/lesson.md" {
		t.Fatalf("unexpected lesson data: %+v", p.Lessons[0])
	}

	// Add source file reference
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (102, ?, 'topics/02-std/source.md', 'source', 'def', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`UPDATE topics SET source_file_id = 102, source_start_line = 10, source_end_line = 25 WHERE id = ?`, topicID); err != nil {
		t.Fatal(err)
	}

	p, err = s.Panel(ctx, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source == nil {
		t.Fatal("expected non-nil Source")
	}
	if p.Source.FileID != 102 || p.Source.RelPath != "topics/02-std/source.md" || p.Source.Start != 10 || p.Source.End != 25 {
		t.Fatalf("unexpected source: %+v", p.Source)
	}
}

func TestPanelAncestorsAndChildren(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()

	var rootID, childID, grandChildID int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'نوع‌ها'`).Scan(&rootID); err != nil {
		t.Fatal(err)
	}

	// Create child and grandchild
	res, err := w.Exec(`INSERT INTO topics (roadmap_id, title, title_norm, slug, stable_key, dir_name, ord, parent_id, state)
		VALUES (1, 'زیرنوع ۱', 'زیرنوع ۱', 'sub-1', 'k01', 'sub-1', 1.1, ?, 'active')`, rootID)
	if err != nil {
		t.Fatal(err)
	}
	childID, _ = res.LastInsertId()

	res, err = w.Exec(`INSERT INTO topics (roadmap_id, title, title_norm, slug, stable_key, dir_name, ord, parent_id, state)
		VALUES (1, 'زیرنوع ۲', 'زیرنوع ۲', 'sub-2', 'k02', 'sub-2', 1.2, ?, 'archived')`, childID)
	if err != nil {
		t.Fatal(err)
	}
	grandChildID, _ = res.LastInsertId()

	// Panel of grandchild should list ancestors [rootID, childID]
	p, err := s.Panel(ctx, grandChildID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Ancestors) != 2 {
		t.Fatalf("expected 2 ancestors, got %d", len(p.Ancestors))
	}
	if p.Ancestors[0].ID != rootID || p.Ancestors[1].ID != childID {
		t.Fatalf("unexpected ancestors: %+v", p.Ancestors)
	}
	if p.Ancestors[1].Archived {
		t.Fatalf("child is active, but marked archived")
	}

	// Panel of child should list children [grandChildID]
	pChild, err := s.Panel(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pChild.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(pChild.Children))
	}
	if pChild.Children[0].ID != grandChildID || !pChild.Children[0].Archived {
		t.Fatalf("unexpected child: %+v", pChild.Children[0])
	}
}

func TestSuggestionTopic(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()

	var topicID int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'نوع‌ها'`).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	// Non-existent suggestion
	_, err := s.SuggestionTopic(ctx, 9999)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Insert suggestion
	if _, err := w.Exec(`INSERT INTO suggestions (id, topic_id, kind, title, title_norm, reason)
		VALUES (10, ?, 'prerequisite', 'پیش‌نیاز', 'پیش‌نیاز', 'دلیل')`, topicID); err != nil {
		t.Fatal(err)
	}

	got, err := s.SuggestionTopic(ctx, 10)
	if err != nil {
		t.Fatalf("SuggestionTopic failed: %v", err)
	}
	if got != topicID {
		t.Fatalf("expected topic %d, got %d", topicID, got)
	}
}

// T9.9: Prev/Next follow the roadmap's tree order over active topics.
func TestPanelPrevNext(t *testing.T) {
	s, db := setup(t, md)
	// tree rule: «جزئیات» is a child of «نوع‌ها», ordered between it and the next sibling
	if _, err := db.Writer().Exec(`INSERT INTO topics (roadmap_id, group_id, parent_id, stable_key, title, title_norm, ord, slug, dir_name)
		SELECT roadmap_id, group_id, id, 'child', 'جزئیات', 'جزئیات', ord + 0.5, 'details', '99-details' FROM topics WHERE title = 'نوع‌ها'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := func(title string) int64 {
		var id int64
		if err := db.Writer().QueryRow(`SELECT id FROM topics WHERE title = ?`, title).Scan(&id); err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		return id
	}
	title := func(r *topics.Ref) string {
		if r == nil {
			return ""
		}
		return r.Title
	}
	check := func(at, prev, next string) {
		t.Helper()
		p, err := s.Panel(ctx, id(at))
		if err != nil {
			t.Fatal(err)
		}
		if title(p.Prev) != prev || title(p.Next) != next {
			t.Errorf("%s: prev/next = %q/%q, want %q/%q", at, title(p.Prev), title(p.Next), prev, next)
		}
	}
	check("نوع‌ها", "", "جزئیات")                    // first
	check("جزئیات", "نوع‌ها", "کتابخانه استاندارد")  // child sits between its parent and the next sibling
	check("کتابخانه استاندارد", "جزئیات", "گوروتین") // crosses groups
	check("گوروتین", "کتابخانه استاندارد", "")       // last

	if _, err := db.Writer().Exec(`UPDATE topics SET state = 'archived' WHERE title = 'کتابخانه استاندارد'`); err != nil {
		t.Fatal(err)
	}
	check("جزئیات", "نوع‌ها", "گوروتین")             // archived topics are skipped
	check("کتابخانه استاندارد", "جزئیات", "گوروتین") // an archived topic still links to its active neighbours
}
