package topics

import (
	"context"
	"database/sql"
	"errors"

	"learninghub/internal/domain"
)

// SetVersion is one exercise set of a topic (exercises tab, T4.5).
type SetVersion struct {
	ID, Version  int64
	Current      bool
	Model        string
	CreatedAt    string
	ExercisesRel string // exercises.md
}

// Exercise is one exercise of a set with its answer file. The rubric is never part of it (.internal stays hidden).
type Exercise struct {
	ExID, Title, Type, Difficulty string
	Weight                        float64
	AnswerFileID                  int64
	AnswerRel, Presence           string
	TemplateSHA                   string
	EvaluatedSHA                  string // answer hash at the latest evaluation; "" = never evaluated
}

// Answer statuses of the answers tab (R3.3).
const (
	AnswerMissing = "missing"
	AnswerEmpty   = "unanswered"
	AnswerDone    = "answered"
	AnswerChanged = "changed"
)

// AnswerStatus derives the status from the hash on disk now; diskSHA "" means the file is gone.
func (x *Exercise) AnswerStatus(diskSHA string) string {
	switch {
	case diskSHA == "":
		return AnswerMissing
	case diskSHA == x.TemplateSHA:
		return AnswerEmpty
	case x.EvaluatedSHA != "" && diskSHA != x.EvaluatedSHA:
		return AnswerChanged
	default:
		return AnswerDone
	}
}

// Sets returns the exercise sets of a topic, newest first.
func (s *Service) Sets(ctx context.Context, topicID int64) ([]SetVersion, error) {
	rows, err := s.db.Writer().QueryContext(ctx, `
		SELECT x.id, x.version, x.is_current, COALESCE(x.model, ''), x.created_at, f.rel_path
		FROM exercise_sets x JOIN files f ON f.id = x.exercises_file
		WHERE x.topic_id = ? ORDER BY x.version DESC`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SetVersion
	for rows.Next() {
		var v SetVersion
		if err := rows.Scan(&v.ID, &v.Version, &v.Current, &v.Model, &v.CreatedAt, &v.ExercisesRel); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Exercises returns the exercises of a set in order.
func (s *Service) Exercises(ctx context.Context, setID int64) ([]Exercise, error) {
	rows, err := s.db.Writer().QueryContext(ctx, `
		SELECT e.ex_id, e.title, e.type, e.difficulty, e.weight, f.id, f.rel_path, f.presence, e.template_sha256,
			COALESCE((SELECT i.answer_sha256 FROM attempt_items i JOIN attempts a ON a.id = i.attempt_id
				WHERE i.exercise_id = e.id AND i.status = 'evaluated' AND i.answer_sha256 IS NOT NULL
				ORDER BY a.attempt_no DESC LIMIT 1), '')
		FROM exercises e JOIN files f ON f.id = e.answer_file_id
		WHERE e.set_id = ? ORDER BY e.ord`, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exercise
	for rows.Next() {
		var x Exercise
		if err := rows.Scan(&x.ExID, &x.Title, &x.Type, &x.Difficulty, &x.Weight, &x.AnswerFileID, &x.AnswerRel, &x.Presence,
			&x.TemplateSHA, &x.EvaluatedSHA); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// File is a registered file as served by GET /files/{id}.
type File struct {
	RelPath, Kind string
	Locked        bool // a rubric before every exercise of its set was evaluated once
}

// File returns file id or domain.ErrNotFound.
func (s *Service) File(ctx context.Context, id int64) (*File, error) {
	f := &File{}
	err := s.db.Writer().QueryRowContext(ctx, `
		SELECT f.rel_path, f.kind, f.kind = 'rubric' AND (
			NOT EXISTS (SELECT 1 FROM exercise_sets x WHERE x.rubric_file = f.id)
			OR EXISTS (SELECT 1 FROM exercise_sets x JOIN exercises e ON e.set_id = x.id
				WHERE x.rubric_file = f.id AND NOT EXISTS (SELECT 1 FROM attempt_items i
					WHERE i.exercise_id = e.id AND i.status = 'evaluated')))
		FROM files f WHERE f.id = ?`, id).Scan(&f.RelPath, &f.Kind, &f.Locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return f, err
}
