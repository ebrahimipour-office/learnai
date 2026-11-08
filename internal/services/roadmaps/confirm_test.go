package roadmaps_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/services/roadmaps"
)

var recordTables = []string{"roadmaps", "roadmap_revisions", "groups", "topics"}

func TestConfirmRecordsKeptTopics(t *testing.T) {
	base := t.TempDir()
	s, db, root := newServiceIn(t, base)
	ctx := context.Background()

	id, err := s.CreateDraft(ctx, "go.md", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := s.Draft(ctx, id)
	sel, _ := d.Preview(d.Choice)
	// Exclude Goroutines, the only topic of Concurrency: that group must not be recorded.
	goroutines := sel.Topics[2].StableKey
	if err := s.SetChoice(ctx, d, roadmaps.Choice{Rule: d.Choice.Rule, Excluded: []string{goroutines}}); err != nil {
		t.Fatal(err)
	}

	rid, n, err := s.Confirm(ctx, id)
	if err != nil || n != 2 {
		t.Fatalf("confirm: want 2 topics, got %d (%v)", n, err)
	}
	// R11.2-1: one topic row per confirmed topic; the only group is Basics (the sole H1 "Go" is the title).
	for table, want := range map[string]int{"roadmaps": 1, "roadmap_revisions": 1, "groups": 1, "topics": 2, "import_drafts": 0} {
		if got := count(t, db, table); got != want {
			t.Errorf("%s: want %d rows, got %d", table, want, got)
		}
	}

	var slug, rule, relPath, sha string
	if err := db.Writer().QueryRow(`SELECT r.slug, r.select_rule, v.rel_path, v.sha256 FROM roadmaps r
		JOIN roadmap_revisions v ON v.roadmap_id = r.id WHERE r.id = ?`, rid).Scan(&slug, &rule, &relPath, &sha); err != nil {
		t.Fatal(err)
	}
	if slug != "go" || relPath != "roadmaps/go/rev-001.md" || rule != `{"kind":"deepest"}` {
		t.Errorf("roadmap row: slug=%q rel=%q rule=%s", slug, relPath, rule)
	}
	got, err := root.ReadFile(relPath)
	if err != nil || string(got) != sample || sha != fsx.HashBytes(got) {
		t.Errorf("rev-001.md must be the byte-exact upload with matching sha: %q (%v)", got, err)
	}

	rows, err := db.Writer().Query(`SELECT t.title, t.dir_name, t.stable_key, g.title, t.source_rev_id IS NOT NULL
		FROM topics t JOIN groups g ON g.id = t.group_id ORDER BY t.ord`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][2]string{{"Types", "01-types"}, {"Loops", "02-loops"}}
	i := 0
	for rows.Next() {
		var title, dir, key, group string
		var hasRev bool
		if err := rows.Scan(&title, &dir, &key, &group, &hasRev); err != nil {
			t.Fatal(err)
		}
		if title != want[i][0] || dir != want[i][1] || key != sel.Topics[i].StableKey || group != "Basics" || !hasRev {
			t.Errorf("topic %d: %s %s %s %s rev=%v", i, title, dir, key, group, hasRev)
		}
		i++
	}
	// Import puts only each chapter's text in its topic folder (§12.6); lessons come on first generation.
	if ents, _ := os.ReadDir(filepath.Join(base, "topics", "01-types")); len(ents) != 1 || ents[0].Name() != "source.md" {
		t.Errorf("topics/01-types after import: %v", ents)
	}

	// A second import of the same name gets fresh slug and dir numbers.
	id2, _ := s.CreateDraft(ctx, "go.md", []byte(sample))
	if _, _, err := s.Confirm(ctx, id2); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	r2, _ := db.Writer().Query(`SELECT dir_name FROM topics ORDER BY id`)
	for r2.Next() {
		var d string
		_ = r2.Scan(&d)
		dirs = append(dirs, d)
	}
	r2.Close()
	if len(dirs) != 5 || dirs[4] != "05-goroutines" || !root.Exists("roadmaps/go-2/rev-001.md") {
		t.Errorf("second import: dirs=%v", dirs)
	}
}

func TestConfirmIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	t.Run("db failure mid-way", func(t *testing.T) {
		base := t.TempDir()
		s, db, _ := newServiceIn(t, base)
		id, _ := s.CreateDraft(ctx, "go.md", []byte(sample))
		// Fails on the second topic, after the roadmap, revision, groups and first topic are inserted.
		if _, err := db.Writer().Exec(`CREATE TRIGGER boom BEFORE INSERT ON topics WHEN NEW.title = 'Loops'
			BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Confirm(ctx, id); err == nil {
			t.Fatal("want error")
		}
		for _, table := range recordTables {
			if n := count(t, db, table); n != 0 {
				t.Errorf("%s: want 0 rows after failure, got %d", table, n)
			}
		}
		for _, dir := range []string{"roadmaps", "topics"} {
			if _, err := os.Stat(filepath.Join(base, dir)); !os.IsNotExist(err) {
				t.Errorf("%s/ must not exist after failure: %v", dir, err)
			}
		}
		if _, err := s.Draft(ctx, id); err != nil {
			t.Errorf("draft must survive a failed confirm: %v", err)
		}
	})

	t.Run("file write failure", func(t *testing.T) {
		base := t.TempDir()
		s, db, _ := newServiceIn(t, base)
		id, _ := s.CreateDraft(ctx, "go.md", []byte(sample))
		// A plain file named "roadmaps" makes creating roadmaps/go/ fail.
		if err := os.WriteFile(filepath.Join(base, "roadmaps"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Confirm(ctx, id); err == nil {
			t.Fatal("want error")
		}
		for _, table := range recordTables {
			if n := count(t, db, table); n != 0 {
				t.Errorf("%s: want 0 rows after failure, got %d", table, n)
			}
		}
	})
}

func TestConfirmRejects(t *testing.T) {
	s, db := newService(t)
	ctx := context.Background()

	id, _ := s.CreateDraft(ctx, "list.md", []byte("- a\n- b\n"))
	if _, _, err := s.Confirm(ctx, id); !errors.Is(err, roadmaps.ErrNothingSelected) {
		t.Errorf("zero topics: want ErrNothingSelected, got %v", err)
	}

	id, _ = s.CreateDraft(ctx, "go.md", []byte(sample))
	if _, _, err := s.Confirm(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Confirm(ctx, id); !errors.Is(err, roadmaps.ErrDraftNotFound) {
		t.Errorf("second confirm: want ErrDraftNotFound, got %v", err)
	}
	if n := count(t, db, "roadmaps"); n != 1 {
		t.Errorf("double confirm imported %d roadmaps", n)
	}
}

// T7.2 acceptance: the roadmap title is the first H1, else the file name; a preview edit is kept.
func TestConfirmRoadmapTitle(t *testing.T) {
	ctx := context.Background()
	title := func(s *roadmaps.Service, db interface{ Writer() *sql.DB }, rid int64) string {
		t.Helper()
		var got string
		if err := db.Writer().QueryRow(`SELECT title FROM roadmaps WHERE id = ?`, rid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	confirm := func(s *roadmaps.Service, id string) int64 {
		t.Helper()
		rid, _, err := s.Confirm(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return rid
	}

	s, db := newService(t)
	id, _ := s.CreateDraft(ctx, "book.md", []byte("# عنوان کتاب\n## فصل ۱\n"))
	if d, _ := s.Draft(ctx, id); d.DefaultTitle() != "عنوان کتاب" || d.Title != "" {
		t.Fatalf("default %q, stored %q", d.DefaultTitle(), d.Title)
	}
	if got := title(s, db, confirm(s, id)); got != "عنوان کتاب" {
		t.Errorf("H1 title: got %q", got)
	}

	id, _ = s.CreateDraft(ctx, "notes.md", []byte("## a\n## b\n"))
	if got := title(s, db, confirm(s, id)); got != "notes" {
		t.Errorf("no H1: got %q, want file name", got)
	}

	id, _ = s.CreateDraft(ctx, "x.md", []byte("# Old\n## a\n"))
	d, _ := s.Draft(ctx, id)
	if err := s.SetTitle(ctx, d, "  عنوان من  "); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle(ctx, d, strings.Repeat("ا", roadmaps.MaxTitleRunes+1)); !errors.Is(err, roadmaps.ErrInvalidSource) {
		t.Fatalf("long title: %v", err)
	}
	if d, _ := s.Draft(ctx, id); d.Title != "عنوان من" {
		t.Fatalf("stored title %q", d.Title)
	}
	rid := confirm(s, id)
	if got := title(s, db, rid); got != "عنوان من" {
		t.Errorf("edited title: got %q", got)
	}

	// re-import keeps the current title unless the user chose a replacement
	id, _ = s.CreateReimport(ctx, rid, "x.md", []byte("# New\n## a\n"))
	confirm(s, id)
	if got := title(s, db, rid); got != "عنوان من" {
		t.Errorf("re-import without replace: got %q", got)
	}
	id, _ = s.CreateReimport(ctx, rid, "x.md", []byte("# New\n## a\n"))
	d, _ = s.Draft(ctx, id)
	if err := s.SetTitle(ctx, d, d.DefaultTitle()); err != nil {
		t.Fatal(err)
	}
	confirm(s, id)
	if got := title(s, db, rid); got != "New" {
		t.Errorf("re-import with replace: got %q", got)
	}
}
