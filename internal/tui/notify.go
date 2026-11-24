package tui

import (
	"encoding/json"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

type bellMsg struct{}

// shouldNotify reports whether a frame should trigger a bell and/or notice.
// It returns: shouldBell, notice, turnID, outcome, timestamp.
// A frame triggers a bell if notify is on, it's a terminal turn state event,
// its seq is live (at or above live_from_seq), and it hasn't been processed before.
// A frame triggers a notice if this is a within-process reconnect and it's a
// replay of a terminal event that is above the retained seq.
// Always updates lastProcessedSeq for every processed frame.
func (a *App) shouldNotify(frame wire.Frame, isFirstConnect bool) (bell bool, notice bool, turnID, outcome, at string) {
	if frame.Kind != wire.EventKindTurnState {
		// Still track this frame even if it's not terminal
		a.lastProcessedSeq = max(a.lastProcessedSeq, frame.Seq)
		return
	}

	var body wire.EvTurnState
	if json.Unmarshal(frame.Body, &body) != nil || body.TurnID == "" {
		// Still track this frame even if it fails to parse
		a.lastProcessedSeq = max(a.lastProcessedSeq, frame.Seq)
		return
	}

	if !isTerminalState(body.State) {
		// Still track this frame even if it's not terminal state
		a.lastProcessedSeq = max(a.lastProcessedSeq, frame.Seq)
		return
	}

	turnID = body.TurnID
	outcome = body.State
	at = frame.At

	// Determine if this is replay or live
	isLive := frame.Seq >= a.liveFromSeq

	// Bell: only if notify is on and it's a live terminal event not seen before
	if a.notify && isLive && frame.Seq > a.lastProcessedSeq {
		bell = true
	}

	// Notice: only on within-process reconnect (not first connect), for replay terminal events above retained seq
	if !isFirstConnect && !isLive && frame.Seq > a.lastProcessedSeq {
		notice = true
	}

	// Always update lastProcessedSeq for this terminal event
	a.lastProcessedSeq = max(a.lastProcessedSeq, frame.Seq)

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
