package topics_test

import (
	"context"
	"errors"
	"testing"

	"learninghub/internal/domain"
	"learninghub/internal/services/topics"
)

func TestAnswerStatus(t *testing.T) {
	ex := &topics.Exercise{
		TemplateSHA:  "tmpl-sha",
		EvaluatedSHA: "eval-sha",
	}

	if got := ex.AnswerStatus(""); got != topics.AnswerMissing {
		t.Fatalf("expected AnswerMissing, got %q", got)
	}
	if got := ex.AnswerStatus("tmpl-sha"); got != topics.AnswerEmpty {
		t.Fatalf("expected AnswerEmpty, got %q", got)
	}
	if got := ex.AnswerStatus("eval-sha"); got != topics.AnswerDone {
		t.Fatalf("expected AnswerDone, got %q", got)
	}
	if got := ex.AnswerStatus("new-sha"); got != topics.AnswerChanged {
		t.Fatalf("expected AnswerChanged, got %q", got)
	}

	// Without evaluated SHA
	ex2 := &topics.Exercise{TemplateSHA: "tmpl-sha"}
	if got := ex2.AnswerStatus("new-sha"); got != topics.AnswerDone {
		t.Fatalf("expected AnswerDone for un-evaluated answer, got %q", got)
	}
}

func TestSetsAndExercises(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()

	var topicID int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'نوع‌ها'`).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	// When no sets exist
	sets, err := s.Sets(ctx, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Fatalf("expected 0 sets, got %d", len(sets))
	}

	// Insert files for exercises.md and rubric
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (201, ?, 'topics/01-types/exercises.md', 'exercises_md', 'sha-ex', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (202, ?, 'topics/01-types/.internal/rubric-v1.json', 'rubric', 'sha-rb', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (203, ?, 'topics/01-types/manifest.json', 'manifest', 'sha-mn', 100)`, topicID); err != nil {
		t.Fatal(err)
	}

	// Insert exercise_sets
	res, err := w.Exec(`INSERT INTO exercise_sets (topic_id, version, is_current, model, exercises_file, rubric_file, manifest_file, created_at)
		VALUES (?, 1, 1, 'claude-3-7', 201, 202, 203, '2026-11-15T12:00:00Z')`, topicID)
	if err != nil {
		t.Fatal(err)
	}
	setID, _ := res.LastInsertId()

	sets, err = s.Sets(ctx, topicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 {
		t.Fatalf("expected 1 set, got %d", len(sets))
	}
	if sets[0].ID != setID || sets[0].Version != 1 || !sets[0].Current || sets[0].Model != "claude-3-7" || sets[0].ExercisesRel != "topics/01-types/exercises.md" {
		t.Fatalf("unexpected set: %+v", sets[0])
	}

	// Insert answer file and exercise
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes, presence) VALUES (204, ?, 'topics/01-types/q01.go', 'answer', 'sha-ans', 100, 'present')`, topicID); err != nil {
		t.Fatal(err)
	}
	res, err = w.Exec(`INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256, template_norm_sha256, external_deps)
		VALUES (?, 'q01', 1, 'تمرین اول', 'coding', 'medium', 1.0, 204, 'sha-tmpl', 'sha-norm', '[{"topic":"pkg","note":"std"}]')`, setID)
	if err != nil {
		t.Fatal(err)
	}
	exRowID, _ := res.LastInsertId()

	exs, err := s.Exercises(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	if len(exs) != 1 {
		t.Fatalf("expected 1 exercise, got %d", len(exs))
	}
	if exs[0].ExID != "q01" || exs[0].Title != "تمرین اول" || exs[0].Type != "coding" || exs[0].Difficulty != "medium" || exs[0].Weight != 1.0 {
		t.Fatalf("unexpected exercise fields: %+v", exs[0])
	}
	if exs[0].AnswerRel != "topics/01-types/q01.go" || exs[0].Presence != "present" || exs[0].TemplateSHA != "sha-tmpl" || exs[0].TemplateNormSHA != "sha-norm" {
		t.Fatalf("unexpected answer/template fields: %+v", exs[0])
	}
	if len(exs[0].External) != 1 || exs[0].External[0].Topic != "pkg" || exs[0].External[0].Note != "std" {
		t.Fatalf("unexpected external deps: %+v", exs[0].External)
	}
	if exs[0].EvaluatedSHA != "" {
		t.Fatalf("expected empty EvaluatedSHA before evaluation, got %q", exs[0].EvaluatedSHA)
	}

	// Now insert an attempt with evaluated item to verify EvaluatedSHA population
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (205, ?, 'topics/01-types/result-1.json', 'result', 'sha-res', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (206, ?, 'topics/01-types/summary-1.md', 'summary', 'sha-sum', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	res, err = w.Exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, started_at, finished_at, topic_score, answered_count, total_count, is_partial, result_file, summary_file)
		VALUES (?, 1, 'full', 1, '2026-11-15T12:00:00.000Z', '2026-11-15T12:10:00.000Z', 95.5, 1, 1, 0, 205, 206)`, setID)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, _ := res.LastInsertId()

	if _, err := w.Exec(`INSERT INTO attempt_items (attempt_id, exercise_id, status, score, answer_sha256, flags)
		VALUES (?, ?, 'evaluated', 95, 'sha-eval-latest', '[]')`, attemptID, exRowID); err != nil {
		t.Fatal(err)
	}

	exs, err = s.Exercises(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	if exs[0].EvaluatedSHA != "sha-eval-latest" {
		t.Fatalf("expected EvaluatedSHA 'sha-eval-latest', got %q", exs[0].EvaluatedSHA)
	}
}

