package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	for _, want := range []string{"قاعده پیشنهادی: <strong>آیتم‌های لیست</strong>", "سرفصل‌ها فازند", `name="rule" value="items"`, "اعمال پیشنهاد", "<strong>2</strong> موضوع انتخاب‌شده از 2"} {
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
