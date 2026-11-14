//go:build unix

package procgroup

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// killGrace is the SIGTERM → SIGKILL delay (TDD §7.4).
const killGrace = 3 * time.Second

// pollEvery is how often escalation checks whether the group is already gone.
const pollEvery = 50 * time.Millisecond

type Group struct {
	cmd   *exec.Cmd
	once  sync.Once
	err   error
	grace time.Duration
	kill  func(pid int, sig syscall.Signal) error // syscall.Kill; tests spy on it
}

func newGroup(cmd *exec.Cmd) (*Group, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &Group{cmd: cmd, grace: killGrace, kill: syscall.Kill}, nil
}

func (g *Group) attach() {}

// Kill sends SIGTERM to the group once, then SIGKILL after the grace period unless the group is
// gone by then (even if the leader already exited, its helpers get the same grace).
// shortcut: a hard crash of hub (SIGKILL) leaves the group running on unix; windows covers it via KILL_ON_JOB_CLOSE.
func (g *Group) Kill() error {
	if g.cmd.Process == nil {
		return nil
	}
	g.once.Do(func() {
		pgid := g.cmd.Process.Pid
		if g.err = g.kill(-pgid, syscall.SIGTERM); errors.Is(g.err, syscall.ESRCH) {
			g.err = nil // group already gone: nothing to escalate
			return
		}
		go g.escalate(pgid)
	})
	return g.err
}

// escalate SIGKILLs the group after the grace period. It stops as soon as the group is gone:
// the pgid stays reserved only while the group exists, so a later signal could hit a reused one.
func (g *Group) escalate(pgid int) {
	for deadline := time.Now().Add(g.grace); time.Now().Before(deadline); {
		time.Sleep(pollEvery)
		if errors.Is(g.kill(-pgid, 0), syscall.ESRCH) {
			return
		}
	}
	g.kill(-pgid, syscall.SIGKILL)
}

// release has nothing to free on unix; survivors are left to Kill's escalation.
func (g *Group) release() {}
