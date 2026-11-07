package scoring_test

import (
	"testing"

	"learninghub/internal/schema"
	"learninghub/internal/scoring"
)

func it(w float64, status string, score int) schema.ResultItem {
	return schema.ResultItem{ExerciseID: "q01", Weight: w, Status: status, Score: score}
}

const (
	ev = "evaluated"
	co = "carried_over"
	un = "unanswered"
)

func TestAggregate(t *testing.T) {
	cases := []struct {
		name     string
		items    []schema.ResultItem
		ws       float64
		answered int
		partial  bool
	}{
		{"single full", []schema.ResultItem{it(1, ev, 100)}, 100, 1, false},
		{"single zero", []schema.ResultItem{it(1, ev, 0)}, 0, 1, false},
		{"equal weights", []schema.ResultItem{it(1, ev, 80), it(1, ev, 60)}, 70, 2, false},
		{"unequal weights", []schema.ResultItem{it(1, ev, 100), it(3, ev, 0)}, 25, 2, false},
		{"doc example", []schema.ResultItem{it(1, ev, 90), it(2, co, 70), it(1, un, 0)}, 57.5, 2, true},
		{"all unanswered", []schema.ResultItem{it(1, un, 0), it(2, un, 0)}, 0, 0, true},
		{"unanswered forced to zero", []schema.ResultItem{it(1, ev, 100), it(1, un, 90)}, 50, 1, true},
		{"all carried over", []schema.ResultItem{it(2, co, 40), it(2, co, 60)}, 50, 2, false},
		{"carried and evaluated", []schema.ResultItem{it(1, co, 100), it(1, ev, 50)}, 75, 2, false},
		{"round down 2dp", []schema.ResultItem{it(1, ev, 100), it(1, ev, 0), it(1, ev, 0)}, 33.33, 3, false},
		{"round up 2dp", []schema.ResultItem{it(1, ev, 100), it(1, ev, 100), it(1, ev, 0)}, 66.67, 3, false},
		{"fractional weights", []schema.ResultItem{it(0.5, ev, 80), it(1.5, ev, 40)}, 50, 2, false},
		{"heavy weight dominates", []schema.ResultItem{it(1000, ev, 10), it(1, ev, 100)}, 10.09, 2, false},
		{"one of five answered", []schema.ResultItem{it(1, ev, 100), it(1, un, 0), it(1, un, 0), it(1, un, 0), it(1, un, 0)}, 20, 1, true},
		{"three of five answered", []schema.ResultItem{it(1, ev, 90), it(1, co, 80), it(1, ev, 70), it(1, un, 0), it(1, un, 0)}, 48, 3, true},
		{"empty", nil, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := scoring.Aggregate(c.items)
			if a.WeightedScore != c.ws || a.AnsweredCount != c.answered || a.Partial != c.partial || a.TotalCount != len(c.items) {
				t.Fatalf("got %+v, want ws=%v answered=%d partial=%v total=%d", a, c.ws, c.answered, c.partial, len(c.items))
			}
			if a.Formula != scoring.Formula || a.ComputedBy != "app" {
				t.Fatalf("formula/computed_by: %+v", a)
			}
		})
	}
}

// Aggregate must agree with the independent check in schema.CrossCheckResult.
func TestAggregatePassesCrossCheck(t *testing.T) {
	m := &schema.Manifest{Exercises: []schema.ManifestExercise{{ID: "q01", Weight: 1}, {ID: "q02", Weight: 2}, {ID: "q03", Weight: 1}}}
	res := &schema.Result{Items: []schema.ResultItem{
		{ExerciseID: "q01", Weight: 1, Status: ev, Score: 90},
		{ExerciseID: "q02", Weight: 2, Status: co, Score: 70},
		{ExerciseID: "q03", Weight: 1, Status: un, Score: 0},
	}}
	res.Aggregate = scoring.Aggregate(res.Items)
	if err := schema.CrossCheckResult(res, m); err != nil {
		t.Fatal(err)
	}
}

func TestExerciseScore(t *testing.T) {
	cs := []schema.CriteriaScore{{CriterionID: "c1", Awarded: 30, Max: 30}, {CriterionID: "c2", Awarded: 25, Max: 40}, {CriterionID: "c3", Awarded: 0, Max: 30}}
	cases := []struct {
		name     string
		criteria []schema.CriteriaScore
		ai       int
		score    int
		mismatch bool
	}{
		{"ai agrees", cs, 55, 55, false},
		{"ai inflates", cs, 90, 55, true},
		{"ai deflates", cs, 10, 55, true},
		{"all zero", []schema.CriteriaScore{{CriterionID: "c1", Max: 100}}, 0, 0, false},
		{"full marks", []schema.CriteriaScore{{CriterionID: "c1", Awarded: 60, Max: 60}, {CriterionID: "c2", Awarded: 40, Max: 40}}, 100, 100, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, mm := scoring.ExerciseScore(c.criteria, c.ai)
			if s != c.score || mm != c.mismatch {
				t.Fatalf("got (%d,%v), want (%d,%v)", s, mm, c.score, c.mismatch)
			}
		})
	}
}
