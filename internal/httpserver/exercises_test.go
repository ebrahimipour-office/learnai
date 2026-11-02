package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
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
