package skills

import (
	"testing"

	"learninghub/internal/fsx"
)

func TestInstallKeepsUserFiles(t *testing.T) {
	root, err := fsx.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteNew("profile.md", []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 { // AGENTS.md + 4 skills; profile.md already there
		t.Errorf("installed %d files, want 5", n)
	}
	for _, rel := range []string{"AGENTS.md", SkillRel("lesson-generator"), SkillRel("exercise-generator"), SkillRel("answer-evaluator")} {
		if !root.Exists(rel) {
			t.Errorf("%s not installed", rel)
		}
	}
	if b, _ := root.ReadFile("profile.md"); string(b) != "mine" {
		t.Errorf("profile.md overwritten: %q", b)
	}
	if n, err := Install(root); err != nil || n != 0 {
		t.Errorf("second install: n=%d err=%v", n, err)
	}
}
