package roadmaps

import (
	"context"
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

const analyzerSkill = "roadmap-analyzer"

// Suggestion is import_drafts.ai_suggestion (T6.2). It only ever changes the draft when the user applies its rule.
type Suggestion struct {
	Rule   roadmap.Rule `json:"rule"`
	Reason string       `json:"reason"`
	JobID  int64        `json:"job_id"`
}

// AnalyzeParams is jobs.params for a roadmap_analyze job.
type AnalyzeParams struct {
	DraftID string `json:"draft_id"`
}

// DraftScope is the job scope of a draft: one analysis at a time per draft.
func DraftScope(id string) string { return "draft:" + id }

// Analyzer is the jobs.Handler for domain.JobKindRoadmapAnalyze. It creates no file and no
// roadmap/topic row; its only write is import_drafts.ai_suggestion.
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

func (a *Analyzer) Handle(ctx context.Context, j jobs.Job) error {
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
	if err != nil {
		return err
	}
	in := prompt.Input{JobID: j.ID, Task: ai.TaskRoadmapAnalyze,
		Files: map[string][]byte{"task.json": task, "roadmap_excerpt.md": []byte(Titles(tree))}}
	for _, f := range []struct {
		dst *[]byte
		rel string
	}{{&in.Agents, "AGENTS.md"}, {&in.Profile, "profile.md"}, {&in.Skill, skills.SkillRel(analyzerSkill)}} {
		if *f.dst, err = s.root.ReadFile(f.rel); err != nil {
			return fmt.Errorf("reading %s: %w", f.rel, err)
		}
	}
	if in.Schema, err = schema.GetSchemaBytes("ai-roadmap-analyze-output.schema.json"); err != nil {
		return err
	}
	req, err := (&prompt.Composer{Root: s.root}).Compose(in)
	if err != nil {
		return err
	}
	req.Model, req.Timeout, req.StallTimeout = a.Model, a.Timeout, a.Stall
	db := s.db.Writer()
	if _, err := db.ExecContext(ctx, `UPDATE jobs SET phase = 'ai', inputs_fingerprint = ?, adapter = ?, model = ?, skill = ?, skill_version = ? WHERE id = ?`,
		fsx.HashBytes(task), a.Adapter.Name(), a.Model, req.Skill.Name, req.Skill.Version, j.ID); err != nil {
		return err
	}

	var sug Suggestion
	resp, err := ai.WatchValid(ctx, a.Adapter, req, nil, func(raw json.RawMessage) (err error) {
		sug, err = ValidateSuggestion(tree, raw)
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
