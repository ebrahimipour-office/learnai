package exercises_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
	"learninghub/internal/config"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/schema"
	"learninghub/internal/services/exercises"
	"learninghub/internal/store"
	"learninghub/skills"
)

const key = "a3f9c2d41b7e8f06"

type env struct {
	t      *testing.T
	db     *store.DB
	root   *fsx.Root
	runner *jobs.Runner
	ad     *fake.Adapter
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := fsx.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	if _, err := skills.Install(root); err != nil {
		t.Fatal(err)
	}
	if err := root.MkdirAll("topics/02-http/lesson", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteNew("topics/02-http/lesson/lesson.md", []byte("# HTTP\n\nlesson body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'go', 'Go', '{}')`,
		`INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name, status) VALUES
			(2, 1, '` + key + `', 'HTTP', 'http', 2, 'http', '02-http', 'content_ready'),
			(3, 1, 'bbbbbbbbbbbbbbbb', 'Routing', 'routing', 3, 'routing', '03-routing', 'created')`,
		`INSERT INTO files (id, topic_id, kind, owner_type, owner_id, rel_path, sha256, size_bytes) VALUES
			(1, 2, 'lesson', 'lesson_version', 1, 'topics/02-http/lesson/lesson.md', 'x', 1)`,
		`INSERT INTO lesson_versions (id, topic_id, version, file_id, keywords, is_current) VALUES (1, 2, 1, 1, '[]', 1)`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ad := &fake.Adapter{Scenario: fake.OK}
	r := jobs.New(db, 1)
	r.Poll, r.Heartbeat, r.Files = 20*time.Millisecond, time.Hour, root
	svc := &exercises.Service{DB: db, Root: root, Adapter: ad, Model: "m1", Language: "fa", Timeout: time.Minute, Stall: time.Minute,
		Config: config.DefaultConfig(dir).Exercises}
	r.Register(domain.JobKindExercises, svc.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &env{t, db, root, r, ad}
}

func exercise(title, slug, ext string, points ...int) map[string]any {
	var criteria []map[string]any
	for i, p := range points {
		criteria = append(criteria, map[string]any{"id": "c" + string(rune('1'+i)), "description": "d", "max_points": p, "category": "correctness"})
	}
	return map[string]any{
		"title": title, "type": "coding", "difficulty": "easy", "weight": 2,
		"statement_markdown": "Write it.",
		"answer":             map[string]any{"slug": slug, "ext": ext, "language": "go", "starter": "package main"},
		"rubric": map[string]any{
			"reference_answer": map[string]any{"format": "code", "content": "package main"},
			"criteria":         criteria,
		},
	}
}

func output(ex ...map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"exercises": ex,
		"suggestions": []map[string]string{{"kind": "supplementary", "title": "TLS", "reason": "next"}}})
	return b
}

func good() json.RawMessage {
	return output(exercise("Hello handler", "Hello Handler!", "go", 60, 40),
		exercise("Explain context", "explain-context", ".md", 100),
		exercise("Status codes", "status_codes", ".go", 50, 50))
}

func (e *env) run(topicID int64, out json.RawMessage, params string) (status, code string) {
	e.t.Helper()
	e.ad.Output = out
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindExercises,
		Scope: "topic:" + string(rune('0'+topicID)), TopicID: topicID, Params: json.RawMessage(params)})
	if err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var c *string
		if err := e.db.Writer().QueryRow(`SELECT status, error_code FROM jobs WHERE id = ?`, id).Scan(&status, &c); err != nil {
			e.t.Fatal(err)
		}
		if status != "queued" && status != "running" {
			if c != nil {
				code = *c
			}
			return status, code
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("job %d did not finish", id)
	return
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Writer().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) read(rel string) []byte {
	e.t.Helper()
	b, err := e.root.ReadFile(rel)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func TestFirstSet(t *testing.T) {
	e := setup(t)
	if st, code := e.run(2, good(), `{}`); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	md := e.read("topics/02-http/exercises/v1/exercises.md")
	for _, h := range []string{"## [q01] Hello handler\n", "## [q02] Explain context\n", "## [q03] Status codes\n"} {
		if !strings.Contains(string(md), h) {
			t.Errorf("exercises.md lacks %q:\n%s", h, md)
		}
	}
	mb := e.read("topics/02-http/exercises/v1/manifest.json")
	if err := schema.ValidateManifest(mb); err != nil {
		t.Fatal(err)
	}
	var m schema.Manifest
	_ = json.Unmarshal(mb, &m)
	if err := exercises.CheckHeadings(md, &m); err != nil {
		t.Fatal(err)
	}
	if m.Topic.StableKey != key || m.Set.Version != 1 || m.Set.Generator.JobID == 0 || m.Set.Generator.Skill != "exercise-generator" {
		t.Errorf("manifest header %+v", m)
	}
	want := []string{"q01-hello-handler.go", "q02-explain-context.md", "q03-status-codes.go"}
	for i, ex := range m.Exercises {
		if ex.Answer.FileName != want[i] {
			t.Errorf("file_name %d = %s, want %s", i, ex.Answer.FileName, want[i])
		}
		a := e.read("topics/02-http/answers/v1/" + want[i])
		if !strings.Contains(string(a), "@hub:topic="+key+",set=v1,ex="+ex.ID) || !strings.Contains(string(a), fsx.AnswerMarker) {
			t.Errorf("answer %s:\n%s", want[i], a)
		}
	}
	if err := schema.ValidateRubric(e.read("topics/02-http/.internal/rubric-v1.json")); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM exercises x JOIN exercise_sets s ON s.id = x.set_id JOIN files f ON f.id = x.answer_file_id
		WHERE s.topic_id = 2 AND s.is_current = 1 AND f.kind = 'answer' AND f.immutable = 1 AND f.owner_id = x.id`); n != 3 {
		t.Errorf("exercise rows with answer files = %d, want 3", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM files WHERE owner_type = 'exercise_set' AND owner_id = (SELECT id FROM exercise_sets WHERE topic_id = 2)`); n != 3 {
		t.Errorf("set files = %d, want 3", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM topics WHERE id = 2 AND status = 'exercises_ready'`); n != 1 {
		t.Error("topic status not exercises_ready")
	}
	if n := e.count(`SELECT COUNT(*) FROM suggestions WHERE topic_id = 2 AND title = 'TLS'`); n != 1 {
		t.Error("suggestion not stored")
	}
	if calls := e.ad.Calls(); len(calls) != 1 {
		t.Errorf("adapter runs = %d, want 1", len(calls))
	}
}

func TestInvalidThenValidRetriesOnce(t *testing.T) {
	e := setup(t)
	e.ad.Scenario = fake.InvalidThenValid
	if st, code := e.run(2, good(), `{}`); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	calls := e.ad.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1], "rejected by the validator") || strings.Contains(calls[0], "rejected") {
		t.Fatalf("prompts: %q", calls)
	}
}

func TestInvalidTwiceFailsCleanly(t *testing.T) {
	for name, tc := range map[string]struct {
		scenario string
		out      json.RawMessage
	}{
		"invalid_json":    {fake.InvalidJSON, good()},
		"points_not_100":  {fake.OK, output(exercise("A", "a", ".go", 60, 30))},
		"ext_not_allowed": {fake.OK, output(exercise("A", "a", ".exe", 100))},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.ad.Scenario = tc.scenario
			if st, code := e.run(2, tc.out, `{}`); st != "failed" || code != ai.Code(ai.ErrInvalidOutput) {
				t.Fatalf("status %s (%s)", st, code)
			}
			if n := len(e.ad.Calls()); n != 2 {
				t.Errorf("adapter runs = %d, want 2", n)
			}
			if _, err := e.root.Stat("topics/02-http/exercises"); err == nil {
				t.Error("exercises dir created on failure")
			}
			if n := e.count(`SELECT COUNT(*) FROM exercise_sets`); n != 0 {
				t.Errorf("sets = %d", n)
			}
		})
	}
}

func TestNeedsLesson(t *testing.T) {
	e := setup(t)
	if st, _ := e.run(3, good(), `{}`); st != "failed" {
		t.Fatalf("status %s", st)
	}
	if n := len(e.ad.Calls()); n != 0 {
		t.Errorf("adapter ran %d times without a lesson", n)
	}
}

func TestNewVersionKeepsOld(t *testing.T) {
	e := setup(t)
	if st, code := e.run(2, good(), `{}`); st != "succeeded" {
		t.Fatalf("v1: %s (%s)", st, code)
	}
	rows, err := e.db.Writer().Query(`SELECT rel_path FROM files WHERE topic_id = 2`)
	if err != nil {
		t.Fatal(err)
	}
	v1 := map[string]string{} // lesson + every v1 file: exercises.md, manifest, rubric, answers
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			t.Fatal(err)
		}
		v1[rel] = fsx.HashBytes(e.read(rel))
	}
	if err := rows.Close(); err != nil || len(v1) < 6 {
		t.Fatalf("v1 files: %d (%v)", len(v1), err)
	}
	if st, _ := e.run(2, good(), `{}`); st != "failed" {
		t.Fatalf("second set without confirm: %s", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Errorf("audit rows before confirm: %d", n)
	}
	if st, code := e.run(2, good(), `{"confirm_new_version":true}`); st != "succeeded" {
		t.Fatalf("v2: %s (%s)", st, code)
	}
	for rel, h := range v1 {
		if fsx.HashBytes(e.read(rel)) != h {
			t.Errorf("%s changed", rel)
		}
	}
	e.read("topics/02-http/answers/v2/q01-hello-handler.go")
	if n := e.count(`SELECT COUNT(*) FROM exercise_sets WHERE topic_id = 2 AND is_current = 1 AND version = 2`); n != 1 {
		t.Error("v2 not current")
	}
	if n := e.count(`SELECT COUNT(*) FROM exercise_sets WHERE topic_id = 2 AND is_current = 0 AND version = 1`); n != 1 {
		t.Error("v1 row not kept as history")
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log a JOIN exercise_sets s ON s.id = a.target_id
		WHERE a.action = 'new_exercise_set' AND a.target_type = 'exercise_set' AND s.version = 2
		AND json_extract(a.detail, '$.previous_version') = 1`); n != 1 {
		t.Error("no audit_log row for confirmed v2")
	}
}

func TestExistingAnswerRejectsWholeCommit(t *testing.T) {
	e := setup(t)
	if err := e.root.MkdirAll("topics/02-http/answers/v1", 0o755); err != nil {
		t.Fatal(err)
	}
	mine := []byte("my old answer\n")
	if err := e.root.WriteNew("topics/02-http/answers/v1/q02-explain-context.md", mine, 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.run(2, good(), `{}`); st != "failed" {
		t.Fatalf("status %s", st)
	}
	if got := e.read("topics/02-http/answers/v1/q02-explain-context.md"); string(got) != string(mine) {
		t.Error("existing answer was changed")
	}
	for _, rel := range []string{"topics/02-http/answers/v1/q01-hello-handler.go", "topics/02-http/exercises/v1/manifest.json", "topics/02-http/.internal/rubric-v1.json"} {
		if _, err := e.root.Stat(rel); err == nil {
			t.Errorf("%s left behind", rel)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM files WHERE kind != 'lesson'`); n != 0 {
		t.Errorf("file rows = %d", n)
	}
}

func TestBuildRejectsSmuggledHeading(t *testing.T) {
	ex := exercise("A", "a", ".go", 100)
	ex["statement_markdown"] = "text\n\n## [q02] fake\n"
	_, err := exercises.Build(output(ex), exercises.Meta{StableKey: key, TopicTitle: "T", Version: 1, CreatedAt: "2026-11-01T00:00:00Z",
		Generator: schema.ManifestGenerator{Adapter: "fake", Skill: "s", SkillVersion: "1"}, AllowedExts: []string{".go"}, CountMin: 1, CountMax: 5})
	if !errors.Is(err, ai.ErrInvalidOutput) || !strings.Contains(err.Error(), "headings") {
		t.Fatalf("err = %v", err)
	}
}

// Rule 2 of TDD §5.1: each manifest id appears exactly once, in order, as "## [qNN] <title>".
func TestCheckHeadings(t *testing.T) {
	m := &schema.Manifest{Exercises: []schema.ManifestExercise{{ID: "q01", Title: "A b"}, {ID: "q02", Title: "C"}}}
	cases := map[string]string{
		"ok":        "# T\n\n## [q01] A b\n\nx\n\n## [q02] C\n\ny\n",
		"ok crlf":   "# T\r\n\r\n## [q01] A b\r\n\r\nx\r\n\r\n## [q02] C\r\n",
		"missing":   "# T\n\n## [q01] A b\n\nx\n",
		"duplicate": "## [q01] A b\n\n## [q01] A b\n\n## [q02] C\n",
		"swapped":   "## [q02] C\n\n## [q01] A b\n",
		"title":     "## [q01] A\n\n## [q02] C\n",
		"no space":  "## [q01]A b\n\n## [q02] C\n",
	}
	for name, md := range cases {
		err := exercises.CheckHeadings([]byte(md), m)
		if wantOK := strings.HasPrefix(name, "ok"); (err == nil) != wantOK {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// T4.6: an external topic the AI forgot in the rubric is mirrored as do_not_penalize, in one run (no retry).
func TestExternalKnowledgeMirroredInRubric(t *testing.T) {
	e := setup(t)
	ex := exercise("TLS client", "tls", ".go", 100)
	ex["external_knowledge"] = []map[string]string{{"topic": "X.509", "note": "certificate basics"}, {"topic": "DNS", "note": "lookup"}}
	ex["rubric"].(map[string]any)["out_of_scope_knowledge"] = []map[string]string{{"topic": "dns", "handling": "do_not_penalize"}}
	if st, code := e.run(2, output(ex, exercise("B", "b", ".go", 100), exercise("C", "c", ".go", 100)), `{}`); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	if n := len(e.ad.Calls()); n != 1 {
		t.Errorf("adapter runs = %d, want 1", n)
	}
	var r schema.Rubric
	_ = json.Unmarshal(e.read("topics/02-http/.internal/rubric-v1.json"), &r)
	got := r.Exercises[0].OutOfScopeKnowledge
	if len(got) != 2 || got[0].Topic != "dns" || got[1].Topic != "X.509" || got[1].Handling != "do_not_penalize" {
		t.Errorf("out_of_scope_knowledge = %+v", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM exercises WHERE ex_id = 'q01' AND external_deps LIKE '%X.509%'`); n != 1 {
		t.Error("external_deps not stored")
	}
}
