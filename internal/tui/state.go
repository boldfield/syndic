package tui

import (
	"encoding/json"

	"github.com/boldfield/syndic/internal/wire"
)

// State holds what the UI knows about the engine and current turn.
type State struct {
	Hello       wire.HelloReply
	Status      wire.StatusReply
	CurrentTurn string
	LastSeq     int64
	ConnState   ConnectionState
	TurnState   map[string]string
}

type ConnectionState string

const (
	ConnStateConnecting   ConnectionState = "connecting"
	ConnStateReconnecting ConnectionState = "reconnecting"
	ConnStateConnected    ConnectionState = "connected"
	ConnStateClosed       ConnectionState = "closed"
)

// Apply processes an event and returns an updated state.
func Apply(s State, event wire.Frame) State {
	if event.Seq > 0 {
		s.LastSeq = event.Seq
	}

	if s.TurnState == nil {
		s.TurnState = make(map[string]string)
	}

	switch event.Kind {
	case "turn_submitted":
		// Turn submitted - CurrentTurn is set by handleSubmitResult
	case "turn_state":
		var body wire.EvTurnState
		if err := json.Unmarshal(event.Body, &body); err == nil {
			s.TurnState[body.TurnID] = body.State
			if isTerminalState(body.State) && s.CurrentTurn == body.TurnID {
				s.CurrentTurn = ""
			}
		}
	case "engine_stopping":
		s.ConnState = ConnStateReconnecting
	}

	return s
}

func isTerminalState(state string) bool {
	return state == "approved" || state == "solo_complete" || state == "incomplete" || state == "cancelled" || state == "overridden"
}
