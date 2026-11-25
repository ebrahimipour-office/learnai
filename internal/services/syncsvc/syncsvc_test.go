package syncsvc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/services/syncsvc"
	"learninghub/internal/store"
)

type env struct {
	t    *testing.T
	base string
	db   *store.DB
	root *fsx.Root
	svc  *syncsvc.Service
}

const (
	lessonRel = "topics/01-http/lesson/lesson.md"
	rubricRel = "topics/01-http/exercises/v1/rubric.json"
	ans1Rel   = "topics/01-http/exercises/v1/answers/q01.go"
	ans2Rel   = "topics/01-http/exercises/v1/answers/q02.go"
)

func setup(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	db, err := store.Open(filepath.Join(base, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	e := &env{t, base, db, root, &syncsvc.Service{DB: db, Root: root}}

	e.exec(`INSERT INTO roadmaps (id, slug, title, select_rule) VALUES (1, 'go', 'Go', '{}')`)
	e.exec(`INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name) VALUES (1, 1, 'http-key', 'HTTP', 'http', 1, 'http', '01-http')`)
	files := []struct {
		id        int
		kind, rel string
		content   string
	}{
		{1, "lesson", lessonRel, "# HTTP\n"},
		{2, "rubric", rubricRel, `{"r":1}`},
		{3, "answer", ans1Rel, string(fsx.GenerateAnswerTemplate("http-key", 1, "q01", "One", ".go", ""))},
		{4, "answer", ans2Rel, string(fsx.GenerateAnswerTemplate("http-key", 1, "q02", "Two", ".go", ""))},
		{5, "exercises_md", "topics/01-http/exercises/v1/exercises.md", "# ex\n"},
		{6, "manifest", "topics/01-http/exercises/v1/manifest.json", "{}"},
	}
	for _, f := range files {
		e.write(f.rel, f.content)
		sha := fsx.HashString(f.content)
		e.exec(`INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes, observed_sha256) VALUES (?, 1, ?, ?, ?, ?, ?)`,
			f.id, f.kind, f.rel, sha, len(f.content), sha)
	}
	e.exec(`INSERT INTO exercise_sets (id, topic_id, version, exercises_file, manifest_file, rubric_file, is_current) VALUES (1, 1, 1, 5, 6, 2, 1)`)
	e.exec(`INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256) VALUES
		(1, 'q01', 1, 'One', 'coding', 'easy', 1, 3, 'x'), (1, 'q02', 2, 'Two', 'coding', 'easy', 1, 4, 'y')`)
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Writer().Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) write(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) move(from, to string) {
	e.t.Helper()
	p := filepath.Join(e.base, filepath.FromSlash(to))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(e.base, filepath.FromSlash(from)), p); err != nil {
		e.t.Fatal(err)
	}
}

// snapshot is every file's bytes on disk plus the immutable part of each files row.
func (e *env) snapshot() (disk map[string]string, rows map[int64]string) {
	e.t.Helper()
	disk, rows = map[string]string{}, map[int64]string{}
	filepath.WalkDir(filepath.Join(e.base, "topics"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			disk[p] = string(b)
		}
		return err
	})
	q, err := e.db.Writer().Query(`SELECT id, rel_path || '|' || sha256 || '|' || size_bytes || '|' || kind FROM files`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer q.Close()
	for q.Next() {
		var id int64
		var v string
		q.Scan(&id, &v)
		rows[id] = v
	}
	return disk, rows
}

func (e *env) presence(id int64) string {
	var p string
	if err := e.db.Writer().QueryRow(`SELECT presence FROM files WHERE id = ?`, id).Scan(&p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// R11.2-9: delete, move, add and edit are each reported correctly and nothing is deleted or rewritten.
func TestScanScenarios(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	run, err := e.svc.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := run.Report; len(r.Missing)+len(r.Moved)+len(r.New)+len(r.Modified) != 0 {
		t.Fatalf("clean tree: want empty report, got %+v", r)
	}

	if err := os.Remove(filepath.Join(e.base, filepath.FromSlash(lessonRel))); err != nil { // delete
		t.Fatal(err)
	}
	e.move(rubricRel, "topics/01-http/rubric-copy.json")                                                                               // move, same content
	e.move(ans1Rel, "topics/01-http/mine/q01.go")                                                                                      // move …
	e.write("topics/01-http/mine/q01.go", string(fsx.GenerateAnswerTemplate("http-key", 1, "q01", "One", ".go", ""))+"package main\n") // … and edit
	e.write(ans2Rel, string(fsx.GenerateAnswerTemplate("http-key", 1, "q02", "Two", ".go", ""))+"answer\n")                            // answers change: not "modified"
	e.write("topics/01-http/exercises/v1/manifest.json", `{"edited":true}`)                                                            // edit
	e.write("topics/01-http/notes.txt", "my notes")                                                                                    // add
	diskBefore, rowsBefore := e.snapshot()

	run, err = e.svc.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := run.Report
	if len(r.Missing) != 1 || r.Missing[0].FileID != 1 {
		t.Errorf("missing = %+v, want lesson", r.Missing)
	}
	if len(r.Moved) != 2 || r.Moved[0].FileID != 2 || r.Moved[0].By != "hash" || r.Moved[0].To != "topics/01-http/rubric-copy.json" ||
		r.Moved[1].FileID != 3 || r.Moved[1].By != "header" || r.Moved[1].To != "topics/01-http/mine/q01.go" {
		t.Errorf("moved = %+v", r.Moved)
	}
	if len(r.New) != 1 || r.New[0] != "topics/01-http/notes.txt" {
		t.Errorf("new = %v", r.New)
	}
	if len(r.Modified) != 1 || r.Modified[0].FileID != 6 {
		t.Errorf("modified = %+v, want manifest only", r.Modified)
	}

	diskAfter, rowsAfter := e.snapshot()
	if len(diskAfter) != len(diskBefore) || len(rowsAfter) != len(rowsBefore) {
		t.Fatal("scan added or removed files or rows")
	}
	for k, v := range diskBefore {
		if diskAfter[k] != v {
			t.Errorf("scan changed %s on disk", k)
		}
	}
	for k, v := range rowsBefore {
		if rowsAfter[k] != v {
			t.Errorf("scan rewrote files row %d: %s → %s", k, v, rowsAfter[k])
		}
	}
	for id, want := range map[int64]string{1: "missing", 2: "missing", 3: "missing", 4: "present", 6: "present"} {
		if got := e.presence(id); got != want {
			t.Errorf("file %d presence = %s, want %s", id, got, want)
		}
	}
	var obs string
	e.db.Writer().QueryRow(`SELECT observed_sha256 FROM files WHERE id = 6`).Scan(&obs)
	if obs != fsx.HashString(`{"edited":true}`) {
		t.Errorf("observed_sha256 not updated: %s", obs)
	}

	// Applying is a separate, confirmed step: only the chosen move is recorded, no file is touched.
	if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{1}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("apply non-move: err = %v", err)
	}
	applied, err := e.svc.ApplyMoves(ctx, run.ID, []int64{3})
	if err != nil || len(applied) != 1 {
		t.Fatalf("apply: %v %+v", applied, err)
	}
	var rel string
	e.db.Writer().QueryRow(`SELECT rel_path FROM files WHERE id = 3`).Scan(&rel)
	if rel != "topics/01-http/mine/q01.go" || e.presence(3) != "present" || e.presence(2) != "missing" {
		t.Errorf("after apply: rel=%s p3=%s p2=%s", rel, e.presence(3), e.presence(2))
	}
	if disk, _ := e.snapshot(); len(disk) != len(diskAfter) {
		t.Error("apply touched the disk")
	}
	var audits int
	e.db.Writer().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'sync_move' AND target_id = 3`).Scan(&audits)
	if audits != 1 {
		t.Errorf("audit rows = %d", audits)
	}
	if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{2}); !errors.Is(err, domain.ErrInvalidState) {
		t.Errorf("second apply on same run: err = %v", err)
	}
	latest, err := e.svc.Latest(ctx)
	if err != nil || latest.ID != run.ID || len(latest.Applied) != 1 {
		t.Errorf("latest = %+v, %v", latest, err)
	}

	// A file that comes back is present again on the next scan.
	e.write(lessonRel, "# HTTP\n")
	if _, err := e.svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if e.presence(1) != "present" {
		t.Error("restored lesson still missing")
	}
}

func TestApplyRefusesStaleMove(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.move(rubricRel, "topics/01-http/r.json")
	run, err := e.svc.Scan(ctx)
	if err != nil || len(run.Report.Moved) != 1 {
		t.Fatalf("scan: %v %+v", err, run)
	}
	e.move("topics/01-http/r.json", rubricRel) // user moved it back
	if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{2}); !errors.Is(err, syncsvc.ErrStale) {
		t.Errorf("err = %v, want ErrStale", err)
	}
	var rel string
	e.db.Writer().QueryRow(`SELECT rel_path FROM files WHERE id = 2`).Scan(&rel)
	if rel != rubricRel {
		t.Errorf("rel_path changed to %s", rel)
	}
}

func TestAmbiguousHashStaysMissingAndNew(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.move(rubricRel, "topics/01-http/a.json")
	e.write("topics/01-http/b.json", `{"r":1}`) // two candidates with the rubric's hash
	run, err := e.svc.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Report.Moved) != 0 || len(run.Report.Missing) != 1 || len(run.Report.New) != 2 {
		t.Errorf("report = %+v", run.Report)
	}
}

// CR-04: a file name fsx rejects is reported as skipped; the rest of the scan is complete.
func TestInvalidNameIsSkippedNotFatal(t *testing.T) {
	e := setup(t)
	// On Windows, `:` or `?` cannot be created on NTFS, but a >220 byte path can be created.
	// On Unix, `:` can also be created.
	bad := "topics/01-http/" + strings.Repeat("a", 215) + ".md"
	if runtime.GOOS != "windows" {
		bad = "topics/01-http/notes:draft.md"
	}
	e.write(bad, "x")
	e.write("topics/01-http/extra.md", "new")
	e.write("topics/01-http/.extra.md.tmp-0123456789abcdef", "partial") // crash leftover: not new (T13.4)
	e.move(rubricRel, "topics/01-http/rubric-moved.json")
	run, err := e.svc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := run.Report
	if len(r.Skipped) != 1 || r.Skipped[0] != bad {
		t.Errorf("skipped = %v, want [%s]", r.Skipped, bad)
	}
	if len(r.Moved) != 1 || len(r.New) != 1 || r.New[0] != "topics/01-http/extra.md" || len(r.Missing) != 0 {
		t.Errorf("report = %+v", r)
	}
}

// T12.1: an unregistered file or folder that cannot be read (permissions) is skipped, not fatal.
func TestUnreadableIsSkippedNotFatal(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs unix permissions and a non-root user")
	}
	e := setup(t)
	e.write("topics/01-http/locked.md", "x")
	e.write("topics/01-http/shut/inner.md", "y")
	e.write("topics/01-http/extra.md", "new")
	for rel, mode := range map[string]os.FileMode{"topics/01-http/locked.md": 0o000, "topics/01-http/shut": 0o000} {
		abs := filepath.Join(e.root.BaseDir(), filepath.FromSlash(rel))
		if err := os.Chmod(abs, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(abs, 0o755) })
	}
	run, err := e.svc.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := run.Report
	slices.Sort(r.Skipped)
	if want := []string{"topics/01-http/locked.md", "topics/01-http/shut"}; !slices.Equal(r.Skipped, want) {
		t.Errorf("skipped = %v, want %v", r.Skipped, want)
	}
	if len(r.New) != 1 || r.New[0] != "topics/01-http/extra.md" {
		t.Errorf("new = %v", r.New)
	}
}

// T6.4: sync recomputes every topic's status; a missing file lowers it and its return restores it.
func TestSyncRecomputesStatus(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.exec(`UPDATE exercises SET template_sha256 = (SELECT sha256 FROM files WHERE id = answer_file_id)`)
	e.exec(`INSERT INTO lesson_versions (topic_id, version, file_id, keywords, is_current) VALUES (1, 1, 1, '[]', 1)`)
	e.exec(`INSERT INTO topics (id, roadmap_id, stable_key, title, title_norm, ord, slug, dir_name, status)
		VALUES (2, 1, 'empty', 'Empty', 'empty', 2, 'empty', '02-empty', 'evaluated')`)
	status := func(id int) string {
		t.Helper()
		var s string
		if err := e.db.Writer().QueryRow(`SELECT status FROM topics WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	scan := func() *syncsvc.Run {
		t.Helper()
		run, err := e.svc.Scan(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	steps := []struct {
		name string
		do   func()
		want string
	}{
		{"templates only", func() {}, "exercises_ready"},
		{"answer edited", func() { e.write(ans1Rel, "package main // mine\n") }, "answered"},
		{"lesson deleted", func() { e.move(lessonRel, "../lesson.bak") }, "created"},
		{"lesson back", func() { e.move("../lesson.bak", lessonRel) }, "answered"},
		{"attempt", func() {
			e.exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, topic_score, answered_count, total_count, is_partial,
				result_file, summary_file, started_at, finished_at) VALUES (1, 1, 'full', 1, 50, 1, 2, 1, 6, 6, 'x', 'x')`)
		}, "evaluated"},
	}
	for _, s := range steps {
		s.do()
		scan()
		if got := status(1); got != s.want {
			t.Errorf("%s: status = %s, want %s", s.name, got, s.want)
		}
	}
	if got := status(2); got != "created" {
		t.Errorf("topic without records: status = %s, want created", got)
	}

	// A moved exercises.md lowers the status until the user applies the move.
	e.move("topics/01-http/exercises/v1/exercises.md", "topics/01-http/ex.md")
	run := scan()
	if got := status(1); got != "content_ready" {
		t.Errorf("moved exercises.md: status = %s, want content_ready", got)
	}
	if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{5}); err != nil {
		t.Fatal(err)
	}
	if got := status(1); got != "evaluated" {
		t.Errorf("after apply: status = %s, want evaluated", got)
	}
}

// CR-52: change the target between Scan and Apply → refused (both for hash match and header match).
func TestCR52_ChangeTargetBetweenScanAndApplyRefused(t *testing.T) {
	t.Run("hash match target changed", func(t *testing.T) {
		e := setup(t)
		ctx := context.Background()
		e.move(rubricRel, "topics/01-http/r.json")
		run, err := e.svc.Scan(ctx)
		if err != nil || len(run.Report.Moved) != 1 || run.Report.Moved[0].By != "hash" {
			t.Fatalf("scan: %v %+v", err, run)
		}
		// Target content is modified between Scan and ApplyMoves
		e.write("topics/01-http/r.json", `{"tampered":true}`)
		if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{2}); !errors.Is(err, syncsvc.ErrStale) {
			t.Errorf("err = %v, want ErrStale", err)
		}
		var rel string
		e.db.Writer().QueryRow(`SELECT rel_path FROM files WHERE id = 2`).Scan(&rel)
		if rel != rubricRel {
			t.Errorf("rel_path changed to %s, want original %s", rel, rubricRel)
		}
	})

	t.Run("header match target header corrupted", func(t *testing.T) {
		e := setup(t)
		ctx := context.Background()
		// Write modified content (with header) to an unregistered path so hash doesn't match template
		headerContent := string(fsx.GenerateAnswerTemplate("http-key", 1, "q01", "One", ".go", "")) + "\n// my solution code\n"
		e.write("topics/01-http/answers/q01_moved.go", headerContent)
		// Delete original answer file so it is missing
		os.Remove(filepath.Join(e.base, filepath.FromSlash(ans1Rel)))
		run, err := e.svc.Scan(ctx)
		if err != nil || len(run.Report.Moved) != 1 || run.Report.Moved[0].By != "header" {
			t.Fatalf("scan: %v %+v", err, run)
		}
		// Target file header is corrupted or stripped between Scan and ApplyMoves
		e.write("topics/01-http/answers/q01_moved.go", "package main\n// no hub header here\n")
		if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{3}); !errors.Is(err, syncsvc.ErrStale) {
			t.Errorf("err = %v, want ErrStale", err)
		}
		var rel string
		e.db.Writer().QueryRow(`SELECT rel_path FROM files WHERE id = 3`).Scan(&rel)
		if rel != ans1Rel {
			t.Errorf("rel_path changed to %s, want original %s", rel, ans1Rel)
		}
	})
}

// CR-52: an empty missing file and an empty new file → no proposal.
func TestCR52_EmptyFilesProduceNoMoveProposal(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	// Register an empty file that is missing on disk
	emptySHA := fsx.HashString("")
	e.exec(`INSERT INTO files (id, topic_id, kind, rel_path, sha256, size_bytes) VALUES (99, 1, 'lesson', 'topics/01-http/empty_missing.md', ?, 0)`, emptySHA)
	// Create an empty unregistered new file on disk
	e.write("topics/01-http/empty_new.md", "")

	run, err := e.svc.Scan(ctx)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, m := range run.Report.Moved {
		if m.FileID == 99 || m.To == "topics/01-http/empty_new.md" {
			t.Fatalf("empty file matched as move: %+v", m)
		}
	}
	missingFound := false
	for _, item := range run.Report.Missing {
		if item.FileID == 99 {
			missingFound = true
			break
		}
	}
	if !missingFound {
		t.Errorf("expected file 99 in missing report, got: %+v", run.Report.Missing)
	}
	newFound := false
	for _, p := range run.Report.New {
		if p == "topics/01-http/empty_new.md" {
			newFound = true
			break
		}
	}
	if !newFound {
		t.Errorf("expected empty_new.md in new report, got: %+v", run.Report.New)
	}
}

// CR-52: Scan guards updates with WHERE id=? AND rel_path=? AND sha256=? so stale observations
// do not overwrite records changed concurrently in the database.
func TestCR52_ScanGuardsAgainstConcurrentDBUpdate(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	newSHA := fsx.HashString("# HTTP Updated Concurrently\n")
	// Hook executes after disk observation but before WithTx
	syncsvc.SetBeforeCommitTx(t, func() {
		e.exec(`UPDATE files SET sha256 = ?, observed_sha256 = ? WHERE id = 1`, newSHA, newSHA)
	})

	run, err := e.svc.Scan(ctx)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	_ = run

	// The row in files should NOT have been overwritten with the stale sha/mtime
	var dbSHA, dbObs string
	e.db.Writer().QueryRow(`SELECT sha256, observed_sha256 FROM files WHERE id = 1`).Scan(&dbSHA, &dbObs)
	if dbSHA != newSHA || dbObs != newSHA {
		t.Errorf("row was overwritten: sha=%s obs=%s, want %s", dbSHA, dbObs, newSHA)
	}
}

// CR-52: ApplyMoves re-verifies against DB row and refuses move if file was concurrently changed in DB.
func TestCR52_ApplyMovesRefusesConcurrentlyChangedDBRow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.move(rubricRel, "topics/01-http/r.json")
	run, err := e.svc.Scan(ctx)
	if err != nil || len(run.Report.Moved) != 1 {
		t.Fatalf("scan: %v %+v", err, run)
	}
	// Concurrently change the file's sha256 in the database before user applies move
	e.exec(`UPDATE files SET sha256 = 'different_sha' WHERE id = 2`)
	if _, err := e.svc.ApplyMoves(ctx, run.ID, []int64{2}); !errors.Is(err, syncsvc.ErrStale) {
		t.Errorf("err = %v, want ErrStale", err)
	}
}

// T9.15: a lesson.md edited by hand becomes searchable at the next scan; an unchanged one is not rewritten.
func TestScanReindexesEditedLesson(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hits := func(phrase string) (n int) {
		t.Helper()
		if err := e.db.Writer().QueryRow(`SELECT COUNT(*) FROM lesson_fts WHERE lesson_fts MATCH ?`, `"`+phrase+`"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := e.svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := hits("http"); n != 0 {
		t.Fatalf("unchanged lesson indexed by scan: %d", n)
	}
	e.write(lessonRel, "# HTTP\nmiddleware chains\n")
	if _, err := e.svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if hits("middleware") != 1 {
		t.Error("edited lesson not reindexed")
	}
}

// T10.7: an unchanged file is not hashed again; a same-size edit with a new mtime is.
func TestScanSkipsHashOfUnchangedFiles(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	n := syncsvc.CountHashes(t)
	run, err := e.svc.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 0 || len(run.Report.Modified) != 0 {
		t.Fatalf("second scan: %d hashes, modified %v; want none", *n, run.Report.Modified)
	}

	e.write(lessonRel, "# HTTX\n") // same size
	p := filepath.Join(e.base, filepath.FromSlash(lessonRel))
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	run, err = e.svc.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 1 || len(run.Report.Modified) != 1 || run.Report.Modified[0].FileID != 1 {
		t.Fatalf("after same-size edit: %d hashes, modified %v; want 1 hash and the lesson", *n, run.Report.Modified)
	}
}
