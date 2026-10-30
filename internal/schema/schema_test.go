package schema_test

import (
	"encoding/json"
	"testing"

	"learninghub/internal/schema"
)

const sampleManifestJSON = `{
  "schema_version": 1,
  "topic": { "stable_key": "a3f9c2d41b7e8f06", "title": "مبانی HTTP در Go" },
  "set": {
    "version": 1,
    "rubric_version": 1,
    "created_at": "2026-10-30T09:15:00Z",
    "generator": { "adapter": "antigravity", "model": "gemini-3.8-flash-medium",
                   "skill": "exercise-generator", "skill_version": "1.0.0", "job_id": 42 }
  },
  "exercises": [
    { "id": "q01", "order": 1, "title": "نوشتن Handler ساده", "type": "coding",
      "difficulty": "easy", "weight": 1,
      "answer": { "slug": "hello-handler", "ext": ".go", "language": "go",
                  "file_name": "q01-hello-handler.go" },
      "external_knowledge": [] },
    { "id": "q02", "order": 2, "title": "نقش context در درخواست", "type": "conceptual",
      "difficulty": "medium", "weight": 2,
      "answer": { "slug": "explain-context", "ext": ".md", "file_name": "q02-explain-context.md" },
      "external_knowledge": [ { "topic": "goroutine", "note": "در درس قبلی تدریس شده است" } ] }
  ]
}`

const sampleRubricJSON = `{
  "schema_version": 1,
  "rubric_version": 1,
  "set_version": 1,
  "topic_stable_key": "a3f9c2d41b7e8f06",
  "exercises": [
    {
      "id": "q01",
      "reference_answer": {
        "format": "code",
        "language": "go",
        "content": "func hello(w http.ResponseWriter, r *http.Request) {\n\tfmt.Fprint(w, \"hello\")\n}"
      },
      "criteria": [
        { "id": "c1", "description": "امضای صحیح Handler", "max_points": 30, "category": "correctness" },
        { "id": "c2", "description": "نوشتن پاسخ روی ResponseWriter", "max_points": 40, "category": "correctness" },
        { "id": "c3", "description": "ثبت Handler روی mux", "max_points": 30, "category": "completeness" }
      ],
      "common_errors": [ { "type": "technical", "description": "فراموشی import fmt" } ],
      "out_of_scope_knowledge": []
    },
    {
      "id": "q02",
      "reference_answer": {
        "format": "text",
        "language": "markdown",
        "content": "کانتکست برای لغو و مدیریت چرخه حیات درخواست استفاده می‌شود."
      },
      "criteria": [
        { "id": "c1", "description": "توضیح لغو و مهلت زمانی", "max_points": 50, "category": "concept" },
        { "id": "c2", "description": "انتقال مقادیر درخواست", "max_points": 50, "category": "concept" }
      ],
      "common_errors": [],
      "out_of_scope_knowledge": [ { "topic": "goroutine", "handling": "do_not_penalize" } ]
    }
  ]
}`

const sampleResultJSON = `{
  "schema_version": 1,
  "topic": { "stable_key": "a3f9c2d41b7e8f06", "title": "مبانی HTTP در Go" },
  "set_version": 1,
  "attempt_no": 1,
  "mode": "full",
  "rubric_version": 1,
  "started_at": "2026-10-31T10:00:00Z",
  "finished_at": "2026-10-31T10:03:41Z",
  "job_id": 57,
  "evaluator": { "adapter": "antigravity", "model": "gemini-3.8-flash-medium",
                 "skill": "answer-evaluator", "skill_version": "1.0.0" },
  "items": [
    { "exercise_id": "q01", "weight": 1, "status": "evaluated", "score": 90,
      "answer_sha256": "9f2ce1a0b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0",
      "report_file": "q01-report.md",
      "criteria_scores": [
        {"criterion_id": "c1", "awarded": 30, "max": 30},
        {"criterion_id": "c2", "awarded": 40, "max": 40},
        {"criterion_id": "c3", "awarded": 20, "max": 30}
      ],
      "error_types": ["omission"]
    },
    { "exercise_id": "q02", "weight": 2, "status": "unanswered", "score": 0 }
  ],
  "aggregate": {
    "weighted_score": 30.0,
    "answered_count": 1,
    "total_count": 2,
    "partial": true,
    "formula": "sum(weight_i*score_i)/sum(weight_i); unanswered=0",
    "computed_by": "app"
  }
}`

