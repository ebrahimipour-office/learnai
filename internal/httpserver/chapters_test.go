package httpserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai/fake"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/services/evaluation"
	"learninghub/internal/services/exercises"
)

// TestChaptersRunIndependently is T7.7: two chapters run lesson, exercises and evaluation jobs at the
// same time, and each job's sandbox holds only its own chapter's source.md (TDD §12.8).
func TestChaptersRunIndependently(t *testing.T) {
	e := newEnv(t)
	csrf, w := e.sm.CSRFToken(), e.db.Writer()
	draft := e.upload("book.md", []byte("# Book\n\n## One\n\ntext of chapter one\n\n## Two\n\ntext of chapter two\n")).Header().Get("Location")
	keys := extractValues(e.do(httptest.NewRequest(http.MethodGet, draft, nil)).Body.String(), `name="include" value="`)
	e.selection(draft, url.Values{"rule": {"deepest"}, "shown_rule": {"deepest"}, "include": keys}, true)
	if rec := e.confirm(draft, url.Values{"csrf_token": {csrf}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	type chapter struct {
		id        int64
		dir, text string
	}
	chs := []chapter{{text: "text of chapter one"}, {text: "text of chapter two"}}
	for i, title := range []string{"One", "Two"} {
		if err := w.QueryRow(`SELECT id, dir_name FROM topics WHERE title = ? AND source_file_id IS NOT NULL`, title).Scan(&chs[i].id, &chs[i].dir); err != nil {
			t.Fatalf("chapter %s: %v", title, err)
		}
	}

	// both enqueues the job on both chapters while the AI hangs, then checks each sandbox.
	both := func(kind string, form url.Values, step string) {
		t.Helper()
		e.ad.Scenario = fake.Hang
		calls := len(e.ad.Calls())
		var ids []int64
		for _, c := range chs {
			if rec := e.post(fmt.Sprintf("/topics/%d/jobs/%s", c.id, kind), form, true); rec.Code != http.StatusAccepted {
				t.Fatalf("%s on topic %d: %d %s", kind, c.id, rec.Code, rec.Body.String())
			}
			id, _ := e.lastJob()
			ids = append(ids, id)
		}
		for deadline := time.Now().Add(5 * time.Second); len(e.ad.Calls()) < calls+2; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: both chapters' jobs did not reach the AI at the same time", kind)
			}
		}
		for i, c := range chs {
			b, err := os.ReadFile(filepath.Join(e.base, filepath.FromSlash(path.Join(prompt.WorkRel(ids[i]), step, "inputs/source.md"))))
			if err != nil || !strings.Contains(string(b), c.text) || strings.Contains(string(b), chs[1-i].text) {
				t.Errorf("%s: topic %d sandbox source.md = %q (%v), want only %q", kind, c.id, b, err, c.text)
			}
		}
		for _, id := range ids {
			e.post(fmt.Sprintf("/jobs/%d/cancel", id), nil, true)
			e.pollUntilDone(id)
		}
		e.ad.Scenario = fake.OK
	}
	// run finishes a stage on both chapters so the next one can start.
	run := func(kind string, form url.Values) {
		t.Helper()
		for _, c := range chs {
			if rec := e.post(fmt.Sprintf("/topics/%d/jobs/%s", c.id, kind), form, true); rec.Code != http.StatusAccepted {
				t.Fatalf("%s on topic %d: %d", kind, c.id, rec.Code)
			}
			id, _ := e.lastJob()
			e.pollUntilDone(id)
			if _, st := e.lastJob(); st != "succeeded" {
				t.Fatalf("%s on topic %d: %s", kind, c.id, st)
			}
		}
	}

	both("lesson", nil, "")
	e.ad.Output = lessonOutput("درس")
	run("lesson", nil)

	both("exercises", url.Values{"count": {"3"}}, "")
	e.ad.Output = exOutput()
	run("exercises", url.Values{"count": {"3"}})

	for _, c := range chs {
		rel := filepath.Join(e.base, filepath.FromSlash(path.Join(exercises.AnswersRel(c.dir, 1), "q01-hello.go")))
		if err := os.WriteFile(rel, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	both("evaluate", url.Values{"mode": {"full"}}, "q01")
	e.ad.Output = evalOutput("q01", 90)
	run("evaluate", url.Values{"mode": {"full"}})
	for _, c := range chs { // separate lesson, exercises, answers and report per chapter
		for _, rel := range []string{"lesson/lesson.md", "exercises/v1/exercises.md", "answers/v1/q01-hello.go",
			strings.TrimPrefix(path.Join(evaluation.AttemptRel(c.dir, 1, 1), evaluation.ReportFile("q01")), "topics/"+c.dir+"/")} {
			if _, err := os.Stat(filepath.Join(e.base, "topics", c.dir, filepath.FromSlash(rel))); err != nil {
				t.Errorf("topic %d: %v", c.id, err)
			}
		}
	}
}
