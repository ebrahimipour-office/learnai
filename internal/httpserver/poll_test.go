package httpserver_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai/fake"
)

// holdWriter keeps a write transaction open until the returned func is called.
func (e *env) holdWriter() (release func()) {
	e.t.Helper()
	started, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- e.db.WithTx(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('t13_10', 'x')`); err != nil {
				return err
			}
			close(started)
			<-stop
			return nil
		})
	}()
	select {
	case <-started:
	case err := <-done:
		e.t.Fatalf("hold writer: %v", err)
	}
	return func() {
		close(stop)
		if err := <-done; err != nil {
			e.t.Errorf("hold writer: %v", err)
		}
	}
}

// startSlowRegenerate queues a second, slow lesson job on topic 1 and returns its id.
func (e *env) startSlowRegenerate() int64 {
	e.t.Helper()
	e.ad.Scenario, e.ad.Delay = fake.Slow, 3*time.Second
	if rec := e.post("/topics/1/jobs/lesson", url.Values{"confirm_regenerate": {"1"}}, true); rec.Code != http.StatusAccepted {
		e.t.Fatalf("regenerate: %d %s", rec.Code, rec.Body.String())
	}
	id, status := e.lastJob()
	if status != "queued" && status != "running" {
		e.t.Fatalf("job %d: %s, want active", id, status)
	}
	return id
}

// T13.10: the job card and draft polls read from the read pool, so an open write transaction does not delay them.
func TestPollsDoNotWaitForWriter(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("## مقدمه\n\nمتن درس.")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id) // a lesson version exists, so the card poll must still see Panel.Lessons
	draftURL := e.upload("go.md", []byte("# Go\n## Phase 1\n- Types\n")).Header().Get("Location")

	id = e.startSlowRegenerate()

	release := e.holdWriter()
	defer release()
	for _, path := range []string{"/jobs/" + itoa(id) + "/card?poll=1", draftURL + "/ai?poll=1"} {
		start, got := time.Now(), make(chan *httptest.ResponseRecorder, 1)
		go func() { got <- e.get(path) }()
		var rec *httptest.ResponseRecorder
		select {
		case rec = <-got:
		case <-time.After(2 * time.Second): // a poll on the writer blocks until release
			t.Fatalf("GET %s blocked behind the open write transaction", path)
		}
		if d := time.Since(start); rec.Code != http.StatusOK || d > 100*time.Millisecond {
			t.Errorf("GET %s with the writer busy: %d in %v, want 200 in <100ms\n%s", path, rec.Code, d, rec.Body.String())
		}
	}
	if j, err := e.runner.Get(context.Background(), id); err != nil || !j.Active() {
		t.Fatalf("job %d: %+v %v", id, j, err)
	}
}

// T13.10: the trimmed panel of the job card poll renders exactly what the full panel does.
func TestJobCardPollHTMLUnchanged(t *testing.T) {
	e := newEnv(t)
	e.seedTopics()
	e.ad.Output = lessonOutput("## مقدمه\n\nمتن درس.")
	e.post("/topics/1/jobs/lesson", nil, true)
	id, _ := e.lastJob()
	e.pollUntilDone(id)

	id = e.startSlowRegenerate()
	for _, tab := range []string{"", "exercises", "answers", "reports"} {
		card := "/jobs/" + itoa(id) + "/card?tab=" + tab
		var poll, full string
		for range 20 { // the running job may change phase between the two GETs
			if poll, full = e.get(card+"&poll=1").Body.String(), e.get(card).Body.String(); poll == full {
				break
			}
		}
		if poll != full {
			t.Errorf("tab %q: poll card differs from the full-panel card\npoll:\n%s\nfull:\n%s", tab, poll, full)
		}
		if !strings.Contains(poll, "بازتولید درس") && tab == "" {
			t.Errorf("tab %q: poll card lost Panel.Lessons\n%s", tab, poll)
		}
	}
}

// T13.10: the draft poll still shows the analysis while it runs and reloads the page once it ends.
func TestDraftPollWithoutContent(t *testing.T) {
	e := newEnv(t)
	draftURL := e.upload("go.md", []byte("# Go\n## Phase 1\n- Types\n")).Header().Get("Location")
	rec := e.do(httptest.NewRequest(http.MethodGet, draftURL+"/ai?poll=1", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("finished analysis poll: %d refresh=%q", rec.Code, rec.Header().Get("HX-Refresh"))
	}
	// Without poll the section is rendered from the full draft, H2 hint included.
	if body := e.get(draftURL + "/ai").Body.String(); !strings.Contains(body, "سرفصل‌های سطح ۲") {
		t.Errorf("non-poll section lost the H2 hint\n%s", body)
	}
}
