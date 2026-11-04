package topics

import (
	"bytes"
	"errors"
	"io/fs"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
)

// Evaluation modes (POST /topics/{id}/jobs/evaluate mode=).
const (
	ModeIncremental = "incremental"
	ModeFull        = "full"
)

// Pick is an answer chosen for evaluation, with the bytes and hash read once so what is sent is what was hashed.
type Pick struct {
	Exercise
	SHA  string
	Body []byte
}

// Selection splits a set's exercises for an evaluation (R3.4). Exercises in neither list are
// unanswered, missing, or (incremental) unchanged since their last evaluation.
type Selection struct {
	Send   []Pick     // go to the AI
	Review []Exercise // over max_answer_bytes or binary (NUL byte): "needs manual review", never sent (RK17)
}

// SelectForEvaluation picks the answers to send. Unanswered files are never sent. Incremental sends
// answers never evaluated or changed since; full sends every answered one. Nothing to send ⇒ domain.ErrNothingToEvaluate.
func SelectForEvaluation(root *fsx.Root, list []Exercise, mode string, maxBytes int64) (*Selection, error) {
	if mode != ModeIncremental && mode != ModeFull {
		return nil, domain.ErrInvalidInput
	}
	sel := &Selection{}
	for _, x := range list {
		info, err := root.Stat(x.AnswerRel)
		if errors.Is(err, fs.ErrNotExist) {
			continue // missing
		}
		if err != nil {
			return nil, err
		}
		if info.Size() > maxBytes {
			sel.Review = append(sel.Review, x)
			continue
		}
		b, err := root.ReadFile(x.AnswerRel)
		if err != nil {
			return nil, err
		}
		sha := fsx.HashBytes(b)
		switch x.AnswerStatus(sha) {
		case AnswerEmpty:
			continue
		case AnswerDone:
			if mode == ModeIncremental && x.EvaluatedSHA != "" {
				continue // unchanged since its last evaluation; carried over (I3)
			}
		}
		if int64(len(b)) > maxBytes || bytes.IndexByte(b, 0) >= 0 {
			sel.Review = append(sel.Review, x)
			continue
		}
		sel.Send = append(sel.Send, Pick{Exercise: x, SHA: sha, Body: b})
	}
	if len(sel.Send) == 0 {
		return sel, domain.ErrNothingToEvaluate
	}
	return sel, nil
}
