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

// CR-05: confirm is idempotent when orphan revision files, source files, or topic folders exist.
func TestConfirmIdempotentWithLeftoverFiles(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	s, db, root := newServiceIn(t, base)

	// 1. Initial import: creates roadmap "go" with rev-001.md and topics 01-types, 02-loops, 03-goroutines.
	id, err := s.CreateDraft(ctx, "go.md", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	rid, n, err := s.Confirm(ctx, id)
	if err != nil || n != 3 {
		t.Fatalf("initial confirm: %d topics, %v", n, err)
	}

	// Scenario 1: orphan rev-002.md with identical bytes is adopted on re-import.
	reimport1 := "# Go\n## Basics\n### Types\nIntro to types\n### Loops\nIntro to loops\n"
	orphanRel := "roadmaps/go/rev-002.md"
	if err := os.WriteFile(filepath.Join(base, orphanRel), []byte(reimport1), 0o644); err != nil {
		t.Fatal(err)
	}
	id2, err := s.CreateReimport(ctx, rid, "go.md", []byte(reimport1))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Confirm(ctx, id2); err != nil {
		t.Fatalf("confirm with identical orphan rev-002: %v", err)
	}
	// Verify rev_no 2 recorded rev-002.md and file content was not corrupted.
	var revPath string
	if err := db.Writer().QueryRow(`SELECT rel_path FROM roadmap_revisions WHERE roadmap_id = ? AND rev_no = 2`, rid).Scan(&revPath); err != nil {
		t.Fatal(err)
	}
	if revPath != orphanRel {
		t.Errorf("rev 2: got %s, want %s", revPath, orphanRel)
	}
	gotBytes, err := root.ReadFile(orphanRel)
	if err != nil || string(gotBytes) != reimport1 {
		t.Errorf("rev-002 bytes altered or missing: %q, %v", string(gotBytes), err)
	}

	// Scenario 2: orphan rev-003.md with different bytes causes re-import to advance to rev-004.md.
	orphan3Rel := "roadmaps/go/rev-003.md"
	orphan3Content := "orphan different bytes"
	if err := os.WriteFile(filepath.Join(base, orphan3Rel), []byte(orphan3Content), 0o644); err != nil {
		t.Fatal(err)
	}
	reimport2 := "# Go\n## Basics\n### Types\nIntro to types v3\n### Loops\nIntro to loops v3\n"
	id3, err := s.CreateReimport(ctx, rid, "go.md", []byte(reimport2))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Confirm(ctx, id3); err != nil {
		t.Fatalf("confirm with conflicting orphan rev-003: %v", err)
	}
	var rev3Path string
	if err := db.Writer().QueryRow(`SELECT rel_path FROM roadmap_revisions WHERE roadmap_id = ? AND rev_no = 4`, rid).Scan(&rev3Path); err != nil {
		t.Fatalf("expected rev_no 4 after skipping conflicting rev-003: %v", err)
	}
	if rev3Path != "roadmaps/go/rev-004.md" {
		t.Errorf("revPath: got %s, want rev-004.md", rev3Path)
	}
	// Pre-existing rev-003.md must remain byte-identical.
	orphan3Got, err := root.ReadFile(orphan3Rel)
	if err != nil || string(orphan3Got) != orphan3Content {
		t.Errorf("orphan rev-003 altered: got %q, want %q (%v)", string(orphan3Got), orphan3Content, err)
	}
	// rev-004.md contains reimport2.
	rev4Got, err := root.ReadFile(rev3Path)
	if err != nil || string(rev4Got) != reimport2 {
		t.Errorf("rev-004 bytes: got %q, want %q (%v)", string(rev4Got), reimport2, err)
	}

	// Scenario 3: orphan folder topics/04-x/source.md: next import skips 04 and uses a free folder (05-x).
	orphanFolder := filepath.Join(base, "topics", "04-orphan")
	if err := os.MkdirAll(orphanFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	orphanSource := "stray orphan source"
	if err := os.WriteFile(filepath.Join(orphanFolder, "source.md"), []byte(orphanSource), 0o644); err != nil {
		t.Fatal(err)
	}

	id4, err := s.CreateDraft(ctx, "fresh.md", []byte("# Fresh\n## Topics\n### Alpha\nContent of alpha\n"))
	if err != nil {
		t.Fatal(err)
	}
	rid4, _, err := s.Confirm(ctx, id4)
	if err != nil {
		t.Fatalf("confirm fresh draft with orphan topic folder: %v", err)
	}
	var freshDir string
	if err := db.Writer().QueryRow(`SELECT dir_name FROM topics WHERE roadmap_id = ?`, rid4).Scan(&freshDir); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(freshDir, "04-") {
		t.Errorf("expected fresh topic to skip 04-orphan, got %s", freshDir)
	}
	// The pre-existing orphan folder and source.md must remain byte-identical.
	strayGot, err := root.ReadFile("topics/04-orphan/source.md")
	if err != nil || string(strayGot) != orphanSource {
		t.Errorf("orphan source.md altered: got %q, want %q (%v)", string(strayGot), orphanSource, err)
	}
}
