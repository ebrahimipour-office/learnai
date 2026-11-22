package procgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Sweep SIGKILLs every process whose working directory is dir or under it, and returns how many.
// Run at startup on <base>/.app/work: only hub starts processes there (AI tools, go vet), so whatever
// still runs there outlived a crashed hub (T12.2). Identity is the cwd itself, not a saved pid, so a
// reused pid can never be hit. Processes of other users are unreadable and skipped.
func Sweep(dir string) int {
	dir = filepath.Clean(dir)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self, n := os.Getpid(), 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
		if err != nil {
			continue
		}
		cwd = strings.TrimSuffix(cwd, " (deleted)") // the crashed run's sandbox may be gone already
		if cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator)) {
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				n++
			}
		}
	}
	return n
}
