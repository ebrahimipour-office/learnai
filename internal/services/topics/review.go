package topics

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// ReviewTopic is an active topic whose latest evaluation of the current set is low or old (T10.17).
type ReviewTopic struct {
	ID         int64
	Title      string
	Score      float64
	FinishedAt string
	Low, Stale bool
	Errors     []ErrorCount // most frequent first
}

// ErrorCount is how many exercises an error type was reported on.
type ErrorCount struct {
	Type string // conceptual | logical | technical | style | omission
	N    int
}

// Review is the "for review" section of /topics.
type Review struct {
	Topics []ReviewTopic
	Errors []ErrorCount // over the latest attempts of all active topics
}

// Review lists topics whose latest attempt scored below scoreBelow or finished more than staleDays
// before now, low scores first (lowest first), then oldest. Error types count only latest attempts,
// so mistakes already fixed by a newer attempt drop out. Returns nil when no topic was ever evaluated.
func (s *Service) Review(ctx context.Context, scoreBelow float64, staleDays int, now time.Time) (*Review, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		WITH latest AS (
			SELECT t.id, t.title, a.id AS aid, a.topic_score, a.finished_at,
				ROW_NUMBER() OVER (PARTITION BY t.id ORDER BY a.attempt_no DESC) AS rn
			FROM topics t
			JOIN exercise_sets es ON es.topic_id = t.id AND es.is_current = 1
			JOIN attempts a ON a.set_id = es.id
			WHERE t.state = 'active')
		SELECT l.id, l.title, l.topic_score, l.finished_at, j.value, COUNT(j.value)
		FROM latest l
		LEFT JOIN attempt_items i ON i.attempt_id = l.aid
		LEFT JOIN json_each(i.error_types) j
		WHERE l.rn = 1
		GROUP BY l.id, j.value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cutoff := now.Add(-time.Duration(staleDays) * 24 * time.Hour)
	var all []*ReviewTopic
	byID := map[int64]*ReviewTopic{}
	total := map[string]int{}
	for rows.Next() {
		var x ReviewTopic
		var et *string
		var n int
		if err := rows.Scan(&x.ID, &x.Title, &x.Score, &x.FinishedAt, &et, &n); err != nil {
			return nil, err
		}
		t := byID[x.ID]
		if t == nil {
			t = &x
			fin, err := time.Parse(time.RFC3339Nano, x.FinishedAt)
			t.Low, t.Stale = x.Score < scoreBelow, err == nil && fin.Before(cutoff)
			byID[x.ID] = t
			all = append(all, t)
		}
		if et != nil {
			t.Errors = append(t.Errors, ErrorCount{*et, n})
			total[*et] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	r := &Review{}
	for et, n := range total {
		r.Errors = append(r.Errors, ErrorCount{et, n})
	}
	sortErrors(r.Errors)
	for _, t := range all {
		if t.Low || t.Stale {
			sortErrors(t.Errors)
			r.Topics = append(r.Topics, *t)
		}
	}
	slices.SortFunc(r.Topics, func(a, b ReviewTopic) int {
		if a.Low != b.Low {
			if a.Low {
				return -1
			}
			return 1
		}
		if a.Low {
			return cmp.Or(cmp.Compare(a.Score, b.Score), cmp.Compare(a.FinishedAt, b.FinishedAt))
		}
		return cmp.Compare(a.FinishedAt, b.FinishedAt)
	})
	return r, nil
}

func sortErrors(e []ErrorCount) {
	slices.SortFunc(e, func(a, b ErrorCount) int { return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(a.Type, b.Type)) })
}
