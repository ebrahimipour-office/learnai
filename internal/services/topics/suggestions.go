package topics

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/services/roadmaps"
)

// Suggestion is an AI prerequisite/supplementary suggestion of a topic (R8.3, T6.1).
type Suggestion struct {
	ID              int64
	Kind            string // prerequisite | supplementary
	Title, Reason   string
	Status          string // pending | accepted | rejected
	AcceptedTopicID int64  // 0 unless accepted
	CreatedAt       string
}

// Suggestions returns the suggestions of a topic, pending first, then newest first.
func (s *Service) Suggestions(ctx context.Context, topicID int64) ([]Suggestion, error) {
	rows, err := s.db.Writer().QueryContext(ctx, `
		SELECT id, kind, title, reason, status, COALESCE(accepted_topic_id, 0), created_at FROM suggestions
		WHERE topic_id = ? ORDER BY status <> 'pending', id DESC`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Suggestion
	for rows.Next() {
		var x Suggestion
		if err := rows.Scan(&x.ID, &x.Kind, &x.Title, &x.Reason, &x.Status, &x.AcceptedTopicID, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SuggestionTopic returns the source topic of suggestion id, or domain.ErrNotFound.
func (s *Service) SuggestionTopic(ctx context.Context, id int64) (int64, error) {
	var topicID int64
	err := s.db.Writer().QueryRowContext(ctx, `SELECT topic_id FROM suggestions WHERE id = ?`, id).Scan(&topicID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	return topicID, err
}

// AcceptSuggestion turns a pending suggestion into a topic with origin='suggested' in the source
// topic's roadmap and group, ordered right before (prerequisite) or after (supplementary) it.
// Roadmap topics keep their ord, so the roadmap's own order and structure never change (I9).
// Returns the new topic id; a decided suggestion gives domain.ErrInvalidState.
// shortcut: a re-import renumbers roadmap topics' ord, so an accepted topic may drift from its source; fix with T6.3/T6.4 if seen.
func (s *Service) AcceptSuggestion(ctx context.Context, id int64) (newID int64, err error) {
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var kind, title, titleNorm, status string
		var src, roadmapID int64
		var group sql.NullInt64
		var ord float64
		err := tx.QueryRowContext(ctx, `SELECT x.kind, x.title, x.title_norm, x.status, t.id, t.roadmap_id, t.group_id, t.ord
			FROM suggestions x JOIN topics t ON t.id = x.topic_id WHERE x.id = ?`, id).
			Scan(&kind, &title, &titleNorm, &status, &src, &roadmapID, &group, &ord)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != string(domain.SuggestionStatusPending) {
			return domain.ErrInvalidState
		}
		// Midpoint to the neighbour on that side (±1 when there is none) keeps every other ord as is.
		var near sql.NullFloat64
		q := `SELECT MAX(ord) FROM topics WHERE roadmap_id = ? AND ord < ?`
		edge := ord - 1
		if kind == string(domain.SuggestionKindSupplementary) {
			q, edge = `SELECT MIN(ord) FROM topics WHERE roadmap_id = ? AND ord > ?`, ord+1
		}
		if err := tx.QueryRowContext(ctx, q, roadmapID, ord).Scan(&near); err != nil {
			return err
		}
		if near.Valid {
			edge = near.Float64
		}
		seq, err := roadmaps.NextTopicSeq(ctx, tx, s.root)
		if err != nil {
			return err
		}
		slug, dir := roadmaps.TopicDir(seq, title)
		res, err := tx.ExecContext(ctx, `INSERT INTO topics (roadmap_id, group_id, stable_key, title, title_norm, ord, slug, dir_name, origin)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'suggested')`,
			roadmapID, group, fsx.HashString("suggestion:" + strconv.FormatInt(id, 10))[:16], title, titleNorm, (ord+edge)/2, slug, dir)
		if err != nil {
			return err
		}
		if newID, err = res.LastInsertId(); err != nil {
			return err
		}
		if err := decide(ctx, tx, id, domain.SuggestionStatusAccepted, newID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_log (action, target_type, target_id, detail)
			VALUES ('suggestion_accepted', 'suggestion', ?, json_object('source_topic_id', ?, 'topic_id', ?, 'kind', ?, 'title', ?))`,
			id, src, newID, kind, title)
		return err
	})
	return newID, err
}

// RejectSuggestion archives a pending suggestion (status rejected); it never becomes a topic.
func (s *Service) RejectSuggestion(ctx context.Context, id int64) error {
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := decide(ctx, tx, id, domain.SuggestionStatusRejected, 0); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO audit_log (action, target_type, target_id, detail)
			SELECT 'suggestion_rejected', 'suggestion', id, json_object('source_topic_id', topic_id, 'kind', kind, 'title', title)
			FROM suggestions WHERE id = ?`, id)
		return err
	})
}

// decide moves a pending suggestion to status; ErrNotFound / ErrInvalidState when it is missing or decided.
func decide(ctx context.Context, tx *sql.Tx, id int64, status domain.SuggestionStatus, topicID int64) error {
	res, err := tx.ExecContext(ctx, `UPDATE suggestions SET status = ?, accepted_topic_id = NULLIF(?, 0),
		decided_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ? AND status = 'pending'`, string(status), topicID, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM suggestions WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return domain.ErrInvalidState
}
