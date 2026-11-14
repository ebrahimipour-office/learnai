package exercises

// SetBeforeCommitTx replaces the crash-injection hook until the test ends.
func SetBeforeCommitTx(t interface{ Cleanup(func()) }, f func()) {
	old := beforeCommitTx
	beforeCommitTx = f
	t.Cleanup(func() { beforeCommitTx = old })
}

// SetInCommitTx replaces the in-transaction hook until the test ends.
func SetInCommitTx(t interface{ Cleanup(func()) }, f func()) {
	old := inCommitTx
	inCommitTx = f
	t.Cleanup(func() { inCommitTx = old })
}