func TestFile(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()

	var topicID int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'نوع‌ها'`).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	// Not found
	_, err := s.File(ctx, 99999)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected domain.ErrNotFound, got %v", err)
	}

	// Regular file (lesson)
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (301, ?, 'topics/01-types/lesson.md', 'lesson', 'sha-lsn', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	f, err := s.File(ctx, 301)
	if err != nil {
		t.Fatal(err)
	}
	if f.RelPath != "topics/01-types/lesson.md" || f.Kind != "lesson" || f.Locked {
		t.Fatalf("unexpected file result: %+v", f)
	}

	// Rubric file for a set with unevaluated exercise -> locked should be true
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (302, ?, 'topics/01-types/.internal/rubric-v1.json', 'rubric', 'sha-rb', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	res, err := w.Exec(`INSERT INTO exercise_sets (topic_id, version, is_current, model, exercises_file, rubric_file, manifest_file, created_at)
		VALUES (?, 1, 1, 'fake', 301, 302, 301, '2026-11-15T12:00:00Z')`, topicID)
	if err != nil {
		t.Fatal(err)
	}
	setID, _ := res.LastInsertId()

	res, err = w.Exec(`INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256, external_deps)
		VALUES (?, 'q01', 1, 'Q1', 'coding', 'easy', 1.0, 301, 'sha-t', '[]')`, setID)
	if err != nil {
		t.Fatal(err)
	}
	exID, _ := res.LastInsertId()

	f, err = s.File(ctx, 302)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Locked {
		t.Fatalf("expected rubric to be locked before evaluation")
	}

	// Now add evaluated attempt item -> rubric should unlock
	res, err = w.Exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, started_at, finished_at, topic_score, answered_count, total_count, is_partial, result_file, summary_file)
		VALUES (?, 1, 'full', 1, '2026-11-15T12:00:00.000Z', '2026-11-15T12:10:00.000Z', 100, 1, 1, 0, 301, 301)`, setID)
	if err != nil {
		t.Fatal(err)
	}
	attID, _ := res.LastInsertId()

	if _, err := w.Exec(`INSERT INTO attempt_items (attempt_id, exercise_id, status, score, answer_sha256, flags)
		VALUES (?, ?, 'evaluated', 100, 'sha-ans', '[]')`, attID, exID); err != nil {
		t.Fatal(err)
	}

	f, err = s.File(ctx, 302)
	if err != nil {
		t.Fatal(err)
	}
	if f.Locked {
		t.Fatalf("expected rubric to be unlocked after evaluation")
	}
}

