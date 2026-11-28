package jobs_test

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/store"
)

const lessonRel = "topics/01-intro/lesson/lesson.md"

// lessonLike follows the handler pipeline (TDD §6.6) with a fake adapter: it fills work/ and
// staging/, runs the AI under the watchdog, then commits lesson.md.
func lessonLike(root *fsx.Root, a ai.Adapter, req ai.Request, inCommit func()) jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		for _, dir := range []string{prompt.WorkRel(j.ID), jobs.StagingRel(j.ID)} {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := root.WriteNew(dir+"/x", []byte("x"), 0o644); err != nil {
				return err
			}
		}
		if _, err := ai.Watch(ctx, a, req, nil); err != nil {
			return err
		}
		return j.Commit(ctx, func(ctx context.Context) error {
			if inCommit != nil {
				inCommit()
			}
			if err := root.MkdirAll(path.Dir(lessonRel), 0o755); err != nil {
				return err
			}
			return root.WriteNew(lessonRel, []byte("# lesson"), 0o644)
		})
	}
}

func filesRunner(t *testing.T) (*jobs.Runner, *store.DB, *fsx.Root) {
	t.Helper()
	r, db := newRunner(t)
	root, err := fsx.OpenRoot(filepath.Dir(db.Path()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	r.Files = root
	return r, db, root
}

func waitCleaned(t *testing.T, root *fsx.Root, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, p := range paths {
		for root.Exists(p) {
			if time.Now().After(deadline) {
				t.Fatalf("%s still exists after 5s", p)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestCancelRunningJob(t *testing.T) {
	r, db, root := filesRunner(t)
	r.Register(domain.JobKindLesson, lessonLike(root, &fake.Adapter{Scenario: fake.Slow, Delay: time.Minute}, ai.Request{}, nil))
	id := enqueue(t, r, "topic:1")
	start(t, r)
	waitStatus(t, db, id, domain.JobStatusRunning)
	for !root.Exists(jobs.StagingRel(id) + "/x") {
		time.Sleep(5 * time.Millisecond)
	}

	begin := time.Now()
	if late, err := r.Cancel(context.Background(), id); err != nil || late {
		t.Fatalf("cancel: late=%v err=%v", late, err)
	}
	got := waitStatus(t, db, id, domain.JobStatusCanceled)
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("cancel took %s (limit 5s)", d)
	}
	if !got.finished {
		t.Error("finished_at not set")
	}
	waitCleaned(t, root, prompt.WorkRel(id), jobs.StagingRel(id), lessonRel)
	if _, err := r.Cancel(context.Background(), id); !errors.Is(err, domain.ErrInvalidState) {
		t.Errorf("second cancel: want ErrInvalidState, got %v", err)
	}
}

func TestCancelQueuedJob(t *testing.T) {
	r, db := newRunner(t)
	ran := make(chan int64, 2)
	release := make(chan struct{})
	r.Register(domain.JobKindLesson, func(_ context.Context, j jobs.Job) error { ran <- j.ID; <-release; return nil })
	first := enqueue(t, r, "topic:1")
	second := enqueue(t, r, "topic:2")
	start(t, r)
	waitStatus(t, db, first, domain.JobStatusRunning)

	if _, err := r.Cancel(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if got := get(t, db, second); got.status != "canceled" || got.started {
		t.Fatalf("queued job: got %+v", got)
	}
	close(release)
	waitStatus(t, db, first, domain.JobStatusSucceeded)
	if id := <-ran; id != first || len(ran) != 0 {
		t.Error("canceled queued job must never run")
	}
	if _, err := r.Cancel(context.Background(), 999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown job: want ErrNotFound, got %v", err)
	}
}

func TestTimeoutLeavesNoFile(t *testing.T) {
	r, db, root := filesRunner(t)
	r.Register(domain.JobKindLesson, lessonLike(root,
		&fake.Adapter{Scenario: fake.Slow, Delay: time.Minute}, ai.Request{Timeout: 100 * time.Millisecond}, nil))
	id := enqueue(t, r, "topic:1")
	start(t, r)
	if got := waitStatus(t, db, id, domain.JobStatusFailed); got.code != "timeout" {
		t.Fatalf("want failed(timeout), got %+v", got)
	}
	if root.Exists(lessonRel) {
		t.Error("timeout must not leave a lesson file")
	}
	if !root.Exists(jobs.StagingRel(id)) {
		t.Error("failed job keeps staging/ for retry")
	}
}

func TestStallFailsJob(t *testing.T) {
	r, db, root := filesRunner(t)
	r.Register(domain.JobKindLesson, lessonLike(root,
		&fake.Adapter{Scenario: fake.Hang}, ai.Request{StallTimeout: 100 * time.Millisecond}, nil))
	id := enqueue(t, r, "topic:1")
	start(t, r)
	if got := waitStatus(t, db, id, domain.JobStatusFailed); got.code != "stalled" {
		t.Fatalf("want failed(stalled), got %+v", got)
	}
	if root.Exists(lessonRel) {
		t.Error("stall must not leave a lesson file")
	}
}

func TestCancelDuringCommitIsLate(t *testing.T) {
	r, db, root := filesRunner(t)
	committing, release := make(chan struct{}), make(chan struct{})
	r.Register(domain.JobKindLesson, lessonLike(root,
		&fake.Adapter{Scenario: fake.OK, Output: []byte(`{}`)}, ai.Request{},
		func() { close(committing); <-release }))
	id := enqueue(t, r, "topic:1")
	start(t, r)
	<-committing

	late, err := r.Cancel(context.Background(), id)
	if err != nil || !late {
		t.Fatalf("cancel during commit: want late, got late=%v err=%v", late, err)
	}
	close(release)
	got := waitStatus(t, db, id, domain.JobStatusSucceeded)
	if got.msg != jobs.LateCancelMessage {
		t.Errorf("want late-cancel message, got %q", got.msg)
	}
	if !root.Exists(lessonRel) {
		t.Error("commit must complete despite the cancel")
	}
	waitCleaned(t, root, prompt.WorkRel(id), jobs.StagingRel(id))
}

// A cancel that lands after the AI step but before commit starts must stop the commit.
func TestCancelBeforeCommitBlocksIt(t *testing.T) {
	r, db, root := filesRunner(t)
	afterAI, release := make(chan struct{}), make(chan struct{})
	r.Register(domain.JobKindLesson, func(ctx context.Context, j jobs.Job) error {
		close(afterAI)
		<-release
		// Ignores ctx on purpose: the commit gate alone must refuse.
		return j.Commit(context.WithoutCancel(ctx), func(context.Context) error {
			return root.WriteNew("topics/should-not-exist", nil, 0o644)
		})
	})
	id := enqueue(t, r, "topic:1")
	start(t, r)
	<-afterAI
	if _, err := r.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitStatus(t, db, id, domain.JobStatusCanceled)
	if root.Exists("topics/should-not-exist") {
		t.Error("commit ran after cancel")
	}
}

// autoRunner is a filesRunner whose rate_limited/interrupted failures retry by themselves (T10.8).
func autoRunner(t *testing.T, delay time.Duration) (*jobs.Runner, *store.DB, *fsx.Root) {
	t.Helper()
	r, db, root := filesRunner(t)
	r.AutoRetry, r.RetryDelay = 2, delay
	return r, db, root
}

// retryOf returns the job queued as the retry of id, waiting up to 5s for it.
func retryOf(t *testing.T, db *store.DB, id int64) (next int64, attempt int, notBefore string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		err := db.Writer().QueryRow(`SELECT id, auto_attempt, COALESCE(not_before, '') FROM jobs WHERE retry_of = ?`, id).
			Scan(&next, &attempt, &notBefore)
		if err == nil {
			return next, attempt, notBefore
		}
		if time.Now().After(deadline) {
			t.Fatalf("no retry of job %d: %v", id, err)
		}
	}
}

func TestRateLimitedRetriesAfterDelay(t *testing.T) {
	r, db, root := autoRunner(t, 300*time.Millisecond)
	a := &fake.Adapter{Scenario: fake.RateLimited, Fails: 1}
	r.Register(domain.JobKindLesson, lessonLike(root, a, ai.Request{}, nil))
	start(t, r)
	first := enqueue(t, r, "topic:1")

	if got := waitStatus(t, db, first, domain.JobStatusFailed); got.code != "rate_limited" {
		t.Fatalf("first run: %+v", got)
	}
	next, attempt, notBefore := retryOf(t, db, first)
	if attempt != 1 || notBefore == "" {
		t.Fatalf("auto retry: attempt %d, not_before %q", attempt, notBefore)
	}
	time.Sleep(100 * time.Millisecond) // well inside the delay
	if got := get(t, db, next); got.status != "queued" || got.started {
		t.Fatalf("retry ran before its delay: %+v", got)
	}
	waitStatus(t, db, next, domain.JobStatusSucceeded)
	if !root.Exists(lessonRel) || len(a.Calls()) != 2 {
		t.Errorf("want the lesson after 2 AI runs, got %d runs", len(a.Calls()))
	}
	if info, err := r.Get(context.Background(), next); err != nil || info.AutoAttempt != 1 || info.AutoMax != 2 {
		t.Errorf("card info: %+v, %v", info, err)
	}
}

func TestAutoRetryStopsAfterLimit(t *testing.T) {
	r, db, root := autoRunner(t, 10*time.Millisecond)
	a := &fake.Adapter{Scenario: fake.RateLimited}
	r.Register(domain.JobKindLesson, lessonLike(root, a, ai.Request{}, nil))
	start(t, r)
	id := enqueue(t, r, "topic:1")
	for range 2 {
		waitStatus(t, db, id, domain.JobStatusFailed)
		id, _, _ = retryOf(t, db, id)
	}
	if got := waitStatus(t, db, id, domain.JobStatusFailed); got.code != "rate_limited" {
		t.Fatalf("last retry: %+v", got)
	}
	time.Sleep(100 * time.Millisecond)
	var n int
	if err := db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil || n != 3 {
		t.Errorf("want 1 run + 2 automatic retries, got %d jobs (%v)", n, err)
	}
	// A manual retry still works and starts a fresh automatic count.
	if _, err := r.Retry(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestOtherFailureIsNotRetried(t *testing.T) {
	r, db, root := autoRunner(t, 10*time.Millisecond)
	r.Register(domain.JobKindLesson, lessonLike(root, &fake.Adapter{Scenario: fake.Missing}, ai.Request{}, nil))
	start(t, r)
	id := enqueue(t, r, "topic:1")
	if got := waitStatus(t, db, id, domain.JobStatusFailed); got.code != "adapter_missing" {
		t.Fatalf("got %+v", got)
	}
	time.Sleep(100 * time.Millisecond)
	var n int
	if err := db.Writer().QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil || n != 1 {
		t.Errorf("want no retry, got %d jobs (%v)", n, err)
	}
}

func TestCancelScheduledRetry(t *testing.T) {
	r, db, root := autoRunner(t, time.Hour)
	a := &fake.Adapter{Scenario: fake.RateLimited}
	r.Register(domain.JobKindLesson, lessonLike(root, a, ai.Request{}, nil))
	start(t, r)
	first := enqueue(t, r, "topic:1")
	waitStatus(t, db, first, domain.JobStatusFailed)
	next, _, _ := retryOf(t, db, first)
	if late, err := r.Cancel(context.Background(), next); err != nil || late {
		t.Fatalf("cancel: late=%v err=%v", late, err)
	}
	waitStatus(t, db, next, domain.JobStatusCanceled)
	// The scope is free again for the user.
	waitStatus(t, db, enqueue(t, r, "topic:1"), domain.JobStatusFailed)
	if len(a.Calls()) != 2 {
		t.Errorf("want the new job to run, got %d AI runs", len(a.Calls()))
	}
}

// T13.7: an automatic retry waiting for its delay does not block a tool switch; a due or running job does.
func TestWithIdleIgnoresWaitingRetry(t *testing.T) {
	r, db := newRunner(t)
	if _, err := db.Writer().Exec(`INSERT INTO jobs (kind, scope_key, status, not_before) VALUES ('lesson', 'topic:1', 'queued', '2999-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := r.WithIdle(context.Background(), func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("waiting retry blocked the switch: ran=%v err=%v", ran, err)
	}
	enqueue(t, r, "topic:2")
	if err := r.WithIdle(context.Background(), func() error { return nil }); !errors.Is(err, domain.ErrScopeBusy) {
		t.Fatalf("a due queued job must block: %v", err)
	}
}

// T13.7: a failed journal replay still queues the retry of the job it marked interrupted.
func TestRecoverReplayFailureKeepsAutoRetry(t *testing.T) {
	r, db, _ := autoRunner(t, time.Hour)
	res, err := db.Writer().Exec(`INSERT INTO jobs (kind, scope_key, status, started_at) VALUES ('lesson', 'topic:1', 'running', '2026-01-01T00:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	crashed, _ := res.LastInsertId()
	fail := true
	r.SetReplayFault(func() error {
		if fail {
			fail = false
			return errors.New("replay fault")
		}
		return nil
	})
	if _, err := r.Recover(context.Background()); err == nil {
		t.Fatal("want the replay error")
	}
	if _, err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, attempt, _ := retryOf(t, db, crashed); attempt != 1 {
		t.Fatalf("retry attempt %d", attempt)
	}
}
