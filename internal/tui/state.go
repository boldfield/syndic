package tui

import (
	"encoding/json"

	"github.com/boldfield/syndic/internal/wire"
)

// ConnectionState is where the socket connection stands.
type ConnectionState string

const (
	ConnStateConnecting   ConnectionState = "connecting"
	ConnStateConnected    ConnectionState = "connected"
	ConnStateReconnecting ConnectionState = "reconnecting"
)

// State holds what the UI knows about the engine.
type State struct {
	Hello       wire.HelloReply
	Status      wire.StatusReply
	CurrentTurn string
	LastSeq     int64
	ConnState   ConnectionState
	TurnState   map[string]string
}

// apply folds one event frame into the state and returns the result. Later
// tasks extend it.
func apply(s State, f wire.Frame) State {
	if f.Seq > s.LastSeq {
		s.LastSeq = f.Seq
	}
	switch f.Kind {
	case wire.EventKindTurnSubmitted:
		var body wire.EvTurnSubmitted
		if json.Unmarshal(f.Body, &body) == nil {
			s = setTurnState(s, body.TurnID, "submitted")
			s.CurrentTurn = body.TurnID
		}
	case wire.EventKindTurnState:
		var body wire.EvTurnState
		if json.Unmarshal(f.Body, &body) == nil {
			s = setTurnState(s, body.TurnID, body.State)
		}
	}
	return s
}

func setTurnState(s State, turnID, state string) State {
	ts := make(map[string]string, len(s.TurnState)+1)
	for k, v := range s.TurnState {
		ts[k] = v
	}
	ts[turnID] = state
	s.TurnState = ts
	if isTerminalState(state) && s.CurrentTurn == turnID {
		s.CurrentTurn = ""
	}
	return s
}

func isTerminalState(state string) bool {
	switch state {
	case "approved", "solo_complete", "incomplete", "cancelled", "overridden":
		return true
	}
	return false
}