func TestValidateManifest_Success(t *testing.T) {
	if err := schema.ValidateManifest([]byte(sampleManifestJSON)); err != nil {
		t.Fatalf("ValidateManifest failed: %v", err)
	}

	var m schema.Manifest
	if err := json.Unmarshal([]byte(sampleManifestJSON), &m); err != nil {
		t.Fatalf("json.Unmarshal Manifest failed: %v", err)
	}

	allowedExts := []string{".go", ".md"}
	if err := schema.CrossCheckManifest(&m, allowedExts, 1, 10); err != nil {
		t.Fatalf("CrossCheckManifest failed: %v", err)
	}
}

func TestValidateRubric_Success(t *testing.T) {
	if err := schema.ValidateRubric([]byte(sampleRubricJSON)); err != nil {
		t.Fatalf("ValidateRubric failed: %v", err)
	}

	var r schema.Rubric
	if err := json.Unmarshal([]byte(sampleRubricJSON), &r); err != nil {
		t.Fatalf("json.Unmarshal Rubric failed: %v", err)
	}

	if err := schema.CrossCheckRubric(&r); err != nil {
		t.Fatalf("CrossCheckRubric failed: %v", err)
	}

	var m schema.Manifest
	_ = json.Unmarshal([]byte(sampleManifestJSON), &m)
	if err := schema.CrossCheckManifestAndRubric(&m, &r); err != nil {
		t.Fatalf("CrossCheckManifestAndRubric failed: %v", err)
	}
}

func TestValidateResult_Success(t *testing.T) {
	if err := schema.ValidateResult([]byte(sampleResultJSON)); err != nil {
		t.Fatalf("ValidateResult failed: %v", err)
	}

	var res schema.Result
	if err := json.Unmarshal([]byte(sampleResultJSON), &res); err != nil {
		t.Fatalf("json.Unmarshal Result failed: %v", err)
	}

	var m schema.Manifest
	_ = json.Unmarshal([]byte(sampleManifestJSON), &m)

	if err := schema.CrossCheckResult(&res, &m); err != nil {
		t.Fatalf("CrossCheckResult failed: %v", err)
	}
}

func TestValidateManifest_RejectsAdditionalProperties(t *testing.T) {
	badJSON := `{
		"schema_version": 1,
		"topic": { "stable_key": "a3f9c2d41b7e8f06", "title": "T" },
		"set": { "version": 1, "rubric_version": 1, "created_at": "now", "generator": {"adapter":"a","skill":"s","skill_version":"1"} },
		"exercises": [],
		"extra_unauthorized_field": "hacked"
	}`

	if err := schema.ValidateManifest([]byte(badJSON)); err == nil {
		t.Errorf("Expected schema to reject additionalProperties, got nil")
	}
}

func TestCrossCheckRubric_RejectsPointsNotSummingTo100(t *testing.T) {
	r := schema.Rubric{
		Exercises: []schema.RubricExercise{
			{
				ID: "q01",
				Criteria: []schema.Criterion{
					{ID: "c1", MaxPoints: 30},
					{ID: "c2", MaxPoints: 40}, // Sum = 70 != 100
				},
			},
		},
	}

	if err := schema.CrossCheckRubric(&r); err == nil {
		t.Errorf("Expected error when criteria max points do not sum to 100")
	}
}

func TestValidateAILessonOutput(t *testing.T) {
	valid := `{
		"lesson_markdown": "# درس اول\n\nاین یک درس تستی است.",
		"keywords": [ { "term": "Go", "gloss": "زبان برنامه نویسی" } ],
		"suggestions": [ { "kind": "prerequisite", "title": "مبانی متغیرها", "reason": "پیش نیاز" } ]
	}`

	if err := schema.ValidateAILessonOutput([]byte(valid)); err != nil {
		t.Fatalf("ValidateAILessonOutput failed: %v", err)
	}
}
