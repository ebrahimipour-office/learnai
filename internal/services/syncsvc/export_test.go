package syncsvc

// SetBeforeCommitTx replaces the hook between observation and DB transaction until the test ends.
func SetBeforeCommitTx(t interface{ Cleanup(func()) }, f func()) {
	old := beforeCommitTx
	beforeCommitTx = f
	t.Cleanup(func() { beforeCommitTx = old })
}
