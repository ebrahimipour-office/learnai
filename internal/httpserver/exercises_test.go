package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/httpserver"
)

func exOutput() json.RawMessage {
	ex := func(title, slug, ext string, typ string) map[string]any {
		return map[string]any{
			"title": title, "type": typ, "difficulty": "easy", "weight": 1, "statement_markdown": "Write it.",
			"answer": map[string]any{"slug": slug, "ext": ext, "language": "go", "starter": "package main"},
			"rubric": map[string]any{"reference_answer": map[string]any{"format": "code", "content": "SECRET-REF"},
				"criteria": []map[string]any{{"id": "c1", "description": "d", "max_points": 100, "category": "correctness"}}},
		}
	}
	b, _ := json.Marshal(map[string]any{"suggestions": []any{}, "exercises": []any{
		ex("Hello handler", "hello", "go", "coding"), ex("Explain context", "explain", "md", "conceptual"), ex("Status codes", "status", "go", "coding")}})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["exercises"].([]any)[2].(map[string]any)["external_knowledge"] = []map[string]string{{"topic": "RFC 9110", "note": "status semantics"}}
	b, _ = json.Marshal(m)
	return b
}

// exerciseSet gives topic 1 a lesson and an exercise set made through the UI.
func (e *env) exerciseSet() {
	e.t.Helper()
	e.seedTopics()
	if _, err := e.db.Writer().Exec(`UPDATE topics SET stable_key = 'a3f9c2d41b7e8f06' WHERE id = 1`); err != nil {
		e.t.Fatal(err)
	}
	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	e.ad.Output = exOutput()
	rec := e.post("/topics/1/jobs/exercises", url.Values{"tab": {"exercises"}, "count": {"3"}}, true)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), "tab=exercises") {
		e.t.Fatalf("enqueue exercises: %d %s", rec.Code, rec.Body.String())
	}
	id, _ = e.lastJob()
	e.pollUntilDone(id)
	if _, st := e.lastJob(); st != "succeeded" {
		e.t.Fatalf("exercises job %s", st)
	}
}

func (e *env) answerID(exID string) (fileID string, abs string) {
	e.t.Helper()
	var rel string
	if err := e.db.Writer().QueryRow(`SELECT f.id, f.rel_path FROM exercises x JOIN files f ON f.id = x.answer_file_id
		JOIN exercise_sets s ON s.id = x.set_id WHERE s.is_current = 1 AND x.ex_id = ?`, exID).Scan(&fileID, &rel); err != nil {
		e.t.Fatal(err)
	}
	return fileID, filepath.Join(e.base, filepath.FromSlash(rel))
}

