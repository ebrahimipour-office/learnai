package lessons_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai/fake"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/services/lessons"
	"learninghub/internal/store"
	"learninghub/skills"
)

const (
	lessonRel = "topics/02-http/lesson/lesson.md"
	histRel   = "topics/02-http/lesson/history/lesson.0001.md"
)

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
	for _, q := range []string{
		`INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'go', 'Go', '{}')`,
		`INSERT INTO groups (id, roadmap_id, stable_key, title, level, ord) VALUES (1, 1, 'g', 'Web', 1, 1)`,
		`INSERT INTO topics (id, roadmap_id, group_id, stable_key, title, title_norm, ord, slug, dir_name) VALUES
			(1, 1, 1, 'a', 'Intro', 'intro', 1, 'intro', '01-intro'),
			(2, 1, 1, 'b', 'HTTP', 'http', 2, 'http', '02-http'),
			(3, 1, 1, 'c', 'Routing', 'routing', 3, 'routing', '03-routing')`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ad := &fake.Adapter{Scenario: fake.OK}
	r := jobs.New(db, 1)
	r.Poll, r.Heartbeat, r.Files = 20*time.Millisecond, time.Hour, root
	svc := &lessons.Service{DB: db, Root: root, Adapter: ad, Model: "m1", Language: "fa", Timeout: time.Minute, Stall: time.Minute, MaxSource: 64}
	r.Register(domain.JobKindLesson, svc.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &env{t, db, root, r, ad}
}

func output(text string, keywords ...string) json.RawMessage {
	kw := []map[string]string{}
	for _, k := range keywords {
		kw = append(kw, map[string]string{"term": k, "gloss": "about " + k})
	}
	b, _ := json.Marshal(map[string]any{
		"lesson_markdown": text,
		"keywords":        kw,
		"suggestions": []map[string]string{
			{"kind": "prerequisite", "title": "TCP", "reason": "HTTP runs on it"},
			{"kind": "prerequisite", "title": " tcp ", "reason": "duplicate"},
		},
	})
	return b
}

// run enqueues a lesson job for topic 2 and waits for it to finish.
func (e *env) run(out json.RawMessage, params string) (status, code string) {
	e.t.Helper()
	e.ad.Output = out
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: "topic:2", TopicID: 2, Params: json.RawMessage(params)})
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

