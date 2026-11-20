package tui

// renderStrip returns the status strip line.
func renderStrip(s State, width, height int) string {
	switch s.ConnState {
	case ConnStateReconnecting:
		return "engine: reconnecting"
	case ConnStateConnecting:
		return "engine: connecting"
	}
	return "strip"
}
