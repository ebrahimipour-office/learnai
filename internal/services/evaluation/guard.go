package evaluation

import (
	"regexp"

	"learninghub/internal/fsx"
)

// «نیازمند بازبینی» reasons (T5.6, RK5). They never change the score: the official score is
// always the criteria sum; a flag only tells the user not to trust it blindly.
const (
	FlagInjection         = "injection_suspected" // the answer talks to the grader
	FlagTemplateHighScore = "template_high_score" // template re-spaced, yet a high score
	FlagNeedsReview       = "needs_review"        // over max_answer_bytes or binary: never sent, scored 0 (RK17, CR-09)
)

// templateHighScore is the score from which an unchanged template is suspicious.
const templateHighScore = 50

var flagText = map[string]string{
	FlagInjection:         "پاسخ شامل متنی شبیه دستور به ارزیاب است؛ نمره فقط جمع امتیاز معیارهاست و باید بازبینی شود.",
	FlagTemplateHighScore: "پاسخ جز فاصله‌گذاری با قالب اولیه فرقی ندارد ولی نمره بالا گرفته است.",
}

// shortcut: phrase list, not a classifier; obfuscated injections pass. The answerer is the user
// themself (RK5 accepted), so this only catches the obvious and keeps the score transparent.
var injectionRe = regexp.MustCompile(`(?i)` +
	`\b(ignore|disregard|forget|override)\s+(all\s+|any\s+)?(of\s+)?(the\s+|your\s+)?(previous|prior|above|earlier|grading|system)?\s*(instructions|rules|prompts?|rubric)\b` +
	`|\b(give|award|assign|grant)\s+(me\s+|this\s+(answer\s+)?)?(a\s+|the\s+)?(full|perfect|maximum|max|100|top)\s*(%|percent)?\s*(score|marks|points|grade)\b` +
	`|\bscore\s*[:=]\s*100\b|\bsystem\s+prompt\b|\byou\s+are\s+now\b|\bas\s+the\s+(grader|evaluator)\b` +
	`|نادیده\s*بگیر|نمره\s*(ی\s*)?(کامل|۱۰۰|100)|دستور(العمل)?(‌ها|ها)?\s*(ی\s*)?(قبلی|بالا)`)

// Flags returns the review reasons for one evaluated answer.
func Flags(answer []byte, templateNormSHA string, score int) []string {
	var f []string
	if injectionRe.MatchString(fsx.ExtractAnswerContent(answer)) {
		f = append(f, FlagInjection)
	}
	if templateNormSHA != "" && score >= templateHighScore && fsx.NormalizedAnswerSHA(answer) == templateNormSHA {
		f = append(f, FlagTemplateHighScore)
	}
	return f
}
