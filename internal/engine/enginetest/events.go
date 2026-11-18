package enginetest

import (
	"encoding/json"
	"time"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// Event is a journal event with its payload decoded into the wire.Ev struct
// for its kind (a pointer to it). A kind the harness does not know keeps its
// raw JSON in Payload.
type Event struct {
	Seq     int64
	TurnID  string
	Kind    string
	At      time.Time
	Payload any
}

var payloads = map[string]func() any{
	wire.EventKindEngineStarted:          func() any { return new(wire.EvEngineStarted) },
	wire.EventKindEngineStopping:         func() any { return new(wire.EvEngineStopping) },
	wire.EventKindTurnSubmitted:          func() any { return new(wire.EvTurnSubmitted) },
	wire.EventKindTurnState:              func() any { return new(wire.EvTurnState) },
	wire.EventKindParticipantState:       func() any { return new(wire.EvParticipantState) },
	wire.EventKindAttemptBegun:           func() any { return new(wire.EvAttemptBegun) },
	wire.EventKindAttemptCompleted:       func() any { return new(wire.EvAttemptCompleted) },
	wire.EventKindAttemptUnknown:         func() any { return new(wire.EvAttemptUnknown) },
	wire.EventKindMessageAppended:        func() any { return new(wire.EvMessageAppended) },
	wire.EventKindCandidateFrozen:        func() any { return new(wire.EvCandidateFrozen) },
	wire.EventKindVerdictRecorded:        func() any { return new(wire.EvVerdictRecorded) },
	wire.EventKindVerdictsDelivered:      func() any { return new(wire.EvVerdictsDelivered) },
	wire.EventKindBlockOpened:            func() any { return new(wire.EvBlockOpened) },
	wire.EventKindBlockState:             func() any { return new(wire.EvBlockState) },
	wire.EventKindCapacity:               func() any { return new(wire.EvCapacity) },
	wire.EventKindDeadline:               func() any { return new(wire.EvDeadline) },
	wire.EventKindBudget:                 func() any { return new(wire.EvBudget) },
	wire.EventKindBudgetEstimateExceeded: func() any { return new(wire.EvBudgetEstimateExceeded) },
	wire.EventKindBudgetExhausted:        func() any { return new(wire.EvBudgetExhausted) },
	wire.EventKindOverride:               func() any { return new(wire.EvOverride) },
}

func decode(ev journal.Event) Event {
	out := Event{Seq: ev.Seq, TurnID: ev.TurnID, Kind: ev.Kind, At: ev.At}
	if mk, ok := payloads[ev.Kind]; ok {
		p := mk()
		if err := json.Unmarshal(ev.Data, p); err == nil {
			out.Payload = p
			return out
		}
	}
	out.Payload = json.RawMessage(ev.Data)
	return out
}
