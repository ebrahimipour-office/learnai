package maintenance_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"learninghub/internal/fsx"
	"learninghub/internal/services/maintenance"
	"learninghub/internal/store"
)

func TestRunCleansExpiredAndBacksUp(t *testing.T) {
	base := t.TempDir()
	db, err := store.Open(filepath.Join(base, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	old, recent := "2026-09-01T00:00:00.000Z", "2026-11-06T00:00:00.000Z"
	// id: status, finished_at
	for _, j := range []struct {
		id       int
		status   string
		finished any
	}{{1, "succeeded", recent}, {2, "failed", recent}, {3, "failed", old}, {4, "running", nil}, {5, "succeeded", old}} {
		if _, err := db.Writer().Exec(`INSERT INTO jobs (id, kind, scope_key, status, finished_at) VALUES (?, 'lesson', ?, ?, ?)`,
			j.id, "topic:"+string(rune('0'+j.id)), j.status, j.finished); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"1", "2", "3", "4", "5", "99"} {
		for _, d := range []string{".app/work/", ".app/staging/", ".app/logs/jobs/"} {
			write(d+id+"/x", "x")
		}
	}
	write(".app/work/notes/x", "not a job dir")
	for _, day := range []string{"01", "02", "03", "04", "05", "06", "07"} {
		write(".app/backup/app-2026-10-"+day+".db", "old")
	}
	user := map[string]string{
		"topics/01-http/lesson/lesson.md":           "# HTTP\n",
		"topics/01-http/exercises/v1/answers/q1.go": "package main\n",
		"AGENTS.md": "rules\n", "profile.md": "me\n",
	}
	for rel, c := range user {
		write(rel, c)
	}
	snapshot := func() map[string]string {
		out := map[string]string{}
		filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			rel, _ := filepath.Rel(base, p)
			if err == nil && !d.IsDir() && !strings.HasPrefix(rel, ".app") && !strings.HasPrefix(rel, "app.db") {
				b, _ := os.ReadFile(p)
				out[rel] = fsx.HashString(string(b))
			}
			return err
		})
		return out
	}
	before := snapshot()

	svc := &maintenance.Service{DB: db, Root: root, LogRetention: 30 * 24 * time.Hour}
	res, err := svc.Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(base, rel)); return err == nil }
	want := map[string]bool{
		// work/staging: kept only for running and recent failed jobs
		".app/work/1": false, ".app/work/2": true, ".app/work/3": false, ".app/work/4": true, ".app/work/5": false, ".app/work/99": false,
		".app/staging/1": false, ".app/staging/2": true, ".app/staging/4": true, ".app/staging/99": false,
		// logs: kept within retention whatever the outcome
		".app/logs/jobs/1": true, ".app/logs/jobs/2": true, ".app/logs/jobs/3": false, ".app/logs/jobs/4": true,
		".app/logs/jobs/5": false, ".app/logs/jobs/99": false,
		".app/work/notes": true,
		// 7 old + today = 8 → the oldest goes
		".app/backup/app-2026-10-01.db": false, ".app/backup/app-2026-10-02.db": true, ".app/backup/app-2026-11-07.db": true,
	}
	for rel, w := range want {
		if exists(rel) != w {
			t.Errorf("%s exists = %t, want %t", rel, !w, w)
		}
	}
	if res.Backup != ".app/backup/app-2026-11-07.db" {
		t.Errorf("Backup = %q", res.Backup)
	}
	bk, err := sql.Open("sqlite", filepath.Join(base, ".app/backup/app-2026-11-07.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bk.Close()
	var n int
	if err := bk.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil || n != 5 {
		t.Errorf("backup jobs = %d, %v; want 5", n, err)
	}

	// Same day again: no new backup, nothing overwritten.
	res, err = svc.Run(context.Background(), now.Add(time.Hour))
	if err != nil || res.Backup != "" {
		t.Errorf("second run: backup %q, err %v", res.Backup, err)
	}

	after := snapshot()
	if len(after) != len(before) {
		t.Errorf("user files changed: %v → %v", before, after)
	}
	for rel, h := range before {
		if after[rel] != h {
			t.Errorf("%s changed", rel)
		}
	}
}

// CR-10: maintenance skips symlinked entries
func TestMaintenance_SkipsSymlinkedEntries(t *testing.T) {
	base := t.TempDir()
	db, err := store.Open(filepath.Join(base, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Create user topic directory with important user files
	topicDir := filepath.Join(base, "topics", "01-http")
	if err := os.MkdirAll(topicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	answerPath := filepath.Join(topicDir, "answer.go")
	if err := os.WriteFile(answerPath, []byte("package main\n// student code"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create .app/work
	workBase := filepath.Join(base, ".app", "work")
	if err := os.MkdirAll(workBase, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create symlink .app/work/88 -> topics/01-http
	linkPath := filepath.Join(workBase, "88")
	err = os.Symlink(topicDir, linkPath)
	if err != nil && runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", linkPath, topicDir)
		if out, jerr := cmd.CombinedOutput(); jerr != nil {
			t.Skipf("cannot create symlink or junction: %v / %s", err, string(out))
		}
	} else if err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	svc := &maintenance.Service{DB: db, Root: root, LogRetention: 30 * 24 * time.Hour}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	res, err := svc.Run(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}

	// Symlinked entry must NOT be removed through the symlink
	for _, removed := range res.Removed {
		if removed == ".app/work/88" {
			t.Error("maintenance removed symlinked directory")
		}
	}

	// User answer file in topics/01-http/answer.go must be intact
	b, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("topics/01-http/answer.go was deleted: %v", err)
	}
	if string(b) != "package main\n// student code" {
		t.Fatalf("topics/01-http/answer.go content modified: %s", string(b))
	}
}
