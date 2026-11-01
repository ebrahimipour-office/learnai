package topics_test

import (
	"context"
	"path/filepath"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/store"
)

// Titles use ZWNJ (نوع‌ها) and Persian ک so searches with a space or Arabic ك must still match.
const md = "# کتاب Go\n## مبانی\n### نوع‌ها\n### کتابخانه استاندارد\n## همروندی\n### گوروتین\n"

func setup(t *testing.T, contents ...string) (*topics.Service, *store.DB) {
	t.Helper()
	base := t.TempDir()
	db, err := store.Open(filepath.Join(base, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	rs := roadmaps.NewService(db, root)
	ctx := context.Background()
	for _, c := range contents {
		id, err := rs.CreateDraft(ctx, "go.md", []byte(c))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := rs.Confirm(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return topics.NewService(db), db
}

func titles(nodes []*topics.Node) (out []string) {
	for _, n := range nodes {
		if n.Kind == "topic" {
			out = append(out, n.Title)
		}
		out = append(out, titles(n.Items)...)
	}
	return out
}

func list(t *testing.T, s *topics.Service, f topics.Filter) ([]*topics.Node, int, int) {
	t.Helper()
	tree, matched, total, err := s.List(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return tree, matched, total
}

func TestListGroupsInImportOrder(t *testing.T) {
	s, _ := setup(t, md)
	tree, matched, total := list(t, s, topics.Filter{})
	if matched != 3 || total != 3 || len(tree) != 1 || tree[0].Kind != "roadmap" || tree[0].Title != "go" {
		t.Fatalf("want one roadmap with 3 topics, got %d/%d %+v", matched, total, tree)
	}
	book := tree[0].Items
	if len(book) != 1 || book[0].Title != "کتاب Go" || len(book[0].Items) != 2 {
		t.Fatalf("want group کتاب Go with 2 subgroups, got %+v", book)
	}
	basics := book[0].Items[0]
	if basics.Title != "مبانی" || len(basics.Items) != 2 || basics.Items[0].Title != "نوع‌ها" {
		t.Errorf("first subgroup wrong: %+v", basics)
	}
	if got := titles(tree); len(got) != 3 || got[2] != "گوروتین" || tree[0].Items[0].Items[0].Items[0].Status != "created" {
		t.Errorf("document order: %v", got)
	}
}

func TestListSearchAndFilters(t *testing.T) {
	s, db := setup(t, md)
	for _, c := range []struct{ q, want string }{
		{"نوع ها", "نوع‌ها"},               // space matches ZWNJ
		{"كتابخانه", "کتابخانه استاندارد"}, // Arabic ك matches Persian ک
		{"  GOROUTINE  ", ""},              // trimmed, case-folded, no match
		{"گوروتین", "گوروتین"},
	} {
		tree, matched, total := list(t, s, topics.Filter{Query: c.q})
		got := titles(tree)
		if total != 3 || (c.want == "" && matched != 0) || (c.want != "" && (matched != 1 || got[0] != c.want)) {
			t.Errorf("q=%q: want %q, got %v (total %d)", c.q, c.want, got, total)
		}
	}
	// A match keeps only its own branch: the همروندی group disappears.
	if tree, _, _ := list(t, s, topics.Filter{Query: "نوع"}); len(tree[0].Items[0].Items) != 1 {
		t.Errorf("unmatched groups must be dropped: %+v", tree[0].Items[0].Items)
	}

	if _, err := db.Writer().Exec(`UPDATE topics SET status = 'content_ready', state = 'archived', origin = 'suggested' WHERE title = 'گوروتین'`); err != nil {
		t.Fatal(err)
	}
	tree, matched, _ := list(t, s, topics.Filter{Status: "content_ready"})
	if matched != 1 {
		t.Fatalf("status filter: want 1, got %d", matched)
	}
	g := tree[0].Items[0].Items[0].Items[0]
	if g.State != "archived" || g.Origin != "suggested" || g.Score.Valid {
		t.Errorf("topic fields: %+v", g)
	}
}

func TestListShowsLatestAttemptOfCurrentSet(t *testing.T) {
	s, db := setup(t, "# A\n## T\n")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Writer().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 5; i++ {
		exec(`INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes) VALUES (?, 1, 'result', ?, 'x', 1)`, i, "f"+string(rune('0'+i)))
	}
	exec(`INSERT INTO exercise_sets (id, topic_id, version, exercises_file, manifest_file, rubric_file, is_current) VALUES (1, 1, 1, 1, 2, 3, 1)`)
	for _, a := range []struct {
		no      int
		score   float64
		partial int
	}{{1, 90, 0}, {2, 40, 1}} {
		exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, topic_score, answered_count, total_count, is_partial,
			result_file, summary_file, started_at, finished_at) VALUES (1, ?, 'full', 1, ?, 1, 2, ?, 4, 5, 'x', 'x')`, a.no, a.score, a.partial)
	}
	tree, _, _ := list(t, s, topics.Filter{})
	topic := tree[0].Items[0].Items[0]
	if !topic.Score.Valid || topic.Score.Float64 != 40 || !topic.Partial {
		t.Errorf("want latest attempt 40 partial, got %+v", topic)
	}
}

func TestListEmpty(t *testing.T) {
	s, _ := setup(t)
	if tree, matched, total := list(t, s, topics.Filter{}); tree != nil || matched != 0 || total != 0 {
		t.Errorf("empty db: %v %d %d", tree, matched, total)
	}
}
