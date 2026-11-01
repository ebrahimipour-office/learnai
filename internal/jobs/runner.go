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
	"sync"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/domain"
	"learninghub/internal/store"
)

const (
	nowSQL = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	// InterruptedMessage is shown when the app closed while the job ran (TDD §6.4).
	InterruptedMessage = "برنامه هنگام اجرا بسته شد؛ قابل تکرار است"
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
}

// Handler does one job. Returning nil means the job succeeded; ctx is cancelled on shutdown.
type Handler func(ctx context.Context, j Job) error

// BusyError is returned by Enqueue when the scope already has a queued or running job.
type BusyError struct{ ActiveID int64 }

func (e *BusyError) Error() string {
	return fmt.Sprintf("%v (active job %d)", domain.ErrScopeBusy, e.ActiveID)
}
func (e *BusyError) Unwrap() error { return domain.ErrScopeBusy }

type Runner struct {
	db       *store.DB
	max      int
	handlers map[domain.JobKind]Handler
	wake     chan struct{}

	Poll      time.Duration // dispatcher fallback tick (TDD §6.5: 2s)
	Heartbeat time.Duration // heartbeat_at update interval (TDD §6.5: 5s)
}

// New returns a runner that runs at most maxConcurrent jobs at once (config jobs.max_concurrent).
func New(db *store.DB, maxConcurrent int) *Runner {
	return &Runner{
		db: db, max: max(maxConcurrent, 1), handlers: map[domain.JobKind]Handler{}, wake: make(chan struct{}, 1),
		Poll: 2 * time.Second, Heartbeat: 5 * time.Second,
	}
}

// Register sets the handler for kind. Call it before Run.
func (r *Runner) Register(kind domain.JobKind, h Handler) { r.handlers[kind] = h }

// Enqueue queues j (ID and RetryOf are ignored) and returns the new job id, or *BusyError.
func (r *Runner) Enqueue(ctx context.Context, j Job) (int64, error) {
	return r.enqueue(ctx, j, 0)
}

// Retry queues a new job with retry_of = id; only failed jobs can be retried (TDD §6.4).
func (r *Runner) Retry(ctx context.Context, id int64) (int64, error) {
	var j Job
	var status, params string
	var topic, roadmap sql.NullInt64
	err := r.db.Writer().QueryRowContext(ctx,
		`SELECT kind, scope_key, topic_id, roadmap_id, params, status FROM jobs WHERE id = ?`, id).
		Scan(&j.Kind, &j.Scope, &topic, &roadmap, &params, &status)
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
	return r.enqueue(ctx, j, id)
}

func (r *Runner) enqueue(ctx context.Context, j Job, retryOf int64) (id int64, err error) {
	if j.Scope == "" {
		return 0, fmt.Errorf("%w: empty job scope", domain.ErrInvalidInput)
	}
	if len(j.Params) == 0 {
		j.Params = json.RawMessage(`{}`)
	}
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
		res, err := tx.ExecContext(ctx,
			`INSERT INTO jobs (kind, scope_key, topic_id, roadmap_id, status, params, retry_of) VALUES (?, ?, ?, ?, 'queued', ?, ?)`,
			j.Kind, j.Scope, nullID(j.TopicID), nullID(j.RoadmapID), string(j.Params), nullID(retryOf))
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

// Recover marks jobs left running by a previous process as failed/interrupted; queued jobs stay queued.
func (r *Runner) Recover(ctx context.Context) (int64, error) {
	res, err := r.db.Writer().ExecContext(ctx, `UPDATE jobs SET status = 'failed', error_code = 'interrupted',
		error_message = ?, finished_at = `+nowSQL+` WHERE status = 'running'`, InterruptedMessage)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Run recovers interrupted jobs, then dispatches queued jobs in FIFO order until ctx is done.
// Jobs still running at that point are cancelled and recorded as failed/interrupted; Run returns
// after every worker has finished.
func (r *Runner) Run(ctx context.Context) error {
	if n, err := r.Recover(ctx); err != nil {
		return fmt.Errorf("recovering jobs: %w", err)
	} else if n > 0 {
		slog.Warn("Marked jobs interrupted by the previous run as failed", "count", n)
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
		err := r.db.Writer().QueryRowContext(ctx, `SELECT id, kind, scope_key, topic_id, roadmap_id, params, retry_of
			FROM jobs WHERE status = 'queued' ORDER BY enqueued_at, id LIMIT 1`).
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
	go r.heartbeat(jobCtx, j.ID)

	err := r.call(jobCtx, j)
	status, code, msg := domain.JobStatusSucceeded, "", ""
	switch {
	case err == nil:
	case ctx.Err() != nil: // shutdown (TDD §6.5): retryable like a crash
		status, code, msg = domain.JobStatusFailed, "interrupted", InterruptedMessage
	default:
		status, code, msg = domain.JobStatusFailed, ai.Code(err), err.Error()
		if code == "tool_error" && !errors.Is(err, ai.ErrToolFailed) {
			code = "internal" // not from the AI tool
		}
	}
	// The final write must land even when ctx is already cancelled by shutdown.
	_, werr := r.db.Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE jobs SET status = ?, error_code = ?,
		error_message = ?, finished_at = `+nowSQL+` WHERE id = ?`, status, nullStr(code), nullStr(msg), j.ID)
	if werr != nil {
		slog.Error("Recording job result failed", "job", j.ID, "error", werr)
	}
	slog.Info("Job finished", "job", j.ID, "status", status, "error_code", code)
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
