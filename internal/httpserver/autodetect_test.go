package httpserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"learninghub/internal/ai/fake"
	"learninghub/internal/services/roadmaps"
)

// TestAutoDetectAndConditionalConfirm is T8.5 (§13.3): upload queues chapter_detect, confirm is 409
// while it runs or without a suggestion, and a failure shows the reason, the log path and «ادامه بدون AI»,
// which imports every heading as a record (tree).
func TestAutoDetectAndConditionalConfirm(t *testing.T) {
	e := newEnv(t)
	csrf := e.sm.CSRFToken()
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := e.db.Writer().QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	e.ad.Scenario = fake.Missing
	draft := e.upload("go.md", []byte("# Go\n## Basics\n### Types\n### Loops\n")).Header().Get("Location")
	scope := "draft:" + strings.TrimPrefix(draft, "/roadmaps/drafts/")
	if n := count(`SELECT COUNT(*) FROM jobs WHERE kind = 'chapter_detect' AND scope_key = ?`, scope); n != 1 {
		t.Fatalf("upload queued %d chapter_detect jobs, want 1", n)
	}
	var jobID int64
	var code string
	if err := e.db.Writer().QueryRow(`SELECT id, error_code FROM jobs WHERE scope_key = ?`, scope).Scan(&jobID, &code); err != nil || code != "adapter_missing" {
		t.Fatalf("job: %q %v, want adapter_missing", code, err)
	}

	// Without a suggestion and without no_ai: refused.
	if rec := e.confirm(draft, url.Values{"csrf_token": {csrf}, "confirm": {"1"}, "no_ai": {""}}); rec.Code != http.StatusConflict {
		t.Fatalf("confirm without suggestion: %d, want 409", rec.Code)
	}

	// The failure page: message, technical details, settings link and both choices.
	page := e.do(httptest.NewRequest(http.MethodGet, draft, nil)).Body.String()
	for _, want := range []string{"فایل اجرایی ابزار انتخاب‌شده پیدا نشد", "جزئیات فنی", fmt.Sprintf(".app/logs/jobs/%d/", jobID),
		`href="/settings"`, "هوش مصنوعی در دسترس نیست", `name="no_ai" value="1"`, "ادامه بدون AI", "تلاش دوباره"} {
		if !strings.Contains(page, want) {
			t.Errorf("failure page lacks %q", want)
		}
	}

	// While a detection runs: refused even with no_ai.
	e.ad.Scenario = fake.Hang
	if rec := e.post(draft+"/chapters", nil, true); rec.Code != http.StatusAccepted {
		t.Fatalf("retry detection: %d", rec.Code)
	}
	if rec := e.confirm(draft, url.Values{"csrf_token": {csrf}, "confirm": {"1"}, "rule": {"tree"}}); rec.Code != http.StatusConflict {
		t.Fatalf("confirm while detecting: %d, want 409", rec.Code)
	}
	id, _ := e.lastJob()
	if _, err := e.runner.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	e.waitDraftJob(draft)

	// «ادامه بدون AI»: every heading is a record (tree), with parent links.
	if rec := e.confirm(draft, url.Values{"csrf_token": {csrf}, "confirm": {"1"}, "no_ai": {"1"}, "rule": {"tree"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm without AI: %d %s", rec.Code, rec.Body.String())
	}
	if n := count(`SELECT COUNT(*) FROM topics`); n != 3 {
		t.Errorf("topics: %d, want 3 (Basics, Types, Loops)", n)
	}
	if n := count(`SELECT COUNT(*) FROM topics c JOIN topics p ON p.id = c.parent_id WHERE p.title = 'Basics'`); n != 2 {
		t.Errorf("children of Basics: %d, want 2", n)
	}
	if n := count(`SELECT COUNT(*) FROM roadmaps WHERE json_extract(select_rule, '$.kind') = 'tree'`); n != 1 {
		t.Errorf("roadmap rule is not tree")
	}
}

// TestTreeCap is R12: a tree selection over the cap warns in the preview and confirm is 422.
func TestTreeCap(t *testing.T) {
	e := newEnv(t)
	var b strings.Builder
	b.WriteString("# Big\n")
	for i := range roadmaps.MaxTreeTopics + 1 {
		fmt.Fprintf(&b, "## H%d\n", i)
	}
	draft := e.upload("big.md", []byte(b.String())).Header().Get("Location")
	e.selection(draft, url.Values{"rule": {"tree"}}, true)
	if page := e.do(httptest.NewRequest(http.MethodGet, draft, nil)).Body.String(); !strings.Contains(page, fmt.Sprintf("بیش از %d رکورد", roadmaps.MaxTreeTopics)) {
		t.Error("preview has no cap warning")
	}
	if rec := e.confirm(draft, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm over cap: %d, want 422", rec.Code)
	}
	var n int
	if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM topics`).Scan(&n); err != nil || n != 0 {
		t.Errorf("topics after refused confirm: %d %v", n, err)
	}
}
