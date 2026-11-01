// Package procgroup runs a child so that cancelling it kills its whole process tree (TDD §7.4).
package procgroup

import (
	"errors"
	"os"
	"os/exec"
)

// Start starts cmd in its own process group (unix) or Job Object (windows).
// It replaces cmd.Cancel, so cancelling an exec.CommandContext context kills the tree.
// Call Close after cmd.Wait to kill any descendants that outlived the child.
func Start(cmd *exec.Cmd) (*Group, error) {
	g, err := newGroup(cmd)
	if err != nil {
		return nil, err
	}
	cmd.Cancel = g.Kill
	if err := cmd.Start(); err != nil {
		g.release()
		return nil, err
	}
	g.attach()
	return g, nil
}

// Close kills whatever is left of the tree and releases OS resources. Safe to call twice.
func (g *Group) Close() error {
	g.Kill()
	g.release()
	return nil
}

func ignoreDone(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