func TestAttemptsAndAttemptItems(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	w := db.Writer()

	var topicID int64
	if err := w.QueryRow(`SELECT id FROM topics WHERE title = 'نوع‌ها'`).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	// Insert file dependencies
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (401, ?, 'topics/01/f.md', 'exercises_md', 's1', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (402, ?, 'topics/01/res1.json', 'result', 's2', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (403, ?, 'topics/01/sum1.md', 'summary', 's3', 100)`, topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (404, ?, 'topics/01/rep1.md', 'report', 's4', 100)`, topicID); err != nil {
		t.Fatal(err)
	}

	// Insert set
	res, err := w.Exec(`INSERT INTO exercise_sets (topic_id, version, is_current, model, exercises_file, rubric_file, manifest_file, created_at)
		VALUES (?, 1, 1, 'fake', 401, 401, 401, '2026-11-15T12:00:00Z')`, topicID)
	if err != nil {
		t.Fatal(err)
	}
	setID, _ := res.LastInsertId()

	res, err = w.Exec(`INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256, external_deps)
		VALUES (?, 'q01', 1, 'تست ۱', 'coding', 'easy', 1.0, 401, 's', '[]')`, setID)
	if err != nil {
		t.Fatal(err)
	}
	exID, _ := res.LastInsertId()

	// Empty attempts
	atts, err := s.Attempts(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 0 {
		t.Fatalf("expected 0 attempts, got %d", len(atts))
	}

	// Insert attempt 1
	res, err = w.Exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, started_at, finished_at, topic_score, answered_count, total_count, is_partial, result_file, summary_file)
		VALUES (?, 1, 'full', 1, '2026-11-15T12:00:00.000Z', '2026-11-15T12:05:00.000Z', 80.0, 1, 1, 0, 402, 403)`, setID)
	if err != nil {
		t.Fatal(err)
	}
	att1ID, _ := res.LastInsertId()

	// Insert attempt 2 (incremental)
	res, err = w.Exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, started_at, finished_at, topic_score, answered_count, total_count, is_partial, result_file, summary_file)
		VALUES (?, 2, 'incremental', 1, '2026-11-15T12:10:00.000Z', '2026-11-15T12:15:00.000Z', 90.0, 1, 1, 1, 402, 403)`, setID)
	if err != nil {
		t.Fatal(err)
	}
	att2ID, _ := res.LastInsertId()

	// Insert attempt items for attempt 2, carried from attempt 1
	if _, err := w.Exec(`INSERT INTO attempt_items (attempt_id, exercise_id, status, score, carried_from_attempt, report_file, answer_sha256, flags)
		VALUES (?, ?, 'evaluated', 90, ?, 404, 's-ans', '["needs_review"]')`, att2ID, exID, att1ID); err != nil {
		t.Fatal(err)
	}

	// Verify Attempts (ordered by attempt_no DESC -> att2 then att1)
	atts, err = s.Attempts(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(atts))
	}
	if atts[0].No != 2 || atts[0].Mode != "incremental" || !atts[0].Partial || atts[0].Score != 90.0 {
		t.Fatalf("unexpected attempt 2 data: %+v", atts[0])
	}
	if atts[1].No != 1 || atts[1].Mode != "full" || atts[1].Partial || atts[1].Score != 80.0 {
		t.Fatalf("unexpected attempt 1 data: %+v", atts[1])
	}

	// Verify AttemptItems
	items, err := s.AttemptItems(ctx, att2ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.ExID != "q01" || it.Title != "تست ۱" || it.Status != "evaluated" || it.Score != 90 {
		t.Fatalf("unexpected item: %+v", it)
	}
	if it.CarriedFrom != 1 {
		t.Fatalf("expected carried from attempt 1, got %d", it.CarriedFrom)
	}
	if it.ReportFileID != 404 || it.ReportRel != "topics/01/rep1.md" {
		t.Fatalf("unexpected report file data: %d, %s", it.ReportFileID, it.ReportRel)
	}
	if len(it.Flags) != 1 || it.Flags[0] != "needs_review" {
		t.Fatalf("unexpected flags: %+v", it.Flags)
	}
}
