// Package jobs is the background job runner (TDD §6.4, §6.5): a FIFO queue in the jobs table,
// one dispatcher, a global semaphore and crash recovery. It only knows Handler; services register them.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"sync"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/store"
)

const (
	nowSQL = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	// sqlTime is nowSQL's layout in Go, so stored times compare as strings.
	sqlTime = "2006-01-02T15:04:05.000Z"
	// InterruptedMessage is shown when the app closed while the job ran (TDD §6.4).
	InterruptedMessage = "برنامه هنگام اجرا بسته شد؛ قابل تکرار است"
	// LateCancelMessage is recorded on a job that succeeded because a cancel reached it during commit.
	LateCancelMessage = "لغو دیر رسید؛ کار در مرحله ثبت بود و کامل شد"
)

// Job is what a Handler receives.
type Job struct {
	ID        int64
	Kind      domain.JobKind
	Scope     string // 'topic:12' | 'roadmap:3'
	TopicID   int64  // 0 = none
	RoadmapID int64  // 0 = none
	Params    json.RawMessage
	RetryOf   int64 // 0 = none

	r *Runner
}

// Commit runs fn as the job's commit phase (TDD §6.6). It refuses to start once a cancel was
// requested; after that fn runs with a context that is never cancelled, so a cancel arriving
// now is too late and the job still succeeds (no half-committed state).
func (j Job) Commit(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: before commit: %v", ai.ErrCanceled, err)
	}
	res, err := j.r.db.Writer().ExecContext(ctx,
		`UPDATE jobs SET phase = 'commit' WHERE id = ? AND status = 'running' AND cancel_requested = 0`, j.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: before commit", ai.ErrCanceled)
	}
	return fn(context.WithoutCancel(ctx))
}

// StagingRel is where a job builds its output before commit (TDD §6.6).
func StagingRel(jobID int64) string { return path.Join(".app/staging", strconv.FormatInt(jobID, 10)) }

// Handler does one job. Returning nil means the job succeeded; ctx is cancelled on shutdown.
type Handler func(ctx context.Context, j Job) error

// BusyError is returned by Enqueue when the scope already has a queued or running job.
type BusyError struct{ ActiveID int64 }

func (e *BusyError) Error() string {
	return fmt.Sprintf("%v (active job %d)", domain.ErrScopeBusy, e.ActiveID)
}
func (e *BusyError) Unwrap() error { return domain.ErrScopeBusy }

const finalWriteAttempts = 5

type Runner struct {
	db       *store.DB
	max      int
	handlers map[domain.JobKind]Handler
	wake     chan struct{}

	enqueueMu sync.RWMutex // CR-42: serialises Enqueue against WithIdle

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	finals  map[int64]Info // results whose final DB write failed (CR-38); overlaid by Get/Latest

	finalWriteFault func() error // test hook: a non-nil error fails one final-write attempt
	replayFault     func() error // test hook: a non-nil error fails Recover's journal replay

	Poll      time.Duration // dispatcher fallback tick (TDD §6.5: 2s)
	Heartbeat time.Duration // heartbeat_at update interval (TDD §6.5: 5s)
	// Files is the base dir; work/ and staging/ of succeeded and canceled jobs are removed from it.
	// Failed jobs keep them for diagnosis and retry. nil skips cleanup.
	Files *fsx.Root
	// AutoRetry is how many times in a row a job failed with rate_limited or interrupted is
	// queued again by itself, each RetryDelay after the failure (T10.8). 0 disables it.
	AutoRetry  int
	RetryDelay time.Duration
}

// autoRetryCodes are the failures that usually pass by waiting (T10.8); others need the user.
var autoRetryCodes = map[string]bool{"rate_limited": true, "interrupted": true}

// New returns a runner that runs at most maxConcurrent jobs at once (config jobs.max_concurrent).
func New(db *store.DB, maxConcurrent int) *Runner {
	return &Runner{
		db: db, max: max(maxConcurrent, 1), handlers: map[domain.JobKind]Handler{}, wake: make(chan struct{}, 1),
		running: map[int64]context.CancelFunc{}, finals: map[int64]Info{},
		finalWriteFault: func() error { return nil },
		replayFault:     func() error { return nil },
		Poll:            2 * time.Second, Heartbeat: 5 * time.Second,
	}
}

// Register sets the handler for kind. Call it before Run.
func (r *Runner) Register(kind domain.JobKind, h Handler) { r.handlers[kind] = h }

