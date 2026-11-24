package tui

// confirmPrompt is an interface for confirmation prompts that require a reason.
type confirmPrompt interface {
	isPendingConfirmation()
}

// overridePrompt is an /override waiting for its reason.
type overridePrompt struct {
	turn string
}

func (p *overridePrompt) isPendingConfirmation() {}

// releasePrompt is a /release waiting for its reason.
type releasePrompt struct {
	pool      string
	attemptID string
}

func (p *releasePrompt) isPendingConfirmation() {}

// openReasonForm shows a form prompting for a required reason.
// It sets a.confirm, a.panel, and the prompt to reasonPrompt.
// When the user presses Enter, confirmEnter() is called with the reason.
// When Esc is pressed, cancelConfirm() is called.
func openReasonForm(a *App, panelLines []string, confirm confirmPrompt) {
	a.confirm = confirm
	a.panel = panelLines
	a.setPrompt(reasonPrompt)
	a.refusal = ""
}
