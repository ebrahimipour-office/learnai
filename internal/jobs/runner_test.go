package jobs_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/domain"
	"learninghub/internal/jobs"
	"learninghub/internal/store"
)

func newRunner(t *testing.T) (*jobs.Runner, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := jobs.New(db, 1)
	r.Poll, r.Heartbeat = 20*time.Millisecond, 20*time.Millisecond
	return r, db
}

// start runs r in the background; the returned stop cancels it and waits for Run to return.
func start(t *testing.T, r *jobs.Runner) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

type row struct {
	status, code, msg string
	retryOf           int64
	started, finished bool
}

func get(t *testing.T, db *store.DB, id int64) row {
	t.Helper()
	var r row
	var code, msg, started, finished *string
	var retry *int64
	err := db.Writer().QueryRow(`SELECT status, error_code, error_message, retry_of, started_at, finished_at FROM jobs WHERE id = ?`, id).
		Scan(&r.status, &code, &msg, &retry, &started, &finished)
	if err != nil {
		t.Fatal(err)
	}
	if code != nil {
		r.code = *code
	}
	if msg != nil {
		r.msg = *msg
	}
	if retry != nil {
		r.retryOf = *retry
	}
	r.started, r.finished = started != nil, finished != nil
	return r
}

func waitStatus(t *testing.T, db *store.DB, id int64, want domain.JobStatus) row {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := get(t, db, id)
		if r.status == string(want) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %d: want %s, still %s", id, want, r.status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func enqueue(t *testing.T, r *jobs.Runner, scope string) int64 {
	t.Helper()
	id, err := r.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEnqueueSameScopeIsBusy(t *testing.T) {
	r, db := newRunner(t)
	first := enqueue(t, r, "topic:1")

	_, err := r.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindExercises, Scope: "topic:1"})
	var busy *jobs.BusyError
	if !errors.Is(err, domain.ErrScopeBusy) || !errors.As(err, &busy) || busy.ActiveID != first {
		t.Fatalf("want ErrScopeBusy with active job %d, got %v", first, err)
	}
	if _, err := r.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: "topic:2"}); err != nil {
		t.Fatalf("other scope must enqueue: %v", err)
	}

	// Once the first job is done, the scope is free again.
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { return nil })
	start(t, r)
	waitStatus(t, db, first, domain.JobStatusSucceeded)
	enqueue(t, r, "topic:1")
}

func TestFIFOWithOneSlot(t *testing.T) {
	r, db := newRunner(t)
	var mu sync.Mutex
	var order []string
	var inFlight, peak atomic.Int32
	r.Register(domain.JobKindLesson, func(_ context.Context, j jobs.Job) error {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		order = append(order, j.Scope)
		mu.Unlock()
		return nil
	})
	scopes := []string{"topic:3", "topic:1", "topic:2"}
	var ids []int64
	for _, s := range scopes {
		ids = append(ids, enqueue(t, r, s))
	}
	start(t, r)
	for _, id := range ids {
		waitStatus(t, db, id, domain.JobStatusSucceeded)
	}
	if peak.Load() != 1 {
		t.Errorf("max_concurrent=1 but %d jobs ran at once", peak.Load())
	}
	for i := range scopes {
		if order[i] != scopes[i] {
			t.Fatalf("want FIFO %v, got %v", scopes, order)
		}
	}
}

func TestFailureIsRecorded(t *testing.T) {
	r, db := newRunner(t)
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { return errors.New("disk full") })
	r.Register(domain.JobKindExercises, func(context.Context, jobs.Job) error { panic("boom") })
	failed := enqueue(t, r, "topic:1")
	panicked, _ := r.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindExercises, Scope: "topic:2"})
	unhandled, _ := r.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindEvaluate, Scope: "topic:3"})
	start(t, r)

	if got := waitStatus(t, db, failed, domain.JobStatusFailed); got.code != "internal" || got.msg != "disk full" || !got.finished {
		t.Errorf("handler error: got %+v", got)
	}
	if got := waitStatus(t, db, panicked, domain.JobStatusFailed); got.code != "internal" {
		t.Errorf("panic: got %+v", got)
	}
	waitStatus(t, db, unhandled, domain.JobStatusFailed)
}

