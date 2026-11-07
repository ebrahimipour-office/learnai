package rebuild_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/schema"
	"learninghub/internal/services/rebuild"
	"learninghub/internal/store"
)

const roadmapMD = "# Go\n\n## Basics\n\n### Variables\n\n### HTTP\n\n### Channels\n"

func keyOf(t *testing.T, title string) string {
	sel, err := roadmap.Select(roadmap.Parse([]byte(roadmapMD)), roadmap.Rule{Kind: roadmap.RuleDeepest})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range sel.Topics {
		if x.Title == title {
			return x.StableKey
		}
	}
	t.Fatalf("no topic %q", title)
	return ""
}

func snapshot(t *testing.T, dir string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), "app.db") {
			b, _ := os.ReadFile(p)
			out[p] = fsx.HashBytes(b)
		}
		return nil
	})
	return out
}

func TestRebuildFromFiles(t *testing.T) {
	base := t.TempDir()
	key := keyOf(t, "HTTP")
	q02 := string(fsx.GenerateAnswerTemplate(key, 1, "q02", "Two", ".md", ""))
	q01 := string(fsx.GenerateAnswerTemplate(key, 1, "q01", "One", ".go", "package main\n")) + "func main() {}\n"
	manifest := fmt.Sprintf(`{"schema_version":1,"topic":{"stable_key":%q,"title":"HTTP"},
		"set":{"version":1,"rubric_version":1,"created_at":"2026-11-01T10:00:00Z","generator":{"adapter":"agy","model":"m1","skill":"exercises","skill_version":"1"}},
		"exercises":[
		 {"id":"q01","order":1,"title":"One","type":"coding","difficulty":"easy","weight":1,"answer":{"slug":"one","ext":".go","file_name":"q01-one.go"}},
		 {"id":"q02","order":2,"title":"Two","type":"conceptual","difficulty":"medium","weight":3,"answer":{"slug":"two","ext":".md","file_name":"q02-two.md"},
		  "external_knowledge":[{"topic":"TLS","note":"n"}]}]}`, key)
	result := fmt.Sprintf(`{"schema_version":1,"topic":{"stable_key":%q,"title":"HTTP"},"set_version":1,"attempt_no":1,"mode":"full",
		"rubric_version":1,"started_at":"2026-11-02T10:00:00Z","finished_at":"2026-11-02T10:05:00Z",
		"evaluator":{"adapter":"agy","model":"m1","skill":"evaluate","skill_version":"2"},
		"items":[
		 {"exercise_id":"q01","weight":1,"status":"evaluated","score":80,"answer_sha256":%q,"report_file":"q01-report.md",
		  "criteria_scores":[{"criterion_id":"c1","awarded":8,"max":10}],"error_types":["style"]},
		 {"exercise_id":"q02","weight":3,"status":"unanswered","score":0}],
		"aggregate":{"weighted_score":20,"answered_count":1,"total_count":2,"partial":true,
		 "formula":"sum(weight_i*score_i)/sum(weight_i); unanswered=0","computed_by":"app"}}`, key, fsx.HashString(q01))
	if err := schema.ValidateManifest([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := schema.ValidateResult([]byte(result)); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"roadmaps/go/rev-001.md":                            roadmapMD,
		"topics/02-http/lesson/lesson.md":                   "# HTTP v2\n",
		"topics/02-http/lesson/history/lesson.0001.md":      "# HTTP v1\n",
		"topics/02-http/exercises/v1/exercises.md":          "## [q01] One\n\n## [q02] Two\n",
		"topics/02-http/exercises/v1/manifest.json":         manifest,
		"topics/02-http/.internal/rubric-v1.json":           "{}",
		"topics/02-http/answers/v1/q01-one.go":              q01,
		"topics/02-http/answers/v1/q02-two.md":              q02,
		"topics/02-http/reports/v1/attempt-1/result.json":   result,
		"topics/02-http/reports/v1/attempt-1/summary.md":    "summary\n",
		"topics/02-http/reports/v1/attempt-1/q01-report.md": "report\n",
		"topics/01-variables/lesson/lesson.md":              "# Variables\n",
		"topics/07-orphan/lesson/lesson.md":                 "# Orphan\n",
	}
	for rel, body := range files {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, base)

	db, err := store.Open(filepath.Join(base, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx := context.Background()

	rep, err := rebuild.Run(ctx, db, root)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Roadmaps != 1 || rep.Topics != 4 || rep.Lessons != 4 || rep.Sets != 1 || rep.Attempts != 1 || rep.Files != 12 || len(rep.Warnings) != 1 {
		t.Errorf("report = %+v", rep)
	}

	// Files are read only.
	after := snapshot(t, base)
	if len(after) != len(before) {
		t.Errorf("file set changed: %d -> %d", len(before), len(after))
	}
	for p, h := range before {
		if after[p] != h {
			t.Errorf("%s changed", p)
		}
	}

	want := map[string]string{
		"01-variables": "roadmap active content_ready",
		"02-http":      "roadmap active evaluated",
		"07-orphan":    "suggested active content_ready",
		"08-channels":  "roadmap active created", // no folder yet: numbered after the highest existing one
	}
	rows, err := db.Writer().Query(`SELECT dir_name, origin, state, status, stable_key FROM topics`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var dir, origin, state, st, k string
		rows.Scan(&dir, &origin, &state, &st, &k)
		got[dir] = origin + " " + state + " " + st
		if dir == "02-http" && k != key {
			t.Errorf("http stable_key = %s", k)
		}
	}
	rows.Close()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("topics = %v, want %v", got, want)
	}

	// Scores and attempts match result.json.
	var score float64
	var answered, total, partial int
	var mode string
	db.Writer().QueryRow(`SELECT topic_score, answered_count, total_count, is_partial, mode FROM attempts WHERE attempt_no = 1`).
		Scan(&score, &answered, &total, &partial, &mode)
	if score != 20 || answered != 1 || total != 2 || partial != 1 || mode != "full" {
		t.Errorf("attempt = %v %d %d %d %s", score, answered, total, partial, mode)
	}
	var items, reports int
	var q01Score int
	var et string
	db.Writer().QueryRow(`SELECT COUNT(*), COUNT(report_file) FROM attempt_items`).Scan(&items, &reports)
	db.Writer().QueryRow(`SELECT i.score, i.error_types FROM attempt_items i JOIN exercises x ON x.id = i.exercise_id WHERE x.ex_id = 'q01'`).
		Scan(&q01Score, &et)
	if items != 2 || reports != 1 || q01Score != 80 || et != `["style"]` {
		t.Errorf("items %d reports %d q01 %d %s", items, reports, q01Score, et)
	}
	var cur, hist int
	db.Writer().QueryRow(`SELECT version FROM lesson_versions l JOIN topics t ON t.id = l.topic_id WHERE t.dir_name = '02-http' AND is_current = 1`).Scan(&cur)
	db.Writer().QueryRow(`SELECT COUNT(*) FROM lesson_versions WHERE is_current = 0`).Scan(&hist)
	if cur != 2 || hist != 1 {
		t.Errorf("lesson current v%d, history %d", cur, hist)
	}
	var orphans int
	db.Writer().QueryRow(`SELECT COUNT(*) FROM files WHERE owner_id IS NULL OR presence != 'present'`).Scan(&orphans)
	if orphans != 0 {
		t.Errorf("%d files without owner or not present", orphans)
	}

	if _, err := rebuild.Run(ctx, db, root); !errors.Is(err, rebuild.ErrNotEmpty) {
		t.Errorf("second run: %v, want ErrNotEmpty", err)
	}
}
