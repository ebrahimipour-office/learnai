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
	"os/exec"
	"path/filepath"
	"regexp"
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
	"learninghub/internal/services/maintenance"
	"learninghub/internal/services/qa"
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
	runner.Register(domain.JobKindQA, (&qa.Service{DB: db, Root: root, Adapter: ad, Model: "m1",
		Language: "fa", Timeout: time.Minute, Stall: time.Minute, MaxSource: 204800}).Handle)
	drafts := roadmaps.NewService(db, root)
	analyzer := &roadmaps.Analyzer{Drafts: drafts, Adapter: ad, Model: "m1", Timeout: time.Minute, Stall: time.Minute}
	runner.Register(domain.JobKindRoadmapAnalyze, analyzer.Handle)
	runner.Register(domain.JobKindChapterDetect, analyzer.DetectChapters)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	settings := httpserver.Settings{DB: db, Adapter: ad, Model: "m1", Exercises: exCfg, MaxAnswerBytes: 1024,
		Maintenance: &maintenance.Service{DB: db, Root: root}}
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
	// CR-20: htmx errors land in #flash; a stale CSRF token reloads the page.
	rec = e.do(httptest.NewRequest(http.MethodGet, "/topics", nil))
	if !strings.Contains(rec.Body.String(), `<div id="flash" class="error" role="alert" hidden>`) {
		t.Error("layout lacks the #flash alert container")
	}
	rec = e.do(httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	for _, want := range []string{`"htmx:responseError"`, `"htmx:sendError"`, `"X-CSRF-Stale"`, "location.reload()", `"selection-change"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("app.js missing %s", want)
		}
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
	if rec.Code != http.StatusOK || !strings.Contains(page, "<strong>۳</strong> موضوع") || !strings.Contains(page, "Goroutines") {
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
	if rec.Code != http.StatusOK || strings.Contains(frag, "<html") || !strings.Contains(frag, "<strong>۲</strong> موضوع انتخاب‌شده از ۳") {
		t.Fatalf("htmx selection: want fragment with 2 of 3, got %d\n%s", rec.Code, frag)
	}

	// Switching rule resets ticks; level-2 gives 2 topics (Basics, Concurrency).
	rec = e.selection(draftURL, url.Values{"rule": {"level-2"}, "shown_rule": {"deepest"}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("non-htmx selection: want 303, got %d", rec.Code)
	}
	rec = e.do(httptest.NewRequest(http.MethodGet, draftURL, nil))
	if !strings.Contains(rec.Body.String(), "<strong>۲</strong> موضوع انتخاب‌شده از ۲") {
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
	if strings.Contains(frag, "<html") || !strings.Contains(frag, "<strong>۱۱</strong> موضوع یافت شد") {
		t.Errorf("htmx search fragment wrong:\n%s", frag)
	}
}

// T10.14: the tree shows a mini stepper (label for screen readers) instead of the status badge, groups carry
// a key, and app.js remembers closed groups in localStorage and opens/closes all of them from one button.
func TestTopicListDensity(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## Basics\n### Types\n### Loops\n")).Header().Get("Location")
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d", rec.Code)
	}
	body := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String()
	_, tree, _ := strings.Cut(body, `<ul class="tree">`)
	if strings.Contains(tree, "badge status-") || strings.Count(tree, `<span class="mini-steps" title="ایجادشده"><i></i><i></i><i></i><i></i><span class="sr-only">ایجادشده</span></span>`) != 2 {
		t.Errorf("tree must show an empty mini stepper with an sr-only label, no status badge:\n%s", tree)
	}
	if !strings.Contains(tree, `<details open data-group="`) || !strings.Contains(body, `data-toggle-groups>`) {
		t.Errorf("groups need data-group and the page an open/close-all button:\n%s", body)
	}
	appJS := e.do(httptest.NewRequest(http.MethodGet, "/static/app.js", nil)).Body.String()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on host; skipping node DOM test")
	}
	script := `
const code = process.argv[1];
function run(stored, blocked) {
  const listeners = {}, store = stored ? { closedGroups: stored } : {};
  const mk = (id) => { const d = { dataset: { group: id }, _open: true };
    Object.defineProperty(d, "open", { get() { return this._open; }, set(v) { this._open = v; listeners.toggle?.({ target: this }); } });
    return d; };
  const groups = [mk("1"), mk("2")];
  const btn = { closest: (s) => (s === "button[data-toggle-groups]" ? btn : null) };
  const document = {
    addEventListener(evt, fn) { listeners[evt] = fn; },
    querySelectorAll: (s) => (s === "details[data-group]" ? groups : []),
    body: { addEventListener() {} },
  };
  const localStorage = blocked ? { getItem() { throw 1; }, setItem() { throw 1; } }
    : { getItem: (k) => store[k] ?? null, setItem(k, v) { store[k] = v; } };
  new Function("document", "localStorage", code)(document, localStorage);
  return { groups, store, click: () => listeners.click({ target: btn }) };
}
(async () => {
  let r = run('["2"]');
  if (!r.groups[0].open || r.groups[1].open) process.exit(2); // restored
  r.groups[0].open = false; // user closes group 1
  if (JSON.stringify(JSON.parse(r.store.closedGroups).sort()) !== '["1","2"]') process.exit(3);
  await r.click(); // some closed → open all
  if (!r.groups.every((d) => d.open) || r.store.closedGroups !== "[]") process.exit(4);
  await r.click(); // all open → close all
  if (r.groups.some((d) => d.open)) process.exit(5);
  r = run("not json");
  if (!r.groups.every((d) => d.open)) process.exit(6);
  r = run(null, true); // storage blocked: toggling still works
  await r.click();
  if (r.groups.some((d) => d.open)) process.exit(7);
})().catch((e) => { console.error(e); process.exit(9); });
`
	if out, err := exec.Command(nodePath, "-e", script, appJS).CombinedOutput(); err != nil {
		t.Fatalf("node groups test failed (%v): %s", err, out)
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
	for _, want := range []string{"ورود مجدد", "۱ جدید، ۰ تغییرکرده، ۱ ناپدیدشده، ۱ بدون تغییر", "شاید همان «<bdi>Loops</bdi>»"} {
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

func TestFaviconReturns204(t *testing.T) {
	e := newEnv(t)
	rec := e.do(httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("GET /favicon.ico status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestCR45_TopicsHistoryRestoreReturnsFullPage(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## Basics\n### Types\n")).Header().Get("Location")
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d", rec.Code)
	}

	// 1. Regular browser navigation returns full page with <header and <html.
	reqPlain := httptest.NewRequest(http.MethodGet, "/topics?q=Types", nil)
	recPlain := e.do(reqPlain)
	if recPlain.Code != http.StatusOK {
		t.Fatalf("plain: want 200, got %d", recPlain.Code)
	}
	bodyPlain := recPlain.Body.String()
	if !strings.Contains(bodyPlain, "<header") || !strings.Contains(bodyPlain, "<html") {
		t.Fatalf("plain: expected full page with header, got:\n%s", bodyPlain)
	}

	// 2. htmx search request returns fragment without <header and without <html.
	reqHx := httptest.NewRequest(http.MethodGet, "/topics?q=Types", nil)
	reqHx.Header.Set("HX-Request", "true")
	recHx := e.do(reqHx)
	if recHx.Code != http.StatusOK {
		t.Fatalf("hx: want 200, got %d", recHx.Code)
	}
	bodyHx := recHx.Body.String()
	if strings.Contains(bodyHx, "<header") || strings.Contains(bodyHx, "<html") {
		t.Fatalf("hx search: expected fragment without header, got:\n%s", bodyHx)
	}
	if !strings.Contains(bodyHx, `<div id="topic-results">`) {
		t.Fatalf("hx search: expected #topic-results fragment, got:\n%s", bodyHx)
	}

	// 3. htmx history restore request (with both HX-Request and HX-History-Restore-Request)
	// MUST return full page containing <header and <html so body is not corrupted on history-cache miss.
	reqRestore := httptest.NewRequest(http.MethodGet, "/topics?q=Types", nil)
	reqRestore.Header.Set("HX-Request", "true")
	reqRestore.Header.Set("HX-History-Restore-Request", "true")
	recRestore := e.do(reqRestore)
	if recRestore.Code != http.StatusOK {
		t.Fatalf("restore: want 200, got %d", recRestore.Code)
	}
	bodyRestore := recRestore.Body.String()
	if !strings.Contains(bodyRestore, "<header") || !strings.Contains(bodyRestore, "<html") {
		t.Fatalf("restore: expected full page with header, got:\n%s", bodyRestore)
	}
	if !strings.Contains(bodyRestore, `<div id="topic-results">`) {
		t.Fatalf("restore: expected #topic-results in full page, got:\n%s", bodyRestore)
	}
}

// T10.16: a search result links to the topic with ?q=; app.js marks the matches in text nodes and adds shortcuts.
func TestSearchHighlightAndShortcuts(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## Basics\n### Types\n")).Header().Get("Location")
	if rec := e.confirm(draftURL, url.Values{"csrf_token": {e.sm.CSRFToken()}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm: %d", rec.Code)
	}
	body := e.do(httptest.NewRequest(http.MethodGet, "/topics?q=Types", nil)).Body.String()
	if !regexp.MustCompile(`<a href="/topics/\d+\?q=Types">`).MatchString(body) {
		t.Errorf("search result must link with ?q=:\n%s", body)
	}
	if body := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String(); strings.Contains(body, "?q=") {
		t.Errorf("no search ⇒ plain links:\n%s", body)
	}
	if !strings.Contains(body, `<dialog id="shortcuts"`) {
		t.Error("layout must carry the shortcuts dialog")
	}
	appJS := e.do(httptest.NewRequest(http.MethodGet, "/static/app.js", nil)).Body.String()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on host; skipping node DOM test")
	}
	script := `
const code = process.argv[1];
class Node_ { constructor() { this.parent = null; } replaceWith(n) { const p = this.parent, i = p.kids.indexOf(this); p.kids[i] = n; n.parent = p; } }
class Text extends Node_ {
  constructor(v) { super(); this.nodeValue = v; }
  splitText(i) { const t = new Text(this.nodeValue.slice(i)); this.nodeValue = this.nodeValue.slice(0, i);
    const p = this.parent; p.kids.splice(p.kids.indexOf(this) + 1, 0, t); t.parent = p; return t; }
}
class El extends Node_ {
  constructor(tag, kids = [], attrs = {}) { super(); this.tag = tag; this.attrs = attrs; this.kids = []; for (const k of kids) this.add(k); }
  add(k) { k = typeof k === "string" ? new Text(k) : k; k.parent = this; this.kids.push(k); }
  set textContent(v) { this.kids = []; this.add(v); }
  html() { return "<" + this.tag + Object.entries(this.attrs).map(([k, v]) => " " + k + '="' + v + '"').join("") + ">" +
    this.kids.map((k) => (k instanceof Text ? k.nodeValue : k.html())).join("") + "</" + this.tag + ">"; }
}
function texts(n, out = []) { for (const k of n.kids) k instanceof Text ? out.push(k) : texts(k, out); return out; }
function run(search, article) {
  const listeners = {};
  let scrolled = null, focused = false, clicked = null, modal = false, href = null;
  const links = { 'a[rel="prev"]': { click() { clicked = "prev"; } }, 'a[rel="next"]': { click() { clicked = "next"; } } };
  const document = {
    addEventListener(evt, fn) { listeners[evt] = fn; },
    querySelectorAll: (s) => (s === "article.md" && article ? [article] : []),
    querySelector: (s) => (s === "input[type=search]" ? (search.input ? { focus() { focused = true; } } : null) : links[s]),
    getElementById: (id) => (id === "shortcuts" ? { showModal() { modal = true; } } : null),
    createElement: (tag) => { const m = new El(tag); m.scrollIntoView = () => { scrolled = m; }; return m; },
    createTreeWalker: (root) => { const all = texts(root); let i = 0; return { nextNode: () => all[i++] || null }; },
    body: { addEventListener() {} },
  };
  const location = { search: search.q, set href(v) { href = v; } };
  new Function("document", "location", "NodeFilter", code)(document, location, { SHOW_TEXT: 4 });
  const key = (code, extra = {}) => { let prevented = false;
    listeners.keydown({ code, shiftKey: false, target: { closest: () => extra.inField || null }, preventDefault() { prevented = true; }, ...extra });
    return prevented; };
  return { key, get: () => ({ scrolled, focused, clicked, modal, href }) };
}
const mk = () => new El("article", [new El("p", ["Use a WaitGroup; waitgroup.Wait() blocks."]), new El("pre", [new El("code", ["var wg sync.WaitGroup"], { class: "WaitGroup" })])]);
let a = mk(), r = run({ q: "?q=WaitGroup" }, a);
const want = '<article><p>Use a <mark>WaitGroup</mark>; <mark>waitgroup</mark>.Wait() blocks.</p><pre><code class="WaitGroup">var wg sync.<mark>WaitGroup</mark></code></pre></article>';
if (a.html() !== want) { console.error(a.html()); process.exit(2); }
if (r.get().scrolled?.tag !== "mark" || r.get().scrolled.kids[0].nodeValue !== "WaitGroup") process.exit(3);
for (const q of ["", "?q=", "?q=%20%20", "?q=nomatch"]) { a = mk(); r = run({ q }, a); if (a.html() !== mk().html() || r.get().scrolled) process.exit(4); }
r = run({ q: "", input: true });
if (!r.key("Slash") || !r.get().focused) process.exit(5);
r = run({ q: "" });
r.key("Slash"); if (r.get().href !== "/topics") process.exit(6);
r.key("BracketLeft"); if (r.get().clicked !== "prev") process.exit(7);
r.key("BracketRight"); if (r.get().clicked !== "next") process.exit(8);
r.key("Slash", { shiftKey: true }); if (!r.get().modal) process.exit(9);
r = run({ q: "", input: true });
if (r.key("Slash", { inField: {} }) || r.get().focused) process.exit(10); // typing in a field: no shortcut
if (r.key("BracketLeft", { ctrlKey: true }) || r.get().clicked) process.exit(11);
if (r.key("KeyA")) process.exit(12);
`
	if out, err := exec.Command(nodePath, "-e", script, appJS).CombinedOutput(); err != nil {
		t.Fatalf("node highlight/shortcut test failed (%v): %s", err, out)
	}
}

func TestCR46_DraftFormSelectionChangeTrigger(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## Part 1\nLine 3\n## Part 2\nLine 5\n## Part 3\nLine 7\n")).Header().Get("Location")

	// 1. GET draft page: form must use hx-trigger="selection-change", not "change"
	page := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(page, `hx-trigger="selection-change"`) {
		t.Fatalf("expected hx-trigger=\"selection-change\" on draft form, got:\n%s", page)
	}
	if strings.Contains(page, `hx-trigger="change"`) {
		t.Fatalf("found unwanted hx-trigger=\"change\" on draft form:\n%s", page)
	}

	// 2. Switch to tree rule to inspect chapter rows
	rec := e.selection(draftURL, url.Values{"rule": {"tree"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("switch to tree: want 200, got %d", rec.Code)
	}
	treeFrag := rec.Body.String()
	if !strings.Contains(treeFrag, `hx-trigger="selection-change"`) {
		t.Fatalf("fragment expected hx-trigger=\"selection-change\", got:\n%s", treeFrag)
	}
	if !strings.Contains(treeFrag, `id="ttl-1"`) || !strings.Contains(treeFrag, `id="ttl-2"`) {
		t.Fatalf("expected stable input ids ttl-1, ttl-2, got:\n%s", treeFrag)
	}

	// 3. app.js checks: delegated listener handles selection-change for select and checkboxes, ignoring text inputs
	appJS := e.do(httptest.NewRequest(http.MethodGet, "/static/app.js", nil)).Body.String()
	for _, expected := range []string{
		`closest("#draft-form")`,
		`matches("select, input[type='checkbox']")`,
		`htmx.trigger(form, "selection-change")`,
	} {
		if !strings.Contains(appJS, expected) {
			t.Errorf("app.js missing expected pattern %q", expected)
		}
	}

	// 4. Title edit + merge via form submission:
	// Set chapters rule with 3 chapters
	e.selection(draftURL, url.Values{
		"rule":       {"chapters"},
		"shown_rule": {"chapters"},
		"ch_line":    {"2", "4", "6"},
		"ch_end":     {"3", "5", "7"},
		"ch_title":   {"Chapter One", "Chapter Two", "Chapter Three"},
		"include":    {"k1", "k2", "k3"},
	}, true)

	// User edits title of Chapter 1 and clicks merge on Chapter 1 (merging 1 and 2)
	mergeReq := url.Values{
		"rule":       {"chapters"},
		"shown_rule": {"chapters"},
		"ch_line":    {"2", "4", "6"},
		"ch_end":     {"3", "5", "7"},
		"ch_title":   {"Edited Chapter One", "Chapter Two", "Chapter Three"},
		"merge":      {"1"},
	}
	recMerge := e.selection(draftURL, mergeReq, false)
	if recMerge.Code != http.StatusSeeOther {
		t.Fatalf("merge submit: want 303, got %d", recMerge.Code)
	}

	// Draft re-renders with N-1 (2) chapters and the edited title is preserved
	pageAfterMerge := e.do(httptest.NewRequest(http.MethodGet, draftURL, nil)).Body.String()
	if !strings.Contains(pageAfterMerge, `value="Edited Chapter One"`) {
		t.Errorf("expected edited chapter title to be preserved after merge, got:\n%s", pageAfterMerge)
	}
	if strings.Contains(pageAfterMerge, `value="Chapter Two"`) {
		t.Errorf("expected Chapter Two to be absorbed in merge, got:\n%s", pageAfterMerge)
	}
	if !strings.Contains(pageAfterMerge, `value="Chapter Three"`) {
		t.Errorf("expected Chapter Three to remain, got:\n%s", pageAfterMerge)
	}
}

func TestCR47_CopyButtonRestoresOriginalLabel(t *testing.T) {
	e := newEnv(t)

	// 1. Verify app.js implementation for CR-47
	appJS := e.do(httptest.NewRequest(http.MethodGet, "/static/app.js", nil)).Body.String()
	for _, expected := range []string{
		`btn.dataset.label ??= btn.textContent;`,
		`clearTimeout(btn._copyTimeout);`,
		`btn.textContent = btn.dataset.label;`,
	} {
		if !strings.Contains(appJS, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}

	// 2. If node is available on host, run DOM regression test for rapid clicks
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on host; skipping node DOM test")
	}

	script := `
const fs = require("fs");
const code = process.argv[1];

let listeners = {};
const document = {
  addEventListener(evt, fn) { listeners[evt] = fn; },
  querySelectorAll() { return []; },
  body: { addEventListener() {} }
};
const navigator = {
  clipboard: {
    async writeText() { return Promise.resolve(); }
  }
};
let timeouts = [];
let nextId = 1;
function customSetTimeout(cb, delay) {
  const id = nextId++;
  timeouts.push({ id, cb, delay });
  return id;
}
function customClearTimeout(id) {
  timeouts = timeouts.filter(t => t.id !== id);
}

const fn = new Function("document", "navigator", "clearTimeout", "setTimeout", code);
fn(document, navigator, customClearTimeout, customSetTimeout);

async function run() {
  const btn = {
    dataset: { copy: "/path/to/ex01.go" },
    textContent: "کپی مسیر",
    parentElement: null,
    closest(sel) { return sel.includes("button[data-copy]") ? this : null; }
  };
  const ev = { target: btn };

  // First click: text becomes "کپی شد", label stored as "کپی مسیر"
  await listeners["click"](ev);
  if (btn.textContent !== "کپی شد" || btn.dataset.label !== "کپی مسیر") {
    process.exit(2);
  }

  // Second rapid click (e.g. 500ms later while still "کپی شد"):
  // dataset.label MUST NOT be overwritten with "کپی شد"
  await listeners["click"](ev);
  if (btn.textContent !== "کپی شد" || btn.dataset.label !== "کپی مسیر") {
    process.exit(3);
  }

  // Timeout fires 1.5s after the last click
  if (timeouts.length !== 1) process.exit(4);
  timeouts[0].cb();

  // textContent must be restored to original "کپی مسیر"
  if (btn.textContent !== "کپی مسیر") {
    process.exit(5);
  }
  process.exit(0);
}
run();
`
	cmd := exec.Command(nodePath, "-e", script, appJS)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node DOM regression test failed (exit code %v): %s", err, string(out))
	}
}

// T9.11: each rendered code block gets one copy button that copies the code, also after an htmx swap.
func TestCodeBlockCopyButton(t *testing.T) {
	e := newEnv(t)
	appJS := e.do(httptest.NewRequest(http.MethodGet, "/static/app.js", nil)).Body.String()
	if !strings.Contains(appJS, `querySelectorAll(".md pre")`) || !strings.Contains(appJS, "htmx:afterSwap") {
		t.Fatal("app.js must add code copy buttons on load and after htmx swaps")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on host; skipping node DOM test")
	}
	script := `
const code = process.argv[1];
const listeners = {};
let swap;
let copied = null;
const codeEl = { textContent: 'fmt.Println("hi")\n' };
const pre = {
  previousElementSibling: null,
  querySelector(sel) { return sel === "code" ? codeEl : null; },
  before(btn) { this.previousElementSibling = btn; btn.nextElementSibling = this; created.push(btn); },
};
const created = [];
function makeButton() {
  const classes = new Set();
  return {
    dataset: {}, attrs: {},
    set className(v) { classes.add(v); },
    classList: { contains: (c) => classes.has(c) },
    setAttribute(k, v) { this.attrs[k] = v; },
    closest(sel) { return sel.includes("button.copy-code") && classes.has("copy-code") ? this : null; },
  };
}
const document = {
  addEventListener(evt, fn) { listeners[evt] = fn; },
  querySelectorAll(sel) { return sel === ".md pre" ? [pre] : []; },
  createElement(tag) { if (tag !== "button") process.exit(10); return makeButton(); },
  body: { addEventListener(evt, fn) { if (evt === "htmx:afterSwap") swap = fn; } },
};
const navigator = { clipboard: { async writeText(s) { copied = s; } } };
const fn = new Function("document", "navigator", "clearTimeout", "setTimeout", "getSelection", code);
fn(document, navigator, () => {}, () => 1, () => ({}));

(async () => {
  if (created.length !== 1 || created[0].textContent !== "کپی" || created[0].type !== "button") process.exit(2);
  swap({ detail: { target: document } }); // a swap must not add a second button
  if (created.length !== 1) process.exit(3);
  await listeners["click"]({ target: created[0] });
  if (copied !== codeEl.textContent || created[0].textContent !== "کپی شد") process.exit(4);
  process.exit(0);
})();
`
	if out, err := exec.Command(nodePath, "-e", script, appJS).CombinedOutput(); err != nil {
		t.Fatalf("node DOM test failed (%v): %s", err, out)
	}
}

// T9.14: a header button picks auto/light/dark; theme.js runs in <head> (no flash) and remembers the choice.
func TestThemeToggle(t *testing.T) {
	e := newEnv(t)
	page := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)).Body.String() // full page, not an htmx fragment
	head, _, _ := strings.Cut(page, "</head>")
	if !strings.Contains(head, `<script src="/static/theme.js"></script>`) || !strings.Contains(page, `<button type="button" class="theme-toggle" data-theme-toggle`) {
		t.Fatal("layout must load theme.js in <head> without defer and render the theme button")
	}
	// T10.3: skip link first in <body>, targeting a focusable <main>; the theme button announces its new state.
	if _, body, _ := strings.Cut(page, "<body"); !strings.Contains(body, `<a class="skip" href="#main">`) || !strings.Contains(page, `<main id="main" tabindex="-1">`) || !strings.Contains(page, `aria-live="polite">تم:`) {
		t.Error("layout must render the skip link, main#main and a live theme button")
	}
	css := e.do(httptest.NewRequest(http.MethodGet, "/static/app.css", nil)).Body.String()
	for _, want := range []string{`:root:not([data-theme="light"])`, `:root[data-theme="dark"]`, `:root[data-theme="light"]`} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing %s", want)
		}
	}
	rec := e.do(httptest.NewRequest(http.MethodGet, "/static/theme.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("theme.js: %d", rec.Code)
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on host; skipping node DOM test")
	}
	script := `
const code = process.argv[1];
function run(stored, blocked) {
  const attrs = {}, store = stored ? { theme: stored } : {}, listeners = {};
  const btn = { textContent: "", closest: (s) => (s === "button[data-theme-toggle]" ? btn : null) };
  const document = {
    documentElement: { setAttribute(k, v) { attrs[k] = v; }, removeAttribute(k) { delete attrs[k]; } },
    querySelector: (s) => (s === "button[data-theme-toggle]" ? btn : null),
    addEventListener(evt, fn) { listeners[evt] = fn; },
  };
  const localStorage = blocked ? { getItem() { throw 1; }, setItem() { throw 1; }, removeItem() { throw 1; } }
    : { getItem: (k) => store[k] ?? null, setItem(k, v) { store[k] = v; }, removeItem(k) { delete store[k]; } };
  new Function("document", "localStorage", code)(document, localStorage);
  return { attrs, store, btn, click: () => listeners.click({ target: btn }) };
}
let r = run("dark");
if (r.attrs["data-theme"] !== "dark" || r.btn.textContent !== "تم: تاریک") process.exit(2);
r.click(); // dark → auto
if ("data-theme" in r.attrs || "theme" in r.store || r.btn.textContent !== "تم: خودکار") process.exit(3);
r.click(); // auto → light
if (r.attrs["data-theme"] !== "light" || r.store.theme !== "light") process.exit(4);
r = run("bogus");
if ("data-theme" in r.attrs) process.exit(5);
r = run(null, true); // storage blocked: still works for this page
r.click();
if (r.attrs["data-theme"] !== "light") process.exit(6);
`
	if out, err := exec.Command(nodePath, "-e", script, rec.Body.String()).CombinedOutput(); err != nil {
		t.Fatalf("node theme test failed (%v): %s", err, out)
	}
}
