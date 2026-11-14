package httpserver_test

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
	"learninghub/internal/httpserver"
	"learninghub/skills"
)

func (e *env) settingsPage(rerun bool) string {
	e.t.Helper()
	if rerun {
		if rec := e.post("/settings/doctor", nil, false); rec.Code != http.StatusSeeOther {
			e.t.Fatalf("rerun: %d", rec.Code)
		}
	}
	rec := e.get("/settings")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	return html.UnescapeString(rec.Body.String())
}

func TestSettingsDoctor(t *testing.T) {
	e := newEnv(t)
	// CR-48: viewing the page (here a cross-site navigation) never runs Probe.
	e.ad.ProbeErr = fmt.Errorf("%w: agy", ai.ErrBinaryNotFound)
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Dest", "document")
	if rec := e.do(req); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "آماده است") ||
		strings.Contains(html.UnescapeString(rec.Body.String()), ai.Message("adapter_missing")) || !strings.Contains(rec.Body.String(), "هنوز بررسی نشده") {
		t.Fatalf("GET /settings probed or hid the run button: %d\n%s", rec.Code, rec.Body.String())
	}
	e.ad.ProbeErr = nil
	if b := e.settingsPage(true); !strings.Contains(b, "آماده است") || !strings.Contains(b, "fake") {
		t.Fatalf("ready probe not shown:\n%s", b)
	}

	e.ad.ProbeErr = fmt.Errorf("%w: agy", ai.ErrBinaryNotFound)
	if b := e.settingsPage(false); !strings.Contains(b, "آماده است") {
		t.Fatal("result must be cached until rerun")
	}
	if b := e.settingsPage(true); !strings.Contains(b, ai.Message("adapter_missing")) || strings.Contains(b, "آماده است") {
		t.Fatalf("adapter_missing hint missing:\n%s", b)
	}

	e.ad.ProbeErr = nil
	e.ad.Caps = &ai.Capabilities{Version: "1.4.12", Notes: []string{"Eligibility check failed"}}
	if b := e.settingsPage(true); !strings.Contains(b, ai.Message("auth_required")) || !strings.Contains(b, "1.4.12") ||
		!strings.Contains(b, "Eligibility check failed") {
		t.Fatalf("auth_required hint missing:\n%s", b)
	}

	e.ad.Caps = &ai.Capabilities{Authenticated: true, Models: []string{"m2"}}
	if b := e.settingsPage(true); !strings.Contains(b, "در فهرست `agy models` نیست") {
		t.Fatalf("unknown model not flagged:\n%s", b)
	}
	e.ad.Caps.Models = []string{"m1", "m2"}
	if b := e.settingsPage(true); strings.Contains(b, "agy models` نیست") || !strings.Contains(b, "آماده است") {
		t.Fatalf("listed model flagged:\n%s", b)
	}
}

func TestSettingsPrivacyAck(t *testing.T) {
	e := newEnv(t)
	e.unack()
	if b := e.settingsPage(false); !strings.Contains(b, httpserver.PrivacyNotice) || !strings.Contains(b, `name="ack"`) {
		t.Fatalf("privacy notice/form missing:\n%s", b)
	}
	if rec := e.post("/settings/privacy", url.Values{}, false); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing ack: %d", rec.Code)
	}
	if rec := e.post("/settings/privacy", url.Values{"ack": {"1"}}, false); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/topics" {
		t.Fatalf("ack: %d", rec.Code)
	}
	var v string
	if err := e.db.Writer().QueryRow(`SELECT value FROM meta WHERE key = 'privacy_ack'`).Scan(&v); err != nil || v == "" {
		t.Fatalf("privacy_ack not stored: %q %v", v, err)
	}
	if b := e.settingsPage(false); !strings.Contains(b, "تأییدشده") || strings.Contains(b, `name="ack"`) {
		t.Fatalf("ack not shown:\n%s", b)
	}
	// Localized date format YYYY-MM-DD HH:MM:SS (CR-61)
	if !regexp.MustCompile(`تأییدشده در <bdi>\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}</bdi>`).MatchString(e.settingsPage(false)) {
		t.Errorf("expected localized date in privacy ack, got:\n%s", e.settingsPage(false))
	}
}

func TestJobCardPersianError(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Scenario = "broken" // fake returns ErrToolFailed
	if rec := e.post("/topics/1/jobs/lesson", nil, true); rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d", rec.Code)
	}
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	b := html.UnescapeString(e.get("/topics/1").Body.String())
	if !strings.Contains(b, ai.Message("tool_error")) || !strings.Contains(b, "جزئیات فنی") || !strings.Contains(b, "unknown scenario") {
		t.Fatalf("persian error/details missing:\n%s", b)
	}
}

func (e *env) unack() {
	e.t.Helper()
	if _, err := e.db.Writer().Exec(`DELETE FROM meta WHERE key = 'privacy_ack'`); err != nil {
		e.t.Fatal(err)
	}
}

