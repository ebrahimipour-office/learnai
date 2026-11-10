package exercises

// SetBeforeCommitTx replaces the crash-injection hook until the test ends.
func SetBeforeCommitTx(t interface{ Cleanup(func()) }, f func()) {
	old := beforeCommitTx
	beforeCommitTx = f
	t.Cleanup(func() { beforeCommitTx = old })
}
