package httpserver_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
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

	"learninghub/internal/ai/fake"
	"learninghub/internal/config"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/httpserver"
	"learninghub/internal/jobs"
	"learninghub/internal/services/evaluation"
	"learninghub/internal/services/exercises"
	"learninghub/internal/services/lessons"
	"learninghub/internal/services/roadmaps"
	"learninghub/internal/services/topics"
	"learninghub/internal/store"
	"learninghub/skills"
)

type env struct {
	t      *testing.T
	h      http.Handler
	sm     *httpserver.SecurityManager
	db     *store.DB
	base   string
	root   *fsx.Root
	runner *jobs.Runner
	ad     *fake.Adapter
}

func newEnv(t *testing.T, opts ...func(*httpserver.Settings)) *env {
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
	if _, err := skills.Install(root); err != nil {
		t.Fatal(err)
	}
	ad := &fake.Adapter{Scenario: fake.OK}
	runner := jobs.New(db, 2)
	runner.Poll, runner.Heartbeat, runner.Files = 20*time.Millisecond, time.Hour, root
	runner.Register(domain.JobKindLesson, (&lessons.Service{DB: db, Root: root, Adapter: ad, Model: "m1",
		Language: "fa", Timeout: time.Minute, Stall: time.Minute, MaxSource: 204800}).Handle)
	exCfg := config.DefaultConfig(base).Exercises
	runner.Register(domain.JobKindExercises, (&exercises.Service{DB: db, Root: root, Adapter: ad, Model: "m1",
		Language: "fa", Timeout: time.Minute, Stall: time.Minute, Config: exCfg, MaxSource: 204800}).Handle)
	runner.Register(domain.JobKindEvaluate, (&evaluation.Service{DB: db, Root: root, Adapter: ad, Model: "m1",
		Language: "fa", Timeout: time.Minute, Stall: time.Minute, MaxAnswerBytes: 1024, MaxSource: 204800}).Handle)
	drafts := roadmaps.NewService(db, root)
	analyzer := &roadmaps.Analyzer{Drafts: drafts, Adapter: ad, Model: "m1", Timeout: time.Minute, Stall: time.Minute}
	runner.Register(domain.JobKindRoadmapAnalyze, analyzer.Handle)
	runner.Register(domain.JobKindChapterDetect, analyzer.DetectChapters)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	settings := httpserver.Settings{DB: db, Adapter: ad, Model: "m1", Exercises: exCfg, MaxAnswerBytes: 1024}
	for _, o := range opts {
		o(&settings)
	}
	// Most tests are past first run; TestFirstRunGate removes the ack.
	if _, err := db.Writer().Exec(`INSERT INTO meta (key, value) VALUES ('privacy_ack', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	srv := httpserver.NewServer("127.0.0.1:8787", sm, drafts, topics.NewService(db, root), runner, root, settings)
	if srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 {
		t.Error("server timeouts must be set (Section 8)")
	}
	return &env{t: t, h: srv.Handler, sm: sm, db: db, base: base, root: root, runner: runner, ad: ad}
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
	return e.uploadTo("", name, content)
}

// uploadTo posts a roadmap file; a non-empty roadmapID makes it a re-import.
func (e *env) uploadTo(roadmapID, name string, content []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("csrf_token", e.sm.CSRFToken())
	if roadmapID != "" {
		_ = mw.WriteField("roadmap_id", roadmapID)
	}
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(content)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/roadmaps/drafts", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	rec := e.do(req)
	if loc := rec.Header().Get("Location"); rec.Code == http.StatusSeeOther {
		e.waitDraftJob(loc)
	}
	return rec
}

// waitDraftJob waits for the chapter_detect queued on upload (§13.2), so confirm never races it.
func (e *env) waitDraftJob(draftURL string) {
	e.t.Helper()
	scope := "draft:" + strings.TrimPrefix(draftURL, "/roadmaps/drafts/")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var active int
		if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs WHERE scope_key = ? AND status IN ('queued','running')`, scope).Scan(&active); err != nil {
			e.t.Fatal(err)
		}
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("draft job on %s did not finish", scope)
		}
	}
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
	// T7.9: the Persian font is embedded locally (CSP forbids remote fonts) and app.css points at it.
	rec = e.do(httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if !strings.Contains(rec.Body.String(), "url(fonts/vazirmatn-arabic.woff2)") {
		t.Error("app.css does not load the local Vazirmatn font")
	}
	for _, f := range []string{"vazirmatn-arabic.woff2", "vazirmatn-latin.woff2"} {
		rec = e.do(httptest.NewRequest(http.MethodGet, "/static/fonts/"+f, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "font/woff2" || !strings.HasPrefix(rec.Body.String(), "wOF2") {
			t.Errorf("font %s: got %d %q", f, rec.Code, rec.Header().Get("Content-Type"))
		}
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

// confirm posts the confirm form; without an explicit no_ai field it chooses «ادامه بدون AI» (§13.3).
func (e *env) confirm(draftURL string, form url.Values) *httptest.ResponseRecorder {
	if _, ok := form["no_ai"]; !ok {
		form = maps.Clone(form)
		form.Set("no_ai", "1")
	}
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

// T7.2: the preview offers the first H1 as an editable title; the edit is what gets recorded,
// and a re-import replaces the title only when the box is ticked.
func TestConfirmImportTitle(t *testing.T) {
	e := newEnv(t)
	csrf := e.sm.CSRFToken()
	title := func() (s string) {
		t.Helper()
		if err := e.db.Writer().QueryRow("SELECT title FROM roadmaps WHERE id = 1").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	draftURL := e.upload("go.md", []byte("# آموزش Go\n## Basics\n")).Header().Get("Location")
	if page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String(); !strings.Contains(page, `name="title" value="آموزش Go"`) {
		t.Fatalf("preview must prefill the H1 title:\n%s", page)
	}
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}, "confirm": {"1"}, "title": {strings.Repeat("x", 201)}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("long title: want 422, got %d", rec.Code)
	}
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}, "confirm": {"1"}, "title": {"کتاب من"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body.String())
	}
	if got := title(); got != "کتاب من" {
		t.Fatalf("title = %q", got)
	}

	for _, c := range []struct {
		replace bool
		want    string
	}{{false, "کتاب من"}, {true, "Go 2"}} {
		draftURL = e.uploadTo("1", "go.md", []byte("# Go 2\n## Basics\n")).Header().Get("Location")
		form := url.Values{"csrf_token": {csrf}, "confirm": {"1"}, "title": {"Go 2"}}
		if c.replace {
			form.Set("replace_title", "1")
		}
		if rec := e.confirm(draftURL, form); rec.Code != http.StatusSeeOther {
			t.Fatalf("re-import confirm: %d %s", rec.Code, rec.Body.String())
		}
		if got := title(); got != c.want {
			t.Errorf("re-import replace=%v: title %q, want %q", c.replace, got, c.want)
		}
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
	if p95 := times[18]; !raceEnabled && p95 > 300*time.Millisecond {
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

func TestReimportFlow(t *testing.T) {
	e := newEnv(t)
	csrf := e.sm.CSRFToken()
	draftURL := e.upload("go.md", []byte("# Go\n## Basics\n### Types\n### Loops\n")).Header().Get("Location")
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("first import: %d", rec.Code)
	}
	form := e.do(httptest.NewRequest(http.MethodGet, "/roadmaps/new?roadmap_id=1", nil)).Body.String()
	if !strings.Contains(form, `<option value="1" selected>`) {
		t.Errorf("upload form must offer re-import of roadmap 1:\n%s", form)
	}
	if rec := e.uploadTo("99", "go.md", []byte("# Go\n")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown roadmap: want 422, got %d", rec.Code)
	}

	rec := e.uploadTo("1", "go.md", []byte("# Go\n## Basics\n### Types\n### Loop\n"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("re-import upload: %d %s", rec.Code, rec.Body.String())
	}
	draftURL = rec.Header().Get("Location")
	page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	for _, want := range []string{"ورود مجدد", "1 جدید، 0 تغییرکرده، 1 ناپدیدشده، 1 بدون تغییر", "شاید همان «<bdi>Loops</bdi>»"} {
		if !strings.Contains(page, want) {
			t.Errorf("preview must contain %q:\n%s", want, page)
		}
	}
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {csrf}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm re-import: %d %s", rec.Code, rec.Body.String())
	}
	var topics, archived int
	_ = e.db.Writer().QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE state = 'archived') FROM topics`).Scan(&topics, &archived)
	if topics != 3 || archived != 1 {
		t.Errorf("want 3 topics with 1 archived, got %d/%d", topics, archived)
	}
	if _, err := os.Stat(filepath.Join(e.base, "roadmaps", "go", "rev-002.md")); err != nil {
		t.Errorf("rev-002.md: %v", err)
	}
}