// TestFirstRunGate: before the privacy ack the topic list and every AI action lead to settings,
// and nothing is queued (T6.7).
func TestFirstRunGate(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.unack()
	req := httptest.NewRequest(http.MethodGet, "/topics", nil)
	if rec := e.do(req); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
		t.Fatalf("GET /topics before ack: %d %v", rec.Code, rec.Header())
	}
	if b := e.settingsPage(false); !strings.Contains(b, "راه‌اندازی اولیه") {
		t.Error("settings page must explain first run")
	}
	for _, p := range []string{"/topics/1/jobs/lesson", "/topics/1/jobs/exercises", "/topics/1/jobs/evaluate", "/jobs/1/retry", "/roadmaps/drafts/1/ai-assist"} {
		if rec := e.post(p, nil, true); rec.Code != http.StatusConflict || rec.Header().Get("HX-Redirect") != "/settings" {
			t.Errorf("%s (htmx) before ack: %d %v", p, rec.Code, rec.Header())
		}
		if rec := e.post(p, nil, false); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings" {
			t.Errorf("%s (form) before ack: %d", p, rec.Code)
		}
	}
	var n int
	if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("jobs queued before ack: %d %v", n, err)
	}
	if rec := e.post("/settings/privacy", url.Values{"ack": {"1"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("ack: %d", rec.Code)
	}
	if rec := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)); rec.Code != http.StatusOK {
		t.Errorf("GET /topics after ack: %d", rec.Code)
	}
}

// named gives a fake adapter its own name so a switch can hold two of them.
type named struct {
	ai.Adapter
	n string
}

func (a named) Name() string { return a.n }

func TestSettingsSelectAdapter(t *testing.T) {
	sw := ai.NewSwitch("claude", named{&fake.Adapter{}, "antigravity"}, named{&fake.Adapter{}, "claude"}, named{&fake.Adapter{}, "codex"})
	e := newEnv(t, func(s *httpserver.Settings) { s.Adapter, s.Switch = sw, sw })
	if b := e.settingsPage(false); !strings.Contains(b, `<option value="claude" selected>`) {
		t.Fatalf("current tool not selected:\n%s", b)
	}
	if rec := e.post("/settings/adapter", url.Values{"adapter": {"nope"}}, false); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown tool: %d", rec.Code)
	}

	// A queued job blocks the switch: it would read the name and Run of different tools.
	if _, err := e.db.Writer().Exec(`INSERT INTO jobs (kind, scope_key, status, params) VALUES ('lesson', 'x', 'queued', '{}')`); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/settings/adapter", url.Values{"adapter": {"antigravity"}}, false); rec.Code != http.StatusConflict || sw.Name() != "claude" {
		t.Fatalf("switch while busy: %d %s", rec.Code, sw.Name())
	}
	if _, err := e.db.Writer().Exec(`DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}

	if rec := e.post("/settings/adapter", url.Values{"adapter": {"antigravity"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("select: %d %s", rec.Code, rec.Body.String())
	}
	if sw.Name() != "antigravity" || httpserver.SavedAdapter(context.Background(), e.db, "claude") != "antigravity" {
		t.Fatalf("not switched/saved: %s", sw.Name())
	}
	b := e.settingsPage(false)
	if !strings.Contains(b, `<option value="antigravity" selected>`) {
		t.Fatalf("new tool not shown:\n%s", b)
	}
	if !strings.Contains(b, `<form method="post" action="/settings/adapter" class="adapter-form">`) {
		t.Errorf("expected class adapter-form on adapter form, got:\n%s", b)
	}
	if !strings.Contains(b, `<bdi>config.toml</bdi>`) {
		t.Errorf("expected <bdi>config.toml</bdi>, got:\n%s", b)
	}
	if strings.Contains(b, "ساندباکس Codex") {
		t.Errorf("antigravity must not show codex warning:\n%s", b)
	}

	// Switching to codex should display the residual sandbox risk warning (CR-11)
	if rec := e.post("/settings/adapter", url.Values{"adapter": {"codex"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("select codex: %d %s", rec.Code, rec.Body.String())
	}
	b = e.settingsPage(false)
	if !strings.Contains(b, "هشدار امنیتی") || !strings.Contains(b, "ساندباکس Codex") {
		t.Errorf("expected codex security warning banner on settings page, got:\n%s", b)
	}
}

// T8.4: an installed skill older than the embedded one is offered for upgrade, never replaced silently.
func TestSettingsSkillUpgrade(t *testing.T) {
	e := newEnv(t)
	rel := skills.SkillRel("chapter-detector")
	old := []byte("---\nname: chapter-detector\nversion: 1.0.0\n---\nmine\n")
	if err := e.root.WriteAtomic(rel, old, 0o644); err != nil {
		t.Fatal(err)
	}
	page := e.settingsPage(false)
	if !strings.Contains(page, "نسخه جدید مهارت") || !strings.Contains(page, `action="/settings/skills/chapter-detector/upgrade"`) {
		t.Fatalf("no upgrade offer\n%s", page)
	}
	if !strings.Contains(page, `<span dir="ltr"><strong><bdi>chapter-detector</bdi></strong>: 1.0.0 ← 2.0.0</span>`) {
		t.Errorf("BiDi isolated skill update missing in page:\n%s", page)
	}
	if b, _ := e.root.ReadFile(rel); string(b) != string(old) {
		t.Fatal("viewing settings changed the skill")
	}
	if rec := e.post("/settings/skills/lesson-generator/upgrade", nil, false); rec.Code != http.StatusNotFound {
		t.Errorf("up-to-date skill: want 404, got %d", rec.Code)
	}
	if rec := e.post("/settings/skills/chapter-detector/upgrade", nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("upgrade: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := e.root.ReadFile(".agent/chapter-detector/SKILL.1.0.0.bak.md"); string(b) != string(old) {
		t.Errorf("backup = %q", b)
	}
	if strings.Contains(e.settingsPage(false), "نسخه جدید مهارت") {
		t.Error("offer still shown after upgrade")
	}
}
