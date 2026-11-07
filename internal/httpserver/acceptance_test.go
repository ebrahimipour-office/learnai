package httpserver_test

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai/fake"
	"learninghub/internal/config"
	"learninghub/internal/fsx"
	"learninghub/internal/schema"
	"learninghub/internal/scoring"
	"learninghub/internal/services/evaluation"
	"learninghub/internal/services/exercises"
)

// tree maps every regular file under base/rel to its sha256, keyed by slash path from base.
func tree(t *testing.T, base, rel string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(filepath.Join(base, filepath.FromSlash(rel)), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(base, p)
		m[filepath.ToSlash(r)] = fsx.HashBytes(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func evalOutput(exID string, aiScore int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"exercise_id":%q,"criteria_scores":[{"criterion_id":"c1","awarded":70,"comment":"ok"}],
		"score":%d,"errors":[],"strengths":[],"corrected_answer":{"format":"text","content":"x"},"teaching_notes_markdown":"n"}`, exID, aiScore))
}

// TestAcceptanceR11_2 is the T6.9 end-to-end suite: one learner's journey over HTTP with the fake adapter,
// checking each R11.2 acceptance criterion where the journey reaches it (TDD §9 traceability table).
// One flat test, not subtests: the env helpers call the parent t.Fatal.
func TestAcceptanceR11_2(t *testing.T) {
	e := newEnv(t)
	csrf, w := e.sm.CSRFToken(), e.db.Writer()
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := w.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	abs := func(rel string) string { return filepath.Join(e.base, filepath.FromSlash(rel)) }
	read := func(rel string) []byte {
		t.Helper()
		b, err := os.ReadFile(abs(rel))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write := func(rel string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(abs(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs(rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runJob := func(path string, form url.Values) string {
		t.Helper()
		if rec := e.post(path, form, true); rec.Code != http.StatusAccepted {
			t.Fatalf("%s: want 202, got %d %s", path, rec.Code, rec.Body.String())
		}
		id, _ := e.lastJob()
		e.pollUntilDone(id)
		_, st := e.lastJob()
		return st
	}

	// R11.2-1: confirming records exactly the ticked topics.
	draft := e.upload("go.md", []byte("# Go\n## Web\n### HTTP\n### Routing\n### Middleware\n")).Header().Get("Location")
	keys := extractValues(e.do(httptest.NewRequest(http.MethodGet, draft, nil)).Body.String(), `name="include" value="`)
	if len(keys) != 3 {
		t.Fatalf("R11.2-1: want 3 topics in the preview, got %v", keys)
	}
	e.selection(draft, url.Values{"rule": {"deepest"}, "shown_rule": {"deepest"}, "include": keys[:2]}, true)
	if rec := e.confirm(draft, url.Values{"csrf_token": {csrf}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("R11.2-1: confirm: %d %s", rec.Code, rec.Body.String())
	}
	if n := count(`SELECT COUNT(*) FROM topics`); n != 2 {
		t.Fatalf("R11.2-1: want 2 topic records for 2 ticked topics, got %d", n)
	}
	var httpID, routingID int64
	var dir string
	_ = w.QueryRow(`SELECT id, dir_name FROM topics WHERE title = 'HTTP'`).Scan(&httpID, &dir)
	_ = w.QueryRow(`SELECT id FROM topics WHERE title = 'Routing'`).Scan(&routingID)
	topicURL, top := fmt.Sprintf("/topics/%d", httpID), "topics/"+dir

	// R11.2-8 (HTTP): a foreign Host, Origin or site is refused and starts nothing.
	for _, h := range []map[string]string{{"Origin": "https://evil.com"}, {"Origin": "null"}, {"Sec-Fetch-Site": "cross-site"}, {"Host": "evil.com"}} {
		req := httptest.NewRequest(http.MethodPost, topicURL+"/jobs/lesson", nil)
		req.Header.Set("X-CSRF-Token", csrf)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		req.Host = cmp.Or(h["Host"], req.Host)
		if rec := e.do(req); rec.Code != http.StatusForbidden {
			t.Errorf("R11.2-8: %v: want 403, got %d", h, rec.Code)
		}
	}
	if n := count(`SELECT COUNT(*) FROM jobs`); n != 0 {
		t.Fatalf("R11.2-8: refused requests must not enqueue, got %d jobs", n)
	}

	// R11.2-2: lesson.md is written, rendered, and the keywords close it.
	e.ad.Output = lessonOutput("## مقدمه\n\nمتن درس.")
	if st := runJob(topicURL+"/jobs/lesson", nil); st != "succeeded" {
		t.Fatalf("R11.2-2: lesson job %s", st)
	}
	lesson := string(read(top + "/lesson/lesson.md"))
	if i, k := strings.Index(lesson, "ادامه متن درس"), strings.LastIndex(lesson, "Request"); i < 0 || k < i {
		t.Errorf("R11.2-2: keywords must close lesson.md:\n%s", lesson)
	}
	page := e.get(topicURL).Body.String()
	if i, k := strings.Index(page, `<h2 dir="auto">مقدمه</h2>`), strings.Index(page, "کلیدواژه"); i < 0 || k < i {
		t.Error("R11.2-2: panel must render the lesson with keywords after it")
	}

	// R11.2-7: one active job per topic, other topics stay usable, cancel works, status shows in the panel.
	e.ad.Scenario, e.ad.Delay = fake.Slow, 10*time.Second
	rt := fmt.Sprintf("/topics/%d", routingID)
	if rec := e.post(rt+"/jobs/lesson", nil, true); rec.Code != http.StatusAccepted {
		t.Fatalf("R11.2-7: slow job: %d", rec.Code)
	}
	slow, _ := e.lastJob()
	if rec := e.post(rt+"/jobs/lesson", nil, true); rec.Code != http.StatusConflict {
		t.Errorf("R11.2-7: second job on a busy topic: want 409, got %d", rec.Code)
	}
	if _, err := w.Exec(`INSERT INTO jobs (kind, scope_key, topic_id, status, params)
		SELECT kind, scope_key, topic_id, 'queued', '{}' FROM jobs WHERE id = ?`, slow); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("R11.2-7: the unique index must refuse a second active job, got %v", err)
	}
	if strings.Contains(e.get(topicURL).Body.String(), `type="submit" disabled`) {
		t.Error("R11.2-7: another topic's buttons must stay enabled")
	}
	if rec := e.post(fmt.Sprintf("/jobs/%d/cancel", slow), nil, true); rec.Code != http.StatusOK {
		t.Fatalf("R11.2-7: cancel: %d", rec.Code)
	}
	e.pollUntilDone(slow)
	if !strings.Contains(e.get(rt).Body.String(), "لغوشده") {
		t.Error("R11.2-7: the panel must show the canceled job")
	}
	e.ad.Scenario = fake.OK

	// R11.2-4: an answer file already on disk fails the whole commit and changes nothing.
	planted := path.Join(exercises.AnswersRel(dir, 1), "q01-hello.go")
	write(planted, []byte("// my own work\n"))
	before := tree(t, e.base, "topics")
	e.ad.Output = exOutput()
	if st := runJob(topicURL+"/jobs/exercises", url.Values{"count": {"3"}}); st != "failed" {
		t.Fatalf("R11.2-4: exercises over an existing answer file: want failed, got %s", st)
	}
	if after := tree(t, e.base, "topics"); !maps.Equal(before, after) {
		t.Errorf("R11.2-4: files changed:\nbefore %v\nafter  %v", before, after)
	}
	if n := count(`SELECT COUNT(*) FROM exercise_sets`); n != 0 {
		t.Errorf("R11.2-4: a failed commit must leave no set, got %d", n)
	}
	if !strings.Contains(e.get(topicURL+"/exercises").Body.String(), "ناموفق") {
		t.Error("R11.2-7: the failed job must show in the panel")
	}
	_ = os.Remove(abs(planted))

	// R11.2-3: a valid manifest, every id in exercises.md, one answer file per exercise.
	if st := runJob(topicURL+"/jobs/exercises", url.Values{"count": {"3"}}); st != "succeeded" {
		t.Fatalf("R11.2-3: exercises job %s", st)
	}
	setRel, ansRel := exercises.SetRel(dir, 1), exercises.AnswersRel(dir, 1)
	man := read(setRel + "/manifest.json")
	var m schema.Manifest
	cfg := config.DefaultConfig(e.base).Exercises
	if err := schema.ValidateManifest(man); err != nil {
		t.Fatalf("R11.2-3: manifest: %v", err)
	}
	if err := json.Unmarshal(man, &m); err != nil {
		t.Fatal(err)
	}
	if err := schema.CrossCheckManifest(&m, cfg.AllowedAnswerExts, cfg.CountMin, cfg.CountMax); err != nil {
		t.Errorf("R11.2-3: crosscheck: %v", err)
	}
	if err := exercises.CheckHeadings(read(setRel+"/exercises.md"), &m); err != nil {
		t.Errorf("R11.2-3: exercises.md: %v", err)
	}
	answers := tree(t, e.base, ansRel)
	if len(answers) != len(m.Exercises) {
		t.Errorf("R11.2-3: want %d answer files, got %v", len(m.Exercises), answers)
	}
	for _, x := range m.Exercises {
		if _, ok := answers[path.Join(ansRel, x.Answer.FileName)]; !ok {
			t.Errorf("R11.2-3: no answer file for %s", x.ID)
		}
	}

	// R11.2-5: the report embeds the answer byte for byte; the score is the software's, not the AI's.
	q01 := path.Join(ansRel, "q01-hello.go")
	answer := []byte("\ufeffpackage main\r\n\r\n// پاسخ من ```` با backtick\r\nfunc main() {}\r\n")
	write(q01, answer)
	e.ad.Output = evalOutput("q01", 95)
	if st := runJob(topicURL+"/jobs/evaluate", url.Values{"mode": {"full"}}); st != "succeeded" {
		t.Fatalf("R11.2-5: evaluate job %s", st)
	}
	att1 := evaluation.AttemptRel(dir, 1, 1)
	report := read(att1 + "/" + evaluation.ReportFile("q01"))
	if raw, ok := evaluation.RawAnswer(report); !ok || !bytes.Equal(raw, answer) {
		t.Errorf("R11.2-5: raw answer in report differs:\n%q\n%q", raw, answer)
	}
	for _, want := range []string{"Write it.", "- تلاش: 1", "- نسخه Rubric: 1", "## پاسخ شما (متن خام)", "## نمره: 70 از ۱۰۰",
		"نمره اعلام‌شده توسط AI 95", "## خطاها", "## پاسخ اصلاح‌شده", "## توضیح آموزشی"} {
		if !bytes.Contains(report, []byte(want)) {
			t.Errorf("R11.2-5: report missing %q", want)
		}
	}
	var res schema.Result
	if err := json.Unmarshal(read(att1+"/result.json"), &res); err != nil {
		t.Fatal(err)
	}
	var topicScore float64
	_ = w.QueryRow(`SELECT topic_score FROM attempts WHERE attempt_no = 1`).Scan(&topicScore)
	if got := scoring.Aggregate(res.Items).WeightedScore; got != res.Aggregate.WeightedScore || got != topicScore {
		t.Errorf("R11.2-5: topic score: recomputed %v, result.json %v, db %v", got, res.Aggregate.WeightedScore, topicScore)
	}
	if !bytes.Equal(read(q01), answer) {
		t.Error("R11.2-4: evaluation must not touch the answer file")
	}

	// R11.2-6: evaluating again makes attempt-2 and leaves attempt-1 byte-identical; incremental carries unchanged items.
	att1Files := tree(t, e.base, att1)
	if st := runJob(topicURL+"/jobs/evaluate", url.Values{"mode": {"full"}}); st != "succeeded" {
		t.Fatalf("R11.2-6: second evaluate %s", st)
	}
	if after := tree(t, e.base, att1); !maps.Equal(att1Files, after) {
		t.Error("R11.2-6: attempt-1 changed after a new evaluation")
	}
	read(evaluation.AttemptRel(dir, 1, 2) + "/" + evaluation.ReportFile("q01"))
	q02 := path.Join(ansRel, m.Exercises[1].Answer.FileName)
	write(q02, append(read(q02), "\nپاسخ من: context لغو را منتشر می‌کند.\n"...)) // keeps the @hub header
	e.ad.Output = evalOutput("q02", 70)
	if st := runJob(topicURL+"/jobs/evaluate", url.Values{"mode": {"incremental"}}); st != "succeeded" {
		t.Fatalf("R11.2-6: incremental evaluate %s", st)
	}
	var res3 schema.Result
	if err := json.Unmarshal(read(evaluation.AttemptRel(dir, 1, 3)+"/result.json"), &res3); err != nil {
		t.Fatal(err)
	}
	if it := res3.Items[0]; it.ExerciseID != "q01" || it.CarriedFromAttempt == nil || *it.CarriedFromAttempt != 2 {
		t.Errorf("R11.2-6: unchanged q01 must be carried from attempt 2: %+v", it)
	}
	if n := count(`SELECT COUNT(*) FROM files WHERE kind IN ('report','summary','result') AND immutable = 0`); n != 0 {
		t.Errorf("R11.2-6: %d report files are not immutable", n)
	}

	// R11.2-9: sync reports missing/moved/new/modified and changes no byte or record until confirmed.
	_ = os.Remove(abs(path.Join(ansRel, m.Exercises[2].Answer.FileName)))
	moved := path.Join(top, "answers", "mine", m.Exercises[1].Answer.FileName)
	write(moved, read(q02))
	_ = os.Remove(abs(q02))
	write(path.Join(top, "notes.md"), []byte("یادداشت\n"))
	write(top+"/lesson/lesson.md", []byte(lesson+"\nویرایش من\n"))
	files := func() string {
		var s string
		_ = w.QueryRow(`SELECT group_concat(id || ':' || rel_path || ':' || sha256, '|') FROM (SELECT * FROM files ORDER BY id)`).Scan(&s)
		return s
	}
	disk, rows := tree(t, e.base, "topics"), files()
	if rec := e.post("/sync/scan", nil, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("R11.2-9: scan: %d", rec.Code)
	}
	page = e.get("/sync").Body.String()
	if !strings.Contains(page, "1 گم‌شده، 1 جابه‌جاشده، 1 جدید، 1 تغییرکرده") {
		t.Errorf("R11.2-9: sync report:\n%s", page)
	}
	if !maps.Equal(disk, tree(t, e.base, "topics")) || rows != files() {
		t.Fatal("R11.2-9: a scan must not change files or file records")
	}
	var runID int64
	_ = w.QueryRow(`SELECT id FROM sync_runs`).Scan(&runID)
	if rec := e.post(fmt.Sprintf("/sync/%d/apply", runID), url.Values{"confirm": {"1"}, "move": extractValues(page, `name="move" value="`)}, false); rec.Code != http.StatusSeeOther {
		t.Fatalf("R11.2-9: apply: %d %s", rec.Code, rec.Body.String())
	}
	if !maps.Equal(disk, tree(t, e.base, "topics")) || count(`SELECT COUNT(*) FROM files WHERE rel_path = ?`, moved) != 1 {
		t.Error("R11.2-9: applying a move updates only the record, never the disk")
	}

	// R11.2-1: re-importing deletes no record and changes no file.
	disk = tree(t, e.base, "topics")
	maps.Copy(disk, tree(t, e.base, "roadmaps"))
	topics := count(`SELECT COUNT(*) FROM topics`)
	rec := e.uploadTo("1", "go.md", []byte("# Go\n## Web\n### HTTP\n### Router\n### Middleware\n"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("R11.2-1: re-import upload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.confirm(rec.Header().Get("Location"), url.Values{"csrf_token": {csrf}, "confirm": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("R11.2-1: re-import confirm: %d %s", rec.Code, rec.Body.String())
	}
	if n := count(`SELECT COUNT(*) FROM topics`); n < topics || count(`SELECT COUNT(*) FROM topics WHERE title = 'Routing' AND state = 'archived'`) != 1 {
		t.Errorf("R11.2-1: re-import: %d topics (was %d); the vanished one must be archived, not deleted", n, topics)
	}
	after := tree(t, e.base, "topics")
	maps.Copy(after, tree(t, e.base, "roadmaps"))
	for k, v := range disk {
		if after[k] != v {
			t.Errorf("R11.2-1: re-import changed %s", k)
		}
	}

	// R11.2-8 (paths): a record pointing outside the base dir, directly or by symlink, is never served.
	outside := filepath.Join(filepath.Dir(e.base), "outside.txt")
	if err := os.WriteFile(outside, []byte("TOP-SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	escapes := []string{"../outside.txt"}
	if err := os.Symlink(outside, abs(path.Join(top, "link.md"))); err == nil {
		escapes = append(escapes, path.Join(top, "link.md"))
	} else {
		t.Logf("R11.2-8: symlink case skipped: %v", err)
	}
	for _, rel := range escapes {
		res, err := w.Exec(`INSERT INTO files (topic_id, kind, rel_path, sha256, size_bytes) VALUES (?, 'lesson', ?, 'x', 10)`, httpID, rel)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if rec := e.get(fmt.Sprintf("/files/%d", id)); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "TOP-SECRET") {
			t.Errorf("R11.2-8: %s: want 404 without content, got %d", rel, rec.Code)
		}
	}
}