func (e *env) read(rel string) string {
	e.t.Helper()
	b, err := e.root.ReadFile(rel)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func TestFirstLesson(t *testing.T) {
	e := setup(t)
	if st, code := e.run(output("# HTTP\n\nbody", "request", "status code"), `{}`); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	md := e.read(lessonRel)
	if !strings.HasPrefix(md, "# HTTP\n\nbody\n") || !strings.HasSuffix(md, "## کلیدواژه‌ها\n\n- **request**: about request\n- **status code**: about status code\n") {
		t.Errorf("lesson.md:\n%s", md)
	}
	var sha, kw string
	var cur int
	if err := e.db.Writer().QueryRow(`SELECT f.sha256, v.keywords, v.is_current FROM lesson_versions v JOIN files f ON f.id = v.file_id
		WHERE v.topic_id = 2 AND v.version = 1 AND f.rel_path = ? AND f.owner_id = v.id`, lessonRel).Scan(&sha, &kw, &cur); err != nil {
		t.Fatal(err)
	}
	if sha != fsx.HashString(md) || cur != 1 || !strings.Contains(kw, "status code") {
		t.Errorf("version row: sha ok=%v current=%d keywords=%s", sha == fsx.HashString(md), cur, kw)
	}
	if n := e.count(`SELECT COUNT(*) FROM suggestions WHERE topic_id = 2 AND status = 'pending'`); n != 1 {
		t.Errorf("%d pending suggestions, want 1 (duplicate dropped)", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM topics WHERE id = 2 AND status = 'content_ready'`); n != 1 {
		t.Error("topic status not content_ready")
	}
	if n := e.count(`SELECT COUNT(*) FROM jobs WHERE skill = 'lesson-generator' AND skill_version = '1.2.0' AND model = 'm1' AND result_ref IS NOT NULL`); n != 1 {
		t.Error("job metadata not recorded")
	}
	if e.root.Exists(".app/work/1") {
		t.Error("work dir left behind")
	}
}

func TestRegenerate(t *testing.T) {
	e := setup(t)
	e.run(output("# v1 lesson", "a"), `{}`)
	v1 := e.read(lessonRel)

	if st, _ := e.run(output("# v2 lesson", "b"), `{}`); st != "failed" {
		t.Errorf("regenerate without confirm: %s", st)
	}
	if e.read(lessonRel) != v1 || e.root.Exists(histRel) {
		t.Fatal("unconfirmed regenerate touched files")
	}

	if st, code := e.run(output("# v2 lesson", "b"), `{"confirm_regenerate":true}`); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	if got := e.read(histRel); fsx.HashString(got) != fsx.HashString(v1) {
		t.Errorf("history copy differs from v1: %q", got)
	}
	v2 := e.read(lessonRel)
	if !strings.HasPrefix(v2, "# v2 lesson") {
		t.Errorf("lesson.md not replaced: %q", v2)
	}
	// v1 now points at its history file; v2 is current and owns the lesson.md row.
	if n := e.count(`SELECT COUNT(*) FROM lesson_versions v JOIN files f ON f.id = v.file_id
		WHERE v.version = 1 AND v.is_current = 0 AND f.rel_path = ? AND f.sha256 = ? AND f.owner_id = v.id`, histRel, fsx.HashString(v1)); n != 1 {
		t.Error("v1 row not moved to history")
	}
	if n := e.count(`SELECT COUNT(*) FROM lesson_versions v JOIN files f ON f.id = v.file_id
		WHERE v.version = 2 AND v.is_current = 1 AND f.rel_path = ? AND f.sha256 = ? AND f.owner_id = v.id`, lessonRel, fsx.HashString(v2)); n != 1 {
		t.Error("v2 row wrong")
	}
	if n := e.count(`SELECT COUNT(*) FROM files`); n != 2 {
		t.Errorf("%d files rows, want 2", n)
	}
}

func TestDBFailureRestoresFiles(t *testing.T) {
	e := setup(t)
	e.run(output("# v1 lesson", "a"), `{}`)
	v1 := e.read(lessonRel)
	if _, err := e.db.Writer().Exec(`CREATE TRIGGER boom BEFORE INSERT ON lesson_versions WHEN NEW.version = 2
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.run(output("# v2 lesson", "b"), `{"confirm_regenerate":true}`); st != "failed" {
		t.Fatalf("status %s", st)
	}
	if e.read(lessonRel) != v1 {
		t.Error("lesson.md not restored")
	}
	if e.root.Exists(histRel) {
		t.Error("history file left behind")
	}
	if n := e.count(`SELECT COUNT(*) FROM lesson_versions WHERE is_current = 1 AND version = 1`); n != 1 {
		t.Error("v1 no longer current")
	}

	// A first lesson whose transaction fails leaves no lesson.md.
	if _, err := e.db.Writer().Exec(`DROP TRIGGER boom; CREATE TRIGGER boom BEFORE INSERT ON lesson_versions WHEN NEW.topic_id = 3
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	e.ad.Output = output("# routing", "r")
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: "topic:3", TopicID: 3, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for e.count(`SELECT COUNT(*) FROM jobs WHERE id = ? AND status = 'failed'`, id) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if e.root.Exists("topics/03-routing/lesson/lesson.md") {
		t.Error("first lesson.md left after failed transaction")
	}
}

func TestInvalidOutput(t *testing.T) {
	e := setup(t)
	e.ad.Scenario = fake.Injection
	if st, code := e.run(nil, `{}`); st != "failed" || code != "invalid_output" {
		t.Errorf("status %s code %s", st, code)
	}
	if e.root.Exists(lessonRel) || e.count(`SELECT COUNT(*) FROM files`) != 0 {
		t.Error("files written for invalid output")
	}
}

func TestTaskInputs(t *testing.T) {
	e := setup(t)
	e.ad.Scenario = fake.Hang // keeps the sandbox around while we look at it
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: "topic:2", TopicID: 2, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	rel := prompt.WorkRel(id) + "/inputs/task.json"
	for !e.root.Exists(rel) {
		time.Sleep(5 * time.Millisecond)
	}
	var task map[string]string
	if err := json.Unmarshal([]byte(e.read(rel)), &task); err != nil {
		t.Fatal(err)
	}
	if task["topic"] != "HTTP" || task["group"] != "Web" || task["previous_topic"] != "Intro" || task["next_topic"] != "Routing" || task["language"] != "fa" {
		t.Errorf("task.json: %v", task)
	}
	e.runner.Cancel(context.Background(), id)
}

// withSource gives topic 2 a chapter source.md the way roadmaps.Confirm records it (TDD §12.6).
func (e *env) withSource(text string) string {
	e.t.Helper()
	rel := "topics/02-http/source.md"
	if err := e.root.MkdirAll("topics/02-http", 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := e.root.WriteNew(rel, []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
	sha := fsx.HashString(text)
	if _, err := e.db.Writer().Exec(`INSERT INTO files (id, topic_id, kind, owner_type, owner_id, rel_path, sha256, size_bytes, observed_sha256)
		VALUES (99, 2, 'source', 'topic', 2, ?, ?, ?, ?)`, rel, sha, len(text), sha); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Writer().Exec(`UPDATE topics SET source_file_id = 99, source_sha256 = ? WHERE id = 2`, sha); err != nil {
		e.t.Fatal(err)
	}
	return rel
}

func TestChapterSourceInput(t *testing.T) {
	e := setup(t)
	e.withSource("## HTTP\n\nchapter text\n")
	// Another chapter's source must never reach topic 2's sandbox.
	if err := e.root.WriteNew("topics/01-intro/source.md", []byte("## Intro\n\nother chapter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Writer().Exec(`INSERT INTO files (id, topic_id, kind, owner_type, owner_id, rel_path, sha256, size_bytes, observed_sha256)
		VALUES (98, 1, 'source', 'topic', 1, 'topics/01-intro/source.md', 'x', 1, 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Writer().Exec(`UPDATE topics SET source_file_id = 98 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	e.ad.Scenario = fake.Hang
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: "topic:2", TopicID: 2, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer e.runner.Cancel(context.Background(), id)
	for len(e.ad.Calls()) == 0 { // Run is called only after the sandbox is complete
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(e.ad.Calls()[0], "inputs/source.md") {
		t.Errorf("prompt does not list inputs/source.md:\n%s", e.ad.Calls()[0])
	}
	if got := e.read(prompt.WorkRel(id) + "/inputs/source.md"); got != "## HTTP\n\nchapter text\n" {
		t.Errorf("inputs/source.md = %q", got)
	}
	if !e.root.Exists(prompt.WorkRel(id) + "/inputs/roadmap_excerpt.md") {
		t.Error("roadmap_excerpt.md dropped")
	}
}

func TestChapterSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name, text, code string
		remove           bool
	}{
		{"exactly at limit", strings.Repeat("a", 64), "", false},
		{"too large", strings.Repeat("a", 65), "source_too_large", false},
		{"missing", "x", "source_missing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			rel := e.withSource(tc.text)
			if tc.remove {
				if err := e.root.Remove(rel); err != nil {
					t.Fatal(err)
				}
			}
			st, code := e.run(output("# HTTP\n\nchapter lesson", "a"), `{}`)
			if tc.code == "" {
				if st != "succeeded" {
					t.Fatalf("status %s (%s)", st, code)
				}
				return
			}
			if st != "failed" || code != tc.code {
				t.Errorf("status %s code %s, want failed %s", st, code, tc.code)
			}
			if len(e.ad.Calls()) != 0 || e.root.Exists(lessonRel) {
				t.Error("AI called or lesson written despite a bad source")
			}
		})
	}
}

// crash makes the next commit die after its file writes and before the DB transaction (CR-03):
// the panic skips the in-process undo, as a killed process would.
func (e *env) crash(out json.RawMessage, params string) {
	e.t.Helper()
	lessons.SetBeforeCommitTx(e.t, func() { panic("crash") })
	if st, _ := e.run(out, params); st != "failed" {
		e.t.Fatalf("crashed job: status %s", st)
	}
	lessons.SetBeforeCommitTx(e.t, func() {})
}

// restart replays commit journals as a new process would on start.
func (e *env) restart() {
	e.t.Helper()
	if _, err := e.runner.Recover(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM jobs WHERE commit_journal IS NOT NULL`); n != 0 {
		e.t.Errorf("%d journals left after recovery", n)
	}
}

func TestCrashBeforeTxFirstLesson(t *testing.T) {
	e := setup(t)
	e.crash(output("# lost lesson", "a"), `{}`)
	if !e.root.Exists(lessonRel) {
		t.Fatal("crash did not leave lesson.md (test setup)")
	}
	e.restart()
	if e.root.Exists(lessonRel) {
		t.Error("orphan lesson.md not removed")
	}
	if st, code := e.run(output("# HTTP lesson", "a"), `{}`); st != "succeeded" {
		t.Fatalf("next job: %s (%s)", st, code)
	}
	if n := e.count(`SELECT COUNT(*) FROM lesson_versions WHERE topic_id = 2 AND version = 1 AND is_current = 1`); n != 1 {
		t.Error("next job did not record v1")
	}
}

func TestCrashBeforeTxRegenerateRestoresLesson(t *testing.T) {
	e := setup(t)
	e.run(output("# v1 lesson", "a"), `{}`)
	v1 := e.read(lessonRel)
	e.crash(output("# lost v2 lesson", "b"), `{"confirm_regenerate":true}`)
	if e.read(lessonRel) == v1 || !e.root.Exists(histRel) {
		t.Fatal("crash did not overwrite lesson.md (test setup)")
	}
	e.restart()
	if e.read(lessonRel) != v1 {
		t.Error("lesson.md not restored byte for byte")
	}
	if e.root.Exists(histRel) {
		t.Error("history copy left behind")
	}
	if st, code := e.run(output("# v2 lesson", "b"), `{"confirm_regenerate":true}`); st != "succeeded" {
		t.Fatalf("next job: %s (%s)", st, code)
	}
	if n := e.count(`SELECT COUNT(*) FROM lesson_versions WHERE version = 2 AND is_current = 1`); n != 1 {
		t.Error("next job did not record v2")
	}
}

func TestCrashRecoveryKeepsModifiedFile(t *testing.T) {
	e := setup(t)
	e.crash(output("# lost lesson", "a"), `{}`)
	if err := e.root.WriteAtomic(lessonRel, []byte("my notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.restart()
	if e.read(lessonRel) != "my notes" {
		t.Error("modified file was touched")
	}
	if n := e.count(`SELECT COUNT(*) FROM jobs WHERE error_message LIKE ?`, "%"+lessonRel+"%"); n != 1 {
		t.Error("kept file not reported on the job")
	}
}

// excerptFor runs a hanging lesson job for the topic and returns its roadmap_excerpt.md.
func (e *env) excerptFor(topic int64) string {
	e.t.Helper()
	e.ad.Scenario = fake.Hang
	n := len(e.ad.Calls())
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: fmt.Sprintf("topic:%d", topic), TopicID: topic, Params: json.RawMessage(`{}`)})
	if err != nil {
		e.t.Fatal(err)
	}
	defer e.runner.Cancel(context.Background(), id)
	for len(e.ad.Calls()) == n { // Run is called only after the sandbox is complete
		time.Sleep(5 * time.Millisecond)
	}
	return e.read(prompt.WorkRel(id) + "/inputs/roadmap_excerpt.md")
}

func TestTreeExcerpt(t *testing.T) {
	e := setup(t)
	if got := e.excerptFor(2); got != "# HTTP\n" { // no parent_id: unchanged
		t.Errorf("flat excerpt = %q", got)
	}
	for _, q := range []string{
		`UPDATE topics SET parent_id = 1 WHERE id IN (2, 3)`,
		`INSERT INTO topics (id, roadmap_id, group_id, stable_key, title, title_norm, ord, slug, dir_name, parent_id, state) VALUES
			(4, 1, 1, 'd', 'Old', 'old', 4, 'old', '04-old', 1, 'archived')`,
	} {
		if _, err := e.db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := e.excerptFor(1), "# Intro\n\n## زیربخش‌ها (هر کدام درس جدا دارند)\n\n- HTTP\n- Routing\n"; got != want {
		t.Errorf("parent excerpt = %q, want %q", got, want)
	}
	if got, want := e.excerptFor(3), "زیربخشِ: Intro\n\n# Routing\n"; got != want {
		t.Errorf("child excerpt = %q, want %q", got, want)
	}
}
