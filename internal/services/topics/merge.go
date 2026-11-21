package topics

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"

	"learninghub/internal/domain"
	"learninghub/internal/services/roadmaps"
)

// emptyTopic holds for an active roadmap topic that nothing was ever generated, recorded or run for;
// only its chapter text (source.md, written by the import itself) may exist.
// shortcut: merging two topics that both have content is out of scope (T11.2); add a real merge if renames with history on both sides show up.
const emptyTopic = `t.origin = 'roadmap' AND t.state = 'active' AND t.status = 'created'
	AND NOT EXISTS (SELECT 1 FROM files WHERE topic_id = t.id AND kind <> 'source')
	AND NOT EXISTS (SELECT 1 FROM lesson_versions WHERE topic_id = t.id)
	AND NOT EXISTS (SELECT 1 FROM exercise_sets WHERE topic_id = t.id)
	AND NOT EXISTS (SELECT 1 FROM jobs WHERE topic_id = t.id)
	AND NOT EXISTS (SELECT 1 FROM suggestions WHERE topic_id = t.id OR accepted_topic_id = t.id)
	AND NOT EXISTS (SELECT 1 FROM lesson_qa WHERE topic_id = t.id)`

// MergeCandidate is an archived topic of the same roadmap that an empty topic can hand its place to.
type MergeCandidate struct {
	ID    int64
	Title string
}

// MergeCandidates lists, most similar title first, the archived roadmap topics of id's roadmap that
// Merge accepts for id; nil when id is not an empty topic (RK8, T11.2).
func (s *Service) MergeCandidates(ctx context.Context, id int64) ([]MergeCandidate, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT o.id, o.title, t.title FROM topics t
		JOIN topics o ON o.roadmap_id = t.roadmap_id AND o.origin = 'roadmap' AND o.state = 'archived'
		WHERE t.id = ? AND `+emptyTopic, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		MergeCandidate
		sim float64
	}
	var all []scored
	for rows.Next() {
		var c scored
		var title string
		if err := rows.Scan(&c.ID, &c.Title, &title); err != nil {
			return nil, err
		}
		c.sim = roadmaps.Similarity(title, c.Title)
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(all, func(a, b scored) int { return cmp.Compare(b.sim, a.sim) })
	var out []MergeCandidate
	for _, c := range all {
		out = append(out, c.MergeCandidate)
	}
	return out, nil
}

// Merge undoes a rename confirmed without a link: the empty topic target is deleted and the archived
// topic source takes its key, title, outline, place, parent and chapter range, and becomes active again,
// keeping its own id, folder and history. Target's source.md stays on disk untracked (nothing is deleted);
// source keeps its own source.md and source_sha256, so the next re-import refreshes it if the text changed. Anything else (target not empty, source not archived in the same
// roadmap) gives domain.ErrInvalidState and changes nothing.
func (s *Service) Merge(ctx context.Context, targetID, sourceID int64) error {
	return s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var key, title, titleNorm string
		var outline sql.NullString
		var revID, group, parent, start, end sql.NullInt64
		var ord float64
		err := tx.QueryRowContext(ctx, `SELECT t.stable_key, t.title, t.title_norm, t.outline, t.ord, t.group_id, t.parent_id, t.source_rev_id,
			t.source_start_line, t.source_end_line
			FROM topics t JOIN topics o ON o.id = ? AND o.roadmap_id = t.roadmap_id AND o.origin = 'roadmap' AND o.state = 'archived'
			WHERE t.id = ? AND t.id <> o.id AND `+emptyTopic, sourceID, targetID).
			Scan(&key, &title, &titleNorm, &outline, &ord, &group, &parent, &revID, &start, &end)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrInvalidState
		}
		if err != nil {
			return err
		}
		if parent.Int64 == sourceID {
			parent = sql.NullInt64{} // source was target's parent: it cannot hang from itself
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET parent_id = ? WHERE parent_id = ?`, sourceID, targetID); err != nil {
			return err
		}
		// delete first: (roadmap_id, stable_key) is unique and source takes target's key; the
		// source_file_id ↔ files.topic_id cycle is cut before the chapter rows go
		for _, q := range []string{`UPDATE topics SET source_file_id = NULL WHERE id = ?`,
			`DELETE FROM files WHERE topic_id = ? AND kind = 'source'`, `DELETE FROM topics WHERE id = ?`} {
			if _, err := tx.ExecContext(ctx, q, targetID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET stable_key = ?, title = ?, title_norm = ?, outline = ?, ord = ?,
			group_id = ?, parent_id = ?, source_rev_id = COALESCE(?, source_rev_id), source_start_line = ?, source_end_line = ?,
			state = 'active', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
			key, title, titleNorm, outline, ord, group, parent, revID, start, end, sourceID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_log (action, target_type, target_id, detail)
			VALUES ('topic_merged', 'topic', ?, json_object('deleted_topic_id', ?, 'new_key', ?, 'title', ?))`,
			sourceID, targetID, key, title)
		return err
	})
}
