package topics_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/services/lessons"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/store"
)

// Titles use ZWNJ (نوع‌ها) and Persian ک so searches with a space or Arabic ك must still match.
const md = "# کتاب Go\n## مبانی\n### نوع‌ها\n### کتابخانه استاندارد\n## همروندی\n### گوروتین\n"

func setup(t *testing.T, contents ...string) (*topics.Service, *store.DB) {
	t.Helper()
	s, db, _ := setupRS(t, contents...)
	return s, db
}

// setupRS also returns the roadmaps service, for re-imports.
func setupRS(t *testing.T, contents ...string) (*topics.Service, *store.DB, *roadmaps.Service) {
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
	return topics.NewService(db, root), db, rs
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
	// the sole H1 is the roadmap title, not a group (T7.2)
	if matched != 3 || total != 3 || len(tree) != 1 || tree[0].Kind != "roadmap" || tree[0].Title != "کتاب Go" {
		t.Fatalf("want one roadmap with 3 topics, got %d/%d %+v", matched, total, tree)
	}
	book := tree[0].Items
	if len(book) != 2 {
		t.Fatalf("want 2 top-level groups, got %+v", book)
	}
	basics := book[0]
	if basics.Title != "مبانی" || len(basics.Items) != 2 || basics.Items[0].Title != "نوع‌ها" {
		t.Errorf("first group wrong: %+v", basics)
	}
	if got := titles(tree); len(got) != 3 || got[2] != "گوروتین" || tree[0].Items[0].Items[0].Status != "created" {
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
	// Matching group title returns topics inside that group
	treeGroup, matchedGroup, _ := list(t, s, topics.Filter{Query: "همروندی"})
	if matchedGroup != 1 || len(titles(treeGroup)) != 1 || titles(treeGroup)[0] != "گوروتین" {
		t.Errorf("group title search: want [گوروتین], got %v", titles(treeGroup))
	}
	treeBasics, matchedBasics, _ := list(t, s, topics.Filter{Query: "مبانی"})
	if matchedBasics != 2 || len(titles(treeBasics)) != 2 {
		t.Errorf("group title search for مبانی: want 2 topics, got %d (%v)", matchedBasics, titles(treeBasics))
	}

	// A match keeps only its own branch: the همروندی group disappears.
	if tree, _, _ := list(t, s, topics.Filter{Query: "نوع"}); len(tree[0].Items) != 1 {
		t.Errorf("unmatched groups must be dropped: %+v", tree[0].Items)
	}

	if _, err := db.Writer().Exec(`UPDATE topics SET status = 'content_ready', state = 'archived', origin = 'suggested' WHERE title = 'گوروتین'`); err != nil {
		t.Fatal(err)
	}
	if _, matched, _ := list(t, s, topics.Filter{Status: "content_ready"}); matched != 0 {
		t.Fatalf("archived topics are hidden by default (T9.12), got %d", matched)
	}
	tree, matched, _ := list(t, s, topics.Filter{Status: "content_ready", Archived: true})
	if matched != 1 {
		t.Fatalf("status filter: want 1, got %d", matched)
	}
	g := tree[0].Items[0].Items[0]
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
	// the fixed file ids below need the source.md row of the import (§12.6) out of the way
	exec(`UPDATE topics SET source_file_id = NULL`)
	exec(`DELETE FROM files`)
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
	exec(`UPDATE files SET presence = 'missing' WHERE id IN (1, 2)`)
	tree, _, _ := list(t, s, topics.Filter{})
	topic := tree[0].Items[0] // "# A" is the title, so T is ungrouped
	if !topic.Score.Valid || topic.Score.Float64 != 40 || !topic.Partial {
		t.Errorf("want latest attempt 40 partial, got %+v", topic)
	}
	if topic.Missing != 2 {
		t.Errorf("Missing = %d, want 2", topic.Missing)
	}
	if rm := tree[0]; !rm.Avg.Valid || rm.Avg.Float64 != 40 {
		t.Errorf("roadmap Avg = %v, want latest score 40", rm.Avg)
	}
}

// TestListRoadmapProgress: T9.8 counts active topics only, picks the first unevaluated one in
// roadmap order, and ignores the search filter.
func TestListRoadmapProgress(t *testing.T) {
	s, db := setup(t, md)
	var ids []int64
	rows, err := db.Writer().Query(`SELECT id FROM topics ORDER BY ord`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 3 {
		t.Fatalf("want 3 topics, got %d", len(ids))
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Writer().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(f topics.Filter, done, total int, next int64) {
		t.Helper()
		tree, _, _ := list(t, s, f)
		if len(tree) != 1 {
			t.Fatalf("want 1 roadmap, got %d", len(tree))
		}
		rm := tree[0]
		if rm.Done != done || rm.Total != total || rm.NextID != next || rm.Avg.Valid {
			t.Errorf("%+v: got done=%d total=%d next=%d avg=%v, want %d/%d next=%d no avg", f, rm.Done, rm.Total, rm.NextID, rm.Avg, done, total, next)
		}
	}
	check(topics.Filter{}, 0, 3, ids[0])
	exec(`UPDATE topics SET status = 'evaluated' WHERE id = ?`, ids[0])
	check(topics.Filter{}, 1, 3, ids[1])
	check(topics.Filter{Query: "گوروتین"}, 1, 3, ids[1]) // filter narrows the tree, not the stats
	// T10.4: a topic with work under way comes before an earlier one not yet started
	exec(`UPDATE topics SET status = 'content_ready' WHERE id = ?`, ids[2])
	check(topics.Filter{}, 1, 3, ids[2])
	exec(`UPDATE topics SET status = 'created' WHERE id = ?`, ids[2])
	exec(`UPDATE topics SET state = 'archived' WHERE id = ?`, ids[1])
	check(topics.Filter{}, 1, 2, ids[2])
	exec(`UPDATE topics SET status = 'evaluated' WHERE id = ?`, ids[2])
	check(topics.Filter{}, 2, 2, 0)
}

func TestListEmpty(t *testing.T) {
	s, _ := setup(t)
	if tree, matched, total := list(t, s, topics.Filter{}); tree != nil || matched != 0 || total != 0 {
		t.Errorf("empty db: %v %d %d", tree, matched, total)
	}
}

// T9.15: a word found only in a topic's lesson text finds the topic, labelled "in lesson".
func TestListSearchesLessonText(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	index := func(title, body string) {
		t.Helper()
		if err := db.WithTx(ctx, func(tx *sql.Tx) error {
			var id int64
			if err := tx.QueryRow(`SELECT id FROM topics WHERE title = ?`, title).Scan(&id); err != nil {
				return err
			}
			return lessons.IndexText(ctx, tx, id, []byte(body))
		}); err != nil {
			t.Fatal(err)
		}
	}
	index("گوروتین", "# گوروتین\nکانال‌ها و select برای هماهنگی")
	index("نوع‌ها", "# نوع‌ها\nint و string؛ گوروتین اینجا فقط نام برده می‌شود")

	for _, c := range []struct {
		query  string
		want   string // matched titles, in order
		labels string // titles labelled InLesson
	}{
		{"کانال ها", "گوروتین", "گوروتین"},      // ZWNJ in the lesson, space in the query
		{"SELECT", "گوروتین", "گوروتین"},        // case-insensitive
		{"گوروتین", "نوع‌ها|گوروتین", "نوع‌ها"}, // title match is not labelled
		{"if", "", ""},             // short query scans the text: no lesson has "if"
		{"in", "نوع‌ها", "نوع‌ها"}, // short query (< 3 runes) still finds "int"
		{`"x`, "", ""},             // a quote cannot break the MATCH expression
	} {
		tree, _, _ := list(t, s, topics.Filter{Query: c.query})
		var labelled []string
		var walk func([]*topics.Node)
		walk = func(ns []*topics.Node) {
			for _, n := range ns {
				if n.InLesson {
					labelled = append(labelled, n.Title)
				}
				walk(n.Items)
			}
		}
		walk(tree)
		if got := strings.Join(titles(tree), "|"); got != c.want {
			t.Errorf("%q: matched %q, want %q", c.query, got, c.want)
		}
		if got := strings.Join(labelled, "|"); got != c.labels {
			t.Errorf("%q: labelled %q, want %q", c.query, got, c.labels)
		}
	}
}

func TestResume(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	if r, err := s.Resume(ctx); err != nil || r != nil {
		t.Fatalf("no topic opened: want nil, got %+v %v", r, err)
	}
	var a, b int64
	if err := db.Writer().QueryRow(`SELECT MIN(id), MAX(id) FROM topics`).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`UPDATE topics SET status = 'evaluated' WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{b, a} { // a is opened last
		if err := s.Touch(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Resume(ctx)
	if err != nil || r == nil || r.ID != a || r.Status != "evaluated" || r.Counts["evaluated"] != 1 || r.Counts["created"] != 2 {
		t.Fatalf("want topic %d with counts, got %+v %v", a, r, err)
	}
	if err := s.SetArchived(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Resume(ctx); err != nil || r == nil || r.ID != b {
		t.Fatalf("archived topic must be skipped: want %d, got %+v %v", b, r, err)
	}
}

// T10.5: page reads use the read-only pool, so an open write transaction does not block them.
func TestListDoesNotWaitForWriter(t *testing.T) {
	s, db := setup(t, md)
	tx, err := db.Writer().BeginTx(context.Background(), nil) // holds the single writer connection
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE topics SET title = 'دیده‌نشده'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tree, _, total, err := s.List(ctx, topics.Filter{})
	if err != nil || total != 3 {
		t.Fatalf("List during an open write: total=%d err=%v", total, err)
	}
	if got := strings.Join(titles(tree), ","); strings.Contains(got, "دیده‌نشده") {
		t.Errorf("uncommitted write must not be visible: %s", got)
	}
	if _, err := s.Panel(ctx, tree[0].Items[0].Items[0].ID); err != nil {
		t.Errorf("Panel during an open write: %v", err)
	}
}