// Simulates the app dying mid-job: a row is left running. On the next start it becomes
// failed/interrupted and can be retried; queued jobs carry on.
func TestRecoveryAfterCrash(t *testing.T) {
	r, db := newRunner(t)
	res, err := db.Writer().Exec(`INSERT INTO jobs (kind, scope_key, status, started_at) VALUES ('lesson', 'topic:1', 'running', '2026-01-01T00:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	crashed, _ := res.LastInsertId()
	queued := enqueue(t, r, "topic:2")
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { return nil })

	if _, err := r.Retry(context.Background(), crashed); !errors.Is(err, domain.ErrInvalidState) {
		t.Fatalf("retry of a running job: want ErrInvalidState, got %v", err)
	}
	start(t, r)

	got := waitStatus(t, db, crashed, domain.JobStatusFailed)
	if got.code != "interrupted" || got.msg != jobs.InterruptedMessage || !got.finished {
		t.Errorf("crashed job: got %+v", got)
	}
	waitStatus(t, db, queued, domain.JobStatusSucceeded)

	retry, err := r.Retry(context.Background(), crashed)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitStatus(t, db, retry, domain.JobStatusSucceeded); got.retryOf != crashed {
		t.Errorf("retry_of: want %d, got %d", crashed, got.retryOf)
	}
	if got := get(t, db, crashed); got.status != "failed" {
		t.Errorf("history must not change: %+v", got)
	}
}

func TestShutdownInterruptsRunningJob(t *testing.T) {
	r, db := newRunner(t)
	started := make(chan struct{})
	r.Register(domain.JobKindLesson, func(ctx context.Context, _ jobs.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	id := enqueue(t, r, "topic:1")
	waiting := enqueue(t, r, "topic:2")
	stop := start(t, r)
	<-started
	waitStatus(t, db, id, domain.JobStatusRunning)
	time.Sleep(60 * time.Millisecond) // a few heartbeats

	stop()
	if got := get(t, db, id); got.status != "failed" || got.code != "interrupted" {
		t.Errorf("running job on shutdown: got %+v", got)
	}
	if got := get(t, db, waiting); got.status != "queued" || got.started {
		t.Errorf("queued job must stay queued: %+v", got)
	}
}

func TestHeartbeat(t *testing.T) {
	r, db := newRunner(t)
	release := make(chan struct{})
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { <-release; return nil })
	id := enqueue(t, r, "topic:1")
	start(t, r)
	waitStatus(t, db, id, domain.JobStatusRunning)

	beat := func() (s string) {
		_ = db.Writer().QueryRow(`SELECT heartbeat_at FROM jobs WHERE id = ?`, id).Scan(&s)
		return s
	}
	first := beat()
	deadline := time.Now().Add(2 * time.Second)
	for beat() == first {
		if time.Now().After(deadline) {
			t.Fatal("heartbeat_at never updated")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	waitStatus(t, db, id, domain.JobStatusSucceeded)
}

// CR-37: a Recover that fails at startup (DB locked past busy_timeout) is retried, not fatal.
func TestRunRetriesRecover(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a DB lock for 6s")
	}
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := jobs.New(db, 1)
	r.Poll = 20 * time.Millisecond
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { return nil })
	id := enqueue(t, r, "topic:1")

	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	start(t, r)
	time.Sleep(6 * time.Second) // longer than busy_timeout: the first Recover fails
	if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); get(t, db, id).status != "succeeded"; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("job never ran: %+v", get(t, db, id))
		}
	}
}

// CR-38: the final status write is retried; if it never lands, Get still reports the real result.
func TestFinalWriteIsRetried(t *testing.T) {
	r, db := newRunner(t)
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { return nil })
	var calls atomic.Int32
	r.SetFinalWriteFault(func() error {
		if calls.Add(1) <= 2 {
			return errors.New("database is locked")
		}
		return nil
	})
	id := enqueue(t, r, "topic:1")
	start(t, r)
	if got := waitStatus(t, db, id, domain.JobStatusSucceeded); !got.finished {
		t.Errorf("got %+v", got)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("write attempts = %d, want 3", n)
	}
}

func TestFinalWriteFailureIsVisible(t *testing.T) {
	r, db := newRunner(t)
	r.Register(domain.JobKindLesson, func(context.Context, jobs.Job) error { return errors.New("disk full") })
	var calls atomic.Int32
	r.SetFinalWriteFault(func() error { calls.Add(1); return errors.New("database is locked") })
	id := enqueue(t, r, "topic:1")
	start(t, r)
	deadline := time.Now().Add(5 * time.Second)
	for {
		i, err := r.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if i.Status == string(domain.JobStatusFailed) {
			if i.ErrorMessage != "disk full" || i.FinishedAt == "" || i.Active() {
				t.Errorf("got %+v", i)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Get still reports %s", i.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 5 {
		t.Errorf("write attempts = %d, want 5", n)
	}
	if got := get(t, db, id); got.status != string(domain.JobStatusRunning) {
		t.Errorf("row: got %+v", got)
	}
}

type fakeSwitchAdapter struct {
	name string
}

func (a fakeSwitchAdapter) Name() string { return a.name }
func (a fakeSwitchAdapter) Probe(context.Context) (ai.Capabilities, error) {
	return ai.Capabilities{}, nil
}
func (a fakeSwitchAdapter) Run(context.Context, ai.Request, chan<- ai.Event) (*ai.Response, error) {
	return &ai.Response{Text: a.name}, nil
}

// CR-42: concurrent enqueue and switch with a barrier under -race -> the recorded adapter equals the adapter that ran.
func TestCR42_ConcurrentEnqueueAndSwitchWithIdle(t *testing.T) {
	sw := ai.NewSwitch("toolA", fakeSwitchAdapter{name: "toolA"}, fakeSwitchAdapter{name: "toolB"})

	for i := 0; i < 20; i++ {
		r, db := newRunner(t)
		_ = sw.Set("toolA") // reset to initial

		var mu sync.Mutex
		var ranTool string

		r.Register(domain.JobKindLesson, func(ctx context.Context, j jobs.Job) error {
			currentName := sw.Name()
			if _, err := db.Writer().Exec(`UPDATE jobs SET adapter = ? WHERE id = ?`, currentName, j.ID); err != nil {
				return err
			}
			resp, err := sw.Run(ctx, ai.Request{}, nil)
			if err != nil {
				return err
			}
			mu.Lock()
			ranTool = resp.Text
			mu.Unlock()
			return nil
		})

		start(t, r)

		var jobID int64
		var switchErr error
		var wg sync.WaitGroup
		barrier := make(chan struct{})

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-barrier
			id, err := r.Enqueue(context.Background(), jobs.Job{Kind: domain.JobKindLesson, Scope: "topic:1"})
			if err == nil {
				atomic.StoreInt64(&jobID, id)
			}
		}()

		go func() {
			defer wg.Done()
			<-barrier
			switchErr = r.WithIdle(context.Background(), func() error {
				return sw.Set("toolB")
			})
		}()

		close(barrier)
		wg.Wait()

		id := atomic.LoadInt64(&jobID)
		if id > 0 {
			waitStatus(t, db, id, domain.JobStatusSucceeded)

			var recordedAdapter string
			err := db.Writer().QueryRow(`SELECT adapter FROM jobs WHERE id = ?`, id).Scan(&recordedAdapter)
			if err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			ran := ranTool
			mu.Unlock()

			if recordedAdapter != ran {
				t.Fatalf("iteration %d: recorded adapter %q != adapter that ran %q", i, recordedAdapter, ran)
			}

			if switchErr == nil {
				// WithIdle succeeded, meaning it executed before Enqueue
				if recordedAdapter != "toolB" {
					t.Fatalf("iteration %d: switch succeeded but job recorded %q", i, recordedAdapter)
				}
			} else {
				// WithIdle was refused because Enqueue executed first
				if !errors.Is(switchErr, domain.ErrScopeBusy) {
					t.Fatalf("iteration %d: unexpected switch error: %v", i, switchErr)
				}
				if recordedAdapter != "toolA" {
					t.Fatalf("iteration %d: switch was refused but job recorded %q", i, recordedAdapter)
				}
			}
		}
	}
}
