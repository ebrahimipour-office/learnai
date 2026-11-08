package topics

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"learninghub/internal/domain"
)

// Panel is one topic with its lesson versions (topic panel, T3.8).
type Panel struct {
	ID                    int64
	Title, Group, Roadmap string
	Status, State, Origin string
	Busy                  bool            // has a queued or running job of any kind: every action button is disabled
	Lessons               []LessonVersion // newest first
}

type LessonVersion struct {
	Version   int64
	RelPath   string // lesson.md for the current version, lesson/history/... for older ones
	Current   bool
	Model     string
	CreatedAt string
}

// Scope is the job scope key of a topic.
func Scope(id int64) string { return "topic:" + strconv.FormatInt(id, 10) }

// Panel returns topic id or domain.ErrNotFound.
func (s *Service) Panel(ctx context.Context, id int64) (*Panel, error) {
	p := &Panel{ID: id}
	err := s.db.Writer().QueryRowContext(ctx, `
		SELECT t.title, COALESCE(g.title, ''), r.title, t.status, t.state, t.origin,
		       EXISTS (SELECT 1 FROM jobs j WHERE j.topic_id = t.id AND j.status IN ('queued','running'))
		FROM topics t JOIN roadmaps r ON r.id = t.roadmap_id LEFT JOIN groups g ON g.id = t.group_id
		WHERE t.id = ?`, id).Scan(&p.Title, &p.Group, &p.Roadmap, &p.Status, &p.State, &p.Origin, &p.Busy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Writer().QueryContext(ctx, `
		SELECT v.version, f.rel_path, v.is_current, COALESCE(v.model, ''), v.created_at
		FROM lesson_versions v JOIN files f ON f.id = v.file_id
		WHERE v.topic_id = ? ORDER BY v.version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v LessonVersion
		if err := rows.Scan(&v.Version, &v.RelPath, &v.Current, &v.Model, &v.CreatedAt); err != nil {
			return nil, err
		}
		p.Lessons = append(p.Lessons, v)
	}
	return p, rows.Err()
}
