package topics

import (
	"bytes"
	"errors"
	"io"
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
// answers never evaluated or changed since; full sends every answered one. A non-empty only (T10.18)
// limits the selection to those exercise IDs, each sent if answered whatever the mode; an ID not in
// list ⇒ domain.ErrInvalidInput. Nothing to send ⇒ domain.ErrNothingToEvaluate.
func SelectForEvaluation(root *fsx.Root, list []Exercise, mode string, only []string, maxBytes int64) (*Selection, error) {
	if mode != ModeIncremental && mode != ModeFull {
		return nil, domain.ErrInvalidInput
	}
	known := map[string]bool{}
	for _, x := range list {
		known[x.ExID] = true
	}
	pick := map[string]bool{}
	for _, id := range only {
		if !known[id] {
			return nil, domain.ErrInvalidInput
		}
		pick[id] = true
	}
	sel := &Selection{}
	for _, x := range list {
		if len(only) > 0 && !pick[x.ExID] {
			continue // not chosen: the commit carries it over or leaves it unanswered
		}
		f, err := root.Open(x.AnswerRel)
		if errors.Is(err, fs.ErrNotExist) {
			continue // missing
		}
		if err != nil {
			return nil, err
		}
		b, over, err := readCapped(f, maxBytes)
		f.Close()
		if err != nil {
			return nil, err
		}
		if over {
			sel.Review = append(sel.Review, x)
			continue
		}
		sha := fsx.HashBytes(b)
		switch x.AnswerStatus(sha) {
		case AnswerEmpty:
			continue
		case AnswerDone:
			if mode == ModeIncremental && len(only) == 0 && x.EvaluatedSHA != "" {
				continue // unchanged since its last evaluation; carried over (I3)
			}
		}
		if bytes.IndexByte(b, 0) >= 0 {
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

// readCapped reads at most max+1 bytes, so a file that grows after any size check cannot exhaust memory.
func readCapped(r io.Reader, max int64) (b []byte, over bool, err error) {
	b, err = io.ReadAll(io.LimitReader(r, max+1))
	return b, int64(len(b)) > max, err
}
