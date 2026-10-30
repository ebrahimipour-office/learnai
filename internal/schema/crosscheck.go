package schema

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrNonContiguousOrder   = errors.New("exercise orders must be strictly sequential 1..N")
	ErrDuplicateExerciseID  = errors.New("duplicate exercise ID found")
	ErrExerciseCountBounds  = errors.New("exercise count outside allowed min/max bounds")
	ErrZeroWeightSum        = errors.New("sum of exercise weights must be greater than zero")
	ErrRubricPointsNot100   = errors.New("sum of criteria max_points for an exercise must equal exactly 100")
	ErrDuplicateCriterionID = errors.New("duplicate criterion ID within exercise")
	ErrIDMismatch           = errors.New("exercise IDs in manifest and rubric do not match")
	ErrMissingOutOfScope    = errors.New("external knowledge in exercise lacks corresponding out_of_scope_knowledge in rubric")
	ErrScoreMismatch        = errors.New("aggregated weighted score does not match calculated value")
	ErrPartialMismatch      = errors.New("partial flag mismatch: must be true if answered_count < total_count")
)

// CrossCheckManifest validates inter-field rules for manifest.json (Section 5.1).
func CrossCheckManifest(m *Manifest, allowedExts []string, minEx, maxEx int) error {
	count := len(m.Exercises)
	if count < minEx || count > maxEx {
		return fmt.Errorf("%w: got %d, want between %d and %d", ErrExerciseCountBounds, count, minEx, maxEx)
	}

	seenIDs := make(map[string]struct{})
	totalWeight := 0.0

	for i, ex := range m.Exercises {
		expectedOrder := i + 1
		if ex.Order != expectedOrder {
			return fmt.Errorf("%w: exercise %s has order %d, expected %d", ErrNonContiguousOrder, ex.ID, ex.Order, expectedOrder)
		}

		if _, exists := seenIDs[ex.ID]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateExerciseID, ex.ID)
		}
		seenIDs[ex.ID] = struct{}{}

		// Verify allowed extension
		if len(allowedExts) > 0 {
			extAllowed := false
			for _, a := range allowedExts {
				if strings.EqualFold(ex.Answer.Ext, a) {
					extAllowed = true
					break
				}
			}
			if !extAllowed {
				return fmt.Errorf("extension %s for %s is not in allowed extensions list", ex.Answer.Ext, ex.ID)
			}
		}

		expectedFileName := fmt.Sprintf("%s-%s%s", ex.ID, ex.Answer.Slug, ex.Answer.Ext)
		if ex.Answer.FileName != expectedFileName {
			return fmt.Errorf("file_name mismatch for %s: got %s, want %s", ex.ID, ex.Answer.FileName, expectedFileName)
		}

		totalWeight += ex.Weight
	}

	if totalWeight <= 0 {
		return ErrZeroWeightSum
	}

	return nil
}

// CrossCheckRubric verifies criteria point sums and ID uniqueness (Section 5.2).
func CrossCheckRubric(r *Rubric) error {
	for _, ex := range r.Exercises {
		seenCriteria := make(map[string]struct{})
		pointsSum := 0

		for _, c := range ex.Criteria {
			if _, exists := seenCriteria[c.ID]; exists {
				return fmt.Errorf("%w: %s in %s", ErrDuplicateCriterionID, c.ID, ex.ID)
			}
			seenCriteria[c.ID] = struct{}{}
			pointsSum += c.MaxPoints
		}

		if pointsSum != 100 {
			return fmt.Errorf("%w: exercise %s criteria sum to %d", ErrRubricPointsNot100, ex.ID, pointsSum)
		}
	}

	return nil
}

// CrossCheckManifestAndRubric guarantees 1-to-1 parity between manifest exercises and rubric entries (R3.4).
func CrossCheckManifestAndRubric(m *Manifest, r *Rubric) error {
	if len(m.Exercises) != len(r.Exercises) {
		return fmt.Errorf("%w: manifest has %d exercises, rubric has %d", ErrIDMismatch, len(m.Exercises), len(r.Exercises))
	}

	rubricExMap := make(map[string]RubricExercise)
	for _, rex := range r.Exercises {
		rubricExMap[rex.ID] = rex
	}

	for _, mex := range m.Exercises {
		rex, ok := rubricExMap[mex.ID]
		if !ok {
			return fmt.Errorf("%w: exercise %s in manifest missing from rubric", ErrIDMismatch, mex.ID)
		}

		// Check external knowledge parity
		if len(mex.ExternalKnowledge) > 0 {
			outMap := make(map[string]struct{})
			for _, o := range rex.OutOfScopeKnowledge {
				outMap[strings.ToLower(o.Topic)] = struct{}{}
			}
			for _, ek := range mex.ExternalKnowledge {
				if _, found := outMap[strings.ToLower(ek.Topic)]; !found {
					return fmt.Errorf("%w: exercise %s has external topic %q missing from rubric out_of_scope_knowledge",
						ErrMissingOutOfScope, mex.ID, ek.Topic)
				}
			}
		}
	}

	return nil
}

// CrossCheckResult verifies result items match the manifest and calculations are exact (D9, Section 5.3).
func CrossCheckResult(res *Result, m *Manifest) error {
	if len(res.Items) != len(m.Exercises) {
		return fmt.Errorf("result items count (%d) does not match manifest exercises count (%d)", len(res.Items), len(m.Exercises))
	}

	manifestWeights := make(map[string]float64)
	totalWeight := 0.0
	for _, ex := range m.Exercises {
		manifestWeights[ex.ID] = ex.Weight
		totalWeight += ex.Weight
	}

	weightedScoreSum := 0.0
	answeredCount := 0

	for _, item := range res.Items {
		w, ok := manifestWeights[item.ExerciseID]
		if !ok {
			return fmt.Errorf("result item %s not in manifest", item.ExerciseID)
		}
		if item.Weight != w {
			return fmt.Errorf("weight mismatch for %s: got %f, want %f", item.ExerciseID, item.Weight, w)
		}

		if item.Status != "unanswered" {
			answeredCount++
			weightedScoreSum += float64(item.Score) * item.Weight
		}
	}

	expectedWeightedScore := 0.0
	if totalWeight > 0 {
		expectedWeightedScore = math.Round((weightedScoreSum/totalWeight)*100) / 100
	}

	diff := math.Abs(res.Aggregate.WeightedScore - expectedWeightedScore)
	if diff > 0.01 {
		return fmt.Errorf("%w: recorded %f, calculated %f", ErrScoreMismatch, res.Aggregate.WeightedScore, expectedWeightedScore)
	}

	expectedPartial := answeredCount < len(m.Exercises)
	if res.Aggregate.Partial != expectedPartial {
		return fmt.Errorf("%w: got %v, expected %v", ErrPartialMismatch, res.Aggregate.Partial, expectedPartial)
	}

	return nil
}
