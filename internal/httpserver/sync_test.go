package httpserver_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"learninghub/internal/fsx"
)

func TestSyncPageScanAndApply(t *testing.T) {
	e := newEnv(t)
	if page := e.get("/sync").Body.String(); !strings.Contains(page, "هنوز اسکنی انجام نشده است") {
		t.Fatalf("empty sync page:\n%s", page)
	}
	for _, q := range []string{
		`INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'go', 'Go', '{}')`,
		`INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name) VALUES (1, 1, 'k', 'HTTP', 'http', 1, 'http', '01-http')`,
		`INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes) VALUES (1, 1, 'rubric', 'topics/01-http/rubric.json', '` + fsx.HashString("{}") + `', 2)`,
	} {
		if _, err := e.db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(e.base, "topics", "01-http", "moved")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rubric.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rec := e.post("/sync/scan", nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body.String())
	}
	page := e.get("/sync").Body.String()
	if !strings.Contains(page, "1 جابه‌جاشده") || !strings.Contains(page, "topics/01-http/moved/rubric.json") || !strings.Contains(page, `name="move" value="1"`) {
		t.Fatalf("report page:\n%s", page)
	}
	var runID string
	e.db.Writer().QueryRow(`SELECT id FROM sync_runs`).Scan(&runID)

	if rec := e.post("/sync/"+runID+"/apply", url.Values{"move": {"1"}}, false); rec.Code != http.StatusConflict {
		t.Errorf("apply without confirm: want 409, got %d", rec.Code)
	}
	if rec := e.post("/sync/"+runID+"/apply", url.Values{"confirm": {"1"}, "move": {"1"}}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	var rel string
	e.db.Writer().QueryRow(`SELECT rel_path FROM files WHERE id = 1`).Scan(&rel)
	if rel != "topics/01-http/moved/rubric.json" {
		t.Errorf("rel_path = %s", rel)
	}
	if rec := e.post("/sync/"+runID+"/apply", url.Values{"confirm": {"1"}, "move": {"1"}}, false); rec.Code != http.StatusConflict {
		t.Errorf("second apply: want 409, got %d", rec.Code)
	}
	if rec := e.post("/sync/999/apply", url.Values{"confirm": {"1"}, "move": {"1"}}, false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown run: want 404, got %d", rec.Code)
	}
}
