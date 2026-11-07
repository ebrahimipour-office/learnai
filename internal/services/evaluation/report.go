package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"learninghub/internal/fsx"
	"learninghub/internal/render"
	"learninghub/internal/schema"
	"learninghub/internal/scoring"
)

// Evaluation is a validated ai-evaluation-output (see Check).
type Evaluation struct {
	ExerciseID     string `json:"exercise_id"`
	CriteriaScores []struct {
		CriterionID string `json:"criterion_id"`
		Awarded     int    `json:"awarded"`
		Comment     string `json:"comment"`
	} `json:"criteria_scores"`
	Score  int `json:"score"`
	Errors []struct {
		Category    string `json:"category"`
		Severity    string `json:"severity"`
		Description string `json:"description"`
		Location    string `json:"location"`
	} `json:"errors"`
	Strengths       []string `json:"strengths"`
	CorrectedAnswer struct {
		Format   string `json:"format"`
		Language string `json:"language"`
		Content  string `json:"content"`
	} `json:"corrected_answer"`
	TeachingNotesMarkdown string `json:"teaching_notes_markdown"`
}

// ReportInput is everything one qNN-report.md is built from (R3.5).
type ReportInput struct {
	Weight          float64
	Question        []byte // the exercise's exercises.md section, "## [qNN] title" included
	Answer          []byte // the answer file's exact bytes
	TemplateNormSHA string // topics.Exercise.TemplateNormSHA; "" = skip the template check
	Rubric          schema.RubricExercise
	Eval            Evaluation
	AttemptNo       int
	RubricVersion   int
	SkillVersion    string
	Date            string // RFC 3339
}

func ReportFile(exID string) string { return exID + "-report.md" }

const rawMarker = "<!-- raw-answer sha256="

var errorCategories = []struct{ key, label string }{
	{"conceptual", "مفهومی"}, {"logical", "منطقی"}, {"technical", "فنی"}, {"style", "سبک"}, {"omission", "جاافتادگی"},
}

// Report builds qNN-report.md, its result.json item and its review flags (Flags). The official score is the sum of awarded
// points (scoring), never the AI's own score; the answer is embedded byte-for-byte (R11.2-5).
func Report(in ReportInput) ([]byte, schema.ResultItem, []string) {
	id := in.Rubric.ID
	maxPts := map[string]int{}
	for _, c := range in.Rubric.Criteria {
		maxPts[c.ID] = c.MaxPoints
	}
	awarded := map[string]int{}
	comments := map[string]string{}
	var cs []schema.CriteriaScore
	for _, c := range in.Eval.CriteriaScores {
		awarded[c.CriterionID], comments[c.CriterionID] = c.Awarded, c.Comment
	}
	for _, c := range in.Rubric.Criteria { // rubric order, so reports and result.json are stable
		cs = append(cs, schema.CriteriaScore{CriterionID: c.ID, Awarded: awarded[c.ID], Max: c.MaxPoints})
	}
	score, mismatch := scoring.ExerciseScore(cs, in.Eval.Score)
	sha := fsx.HashBytes(in.Answer)
	flags := Flags(in.Answer, in.TemplateNormSHA, score)

	var b bytes.Buffer
	fmt.Fprintf(&b, "# گزارش تصحیح %s\n\n", id)
	fmt.Fprintf(&b, "- تلاش: %d\n- تاریخ: %s\n- نسخه Rubric: %d\n- نسخه Skill: %s\n\n", in.AttemptNo, in.Date, in.RubricVersion, in.SkillVersion)
	if len(flags) > 0 {
		b.WriteString("> **⚠ نیازمند بازبینی**\n")
		for _, f := range flags {
			fmt.Fprintf(&b, "> - %s\n", flagText[f])
		}
		b.WriteString("\n")
	}
	b.Write(bytes.TrimRight(in.Question, "\n"))
	b.WriteString("\n\n## پاسخ شما (متن خام)\n\n")
	fmt.Fprintf(&b, "sha256: `%s`\n\n%s%s -->\n", sha, rawMarker, sha)
	f := fence(in.Answer)
	b.WriteString(f + "\n")
	b.Write(in.Answer)
	b.WriteString("\n" + f + "\n")

	fmt.Fprintf(&b, "\n## نمره: %d از ۱۰۰\n\n", score)
	if mismatch {
		fmt.Fprintf(&b, "> نمره اعلام‌شده توسط AI %d بود؛ نمره رسمی جمع امتیاز معیارهاست.\n\n", in.Eval.Score)
	}
	b.WriteString("| معیار | شرح | امتیاز | حداکثر | نظر |\n|---|---|---|---|---|\n")
	for _, c := range in.Rubric.Criteria {
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %s |\n", cell(c.ID), cell(c.Description), awarded[c.ID], c.MaxPoints, cell(comments[c.ID]))
	}

	b.WriteString("\n## خطاها\n\n")
	var types []string
	for _, cat := range errorCategories {
		first := true
		for _, e := range in.Eval.Errors {
			if e.Category != cat.key {
				continue
			}
			if first {
				fmt.Fprintf(&b, "### %s\n\n", cat.label)
				types, first = append(types, cat.key), false
			}
			fmt.Fprintf(&b, "- **[%s]** %s", e.Severity, oneLine(e.Description))
			if loc := oneLine(e.Location); loc != "" {
				fmt.Fprintf(&b, " — `%s`", strings.ReplaceAll(loc, "`", "'"))
			}
			b.WriteString("\n")
		}
		if !first {
			b.WriteString("\n")
		}
	}
	if len(types) == 0 {
		b.WriteString("خطایی ثبت نشد.\n\n")
	}

	if len(in.Eval.Strengths) > 0 {
		b.WriteString("## نقاط قوت\n\n")
		for _, s := range in.Eval.Strengths {
			fmt.Fprintf(&b, "- %s\n", oneLine(s))
		}
		b.WriteString("\n")
	}

	b.WriteString("## پاسخ اصلاح‌شده\n\n")
	ca := render.StripBidi(in.Eval.CorrectedAnswer.Content)
	if in.Eval.CorrectedAnswer.Format == "code" {
		f := fence([]byte(ca))
		fmt.Fprintf(&b, "%s%s\n%s\n%s\n", f, oneLine(in.Eval.CorrectedAnswer.Language), ca, f)
	} else {
		b.WriteString(strings.TrimSpace(ca) + "\n")
	}
	b.WriteString("\n## توضیح آموزشی\n\n")
	b.WriteString(strings.TrimSpace(render.StripBidi(in.Eval.TeachingNotesMarkdown)) + "\n")

	rf := ReportFile(id)
	return b.Bytes(), schema.ResultItem{ExerciseID: id, Weight: in.Weight, Status: "evaluated", Score: score,
		AnswerSHA256: &sha, ReportFile: &rf, CriteriaScores: cs, ErrorTypes: types}, flags
}

