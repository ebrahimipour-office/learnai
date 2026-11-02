package exercises

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"learninghub/internal/ai"
	"learninghub/internal/fsx"
	"learninghub/internal/render"
	"learninghub/internal/schema"
)

// Output is the AI's structured answer (ai-exercises-output.schema.json).
type Output struct {
	Exercises []struct {
		Title             string  `json:"title"`
		Type              string  `json:"type"`
		Difficulty        string  `json:"difficulty"`
		Weight            float64 `json:"weight"`
		StatementMarkdown string  `json:"statement_markdown"`
		Answer            struct {
			Slug     string `json:"slug"`
			Ext      string `json:"ext"`
			Language string `json:"language"`
			Starter  string `json:"starter"`
		} `json:"answer"`
		ExternalKnowledge []schema.ExternalKnowledge `json:"external_knowledge"`
		Rubric            struct {
			ReferenceAnswer     schema.ReferenceAnswer       `json:"reference_answer"`
			Criteria            []schema.Criterion           `json:"criteria"`
			CommonErrors        []schema.CommonError         `json:"common_errors"`
			OutOfScopeKnowledge []schema.OutOfScopeKnowledge `json:"out_of_scope_knowledge"`
			GradingNotes        string                       `json:"grading_notes"`
		} `json:"rubric"`
	} `json:"exercises"`
	Suggestions []struct {
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Reason string `json:"reason"`
	} `json:"suggestions"`
}

// Meta is what the app (not the AI) knows about the set being built.
type Meta struct {
	StableKey, TopicTitle string
	Version               int
	CreatedAt             string // RFC 3339 UTC
	Generator             schema.ManifestGenerator
	AllowedExts           []string
	CountMin, CountMax    int
}

// Answer is one answer template to create.
type Answer struct {
	ExID, FileName string
	Content        []byte
}

// Set is everything the commit writes, already validated.
type Set struct {
	Manifest     schema.Manifest
	Rubric       schema.Rubric
	ExercisesMD  []byte
	ManifestJSON []byte
	RubricJSON   []byte
	Answers      []Answer
	Out          Output
}

const maxWeight = 1000

// Build turns the AI output into the on-disk forms and checks them all (TDD §5.1, §5.2).
// Every failure wraps ai.ErrInvalidOutput so the caller retries once with the message.
func Build(raw json.RawMessage, m Meta) (*Set, error) {
	invalid := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ai.ErrInvalidOutput, fmt.Sprintf(format, a...))
	}
	if err := schema.ValidateAIExercisesOutput(raw); err != nil {
		return nil, invalid("%v", err)
	}
	var out Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, invalid("%v", err)
	}

	set := &Set{Out: out}
	set.Manifest = schema.Manifest{SchemaVersion: 1,
		Topic: schema.ManifestTopic{StableKey: m.StableKey, Title: m.TopicTitle},
		Set:   schema.ManifestSet{Version: m.Version, RubricVersion: 1, CreatedAt: m.CreatedAt, Generator: m.Generator}}
	set.Rubric = schema.Rubric{SchemaVersion: 1, RubricVersion: 1, SetVersion: m.Version, TopicStableKey: m.StableKey, Generator: m.Generator}

	var md strings.Builder
	fmt.Fprintf(&md, "# تمرین‌ها — %s (v%d)\n", oneLine(m.TopicTitle), m.Version)
	for i, x := range out.Exercises {
		id := fmt.Sprintf("q%02d", i+1) // ids are the app's, never the AI's
		title := oneLine(render.StripBidi(x.Title))
		statement := strings.TrimSpace(render.StripBidi(x.StatementMarkdown))
		if title == "" || statement == "" {
			return nil, invalid("exercise %d: empty title or statement_markdown", i+1)
		}
		ext := strings.ToLower(strings.TrimSpace(x.Answer.Ext))
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		ans := schema.ManifestAnswer{Slug: slug(x.Answer.Slug), Ext: ext, Language: strings.TrimSpace(x.Answer.Language)}
		ans.FileName = id + "-" + ans.Slug + ans.Ext
		set.Manifest.Exercises = append(set.Manifest.Exercises, schema.ManifestExercise{
			ID: id, Order: i + 1, Title: title, Type: x.Type, Difficulty: x.Difficulty,
			Weight: math.Min(x.Weight, maxWeight), Answer: ans, ExternalKnowledge: x.ExternalKnowledge,
		})
		r := x.Rubric
		set.Rubric.Exercises = append(set.Rubric.Exercises, schema.RubricExercise{ID: id, ReferenceAnswer: r.ReferenceAnswer,
			Criteria: r.Criteria, CommonErrors: r.CommonErrors, OutOfScopeKnowledge: outOfScope(r.OutOfScopeKnowledge, x.ExternalKnowledge),
			GradingNotes: r.GradingNotes})
		fmt.Fprintf(&md, "\n## [%s] %s\n\n%s\n", id, title, statement)
		set.Answers = append(set.Answers, Answer{ExID: id, FileName: ans.FileName,
			Content: fsx.GenerateAnswerTemplate(m.StableKey, m.Version, id, title, ext, x.Answer.Starter)})
	}
	set.ExercisesMD = []byte(md.String())

	var err error
	if set.ManifestJSON, err = json.MarshalIndent(set.Manifest, "", "  "); err != nil {
		return nil, err
	}
	if set.RubricJSON, err = json.MarshalIndent(set.Rubric, "", "  "); err != nil {
		return nil, err
	}
	set.ManifestJSON, set.RubricJSON = append(set.ManifestJSON, '\n'), append(set.RubricJSON, '\n')
	for _, check := range []func() error{
		func() error { return schema.ValidateManifest(set.ManifestJSON) },
		func() error { return schema.ValidateRubric(set.RubricJSON) },
		func() error { return schema.CrossCheckManifest(&set.Manifest, m.AllowedExts, m.CountMin, m.CountMax) },
		func() error { return schema.CrossCheckRubric(&set.Rubric) },
		func() error { return schema.CrossCheckManifestAndRubric(&set.Manifest, &set.Rubric) },
		func() error { return CheckHeadings(set.ExercisesMD, &set.Manifest) },
	} {
		if err := check(); err != nil {
			return nil, invalid("%v", err)
		}
	}
	return set, nil
}

