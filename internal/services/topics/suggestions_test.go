package topics_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"learninghub/internal/domain"
	"learninghub/internal/services/topics"
)

// T6.1: pending suggestions never reach the list; accepting places a 'suggested' topic next to its
// source without moving roadmap topics; rejecting archives; a decided suggestion cannot change; audit_log records both.
func TestSuggestionDecisions(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()
	var src int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'کتابخانه استاندارد'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO suggestions (id, topic_id, kind, title, title_norm, reason) VALUES (1, ?, 'prerequisite', 'بسته‌ها', 'بسته‌ها', 'لازم است')`,
		`INSERT INTO suggestions (id, topic_id, kind, title, title_norm, reason) VALUES (2, ?, 'supplementary', 'io پیشرفته', 'io پیشرفته', 'ادامه')`,
		`INSERT INTO suggestions (id, topic_id, kind, title, title_norm, reason) VALUES (3, ?, 'supplementary', 'بی‌ربط', 'بی‌ربط', '-')`,
	} {
		if _, err := w.Exec(q, src); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"نوع‌ها", "کتابخانه استاندارد", "گوروتین"}
	tree, _, _ := list(t, s, topics.Filter{})
	if got := titles(tree); !slices.Equal(got, want) {
		t.Fatalf("pending suggestions must not be listed: %v", got)
	}
	ords := func() map[string]float64 {
		m := map[string]float64{}
		rows, err := w.Query(`SELECT title, ord FROM topics WHERE origin = 'roadmap'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			var o float64
			if err := rows.Scan(&k, &o); err != nil {
				t.Fatal(err)
			}
			m[k] = o
		}
		return m
	}
	before := ords()

	pre, err := s.AcceptSuggestion(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptSuggestion(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.RejectSuggestion(ctx, 3); err != nil {
		t.Fatal(err)
	}
	tree, _, _ = list(t, s, topics.Filter{})
	if got, want := titles(tree), []string{"نوع‌ها", "بسته‌ها", "کتابخانه استاندارد", "io پیشرفته", "گوروتین"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if after := ords(); len(after) != len(before) || after["گوروتین"] != before["گوروتین"] || after["نوع‌ها"] != before["نوع‌ها"] {
		t.Fatalf("roadmap ords changed: %v -> %v", before, after)
	}
	var origin, group, srcGroup string
	if err := w.QueryRow(`SELECT origin, COALESCE(group_id, '') FROM topics WHERE id = ?`, pre).Scan(&origin, &group); err != nil {
		t.Fatal(err)
	}
	if err := w.QueryRow(`SELECT COALESCE(group_id, '') FROM topics WHERE id = ?`, src).Scan(&srcGroup); err != nil {
		t.Fatal(err)
	}
	if origin != "suggested" || group != srcGroup {
		t.Fatalf("origin=%s group=%s, want suggested in group %s", origin, group, srcGroup)
	}

	for _, id := range []int64{1, 3} {
		if _, err := s.AcceptSuggestion(ctx, id); !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("accept decided %d: %v", id, err)
		}
		if err := s.RejectSuggestion(ctx, id); !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("reject decided %d: %v", id, err)
		}
	}
	if err := s.RejectSuggestion(ctx, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reject missing: %v", err)
	}

	got, err := s.Suggestions(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	st := map[int64]string{}
	for _, x := range got {
		st[x.ID] = x.Status
	}
	if st[1] != "accepted" || st[2] != "accepted" || st[3] != "rejected" {
		t.Fatalf("statuses = %v", got)
	}
	var accepted, rejected int
	if err := w.QueryRow(`SELECT COUNT(*) FILTER (WHERE action = 'suggestion_accepted'), COUNT(*) FILTER (WHERE action = 'suggestion_rejected') FROM audit_log`).
		Scan(&accepted, &rejected); err != nil {
		t.Fatal(err)
	}
	if accepted != 2 || rejected != 1 {
		t.Fatalf("audit_log accepted=%d rejected=%d", accepted, rejected)
	}
}

// A re-import must not archive an accepted suggested topic (it is not in the roadmap file), and its
// dir_name follows the global topic sequence.
func TestSuggestedTopicSurvivesReimport(t *testing.T) {
	s, db, rs := setupRS(t, md)
	ctx := context.Background()
	w := db.Writer()
	if _, err := w.Exec(`INSERT INTO suggestions (id, topic_id, kind, title, title_norm, reason)
		SELECT 1, id, 'supplementary', 'جنریک', 'جنریک', 'r' FROM topics WHERE title = 'نوع‌ها'`); err != nil {
		t.Fatal(err)
	}
	id, err := s.AcceptSuggestion(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := rs.CreateReimport(ctx, 1, "go.md", []byte(md))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rs.Confirm(ctx, draft); err != nil {
		t.Fatal(err)
	}
	var dir, state, origin string
	if err := w.QueryRow(`SELECT dir_name, state, origin FROM topics WHERE id = ?`, id).Scan(&dir, &state, &origin); err != nil {
		t.Fatal(err)
	}
	if dir != "04-جنریک" || state != "active" || origin != "suggested" {
		t.Fatalf("after re-import: dir=%q state=%s origin=%s", dir, state, origin)
	}
	tree, _, _ := list(t, s, topics.Filter{})
	if got, want := titles(tree), []string{"نوع‌ها", "جنریک", "کتابخانه استاندارد", "گوروتین"}; !slices.Equal(got, want) {
		t.Fatalf("order after re-import = %v, want %v", got, want)
	}
}
