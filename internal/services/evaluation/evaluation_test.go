package evaluation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/schema"
	"learninghub/internal/services/evaluation"
	"learninghub/internal/store"
	"learninghub/skills"
)

// adapter answers per exercise (read from inputs/task.json) and counts calls.
type adapter struct {
	mu                    sync.Mutex
	calls                 map[string]int
	fail                  map[string]bool
	task, question        map[string]string
	inFlight, maxInFlight int
}

func (a *adapter) Name() string                                   { return "test" }
func (a *adapter) Probe(context.Context) (ai.Capabilities, error) { return ai.Capabilities{}, nil }
func (a *adapter) Run(ctx context.Context, req ai.Request, _ chan<- ai.Event) (*ai.Response, error) {
	task, err := os.ReadFile(filepath.Join(req.WorkDir, "inputs", "task.json"))
	if err != nil {
		return nil, err
	}
	q, _ := os.ReadFile(filepath.Join(req.WorkDir, "inputs", "question.md"))
	var t struct {
		ExerciseID string `json:"exercise_id"`
	}
	json.Unmarshal(task, &t)
	a.mu.Lock()
	a.calls[t.ExerciseID]++
	a.task[t.ExerciseID], a.question[t.ExerciseID] = string(task), string(q)
	a.inFlight++
	a.maxInFlight = max(a.maxInFlight, a.inFlight)
	fail := a.fail[t.ExerciseID]
	a.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	a.mu.Lock()
	a.inFlight--
	a.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("%w: scripted", ai.ErrToolFailed)
	}
	return &ai.Response{Structured: json.RawMessage(fmt.Sprintf(`{"exercise_id":%q,"criteria_scores":[{"criterion_id":"c1","awarded":7,"comment":"ok"}],
		"score":70,"errors":[],"strengths":[],"corrected_answer":{"format":"text","content":"x"},"teaching_notes_markdown":"n"}`, t.ExerciseID))}, nil
}

type env struct {
	t      *testing.T
	dir    string
	db     *store.DB
	root   *fsx.Root
	runner *jobs.Runner
	ad     *adapter
}

const tpl = "// answer below\n"

const manifestJSON = `{"schema_version":1,"topic":{"stable_key":"a3f9c2d41b7e8f06","title":"HTTP"},"set":{"version":1,"rubric_version":1},
	"exercises":[{"id":"q01","weight":1},{"id":"q02","weight":2},{"id":"q03","weight":1}]}`

