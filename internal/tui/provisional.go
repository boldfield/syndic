package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

const provisionalQuestionText = "Proceed with provisional deadlines for the rest of this session? (y/n)"

// onProvisional handles a submit refused because the deadlines are still
// placeholder values. Once the user has accepted them in this process the
// submission is resent at once; until then they are asked.
func (a *App) onProvisional(sub submission) tea.Cmd {
	if a.allowProvisional {
		return a.resendProvisional(sub)
	}
	a.ask(&question{
		refused: &sub,
		lines:   a.provisionalLines(),
		yes: func(a *App) tea.Cmd {
			a.allowProvisional = true
			// The answer holds for this process: submits refused while it was
			// asked are resent too rather than asked again.
			cmds := []tea.Cmd{a.resendProvisional(sub)}
			a.questions = slices.DeleteFunc(a.questions, func(q *question) bool {
				if q.refused != nil {
					cmds = append(cmds, a.resendProvisional(*q.refused))
				}
				return q.refused != nil
			})
			return tea.Batch(cmds...)
		},
		no: func(a *App) tea.Cmd {
			a.refusal = "provisional deadlines not accepted; nothing sent"
			return nil
		},
	})
	return nil
}

// resendProvisional sends the refused submission again with
// allow_provisional. The request differs from the refused one, so it gets a
// new submission id.
func (a *App) resendProvisional(sub submission) tea.Cmd {
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	sub.allowProvisional = true
	return a.send(sub)
}

// provisionalLines is the question's panel. Status names the roster but does
// not carry the deadline values, so the panel says provisional deadlines are
// in effect and where they are configured.
func (a *App) provisionalLines() []string {
	roster := "the roster"
	if a.state.Status != nil && a.state.Status.RosterPath != "" {
		roster = safeLine(a.state.Status.RosterPath)
	}
	return []string{
		"The engine refused the submit: provisional deadlines are in effect.",
		"The attempt, turn and delivery deadlines in " + roster + " are placeholder values",
		"the operator has not yet set from real turns.",
		"",
		provisionalQuestionText,
	}
}
