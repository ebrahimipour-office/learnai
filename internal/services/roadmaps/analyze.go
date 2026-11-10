package roadmaps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"learninghub/internal/ai"
	"learninghub/internal/ai/prompt"
	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/jobs"
	"learninghub/internal/roadmap"
	"learninghub/internal/schema"
	"learninghub/skills"
)

const (
	analyzerSkill = "roadmap-analyzer"
	chapterSkill  = "chapter-detector"
	maxPreview    = 300 // runes of body text per heading in outline.json (§12.5)
)

// Suggestion is import_drafts.ai_suggestion (T6.2, T7.4). It only ever changes the draft when the user applies it.
type Suggestion struct {
	Rule   roadmap.Rule `json:"rule"`
	Title  string       `json:"title,omitempty"` // chapter_detect only: suggested roadmap title
	Reason string       `json:"reason"`
	JobID  int64        `json:"job_id"`
}

// AnalyzeParams is jobs.params for a roadmap_analyze or chapter_detect job.
type AnalyzeParams struct {
	DraftID string `json:"draft_id"`
	// Selection is import_drafts.selection when the job was queued. A chapter_detect commit applies its
	// suggestion only while the selection still equals it (compare-and-swap, §13.2); "" = never apply.
	Selection string `json:"selection,omitempty"`
}

// DetectParams is the params of a chapter_detect job on d, with d's current selection for the auto-apply.
func DetectParams(d *Draft) (json.RawMessage, error) {
	sel, err := json.Marshal(d.Choice) // same encoding CreateDraft and SetChoice store
	if err != nil {
		return nil, err
	}
	return json.Marshal(AnalyzeParams{DraftID: d.ID, Selection: string(sel)})
}

// DraftScope is the job scope of a draft: one analysis at a time per draft.
func DraftScope(id string) string { return "draft:" + id }

// Analyzer handles the draft jobs: Handle for domain.JobKindRoadmapAnalyze and DetectChapters for
// domain.JobKindChapterDetect. Neither creates a file or a roadmap/topic row; the only write is
// import_drafts.ai_suggestion.
type Analyzer struct {
	Drafts  *Service
	Adapter ai.Adapter
	Model   string
	Timeout time.Duration // config jobs.timeout_analyze
	Stall   time.Duration
}

// candidateRules are every rule the preview offers, in its order.
func candidateRules() []roadmap.Rule {
	rules := []roadmap.Rule{{Kind: roadmap.RuleDeepest}}
	for n := 1; n <= 6; n++ {
		rules = append(rules, roadmap.Rule{Kind: roadmap.RuleLevel, Level: n})
	}
	return append(rules, roadmap.Rule{Kind: roadmap.RuleItems})
}

// draftTask is what one draft job sends to the AI and how it checks the answer.
type draftTask struct {
	task     ai.Task
	skill    string
	schema   string
	files    map[string][]byte // inputs/<name>
	validate func(json.RawMessage) (Suggestion, error)
	apply    bool // chapter_detect: also apply the suggestion if the selection is unchanged (§13.2)
}

func (a *Analyzer) Handle(ctx context.Context, j jobs.Job) error {
	return a.run(ctx, j, func(d *Draft, tree *roadmap.Node) (draftTask, error) {
		type candidate struct {
			Rule   roadmap.Rule `json:"rule"`
			Topics int          `json:"topics"`
		}
		var cands []candidate
		for _, r := range candidateRules() {
			if sel, err := roadmap.Select(tree, r); err == nil {
				cands = append(cands, candidate{r, len(sel.Topics)})
			}
		}
		task, err := json.MarshalIndent(map[string]any{"source_name": d.SourceName, "candidates": cands}, "", "  ")
		return draftTask{
			task: ai.TaskRoadmapAnalyze, skill: analyzerSkill, schema: "ai-roadmap-analyze-output.schema.json",
			files:    map[string][]byte{"task.json": task, "roadmap_excerpt.md": []byte(Titles(tree))},
			validate: func(raw json.RawMessage) (Suggestion, error) { return ValidateSuggestion(tree, raw) },
		}, err
	})
}

