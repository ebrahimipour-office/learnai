//go:build race

package httpserver_test

// raceEnabled skips latency budgets: the race detector slows code 5-10x.
const raceEnabled = true
