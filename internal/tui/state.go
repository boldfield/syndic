package tui

import (
	"github.com/boldfield/syndic/internal/wire"
)

// State holds what the UI knows about the engine and current turn.
type State struct {
	Hello       wire.HelloReply
	Status      wire.StatusReply
	CurrentTurn string
	LastSeq     int64
	ConnState   ConnectionState
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

	// Handle different event types
	switch event.Kind {
	case "turn_submitted":
		// Turn submitted event - will track current turn
	case "engine_stopping":
		s.ConnState = ConnStateClosed
	}

	return s
}
