package tui

import (
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// yesNoPrompt is the one yes/no confirmation the TUI can have open. The quick
// profile approval and the provisional deadline question both go through it,
// so only one of them can own the keyboard at a time.
type yesNoPrompt struct {
	yes, no func() tea.Cmd
}

// provisionalPrompt is a provisional deadline confirmation waiting for yes or no.
type provisionalPrompt struct{}

// closeProvisional drops the provisional prompt and gives the input line back.
func (a *App) closeProvisional() {
	a.provisional, a.yesNo = nil, nil
	a.panel = nil
	a.input.Prompt = defaultPrompt
}

// provisionalYes resends the submit with allow_provisional: true and a new submission_id.
func (a *App) provisionalYes() tea.Cmd {
	a.closeProvisional()
	a.allowProvisional = true
	a.refusal = ""
	cmd := a.submitInput(a.lastSubmitInput)
	a.updateQuickProfileState() // an approval that waited behind this prompt may open now
	return cmd
}

// provisionalNo cancels the submit without sending anything; keeps the input.
func (a *App) provisionalNo() tea.Cmd {
	a.closeProvisional()
	a.refusal = "submit cancelled; input kept"
	a.updateQuickProfileState()
	return nil
}

// showProvisionalPrompt shows the confirmation dialog for provisional deadlines.
func (a *App) showProvisionalPrompt(status *wire.StatusReply) {
	if a.yesNo != nil || a.confirm != nil {
		// Another prompt owns the keyboard and the panel; the input is kept.
		a.refusal = "provisional deadlines are in effect; answer the open prompt, then submit again"
		return
	}
	var rosterPath string
	if status != nil {
		rosterPath = status.RosterPath
	}
	lines := []string{
		"Provisional deadlines are in effect for this engine.",
	}
	if rosterPath != "" {
		lines = append(lines, "Configured at: "+rosterPath)
	}
	lines = append(lines, "", "Type y to proceed with this submit, or n/esc to cancel.")
	a.provisional = &provisionalPrompt{}
	a.yesNo = &yesNoPrompt{yes: a.provisionalYes, no: a.provisionalNo}
	a.panel = lines
	a.input.Prompt = provisionalDeadlinePrompt
	a.refusal = ""
}