// Enqueue queues j (ID and RetryOf are ignored) and returns the new job id, or *BusyError.
func (r *Runner) Enqueue(ctx context.Context, j Job) (int64, error) {
	return r.enqueue(ctx, j, 0, 0)
}

// Retry queues a new job with retry_of = id; only failed jobs can be retried (TDD §6.4).
func (r *Runner) Retry(ctx context.Context, id int64) (int64, error) {
	return r.retry(ctx, id, false)
}

// retry queues a retry of failed job id; auto ones wait RetryDelay and stop after AutoRetry in a row (0, nil).
func (r *Runner) retry(ctx context.Context, id int64, auto bool) (int64, error) {
	var j Job
	var status, params string
	var topic, roadmap sql.NullInt64
	var attempt int
	err := r.db.Writer().QueryRowContext(ctx,
		`SELECT kind, scope_key, topic_id, roadmap_id, params, status, auto_attempt FROM jobs WHERE id = ?`, id).
		Scan(&j.Kind, &j.Scope, &topic, &roadmap, &params, &status, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if status != string(domain.JobStatusFailed) {
		return 0, fmt.Errorf("%w: job %d is %s", domain.ErrInvalidState, id, status)
	}
	j.TopicID, j.RoadmapID, j.Params = topic.Int64, roadmap.Int64, json.RawMessage(params)
	if !auto {
		return r.enqueue(ctx, j, id, 0)
	}
	if attempt >= r.AutoRetry {
		return 0, nil
	}
	return r.enqueue(ctx, j, id, attempt+1)
}

// autoRetry queues the automatic retry of a job that just failed with code (T10.8).
func (r *Runner) autoRetry(ctx context.Context, id int64, code string) {
	if r.AutoRetry < 1 || !autoRetryCodes[code] {
		return
	}
	if next, err := r.retry(ctx, id, true); err != nil {
		slog.Error("Queueing automatic retry failed", "job", id, "error", err)
	} else if next != 0 {
		slog.Info("Automatic retry queued", "job", id, "retry", next, "delay", r.RetryDelay)
	}
}

// WithIdle runs fn only if there are no running or due queued jobs, serialised against Enqueue (CR-42).
// An automatic retry still waiting for its not_before does not count (T13.7): switching the tool is
// exactly what a rate-limited user needs, and the retry then runs on the new tool.
// Otherwise it returns domain.ErrScopeBusy without calling fn.
func (r *Runner) WithIdle(ctx context.Context, fn func() error) error {
	r.enqueueMu.Lock()
	defer r.enqueueMu.Unlock()

	var busy int
	err := r.db.Writer().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE status = 'running'
			OR (status = 'queued' AND (not_before IS NULL OR not_before <= `+nowSQL+`))`).Scan(&busy)
	if err != nil {
		return err
	}
	if busy > 0 {
		return domain.ErrScopeBusy
	}
	return fn()
}

// enqueue inserts j; autoAttempt > 0 marks an automatic retry, not claimed before now + RetryDelay.
func (r *Runner) enqueue(ctx context.Context, j Job, retryOf int64, autoAttempt int) (id int64, err error) {
	if j.Scope == "" {
		return 0, fmt.Errorf("%w: empty job scope", domain.ErrInvalidInput)
	}
	if len(j.Params) == 0 {
		j.Params = json.RawMessage(`{}`)
	}
	r.enqueueMu.RLock()
	defer r.enqueueMu.RUnlock()

	err = r.db.WithTx(ctx, func(tx *sql.Tx) error {
		// Checked inside the immediate transaction; uq_jobs_active_scope stays the backstop (D8).
		var active int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE scope_key = ? AND status IN ('queued','running')`, j.Scope).Scan(&active)
		if err == nil {
			return &BusyError{ActiveID: active}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var notBefore any
		if autoAttempt > 0 {
			notBefore = time.Now().UTC().Add(r.RetryDelay).Format(sqlTime)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO jobs (kind, scope_key, topic_id, roadmap_id, status, params, retry_of, not_before, auto_attempt)
			VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
			j.Kind, j.Scope, nullID(j.TopicID), nullID(j.RoadmapID), string(j.Params), nullID(retryOf), notBefore, autoAttempt)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return id, nil
}

// Cancel stops job id (TDD §6.5): a queued job is canceled at once; a running one gets
// cancel_requested and its context cancelled, and the runner records canceled once the handler
// returns. late reports that the job was already committing, so it will still succeed.
func (r *Runner) Cancel(ctx context.Context, id int64) (late bool, err error) {
	var status string
	var phase sql.NullString
	err = r.db.WithTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT status, phase FROM jobs WHERE id = ?`, id).Scan(&status, &phase)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		switch domain.JobStatus(status) {
		case domain.JobStatusQueued:
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET status = 'canceled', cancel_requested = 1, finished_at = `+nowSQL+` WHERE id = ?`, id)
		case domain.JobStatusRunning:
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested = 1 WHERE id = ?`, id)
		default:
			err = fmt.Errorf("%w: job %d is %s", domain.ErrInvalidState, id, status)
		}
		return err
	})
	if err != nil || status != string(domain.JobStatusRunning) {
		return false, err
	}
	// Set in the DB first: execute re-reads the flag after registering, so a job claimed just now is not missed.
	r.mu.Lock()
	if cancel := r.running[id]; cancel != nil {
		cancel()
	}
	r.mu.Unlock()
	return phase.String == "commit", nil
}

// Recover marks jobs left running by a previous process as failed/interrupted; queued jobs stay queued.
// Then, when Files is set, it replays the commit journal of every job that did not succeed (CR-03).
func (r *Runner) Recover(ctx context.Context) (int64, error) {
	rows, err := r.db.Writer().QueryContext(ctx, `UPDATE jobs SET status = 'failed', error_code = 'interrupted',
		error_message = ?, finished_at = `+nowSQL+` WHERE status = 'running' RETURNING id`, InterruptedMessage)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	n := int64(len(ids))
	var rerr error
	if r.Files != nil {
		if rerr = r.replayFault(); rerr == nil {
			rerr = r.replayJournals(ctx)
		}
	}
	// Queued even when the replay failed (T13.7): these rows are no longer 'running', so the next Recover
	// would never see them. Run dispatches nothing until Recover succeeds, so a retry still never runs
	// on a half-replayed commit.
	for _, id := range ids {
		r.autoRetry(ctx, id, "interrupted")
	}
	return n, rerr
}

// Run recovers interrupted jobs (retrying until that works), then dispatches queued jobs in FIFO order until ctx is done.
// Jobs still running at that point are cancelled and recorded as failed/interrupted; Run returns
// after every worker has finished.
func (r *Runner) Run(ctx context.Context) error {
	// A failed Recover (e.g. the DB is locked) must not leave a runner that never runs the jobs the UI
	// keeps enqueueing (CR-37): retry with backoff. Recover is safe to repeat.
	for backoff := time.Second; ; backoff = min(2*backoff, 30*time.Second) {
		n, err := r.Recover(ctx)
		if err == nil {
			if n > 0 {
				slog.Warn("Marked jobs interrupted by the previous run as failed", "count", n)
			}
			break
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("Recovering jobs failed; retrying", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
	sem := make(chan struct{}, r.max)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(r.Poll)
	defer tick.Stop()
	for {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		j, err := r.claim(ctx)
		if j == nil {
			<-sem
			if err != nil && ctx.Err() == nil {
				slog.Error("Claiming next job failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-r.wake:
			case <-tick.C:
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.execute(ctx, *j)
		}()
	}
}

// claim moves the oldest queued job to running (CAS on status), or returns nil if there is none.
func (r *Runner) claim(ctx context.Context) (*Job, error) {
	for {
		var j Job
		var topic, roadmap, retry sql.NullInt64
		var params string
		// Order strictly by id for monotonic FIFO claiming (CR-I6).
		err := r.db.Writer().QueryRowContext(ctx, `SELECT id, kind, scope_key, topic_id, roadmap_id, params, retry_of
			FROM jobs WHERE status = 'queued' AND (not_before IS NULL OR not_before <= `+nowSQL+`) ORDER BY id LIMIT 1`).
			Scan(&j.ID, &j.Kind, &j.Scope, &topic, &roadmap, &params, &retry)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		res, err := r.db.Writer().ExecContext(ctx, `UPDATE jobs SET status = 'running', started_at = `+nowSQL+`,
			heartbeat_at = `+nowSQL+` WHERE id = ? AND status = 'queued'`, j.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			j.TopicID, j.RoadmapID, j.RetryOf, j.Params = topic.Int64, roadmap.Int64, retry.Int64, json.RawMessage(params)
			return &j, nil
		}
		// Lost the race (cancelled meanwhile): try the next one.
	}
}

func (r *Runner) execute(ctx context.Context, j Job) {
	slog.Info("Job started", "job", j.ID, "kind", j.Kind, "scope", j.Scope)
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	r.running[j.ID] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, j.ID)
		r.mu.Unlock()
	}()
	if r.cancelRequested(ctx, j.ID) {
		cancel()
	}
	go r.heartbeat(jobCtx, j.ID)

	j.r = r
	err := r.call(jobCtx, j)
	canceled := r.cancelRequested(context.WithoutCancel(ctx), j.ID)
	status, code, msg := domain.JobStatusSucceeded, "", ""
	switch {
	case err == nil:
		if canceled {
			msg = LateCancelMessage
		}
	case canceled:
		status = domain.JobStatusCanceled
	case ctx.Err() != nil: // shutdown (TDD §6.5): retryable like a crash
		status, code, msg = domain.JobStatusFailed, "interrupted", InterruptedMessage
	default:
		status, code, msg = domain.JobStatusFailed, ai.Code(err), err.Error()
		if code == "tool_error" && !errors.Is(err, ai.ErrToolFailed) {
			code = "internal" // not from the AI tool
		}
	}
	if r.Files != nil && status != domain.JobStatusFailed {
		work := prompt.WorkRel(j.ID)
		staging := StagingRel(j.ID)
		var errs []error
		if err := r.Files.EnsureRealDir(work); err == nil {
			if err := r.Files.RemoveAll(work); err != nil {
				errs = append(errs, err)
			}
		} else {
			slog.Error("Refusing to remove job work with symlinks", "job", j.ID, "error", err)
		}
		if err := r.Files.EnsureRealDir(staging); err == nil {
			if err := r.Files.RemoveAll(staging); err != nil {
				errs = append(errs, err)
			}
		} else {
			slog.Error("Refusing to remove job staging with symlinks", "job", j.ID, "error", err)
		}
		if len(errs) > 0 {
			slog.Error("Removing job work/staging failed", "job", j.ID, "error", errors.Join(errs...))
		}
	}
	// The final write must land even when ctx is already cancelled by shutdown.
	// A succeeded job's commit landed; its journal is no longer needed (CR-03).
	// CR-38: retried, because a job left 'running' would become failed/interrupted on the next start
	// even though its commit landed.
	wctx := context.WithoutCancel(ctx)
	var werr error
	for attempt := 0; attempt < finalWriteAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond << (attempt - 1))
		}
		if werr = r.finalWriteFault(); werr == nil {
			_, werr = r.db.Writer().ExecContext(wctx, `UPDATE jobs SET status = ?1, error_code = ?2,
				error_message = ?3, finished_at = `+nowSQL+`,
				commit_journal = CASE WHEN ?1 = 'succeeded' THEN NULL ELSE commit_journal END WHERE id = ?4`,
				status, nullStr(code), nullStr(msg), j.ID)
		}
		if werr == nil {
			break
		}
		slog.Error("Recording job result failed", "job", j.ID, "attempt", attempt+1, "error", werr)
	}
	if werr != nil {
		// shortcut: the row stays 'running' (scope busy) until restart, where Recover marks it interrupted;
		// upgrade to a background re-write if this ever shows up in practice.
		r.mu.Lock()
		r.finals[j.ID] = Info{Status: string(status), ErrorCode: code, ErrorMessage: msg,
			FinishedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		r.mu.Unlock()
	} else if status == domain.JobStatusFailed {
		r.autoRetry(wctx, j.ID, code)
	}
	slog.Info("Job finished", "job", j.ID, "status", status, "error_code", code)
}

func (r *Runner) cancelRequested(ctx context.Context, id int64) bool {
	var c bool
	if err := r.db.Writer().QueryRowContext(ctx, `SELECT cancel_requested FROM jobs WHERE id = ?`, id).Scan(&c); err != nil {
		slog.Error("Reading cancel_requested failed", "job", id, "error", err)
	}
	return c
}

// call runs the handler; a panic fails the job instead of the whole server.
func (r *Runner) call(ctx context.Context, j Job) (err error) {
	h := r.handlers[j.Kind]
	if h == nil {
		return fmt.Errorf("no handler registered for job kind %q", j.Kind)
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panic: %v", p)
		}
	}()
	return h(ctx, j)
}

func (r *Runner) heartbeat(ctx context.Context, id int64) {
	t := time.NewTicker(r.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.db.Writer().ExecContext(ctx, `UPDATE jobs SET heartbeat_at = `+nowSQL+` WHERE id = ? AND status = 'running'`, id); err != nil && ctx.Err() == nil {
				slog.Error("Job heartbeat failed", "job", id, "error", err)
			}
		}
	}
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
