package httpserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"learninghub/internal/fsx"
	"learninghub/internal/render"
)

func TestMDCache(t *testing.T) {
	base := t.TempDir()
	root, err := fsx.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	c := &mdCache{root: root, max: 64, entries: map[mdKey]rendered{}}
	path := filepath.Join(base, "a.md")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	get := func() rendered {
		t.Helper()
		r, err := c.get("a.md")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	write("# one\n\n## two\n")
	r := get()
	get()
	if c.renders != 1 || !strings.Contains(string(r.HTML), "one") || len(r.TOC) != len(render.TOC([]byte("# one\n\n## two\n"))) {
		t.Fatalf("two GETs: want one render, got %d (%q)", c.renders, r.HTML)
	}
	write("# three\n") // a rewrite changes the key
	if r := get(); c.renders != 2 || !strings.Contains(string(r.HTML), "three") {
		t.Fatalf("changed file: want re-render, got %d (%q)", c.renders, r.HTML)
	}
	write("# thref\n") // same size: the mtime tells them apart
	if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := get(); c.renders != 3 || !strings.Contains(string(r.HTML), "thref") {
		t.Fatalf("same-size edit: want re-render, got %d (%q)", c.renders, r.HTML)
	}

	write(strings.Repeat("a", 65))
	if _, err := c.get("a.md"); !errors.Is(err, errTooLarge) {
		t.Errorf("over the cap: want errTooLarge, got %v", err)
	}
	if _, err := c.get("gone.md"); err == nil || errors.Is(err, errTooLarge) {
		t.Errorf("missing file: want an fs error, got %v", err)
	}
	if got := fileError(errTooLarge, "x: ", "a.md"); got != tooLargeMsg+"a.md" {
		t.Errorf("fileError: %q", got)
	}
}
