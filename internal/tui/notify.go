package tui

import (
	"encoding/json"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

type bellMsg struct{}

// shouldNotify classifies a frame and advances the retained seq, the highest
// seq this process has processed, for every frame whatever the notify setting.
// A terminal turn_state frame above the retained seq is either live, at or
// above live_from_seq, which rings when /notify is on, or replay, which
// produces a reconnect notice. The first connection's replay lies below
// live_from_seq and is covered by the retained seq set in onConnected, so a
// new process never claims turns that ended while it was detached. The
// notice does not depend on /notify.
func (a *App) shouldNotify(frame wire.Frame) (bell, notice bool, turnID, outcome, at string) {
	fresh := frame.Seq > a.lastProcessedSeq
	a.lastProcessedSeq = max(a.lastProcessedSeq, frame.Seq)
	if !fresh || frame.Kind != wire.EventKindTurnState {
		return
	}
	var body wire.EvTurnState
	if json.Unmarshal(frame.Body, &body) != nil || body.TurnID == "" || !isTerminalState(body.State) {
		return
	}
	turnID, outcome, at = body.TurnID, body.State, frame.At
	if frame.Seq >= a.liveFromSeq {
		bell = a.notify
	} else {
		notice = true
	}
	return
}

// bellCmd returns a command that emits a BEL to the terminal.
func (a *App) bellCmd() tea.Cmd {
	if a.emitBell == nil {
		return nil
	}
	return func() tea.Msg { a.emitBell(); return bellMsg{} }
}

// recordMissedTerminalEvent stores a terminal event for display on reconnect.
// It's called when a replay terminal event is processed during reconnection.
func (a *App) recordMissedTerminalEvent(turnID, outcome, at string) {
	a.missedTerminalEvents[turnID] = outcome
	a.missedTerminalEventsTm[turnID] = at
}