func setup(t *testing.T, parallel int) *env {
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
	rub := schema.Rubric{SchemaVersion: 1, RubricVersion: 1, SetVersion: 1, TopicStableKey: "a3f9c2d41b7e8f06"}
	for _, id := range []string{"q01", "q02", "q03"} {
		r := schema.RubricExercise{ID: id, ReferenceAnswer: schema.ReferenceAnswer{Format: "text", Content: "ref"},
			Criteria: []schema.Criterion{{ID: "c1", Description: "d", MaxPoints: 10, Category: "correctness"}}}
		if id == "q02" {
			r.OutOfScopeKnowledge = []schema.OutOfScopeKnowledge{{Topic: "TLS handshakes", Handling: "do_not_penalize"}}
		}
		rub.Exercises = append(rub.Exercises, r)
	}
	rubJSON, _ := json.Marshal(rub)
	files := map[string]string{
		"topics/02-http/exercises/v1/exercises.md":  "# Ex\n\n## [q01] One\n\nfirst statement\n\n## [q02] Two\n\nsecond statement\n\n## [q03] Three\n\nthird\n",
		"topics/02-http/exercises/v1/manifest.json": manifestJSON,
		"topics/02-http/.internal/rubric-v1.json":   string(rubJSON),
		"topics/02-http/answers/v1/q01-one.go":      tpl + "answer one\n",
		"topics/02-http/answers/v1/q02-two.md":      tpl + "answer two\n",
		"topics/02-http/answers/v1/q03-three.go":    tpl, // unanswered: never sent
	}
	for rel, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tsha := fsx.HashString(tpl)
	for _, q := range []string{
		`INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'go', 'Go', '{}')`,
		`INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name, status) VALUES
			(2, 1, 'a3f9c2d41b7e8f06', 'HTTP', 'http', 2, 'http', '02-http', 'exercises_ready')`,
		`INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes) VALUES
			(1, 2, 'exercises_md', 'topics/02-http/exercises/v1/exercises.md', 'x', 1),
			(2, 2, 'manifest', 'topics/02-http/exercises/v1/manifest.json', 'x', 1),
			(3, 2, 'rubric', 'topics/02-http/.internal/rubric-v1.json', 'x', 1),
			(4, 2, 'answer', 'topics/02-http/answers/v1/q01-one.go', 'x', 1),
			(5, 2, 'answer', 'topics/02-http/answers/v1/q02-two.md', 'x', 1),
			(6, 2, 'answer', 'topics/02-http/answers/v1/q03-three.go', 'x', 1)`,
		`INSERT INTO exercise_sets (id, topic_id, version, exercises_file, manifest_file, rubric_file, is_current) VALUES (1, 2, 1, 1, 2, 3, 1)`,
		`INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256) VALUES
			(1, 'q01', 1, 'One', 'coding', 'easy', 1, 4, '` + tsha + `'),
			(1, 'q02', 2, 'Two', 'conceptual', 'easy', 1, 5, '` + tsha + `'),
			(1, 'q03', 3, 'Three', 'coding', 'easy', 1, 6, '` + tsha + `')`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ad := &adapter{calls: map[string]int{}, fail: map[string]bool{}, task: map[string]string{}, question: map[string]string{}}
	r := jobs.New(db, 1)
	r.Poll, r.Heartbeat, r.Files = 20*time.Millisecond, time.Hour, root
	svc := &evaluation.Service{DB: db, Root: root, Adapter: ad, Model: "m1", Language: "fa", Timeout: time.Minute, Stall: time.Minute,
		Parallel: parallel, MaxAnswerBytes: 1 << 20}
	r.Register(domain.JobKindEvaluate, svc.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &env{t, dir, db, root, r, ad}
}

func (e *env) enqueue(mode string) int64 {
	e.t.Helper()
	id, err := e.runner.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindEvaluate, Scope: "topic:2", TopicID: 2,
		Params: json.RawMessage(`{"mode":"` + mode + `"}`)})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) wait(id int64) (status, code string) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var c *string
		if err := e.db.Writer().QueryRow(`SELECT status, error_code FROM jobs WHERE id = ?`, id).Scan(&status, &c); err != nil {
			e.t.Fatal(err)
		}
		if status != "queued" && status != "running" {
			if c != nil {
				code = *c
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("job %d did not finish", id)
	return
}

func (e *env) steps(id int64) map[string]string {
	e.t.Helper()
	rows, err := e.db.Writer().Query(`SELECT step_key, status FROM job_steps WHERE job_id = ?`, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, s string
		rows.Scan(&k, &s)
		out[k] = s
	}
	return out
}

func (e *env) calls() map[string]int {
	e.ad.mu.Lock()
	defer e.ad.mu.Unlock()
	out := map[string]int{}
	for k, v := range e.ad.calls {
		out[k] = v
	}
	return out
}

func TestEvaluateOneCallPerExercise(t *testing.T) {
	e := setup(t, 2)
	id := e.enqueue("full")
	if st, code := e.wait(id); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	if c := e.calls(); c["q01"] != 1 || c["q02"] != 1 || c["q03"] != 0 {
		t.Errorf("calls = %v, want q01=1 q02=1 and unanswered q03 never sent", c)
	}
	if s := e.steps(id); len(s) != 2 || s["exercise:q01"] != "done" || s["exercise:q02"] != "done" {
		t.Errorf("steps = %v", s)
	}
	var done, total int
	e.db.Writer().QueryRow(`SELECT progress_done, progress_total FROM jobs WHERE id = ?`, id).Scan(&done, &total)
	if done != 2 || total != 2 {
		t.Errorf("progress %d/%d, want 2/2", done, total)
	}
	if q := e.ad.question["q01"]; !strings.Contains(q, "first statement") || strings.Contains(q, "q02") {
		t.Errorf("question.md for q01 = %q, want only its own section", q)
	}
	if !strings.Contains(e.ad.task["q02"], "TLS handshakes") || !strings.Contains(e.ad.task["q01"], `"out_of_scope_knowledge": []`) {
		t.Errorf("out_of_scope_knowledge not repeated in task.json: %s / %s", e.ad.task["q01"], e.ad.task["q02"])
	}
	if e.ad.maxInFlight != 2 {
		t.Errorf("max in flight = %d, want 2 (parallelism)", e.ad.maxInFlight)
	}
}

func TestEvaluateSequentialByDefault(t *testing.T) {
	e := setup(t, 1)
	if st, _ := e.wait(e.enqueue("full")); st != "succeeded" || e.ad.maxInFlight != 1 {
		t.Fatalf("status %s, max in flight %d", st, e.ad.maxInFlight)
	}
}

func TestEvaluateRetryRunsOnlyUnfinished(t *testing.T) {
	e := setup(t, 1)
	e.ad.fail["q02"] = true
	id := e.enqueue("full")
	if st, code := e.wait(id); st != "failed" || code != "tool_error" {
		t.Fatalf("status %s (%s), want failed/tool_error", st, code)
	}
	if s := e.steps(id); s["exercise:q01"] != "done" || s["exercise:q02"] != "failed" {
		t.Fatalf("steps = %v", s)
	}
	if _, err := e.root.Stat(evaluation.OutputRel(id, "q01")); err != nil {
		t.Fatalf("q01 output not kept in staging: %v", err)
	}

	e.ad.mu.Lock()
	e.ad.fail["q02"] = false
	e.ad.mu.Unlock()
	retry, err := e.runner.Retry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if st, code := e.wait(retry); st != "succeeded" {
		t.Fatalf("retry status %s (%s)", st, code)
	}
	if c := e.calls(); c["q01"] != 1 || c["q02"] != 2 {
		t.Errorf("calls = %v, want q01=1 (inherited) q02=2", c)
	}
	if s := e.steps(retry); s["exercise:q01"] != "done" || s["exercise:q02"] != "done" {
		t.Errorf("retry steps = %v", s)
	}
}

func TestEvaluateRetryDiscardsCheckpointsWhenInputsChange(t *testing.T) {
	e := setup(t, 1)
	e.ad.fail["q02"] = true
	id := e.enqueue("full")
	if st, _ := e.wait(id); st != "failed" {
		t.Fatalf("status %s", st)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "topics/02-http/answers/v1/q01-one.go"), []byte(tpl+"edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ad.mu.Lock()
	e.ad.fail["q02"] = false
	e.ad.mu.Unlock()
	retry, err := e.runner.Retry(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := e.wait(retry); st != "succeeded" {
		t.Fatalf("retry status %s", st)
	}
	if c := e.calls(); c["q01"] != 2 {
		t.Errorf("calls = %v, want q01 re-evaluated after its answer changed", c)
	}
}

func TestCheck(t *testing.T) {
	rub := schema.RubricExercise{ID: "q01", Criteria: []schema.Criterion{{ID: "c1", MaxPoints: 10}, {ID: "c2", MaxPoints: 5}}}
	out := func(id, scores string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"exercise_id":%q,"criteria_scores":[%s],"score":10,"errors":[],"strengths":[],
			"corrected_answer":{"format":"text","content":"x"},"teaching_notes_markdown":"n"}`, id, scores))
	}
	c := func(id string, n int) string {
		return fmt.Sprintf(`{"criterion_id":%q,"awarded":%d,"comment":""}`, id, n)
	}
	for name, tc := range map[string]struct {
		raw json.RawMessage
		ok  bool
	}{
		"valid":          {out("q01", c("c1", 10)+","+c("c2", 0)), true},
		"wrong exercise": {out("q02", c("c1", 1)+","+c("c2", 1)), false},
		"unknown":        {out("q01", c("c1", 1)+","+c("c9", 1)), false},
		"over max":       {out("q01", c("c1", 11)+","+c("c2", 1)), false},
		"duplicate":      {out("q01", c("c1", 1)+","+c("c1", 1)), false},
		"missing":        {out("q01", c("c1", 1)), false},
		"schema":         {json.RawMessage(`{"exercise_id":"q01"}`), false},
	} {
		if err := evaluation.Check(tc.raw, "q01", rub); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func (e *env) read(rel string) []byte {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func TestEvaluateCommitsAttempt(t *testing.T) {
	e := setup(t, 1)
	if st, code := e.wait(e.enqueue("full")); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	dir := evaluation.AttemptRel("02-http", 1, 1)
	var res schema.Result
	b := e.read(dir + "/result.json")
	if err := schema.ValidateResult(b); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &res)
	// q01=7 (w1), q02=7 (w2), q03 unanswered (w1): 21/4
	if a := res.Aggregate; a.WeightedScore != 5.25 || a.AnsweredCount != 2 || !a.Partial || res.AttemptNo != 1 || res.Evaluator.SkillVersion == "" {
		t.Errorf("result = %+v", res)
	}
	got, ok := evaluation.RawAnswer(e.read(dir + "/q01-report.md"))
	if !ok || string(got) != string(e.read("topics/02-http/answers/v1/q01-one.go")) {
		t.Errorf("raw answer in report = %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, dir, "q03-report.md")); err == nil {
		t.Error("report written for an unanswered exercise")
	}
	var score float64
	var partial, mutable, items int
	var status string
	e.db.Writer().QueryRow(`SELECT topic_score, is_partial FROM attempts WHERE set_id = 1 AND attempt_no = 1`).Scan(&score, &partial)
	e.db.Writer().QueryRow(`SELECT COUNT(*) FROM files WHERE kind IN ('report','summary','result') AND immutable = 0`).Scan(&mutable)
	e.db.Writer().QueryRow(`SELECT COUNT(*) FROM attempt_items`).Scan(&items)
	e.db.Writer().QueryRow(`SELECT status FROM topics WHERE id = 2`).Scan(&status)
	if score != 5.25 || partial != 1 || mutable != 0 || items != 3 || status != "evaluated" {
		t.Errorf("db: score %v partial %d mutable %d items %d status %s", score, partial, mutable, items, status)
	}
}

// R11.2-6: re-evaluation makes attempt-(K+1), attempt-K stays byte-identical, unchanged answers carry over.
func TestEvaluateNewAttemptKeepsHistory(t *testing.T) {
	e := setup(t, 1)
	if st, _ := e.wait(e.enqueue("full")); st != "succeeded" {
		t.Fatal(st)
	}
	first := evaluation.AttemptRel("02-http", 1, 1)
	before := map[string]string{}
	for _, f := range []string{"q01-report.md", "q02-report.md", "summary.md", "result.json"} {
		before[f] = fsx.HashBytes(e.read(first + "/" + f))
	}
	if err := os.WriteFile(filepath.Join(e.dir, "topics/02-http/answers/v1/q01-one.go"), []byte(tpl+"better\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, code := e.wait(e.enqueue("incremental")); st != "succeeded" {
		t.Fatalf("status %s (%s)", st, code)
	}
	for f, h := range before {
		if fsx.HashBytes(e.read(first+"/"+f)) != h {
			t.Errorf("attempt-1 %s changed", f)
		}
	}
	var res schema.Result
	json.Unmarshal(e.read(evaluation.AttemptRel("02-http", 1, 2)+"/result.json"), &res)
	st := map[string]string{}
	for _, it := range res.Items {
		st[it.ExerciseID] = it.Status
		if it.ExerciseID == "q02" && (it.CarriedFromAttempt == nil || *it.CarriedFromAttempt != 1 || it.Score != 7) {
			t.Errorf("q02 carried item = %+v", it)
		}
	}
	if st["q01"] != "evaluated" || st["q02"] != "carried_over" || st["q03"] != "unanswered" || res.Aggregate.WeightedScore != 5.25 {
		t.Errorf("attempt 2 = %v, score %v", st, res.Aggregate.WeightedScore)
	}
	if c := e.calls(); c["q01"] != 2 || c["q02"] != 1 {
		t.Errorf("calls = %v, want only the changed q01 sent again", c)
	}
	var carriedFrom int64
	e.db.Writer().QueryRow(`SELECT i.carried_from_attempt FROM attempt_items i JOIN attempts a ON a.id = i.attempt_id
		JOIN exercises x ON x.id = i.exercise_id WHERE a.attempt_no = 2 AND x.ex_id = 'q02'`).Scan(&carriedFrom)
	var firstID int64
	e.db.Writer().QueryRow(`SELECT id FROM attempts WHERE attempt_no = 1`).Scan(&firstID)
	if carriedFrom != firstID {
		t.Errorf("carried_from_attempt = %d, want attempt id %d", carriedFrom, firstID)
	}
}

// A name collision rejects the commit: nothing is overwritten and no attempt row is recorded.
func TestEvaluateCommitCollisionNeverOverwrites(t *testing.T) {
	e := setup(t, 1)
	dir := filepath.Join(e.dir, evaluation.AttemptRel("02-http", 1, 1))
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.wait(e.enqueue("full")); st != "failed" {
		t.Fatalf("status %s, want failed", st)
	}
	if string(e.read(evaluation.AttemptRel("02-http", 1, 1)+"/summary.md")) != "mine" {
		t.Error("existing file overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "q01-report.md")); err == nil {
		t.Error("partial commit left q01-report.md behind")
	}
	var n int
	e.db.Writer().QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&n)
	if n != 0 {
		t.Errorf("%d attempts recorded", n)
	}
}
