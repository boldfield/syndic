package engine

import "time"

// SetPayloadHook installs a hook that may replace any event payload, so a
// test can make a transaction fail after its row writes. Call it before the
// first request.
func (e *Engine) SetPayloadHook(f func(kind string, payload any) any) {
	e.payloadHook = f
}

// SetCrashHook installs a hook the terminal path consults after its Tx
// committed and before each Adapter.Cancel call. When it returns true the
// engine stops there: no call is made and nothing further is written. Call it
// before the first request.
func (e *Engine) SetCrashHook(f func() bool) {
	e.crashHook = f
}

// SetRetryDelay shortens or lengthens the wait before an unreachable
// attempt's retry for engines built afterwards, and returns a function that
// restores it.
func SetRetryDelay(d time.Duration) (restore func()) {
	old := retryDelay
	retryDelay = d
	return func() { retryDelay = old }
}
