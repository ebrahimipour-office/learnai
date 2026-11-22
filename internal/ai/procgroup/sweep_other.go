//go:build !linux

package procgroup

// Sweep is a no-op off linux: windows' KILL_ON_JOB_CLOSE already ends the tree when hub dies.
// shortcut: other unixes have no /proc cwd; leftovers there live until reboot or a manual kill.
func Sweep(dir string) int { return 0 }
