package topics

import (
	"context"
	"database/sql"
	"errors"
)

// Resume is the "continue where you left off" card on /topics (T10.4).
type Resume struct {
	ID            int64
	Title, Status string
	Counts        map[string]int // active topics per status
}

// Touch records that the topic page was opened. No audit row: it is a view, not a change.
func (s *Service) Touch(ctx context.Context, id int64) error {
	_, err := s.db.Writer().ExecContext(ctx, `UPDATE topics SET last_opened_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, id)
	return err
}

// Resume returns the most recently opened active topic, or nil when none was ever opened.
func (s *Service) Resume(ctx context.Context) (*Resume, error) {
	r := &Resume{Counts: map[string]int{}}
	err := s.db.Writer().QueryRowContext(ctx, `SELECT id, title, status FROM topics
		WHERE state = 'active' AND last_opened_at IS NOT NULL ORDER BY last_opened_at DESC, id LIMIT 1`).Scan(&r.ID, &r.Title, &r.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Writer().QueryContext(ctx, `SELECT status, COUNT(*) FROM topics WHERE state = 'active' GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		r.Counts[st] = n
	}
	return r, rows.Err()
}
