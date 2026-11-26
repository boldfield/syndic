package journal

// SetApplyHook installs a test hook on the apply stages and returns a restore
// function.
func SetApplyHook(h func(stage, path string) error) (restore func()) {
	prev := applyHook
	applyHook = h
	return func() { applyHook = prev }
}
