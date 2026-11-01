package httpserver_test

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/httpserver"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/store"
)

type env struct {
	t    *testing.T
	h    http.Handler
	sm   *httpserver.SecurityManager
	db   *store.DB
	base string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	db, err := store.Open(filepath.Join(base, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sm, err := httpserver.NewSecurityManager("127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	srv := httpserver.NewServer("127.0.0.1:8787", sm, roadmaps.NewService(db, root), topics.NewService(db))
	if srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 {
		t.Error("server timeouts must be set (Section 8)")
	}
	return &env{t: t, h: srv.Handler, sm: sm, db: db, base: base}
}

func (e *env) do(req *http.Request) *httptest.ResponseRecorder {
	if req.Host == "example.com" { // httptest default
		req.Host = "127.0.0.1:8787"
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) upload(name string, content []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("csrf_token", e.sm.CSRFToken())
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(content)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/roadmaps/drafts", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	return e.do(req)
}

func (e *env) selection(draftURL string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, draftURL+"/selection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", e.sm.CSRFToken())
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	return e.do(req)
}

func TestSecurityMiddlewareIsWired(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "evil.com"
	if rec := e.do(req); rec.Code != http.StatusForbidden {
		t.Errorf("foreign Host: want 403, got %d", rec.Code)
	}
	rec := e.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz: want 200 ok, got %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("CSP header missing, middleware not wired")
	}
	rec = e.do(httptest.NewRequest(http.MethodGet, "/static/htmx.min.js", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "htmx") {
		t.Errorf("local htmx: want 200, got %d", rec.Code)
	}
}

func TestImportPreviewFlow(t *testing.T) {
	e := newEnv(t)
	md := "# Go\n## Basics\n### Types\n- int\n### Loops\n## Concurrency\n### Goroutines\n"

	rec := e.upload("go.md", []byte(md))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	draftURL := rec.Header().Get("Location")
	if !strings.HasPrefix(draftURL, "/roadmaps/drafts/") {
		t.Fatalf("redirect: got %q", draftURL)
	}

	rec = e.do(httptest.NewRequest(http.MethodGet, draftURL, nil))
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, "<strong>3</strong> موضوع") || !strings.Contains(page, "Goroutines") {
		t.Fatalf("preview page: %d\n%s", rec.Code, page)
	}
	if !strings.Contains(page, e.sm.CSRFToken()) {
		t.Error("preview page must carry the CSRF token for htmx")
	}

	// Untick one topic: fetch the keys from the page, keep all but the first.
	keys := extractValues(page, `name="include" value="`)
	if len(keys) != 3 {
		t.Fatalf("want 3 checkboxes, got %v", keys)
	}
	rec = e.selection(draftURL, url.Values{"rule": {"deepest"}, "shown_rule": {"deepest"}, "include": keys[1:]}, true)
	frag := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(frag, "<html") || !strings.Contains(frag, "<strong>2</strong> موضوع انتخاب‌شده از 3") {
		t.Fatalf("htmx selection: want fragment with 2 of 3, got %d\n%s", rec.Code, frag)
	}

	// Switching rule resets ticks; level-2 gives 2 topics (Basics, Concurrency).
	rec = e.selection(draftURL, url.Values{"rule": {"level-2"}, "shown_rule": {"deepest"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("non-htmx selection: want 303, got %d", rec.Code)
	}
	rec = e.do(httptest.NewRequest(http.MethodGet, draftURL, nil))
	if !strings.Contains(rec.Body.String(), "<strong>2</strong> موضوع انتخاب‌شده از 2") {
		t.Errorf("level-2 preview wrong:\n%s", rec.Body.String())
	}

	if rec := e.selection(draftURL, url.Values{"rule": {"level-9"}}, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad rule: want 422, got %d", rec.Code)
	}

	// R2.2 / R11.2-1: nothing is recorded and no roadmap file exists before confirmation.
	for _, table := range []string{"roadmaps", "roadmap_revisions", "groups", "topics"} {
		var n int
		if err := e.db.Writer().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: want 0 rows before confirm, got %d (%v)", table, n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.base, "roadmaps")); !os.IsNotExist(err) {
		t.Errorf("roadmaps/ must not exist before confirm: %v", err)
	}
}

func TestImportPreviewErrors(t *testing.T) {
	e := newEnv(t)

	if rec := e.upload("a.txt", []byte("# x")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("wrong extension: want 422, got %d", rec.Code)
	}
	if rec := e.upload("big.md", bytes.Repeat([]byte("a"), roadmaps.MaxSourceBytes+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("1MB+1 file: want 413, got %d", rec.Code)
	}
	if rec := e.upload("huge.md", bytes.Repeat([]byte("a"), 2<<20)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("2MB body: want 413, got %d", rec.Code)
	}
	if rec := e.upload("max.md", bytes.Repeat([]byte("a"), roadmaps.MaxSourceBytes)); rec.Code != http.StatusSeeOther {
		t.Errorf("exactly 1MB: want 303, got %d", rec.Code)
	}
	if rec := e.do(httptest.NewRequest(http.MethodGet, "/roadmaps/drafts/nope", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown draft: want 404, got %d", rec.Code)
	}

	// A list-only file finds nothing under the default rule; the preview suggests "items".
	rec := e.upload("list.md", []byte("- Closures\n- Iterators\n"))
	rec = e.do(httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	if !strings.Contains(rec.Body.String(), "آیتم‌های لیست» را امتحان کنید") {
		t.Errorf("zero-topic draft must suggest the items rule:\n%s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/roadmaps/drafts", strings.NewReader(""))
	if rec := e.do(req); rec.Code != http.StatusForbidden {
		t.Errorf("upload without CSRF: want 403, got %d", rec.Code)
	}
}

func extractValues(s, prefix string) []string {
	var out []string
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			return out
		}
		s = s[i+len(prefix):]
		end := strings.IndexByte(s, '"')
		out = append(out, s[:end])
		s = s[end:]
	}
}

func (e *env) confirm(draftURL string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, draftURL+"/confirm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	return e.do(req)
}

func TestConfirmImport(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## Basics\n### Types\n### Loops\n")).Header().Get("Location")
	page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(page, `action="`+draftURL+`/confirm"`) {
		t.Fatalf("preview must offer a confirm form:\n%s", page)
	}
	csrf := e.sm.CSRFToken()

	if rec := e.confirm(draftURL, url.Values{"csrf_token": {""}, "confirm": {"1"}}); rec.Code != http.StatusForbidden {
		t.Errorf("confirm without CSRF: want 403, got %d", rec.Code)
	}
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}}); rec.Code != http.StatusConflict {
		t.Errorf("confirm without confirm=1: want 409, got %d", rec.Code)
	}
	rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}, "confirm": {"1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/topics" {
		t.Fatalf("confirm: want 303 to /topics, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var n int
	if err := e.db.Writer().QueryRow("SELECT COUNT(*) FROM topics").Scan(&n); err != nil || n != 2 {
		t.Errorf("topics: want 2, got %d (%v)", n, err)
	}
	if _, err := os.Stat(filepath.Join(e.base, "roadmaps", "go", "rev-001.md")); err != nil {
		t.Errorf("rev-001.md: %v", err)
	}
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}, "confirm": {"1"}}); rec.Code != http.StatusNotFound {
		t.Errorf("repeat confirm: want 404, got %d", rec.Code)
	}
}

func TestTopicListPage(t *testing.T) {
	e := newEnv(t)
	rec := e.do(httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/topics" {
		t.Errorf("/: want 303 to /topics, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if body := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String(); !strings.Contains(body, `href="/roadmaps/new">یک نقشه راه`) {
		t.Errorf("empty list must link to import:\n%s", body)
	}

	// 500 topics in 10 groups (T2.6: server-side render under 300ms p95).
	var md strings.Builder
	md.WriteString("# Big\n")
	for i := 0; i < 500; i++ {
		if i%50 == 0 {
			fmt.Fprintf(&md, "## Group %d\n", i/50)
		}
		fmt.Fprintf(&md, "### Topic %d\n", i)
	}
	draftURL := e.upload("big.md", []byte(md.String())).Header().Get("Location")
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}

	var times []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		rec = e.do(httptest.NewRequest(http.MethodGet, "/topics", nil))
		times = append(times, time.Since(start))
		if rec.Code != http.StatusOK {
			t.Fatalf("/topics: %d", rec.Code)
		}
	}
	slices.Sort(times)
	t.Logf("500 topics: p95 %v", times[18])
	if p95 := times[18]; p95 > 300*time.Millisecond {
		t.Errorf("500 topics: p95 %v > 300ms", p95)
	}
	if body := rec.Body.String(); strings.Count(body, `class="topic"`) != 500 || !strings.Contains(body, "Topic 499") {
		t.Errorf("full list must show 500 topics")
	}

	req := httptest.NewRequest(http.MethodGet, "/topics?q=topic+42&status=bogus", nil)
	req.Header.Set("HX-Request", "true")
	frag := e.do(req).Body.String()
	// "topic 42" matches 42 and 420-429.
	if strings.Contains(frag, "<html") || !strings.Contains(frag, "<strong>11</strong> موضوع یافت شد") {
		t.Errorf("htmx search fragment wrong:\n%s", frag)
	}
}
