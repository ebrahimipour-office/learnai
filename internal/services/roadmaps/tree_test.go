package roadmaps_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/store"
)

// lines:      1      2      3       4          5     6       7     8       9       10
const treeBook = "# Book\nintro\n## One\none intro\n### A\na text\n### B\nb text\n## Two\ntwo text\n"

func confirmWith(t *testing.T, s *roadmaps.Service, rid int64, src string, c roadmaps.Choice) int64 {
	t.Helper()
	ctx := context.Background()
	var id string
	var err error
	if rid == 0 {
		id, err = s.CreateDraft(ctx, "book.md", []byte(src))
	} else {
		id, err = s.CreateReimport(ctx, rid, "book.md", []byte(src))
	}
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetChoice(ctx, d, c); err != nil {
		t.Fatal(err)
	}
	rid, _, err = s.Confirm(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return rid
}

// treeRows is "title parent-title start-end state" per topic, in ord order.
func treeRows(t *testing.T, db *store.DB) string {
	t.Helper()
	rows, err := db.Writer().Query(`SELECT t.title, COALESCE(p.title, '-'), COALESCE(t.source_start_line, 0), COALESCE(t.source_end_line, 0), t.state
		FROM topics t LEFT JOIN topics p ON p.id = t.parent_id ORDER BY t.state, t.ord`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title, parent, state string
		var start, end int
		if err := rows.Scan(&title, &parent, &start, &end, &state); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %s %02d-%02d %s", title, parent, start, end, state))
	}
	return strings.Join(out, "\n")
}

// T8.3 acceptance: N headings ⇒ N records with the right parent and N source.md files with matching hashes.
func TestConfirmTreeRecordsParents(t *testing.T) {
	base := t.TempDir()
	s, db, root := newServiceIn(t, base)
	confirmWith(t, s, 0, treeBook, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleTree}})
	want := "One - 03-04 active\nA One 05-06 active\nB One 07-08 active\nTwo - 09-10 active"
	if got := treeRows(t, db); got != want {
		t.Fatalf("topics:\n%s\nwant:\n%s", got, want)
	}
	rows, err := db.Writer().Query(`SELECT t.source_start_line, t.source_end_line, t.source_sha256, f.rel_path FROM topics t JOIN files f ON f.id = t.source_file_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var start, end int
		var sha, rel string
		if err := rows.Scan(&start, &end, &sha, &rel); err != nil {
			t.Fatal(err)
		}
		n++
		got, err := root.ReadFile(rel)
		if err != nil || string(got) != string(roadmap.Slice([]byte(treeBook), start, end)) || fsx.HashBytes(got) != sha {
			t.Errorf("%s: %q (%v)", rel, got, err)
		}
	}
	if n != 4 {
		t.Fatalf("source files = %d, want 4", n)
	}
}

// An unticked parent is skipped like an unselected heading: its children hang from the next kept topic up.
func TestConfirmTreeExcludedParent(t *testing.T) {
	src := "# Book\n## One\n### Mid\n#### Leaf\n"
	s, db := newService(t)
	sel, _ := roadmap.Select(roadmap.Parse([]byte(src)), roadmap.Rule{Kind: roadmap.RuleTree})
	confirmWith(t, s, 0, src, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleTree}, Excluded: []string{sel.Topics[1].StableKey}})
	if got, want := treeRows(t, db), "One - 02-02 active\nLeaf One 04-04 active"; got != want {
		t.Fatalf("topics:\n%s\nwant:\n%s", got, want)
	}
}

// Re-importing a deepest roadmap with tree deletes nothing: leaves keep their rows and files, parents are
// new, the old groups are archived. A level roadmap re-imported with tree moves the parent's old text
// (now only its intro) to source-history.
func TestReimportToTree(t *testing.T) {
	base := t.TempDir()
	s, db, _ := newServiceIn(t, base)
	tree := roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleTree}}

	rid := confirmWith(t, s, 0, treeBook, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleDeepest}})
	before := fileHashes(t, base)
	var ids string
	db.Writer().QueryRow(`SELECT group_concat(id) FROM (SELECT id FROM topics ORDER BY id)`).Scan(&ids)
	confirmWith(t, s, rid, treeBook, tree)
	var idsAfter string
	db.Writer().QueryRow(`SELECT group_concat(id) FROM (SELECT id FROM topics WHERE id IN (` + ids + `) AND state = 'active' ORDER BY id)`).Scan(&idsAfter)
	if idsAfter != ids {
		t.Errorf("leaf rows %s, after re-import %s", ids, idsAfter)
	}
	after := fileHashes(t, base)
	for rel, h := range before {
		if after[rel] != h {
			t.Errorf("%s changed or removed by re-import", rel)
		}
	}
	if got, want := treeRows(t, db), "One - 03-04 active\nA One 05-06 active\nB One 07-08 active\nTwo - 09-10 active"; got != want {
		t.Errorf("topics:\n%s\nwant:\n%s", got, want)
	}
	var activeGroups int
	db.Writer().QueryRow(`SELECT COUNT(*) FROM groups WHERE state = 'active'`).Scan(&activeGroups)
	if activeGroups != 0 {
		t.Errorf("active groups = %d, want the old ones archived", activeGroups)
	}

	base2 := t.TempDir()
	s2, _, root2 := newServiceIn(t, base2)
	rid2 := confirmWith(t, s2, 0, treeBook, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleLevel, Level: 2}})
	old, _ := root2.ReadFile("topics/01-one/source.md")
	confirmWith(t, s2, rid2, treeBook, tree)
	cur, _ := root2.ReadFile("topics/01-one/source.md")
	hist, err := root2.ReadFile("topics/01-one/source-history/source.0001.md")
	if err != nil || string(hist) != string(old) || string(cur) != "## One\none intro\n" {
		t.Errorf("source.md %q, history %q (%v)", cur, hist, err)
	}
}

// A failure part-way through a tree confirm leaves zero rows and zero files.
func TestConfirmTreeFailureLeavesNothing(t *testing.T) {
	base := t.TempDir()
	s, db, _ := newServiceIn(t, base)
	if err := os.MkdirAll(filepath.Join(base, "topics"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "topics", "03-b"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, _ := s.CreateDraft(ctx, "book.md", []byte(treeBook))
	d, _ := s.Draft(ctx, id)
	if err := s.SetChoice(ctx, d, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleTree}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Confirm(ctx, id); err == nil {
		t.Fatal("want error")
	}
	for _, table := range append(recordTables, "files") {
		if n := count(t, db, table); n != 0 {
			t.Errorf("%s: %d rows after failure", table, n)
		}
	}
	for _, p := range []string{"topics/01-one/source.md", "topics/02-a/source.md", "roadmaps/book/rev-001.md"} {
		if _, err := os.Stat(filepath.Join(base, p)); !os.IsNotExist(err) {
			t.Errorf("%s left behind: %v", p, err)
		}
	}
}