var headingRe = regexp.MustCompile(`(?m)^## \[(q[0-9]{2,3})\] (.*)$`)

// CheckHeadings re-parses the built exercises.md: every manifest id exactly once, in order,
// as "## [qNN] <title>" (a statement that smuggles in its own "## [qNN]" heading fails here).
func CheckHeadings(md []byte, m *schema.Manifest) error {
	found := headingRe.FindAllSubmatch(md, -1)
	if len(found) != len(m.Exercises) {
		return fmt.Errorf("exercises.md has %d exercise headings, manifest has %d", len(found), len(m.Exercises))
	}
	for i, f := range found {
		ex := m.Exercises[i]
		if got := string(f[1]); got != ex.ID {
			return fmt.Errorf("exercises.md heading %d is %s, want %s", i+1, got, ex.ID)
		}
		if got := strings.TrimRight(string(f[2]), "\r"); got != ex.Title {
			return fmt.Errorf("exercises.md heading %s title is %q, want %q", ex.ID, got, ex.Title)
		}
	}
	return nil
}

// slug maps anything to the manifest slug pattern ^[a-z0-9]+(-[a-z0-9]+){0,5}$, max 40.
func slug(s string) string {
	parts := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
	if len(parts) > 6 {
		parts = parts[:6]
	}
	out := strings.Join(parts, "-")
	if len(out) > 40 { // ASCII only, so byte cut is safe
		out = strings.TrimRight(out[:40], "-")
	}
	if out == "" {
		return "answer"
	}
	return out
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// outOfScope adds a do_not_penalize entry for every external topic the AI left out of the rubric (R3.2, R8.3),
// so a forgotten mirror costs no retry; the crosscheck still guards every other path.
func outOfScope(oos []schema.OutOfScopeKnowledge, ext []schema.ExternalKnowledge) []schema.OutOfScopeKnowledge {
	have := map[string]bool{}
	for _, o := range oos {
		have[strings.ToLower(o.Topic)] = true
	}
	for _, e := range ext {
		if !have[strings.ToLower(e.Topic)] {
			have[strings.ToLower(e.Topic)] = true
			oos = append(oos, schema.OutOfScopeKnowledge{Topic: e.Topic, Handling: "do_not_penalize"})
		}
	}
	return oos
}
