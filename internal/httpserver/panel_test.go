package httpserver_test

import (
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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
	e.ad.Delay = 1500 * time.Millisecond // long enough to see the running card's elapsed time

	rec := e.get("/topics/1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "تولید درس") {
		t.Fatalf("panel: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.post("/topics/1/jobs/lesson", nil, true)
	body := rec.Body.String()
	if rec.Code != http.StatusAccepted || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, `type="submit" disabled`) {
		t.Fatalf("enqueue: want 202 polling card with disabled button, got %d\n%s", rec.Code, body)
	}
	// T10.13: the fragment retitles the browser tab and marks the card busy
	if !strings.HasPrefix(body, "<title>⏳ درس · ") || !strings.Contains(body, `aria-busy="true"`) {
		t.Errorf("active job: want ⏳ title and aria-busy card\n%s", body)
	}
	time.Sleep(1100 * time.Millisecond) // past the first second so elapsed time is non-zero
	if card := e.get("/topics/1").Body.String(); !strings.Contains(card, "زمان سپری‌شده: ") || !strings.Contains(card, "<title>⏳ ") {
		t.Errorf("running card must show elapsed time and ⏳ title\n%s", card)
	}

	// UI is not blocked: another topic opens with its button enabled, the list shows the running job.
	other := e.get("/topics/2").Body.String()
	if strings.Contains(other, `type="submit" disabled`) {
		t.Error("other topic's button must stay enabled")
	}
	if list := e.get("/topics").Body.String(); !strings.Contains(list, "در حال اجرا") || !strings.Contains(list, `href="/topics/1"`) {
		t.Error("topic list must link topics and mark the running one")
	}
	// T9.8: each roadmap shows its progress bar and the next topic to study
	if list := html.UnescapeString(e.get("/topics").Body.String()); !strings.Contains(list, `<progress max="`) || !strings.Contains(list, "ارزیابی‌شده") || !strings.Contains(list, `ادامه یادگیری`) {
		t.Error("topic list must show roadmap progress and a continue link")
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
	for _, want := range []string{`<h3 id="h-مقدمه" dir="auto">مقدمه</h3>`, `<pre dir="ltr"><code class="language-go">`, "موفق", "نسخه ۱",
		`<a href="/topics/1">✓ انجام شد — مشاهده درس</a>`, "مدت: "} {
		if !strings.Contains(page, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	if i, k := strings.Index(page, "مقدمه"), strings.Index(page, "کلیدواژه"); k < 0 || k < i {
		t.Error("keywords section must follow the lesson body")
	}
	if strings.Contains(page, "⏳") || strings.Contains(page, "aria-busy") {
		t.Error("finished job: no ⏳ title or busy card")
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
	if !strings.Contains(page, "نسخه دوم") || !strings.Contains(page, "نسخه ۲") {
		t.Fatal("current lesson must be version 2")
	}
	if old := e.get("/topics/1/lesson?v=1").Body.String(); !strings.Contains(old, "نسخه اول") || !strings.Contains(old, "نسخه قدیمی ۱") {
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

// B7: a tab shows only its own kind's card, but a job of another kind still disables every action (R11.2-2).
func TestTabShowsOwnJobKind(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	if _, err := e.db.Writer().Exec(`INSERT INTO jobs (kind, scope_key, topic_id, status, started_at)
		VALUES ('exercises', 'topic:1', 1, 'running', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	lesson := e.get("/topics/1").Body.String()
	if strings.Contains(lesson, "<strong>تولید تمرین</strong>") || strings.Contains(lesson, `hx-trigger="every 2s"`) {
		t.Error("lesson tab must not show the exercises job card")
	}
	if !strings.Contains(lesson, `type="submit" disabled`) || !strings.Contains(lesson, "کار دیگری روی این موضوع در جریان است") {
		t.Error("lesson button must be disabled while another job of the topic runs")
	}
	if ex := e.get("/topics/1/exercises").Body.String(); !strings.Contains(ex, "<strong>تولید تمرین</strong>") {
		t.Error("exercises tab must show its running job card")
	}
}

// CR-49: showTopic ignores ?tab= and always renders the lesson tab content.
func TestTopicIgnoresTabQueryParam(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()

	e.ad.Output = lessonOutput("متن درس تستی")
	rec := e.post("/topics/1/jobs/lesson", nil, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue lesson: %d", rec.Code)
	}
	id, _ := e.lastJob()
	e.pollUntilDone(id)

	for _, path := range []string{"/topics/1?tab=reports", "/topics/1/lesson?tab=reports", "/topics/1?tab=exercises", "/topics/1?tab=suggestions"} {
		res := e.get(path)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", path, res.Code)
		}
		body := res.Body.String()
		if !strings.Contains(body, "متن درس تستی") {
			t.Errorf("%s: expected lesson content in body", path)
		}
		if !strings.Contains(body, `<section class="lesson">`) {
			t.Errorf("%s: expected lesson section in body", path)
		}
		if strings.Contains(body, `<section class="set">`) {
			t.Errorf("%s: must not render set section", path)
		}
		if !strings.Contains(body, `<a href="/topics/1/lesson" aria-current="page">درس</a>`) {
			t.Errorf("%s: expected lesson link to have aria-current=\"page\"", path)
		}
		if strings.Contains(body, `<a href="/topics/1/reports" aria-current="page">`) {
			t.Errorf("%s: reports link must not have aria-current=\"page\"", path)
		}
	}
}

// T9.9: chapter pager and the status-driven next-step link.
func TestTopicPagerAndNextStep(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	first := e.get("/topics/1").Body.String()
	if !strings.Contains(first, `<a href="/topics/2" rel="next">فصل بعد: <bdi>مسیریابی</bdi> ←</a>`) || strings.Contains(first, `rel="prev"`) {
		t.Error("first topic: want only a next link")
	}
	if strings.Contains(first, `class="next-step"`) {
		t.Error("created topic: no next step (the lesson tab already offers generation)")
	}
	second := e.get("/topics/2/answers").Body.String()
	if !strings.Contains(second, `<a href="/topics/1" rel="prev">→ فصل قبل: <bdi>پروتکل HTTP</bdi></a>`) || strings.Contains(second, `rel="next"`) {
		t.Error("last topic: want only a prev link")
	}

	for status, want := range map[string]string{
		"content_ready":   `<a href="/topics/1/exercises">ساخت تمرین</a>`,
		"exercises_ready": `<a href="/topics/1/answers">نوشتن پاسخ‌ها</a>`,
		"answered":        `<a href="/topics/1/reports">ارزیابی پاسخ‌ها</a>`,
		"evaluated":       `<a href="/topics/2">فصل بعد: <bdi>مسیریابی</bdi></a>`,
	} {
		if _, err := e.db.Writer().Exec(`UPDATE topics SET status = ? WHERE id = 1`, status); err != nil {
			t.Fatal(err)
		}
		if body := e.get("/topics/1").Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s: missing next step %s", status, want)
		}
	}
	if _, err := e.db.Writer().Exec(`UPDATE topics SET status = 'answered' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if body := e.get("/topics/1/reports").Body.String(); strings.Contains(body, `class="next-step"`) {
		t.Error("next step must be hidden on the tab it points to")
	}
}

// T10.9: the stepper marks finished steps and makes the step the status calls for the primary link.
func TestTopicStepper(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	steps := []string{"lesson", "exercises", "answers", "reports"}
	for cur, status := range []string{"created", "content_ready", "exercises_ready", "answered", "evaluated"} {
		if _, err := e.db.Writer().Exec(`UPDATE topics SET status = ? WHERE id = 1`, status); err != nil {
			t.Fatal(err)
		}
		body := e.get("/topics/1/answers").Body.String()
		for i, tab := range steps {
			link := `<a href="/topics/1/` + tab + `"`
			active := strings.Contains(body, link+` class="primary" aria-current="step">`)
			done := strings.Contains(body, `<li class="done">`+link+`>`)
			if active != (i == cur) || done != (i < cur) {
				t.Errorf("%s: step %s active=%v done=%v", status, tab, active, done)
			}
		}
		if strings.Contains(body, `class="next-step"`) {
			t.Errorf("%s: bottom next step belongs to the lesson tab only", status)
		}
	}
}

// T9.10: a lesson with three or more level 2–3 headings shows a TOC linking to their ids.
func TestLessonTOC(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("## مقدمه\n\nمتن\n\n### جزئیات\n\nمتن\n\n## جمع‌بندی")
	if rec := e.post("/topics/1/jobs/lesson", nil, true); rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue lesson: %d", rec.Code)
	}
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	body := e.get("/topics/1").Body.String()
	if !strings.Contains(body, `<details class="toc">`) || !strings.Contains(body, `<li class="toc-3"><a href="#h-`) {
		t.Fatalf("TOC missing:\n%s", body)
	}
	if !strings.Contains(body, `<h3 id="h-مقدمه" dir="auto">مقدمه</h3>`) {
		t.Error("lesson heading lacks its anchor id")
	}
}

// T9.12: archive/unarchive touch no file, are refused while busy or without CSRF, and the list hides
// archived topics unless asked.
func TestArchiveTopic(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	snapshot := func() map[string]string {
		m := map[string]string{}
		dir := filepath.Join(e.base, "topics", "01-http")
		if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			m[p] = string(b)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := snapshot()
	if len(before) == 0 {
		t.Fatal("no lesson files to compare")
	}

	req := httptest.NewRequest(http.MethodPost, "/topics/1/archive", nil)
	if rec := e.do(req); rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF: want 403, got %d", rec.Code)
	}
	if rec := e.post("/topics/1/archive", nil, false); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/topics/1" {
		t.Fatalf("archive: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Error("archiving changed the topic folder")
	}
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, `action="/topics/1/unarchive"`) {
		t.Error("archived topic must offer restore")
	}
	if list := e.get("/topics").Body.String(); strings.Contains(list, `href="/topics/1"`) || !strings.Contains(list, `href="/topics/2"`) {
		t.Error("list must hide the archived topic by default")
	}
	if list := e.do(httptest.NewRequest(http.MethodGet, "/topics?archived=1", nil)).Body.String(); !strings.Contains(list, `href="/topics/1"`) || !strings.Contains(list, `name="archived" value="1" checked`) {
		t.Error("archived=1 must show the archived topic")
	}

	if _, err := e.db.Writer().Exec(`INSERT INTO jobs (kind, scope_key, topic_id, adapter, status) VALUES ('lesson', 'topic:1', 1, 'fake', 'running')`); err != nil { // running, not queued: the live runner must not claim it
		t.Fatal(err)
	}
	if rec := e.post("/topics/1/unarchive", nil, false); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "در جریان است") {
		t.Errorf("busy: want 409 with message, got %d", rec.Code)
	}
	if _, err := e.db.Writer().Exec(`DELETE FROM jobs WHERE status = 'running'`); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/topics/1/unarchive", nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("unarchive: %d", rec.Code)
	}
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, `action="/topics/1/archive"`) {
		t.Error("restored topic must offer archive")
	}
	if rec := e.post("/topics/999/archive", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown topic: want 404, got %d", rec.Code)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Error("restoring changed the topic folder")
	}
}

// T10.4: the resume card appears only once a topic was opened, and points at that topic.
func TestResumeCard(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	page := func() string { return e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String() } // full page, not the htmx fragment
	if list := page(); !strings.Contains(list, `role="search"`) || strings.Contains(list, `class="resume"`) {
		t.Error("no topic opened yet: no resume card")
	}
	e.get("/topics/2/exercises")
	card, _, _ := strings.Cut(page(), `role="search"`)
	if !strings.Contains(card, `class="resume"`) || !strings.Contains(card, `<a href="/topics/2">`) || !strings.Contains(card, "ساخت درس") {
		t.Errorf("resume card must show topic 2 and its next step:\n%s", card)
	}
}

// T10.11: lesson and exercises tabs offer a print button; app.js prints via a delegated listener (CSP) and
// app.css hides the chrome in print.
func TestPrintView(t *testing.T) {
	e := newEnv(t)
	e.exerciseSet() // seeds topics and also builds the lesson
	for _, tab := range []string{"/topics/1", "/topics/1/exercises"} {
		if !strings.Contains(e.get(tab).Body.String(), `<button type="button" class="link" data-print>چاپ</button>`) {
			t.Errorf("%s: print button missing", tab)
		}
	}
	if strings.Contains(e.get("/topics/1/answers").Body.String(), "data-print") {
		t.Error("answers tab has nothing to print")
	}
	if js := e.get("/static/app.js").Body.String(); !strings.Contains(js, `closest("button[data-print]")) return window.print()`) {
		t.Error("app.js must print from a delegated listener")
	}
	css := e.get("/static/app.css").Body.String()
	_, print, ok := strings.Cut(css, "@media print {")
	if !ok || !strings.Contains(print, "header, nav, form, button") || !strings.Contains(print, "white-space: pre-wrap") || !strings.Contains(print, `attr(href)`) {
		t.Error("app.css print rules missing")
	}
}
