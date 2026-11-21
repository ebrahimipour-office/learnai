package topics_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"learninghub/internal/services/topics"
)

// T10.17: only the latest attempt of the current set counts; low scores come first, then stale
// topics; error types are tallied per topic and overall; nothing evaluated ⇒ nil (section hidden).
func TestReview(t *testing.T) {
	s, db := setup(t, md)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	if r, err := s.Review(ctx, 70, 14, now); err != nil || r != nil {
		t.Fatalf("no attempts must give nil, got %+v %v", r, err)
	}

	w := db.Writer()
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := w.Exec(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	topic := func(title string) (id int64) {
		t.Helper()
		if err := w.QueryRow(`SELECT id FROM topics WHERE title = ?`, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec(`INSERT INTO files (id, topic_id, rel_path, kind, sha256, size_bytes) VALUES (900, ?, 'topics/x/f.md', 'exercises_md', 's', 1)`, topic("نوع‌ها"))
	set := func(topicID int64, ver, current int) int64 {
		return exec(`INSERT INTO exercise_sets (topic_id, version, is_current, exercises_file, rubric_file, manifest_file)
			VALUES (?, ?, ?, 900, 900, 900)`, topicID, ver, current)
	}
	exNo := 0
	// attempt adds one evaluated item (on its own exercise) per JSON error list in errs.
	attempt := func(setID int64, no int, score float64, daysAgo int, errs ...string) {
		fin := now.AddDate(0, 0, -daysAgo).Format("2006-01-02T15:04:05.000Z")
		a := exec(`INSERT INTO attempts (set_id, attempt_no, mode, rubric_version, started_at, finished_at, topic_score,
			answered_count, total_count, is_partial, result_file, summary_file) VALUES (?, ?, 'full', 1, ?, ?, ?, 1, 1, 0, 900, 900)`,
			setID, no, fin, fin, score)
		for _, e := range errs {
			exNo++
			ex := exec(`INSERT INTO exercises (set_id, ex_id, ord, title, type, difficulty, weight, answer_file_id, template_sha256, external_deps)
				VALUES (?, ?, ?, 'ت', 'coding', 'easy', 1, 900, 's', '[]')`, setID, fmt.Sprintf("q%02d", exNo), exNo)
			exec(`INSERT INTO attempt_items (attempt_id, exercise_id, status, score, error_types) VALUES (?, ?, 'evaluated', 50, ?)`, a, ex, e)
		}
	}

	// نوع‌ها: an old low attempt fixed by a recent good one ⇒ not listed, old errors not counted.
	sa := set(topic("نوع‌ها"), 1, 1)
	attempt(sa, 1, 40, 20, `["logical","conceptual"]`)
	attempt(sa, 2, 85, 1, `["style"]`)
	// کتابخانه استاندارد: low and recent.
	sb := set(topic("کتابخانه استاندارد"), 1, 1)
	attempt(sb, 1, 50, 2, `["logical","conceptual"]`, `["logical"]`)
	// گوروتین: good but 30 days old; an older non-current set with a low score is ignored.
	g := topic("گوروتین")
	so := set(g, 1, 0)
	attempt(so, 1, 10, 1, `["technical"]`)
	sc := set(g, 2, 1)
	attempt(sc, 1, 90, 30)

	r, err := s.Review(ctx, 70, 14, now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range r.Topics {
		got = append(got, x.Title)
	}
	if want := []string{"کتابخانه استاندارد", "گوروتین"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %v want %v", got, want)
	}
	if x := r.Topics[0]; !x.Low || x.Stale || !reflect.DeepEqual(x.Errors, []topics.ErrorCount{{"logical", 2}, {"conceptual", 1}}) {
		t.Fatalf("low topic: %+v", x)
	}
	if x := r.Topics[1]; x.Low || !x.Stale || len(x.Errors) != 0 {
		t.Fatalf("stale topic: %+v", x)
	}
	if want := []topics.ErrorCount{{"logical", 2}, {"conceptual", 1}, {"style", 1}}; !reflect.DeepEqual(r.Errors, want) {
		t.Fatalf("overall errors: got %v want %v", r.Errors, want)
	}

	// Threshold: below 50 nothing is low; with a 60-day window nothing is stale ⇒ empty list, section still shown.
	r, err = s.Review(ctx, 50, 60, now)
	if err != nil || r == nil || len(r.Topics) != 0 {
		t.Fatalf("threshold: %+v %v", r, err)
	}
}
