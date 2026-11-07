package httpserver_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestSuggestionsTab(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	if _, err := e.db.Writer().Exec(`INSERT INTO suggestions (id, topic_id, kind, title, title_norm, reason) VALUES
		(1, 2, 'prerequisite', 'URL و Query', 'url و query', 'مسیریابی بدون URL قابل فهم نیست'),
		(2, 2, 'supplementary', 'میان‌افزار', 'میان‌افزار', 'ادامه طبیعی')`); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/topics/2/suggestions")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "URL و Query") ||
		!strings.Contains(body, `action="/suggestions/1/accept"`) || !strings.Contains(body, "پیش‌نیاز") {
		t.Fatalf("tab: %d %s", rec.Code, body)
	}
	if body := e.get("/topics").Body.String(); strings.Contains(body, "URL و Query") {
		t.Fatal("pending suggestion must not appear in the topic list")
	}

	for path, want := range map[string]int{"/suggestions/1/accept": http.StatusSeeOther, "/suggestions/2/reject": http.StatusSeeOther} {
		if rec := e.post(path, nil, false); rec.Code != want || rec.Header().Get("Location") != "/topics/2/suggestions" {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := e.post("/suggestions/1/reject", nil, false); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "قبلاً تصمیم") {
		t.Fatalf("second decision: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.post("/suggestions/9/accept", nil, false); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
	body := e.get("/topics").Body.String()
	if !strings.Contains(body, "URL و Query") || !strings.Contains(body, "پیشنهادی") || strings.Contains(body, "میان‌افزار") {
		t.Fatalf("list after decisions: %s", body)
	}
	if i, j := strings.Index(body, "URL و Query"), strings.Index(body, "مسیریابی"); i > j {
		t.Fatal("prerequisite must be listed before its source topic")
	}
	if body := e.get("/topics/2/suggestions").Body.String(); strings.Contains(body, `action="/suggestions/1/accept"`) || !strings.Contains(body, "موضوع ساخته‌شده") {
		t.Fatalf("decided tab: %s", body)
	}
}
