package topics_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"learninghub/internal/domain"
	"learninghub/internal/fsx"
	"learninghub/internal/services/topics"
)

func TestSelectForEvaluation(t *testing.T) {
	base := t.TempDir()
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	tpl, done, changed := []byte("// template\n"), []byte("answer v1\n"), []byte("answer v2\n")
	files := map[string][]byte{
		"empty": tpl, "new": done, "same": done, "changed": changed,
		"big": []byte(strings.Repeat("x", 65)), "binary": []byte("a\x00b"),
	}
	for id, b := range files {
		if err := os.WriteFile(filepath.Join(base, id+".txt"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ex := func(id, evaluated string) topics.Exercise {
		return topics.Exercise{ExID: id, AnswerRel: id + ".txt", TemplateSHA: fsx.HashBytes(tpl), EvaluatedSHA: evaluated}
	}
	list := []topics.Exercise{
		ex("empty", ""), ex("missing", ""), ex("new", ""), ex("same", fsx.HashBytes(done)),
		ex("changed", fsx.HashBytes(done)), ex("big", ""), ex("binary", ""),
	}
	ids := func(s *topics.Selection) (send, review []string) {
		for _, p := range s.Send {
			send = append(send, p.ExID)
			if p.SHA != fsx.HashBytes(files[p.ExID]) || string(p.Body) != string(files[p.ExID]) {
				t.Errorf("%s: pick body/hash differ from disk", p.ExID)
			}
		}
		for _, x := range s.Review {
			review = append(review, x.ExID)
		}
		return
	}

	for mode, want := range map[string][]string{
		topics.ModeIncremental: {"new", "changed"},
		topics.ModeFull:        {"new", "same", "changed"},
	} {
		s, err := topics.SelectForEvaluation(root, list, mode, 64)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		send, review := ids(s)
		if !slices.Equal(send, want) {
			t.Errorf("%s: send = %v, want %v (unanswered and missing never sent)", mode, send, want)
		}
		if !slices.Equal(review, []string{"big", "binary"}) {
			t.Errorf("%s: review = %v, want [big binary]", mode, review)
		}
	}

	// Only unanswered, unchanged and manual-review answers: nothing to evaluate.
	s, err := topics.SelectForEvaluation(root, []topics.Exercise{ex("empty", ""), ex("same", fsx.HashBytes(done)), ex("big", "")}, topics.ModeIncremental, 64)
	if !errors.Is(err, domain.ErrNothingToEvaluate) || len(s.Review) != 1 {
		t.Errorf("want ErrNothingToEvaluate with big in review, got %v %+v", err, s)
	}
	if _, err := topics.SelectForEvaluation(root, list, "partial", 64); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("unknown mode: want ErrInvalidInput, got %v", err)
	}
}
