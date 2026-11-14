//go:build unix

package main

import (
	"os"
	"syscall"
)

// SIGHUP (terminal closed) shuts down like SIGTERM so running AI process trees are killed (CR-23).
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
