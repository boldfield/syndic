package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// confirmPrompt is an interface for confirmation prompts that require a reason.
type confirmPrompt interface {
	// cancelText returns the message to show when the user cancels (Esc).
	cancelText() string
}

// overridePrompt is an /override waiting for its reason.
type overridePrompt struct {
	turn string
}

func (p *overridePrompt) cancelText() string {
	return "override cancelled; nothing sent"
}

// releasePrompt is a /release waiting for its reason.
type releasePrompt struct {
	pool      string
	attemptID string
}

func (p *releasePrompt) cancelText() string {
	return "release cancelled; nothing sent"
}

// reasonForm is the colour-mode Huh form: one required single-line field.
// The plain-terminal fallback has no form; it uses the app's own input line.
type reasonForm struct {
	huhForm     *huh.Form
	useHuh      bool   // true if using huh form
	reasonValue string // the reason entered by the user
}

// newHuhForm builds the one-field form. Huh's submit command quits the
// program, so update() drops it and answers on completion itself.
func (rf *reasonForm) newHuhForm() {
	rf.huhForm = huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Prompt("reason> ").
			Value(&rf.reasonValue).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("reason required")
				}
				return nil
			}),
	)).WithShowHelp(false)
}

// reset empties the field, returning the command that starts the new form.
func (rf *reasonForm) reset() tea.Cmd {
	rf.reasonValue = ""
	rf.newHuhForm()
	return rf.huhForm.Init()
}

// Update handles messages.
func (rf *reasonForm) Update(msg tea.Msg) tea.Cmd {
	updatedForm, cmd := rf.huhForm.Update(msg)
	rf.huhForm = updatedForm.(*huh.Form)
	return cmd
}

// view renders the form into the input region: the refusal row, then rows
// lines, as renderInput does, so the panel and tab bar stay on screen.
func (rf *reasonForm) view(width, rows int) string {
	clip := lipgloss.NewStyle().MaxWidth(max(width, 1))
	var lines []string
	for _, l := range strings.Split(rf.huhForm.View(), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	rows = max(rows, 1) + 1
	lines = lines[:min(rows, len(lines))]
	for len(lines) < rows {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = clip.Render(l)
	}
	return strings.Join(lines, "\n")
}

// IsCompleted returns true if the form has completed successfully.
func (rf *reasonForm) IsCompleted() bool {
	return rf.huhForm.State == huh.StateCompleted
}

// Reason returns the entered reason value.
func (rf *reasonForm) Reason() string {
	return strings.TrimSpace(rf.reasonValue)
}

// openReasonForm prompts for a required reason. With colour it is a Huh form
// whose validation blocks an empty submit; under TERM=dumb or without colour
// it (or below 80x24) is the single-line reason prompt. The panel carries the context and any
// warning in both. The returned command starts the Huh form.
func openReasonForm(a *App, panelLines []string, confirm confirmPrompt) tea.Cmd {
	a.confirm = confirm
	a.panel = panelLines
	a.panelRow = 0
	a.refusal = ""
	if a.plainMode() {
		a.reasonForm = nil
		a.setPrompt(reasonPrompt)
		return nil
	}
	rf := &reasonForm{useHuh: true}
	a.reasonForm = rf
	return rf.reset()
}