// evaluate records an attempt that evaluated exID with the answer hash on disk now.
func (e *env) evaluate(exIDs ...string) {
	e.t.Helper()
	w := e.db.Writer()
	var setID, n int64
	_ = w.QueryRow(`SELECT id FROM exercise_sets WHERE is_current = 1`).Scan(&setID)
	_ = w.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&n)
	res, err := w.Exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, topic_score, answered_count, total_count,
		is_partial, result_file, summary_file, started_at, finished_at) VALUES (?, ?, 'full', 1, 50, 1, 3, 1, 1, 1, 'x', 'x')`, setID, n+1)
	if err != nil {
		e.t.Fatal(err)
	}
	aid, _ := res.LastInsertId()
	for _, x := range exIDs {
		id, abs := e.answerID(x)
		b, _ := os.ReadFile(abs)
		if _, err := w.Exec(`INSERT INTO attempt_items (attempt_id, exercise_id, status, answer_sha256, score)
			SELECT ?, x.id, 'evaluated', ?, 50 FROM exercises x WHERE x.answer_file_id = ?`,
			aid, fsx.HashBytes(b), id); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestExercisesTab(t *testing.T) {
	e := newEnv(t)
	e.exerciseSet()
	page := e.get("/topics/1/exercises").Body.String()
	if strings.Count(page, "دانش خارج از درس") != 1 || !strings.Contains(page, "RFC 9110") {
		t.Error("external knowledge badge missing or on the wrong exercises")
	}
	for _, want := range []string{"Hello handler", "کدنویسی", "مفهومی", "[q01]", `aria-current="page">تمرین‌ها`, "مجموعه جدید"} {
		if !strings.Contains(page, want) {
			t.Errorf("exercises tab missing %q", want)
		}
	}
	for _, leak := range []string{".internal", "rubric", "SECRET-REF"} {
		if strings.Contains(page, leak) {
			t.Errorf("exercises tab leaks %q", leak)
		}
	}
	var count int
	_ = e.db.Writer().QueryRow(`SELECT json_extract(params, '$.count') FROM jobs WHERE kind = 'exercises'`).Scan(&count)
	if count != 3 {
		t.Errorf("job params count = %d, want 3", count)
	}

	// A second set needs confirmation; out-of-range count is rejected before enqueueing.
	if rec := e.post("/topics/1/jobs/exercises", url.Values{"tab": {"exercises"}}, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "نیاز به تأیید") {
		t.Errorf("no confirm: want 409, got %d", rec.Code)
	}
	if rec := e.post("/topics/1/jobs/exercises", url.Values{"confirm_new_version": {"1"}, "count": {"99"}}, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("count 99: want 422, got %d", rec.Code)
	}
	if rec := e.post("/topics/2/jobs/exercises", nil, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ابتدا درس") {
		t.Errorf("no lesson: want 409, got %d", rec.Code)
	}
	var jobsN int
	_ = e.db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = 'exercises'`).Scan(&jobsN)
	if jobsN != 1 {
		t.Errorf("rejected requests must not enqueue; exercises jobs = %d", jobsN)
	}

	// v2 keeps v1 selectable.
	e.ad.Output = exOutput()
	if rec := e.post("/topics/1/jobs/exercises", url.Values{"confirm_new_version": {"1"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed new set: want 303, got %d", rec.Code)
	}
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	if p := e.get("/topics/1/answers?v=1").Body.String(); !strings.Contains(p, "مجموعه قدیمی v1") || !strings.Contains(p, "answers/v1/q01-hello.go") {
		t.Error("?v=1 must show the old set's answer files")
	}
	if p := e.get("/topics/1/answers").Body.String(); !strings.Contains(p, "answers/v2/q01-hello.go") {
		t.Error("default must be the current set v2")
	}
}

func TestAnswersTabStatuses(t *testing.T) {
	e := newEnv(t)
	e.exerciseSet()
	page := e.get("/topics/1/answers").Body.String()
	_, abs1 := e.answerID("q01")
	if !strings.Contains(page, `value="`+abs1+`"`) || !strings.Contains(page, `data-copy="`+abs1+`"`) {
		t.Fatalf("answers tab must offer the full path %s", abs1)
	}
	if strings.Count(page, "بدون پاسخ") != 3 || strings.Contains(page, ".internal") {
		t.Fatal("fresh set: three unanswered files, no .internal paths")
	}

	_, abs2 := e.answerID("q02")
	_, abs3 := e.answerID("q03")
	for _, p := range []string{abs1, abs2} {
		if err := os.WriteFile(p, []byte("my answer"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.evaluate("q01", "q02")
	_ = os.WriteFile(abs2, []byte("edited after evaluation"), 0o644)
	_ = os.Remove(abs3)

	page = e.get("/topics/1/answers").Body.String()
	for _, want := range []string{"status-answered", "status-changed", "status-missing", "تغییرکرده پس از ارزیابی", "گم‌شده"} {
		if !strings.Contains(page, want) {
			t.Errorf("answers tab missing %q", want)
		}
	}
	if strings.Contains(page, "status-unanswered") {
		t.Error("no file is unanswered any more")
	}
}

func TestServeFile(t *testing.T) {
	e := newEnv(t)
	e.exerciseSet()
	goID, _ := e.answerID("q01")
	rec := e.get("/files/" + goID)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") ||
		rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf(".go answer: %d %v", rec.Code, rec.Header())
	}
	mdID, _ := e.answerID("q02")
	if rec := e.get("/files/" + mdID); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf(".md answer: %d %v", rec.Code, rec.Header())
	}

	var rubric string
	_ = e.db.Writer().QueryRow(`SELECT rubric_file FROM exercise_sets WHERE is_current = 1`).Scan(&rubric)
	if rec := e.get("/files/" + rubric); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "SECRET-REF") {
		t.Fatalf("rubric before evaluation: want 403, got %d", rec.Code)
	}
	e.evaluate("q01", "q02")
	if rec := e.get("/files/" + rubric); rec.Code != http.StatusForbidden {
		t.Errorf("rubric with q03 never evaluated: want 403, got %d", rec.Code)
	}
	e.evaluate("q03")
	if rec := e.get("/files/" + rubric); rec.Code != 200 || !strings.Contains(rec.Body.String(), "SECRET-REF") {
		t.Errorf("rubric after every exercise was evaluated: want 200, got %d", rec.Code)
	}

	for _, p := range []string{"/files/999", "/files/abc"} {
		if rec := e.get(p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
	_, abs := e.answerID("q01")
	_ = os.Remove(abs)
	if rec := e.get("/files/" + goID); rec.Code != http.StatusNotFound {
		t.Errorf("deleted file: want 404, got %d", rec.Code)
	}
}

func TestEvaluateEnqueue(t *testing.T) {
	e := newEnv(t)
	e.exerciseSet()
	calls := len(e.ad.Calls())
	evalJobs := func() (n int) {
		_ = e.db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = 'evaluate'`).Scan(&n)
		return
	}

	// Every answer is still its template: nothing to evaluate, no job, no AI call.
	if rec := e.post("/topics/1/jobs/evaluate", nil, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "برای ارزیابی نیست") {
		t.Errorf("templates only: want 422 nothing_to_evaluate, got %d", rec.Code)
	}
	// An oversized answer is not sent and is reported for manual review.
	_, big := e.answerID("q02")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 2048)), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/topics/1/jobs/evaluate", url.Values{"mode": {"full"}}, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "بازبینی دستی") {
		t.Errorf("oversized only: want 422 with manual review, got %d", rec.Code)
	}
	if rec := e.post("/topics/1/jobs/evaluate", url.Values{"mode": {"partial"}}, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad mode: want 422, got %d", rec.Code)
	}
	if rec := e.post("/topics/99/jobs/evaluate", nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown topic: want 404, got %d", rec.Code)
	}
	if n := evalJobs(); n != 0 || len(e.ad.Calls()) != calls {
		t.Fatalf("rejected requests must not enqueue or call the AI; jobs=%d calls=%d", n, len(e.ad.Calls())-calls)
	}

	_, abs := e.answerID("q01")
	if err := os.WriteFile(abs, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/topics/1/jobs/evaluate", nil, true); rec.Code != http.StatusAccepted {
		t.Fatalf("answered: want 202, got %d %s", rec.Code, rec.Body.String())
	}
	var mode string
	_ = e.db.Writer().QueryRow(`SELECT json_extract(params, '$.mode') FROM jobs WHERE kind = 'evaluate'`).Scan(&mode)
	if mode != "incremental" {
		t.Errorf("default mode = %q, want incremental", mode)
	}
	id, _ := e.lastJob()
	e.pollUntilDone(id) // fake output is not an evaluation, so the job fails; it just has to free the scope

	// Evaluated and unchanged: incremental has nothing new, full re-sends it.
	e.evaluate("q01")
	if rec := e.post("/topics/1/jobs/evaluate", nil, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unchanged incremental: want 422, got %d", rec.Code)
	}
	if rec := e.post("/topics/1/jobs/evaluate", url.Values{"mode": {"full"}}, true); rec.Code != http.StatusAccepted {
		t.Errorf("unchanged full: want 202, got %d", rec.Code)
	}
}

// TestReportsTab: what the tab shows equals result.json of the same attempt (T5.7).
func TestReportsTab(t *testing.T) {
	e := newEnv(t)
	e.exerciseSet()
	page := e.get("/topics/1/reports").Body.String()
	if !strings.Contains(page, "هنوز ارزیابی‌ای") || !strings.Contains(page, `action="/topics/1/jobs/evaluate"`) || !strings.Contains(page, `value="full"`) {
		t.Fatal("empty reports tab must show the evaluate form with a mode choice")
	}

	_, abs := e.answerID("q01")
	if err := os.WriteFile(abs, []byte("package main\n\nfunc main() { /* RAW-ANSWER */ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.ad.Output = json.RawMessage(`{"exercise_id":"q01","criteria_scores":[{"criterion_id":"c1","awarded":70,"comment":"ok"}],
		"score":70,"errors":[],"strengths":[],"corrected_answer":{"format":"text","content":"x"},"teaching_notes_markdown":"n"}`)
	if rec := e.post("/topics/1/jobs/evaluate", url.Values{"tab": {"reports"}, "mode": {"full"}}, true); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "tab=reports") {
		t.Fatalf("enqueue evaluate: %d %s", rec.Code, rec.Body.String())
	}
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	if _, st := e.lastJob(); st != "succeeded" {
		t.Fatalf("evaluate job %s", st)
	}

	var rel string
	if err := e.db.Writer().QueryRow(`SELECT f.rel_path FROM attempts a JOIN files f ON f.id = a.result_file`).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(e.base, filepath.FromSlash(rel)))
	var res struct {
		AttemptNo int `json:"attempt_no"`
		Items     []struct {
			ExerciseID string `json:"exercise_id"`
			Score      int    `json:"score"`
		} `json:"items"`
		Aggregate struct {
			WeightedScore float64 `json:"weighted_score"`
			AnsweredCount int     `json:"answered_count"`
			TotalCount    int     `json:"total_count"`
			Partial       bool    `json:"partial"`
		} `json:"aggregate"`
	}
	if err := json.Unmarshal(b, &res); err != nil || len(res.Items) != 3 || !res.Aggregate.Partial {
		t.Fatalf("result.json: %v %s", err, b)
	}

	page = e.get("/topics/1/reports").Body.String()
	a := res.Aggregate
	for _, want := range []string{
		"تلاش " + strconv.Itoa(res.AttemptNo),
		`<td class="score">` + strconv.FormatFloat(a.WeightedScore, 'f', -1, 64) + "</td>",
		"ناقص (" + strconv.Itoa(a.AnsweredCount) + " از " + strconv.Itoa(a.TotalCount) + " پاسخ‌داده‌شده)",
		`aria-current="page">گزارش‌ها`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("reports tab missing %q", want)
		}
	}
	for _, it := range res.Items {
		if want := `data-ex="` + it.ExerciseID + `">` + strconv.Itoa(it.Score) + "<"; !strings.Contains(page, want) {
			t.Errorf("score of %s: want %q in page", it.ExerciseID, want)
		}
	}
	if strings.Contains(page, "RAW-ANSWER") {
		t.Error("report body must show only on the exercise report page")
	}

	// Exercise report page: the committed qNN-report.md with the raw answer.
	rep := e.get("/topics/1/reports?attempt=1&ex=q01").Body.String()
	if !strings.Contains(rep, "RAW-ANSWER") || !strings.Contains(rep, "q01-report.md") {
		t.Error("exercise report page must render q01-report.md")
	}
	if strings.Contains(e.get("/topics/1/reports?attempt=1&ex=q02").Body.String(), `id="report"`) {
		t.Error("unanswered exercise has no report")
	}
}

func TestOpenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("editor stub is a shell script")
	}
	// The stub records its argv; a shell metacharacter in it would show the path went through a shell.
	out := filepath.Join(t.TempDir(), "argv")
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf '%s|' \"$@\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(s *httpserver.Settings) { s.Editor = editor })
	e.exerciseSet()
	if page := e.get("/topics/1/answers").Body.String(); strings.Count(page, "باز کردن") != 3 {
		t.Fatal("answers tab must offer «باز کردن» per file when an editor is configured")
	}
	id, abs := e.answerID("q01")
	if rec := e.post("/files/"+id+"/open", nil, false); rec.Code != http.StatusNoContent {
		t.Fatalf("open: want 204, got %d %s", rec.Code, rec.Body.String())
	}
	var got []byte
	for i := 0; i < 100 && len(got) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		got, _ = os.ReadFile(out)
	}
	if string(got) != abs+"|" {
		t.Fatalf("editor argv = %q, want the absolute path from the DB as one argument", got)
	}

	var rubric string
	_ = e.db.Writer().QueryRow(`SELECT rubric_file FROM exercise_sets WHERE is_current = 1`).Scan(&rubric)
	if rec := e.post("/files/"+rubric+"/open", nil, false); rec.Code != http.StatusForbidden {
		t.Errorf("locked rubric: want 403, got %d", rec.Code)
	}
	_ = os.Remove(abs)
	for _, p := range []string{"/files/" + id + "/open", "/files/999/open", "/files/abc/open"} {
		if rec := e.post(p, nil, false); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}

	plain := newEnv(t)
	plain.exerciseSet()
	if strings.Contains(plain.get("/topics/1/answers").Body.String(), "باز کردن") {
		t.Error("no editor configured: the button must be hidden")
	}
	id, _ = plain.answerID("q01")
	if rec := plain.post("/files/"+id+"/open", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("no editor configured: want 404, got %d", rec.Code)
	}
}
