package tui

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/boldfield/syndic/internal/wire"
)

// shouldNotify reports whether a frame should trigger a bell and/or notice.
// It returns: shouldBell, notice, turnID, outcome, timestamp.
// A frame triggers a bell if notify is on, it's a terminal turn state event,
// its seq is live (at or above live_from_seq), and it hasn't been processed before.
// A frame triggers a notice if it's a replay of a terminal event that occurred
// after the last seq we saw before detachment.
func (a *App) shouldNotify(frame wire.Frame) (bell bool, notice bool, turnID, outcome, at string) {
	if !a.notify {
		return
	}

	if frame.Kind != wire.EventKindTurnState {
		return
	}

	var body wire.EvTurnState
	if json.Unmarshal(frame.Body, &body) != nil || body.TurnID == "" {
		return
	}

	if !isTerminalState(body.State) {
		return
	}

	turnID = body.TurnID
	outcome = body.State
	at = frame.At

	// Determine if this is replay or live
	isLive := frame.Seq >= a.liveFromSeq

	if isLive && frame.Seq > a.lastProcessedSeq {
		// Live event not seen before -> bell
		bell = true
		a.lastProcessedSeq = frame.Seq
	} else if !isLive && frame.Seq > a.lastProcessedSeq {
		// Replay of an event we didn't see before -> notice
		notice = true
		a.lastProcessedSeq = frame.Seq
	}

	return
}

// writeBell writes the BEL character to stdout to ring the terminal bell.
func writeBell() {
	_, _ = fmt.Fprint(os.Stdout, "\x07")
}

// recordMissedTerminalEvent stores a terminal event for display on reconnect.
// It's called when a replay terminal event is processed during reconnection.
func (a *App) recordMissedTerminalEvent(turnID, outcome, at string) {
	a.missedTerminalEvents[turnID] = outcome
	a.missedTerminalEventsTm[turnID] = at
}
