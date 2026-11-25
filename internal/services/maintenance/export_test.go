package maintenance

// SetQuickCheckForTest swaps the backup integrity check; the returned func restores it.
func SetQuickCheckForTest(f func(string) error) func() {
	old := quickCheck
	quickCheck = f
	return func() { quickCheck = old }
}
