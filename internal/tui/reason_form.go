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

// reasonForm holds the state of a reason-entry form.
// It wraps either a Huh form (for colour mode) or falls back to panel-based input (for dumb mode).
type reasonForm struct {
	huhForm     tea.Model // the Huh form model, or nil for fallback mode
	useHuh      bool      // true if huh form should be used
	reasonValue string    // the reason entered by the user (source of truth for Huh mode)
	panelLines  []string  // used for fallback display in dumb mode
}

// openReasonForm creates a form prompting for a required reason.
// In colour mode, uses a Huh form with validation.
// In TERM=dumb or no-colour mode, uses single-line input via panel.
func openReasonForm(a *App, panelLines []string, confirm confirmPrompt) {
	a.confirm = confirm

	// Determine if we should use Huh form
	useHuh := a.terminalCapabilities.Color

	if useHuh {
		// Create reasonForm first with an empty reasonValue
		rf := &reasonForm{
			useHuh:     true,
			panelLines: panelLines,
		}
		// Create a Huh form with one required text field, using the reasonForm's reasonValue field
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
	} else {
		// Fallback: use simple panel + textarea input
		a.reasonForm = &reasonForm{
			useHuh:     false,
			panelLines: panelLines,
		}
		a.panel = panelLines
		a.setPrompt(reasonPrompt)
	}

	a.refusal = ""
}
