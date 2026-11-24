package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// confirmPrompt is an interface for confirmation prompts that require a reason.
type confirmPrompt interface {
	isPendingConfirmation()
	// cancelText returns the message to show when the user cancels (Esc).
	cancelText() string
}

// overridePrompt is an /override waiting for its reason.
type overridePrompt struct {
	turn string
}

func (p *overridePrompt) isPendingConfirmation() {}

func (p *overridePrompt) cancelText() string {
	return "override cancelled; nothing sent"
}

// releasePrompt is a /release waiting for its reason.
type releasePrompt struct {
	pool      string
	attemptID string
}

func (p *releasePrompt) isPendingConfirmation() {}

func (p *releasePrompt) cancelText() string {
	return "release cancelled; nothing sent"
}

// reasonForm wraps either a Huh form (for colour mode) or panel-based input (for dumb mode).
// It acts as a Bubble Tea model when in Huh mode.
type reasonForm struct {
	huhForm     *huh.Form // the Huh form model, or nil for fallback mode
	useHuh      bool      // true if using huh form
	reasonValue string    // the reason entered by the user
	panelLines  []string  // the panel lines (prompt/warning)
}

// Init satisfies tea.Model but doesn't do anything since the form is managed by the app.
func (rf *reasonForm) Init() tea.Cmd {
	return nil
}

// Update handles messages (Huh only; fallback mode is handled by the app's input).
func (rf *reasonForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !rf.useHuh || rf.huhForm == nil {
		return rf, nil
	}
	updatedForm, cmd := rf.huhForm.Update(msg)
	rf.huhForm = updatedForm.(*huh.Form)
	return rf, cmd
}

// View renders the form (Huh only).
func (rf *reasonForm) View() string {
	if !rf.useHuh || rf.huhForm == nil {
		return ""
	}
	return rf.huhForm.View()
}

// IsCompleted returns true if the form has completed successfully.
func (rf *reasonForm) IsCompleted() bool {
	if !rf.useHuh || rf.huhForm == nil {
		return false
	}
	return rf.huhForm.State == huh.StateCompleted
}

// Reason returns the entered reason value.
func (rf *reasonForm) Reason() string {
	return strings.TrimSpace(rf.reasonValue)
}

// openReasonForm creates a form prompting for a required reason.
// In colour mode, uses a Huh form with validation.
// In TERM=dumb or no-colour mode, uses single-line input via panel.
func openReasonForm(a *App, panelLines []string, confirm confirmPrompt) {
	a.confirm = confirm

	// Determine if we should use Huh form
	useHuh := a.terminalCapabilities.Color

	if useHuh {
		// Create reasonForm with Huh
		rf := &reasonForm{
			useHuh:     true,
			panelLines: panelLines,
		}
		// Create a Huh form with one required text field
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewText().
					Title("").
					Value(&rf.reasonValue).
					Validate(func(s string) error {
						if strings.TrimSpace(s) == "" {
							return errors.New("reason required")
						}
						return nil
					}),
			),
		)
		rf.huhForm = form
		a.reasonForm = rf
		// Set panel to show the warning/context
		a.panel = panelLines
		a.panelRow = 0
		a.refusal = ""
	} else {
		// Fallback: use simple panel + textarea input
		a.reasonForm = &reasonForm{
			useHuh:     false,
			panelLines: panelLines,
		}
		a.panel = panelLines
		a.setPrompt(reasonPrompt)
		a.refusal = ""
	}
}
