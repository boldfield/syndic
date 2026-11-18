package engine

// SetPayloadHook installs a hook that may replace any event payload, so a
// test can make a transaction fail after its row writes. Call it before the
// first request.
func (e *Engine) SetPayloadHook(f func(kind string, payload any) any) {
	e.payloadHook = f
}
