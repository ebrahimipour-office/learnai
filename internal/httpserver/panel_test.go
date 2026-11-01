package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai/fake"
)

func (e *env) seedTopics() {
	e.t.Helper()
	for _, q := range []string{
		`INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'go', 'Go', '{}')`,
		`INSERT INTO groups (id, roadmap_id, stable_key, title, level, ord) VALUES (1, 1, 'g', 'وب', 1, 1)`,
		`INSERT INTO topics (id, roadmap_id, group_id, stable_key, title, title_norm, ord, slug, dir_name) VALUES
			(1, 1, 1, 'a', 'پروتکل HTTP', 'پروتکل http', 1, 'http', '01-http'),
			(2, 1, 1, 'b', 'مسیریابی', 'مسیریابی', 2, 'routing', '02-routing')`,
	} {
		if _, err := e.db.Writer().Exec(q); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) post(path string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", e.sm.CSRFToken())
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	return e.do(req)
}

func (e *env) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	return e.do(req)
}

func lessonOutput(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"lesson_markdown": text + "\n\nادامه متن درس.", // schema minLength 10
		"keywords": []map[string]string{{"term": "Request", "gloss": "درخواست کلاینت"}}, "suggestions": []any{}})
	return b
}

// lastJob returns the newest job id and status of topic 1.
func (e *env) lastJob() (id int64, status string) {
	e.t.Helper()
	if err := e.db.Writer().QueryRow(`SELECT id, status FROM jobs ORDER BY id DESC LIMIT 1`).Scan(&id, &status); err != nil {
		e.t.Fatal(err)
	}
	return id, status
}

