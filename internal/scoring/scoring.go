// Package scoring computes exercise and topic scores deterministically (D9, section 5.3).
// Pure functions, no I/O. The topic score is never taken from the AI.
package scoring

import (
	"math"

	"learninghub/internal/schema"
)

const (
	Formula    = "sum(weight_i*score_i)/sum(weight_i); unanswered=0"
	ComputedBy = "app"
)

// ExerciseScore is the official score: the sum of awarded criterion points.
// mismatch reports that the AI's own score disagreed; callers record it as ai_reported_score.
func ExerciseScore(criteria []schema.CriteriaScore, aiReported int) (score int, mismatch bool) {
	for _, c := range criteria {
		score += c.Awarded
	}
	return score, score != aiReported
}

// Aggregate computes the topic score over all items (one per manifest exercise, carrying the
// manifest weight). Unanswered items count as 0 whatever their Score field says.
func Aggregate(items []schema.ResultItem) schema.ResultAggregate {
	var sum, total float64
	answered := 0
	for _, it := range items {
		total += it.Weight
		if it.Status == "unanswered" {
			continue
		}
		answered++
		sum += it.Weight * float64(it.Score)
	}
	ws := 0.0
	if total > 0 {
		ws = math.Round(sum/total*100) / 100
	}
	return schema.ResultAggregate{
		WeightedScore: ws,
		AnsweredCount: answered,
		TotalCount:    len(items),
		Partial:       answered < len(items),
		Formula:       Formula,
		ComputedBy:    ComputedBy,
	}
}
