package tui

import (
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

// provisionalPrompt is a provisional deadline confirmation waiting for yes or no.
type provisionalPrompt struct {
	rosterPath string
}

// provisionalYes resends the submit with allow_provisional: true and a new submission_id.
func (a *App) provisionalYes() tea.Cmd {
	a.provisional = nil
	a.allowProvisional = true
	a.panel = nil
	a.input.Prompt = defaultPrompt
	a.refusal = ""
	id := uuid.New().String()
	return a.submitInputWithID(a.lastSubmitInput, id)
}

// provisionalNo cancels the submit without sending anything; keeps the input.
func (a *App) provisionalNo() tea.Cmd {
	a.provisional = nil
	a.panel = nil
	a.input.Prompt = defaultPrompt
	a.refusal = "submit cancelled; input kept"
	return nil
}

// showProvisionalPrompt shows the confirmation dialog for provisional deadlines.
func (a *App) showProvisionalPrompt(status *wire.StatusReply) {
	if a.provisional != nil {
		return // already showing
	}
	var rosterPath string
	if status != nil {
		rosterPath = status.RosterPath
	}
	pp := &provisionalPrompt{
		rosterPath: rosterPath,
	}
	lines := []string{
		"Provisional deadlines are in effect for this engine.",
	}
	if rosterPath != "" {
		lines = append(lines, "Configured at: "+rosterPath)
	}
	lines = append(lines, "", "Type y to proceed with this submit, or n/esc to cancel.")
	a.provisional = pp
	a.panel = lines
	a.input.Prompt = provisionalDeadlinePrompt
	a.refusal = ""
}
