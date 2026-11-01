package httpserver_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"learninghub/internal/ai"
	"learninghub/internal/httpserver"
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
	if b := e.settingsPage(false); !strings.Contains(b, "آماده است") || !strings.Contains(b, "fake") {
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
	if b := e.settingsPage(false); !strings.Contains(b, httpserver.PrivacyNotice) || !strings.Contains(b, `name="ack"`) {
		t.Fatalf("privacy notice/form missing:\n%s", b)
	}
	if rec := e.post("/settings/privacy", url.Values{}, false); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing ack: %d", rec.Code)
	}
	if rec := e.post("/settings/privacy", url.Values{"ack": {"1"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("ack: %d", rec.Code)
	}
	var v string
	if err := e.db.Writer().QueryRow(`SELECT value FROM meta WHERE key = 'privacy_ack'`).Scan(&v); err != nil || v == "" {
		t.Fatalf("privacy_ack not stored: %q %v", v, err)
	}
	if b := e.settingsPage(false); !strings.Contains(b, "تأییدشده") || strings.Contains(b, `name="ack"`) {
		t.Fatalf("ack not shown:\n%s", b)
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
