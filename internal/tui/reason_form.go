package tui

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

// openReasonForm shows a form prompting for a required reason.
// It sets a.confirm, a.panel, and the prompt to reasonPrompt.
// When the user presses Enter, confirmEnter() is called with the reason.
// When Esc is pressed, cancelConfirm() is called.
// In TERM=dumb or no-colour mode, uses single-line input.
// In coloured mode, the input validates that the reason cannot be empty.
func openReasonForm(a *App, panelLines []string, confirm confirmPrompt) {
	a.confirm = confirm
	// Use panel fallback for TERM=dumb or no-colour mode; Huh form for coloured mode.
	// For now, panel lines are always set; they serve as the display in single-line fallback mode.
	// The form renderer will check terminal capabilities.
	a.panel = panelLines
	a.setPrompt(reasonPrompt)
	a.refusal = ""
}
