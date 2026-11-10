package skills

import (
	"errors"
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
	if n != 7 { // AGENTS.md + .gitignore + 5 skills; profile.md already there
		t.Errorf("installed %d files, want 7", n)
	}
	for _, rel := range []string{"AGENTS.md", ".gitignore", SkillRel("lesson-generator"), SkillRel("exercise-generator"), SkillRel("answer-evaluator"), SkillRel("chapter-detector")} {
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

func TestOutdatedAndUpgrade(t *testing.T) {
	root, err := fsx.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	old := []byte("---\nname: chapter-detector\nversion: 1.0.0\n---\nmy edits\n")
	if err := root.WriteNew(SkillRel("chapter-detector"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root); err != nil {
		t.Fatal(err)
	}
	emb, _ := FS.ReadFile("chapter-detector/SKILL.md")
	ups, err := Outdated(root)
	if err != nil || len(ups) != 1 || ups[0] != (Update{"chapter-detector", "1.0.0", Version(emb)}) {
		t.Fatalf("Outdated = %+v, %v", ups, err)
	}
	if b, _ := root.ReadFile(SkillRel("chapter-detector")); string(b) != string(old) {
		t.Fatal("Outdated must not touch the installed skill")
	}
	if err := Upgrade(root, "lesson-generator"); !errors.Is(err, ErrNotOutdated) {
		t.Errorf("upgrade of an up-to-date skill: %v", err)
	}
	if err := Upgrade(root, "chapter-detector"); err != nil {
		t.Fatal(err)
	}
	if b, _ := root.ReadFile(".agent/chapter-detector/SKILL.1.0.0.bak.md"); string(b) != string(old) {
		t.Errorf("backup = %q", b)
	}
	if b, _ := root.ReadFile(SkillRel("chapter-detector")); string(b) != string(emb) {
		t.Error("skill not upgraded")
	}
	if ups, _ := Outdated(root); len(ups) != 0 {
		t.Errorf("still outdated: %+v", ups)
	}
}

func TestOlder(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.0.0", "2.0.0", true}, {"1.9.0", "1.10.0", true}, {"1.10.0", "1.9.0", false}, {"2.0.0", "2.0.0", false}, {"1.2", "1.2.1", true}} {
		if got := older(c.a, c.b); got != c.want {
			t.Errorf("older(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
