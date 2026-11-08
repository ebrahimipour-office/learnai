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
}

// Active reports a queued or running job: polling continues and the scope's buttons stay disabled.
func (i *Info) Active() bool {
	return i.Status == string(domain.JobStatusQueued) || i.Status == string(domain.JobStatusRunning)
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
	COALESCE(started_at, ''), COALESCE(finished_at, '') FROM jobs `

func (r *Runner) scanInfo(ctx context.Context, where string, args ...any) (*Info, error) {
	var i Info
	err := r.db.Writer().QueryRowContext(ctx, infoCols+where, args...).Scan(&i.ID, &i.TopicID, &i.Kind, &i.Status,
		&i.Phase, &i.Done, &i.Total, &i.ErrorCode, &i.ErrorMessage, &i.CancelRequested, &i.EnqueuedAt, &i.StartedAt, &i.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &i, err
}

// Get returns job id or domain.ErrNotFound.
func (r *Runner) Get(ctx context.Context, id int64) (*Info, error) {
	return r.scanInfo(ctx, `WHERE id = ?`, id)
}

// Latest returns the newest job of kind in scope, or nil if it never had one.
func (r *Runner) Latest(ctx context.Context, scope string, kind domain.JobKind) (*Info, error) {
	i, err := r.scanInfo(ctx, `WHERE scope_key = ? AND kind = ? ORDER BY id DESC LIMIT 1`, scope, kind)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	return i, err
}
