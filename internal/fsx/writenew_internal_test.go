package fsx

import (
	"errors"
	"os"
	"testing"
)

// T13.4: the no-hard-link fallback never leaves a partial file under the final name.
func TestWriteNewDirectCrashLeavesNoFinalFile(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.MkdirAll("d", 0o755); err != nil {
		t.Fatal(err)
	}
	defer SetWriteSyncHookForTest(func(f *os.File, data []byte) error {
		_, _ = f.Write(data[:1])
		f.Close()
		return errors.New("simulated crash")
	})()
	if err := r.writeNewDirect("d/f.md", "", []byte("full"), 0o644); err == nil {
		t.Fatal("expected error")
	}
	if entries, _ := os.ReadDir(dir + "/d"); len(entries) != 0 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestWriteNewDirectNeverOverwrites(t *testing.T) {
	r, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.writeNewDirect("f.md", "", []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.writeNewDirect("f.md", "", []byte("two"), 0o644); !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}
	if b, _ := r.ReadFile("f.md"); string(b) != "one" {
		t.Fatalf("content = %q", b)
	}
}
