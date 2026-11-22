package procgroup

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// T12.2: Sweep kills what runs inside the work dir (and below it), nothing outside it.
func TestSweep(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	start := func(dir string) *exec.Cmd {
		cmd := exec.Command("sleep", "30")
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() })
		return cmd
	}
	for _, d := range []string{work, filepath.Join(work, "7", "q01"), filepath.Join(base, "workshop")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	in, deep, out := start(work), start(filepath.Join(work, "7", "q01")), start(filepath.Join(base, "workshop"))
	if n := Sweep(work); n != 2 {
		t.Fatalf("Sweep killed %d, want 2", n)
	}
	for _, c := range []*exec.Cmd{in, deep} {
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case err := <-done:
			if ee := (*exec.ExitError)(nil); !errors.As(err, &ee) {
				t.Errorf("pid %d: wait = %v, want killed", c.Process.Pid, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("pid %d still running after Sweep", c.Process.Pid)
		}
	}
	if err := syscall.Kill(out.Process.Pid, 0); err != nil {
		t.Fatalf("process outside the work dir was touched: %v", err)
	}
	if n := Sweep(filepath.Join(base, "missing")); n != 0 {
		t.Errorf("Sweep of an empty dir killed %d", n)
	}
}

// T12.2: when hub dies by SIGKILL (no cleanup runs), the kernel kills the child it started (Pdeathsig).
func TestHardCrashKillsChild(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	hub := exec.Command(os.Args[0])
	hub.Env = append(os.Environ(), "PROCGROUP_ROLE=hub")
	hub.Stdout = w
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "ready\n" {
		t.Fatalf("child did not start: %q %v", buf, err)
	}
	hub.Process.Kill()
	hub.Wait()
	waitEOF(t, r, 5*time.Second) // the child held the pipe too: EOF means it is gone
}
