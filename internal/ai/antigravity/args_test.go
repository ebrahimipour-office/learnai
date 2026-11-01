package antigravity

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildArgs(t *testing.T) {
	wd := filepath.Join(t.TempDir(), "work")
	got, err := buildArgs(argSpec{
		Prompt: "-starts with dash", WorkDir: wd, HasSchema: true,
		Model: "m1", Effort: "high", Timeout: 10 * time.Minute, Sandbox: true,
		Extra: []string{"--disable-slash-commands"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-p", "-starts with dash", "--output-format", "stream-json",
		"--json-schema", filepath.Join(wd, "output.schema.json"),
		"--model", "m1", "--effort", "high", "--print-timeout", "10m0s",
		"--sandbox", "--disable-slash-commands",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("args\n got %q\nwant %q", got, want)
	}
}

func TestBuildArgsRejects(t *testing.T) {
	base := argSpec{Prompt: "x", WorkDir: "/w", Model: "m"}
	for _, extra := range []string{
		"--dangerously-skip-permissions",
		"--dangerously-skip-permissions=true",
		" --dangerously-skip-permissions",
		"--mode=auto",
		"-mode=accept-edits",
		"-add-dir=/",
		"--add-dir",
		"--output-format=text",
		"--json-schema",
		"-p",
		"--conversation=abc",
	} {
		s := base
		s.Extra = []string{extra}
		if _, err := buildArgs(s); !errors.Is(err, ErrForbiddenArg) {
			t.Errorf("extra %q: want ErrForbiddenArg, got %v", extra, err)
		}
	}
	for name, s := range map[string]argSpec{
		"empty prompt": {Prompt: "  ", Model: "m"},
		"no model":     {Prompt: "x"},
		"flag model":   {Prompt: "x", Model: "--sandbox"},
		"bad effort":   {Prompt: "x", Model: "m", Effort: "turbo"},
	} {
		if _, err := buildArgs(s); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMask(t *testing.T) {
	in := `Authorization: Bearer abc.DEF-123 key=AIzaSyA1234567890abcdefghijklmnopqrstuv ` +
		`{"access_token":"ya29.secretvalue","total_tokens":42} sk-abcdefghijklmnopqrstu password=hunter2`
	out := mask(in)
	for _, leak := range []string{"abc.DEF-123", "AIzaSyA", "ya29.secret", "sk-abcdef", "hunter2"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, `"total_tokens":42`) {
		t.Errorf("over-masked usage counters: %q", out)
	}
}
