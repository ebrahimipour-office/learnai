// Package status recomputes topics.status, a cache of what the records and the disk show (R2.3, T6.4).
package status

import (
	"context"
	"database/sql"
)

// Recompute sets every topic's status from its records, counting a file only while it is present:
// created → current lesson → content_ready → current exercises.md → exercises_ready →
// an answer that differs from its template → answered; an attempt on the current set → evaluated.
// It returns how many topics changed.
func Recompute(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE topics SET status = s.status, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		FROM (SELECT t.id, CASE
			WHEN NOT EXISTS (SELECT 1 FROM lesson_versions l JOIN files f ON f.id = l.file_id
				WHERE l.topic_id = t.id AND l.is_current = 1 AND f.presence = 'present') THEN 'created'
			WHEN NOT EXISTS (SELECT 1 FROM exercise_sets x JOIN files f ON f.id = x.exercises_file
				WHERE x.topic_id = t.id AND x.is_current = 1 AND f.presence = 'present') THEN 'content_ready'
			WHEN EXISTS (SELECT 1 FROM attempts a JOIN exercise_sets x ON x.id = a.set_id
				WHERE x.topic_id = t.id AND x.is_current = 1) THEN 'evaluated'
			WHEN EXISTS (SELECT 1 FROM exercises e JOIN exercise_sets x ON x.id = e.set_id JOIN files f ON f.id = e.answer_file_id
				WHERE x.topic_id = t.id AND x.is_current = 1 AND f.presence = 'present'
				AND COALESCE(f.observed_sha256, f.sha256) != e.template_sha256) THEN 'answered'
			ELSE 'exercises_ready' END AS status FROM topics t) s
		WHERE s.id = topics.id AND topics.status != s.status`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
