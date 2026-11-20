package tui

// renderInput returns the input line region.
func renderInput(inputLine, refusal string, width, height int) string {
	if refusal != "" {
		return refusal + "\n> " + inputLine
	}
	return "> " + inputLine
}
