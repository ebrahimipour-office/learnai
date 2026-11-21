package jobs_test

import (
	"context"
	"testing"
	"time"

	"learninghub/internal/jobs"
)

func TestUsage(t *testing.T) {
	r, db := newRunner(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02T15:04:05.000Z") }
	for k, c := range []struct {
		adapter, usage, finished string
	}{
		{"claude", `{"input_tokens":100,"output_tokens":20}`, ts(time.Hour)},
		{"claude", `{"input_tokens":5,"output_tokens":1}`, ts(24 * time.Hour)},
		{"claude", `{"input_tokens":999,"output_tokens":999}`, ts(40 * 24 * time.Hour)}, // too old
		{"codex", ``, ts(time.Hour)},         // no usage reported
		{"codex", `not json`, ts(time.Hour)}, // malformed ⇒ 0, no error
		{"claude", `{"input_tokens":7}`, ""}, // unfinished: not counted
	} {
		id := enqueue(t, r, "s"+string(rune('a'+k)))
		var usage, fin any
		if c.usage != "" {
			usage = c.usage
		}
		if c.finished != "" {
			fin = c.finished
		}
		if _, err := db.Writer().ExecContext(ctx, `UPDATE jobs SET status = CASE WHEN ? IS NULL THEN status ELSE 'succeeded' END,
			adapter = ?, usage = ?, finished_at = ? WHERE id = ?`, fin, c.adapter, usage, fin, id); err != nil {
			t.Fatal(err)
		}
	}

	got, err := jobs.UsageSince(ctx, db.Reader(), now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []jobs.UsageRow{{"lesson", "claude", 2, 105, 21}, {"lesson", "codex", 2, 0, 0}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("row %d = %+v, want %+v", k, got[k], want[k])
		}
	}

	i, err := r.Get(ctx, 1)
	if err != nil || i.InputTokens != 100 || i.OutputTokens != 20 {
		t.Errorf("Get(1) tokens = %d/%d, %v; want 100/20", i.InputTokens, i.OutputTokens, err)
	}
	if i, err := r.Get(ctx, 5); err != nil || i.InputTokens != 0 || i.OutputTokens != 0 {
		t.Errorf("Get(5) malformed usage = %+v, %v", i, err)
	}
}
