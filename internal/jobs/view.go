package jobs

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"learninghub/internal/domain"
)

// Info is a job as the UI shows it (job card, TDD §7.5).
type Info struct {
	ID, TopicID                       int64
	Kind, Status, Phase               string
	Done, Total                       int
	ErrorCode, ErrorMessage           string
	CancelRequested                   bool
	EnqueuedAt, StartedAt, FinishedAt string
	AutoAttempt, AutoMax              int    // T10.8: automatic retry n of AutoMax; 0 = started by the user
	NotBefore                         string // a queued automatic retry waits until then
}

// Waiting is how long a queued automatic retry still waits, rounded to seconds; 0 if it may run now.
func (i *Info) Waiting() time.Duration {
	t, err := time.Parse(time.RFC3339Nano, i.NotBefore)
	if err != nil || i.Status != string(domain.JobStatusQueued) {
		return 0
	}
	return max(time.Until(t).Round(time.Second), 0)
}

// Active reports a queued or running job: polling continues and the scope's buttons stay disabled.
func (i *Info) Active() bool {
	return i.Status == string(domain.JobStatusQueued) || i.Status == string(domain.JobStatusRunning)
}

// cardTTL is how long a finished, non-failed job's card stays on its panel.
const cardTTL = 10 * time.Minute

// ShowCard reports whether the panel shows this job's card: active or failed jobs
// always, others only shortly after they finish (so a just-finished one is seen).
func (i *Info) ShowCard() bool {
	if i.Active() || i.Status == string(domain.JobStatusFailed) {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, i.FinishedAt)
	return err == nil && time.Since(t) < cardTTL
}

// Duration is the run time so far (or in total once finished), rounded to seconds; 0 if not started.
func (i *Info) Duration() time.Duration {
	start, err := time.Parse(time.RFC3339Nano, i.StartedAt)
	if err != nil {
		return 0
	}
	end := time.Now()
	if t, err := time.Parse(time.RFC3339Nano, i.FinishedAt); err == nil {
		end = t
	}
	return end.Sub(start).Round(time.Second)
}

const infoCols = `SELECT id, COALESCE(topic_id, 0), kind, status, COALESCE(phase, ''), progress_done, progress_total,
	COALESCE(error_code, ''), COALESCE(error_message, ''), cancel_requested, enqueued_at,
	COALESCE(started_at, ''), COALESCE(finished_at, ''), auto_attempt, COALESCE(not_before, '') FROM jobs `

func (r *Runner) scanInfo(ctx context.Context, where string, args ...any) (*Info, error) {
	var i Info
	err := r.db.Writer().QueryRowContext(ctx, infoCols+where, args...).Scan(&i.ID, &i.TopicID, &i.Kind, &i.Status,
		&i.Phase, &i.Done, &i.Total, &i.ErrorCode, &i.ErrorMessage, &i.CancelRequested, &i.EnqueuedAt, &i.StartedAt, &i.FinishedAt,
		&i.AutoAttempt, &i.NotBefore)
	i.AutoMax = r.AutoRetry
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err == nil && i.Status == string(domain.JobStatusRunning) {
		r.mu.Lock()
		if f, ok := r.finals[i.ID]; ok { // CR-38: the result is known but could not be written
			i.Status, i.ErrorCode, i.ErrorMessage, i.FinishedAt = f.Status, f.ErrorCode, f.ErrorMessage, f.FinishedAt
		}
		r.mu.Unlock()
	}
	return &i, err
}

// Get returns job id or domain.ErrNotFound.
func (r *Runner) Get(ctx context.Context, id int64) (*Info, error) {
	return r.scanInfo(ctx, `WHERE id = ?`, id)
}

// Latest returns the newest job of kind ("" = any kind) in scope, or nil if it never had one.
func (r *Runner) Latest(ctx context.Context, scope string, kind domain.JobKind) (*Info, error) {
	i, err := r.scanInfo(ctx, `WHERE scope_key = ? AND (? = '' OR kind = ?) ORDER BY id DESC LIMIT 1`, scope, kind, kind)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return i, err
}
