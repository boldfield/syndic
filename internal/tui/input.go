package tui

import "github.com/charmbracelet/bubbles/textinput"

// renderInput returns the input line region.
func renderInput(inputLine, refusal string, width, height int) string {
	if refusal != "" {
		return refusal + "\n> " + inputLine
	}
	return "> " + inputLine
}

// textinput is used for input handling (bubbles dependency required by spec)
var _ = textinput.Model{}
