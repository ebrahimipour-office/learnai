//go:build unix

package procgroup

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// CR-22: helpers left behind by a cleanly exited leader get the SIGTERM grace before SIGKILL.
func TestCloseGivesOrphansGrace(t *testing.T) {
	cmd, g, r := startTree(t, context.Background(), "parent-exit")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	g.Close()
	waitEOF(t, r, 5*time.Second)
	if d := time.Since(start); d < time.Second {
		t.Fatalf("SIGTERM-ignoring grandchild killed after %v, want >= 1s grace", d)
	}
}

// CR-22: once the group is gone, nothing more is signalled (its pgid may be reused).
func TestNoSignalAfterGroupGone(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0])
	cmd.Env = append(os.Environ(), "PROCGROUP_ROLE=exit")
	var mu sync.Mutex
	var sigs []syscall.Signal
	g, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	g.grace = 300 * time.Millisecond
	g.kill = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		sigs = append(sigs, sig)
		mu.Unlock()
		return syscall.Kill(pid, sig)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	g.Close() // idempotent
	time.Sleep(3 * g.grace)
	mu.Lock()
	defer mu.Unlock()
	if len(sigs) != 1 || sigs[0] != syscall.SIGTERM {
		t.Fatalf("signals after a clean exit = %v, want one SIGTERM (answered ESRCH)", sigs)
	}
}
