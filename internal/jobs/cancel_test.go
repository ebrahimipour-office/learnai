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
	for _, p := range []string{prompt.WorkRel(id), jobs.StagingRel(id), lessonRel} {
		if root.Exists(p) {
			t.Errorf("%s must not exist after cancel", p)
		}
	}
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
	if root.Exists(prompt.WorkRel(id)) || root.Exists(jobs.StagingRel(id)) {
		t.Error("succeeded job must clean work/ and staging/")
	}
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
