package roadmaps_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/roadmap"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/store"
)

const sample = "# Go\n## Basics\n### Types\n### Loops\n## Concurrency\n### Goroutines\n"

func newService(t *testing.T) (*roadmaps.Service, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return roadmaps.NewService(db), db
}

func count(t *testing.T, db *store.DB, table string) int {
	t.Helper()
	var n int
	if err := db.Writer().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateDraftValidates(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		file    string
		content []byte
		want    error
	}{
		{"too large", "a.md", []byte(strings.Repeat("a", roadmaps.MaxSourceBytes+1)), roadmaps.ErrTooLarge},
		{"wrong extension", "a.txt", []byte("# x"), roadmaps.ErrInvalidSource},
		{"empty", "a.md", []byte(" \n\t"), roadmaps.ErrInvalidSource},
		{"bad utf8", "a.md", []byte{'#', ' ', 0xff, 0xfe}, roadmaps.ErrInvalidSource},
	}
	for _, c := range cases {
		if _, err := s.CreateDraft(ctx, c.file, c.content); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
	}
	if _, err := s.CreateDraft(ctx, `C:\x\road.MD`, []byte(strings.Repeat("a", roadmaps.MaxSourceBytes))); err != nil {
		t.Errorf("exactly 1 MB with Windows path: %v", err)
	}
}

func TestDraftLifecycleCreatesNoRecords(t *testing.T) {
	s, db := newService(t)
	ctx := context.Background()

	id, err := s.CreateDraft(ctx, "go.md", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.SourceName != "go.md" || d.Choice.Rule.Kind != roadmap.RuleDeepest {
		t.Fatalf("unexpected draft: %+v", d)
	}
	sel, err := d.Preview(d.Choice)
	if err != nil || len(sel.Topics) != 3 {
		t.Fatalf("default preview: want 3 topics, got %v (%v)", sel, err)
	}

	c := roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleLevel, Level: 2}, Excluded: []string{"k"}}
	if err := s.SetChoice(ctx, d, c); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.Draft(ctx, id); d.Choice.Rule.Level != 2 || len(d.Choice.Excluded) != 1 {
		t.Errorf("choice not persisted: %+v", d.Choice)
	}
	if err := s.SetChoice(ctx, d, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleLevel, Level: 9}}); !errors.Is(err, roadmaps.ErrInvalidSource) {
		t.Errorf("level 9: want ErrInvalidSource, got %v", err)
	}

	// R2.2: nothing outside import_drafts is written before confirmation.
	for _, table := range []string{"roadmaps", "roadmap_revisions", "groups", "topics"} {
		if n := count(t, db, table); n != 0 {
			t.Errorf("%s: want 0 rows before confirm, got %d", table, n)
		}
	}
}

func TestDraftsExpireAfter24h(t *testing.T) {
	s, db := newService(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	id, err := s.CreateDraft(ctx, "go.md", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(roadmaps.DraftTTL - time.Minute)
	if _, err := s.Draft(ctx, id); err != nil {
		t.Fatalf("before expiry: %v", err)
	}

	now = now.Add(2 * time.Minute)
	if _, err := s.Draft(ctx, id); !errors.Is(err, roadmaps.ErrDraftNotFound) {
		t.Errorf("after expiry: want ErrDraftNotFound, got %v", err)
	}
	if n, err := s.PurgeExpired(ctx); err != nil || n != 1 {
		t.Errorf("purge: want 1 row, got %d (%v)", n, err)
	}
	if n := count(t, db, "import_drafts"); n != 0 {
		t.Errorf("import_drafts: want 0 after purge, got %d", n)
	}
}
