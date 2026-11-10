package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai/fake"
)

func (e *env) analyze(draftURL string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, draftURL+"/ai-assist", nil)
	req.Header.Set("X-CSRF-Token", e.sm.CSRFToken())
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	return e.do(req)
}

// T6.2: the AI suggestion is shown apart from the deterministic rule and only applies when the user does.
func TestDraftAIAssistFlow(t *testing.T) {
	e := newEnv(t)
	e.ad.Output = json.RawMessage(`{"rule":{"kind":"items"},"reason":"سرفصل‌ها فازند و موضوعات آیتم‌های لیست"}`)
	rec := e.upload("go.md", []byte("# Go\n## Phase 1\n- Types\n- Loops\n## Phase 2\n- Goroutines\n"))
	draftURL := rec.Header().Get("Location")

	page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(page, "تحلیل ساختار با AI") || strings.Contains(page, "اعمال پیشنهاد") {
		t.Fatalf("fresh draft: want analyze button, no suggestion\n%s", page)
	}

	rec = e.analyze(draftURL, true)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `id="draft-ai"`) || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("ai-assist: want 202 fragment, got %d\n%s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec = e.do(httptest.NewRequest(http.MethodGet, draftURL+"/ai?poll=1", nil))
		if rec.Header().Get("HX-Refresh") == "true" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("analysis did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if id, status := e.lastJob(); status != "succeeded" {
		t.Fatalf("job %d: %s", id, status)
	}

	page = e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	for _, want := range []string{"قاعده پیشنهادی: <strong>آیتم‌های لیست</strong>", "سرفصل‌ها فازند", `name="rule" value="ai"`, "اعمال پیشنهاد", "<strong>2</strong> موضوع انتخاب‌شده از 2"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	var n int
	if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM topics`).Scan(&n); err != nil || n != 0 {
		t.Errorf("analysis created %d topics (%v)", n, err)
	}

	// The user applies it through the normal selection form.
	if rec := e.selection(draftURL, url.Values{"rule": {"items"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply: %d", rec.Code)
	}
	page = e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(page, "<strong>3</strong> موضوع انتخاب‌شده از 3") || strings.Contains(page, "اعمال پیشنهاد") {
		t.Errorf("after apply: want 3 topics and no apply button\n%s", page)
	}
}

func TestDraftAIAssistBusyAndFailure(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## A\n")).Header().Get("Location")

	e.ad.Scenario = fake.Hang
	if rec := e.analyze(draftURL, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("first: want 303, got %d", rec.Code)
	}
	rec := e.analyze(draftURL, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "در جریان است") {
		t.Fatalf("second: want 409, got %d\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `hx-trigger="every 2s"`) {
		t.Error("running analysis must poll")
	}
	id, _ := e.lastJob()
	if _, err := e.runner.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	// Invalid output twice: the failure shows next to a retry button, no suggestion is stored.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, status := e.lastJob(); status == "canceled" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("hung job not canceled: %s", status)
		}
	}
	e.ad.Scenario = fake.InvalidJSON
	e.analyze(draftURL, false)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, status := e.lastJob(); status == "failed" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("invalid output job: %s", status)
		}
	}
	page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(page, "تحلیل ناموفق بود: خروجی AI معتبر نبود.") || !strings.Contains(page, "تحلیل ساختار با AI") || strings.Contains(page, "قاعده پیشنهادی") {
		t.Errorf("failed analysis page wrong\n%s", page)
	}

	if rec := e.analyze("/roadmaps/drafts/nope", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown draft: want 404, got %d", rec.Code)
	}
}

// T7.4/T8.4: detect chapters (auto-applied as a tree rule), tick, then a chapters rule: rename, merge, reject an invalid edit, confirm.
func TestDraftChapterFlow(t *testing.T) {
	e := newEnv(t)
	e.ad.Output = json.RawMessage(`{"title":"کتاب Go","reason":"هر H2 یک فصل","chapters":[
		{"title":"مقدمات","start_line":3,"end_line":4},{"title":"توابع","start_line":5,"end_line":6},{"title":"همروندی","start_line":7,"end_line":8}]}`)
	draftURL := e.upload("go.md", []byte("# Go Book\npreface\n## Basics\nb\n## Funcs\nf\n## Conc\nc\n")).Header().Get("Location")

	req := httptest.NewRequest(http.MethodPost, draftURL+"/chapters", nil)
	req.Header.Set("X-CSRF-Token", e.sm.CSRFToken())
	req.Header.Set("HX-Request", "true")
	if rec := e.do(req); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `id="draft-ai"`) {
		t.Fatalf("chapters: want 202 fragment, got %d\n%s", rec.Code, rec.Body.String())
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if e.do(httptest.NewRequest(http.MethodGet, draftURL+"/ai?poll=1", nil)).Header().Get("HX-Refresh") == "true" {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("chapter detection did not finish")
		}
	}
	// T8.4: the untouched selection took the suggestion (tree rule + title) on its own; no apply button.
	page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	for _, want := range []string{"<strong>3 فصل</strong>", "عنوان پیشنهادی: <bdi>کتاب Go</bdi>", "همروندی", "(خط 7)",
		`value="کتاب Go"`, "<strong>3</strong> موضوع انتخاب‌شده از 3", `value="tree" selected`} {
		if !strings.Contains(page, want) {
			t.Errorf("suggestion page missing %q", want)
		}
	}
	if strings.Contains(page, "اعمال پیشنهاد") {
		t.Error("an applied suggestion offers no apply button")
	}
	// ticks under the tree rule keep its heading list
	keys := regexp.MustCompile(`name="include" value="([^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(keys) != 3 {
		t.Fatalf("include keys = %v", keys)
	}
	tick := url.Values{"rule": {"tree"}, "shown_rule": {"tree"}, "include": {keys[0][1], keys[2][1]}}
	if rec := e.selection(draftURL, tick, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("tick: %d", rec.Code)
	}
	if page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String(); !strings.Contains(page, "<strong>2</strong> موضوع انتخاب‌شده از 3") {
		t.Errorf("after unticking one tree record\n%s", page)
	}

	// the chapters rule (T7.4) is still editable: rename, merge, reject an invalid edit
	edit := func(merge string, ends ...string) url.Values {
		v := url.Values{"rule": {"chapters"}, "shown_rule": {"chapters"}, "ch_line": {"3", "5", "7"}, "ch_end": ends,
			"ch_title": {"مقدمات", " توابع Go ", "همروندی"}}
		if merge != "" {
			v.Set("merge", merge)
		}
		return v
	}
	rec := e.selection(draftURL, edit("", "4", "7", "8"), true) // overlaps chapter 3
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "فصل 3 با فصل قبلی هم‌پوشانی دارد") {
		t.Fatalf("overlap: want 422 with message, got %d\n%s", rec.Code, rec.Body.String())
	}
	// rename chapter 2 and merge 2+3; chapter 1 unticked
	v := edit("2", "4", "6", "8")
	v["include"] = []string{keys[1][1], keys[2][1]}
	if rec := e.selection(draftURL, v, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("merge: %d", rec.Code)
	}
	page = e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(page, `name="ch_title" value="توابع Go"`) || !strings.Contains(page, "خط 5 تا 8") || strings.Contains(page, `aria-label="عنوان فصل 3"`) {
		t.Fatalf("after merge\n%s", page)
	}

	confirm := url.Values{"confirm": {"1"}, "title": {"کتاب Go"}}
	creq := httptest.NewRequest(http.MethodPost, draftURL+"/confirm", strings.NewReader(confirm.Encode()))
	creq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	creq.Header.Set("X-CSRF-Token", e.sm.CSRFToken())
	if rec := e.do(creq); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d\n%s", rec.Code, rec.Body.String())
	}
	var titles string
	if err := e.db.Writer().QueryRow(`SELECT group_concat(title, '|') FROM (SELECT title FROM topics ORDER BY id)`).Scan(&titles); err != nil || titles != "توابع Go" {
		t.Errorf("topics = %q (%v)", titles, err)
	}
	var rt string
	if err := e.db.Writer().QueryRow(`SELECT title FROM roadmaps`).Scan(&rt); err != nil || rt != "کتاب Go" {
		t.Errorf("roadmap title = %q (%v)", rt, err)
	}
}
