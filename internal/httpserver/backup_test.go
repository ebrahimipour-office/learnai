package httpserver_test

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBackupsOnSettings(t *testing.T) {
	e := newEnv(t)
	if b := e.settingsPage(false); !strings.Contains(b, "هنوز پشتیبانی ساخته نشده") || !strings.Contains(b, `action="/settings/backup"`) {
		t.Fatalf("empty backups section missing:\n%s", b)
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/backup", nil) // no CSRF token
	if rec := e.do(req); rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF: %d", rec.Code)
	}
	if rec := e.post("/settings/backup", nil, false); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "backup=made") {
		t.Fatalf("backup now: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.post("/settings/backup", nil, false); !strings.Contains(rec.Header().Get("Location"), "backup=exists") {
		t.Fatalf("second backup the same day: %s", rec.Header().Get("Location"))
	}
	name := "app-" + time.Now().UTC().Format("2006-01-02") + ".db"
	rec := e.get("/settings?backup=exists")
	if b := rec.Body.String(); !strings.Contains(b, `href="/settings/backups/`+name+`"`) || !strings.Contains(b, "cp .app/backup/"+name+" app.db") ||
		!strings.Contains(b, "از قبل وجود دارد") {
		t.Fatalf("backup not listed with restore command:\n%s", b)
	}
	rec = e.get("/settings/backups/" + name)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "SQLite format 3") || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}

	if err := os.Symlink(filepath.Join(e.base, "app.db"), filepath.Join(e.base, ".app/backup/app-2020-01-02.db")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/settings/backups/..%2Fapp.db":       http.StatusBadRequest, // traversal
		"/settings/backups/app.db":            http.StatusBadRequest,
		"/settings/backups/app-2020-01-01.db": http.StatusNotFound,
		"/settings/backups/app-2020-01-02.db": http.StatusNotFound, // symlink never served
	} {
		if rec := e.get(path); rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	if b := e.settingsPage(false); strings.Contains(b, "app-2020-01-02.db") {
		t.Error("symlinked backup listed")
	}
}

func TestTopicZipExport(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	if rec := e.get("/topics/1/export"); rec.Code != http.StatusNotFound {
		t.Fatalf("no folder yet: %d", rec.Code)
	}
	if rec := e.get("/topics/99/export"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown topic: %d", rec.Code)
	}
	for rel, c := range map[string]string{
		"topics/01-http/lesson/lesson.md":           "# HTTP\n",
		"topics/01-http/exercises/v1/answers/q1.go": "package main\n",
		"topics/01-http/.internal/rubric-v1.json":   "{}",
		"topics/02-routing/lesson/lesson.md":        "other topic",
	} {
		p := filepath.Join(e.base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(e.base, "app.db"), filepath.Join(e.base, "topics/01-http/link.db")); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/topics/1/export")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "01-http.zip") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	if want := []string{"01-http/exercises/v1/answers/q1.go", "01-http/lesson/lesson.md"}; !slices.Equal(names, want) {
		t.Fatalf("zip entries %v, want %v", names, want)
	}
	if !strings.Contains(e.get("/topics/1").Body.String(), `href="/topics/1/export"`) {
		t.Error("topic page has no zip link")
	}
}
