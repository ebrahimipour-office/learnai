package httpserver_test

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
	"learninghub/internal/domain"
	"learninghub/internal/httpserver"
	"learninghub/internal/jobs"
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
	// T13.2: the page says where the data and the database live.
	if b := e.settingsPage(false); !strings.Contains(b, e.root.BaseDir()) || !strings.Contains(b, filepath.Join(e.root.BaseDir(), "app.db")) {
		t.Fatalf("base_dir / app.db path not shown:\n%s", b)
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
	// Local time in Persian digits through localTime (CR-61, O7)
	if !regexp.MustCompile(`تأییدشده در <bdi>[۰-۹]{4}-[۰-۹]{2}-[۰-۹]{2} [۰-۹]{2}:[۰-۹]{2}</bdi>`).MatchString(e.settingsPage(false)) {
		t.Errorf("expected localized date in privacy ack, got:\n%s", e.settingsPage(false))
	}

	// O7: the calendar option switches every date to Jalali; anything else is rejected.
	if rec := e.post("/settings/calendar", url.Values{"calendar": {"julian"}}, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid calendar: %d", rec.Code)
	}
	if rec := e.post("/settings/calendar", url.Values{"calendar": {"jalali"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("select jalali: %d", rec.Code)
	}
	// T12.1: backup dates follow the option too (they used to stay Gregorian).
	if err := e.root.MkdirAll(".app/backup", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.root.WriteNew(".app/backup/app-2026-11-22.db", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := e.settingsPage(false)
	if !regexp.MustCompile(`تأییدشده در <bdi>۱۴[۰-۹]{2}/[۰-۹]{2}/[۰-۹]{2} [۰-۹]{2}:[۰-۹]{2}</bdi>`).MatchString(b) || !strings.Contains(b, `value="jalali" selected`) {
		t.Errorf("expected Jalali date after switching, got:\n%s", b)
	}
	if !regexp.MustCompile(`data-label="تاریخ"><bdi>۱۴[۰-۹]{2}/[۰-۹]{2}/[۰-۹]{2} [۰-۹]{2}:[۰-۹]{2}</bdi>`).MatchString(b) {
		t.Errorf("backup date must be Jalali too:\n%s", b)
	}
	var cal string
	if err := e.db.Writer().QueryRow(`SELECT value FROM meta WHERE key = 'calendar'`).Scan(&cal); err != nil || cal != "jalali" {
		t.Errorf("calendar not stored: %q %v", cal, err)
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
	if rec := e.do(req); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?need_ack=1" {
		t.Fatalf("GET /topics before ack: %d %v", rec.Code, rec.Header())
	}
	if b := e.settingsPage(false); !strings.Contains(b, "راه‌اندازی اولیه") {
		t.Error("settings page must explain first run")
	}
	for _, p := range []string{"/topics/1/jobs/lesson", "/topics/1/jobs/exercises", "/topics/1/jobs/evaluate", "/jobs/1/retry", "/roadmaps/drafts/1/ai-assist"} {
		if rec := e.post(p, nil, true); rec.Code != http.StatusConflict || rec.Header().Get("HX-Redirect") != "/settings?need_ack=1" {
			t.Errorf("%s (htmx) before ack: %d %v", p, rec.Code, rec.Header())
		}
		if rec := e.post(p, nil, false); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?need_ack=1" {
			t.Errorf("%s (form) before ack: %d", p, rec.Code)
		}
	}
	var n int
	if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("jobs queued before ack: %d %v", n, err)
	}
	const needAck = "برای دیدن موضوعات و استفاده از هوش مصنوعی ابتدا این متن را تأیید کنید."
	if b := html.UnescapeString(e.get("/settings?need_ack=1").Body.String()); !strings.Contains(b, needAck) || !strings.Contains(b, "autofocus") {
		t.Error("gate redirect target must say why the user landed on settings")
	}
	if strings.Contains(e.settingsPage(false), needAck) {
		t.Error("plain /settings must not show the gate message")
	}
	if rec := e.post("/settings/privacy", url.Values{"ack": {"1"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("ack: %d", rec.Code)
	}
	if rec := e.do(httptest.NewRequest(http.MethodGet, "/topics", nil)); rec.Code != http.StatusOK {
		t.Errorf("GET /topics after ack: %d", rec.Code)
	}
	if strings.Contains(html.UnescapeString(e.get("/settings?need_ack=1").Body.String()), needAck) {
		t.Error("gate message must disappear after ack")
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

// CR-42: POST /settings/adapter must be atomic with respect to Enqueue under -race.
func TestCR42_SettingsAdapterSwitchConcurrentWithEnqueue(t *testing.T) {
	sw := ai.NewSwitch("antigravity", named{&fake.Adapter{}, "antigravity"}, named{&fake.Adapter{}, "claude"})
	e := newEnv(t, func(s *httpserver.Settings) { s.Adapter, s.Switch = sw, sw })

	for i := 0; i < 10; i++ {
		_ = sw.Set("antigravity")
		var wg sync.WaitGroup
		barrier := make(chan struct{})

		var rec *httptest.ResponseRecorder
		var jobID int64
		var enqueueErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-barrier
			rec = e.post("/settings/adapter", url.Values{"adapter": {"claude"}}, false)
		}()

		go func() {
			defer wg.Done()
			<-barrier
			jobID, enqueueErr = e.runner.Enqueue(context.Background(), jobs.Job{
				Kind:  domain.JobKindLesson,
				Scope: fmt.Sprintf("topic:%d", i+1),
			})
		}()

		close(barrier)
		wg.Wait()

		if enqueueErr != nil {
			t.Fatalf("enqueue error: %v", enqueueErr)
		}

		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusConflict {
			t.Fatalf("unexpected switch status code: %d", rec.Code)
		}

		if rec.Code == http.StatusConflict {
			if sw.Name() != "antigravity" {
				t.Fatalf("switch was refused but active adapter changed to %s", sw.Name())
			}
		} else {
			if sw.Name() != "claude" {
				t.Fatalf("switch succeeded but active adapter is %s", sw.Name())
			}
		}

		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var status string
			_ = e.db.Writer().QueryRow(`SELECT status FROM jobs WHERE id = ?`, jobID).Scan(&status)
			if status != "queued" && status != "running" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := e.db.Writer().Exec(`DELETE FROM jobs WHERE id = ?`, jobID); err != nil {
			t.Fatal(err)
		}
	}
}

type stubAdapterCR43 struct {
	name  string
	model string
	caps  ai.Capabilities
}

func (s *stubAdapterCR43) Name() string                                   { return s.name }
func (s *stubAdapterCR43) Model() string                                  { return s.model }
func (s *stubAdapterCR43) Probe(context.Context) (ai.Capabilities, error) { return s.caps, nil }
func (s *stubAdapterCR43) Run(context.Context, ai.Request, chan<- ai.Event) (*ai.Response, error) {
	return nil, nil
}

// CR-43: ProbeAdapter must format the model warning using the adapter's own CLI and TOML section.
func TestCR43_ProbeAdapterModelWarningMessages(t *testing.T) {
	tests := []struct {
		adapterName string
		model       string
		models      []string
		wantWarning string
		wantTomlKey string
	}{
		{
			adapterName: "antigravity",
			model:       "unlisted-gemini",
			models:      []string{"gemini-2.5-flash"},
			wantWarning: "agy models",
			wantTomlKey: "adapter.model",
		},
		{
			adapterName: "claude",
			model:       "unlisted-claude",
			models:      []string{"claude-3-5-sonnet"},
			wantWarning: "claude models",
			wantTomlKey: "adapter.claude.model",
		},
		{
			adapterName: "codex",
			model:       "unlisted-codex",
			models:      []string{"o3-mini"},
			wantWarning: "codex models",
			wantTomlKey: "adapter.codex.model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.adapterName, func(t *testing.T) {
			ad := &stubAdapterCR43{
				name:  tc.adapterName,
				model: tc.model,
				caps:  ai.Capabilities{Authenticated: true, Models: tc.models},
			}
			d := httpserver.ProbeAdapter(context.Background(), httpserver.Settings{Adapter: ad, Model: tc.model})
			if !strings.Contains(d.ModelWarn, tc.wantWarning) {
				t.Errorf("%s: want warning containing %q, got %q", tc.adapterName, tc.wantWarning, d.ModelWarn)
			}
			if !strings.Contains(d.ModelWarn, tc.wantTomlKey) {
				t.Errorf("%s: want toml key %q, got %q", tc.adapterName, tc.wantTomlKey, d.ModelWarn)
			}
			if !strings.Contains(d.ModelWarn, tc.model) {
				t.Errorf("%s: want model %q in warning, got %q", tc.adapterName, tc.model, d.ModelWarn)
			}

			// When model is in the listed models, no warning should be returned.
			adListed := &stubAdapterCR43{
				name:  tc.adapterName,
				model: tc.models[0],
				caps:  ai.Capabilities{Authenticated: true, Models: tc.models},
			}
			dListed := httpserver.ProbeAdapter(context.Background(), httpserver.Settings{Adapter: adListed, Model: tc.models[0]})
			if dListed.ModelWarn != "" {
				t.Errorf("%s: expected empty ModelWarn for listed model, got %q", tc.adapterName, dListed.ModelWarn)
			}
		})
	}
}

// T10.19: the finished job card and /settings show token usage; zero or missing usage is «نامشخص».
func TestJobUsageShown(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	if b := e.settingsPage(false); strings.Contains(b, "مصرف توکن") {
		t.Error("no finished jobs: usage section must be hidden")
	}
	e.ad.Output = lessonOutput("درس")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, "توکن: نامشخص") {
		t.Errorf("fake adapter reports no tokens: want «نامشخص» on the card\n%s", page)
	}
	if b := e.settingsPage(false); !strings.Contains(b, "مصرف توکن ۳۰ روز اخیر") || !strings.Contains(b, `<td data-label="توکن ورودی">نامشخص</td>`) {
		t.Errorf("settings: want usage table with «نامشخص»\n%s", b)
	}

	if _, err := e.db.Writer().Exec(`UPDATE jobs SET usage = '{"input_tokens":1200,"output_tokens":34}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if page := e.get("/topics/1").Body.String(); !strings.Contains(page, "توکن: ورودی ۱۲۰۰، خروجی ۳۴") {
		t.Errorf("card must show input/output tokens\n%s", page)
	}
	b := e.settingsPage(false)
	for _, want := range []string{"<td>تولید درس</td>", `<bdi>fake</bdi>`, `<td data-label="تعداد">۱</td>`, `<td data-label="توکن ورودی">۱۲۰۰</td>`, `<td data-label="توکن خروجی">۳۴</td>`} {
		if !strings.Contains(b, want) {
			t.Errorf("settings missing %q", want)
		}
	}
}
