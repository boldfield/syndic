package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Release prompt steps
const (
	releaseStepConfirm = iota
	releaseStepReason
)

// releasePromptState tracks the state of a /release command prompt
type releasePromptState struct {
	pool      string
	attemptID *string
	step      int
}

// commandHandler runs a command. It reports a problem by setting a.refusal.
type commandHandler func(a *App, args []string) tea.Cmd

var commands = map[string]commandHandler{
	"quit":         cmdQuit,
	"resume":       cmdResume,
	"mode":         cmdMode,
	"participants": cmdParticipants,
	"cancel":       cmdCancel,
	"why":          cmdWhy,
	"override":     cmdOverride,
	"release":      cmdRelease,
	"drafts":       cmdDrafts,
	"quick":        cmdQuick,
	"help":         cmdHelp,
}

// isCommand reports whether the input line is a command rather than a prompt.
func isCommand(line string) bool { return strings.HasPrefix(line, "/") }

// dispatch runs the command named by line, which starts with "/".
func dispatch(a *App, line string) tea.Cmd {
	fields := strings.Fields(line)
	name := "/"
	if len(fields) > 0 {
		name = fields[0]
		fields = fields[1:]
	}
	h, ok := commands[strings.TrimPrefix(name, "/")]
	if !ok {
		a.refusal = "unknown command: " + name
		return nil
	}
	return h(a, fields)
}

func cmdQuit(a *App, _ []string) tea.Cmd { return a.quit() }

// cmdResume forces the same reconnect a lost connection gets.
func cmdResume(a *App, _ []string) tea.Cmd { return a.reconnect() }

func cmdMode(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /mode <preset>"
		return nil
	}
	preset := args[0]
	if err := a.validatePreset(preset); err != nil {
		a.refusal = err.Error()
		return nil
	}
	a.state.NextPreset = preset
	return nil
}

func cmdParticipants(a *App, _ []string) tea.Cmd {
	a.tab = tabParticipants
	a.pSelected = 0
	a.pScrolled = false
	a.pScroll = 0
	return nil
}

func cmdCancel(a *App, _ []string) tea.Cmd {
	if a.state.CurrentTurn == "" {
		a.refusal = "no open turn"
		return nil
	}
	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}
	return cancelCmd(a.ctx, a.cl, a.state.CurrentTurn)
}

func cmdWhy(a *App, _ []string) tea.Cmd {
	turnID := a.state.CurrentTurn
	if turnID == "" {
		a.refusal = "no turn"
		return nil
	}

	ti, ok := a.state.Turns[turnID]
	if !ok {
		a.refusal = "turn not found"
		return nil
	}

	if ti.TerminalReason == nil {
		a.refusal = fmt.Sprintf("turn is still running (state: %s)", ti.State)
		return nil
	}

	reason := *ti.TerminalReason
	msg := fmt.Sprintf("terminal reason: %s", reason)

	if strings.HasPrefix(reason, "budget:") {
		if a.state.LastBudgetNotice != nil {
			bn := a.state.LastBudgetNotice
			if bn.Estimated != nil && bn.Admitted != nil {
				msg = fmt.Sprintf("%s (admission: %d, estimated: %d, limit: %d)", msg, *bn.Admitted, *bn.Estimated, bn.Limit)
			}
		}
	} else if strings.HasPrefix(reason, "gate:") {
		for _, aid := range attemptIDsForTurn(a.state, turnID) {
			ai := a.state.Attempts[aid]
			if ai.TransportStatus != "" && ai.TransportStatus != "ok" {
				msg = fmt.Sprintf("%s: %s", msg, ai.TransportStatus)
				break
			}
		}
	}

	a.refusal = msg
	return nil
}

func cmdOverride(a *App, args []string) tea.Cmd {
	if len(args) == 0 {
		a.refusal = "usage: /override <reason>"
		return nil
	}

	turnID := a.state.CurrentTurn
	if turnID == "" {
		a.refusal = "no open turn"
		return nil
	}

	ti, ok := a.state.Turns[turnID]
	if !ok {
		a.refusal = "turn not found"
		return nil
	}

	if ti.State != "incomplete" {
		a.refusal = fmt.Sprintf("cannot override: turn is %s, not incomplete", ti.State)
		return nil
	}

	if ti.CandidateHash == nil {
		a.refusal = "cannot override: no candidate frozen"
		return nil
	}

	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}

	reason := strings.Join(args, " ")
	return overrideCmd(a.ctx, a.cl, turnID, reason)
}

func cmdRelease(a *App, args []string) tea.Cmd {
	if len(args) == 0 || len(args) > 2 {
		a.refusal = "usage: /release <pool> [attempt_id]"
		return nil
	}

	if a.cl == nil {
		a.refusal = "engine: not connected"
		return nil
	}

	a.releasePrompt = &releasePromptState{
		pool:      args[0],
		attemptID: nil,
		step:      releaseStepConfirm,
	}
	if len(args) == 2 {
		a.releasePrompt.attemptID = &args[1]
	}
	a.releaseInput.SetValue("")
	a.releaseInput.Focus()
	return nil
}

func cmdDrafts(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /drafts on|off"
		return nil
	}
	switch args[0] {
	case "on":
		a.SetDraftsEnabled(true)
	case "off":
		a.SetDraftsEnabled(false)
	default:
		a.refusal = "usage: /drafts on|off"
		return nil
	}
	return nil
}

func cmdQuick(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /quick <name>|off"
		return nil
	}

	name := args[0]
	if name == "off" {
		a.state.SelectedQuickProfile = nil
	} else {
		if _, ok := a.state.QuickProfiles[name]; !ok {
			a.refusal = fmt.Sprintf("quick profile %q not found", name)
			return nil
		}
		a.state.SelectedQuickProfile = &name
	}
	return nil
}

func cmdHelp(a *App, _ []string) tea.Cmd {
	a.refusal = "commands: /mode <preset> /participants /cancel /why /override <reason> /release <pool> [attempt_id] /drafts on|off /quick <name>|off /help /quit /resume"
	return nil
}

func (a *App) validatePreset(preset string) error {
	if a.state.Run.DefaultPreset == "" {
		return fmt.Errorf("no default preset configured")
	}
	if a.state.Status != nil {
		presets := make(map[string]bool)
		for _, p := range a.state.Status.Participants {
			for _, role := range p.EligibleRoles {
				if role == "author" {
					presets[preset] = true
					break
				}
			}
		}
		if !presets[preset] && preset != "solo" && preset != "review" {
			return fmt.Errorf("unknown preset: %s", preset)
		}
	}
	return nil
}

func attemptIDsForTurn(state State, turnID string) []string {
	var aids []string
	for aid, ai := range state.Attempts {
		if ai.TurnID == turnID {
			aids = append(aids, aid)
		}
	}
	return aids
}
