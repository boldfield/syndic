package tui

// renderStrip returns the status strip line.
func renderStrip(state State, width int) string {
	if state.ConnState == ConnStateReconnecting {
		return "engine: reconnecting"
	}
	return "Status strip"
}
