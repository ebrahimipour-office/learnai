package roadmaps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	// R11.2-1: one topic row per confirmed topic; groups are Go > Basics only.
	for table, want := range map[string]int{"roadmaps": 1, "roadmap_revisions": 1, "groups": 2, "topics": 2, "import_drafts": 0} {
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
	// Topic folders are created on first generation, not on import.
	if _, err := os.Stat(filepath.Join(base, "topics")); !os.IsNotExist(err) {
		t.Errorf("topics/ must not exist after import: %v", err)
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
		if _, err := os.Stat(filepath.Join(base, "roadmaps")); !os.IsNotExist(err) {
			t.Errorf("roadmaps/ must not exist after failure: %v", err)
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
