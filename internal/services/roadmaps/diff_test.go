package roadmaps_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/store"
)

// v2 of sample: Types gains an outline, Loops is renamed, Concurrency is renamed (so Goroutines
// gets a new key) and Channels is added.
const sampleV2 = "# Go\n## Basics\n### Types\n- zero values\n### Loop\n## Parallelism\n### Goroutines\n### Channels\n"

func fileHashes(t *testing.T, base string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"topics", "roadmaps"} {
		err := filepath.WalkDir(filepath.Join(base, dir), func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			out[p] = fsx.HashBytes(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func topicStates(t *testing.T, db *store.DB) map[string]string {
	t.Helper()
	rows, err := db.Writer().Query(`SELECT dir_name, state FROM topics`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var dir, state string
		if err := rows.Scan(&dir, &state); err != nil {
			t.Fatal(err)
		}
		out[dir] = state
	}
	return out
}

func reimport(t *testing.T, s *roadmaps.Service, rid int64, src string) (*roadmaps.Draft, *roadmaps.Diff) {
	t.Helper()
	ctx := context.Background()
	id, err := s.CreateReimport(ctx, rid, "go-v2.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := s.Diff(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	return d, diff
}

func TestReimportDiffArchivesAndKeepsFiles(t *testing.T) {
	base := t.TempDir()
	s, db, root := newServiceIn(t, base)
	ctx := context.Background()

	id, _ := s.CreateDraft(ctx, "go.md", []byte(sample))
	if d, _ := s.Draft(ctx, id); d.RoadmapID != 0 {
		t.Fatal("new import must have no roadmap id")
	} else if diff, err := s.Diff(ctx, d); diff != nil || err != nil {
		t.Fatalf("new import has no diff: %v %v", diff, err)
	}
	rid, _, err := s.Confirm(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Stand-in for generated lesson files: re-import must leave them byte-identical.
	if err := root.WriteNew("topics/02-loops/lesson/lesson.md", []byte("# درس"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeFiles := fileHashes(t, base)
	beforeCounts := map[string]int{}
	for _, table := range recordTables {
		beforeCounts[table] = count(t, db, table)
	}

	d, diff := reimport(t, s, rid, sampleV2)
	type row struct{ Title, OldTitle, Guess string }
	rows := func(items []roadmaps.DiffItem) (out []row) {
		for _, it := range items {
			out = append(out, row{it.Title, it.OldTitle, it.Guess})
		}
		return out
	}
	if got, want := rows(diff.New), []row{{"Loop", "", "Loops"}, {"Goroutines", "", "Goroutines"}, {"Channels", "", ""}}; !reflect.DeepEqual(got, want) {
		t.Errorf("new: got %v, want %v", got, want)
	}
	if got, want := rows(diff.Changed), []row{{"Types", "", ""}}; !reflect.DeepEqual(got, want) {
		t.Errorf("changed: got %v, want %v", got, want)
	}
	if got, want := rows(diff.Gone), []row{{"Loops", "", ""}, {"Goroutines", "", ""}}; !reflect.DeepEqual(got, want) {
		t.Errorf("gone: got %v, want %v", got, want)
	}
	if count(t, db, "roadmap_revisions") != 1 {
		t.Error("the diff preview must not record anything")
	}

	if _, n, err := s.Confirm(ctx, d.ID); err != nil || n != 4 {
		t.Fatalf("confirm re-import: %d %v", n, err)
	}
	// R11.2-1: no record disappears and every existing file is byte-identical.
	for table, before := range beforeCounts {
		if got := count(t, db, table); got < before {
			t.Errorf("%s: %d rows before, %d after", table, before, got)
		}
	}
	after := fileHashes(t, base)
	for p, h := range beforeFiles {
		if filepath.Base(p) == "source.md" && after[p] != h { // a changed chapter keeps its old text (§12.6)
			p = filepath.Join(filepath.Dir(p), "source-history", "source.0001.md")
		}
		if after[p] != h {
			t.Errorf("%s changed or vanished", p)
		}
	}
	if !root.Exists("roadmaps/go/rev-002.md") || count(t, db, "roadmaps") != 1 {
		t.Error("re-import must add rev-002.md to the same roadmap")
	}
	wantStates := map[string]string{"01-types": "active", "02-loops": "archived", "03-goroutines": "archived",
		"04-loop": "active", "05-goroutines": "active", "06-channels": "active"}
	if got := topicStates(t, db); !reflect.DeepEqual(got, wantStates) {
		t.Errorf("topics: got %v, want %v", got, wantStates)
	}
	var outline, groupState string
	_ = db.Writer().QueryRow(`SELECT outline FROM topics WHERE dir_name = '01-types'`).Scan(&outline)
	_ = db.Writer().QueryRow(`SELECT state FROM groups WHERE title = 'Concurrency'`).Scan(&groupState)
	if outline != "- zero values" || groupState != "archived" {
		t.Errorf("Types outline %q, Concurrency group %q", outline, groupState)
	}

	// Importing v1 again restores the archived topics in place and archives the v2-only ones.
	d, diff = reimport(t, s, rid, sample)
	if len(diff.Changed) != 3 || !diff.Changed[1].Restored || !diff.Changed[2].Restored || len(diff.New) != 0 || len(diff.Gone) != 3 {
		t.Errorf("restore diff: %+v", diff)
	}
	if _, _, err := s.Confirm(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	wantStates = map[string]string{"01-types": "active", "02-loops": "active", "03-goroutines": "active",
		"04-loop": "archived", "05-goroutines": "archived", "06-channels": "archived"}
	if got := topicStates(t, db); !reflect.DeepEqual(got, wantStates) {
		t.Errorf("after restore: got %v, want %v", got, wantStates)
	}
}

func TestReimportRejectsUnknownRoadmap(t *testing.T) {
	s, _ := newService(t)
	if _, err := s.CreateReimport(context.Background(), 42, "go.md", []byte(sample)); !errors.Is(err, roadmaps.ErrNoRoadmap) {
		t.Errorf("want ErrNoRoadmap, got %v", err)
	}
}
