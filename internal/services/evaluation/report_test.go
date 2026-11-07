package evaluation

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"learninghub/internal/fsx"
	"learninghub/internal/schema"
	"learninghub/internal/scoring"
)

var testRubric = schema.RubricExercise{ID: "q01", Criteria: []schema.Criterion{
	{ID: "c1", Description: "درستی | منطق", MaxPoints: 60, Category: "logical"},
	{ID: "c2", Description: "سبک", MaxPoints: 40, Category: "style"},
}}

func testEval(t *testing.T) Evaluation {
	t.Helper()
	var ev Evaluation
	raw := `{"exercise_id":"q01","criteria_scores":[{"criterion_id":"c2","awarded":30,"comment":"خوب"},
		{"criterion_id":"c1","awarded":50,"comment":"یک حالت مرزی جا افتاده"}],"score":95,
		"errors":[{"category":"omission","severity":"major","description":"nil بررسی نشده","location":"line 3"},
		{"category":"conceptual","severity":"minor","description":"x‮y"}],
		"strengths":["ساده"],"corrected_answer":{"format":"code","language":"go","content":"x := 1\n` + "```" + `"},
		"teaching_notes_markdown":"**نکته**"}`
	if err := Check(json.RawMessage(raw), "q01", testRubric); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// R11.2-5: the raw answer inside the report equals the file byte-for-byte.
func TestReportRawAnswerByteExact(t *testing.T) {
	ev := testEval(t)
	pieces := []string{"`", "``", "```", "````````", "\r\n", "\n", "\xEF\xBB\xBF", "سلام دنیا", "~~~", "<!-- x -->", " ", "a", "\r", "\x00"}
	cases := [][]byte{{}, []byte("\xEF\xBB\xBF"), []byte("```\n"), []byte("\n```"), []byte("a\r\n```\r\n")}
	r := rand.New(rand.NewPCG(1, 2))
	for range 500 {
		var b bytes.Buffer
		for range r.IntN(40) {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		cases = append(cases, b.Bytes())
	}
	for _, ans := range cases {
		md, item := Report(ReportInput{Weight: 1, Question: []byte("## [q01] t\n\nبنویس\n"), Answer: ans,
			Rubric: testRubric, Eval: ev, AttemptNo: 1, RubricVersion: 1, SkillVersion: "1.0.0", Date: "2026-11-07T10:00:00Z"})
		got, ok := RawAnswer(md)
		if !ok || !bytes.Equal(got, ans) {
			t.Fatalf("answer %q: extracted %q (ok=%v)", ans, got, ok)
		}
		if *item.AnswerSHA256 != fsx.HashBytes(ans) {
			t.Fatalf("sha mismatch for %q", ans)
		}
	}
}

func TestReportSectionsAndScore(t *testing.T) {
	md, item := Report(ReportInput{Weight: 2, Question: []byte("## [q01] عنوان\n\nصورت\n"), Answer: []byte("x"),
		Rubric: testRubric, Eval: testEval(t), AttemptNo: 3, RubricVersion: 2, SkillVersion: "1.2.0", Date: "2026-11-07T10:00:00Z"})
	s := string(md)
	for _, want := range []string{"## [q01] عنوان", "صورت", "sha256: `" + fsx.HashBytes([]byte("x")), "## نمره: 80 از ۱۰۰",
		"نمره اعلام‌شده توسط AI 95", `درستی \| منطق`, "### مفهومی", "### جاافتادگی", "`line 3`", "## نقاط قوت",
		"````go\nx := 1\n```\n````", "**نکته**", "تلاش: 3", "نسخه Rubric: 2", "نسخه Skill: 1.2.0", "2026-11-07T10:00:00Z"} {
		if !strings.Contains(s, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(s, "‮") {
		t.Error("bidi control from AI text kept")
	}
	if item.Score != 80 || *item.ReportFile != "q01-report.md" || item.CriteriaScores[0].CriterionID != "c1" ||
		strings.Join(item.ErrorTypes, ",") != "conceptual,omission" {
		t.Errorf("item = %+v", item)
	}
}

func TestResultJSONAndSummary(t *testing.T) {
	_, ev := Report(ReportInput{Weight: 1, Question: []byte("## [q01] a\n"), Answer: []byte("x"), Rubric: testRubric, Eval: testEval(t), AttemptNo: 2})
	sha := strings.Repeat("a", 64)
	from := 1
	m := &schema.Manifest{Exercises: []schema.ManifestExercise{{ID: "q01", Weight: 1}, {ID: "q02", Weight: 2}, {ID: "q03", Weight: 1}}}
	res := &schema.Result{Topic: schema.ManifestTopic{StableKey: "a3f9c2d41b7e8f06", Title: "HTTP"}, SetVersion: 1, AttemptNo: 2,
		Mode: "incremental", RubricVersion: 1, StartedAt: "2026-11-07T10:00:00Z", FinishedAt: "2026-11-07T10:03:00Z", JobID: 5,
		Evaluator: schema.ManifestGenerator{Adapter: "test", Skill: skillName, SkillVersion: "1.0.0"},
		Aggregate: schema.ResultAggregate{WeightedScore: 99}, // AI or stale value must be replaced
		Items: []schema.ResultItem{ev,
			{ExerciseID: "q02", Weight: 2, Status: "carried_over", Score: 70, AnswerSHA256: &sha, CarriedFromAttempt: &from},
			{ExerciseID: "q03", Weight: 1, Status: "unanswered"}}}
	b, err := ResultJSON(res, m)
	if err != nil {
		t.Fatal(err)
	}
	var back schema.Result
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if want := scoring.Aggregate(res.Items); back.Aggregate != want || want.WeightedScore != 55 || !want.Partial {
		t.Errorf("aggregate = %+v, want %+v", back.Aggregate, want)
	}

	s := string(Summary(res, map[string]string{"q01": "اول", "q02": "دوم"}))
	for _, want := range []string{"HTTP", "نمره موضوع: 55 از ۱۰۰", "ناقص (2 از 3 پاسخ‌داده‌شده)", "[q01](q01-report.md)",
		"منتقل‌شده از تلاش 1", "بدون پاسخ", "| q02 | دوم | 2 |"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q\n%s", want, s)
		}
	}

	res.Items = res.Items[:2] // not every manifest exercise covered
	if _, err := ResultJSON(res, m); err == nil {
		t.Error("result missing a manifest exercise accepted")
	}
}
