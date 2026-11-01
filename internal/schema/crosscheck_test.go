package schema_test

import (
	"encoding/json"
	"errors"
	"testing"

	"learninghub/internal/schema"
)

// samples returns fresh copies of the TDD §5.1/§5.2 examples, which must pass every check.
func samples(t *testing.T) (*schema.Manifest, *schema.Rubric) {
	t.Helper()
	var m schema.Manifest
	var r schema.Rubric
	if err := json.Unmarshal([]byte(sampleManifestJSON), &m); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(sampleRubricJSON), &r); err != nil {
		t.Fatal(err)
	}
	return &m, &r
}

var exts = []string{".go", ".md"}

func checkAll(m *schema.Manifest, r *schema.Rubric) error {
	if err := schema.CrossCheckManifest(m, exts, 1, 5); err != nil {
		return err
	}
	if err := schema.CrossCheckRubric(r); err != nil {
		return err
	}
	return schema.CrossCheckManifestAndRubric(m, r)
}

func TestCrossCheckSamplesPass(t *testing.T) {
	if err := checkAll(samples(t)); err != nil {
		t.Fatal(err)
	}
}

// One failing fixture per rule of TDD §5.1 (1–6) and §5.2 (T4.2).
func TestCrossCheckRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*schema.Manifest, *schema.Rubric)
		want   error
	}{
		{"1 duplicate id", func(m *schema.Manifest, r *schema.Rubric) { m.Exercises[1].ID = "q01" }, schema.ErrDuplicateExerciseID},
		{"1 order gap", func(m *schema.Manifest, r *schema.Rubric) { m.Exercises[1].Order = 3 }, schema.ErrNonContiguousOrder},
		{"1 order starts at 0", func(m *schema.Manifest, r *schema.Rubric) { m.Exercises[0].Order = 0 }, schema.ErrNonContiguousOrder},
		{"3 ext not allowed", func(m *schema.Manifest, r *schema.Rubric) {
			m.Exercises[0].Answer.Ext, m.Exercises[0].Answer.FileName = ".exe", "q01-hello-handler.exe"
		}, schema.ErrExtNotAllowed},
		{"3 file_name not computed", func(m *schema.Manifest, r *schema.Rubric) { m.Exercises[0].Answer.FileName = "evil.go" }, schema.ErrFileNameMismatch},
		{"4 too few", func(m *schema.Manifest, r *schema.Rubric) { m.Exercises = nil }, schema.ErrExerciseCountBounds},
		{"4 too many", func(m *schema.Manifest, r *schema.Rubric) {
			for i := 3; i <= 6; i++ {
				m.Exercises = append(m.Exercises, m.Exercises[0])
			}
		}, schema.ErrExerciseCountBounds},
		{"5 zero weight sum", func(m *schema.Manifest, r *schema.Rubric) { m.Exercises[0].Weight, m.Exercises[1].Weight = 0, 0 }, schema.ErrZeroWeightSum},
		{"6 rubric missing exercise", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises = r.Exercises[:1] }, schema.ErrIDMismatch},
		{"6 rubric has foreign id", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[1].ID = "q09" }, schema.ErrIDMismatch},
		{"6 rubric duplicates id", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[1].ID = "q01" }, schema.ErrIDMismatch},
		{"rubric points under 100", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[0].Criteria[0].MaxPoints = 29 }, schema.ErrRubricPointsNot100},
		{"rubric points over 100", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[1].Criteria[0].MaxPoints = 51 }, schema.ErrRubricPointsNot100},
		{"rubric no criteria", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[0].Criteria = nil }, schema.ErrRubricPointsNot100},
		{"rubric duplicate criterion", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[0].Criteria[1].ID = "c1" }, schema.ErrDuplicateCriterionID},
		{"external knowledge without out_of_scope", func(m *schema.Manifest, r *schema.Rubric) { r.Exercises[1].OutOfScopeKnowledge = nil }, schema.ErrMissingOutOfScope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, r := samples(t)
			tc.mutate(m, r)
			if err := checkAll(m, r); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
