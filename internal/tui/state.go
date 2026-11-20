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
	// Events are processed in seq order and state records the last seq.
	if event.Seq > 0 {
		s.LastSeq = event.Seq
	}
	return s
}
