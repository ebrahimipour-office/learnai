package rebuild_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/services/rebuild"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/store"
)

const chaptersMD = "# Go\n\n## Basics\n\n### Types\n\n### Loops\n\n## Concurrency\n\n### Goroutines\n"

// topicRows is every topic as roadmap, key, title, state, ord, group key, source range and folder.
func topicRows(t *testing.T, db *store.DB) (string, string) {
	t.Helper()
	rows, err := db.Writer().Query(`SELECT r.slug, t.stable_key, t.title, t.state, t.ord, COALESCE(g.stable_key, ''),
		COALESCE(t.source_start_line, 0), COALESCE(t.source_end_line, 0), t.dir_name, t.origin
		FROM topics t JOIN roadmaps r ON r.id = t.roadmap_id LEFT JOIN groups g ON g.id = t.group_id ORDER BY t.dir_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug, key, title, state, group, dir, origin string
		var ord float64
		var start, end int
		if err := rows.Scan(&slug, &key, &title, &state, &ord, &group, &start, &end, &dir, &origin); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %s %q %s %v %s %d-%d %s %s", slug, key, title, state, ord, group, start, end, dir, origin))
	}
	var rule string
	db.Writer().QueryRow(`SELECT select_rule FROM roadmaps`).Scan(&rule)
	return strings.Join(out, "\n"), rule
}

// CR-19: a level-rule import with an excluded topic, re-imported with a chapters rule, rebuilds to the same topics.
func TestRebuildKeepsSelectionRule(t *testing.T) {
	base := t.TempDir()
	dbPath := filepath.Join(base, "app.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx := context.Background()
	s := roadmaps.NewService(db, root)

	confirm := func(rid int64, c roadmaps.Choice) {
		t.Helper()
		var id string
		var err error
		if rid == 0 {
			id, err = s.CreateDraft(ctx, "go.md", []byte(chaptersMD))
		} else {
			id, err = s.CreateReimport(ctx, rid, "go.md", []byte(chaptersMD))
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
		if _, _, err := s.Confirm(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	level := roadmap.Rule{Kind: roadmap.RuleLevel, Level: 2}
	sel, err := roadmap.Select(roadmap.Parse([]byte(chaptersMD)), level)
	if err != nil || len(sel.Topics) != 2 {
		t.Fatalf("level 2: %v %v", sel, err)
	}
	confirm(0, roadmaps.Choice{Rule: level, Excluded: []string{sel.Topics[1].StableKey}}) // Concurrency never recorded
	var rid int64
	db.Writer().QueryRow(`SELECT id FROM roadmaps`).Scan(&rid)
	confirm(rid, roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleChapters, Chapters: []roadmap.Chapter{
		{Line: 5, EndLine: 8, Title: "Types and loops"}, {Line: 11, EndLine: 11, Title: "Goroutines"}}}})

	wantTopics, wantRule := topicRows(t, db)
	if !strings.Contains(wantTopics, "archived") || !strings.Contains(wantRule, "chapters") {
		t.Fatalf("setup: %s\n%s", wantTopics, wantRule)
	}
	db.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(dbPath + suffix)
	}

	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rep, err := rebuild.Run(ctx, db, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings: %v", rep.Warnings)
	}
	gotTopics, gotRule := topicRows(t, db)
	if gotTopics != wantTopics || gotRule != wantRule {
		t.Errorf("after rebuild:\n%s\n%s\nwant:\n%s\n%s", gotTopics, gotRule, wantTopics, wantRule)
	}
}
