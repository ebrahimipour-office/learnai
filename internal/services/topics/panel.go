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
	Source                *Source         // chapter source text (§12.9); nil = topic has none
	Ancestors             []Ref           // tree rule: root first, ending at the parent (§13.6)
	Children              []Ref           // tree rule: direct sub-records in document order
}

// Ref links to another topic.
type Ref struct {
	ID       int64
	Title    string
	Archived bool
}

// Source is a chapter's source.md and its line range in the roadmap revision.
type Source struct {
	FileID     int64
	RelPath    string
	Start, End int64
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
	var srcID sql.NullInt64
	src := Source{}
	err := s.db.Writer().QueryRowContext(ctx, `
		SELECT t.title, COALESCE(g.title, ''), r.title, t.status, t.state, t.origin,
		       EXISTS (SELECT 1 FROM jobs j WHERE j.topic_id = t.id AND j.status IN ('queued','running')),
		       f.id, COALESCE(f.rel_path, ''), COALESCE(t.source_start_line, 0), COALESCE(t.source_end_line, 0)
		FROM topics t JOIN roadmaps r ON r.id = t.roadmap_id LEFT JOIN groups g ON g.id = t.group_id
		LEFT JOIN files f ON f.id = t.source_file_id
		WHERE t.id = ?`, id).Scan(&p.Title, &p.Group, &p.Roadmap, &p.Status, &p.State, &p.Origin, &p.Busy,
		&srcID, &src.RelPath, &src.Start, &src.End)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if srcID.Valid {
		src.FileID = srcID.Int64
		p.Source = &src
	}
	if err := s.refs(ctx, &p.Ancestors, `
		WITH RECURSIVE up(id, depth) AS (
		  SELECT parent_id, 1 FROM topics WHERE id = ? AND parent_id IS NOT NULL
		  UNION ALL SELECT t.parent_id, up.depth + 1 FROM topics t JOIN up ON t.id = up.id
		  WHERE t.parent_id IS NOT NULL AND up.depth < 64)
		SELECT t.id, t.title, t.state = 'archived' FROM up JOIN topics t ON t.id = up.id ORDER BY up.depth DESC`, id); err != nil {
		return nil, err
	}
	if err := s.refs(ctx, &p.Children, `
		SELECT id, title, state = 'archived' FROM topics WHERE parent_id = ? ORDER BY ord`, id); err != nil {
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

func (s *Service) refs(ctx context.Context, dst *[]Ref, query string, id int64) error {
	rows, err := s.db.Writer().QueryContext(ctx, query, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.ID, &r.Title, &r.Archived); err != nil {
			return err
		}
		*dst = append(*dst, r)
	}
	return rows.Err()
}