// pollUntilDone polls the card as htmx does until the server asks for a page refresh.
func (e *env) pollUntilDone(id int64) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := e.get("/jobs/" + itoa(id) + "/card?poll=1")
		if rec.Code != http.StatusOK {
			e.t.Fatalf("card: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("HX-Refresh") == "true" {
			if strings.Contains(rec.Body.String(), `hx-trigger="every 2s"`) {
				e.t.Fatal("finished card must stop polling")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("job did not finish")
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// R11.2-2: generate, see live progress, lesson rendered with keywords at the end.
func TestLessonGenerateFlow(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Scenario, e.ad.Output = fake.Slow, lessonOutput("## مقدمه\n\nمتن درس با `GET /` و کد:\n\n```go\nhttp.ListenAndServe()\n```\n")
	e.ad.Delay = 300 * time.Millisecond

	rec := e.get("/topics/1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "تولید درس") {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.post("/topics/1/jobs/lesson", nil, true)
	body := rec.Body.String()
	if rec.Code != http.StatusAccepted || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, `type="submit" disabled`) {
		t.Fatalf("enqueue: want 202 polling card with disabled button, got %d\n%s", rec.Code, body)
	}

	// UI is not blocked: another topic opens with its button enabled, the list shows the running job.
	other := e.get("/topics/2").Body.String()
	if strings.Contains(other, `type="submit" disabled`) {
		t.Error("other topic's button must stay enabled")
	}
	if list := e.get("/topics").Body.String(); !strings.Contains(list, "در حال اجرا") || !strings.Contains(list, `href="/topics/1"`) {
		t.Error("topic list must link topics and mark the running one")
	}
	if rec := e.post("/topics/1/jobs/lesson", nil, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "در جریان است") {
		t.Errorf("second enqueue: want 409 busy, got %d", rec.Code)
	}

	id, _ := e.lastJob()
	e.pollUntilDone(id)
	if _, st := e.lastJob(); st != "succeeded" {
		t.Fatalf("job status %s", st)
	}
	page := e.get("/topics/1").Body.String()
	for _, want := range []string{`<h2 dir="auto">مقدمه</h2>`, `<pre dir="ltr"><code class="language-go">`, "موفق", "نسخه 1"} {
		if !strings.Contains(page, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	if i, k := strings.Index(page, "مقدمه"), strings.Index(page, "کلیدواژه"); k < 0 || k < i {
		t.Error("keywords section must follow the lesson body")
	}
	if !strings.Contains(page, "بازتولید درس") || strings.Contains(page, `type="submit" disabled`) {
		t.Error("finished: regenerate button must be enabled")
	}
}

func TestLessonRegenerateNeedsConfirm(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("نسخه اول")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)

	rec := e.post("/topics/1/jobs/lesson", nil, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "نیاز به تأیید") {
		t.Fatalf("no confirm: want 409, got %d", rec.Code)
	}
	if n, _ := e.lastJob(); n != id {
		t.Fatal("unconfirmed regenerate must not enqueue")
	}
	e.ad.Output = lessonOutput("نسخه دوم")
	if rec := e.post("/topics/1/jobs/lesson", url.Values{"confirm_regenerate": {"1"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("plain form confirm: want 303, got %d", rec.Code)
	}
	id2, _ := e.lastJob()
	e.pollUntilDone(id2)
	page := e.get("/topics/1").Body.String()
	if !strings.Contains(page, "نسخه دوم") || !strings.Contains(page, "نسخه 2") {
		t.Fatal("current lesson must be version 2")
	}
	if old := e.get("/topics/1/lesson?v=1").Body.String(); !strings.Contains(old, "نسخه اول") || !strings.Contains(old, "نسخه قدیمی 1") {
		t.Error("?v=1 must render the archived version")
	}
}

func TestJobCancelAndRetry(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Scenario, e.ad.Delay = fake.Slow, 10*time.Second
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	if rec := e.post("/jobs/"+itoa(id)+"/cancel", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	e.pollUntilDone(id)
	if _, st := e.lastJob(); st != "canceled" {
		t.Fatalf("after cancel: %s", st)
	}
	if rec := e.post("/jobs/"+itoa(id)+"/cancel", nil, true); rec.Code != http.StatusConflict {
		t.Errorf("cancel finished job: want 409, got %d", rec.Code)
	}
	if rec := e.post("/jobs/"+itoa(id)+"/retry", nil, true); rec.Code != http.StatusConflict {
		t.Errorf("retry canceled job: want 409, got %d", rec.Code)
	}

	e.ad.Scenario = "boom" // unknown scenario ⇒ tool_failed
	e.post("/topics/1/jobs/lesson", nil, true)
	failed, _ := e.lastJob()
	e.pollUntilDone(failed)
	card := e.get("/jobs/" + itoa(failed) + "/card").Body.String()
	if !strings.Contains(card, "ناموفق") || !strings.Contains(card, "تلاش مجدد") || !strings.Contains(card, "tool_error") {
		t.Fatalf("failed card: %s", card)
	}
	e.ad.Scenario, e.ad.Output = fake.OK, lessonOutput("پس از تکرار")
	if rec := e.post("/jobs/"+itoa(failed)+"/retry", nil, true); rec.Code != http.StatusAccepted {
		t.Fatalf("retry: want 202, got %d", rec.Code)
	}
	retry, _ := e.lastJob()
	var of int64
	_ = e.db.Writer().QueryRow(`SELECT retry_of FROM jobs WHERE id = ?`, retry).Scan(&of)
	if of != failed {
		t.Errorf("retry_of = %d, want %d", of, failed)
	}
}

func TestPanelSafetyAndNotFound(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	// The user may edit lesson.md; it is still untrusted when rendered.
	if err := e.root.WriteAtomic("topics/01-http/lesson/lesson.md", []byte("<script>alert(1)</script>\n\n[x](javascript:alert(1))"), 0o644); err != nil {
		t.Fatal(err)
	}
	if page := e.get("/topics/1").Body.String(); strings.Contains(page, "<script>alert") || strings.Contains(page, "javascript:") {
		t.Fatal("edited lesson must be sanitized")
	}
	_ = e.root.Remove("topics/01-http/lesson/lesson.md")
	if rec := e.get("/topics/1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "پیدا نشد") {
		t.Errorf("missing lesson file: want 200 with notice, got %d", rec.Code)
	}
	for _, p := range []string{"/topics/999", "/topics/abc", "/jobs/999/card"} {
		if rec := e.get(p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", p, rec.Code)
		}
	}
	if rec := e.post("/topics/999/jobs/lesson", nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("enqueue unknown topic: want 404, got %d", rec.Code)
	}
}
