package topics

import (
	"context"
	"encoding/json"
)

// Attempt is one evaluation of a set (reports tab, T5.7). Score and counts are the app's (attempts row = result.json).
type Attempt struct {
	ID, No           int64
	Mode, FinishedAt string
	Score            float64
	Answered, Total  int
	Partial          bool
	ResultFileID     int64
	SummaryFileID    int64
}

// AttemptItem is one exercise of an attempt.
type AttemptItem struct {
	ExID, Title, Status string
	Score               int
	CarriedFrom         int64 // attempt_no the score was carried from; 0 = none
	ReportFileID        int64 // 0 = no report (unanswered)
	ReportRel           string
	Flags               []string
}

// Attempts returns the attempts of a set, newest first.
func (s *Service) Attempts(ctx context.Context, setID int64) ([]Attempt, error) {
	rows, err := s.db.Writer().QueryContext(ctx, `
		SELECT id, attempt_no, mode, finished_at, topic_score, answered_count, total_count, is_partial, result_file, summary_file
		FROM attempts WHERE set_id = ? ORDER BY attempt_no DESC`, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.ID, &a.No, &a.Mode, &a.FinishedAt, &a.Score, &a.Answered, &a.Total, &a.Partial,
			&a.ResultFileID, &a.SummaryFileID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttemptItems returns the items of an attempt in exercise order.
func (s *Service) AttemptItems(ctx context.Context, attemptID int64) ([]AttemptItem, error) {
	rows, err := s.db.Writer().QueryContext(ctx, `
		SELECT e.ex_id, e.title, i.status, i.score, COALESCE(c.attempt_no, 0), COALESCE(f.id, 0), COALESCE(f.rel_path, ''), i.flags
		FROM attempt_items i JOIN exercises e ON e.id = i.exercise_id
			LEFT JOIN attempts c ON c.id = i.carried_from_attempt
			LEFT JOIN files f ON f.id = i.report_file
		WHERE i.attempt_id = ? ORDER BY e.ord`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttemptItem
	for rows.Next() {
		var x AttemptItem
		var flags string
		if err := rows.Scan(&x.ExID, &x.Title, &x.Status, &x.Score, &x.CarriedFrom, &x.ReportFileID, &x.ReportRel, &flags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(flags), &x.Flags); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
