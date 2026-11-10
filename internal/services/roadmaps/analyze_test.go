package roadmaps_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	return runDraftJob(t, ad, domain.JobKindRoadmapAnalyze, ambiguous)
}

func runDraftJob(t *testing.T, ad *fake.Adapter, kind domain.JobKind, content string) (*roadmaps.Service, *roadmaps.Draft, *jobs.Info) {
	t.Helper()
	return runDraftJobWith(t, ad, kind, content, func(d *roadmaps.Draft) json.RawMessage {
		p, _ := json.Marshal(roadmaps.AnalyzeParams{DraftID: d.ID})
		return p
	}, nil)
}

// runDraftJobWith queues the job with params(draft), then calls between (if any) before the runner starts.
func runDraftJobWith(t *testing.T, ad *fake.Adapter, kind domain.JobKind, content string,
	params func(*roadmaps.Draft) json.RawMessage, between func(*roadmaps.Service, *roadmaps.Draft)) (*roadmaps.Service, *roadmaps.Draft, *jobs.Info) {
	t.Helper()
	s, db, root := newServiceIn(t, t.TempDir())
	if _, err := skills.Install(root); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := s.CreateDraft(ctx, "go.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	r := jobs.New(db, 1)
	r.Poll, r.Heartbeat, r.Files = 20*time.Millisecond, time.Hour, root
	an := &roadmaps.Analyzer{Drafts: s, Adapter: ad, Timeout: time.Minute, Stall: time.Minute}
	r.Register(domain.JobKindRoadmapAnalyze, an.Handle)
	r.Register(domain.JobKindChapterDetect, an.DetectChapters)
	d0, err := s.Draft(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	jid, err := r.Enqueue(ctx, jobs.Job{Kind: kind, Scope: roadmaps.DraftScope(id), Params: params(d0)})
	if err != nil {
		t.Fatal(err)
	}
	if between != nil {
		between(s, d0)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
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
	if d.AI == nil || !reflect.DeepEqual(d.AI.Rule, want) || d.AI.Reason != "موضوعات آیتم‌های لیست‌اند" || d.AI.JobID != j.ID {
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

// book: H1 title, preface, two H2 chapters (one with a sub-heading), a code block with '#'.
const book = "# کتاب Go\npreface\n## مقدمات\nبدنه یک\n### نوع‌ها\nint\n## توابع\n```\n# نه سرفصل\n```\nfunc\n"

// Skill 1.0.0 output (flat chapters with end_line) still validates and becomes a tree rule (§13.2).
func TestDetectChaptersStoresSuggestionOnly(t *testing.T) {
	ad := &fake.Adapter{Scenario: fake.OK, Output: json.RawMessage(`{"title":" کتاب Go ","reason":"هر H2 یک فصل",
		"chapters":[{"title":"مقدمات","start_line":3,"end_line":6},{"title":" توابع ","start_line":7,"end_line":11}]}`)}
	_, d, j := runDraftJob(t, ad, domain.JobKindChapterDetect, book) // runDraftJob also asserts no roadmap/topic/file rows
	if j.Status != "succeeded" {
		t.Fatalf("job %s: %s %s", j.Status, j.ErrorCode, j.ErrorMessage)
	}
	want := roadmap.Rule{Kind: roadmap.RuleTree, Chapters: []roadmap.Chapter{{Line: 3, Title: "مقدمات"}, {Line: 7, Title: "توابع"}}}
	if d.AI == nil || !reflect.DeepEqual(d.AI.Rule, want) || d.AI.Title != "کتاب Go" || d.AI.JobID != j.ID {
		t.Fatalf("suggestion = %+v", d.AI)
	}
	if d.Choice.Rule.Kind != roadmap.RuleDeepest || d.Title != "" {
		t.Errorf("draft changed without the user applying it: %+v %q", d.Choice.Rule, d.Title)
	}
	if calls := ad.Calls(); len(calls) != 1 || !strings.Contains(calls[0], "chapter-detector") {
		t.Errorf("prompts = %q", calls)
	}
	// The suggestion previews as two chapter topics.
	sel, err := d.Preview(roadmaps.Choice{Rule: d.AI.Rule})
	if err != nil || len(sel.Topics) != 2 || sel.Topics[1].Title != "توابع" {
		t.Errorf("preview = %+v, %v", sel, err)
	}
}

func TestDetectChaptersRejectsInvalidRanges(t *testing.T) {
	for name, chapters := range map[string]string{
		"descending":    `[{"title":"a","start_line":7},{"title":"b","start_line":3}]`,
		"duplicate":     `[{"title":"a","start_line":3},{"title":"b","start_line":3}]`,
		"blank title":   `[{"title":" ","start_line":3}]`,
		"not a heading": `[{"title":"a","start_line":9,"end_line":10}]`,
	} {
		t.Run(name, func(t *testing.T) {
			ad := &fake.Adapter{Scenario: fake.OK, Output: json.RawMessage(`{"title":"t","reason":"r","chapters":` + chapters + `}`)}
			_, d, j := runDraftJob(t, ad, domain.JobKindChapterDetect, book)
			if j.Status != "failed" || j.ErrorCode != "invalid_output" {
				t.Fatalf("job = %s/%s", j.Status, j.ErrorCode)
			}
			if d.AI != nil {
				t.Errorf("suggestion stored from invalid output: %+v", d.AI)
			}
		})
	}
}

func TestOutlineShowsHeadingsAndShortPreview(t *testing.T) {
	src := []byte(book + "## طولانی\n" + strings.Repeat("خط\n", 10) + strings.Repeat("ب", 400) + "\n")
	got := roadmaps.Outline(roadmap.Parse(src), src)
	if len(got) != 5 {
		t.Fatalf("outline = %+v", got)
	}
	if got[1].Title != "مقدمات" || got[1].Line != 3 || got[1].EndLine != 6 || got[1].Level != 2 || got[1].Preview != "بدنه یک" {
		t.Errorf("chapter entry = %+v (preview must stop at the sub-heading)", got[1])
	}
	if got[3].Preview != "```\n# نه سرفصل\n```\nfunc" {
		t.Errorf("code-block preview = %q", got[3].Preview)
	}
	if p := got[4].Preview; strings.Count(p, "\n") != 4 || strings.Contains(p, "ب") {
		t.Errorf("preview must keep 5 lines: %q", p)
	}
	long := []byte("# t\n" + strings.Repeat("ب", 400) + "\n")
	if p := roadmaps.Outline(roadmap.Parse(long), long)[0].Preview; len([]rune(p)) != 300 {
		t.Errorf("preview has %d runes, want 300", len([]rune(p)))
	}
}

// nested is skill 2.0.0 output: every heading, H3 under its H2, no end_line.
const nested = `{"title":"کتاب Go","reason":"همه سرفصل‌ها","chapters":[
	{"title":"مقدمات","start_line":3},{"title":"نوع‌ها","start_line":5},{"title":"توابع","start_line":7}]}`

func detectParams(d *roadmaps.Draft) json.RawMessage {
	p, err := roadmaps.DetectParams(d)
	if err != nil {
		panic(err)
	}
	return p
}

// T8.4: an untouched selection takes the suggestion (and the title, first import) at commit; still no record or file.
func TestDetectChaptersAutoApplies(t *testing.T) {
	ad := &fake.Adapter{Scenario: fake.OK, Output: json.RawMessage(nested)}
	_, d, j := runDraftJobWith(t, ad, domain.JobKindChapterDetect, book, detectParams, nil)
	if j.Status != "succeeded" {
		t.Fatalf("job %s: %s %s", j.Status, j.ErrorCode, j.ErrorMessage)
	}
	if d.AI == nil || !reflect.DeepEqual(d.Choice, roadmaps.Choice{Rule: d.AI.Rule}) || d.Title != "کتاب Go" {
		t.Fatalf("not applied: choice %+v, title %q, suggestion %+v", d.Choice, d.Title, d.AI)
	}
	sel, err := d.Preview(d.Choice)
	if err != nil || len(sel.Topics) != 3 || sel.Topics[1].Parent != 0 || sel.Topics[2].Parent != -1 {
		t.Errorf("preview = %+v, %v", sel.Topics, err)
	}
}

// T8.4: a selection changed while the job ran is kept; the suggestion waits for «اعمال پیشنهاد».
func TestDetectChaptersKeepsUserSelection(t *testing.T) {
	ad := &fake.Adapter{Scenario: fake.OK, Output: json.RawMessage(nested)}
	user := roadmaps.Choice{Rule: roadmap.Rule{Kind: roadmap.RuleLevel, Level: 2}}
	_, d, j := runDraftJobWith(t, ad, domain.JobKindChapterDetect, book, detectParams, func(s *roadmaps.Service, d *roadmaps.Draft) {
		if err := s.SetChoice(context.Background(), d, user); err != nil {
			t.Fatal(err)
		}
	})
	if j.Status != "succeeded" || d.AI == nil {
		t.Fatalf("job %s, suggestion %+v", j.Status, d.AI)
	}
	if !reflect.DeepEqual(d.Choice, user) || d.Title != "" {
		t.Errorf("user's selection overwritten: %+v %q", d.Choice, d.Title)
	}
}
