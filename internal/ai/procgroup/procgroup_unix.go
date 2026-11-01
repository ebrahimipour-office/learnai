//go:build unix

package procgroup

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// killGrace is the SIGTERM → SIGKILL delay (TDD §7.4).
const killGrace = 3 * time.Second

type Group struct{ cmd *exec.Cmd }

func newGroup(cmd *exec.Cmd) (*Group, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &Group{cmd: cmd}, nil
}

func (g *Group) attach() {}

// Kill sends SIGTERM to the group, then SIGKILL after killGrace even if the leader already exited.
// shortcut: a hard crash of hub (SIGKILL) leaves the group running on unix; windows covers it via KILL_ON_JOB_CLOSE.
func (g *Group) Kill() error {
	if g.cmd.Process == nil {
		return nil
	}
	pgid := g.cmd.Process.Pid
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	time.AfterFunc(killGrace, func() { syscall.Kill(-pgid, syscall.SIGKILL) })
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (g *Group) release() {
	if g.cmd.Process != nil {
		syscall.Kill(-g.cmd.Process.Pid, syscall.SIGKILL)
	}
}