// DetectChapters asks the AI to split the draft into chapters (T7.4, §12.5). The AI sees the headings
// with their line ranges and a short preview of each body, never the whole file.
func (a *Analyzer) DetectChapters(ctx context.Context, j jobs.Job) error {
	return a.run(ctx, j, func(d *Draft, tree *roadmap.Node) (draftTask, error) {
		lines := roadmap.LineCount(d.Content)
		task, err := json.MarshalIndent(map[string]any{
			"source_name": d.SourceName, "total_lines": lines, "h1_title": roadmap.Title(tree)}, "", "  ")
		if err != nil {
			return draftTask{}, err
		}
		outline, err := json.MarshalIndent(Outline(tree, d.Content), "", "  ")
		return draftTask{
			task: ai.TaskChapterDetect, skill: chapterSkill, schema: "ai-chapter-detect-output.schema.json",
			files:    map[string][]byte{"task.json": task, "outline.json": outline},
			validate: func(raw json.RawMessage) (Suggestion, error) { return ValidateChapterSuggestion(tree, lines, raw) },
			apply:    true,
		}, err
	})
}

func (a *Analyzer) run(ctx context.Context, j jobs.Job, build func(*Draft, *roadmap.Node) (draftTask, error)) error {
	var p AnalyzeParams
	if err := json.Unmarshal(j.Params, &p); err != nil || p.DraftID == "" {
		return fmt.Errorf("%w: params: %v", domain.ErrInvalidInput, err)
	}
	s := a.Drafts
	d, err := s.Draft(ctx, p.DraftID)
	if err != nil {
		return err
	}
	tree := roadmap.Parse(d.Content)
	t, err := build(d, tree)
	if err != nil {
		return err
	}
	in := prompt.Input{JobID: j.ID, Task: t.task, Files: t.files}
	for _, f := range []struct {
		dst *[]byte
		rel string
	}{{&in.Agents, "AGENTS.md"}, {&in.Profile, "profile.md"}, {&in.Skill, skills.SkillRel(t.skill)}} {
		if *f.dst, err = s.root.ReadFile(f.rel); err != nil {
			return fmt.Errorf("reading %s: %w", f.rel, err)
		}
	}
	if in.Schema, err = schema.GetSchemaBytes(t.schema); err != nil {
		return err
	}
	inputs, err := json.Marshal(t.files) // map keys are sorted: a stable fingerprint of every input
	if err != nil {
		return err
	}
	req, err := (&prompt.Composer{Root: s.root}).Compose(in)
	if err != nil {
		return err
	}
	req.Model, req.Timeout, req.StallTimeout = ai.ModelFor(a.Adapter, a.Model), a.Timeout, a.Stall
	db := s.db.Writer()
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET phase = 'ai', inputs_fingerprint = ?, adapter = ?, model = ?, skill = ?, skill_version = ? WHERE id = ?`,
		fsx.HashBytes(inputs), a.Adapter.Name(), ai.ModelFor(a.Adapter, a.Model), req.Skill.Name, req.Skill.Version, j.ID); err != nil {
		return err
	}

	var sug Suggestion
	resp, err := ai.WatchValid(ctx, a.Adapter, req, nil, func(raw json.RawMessage) (err error) {
		sug, err = t.validate(raw)
		return err
	})
	if resp != nil {
		usage, _ := json.Marshal(resp.Usage)
		if _, uerr := db.ExecContext(context.WithoutCancel(ctx), `UPDATE jobs SET usage = ?, log_dir = ? WHERE id = ?`,
			string(usage), resp.RawLogDir, j.ID); uerr != nil {
			return errors.Join(err, uerr)
		}
	}
	if err != nil {
		return err
	}
	sug.JobID = j.ID
	raw, _ := json.Marshal(sug)

	return j.Commit(ctx, func(ctx context.Context) error {
		res, err := db.ExecContext(ctx, `UPDATE import_drafts SET ai_suggestion = ? WHERE id = ? AND expires_at > ?`,
			string(raw), d.ID, s.ts(s.Now()))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDraftNotFound
		}
		if t.apply && p.Selection != "" {
			choice, err := json.Marshal(Choice{Rule: sug.Rule})
			if err != nil {
				return err
			}
			title := sql.NullString{String: sug.Title, Valid: d.RoadmapID == 0 && sug.Title != ""} // first import only
			// No row = the user changed the selection meanwhile: the suggestion waits for «اعمال پیشنهاد».
			if _, err := db.ExecContext(ctx, `UPDATE import_drafts SET selection = ?, title = COALESCE(title, ?) WHERE id = ? AND selection = ?`,
				string(choice), title, d.ID, p.Selection); err != nil {
				return err
			}
		}
		ref, _ := json.Marshal(map[string]string{"draft_id": d.ID})
		_, err = db.ExecContext(ctx, `UPDATE jobs SET result_ref = ? WHERE id = ?`, string(ref), j.ID)
		return err
	})
}

// ValidateSuggestion checks the AI output against its schema and requires the rule to select at least one topic.
func ValidateSuggestion(tree *roadmap.Node, raw json.RawMessage) (Suggestion, error) {
	var s Suggestion
	if err := schema.ValidateAIRoadmapAnalyzeOutput(raw); err != nil {
		return s, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	if s.Rule.Kind != roadmap.RuleLevel {
		s.Rule.Level = 0
	}
	s.Reason = strings.TrimSpace(s.Reason)
	sel, err := roadmap.Select(tree, s.Rule)
	switch {
	case err != nil:
		return s, fmt.Errorf("%w: rule: %v", ai.ErrInvalidOutput, err)
	case len(sel.Topics) == 0:
		return s, fmt.Errorf("%w: rule selects no topic", ai.ErrInvalidOutput)
	case s.Reason == "":
		return s, fmt.Errorf("%w: empty reason", ai.ErrInvalidOutput)
	}
	return s, nil
}

// ValidateChapterSuggestion checks chapter_detect output: schema, then roadmap.ValidateChapters (nested) on
// the draft (lines = its line count). It yields a tree rule (§13.2): start_line maps to line, titles are
// trimmed and end_line is dropped (the software computes ranges), so skill 1.0.0's flat output still fits.
func ValidateChapterSuggestion(tree *roadmap.Node, lines int, raw json.RawMessage) (Suggestion, error) {
	var out struct {
		Title    string `json:"title"`
		Reason   string `json:"reason"`
		Chapters []struct {
			Title     string `json:"title"`
			StartLine int    `json:"start_line"`
		} `json:"chapters"`
	}
	if err := schema.ValidateAIChapterDetectOutput(raw); err != nil {
		return Suggestion{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Suggestion{}, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	s := Suggestion{Rule: roadmap.Rule{Kind: roadmap.RuleTree}, Title: strings.TrimSpace(out.Title), Reason: strings.TrimSpace(out.Reason)}
	for _, c := range out.Chapters {
		s.Rule.Chapters = append(s.Rule.Chapters, roadmap.Chapter{Line: c.StartLine, Title: strings.TrimSpace(c.Title)})
	}
	if err := roadmap.ValidateChapters(tree, lines, s.Rule.Chapters, true); err != nil {
		return s, fmt.Errorf("%w: %v", ai.ErrInvalidOutput, err)
	}
	if s.Title == "" || s.Reason == "" {
		return s, fmt.Errorf("%w: empty title or reason", ai.ErrInvalidOutput)
	}
	return s, nil
}

// OutlineEntry is one heading of outline.json (§12.5).
type OutlineEntry struct {
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Level   int    `json:"level"`
	Title   string `json:"title"`
	Preview string `json:"preview"`
}

// Outline lists every heading in document order with up to 5 non-blank lines of its own body
// (stopping at the next heading), at most maxPreview runes.
func Outline(tree *roadmap.Node, src []byte) []OutlineEntry {
	var heads []*roadmap.Node
	var walk func(n *roadmap.Node)
	walk = func(n *roadmap.Node) {
		for _, c := range n.Children {
			if c.Kind == roadmap.KindHeading {
				heads = append(heads, c)
				walk(c)
			}
		}
	}
	walk(tree)
	out := []OutlineEntry{}
	for i, h := range heads {
		end := h.EndLine
		if i+1 < len(heads) {
			end = min(end, heads[i+1].Line-1)
		}
		var keep []string
		for _, l := range strings.Split(string(roadmap.Slice(src, h.Line+1, end)), "\n") {
			if l = strings.TrimSpace(l); l != "" && len(keep) < 5 {
				keep = append(keep, l)
			}
		}
		preview := strings.Join(keep, "\n")
		if r := []rune(preview); len(r) > maxPreview {
			preview = string(r[:maxPreview])
		}
		out = append(out, OutlineEntry{Line: h.Line, EndLine: h.EndLine, Level: h.Level, Title: h.Title, Preview: preview})
	}
	return out
}

// Titles is the roadmap as headings and list items only: the AI never sees the body text.
func Titles(root *roadmap.Node) string {
	var b strings.Builder
	var walk func(n *roadmap.Node)
	walk = func(n *roadmap.Node) {
		switch n.Kind {
		case roadmap.KindHeading:
			b.WriteString(strings.Repeat("#", n.Level) + " " + n.Title + "\n")
		case roadmap.KindItem:
			b.WriteString(strings.Repeat("  ", n.Level-1) + "- " + n.Title + "\n")
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return b.String()
}
