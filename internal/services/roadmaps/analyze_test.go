package roadmaps_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/fake"
	"learninghub/internal/domain"
	"learninghub/internal/jobs"
	"learninghub/internal/roadmap"
	"learninghub/internal/services/roadmaps"
	"learninghub/skills"
)

// ambiguous has phases (h2) with topics as list items: the default rule picks the wrong level.
const ambiguous = "# Go\n\nintro text the AI must not see\n\n## Phase 1\n- Types\n- Loops\n## Phase 2\n- Goroutines\n"

func runAnalyze(t *testing.T, ad *fake.Adapter) (*roadmaps.Service, *roadmaps.Draft, *jobs.Info) {
	t.Helper()
	s, db, root := newServiceIn(t, t.TempDir())
	if _, err := skills.Install(root); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := s.CreateDraft(ctx, "go.md", []byte(ambiguous))
	if err != nil {
		t.Fatal(err)
	}
	r := jobs.New(db, 1)
	r.Poll, r.Heartbeat, r.Files = 20*time.Millisecond, time.Hour, root
	r.Register(domain.JobKindRoadmapAnalyze, (&roadmaps.Analyzer{Drafts: s, Adapter: ad, Timeout: time.Minute, Stall: time.Minute}).Handle)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })

	params, _ := json.Marshal(roadmaps.AnalyzeParams{DraftID: id})
	jid, err := r.Enqueue(ctx, jobs.Job{Kind: domain.JobKindRoadmapAnalyze, Scope: roadmaps.DraftScope(id), Params: params})
	if err != nil {
		t.Fatal(err)
	}
	var j *jobs.Info
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if j, err = r.Get(ctx, jid); err != nil {
			t.Fatal(err)
		}
		if !j.Active() {
			break
		}
	}
	for _, table := range []string{"roadmaps", "groups", "topics", "files"} {
		if n := count(t, db, table); n != 0 {
			t.Errorf("%s has %d rows; analysis must not create any", table, n)
		}
	}
	d, err := s.Draft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return s, d, j
}

func TestAnalyzeStoresSuggestionOnly(t *testing.T) {
	ad := &fake.Adapter{Scenario: fake.OK, Output: json.RawMessage(`{"rule":{"kind":"items","level":3},"reason":"  موضوعات آیتم‌های لیست‌اند "}`)}
	_, d, j := runAnalyze(t, ad)
	if j.Status != "succeeded" {
		t.Fatalf("job %s: %s %s", j.Status, j.ErrorCode, j.ErrorMessage)
	}
	want := roadmap.Rule{Kind: roadmap.RuleItems}
	if d.AI == nil || d.AI.Rule != want || d.AI.Reason != "موضوعات آیتم‌های لیست‌اند" || d.AI.JobID != j.ID {
		t.Fatalf("suggestion = %+v", d.AI)
	}
	if d.Choice.Rule.Kind != roadmap.RuleDeepest {
		t.Errorf("draft rule changed to %+v without the user applying it", d.Choice.Rule)
	}
	if calls := ad.Calls(); len(calls) != 1 || !strings.Contains(calls[0], "roadmap-analyzer") {
		t.Errorf("prompts = %q", calls)
	}
}

func TestAnalyzeRejectsRuleWithoutTopics(t *testing.T) {
	// level-5 is valid JSON but selects nothing: invalid twice ⇒ job fails, draft untouched.
	ad := &fake.Adapter{Scenario: fake.OK, Output: json.RawMessage(`{"rule":{"kind":"level","level":5},"reason":"x"}`)}
	_, d, j := runAnalyze(t, ad)
	if j.Status != "failed" || j.ErrorCode != "invalid_output" {
		t.Fatalf("job = %s/%s", j.Status, j.ErrorCode)
	}
	if d.AI != nil {
		t.Errorf("suggestion stored from invalid output: %+v", d.AI)
	}
}

func TestValidateSuggestion(t *testing.T) {
	tree := roadmap.Parse([]byte(ambiguous))
	for raw, ok := range map[string]bool{
		`{"rule":{"kind":"level","level":2},"reason":"ok"}`: true,
		`{"rule":{"kind":"deepest"},"reason":" "}`:          false,
		`{"rule":{"kind":"bogus"},"reason":"x"}`:            false,
		`{"rule":{"kind":"level","level":9},"reason":"x"}`:  false,
		`{"rule":{"kind":"items"},"reason":"x","extra":1}`:  false,
	} {
		_, err := roadmaps.ValidateSuggestion(tree, json.RawMessage(raw))
		if (err == nil) != ok || (err != nil && !errors.Is(err, ai.ErrInvalidOutput)) {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}

func TestTitlesDropsBody(t *testing.T) {
	got := roadmaps.Titles(roadmap.Parse([]byte(ambiguous)))
	want := "# Go\n## Phase 1\n- Types\n- Loops\n## Phase 2\n- Goroutines\n"
	if got != want {
		t.Errorf("Titles =\n%s\nwant\n%s", got, want)
	}
}
