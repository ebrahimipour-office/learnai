package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"learninghub/internal/ai/fake"
)

func (e *env) qaJobs() (n int) {
	e.t.Helper()
	if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = 'qa'`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) qaRows() (n int) {
	e.t.Helper()
	if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM lesson_qa`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// T10.21: ask about the lesson → qa job → answer stored in lesson_qa and rendered safely under the lesson.
func TestLessonQuestion(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	ask := func(q string) int { return e.post("/topics/1/jobs/qa", url.Values{"question": {q}}, true).Code }

	if c := ask("چرا؟"); c != http.StatusConflict {
		t.Errorf("no lesson: want 409, got %d", c)
	}
	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, `action="/topics/1/jobs/qa"`) || !strings.Contains(page, `maxlength="2000"`) {
		t.Error("lesson page: want the question form")
	}

	for name, q := range map[string]string{"empty": "  \n ", "too long": strings.Repeat("س", 2001)} {
		// T13.8(b): 422 so htmx swaps the Persian message in instead of a generic «کد 400».
		if rec := e.post("/topics/1/jobs/qa", url.Values{"question": {q}}, true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "پرسش باید بین") {
			t.Errorf("%s question: want 422 with the message, got %d", name, rec.Code)
		}
	}
	if c := ask("x"); c != http.StatusAccepted {
		t.Fatalf("valid question: want 202, got %d", c)
	}
	noCSRF := strings.NewReader(url.Values{"question": {"x"}}.Encode())
	r, _ := http.NewRequest(http.MethodPost, "/topics/1/jobs/qa", noCSRF)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Host = "127.0.0.1:8787"
	if rec := e.do(r); rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF: want 403, got %d", rec.Code)
	}
	id, _ = e.lastJob() // the "x" job above
	e.pollUntilDone(id)
	if n := e.qaJobs(); n != 1 {
		t.Fatalf("only the valid request may enqueue; qa jobs = %d", n)
	}

	// Invalid output fails the job and stores nothing.
	e.ad.Scenario = fake.InvalidJSON
	if c := ask("بدون پاسخ"); c != http.StatusAccepted {
		t.Fatalf("want 202, got %d", c)
	}
	id, st := e.lastJob()
	e.pollUntilDone(id)
	if _, st = e.lastJob(); st != "failed" {
		t.Errorf("invalid output: job %s, want failed", st)
	}
	before := e.qaRows()

	// A hostile answer and question are shown escaped/sanitized.
	e.ad.Scenario = fake.OK
	ans, _ := json.Marshal(map[string]string{"answer_markdown": "پاسخ **مهم** <script>alert(1)</script> [x](javascript:alert(2)) <img src=x onerror=alert(3)>"})
	e.ad.Output = ans
	if c := ask(`<b onclick="evil()">سؤال</b>`); c != http.StatusAccepted {
		t.Fatalf("want 202, got %d", c)
	}
	id, _ = e.lastJob()
	e.pollUntilDone(id)
	if _, st = e.lastJob(); st != "succeeded" {
		t.Fatalf("qa job %s", st)
	}
	if n := e.qaRows(); n != before+1 {
		t.Errorf("lesson_qa rows = %d, want %d", n, before+1)
	}
	in := e.ad.Inputs()
	last := in[len(in)-1]
	if last["question.md"] != `<b onclick="evil()">سؤال</b>` || !strings.Contains(last["lesson.md"], "ادامه متن درس") {
		t.Errorf("AI inputs: want question.md and lesson.md, got %v", last)
	}
	page := e.get("/topics/1").Body.String()
	if !strings.Contains(page, "<strong>مهم</strong>") || !strings.Contains(page, "<summary><bdi>&lt;b onclick=") {
		t.Error("want the rendered answer and the escaped question")
	}
	for _, bad := range []string{"<script>alert", "javascript:alert", "onerror=", `<b onclick`} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if card := e.get("/jobs/" + itoa(id) + "/card").Body.String(); !strings.Contains(card, `href="/topics/1#qa"`) {
		t.Error("finished qa card: want a link to the answer")
	}
}

// T13.12(b): a lesson page renders only the newest 30 questions; ?qa=all shows the rest.
func TestLessonQuestionsCapped(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	_, err := e.db.Writer().Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 100)
		INSERT INTO jobs(id, kind, scope_key, topic_id, status) SELECT 1000+i, 'qa', 'topic:1', 1, 'succeeded' FROM n`)
	if err == nil {
		_, err = e.db.Writer().Exec(`INSERT INTO lesson_qa(topic_id, lesson_version_id, question, answer_md, job_id)
			SELECT 1, (SELECT id FROM lesson_versions WHERE topic_id = 1), 'پرسش-' || (id-1000) || '؟', 'پاسخ', id FROM jobs WHERE kind = 'qa'`)
	}
	if err != nil {
		t.Fatal(err)
	}
	page := e.get("/topics/1").Body.String()
	if n := strings.Count(page, `class="qa-item"`); n != 30 {
		t.Errorf("want 30 questions, got %d", n)
	}
	if !strings.Contains(page, "پرسش-100؟") || strings.Contains(page, "پرسش-70؟") || !strings.Contains(page, "qa=all#qa") {
		t.Error("want the newest 30 and a «نمایش همه» link")
	}
	all := e.get("/topics/1/lesson?v=1&qa=all").Body.String()
	if n := strings.Count(all, `class="qa-item"`); n != 100 || strings.Contains(all, "qa=all#qa") {
		t.Errorf("?qa=all: want 100 questions and no link, got %d", n)
	}
}
