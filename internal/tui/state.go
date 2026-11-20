package tui

import (
	"encoding/json"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

// ConnectionState is where the socket connection stands.
type ConnectionState string

const (
	ConnStateConnecting   ConnectionState = "connecting"
	ConnStateConnected    ConnectionState = "connected"
	ConnStateReconnecting ConnectionState = "reconnecting"
)

// RunInfo holds information about the current engine run.
type RunInfo struct {
	ID             string
	RosterPath     string
	RosterFileHash string
	RosterFileSize int64
	RosterHash     string
	ManifestHash   string
	StartedAt      time.Time
	ProvisionalDL  bool
	RosterChanged  bool
	DefaultPreset  string
}

// ParticipantInfo holds participant state from status replies.
type ParticipantInfo struct {
	ID                       string
	Model                    string
	ReportedModel            string
	Identity                 string
	State                    string
	Detail                   string
	Pool                     string
	EligibleRoles            []string
	ProbeObservedAt          *time.Time
	ContextBound             *int64
	OutputBound              *int64
	BytesPerToken            *float64
	BytesPerTokenSource      string
	ContextSource            string
	EligibleRequiredReviewer string
	Estimate                 string
}

// TurnInfo holds information about the current turn.
type TurnInfo struct {
	ID              string
	State           string
	Preset          string
	QuickProfile    *string
	SubmittedAt     time.Time
	TurnDeadline    *time.Time
	Deadline        time.Time
	EstimatedTokens int64
	LimitTokens     int64
	Author          string
	AuthorState     string
	Reviewers       map[string]string
	TerminalReason  *string
	CandidateHash   *string
	PhaseAtCancel   *string
}

// AttemptInfo holds information about one attempt.
type AttemptInfo struct {
	ID                  string
	TurnID              string
	Participant         string
	Phase               string
	Epoch               int64
	Deadline            time.Time
	MaxOutputTokens     int64
	EstimatedTokens     int64
	Status              string
	TransportStatus     string
	RepairOf            *string
	EstimatedPromptText string
	ReportedPrompt      *int64
	ReportedCompletion  *int64
}

// BlockInfo holds information about a block.
type BlockInfo struct {
	ID       string
	Severity string
	State    string
}

// MessageInfo holds information about a message.
type MessageInfo struct {
	ID      string
	TurnID  string
	Phase   string
	Sender  string
	SHA256  string
	ByteLen int64
}

// BudgetNotice holds the last budget event.
type BudgetNotice struct {
	Kind        string
	TurnID      string
	Participant *string
	Admitted    *int64
	Estimated   *int64
	Reported    *int64
	Limit       int64
	Charged     int64
}

// State holds what the UI knows about the engine.
type State struct {
	Hello               wire.HelloReply
	Status              *wire.StatusReply
	CurrentTurn         string
	LastSeq             int64
	ConnState           ConnectionState
	TurnState           map[string]string
	Run                 RunInfo
	Participants        map[string]ParticipantInfo
	Pools               map[string][]wire.PoolHold
	OrphanedHolds       []wire.Hold
	QuickProfiles       map[string]wire.QuickProfile
	Turns               map[string]TurnInfo
	Attempts            map[string]AttemptInfo
	Blocks              map[string]BlockInfo
	Messages            map[string]MessageInfo
	LastBudgetNotice    *BudgetNotice
	LastBudgetExceeded  *struct{ Estimated, Reported int64 }
	LastBudgetExhausted *struct{ Charged, Limit int64 }
	SubmitTime          map[string]time.Time
	TurnDeadline        map[string]*time.Time
}

// NewState returns a fresh State.
func NewState() State {
	return State{
		TurnState:     make(map[string]string),
		Participants:  make(map[string]ParticipantInfo),
		Pools:         make(map[string][]wire.PoolHold),
		QuickProfiles: make(map[string]wire.QuickProfile),
		Turns:         make(map[string]TurnInfo),
		Attempts:      make(map[string]AttemptInfo),
		Blocks:        make(map[string]BlockInfo),
		Messages:      make(map[string]MessageInfo),
		SubmitTime:    make(map[string]time.Time),
		TurnDeadline:  make(map[string]*time.Time),
	}
}

// apply folds one event frame into the state and returns the result. It is a
// pure function that tolerates unknown event kinds and unknown members.
func apply(s State, f wire.Frame) State {
	if f.Seq > s.LastSeq {
		s.LastSeq = f.Seq
	}

	switch f.Kind {
	case wire.EventKindEngineStarted:
		var body wire.EvEngineStarted
		if json.Unmarshal(f.Body, &body) == nil {
			s.Run.ID = body.RunID
			s.Run.ManifestHash = body.ManifestHash
			s.Run.RosterHash = body.RosterHash
		}
	case wire.EventKindEngineStopping:
		// Engine stopping; no state update needed from this event alone.

	case wire.EventKindTurnSubmitted:
		var body wire.EvTurnSubmitted
		if json.Unmarshal(f.Body, &body) == nil {
			s = setTurnState(s, body.TurnID, "submitted")
			s.CurrentTurn = body.TurnID
			s.SubmitTime[body.TurnID] = time.Now()
			s.Turns[body.TurnID] = TurnInfo{
				ID:              body.TurnID,
				State:           "submitted",
				Preset:          body.Preset,
				QuickProfile:    body.QuickProfile,
				SubmittedAt:     time.Now(),
				EstimatedTokens: body.EstimatedTokens,
			}
		}

	case wire.EventKindTurnState:
		var body wire.EvTurnState
		if json.Unmarshal(f.Body, &body) == nil {
			s = setTurnState(s, body.TurnID, body.State)
			if ti, ok := s.Turns[body.TurnID]; ok {
				ti.State = body.State
				if body.Reason != nil {
					ti.TerminalReason = body.Reason
				}
				s.Turns[body.TurnID] = ti
			}
		}

	case wire.EventKindParticipantState:
		var body wire.EvParticipantState
		if json.Unmarshal(f.Body, &body) == nil {
			if pi, ok := s.Participants[body.Participant]; ok {
				pi.State = body.State
				if body.Detail != nil {
					pi.Detail = *body.Detail
				}
				s.Participants[body.Participant] = pi
			}
		}

	case wire.EventKindAttemptBegun:
		var body wire.EvAttemptBegun
		if json.Unmarshal(f.Body, &body) == nil {
			deadline, _ := time.Parse(time.RFC3339, body.Deadline)
			s.Attempts[body.AttemptID] = AttemptInfo{
				ID:              body.AttemptID,
				Participant:     body.Participant,
				Phase:           body.Phase,
				Epoch:           body.Epoch,
				Deadline:        deadline,
				MaxOutputTokens: body.MaxOutputTokens,
				EstimatedTokens: body.EstimatedPromptTokens,
				Status:          "in_progress",
				RepairOf:        body.RepairOf,
			}
		}

	case wire.EventKindAttemptCompleted:
		var body wire.EvAttemptCompleted
		if json.Unmarshal(f.Body, &body) == nil {
			if ai, ok := s.Attempts[body.AttemptID]; ok {
				ai.Status = body.Status
				ai.TransportStatus = body.TransportStatus
				if body.Usage != nil {
					ai.ReportedPrompt = body.Usage.PromptTokens
					ai.ReportedCompletion = body.Usage.CompletionTokens
				}
				s.Attempts[body.AttemptID] = ai
			}
		}

	case wire.EventKindAttemptUnknown:
		var body wire.EvAttemptUnknown
		if json.Unmarshal(f.Body, &body) == nil {
			if ai, ok := s.Attempts[body.AttemptID]; ok {
				ai.Status = "unknown"
				s.Attempts[body.AttemptID] = ai
			}
		}

	case wire.EventKindMessageAppended:
		var body wire.EvMessageAppended
		if json.Unmarshal(f.Body, &body) == nil {
			s.Messages[body.MsgID] = MessageInfo{
				ID:      body.MsgID,
				TurnID:  body.TurnID,
				Phase:   body.Phase,
				Sender:  body.Sender,
				SHA256:  body.SHA256,
				ByteLen: body.ByteLen,
			}
		}

	case wire.EventKindCandidateFrozen:
		var body wire.EvCandidateFrozen
		if json.Unmarshal(f.Body, &body) == nil {
			if ti, ok := s.Turns[body.TurnID]; ok {
				ti.CandidateHash = &body.CandidateHash
				s.Turns[body.TurnID] = ti
			}
		}

	case wire.EventKindBlockOpened:
		var body wire.EvBlockOpened
		if json.Unmarshal(f.Body, &body) == nil {
			s.Blocks[body.BlockID] = BlockInfo{
				ID:       body.BlockID,
				Severity: body.Severity,
				State:    "open",
			}
		}

	case wire.EventKindBlockState:
		var body wire.EvBlockState
		if json.Unmarshal(f.Body, &body) == nil {
			if bi, ok := s.Blocks[body.BlockID]; ok {
				bi.State = body.State
				s.Blocks[body.BlockID] = bi
			}
		}

	case wire.EventKindBudget:
		var body wire.EvBudget
		if json.Unmarshal(f.Body, &body) == nil {
			s.LastBudgetNotice = &BudgetNotice{
				Kind:        body.Budget,
				TurnID:      body.TurnID,
				Participant: body.Participant,
				Admitted:    body.Admission,
				Estimated:   body.Estimated,
				Limit:       body.Limit,
			}
		}

	case wire.EventKindBudgetEstimateExceeded:
		var body wire.EvBudgetEstimateExceeded
		if json.Unmarshal(f.Body, &body) == nil {
			s.LastBudgetExceeded = &struct{ Estimated, Reported int64 }{
				Estimated: body.Estimated,
				Reported:  body.Reported,
			}
		}

	case wire.EventKindBudgetExhausted:
		var body wire.EvBudgetExhausted
		if json.Unmarshal(f.Body, &body) == nil {
			s.LastBudgetExhausted = &struct{ Charged, Limit int64 }{
				Charged: body.Charged,
				Limit:   body.Limit,
			}
		}

	case wire.EventKindOverride:
		var body wire.EvOverride
		if json.Unmarshal(f.Body, &body) == nil {
			if ti, ok := s.Turns[body.TurnID]; ok {
				ti.State = "overridden"
				reason := body.Reason
				ti.TerminalReason = &reason
				s.Turns[body.TurnID] = ti
			}
		}

	case wire.EventKindDeadline, wire.EventKindCapacity, wire.EventKindVerdictRecorded,
		wire.EventKindVerdictsDelivered:
		// These events don't directly update state; they're informational.
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
