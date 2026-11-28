package topics

import "context"

// Question is one answered lesson question (lesson_qa, T10.21).
type Question struct {
	Question, AnswerMD string
	Version            int64 // lesson version it was asked about
	CreatedAt          string
}

// Questions lists the topic's newest limit answered questions, newest first; limit < 0 = all.
func (s *Service) Questions(ctx context.Context, topicID int64, limit int) ([]Question, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT q.question, q.answer_md, v.version, q.created_at
		FROM lesson_qa q JOIN lesson_versions v ON v.id = q.lesson_version_id WHERE q.topic_id = ? ORDER BY q.id DESC LIMIT ?`, topicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Question
	for rows.Next() {
		var q Question
		if err := rows.Scan(&q.Question, &q.AnswerMD, &q.Version, &q.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