// RawAnswer returns the answer bytes embedded in a report built by Report.
func RawAnswer(report []byte) ([]byte, bool) {
	i := bytes.Index(report, []byte(rawMarker))
	if i < 0 {
		return nil, false
	}
	rest := report[i:]
	nl := bytes.IndexByte(rest, '\n')
	if nl < 0 {
		return nil, false
	}
	rest = rest[nl+1:]
	nl = bytes.IndexByte(rest, '\n')
	if nl < 0 {
		return nil, false
	}
	f := rest[:nl]
	body := rest[nl+1:]
	// fence() is longer than any backtick run in the answer, so the first closing fence is ours.
	end := bytes.Index(body, append(append([]byte("\n"), f...), '\n'))
	if end < 0 {
		return nil, false
	}
	return body[:end], true
}

// Summary builds summary.md: topic score, the «ناقص» label and the exercise table.
// flagged marks the exercises needing review.
func Summary(res *schema.Result, titles map[string]string, flagged map[string]bool) []byte {
	a := res.Aggregate
	var b bytes.Buffer
	fmt.Fprintf(&b, "# خلاصه ارزیابی — %s\n\n", oneLine(res.Topic.Title))
	fmt.Fprintf(&b, "- تلاش: %d (%s)\n- مجموعه: v%d · نسخه Rubric: %d\n- تاریخ: %s\n\n",
		res.AttemptNo, res.Mode, res.SetVersion, res.RubricVersion, res.FinishedAt)
	fmt.Fprintf(&b, "## نمره موضوع: %s از ۱۰۰\n\n", trimFloat(a.WeightedScore))
	if a.Partial {
		fmt.Fprintf(&b, "**ناقص (%d از %d پاسخ‌داده‌شده)**\n\n", a.AnsweredCount, a.TotalCount)
	}
	b.WriteString("| تمرین | عنوان | وزن | وضعیت | نمره |\n|---|---|---|---|---|\n")
	status := map[string]string{"evaluated": "ارزیابی‌شده", "carried_over": "منتقل‌شده", "unanswered": "بدون پاسخ"}
	for _, it := range res.Items {
		id := it.ExerciseID
		if it.ReportFile != nil {
			id = fmt.Sprintf("[%s](%s)", id, *it.ReportFile)
		}
		st := status[it.Status]
		if it.CarriedFromAttempt != nil {
			st = fmt.Sprintf("%s از تلاش %d", st, *it.CarriedFromAttempt)
		}
		if flagged[it.ExerciseID] {
			st += " ⚠ نیازمند بازبینی"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d |\n", id, cell(titles[it.ExerciseID]), trimFloat(it.Weight), st, it.Score)
	}
	fmt.Fprintf(&b, "\nفرمول: `%s` — محاسبه توسط نرم‌افزار.\n", a.Formula)
	return b.Bytes()
}

// ResultJSON sets the aggregate from scoring (never the AI) and returns result.json, validated
// against the schema and cross-checked against the manifest (section 5.3).
func ResultJSON(res *schema.Result, m *schema.Manifest) ([]byte, error) {
	res.SchemaVersion = 1
	res.Aggregate = scoring.Aggregate(res.Items)
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	if err := schema.ValidateResult(b); err != nil {
		return nil, err
	}
	if err := schema.CrossCheckResult(res, m); err != nil {
		return nil, err
	}
	return b, nil
}

// fence returns a backtick fence longer than any backtick run in b, so b cannot close it.
func fence(b []byte) string {
	n, run := 3, 0
	for _, c := range b {
		if c != '`' {
			run = 0
			continue
		}
		if run++; run >= n {
			n = run + 1
		}
	}
	return strings.Repeat("`", n)
}

func oneLine(s string) string { return strings.Join(strings.Fields(render.StripBidi(s)), " ") }

func cell(s string) string { return strings.ReplaceAll(oneLine(s), "|", `\|`) }

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
