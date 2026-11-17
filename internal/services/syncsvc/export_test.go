package syncsvc

import "learninghub/internal/fsx"

// SetBeforeCommitTx replaces the hook between observation and DB transaction until the test ends.
func SetBeforeCommitTx(t interface{ Cleanup(func()) }, f func()) {
	old := beforeCommitTx
	beforeCommitTx = f
	t.Cleanup(func() { beforeCommitTx = old })
}

// CountHashes counts files hashed by observation until the test ends (T10.7).
func CountHashes(t interface{ Cleanup(func()) }) *int {
	n := new(int)
	old := hashFile
	hashFile = func(r *fsx.Root, rel string) (string, int64, error) { *n++; return old(r, rel) }
	t.Cleanup(func() { hashFile = old })
	return n
}
