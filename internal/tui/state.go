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
	ID            string
	RosterPath    string
	RosterHash    string
	ManifestHash  string
	ProvisionalDL bool
	RosterChanged bool
	DefaultPreset string
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
	TurnDeadline    *time.Time    // submit time plus DeadlineDur
	DeadlineDur     time.Duration // the configured turn deadline
	EstimatedTokens int64
	LimitTokens     int64
	Author          string
	AuthorState     string
	Reviewers       []string
	ReviewerStates  map[string]string
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
	ScrubCount          int
	VerdictClass        string // validation status of the verdict recorded for it
	VerdictParsed       bool
}

// VerdictInfo is one recorded verdict: its classification and whether its
// value parsed.
type VerdictInfo struct {
	ID        string
	AttemptID string
	Parsed    bool
	Class     string
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
	Pools               map[string]wire.Pool
	OrphanedHolds       []wire.Hold
	QuickProfiles       map[string]wire.QuickProfile
	Turns               map[string]TurnInfo
	Attempts            map[string]AttemptInfo
	Blocks              map[string]BlockInfo
	Verdicts            map[string]VerdictInfo
	Messages            map[string]MessageInfo
	LastBudgetNotice    *BudgetNotice
	LastBudgetExceeded  *struct{ Estimated, Reported int64 }
	LastBudgetExhausted *struct{ Charged, Limit int64 }
	UnapprovedProfile   *string
}

// NewState returns a fresh State.
func NewState() State {
	return State{
		TurnState:     make(map[string]string),
		Participants:  make(map[string]ParticipantInfo),
		Pools:         make(map[string]wire.Pool),
		QuickProfiles: make(map[string]wire.QuickProfile),
		Turns:         make(map[string]TurnInfo),
		Attempts:      make(map[string]AttemptInfo),
		Blocks:        make(map[string]BlockInfo),
		Verdicts:      make(map[string]VerdictInfo),
		Messages:      make(map[string]MessageInfo),
	}
}

// apply folds one event frame into the state and returns the result. It is a
// pure function that tolerates unknown event kinds and unknown members. It does
// not call time.Now() and does not mutate shared input maps.
func apply(s State, f wire.Frame) State {
	if f.Seq > s.LastSeq {
		s.LastSeq = f.Seq
	}

	frameTime, _ := time.Parse(time.RFC3339, f.At)

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
			turns := copyMap(s.Turns)
			turns[body.TurnID] = TurnInfo{
				ID:              body.TurnID,
				State:           "submitted",
				Preset:          body.Preset,
				QuickProfile:    body.QuickProfile,
				SubmittedAt:     frameTime,
				EstimatedTokens: body.EstimatedTokens,
				ReviewerStates:  make(map[string]string),
			}
			s.Turns = turns
		}

	case wire.EventKindTurnState:
		var body wire.EvTurnState
		if json.Unmarshal(f.Body, &body) == nil {
			s = setTurnState(s, body.TurnID, body.State)
			turns := copyMap(s.Turns)
			if ti, ok := turns[body.TurnID]; ok {
				ti.State = body.State
				if body.Reason != nil {
					ti.TerminalReason = body.Reason
				}
				turns[body.TurnID] = ti
			}
			s.Turns = turns
		}

	case wire.EventKindParticipantState:
		var body wire.EvParticipantState
		if json.Unmarshal(f.Body, &body) == nil {
			participants := copyMap(s.Participants)
			if pi, ok := participants[body.Participant]; ok {
				pi.State = body.State
				if body.Detail != nil {
					pi.Detail = *body.Detail
				}
				participants[body.Participant] = pi
			}
			s.Participants = participants

			// Update author/reviewer states in the current turn if this participant is involved
			if s.CurrentTurn != "" {
				turns := copyMap(s.Turns)
				if ti, ok := turns[s.CurrentTurn]; ok {
					if ti.Author == body.Participant {
						ti.AuthorState = body.State
					}
					if _, ok := ti.ReviewerStates[body.Participant]; ok {
						ti.ReviewerStates = copyMap(ti.ReviewerStates)
						ti.ReviewerStates[body.Participant] = body.State
					}
					turns[s.CurrentTurn] = ti
				}
				s.Turns = turns
			}
		}

	case wire.EventKindAttemptBegun:
		var body wire.EvAttemptBegun
		if json.Unmarshal(f.Body, &body) == nil {
			deadline, _ := time.Parse(time.RFC3339, body.Deadline)
			attempts := copyMap(s.Attempts)
			attempts[body.AttemptID] = AttemptInfo{
				ID:              body.AttemptID,
				TurnID:          s.CurrentTurn,
				Participant:     body.Participant,
				Phase:           body.Phase,
				Epoch:           body.Epoch,
				Deadline:        deadline,
				MaxOutputTokens: body.MaxOutputTokens,
				EstimatedTokens: body.EstimatedPromptTokens,
				Status:          "in_progress",
				RepairOf:        body.RepairOf,
			}
			s.Attempts = attempts

			// Update turn author/reviewers based on attempt phase
			if s.CurrentTurn != "" {
				turns := copyMap(s.Turns)
				if ti, ok := turns[s.CurrentTurn]; ok {
					if body.Phase == "author" && ti.Author == "" {
						ti.Author = body.Participant
						if pi, ok := s.Participants[body.Participant]; ok {
							ti.AuthorState = pi.State
						}
					} else if body.Phase == "review" {
						if _, seen := ti.ReviewerStates[body.Participant]; !seen {
							ti.Reviewers = append(append([]string(nil), ti.Reviewers...), body.Participant)
							reviewerStates := copyMap(ti.ReviewerStates)
							if pi, ok := s.Participants[body.Participant]; ok {
								reviewerStates[body.Participant] = pi.State
							} else {
								reviewerStates[body.Participant] = ""
							}
							ti.ReviewerStates = reviewerStates
						}
					}
					turns[s.CurrentTurn] = ti
				}
				s.Turns = turns
			}
		}

	case wire.EventKindAttemptCompleted:
		var body wire.EvAttemptCompleted
		if json.Unmarshal(f.Body, &body) == nil {
			attempts := copyMap(s.Attempts)
			if ai, ok := attempts[body.AttemptID]; ok {
				ai.Status = body.Status
				ai.TransportStatus = body.TransportStatus
				ai.ScrubCount = body.ScrubCount
				if body.Usage != nil {
					ai.ReportedPrompt = body.Usage.PromptTokens
					ai.ReportedCompletion = body.Usage.CompletionTokens
				}
				attempts[body.AttemptID] = ai
			}
			s.Attempts = attempts
		}

	case wire.EventKindAttemptUnknown:
		var body wire.EvAttemptUnknown
		if json.Unmarshal(f.Body, &body) == nil {
			attempts := copyMap(s.Attempts)
			if ai, ok := attempts[body.AttemptID]; ok {
				ai.Status = "unknown"
				attempts[body.AttemptID] = ai
			}
			s.Attempts = attempts
		}

	case wire.EventKindMessageAppended:
		var body wire.EvMessageAppended
		if json.Unmarshal(f.Body, &body) == nil {
			messages := copyMap(s.Messages)
			messages[body.MsgID] = MessageInfo{
				ID:      body.MsgID,
				TurnID:  body.TurnID,
				Phase:   body.Phase,
				Sender:  body.Sender,
				SHA256:  body.SHA256,
				ByteLen: body.ByteLen,
			}
			s.Messages = messages
		}

	case wire.EventKindCandidateFrozen:
		var body wire.EvCandidateFrozen
		if json.Unmarshal(f.Body, &body) == nil {
			turns := copyMap(s.Turns)
			if ti, ok := turns[body.TurnID]; ok {
				ti.CandidateHash = &body.CandidateHash
				turns[body.TurnID] = ti
			}
			s.Turns = turns
		}

	case wire.EventKindBlockOpened:
		var body wire.EvBlockOpened
		if json.Unmarshal(f.Body, &body) == nil {
			blocks := copyMap(s.Blocks)
			blocks[body.BlockID] = BlockInfo{
				ID:       body.BlockID,
				Severity: body.Severity,
				State:    "open",
			}
			s.Blocks = blocks
		}

	case wire.EventKindBlockState:
		var body wire.EvBlockState
		if json.Unmarshal(f.Body, &body) == nil {
			blocks := copyMap(s.Blocks)
			if bi, ok := blocks[body.BlockID]; ok {
				bi.State = body.State
				blocks[body.BlockID] = bi
			}
			s.Blocks = blocks
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
			if ti, ok := s.Turns[body.TurnID]; ok && body.Limit > 0 {
				turns := copyMap(s.Turns)
				ti.LimitTokens = body.Limit
				turns[body.TurnID] = ti
				s.Turns = turns
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
			turns := copyMap(s.Turns)
			if ti, ok := turns[body.TurnID]; ok {
				ti.State = "overridden"
				reason := body.Reason
				ti.TerminalReason = &reason
				turns[body.TurnID] = ti
			}
			s.Turns = turns
		}

	case wire.EventKindDeadline:
		var body wire.EvDeadline
		if json.Unmarshal(f.Body, &body) == nil && body.Kind == "turn" && body.TurnID != "" {
			// Update turn deadline from the remaining milliseconds
			if s.CurrentTurn == body.TurnID {
				turns := copyMap(s.Turns)
				if ti, ok := turns[body.TurnID]; ok {
					deadline := frameTime.Add(time.Duration(body.RemainingMs) * time.Millisecond)
					ti.TurnDeadline = &deadline
					if !ti.SubmittedAt.IsZero() {
						ti.DeadlineDur = deadline.Sub(ti.SubmittedAt)
					}
					turns[body.TurnID] = ti
					s.Turns = turns
				}
			}
		}

	case wire.EventKindVerdictRecorded:
		var body wire.EvVerdictRecorded
		if json.Unmarshal(f.Body, &body) == nil {
			verdicts := copyMap(s.Verdicts)
			verdicts[body.VerdictID] = VerdictInfo{
				ID:        body.VerdictID,
				AttemptID: body.AttemptID,
				Parsed:    body.Parsed,
				Class:     body.ValidationStatus,
			}
			s.Verdicts = verdicts
			attempts := copyMap(s.Attempts)
			if ai, ok := attempts[body.AttemptID]; ok {
				ai.VerdictClass = body.ValidationStatus
				ai.VerdictParsed = body.Parsed
				attempts[body.AttemptID] = ai
			}
			s.Attempts = attempts
		}

	case wire.EventKindCapacity, wire.EventKindVerdictsDelivered:
		// These events don't directly update state; they're informational.
	}

	return s
}

// copyMap creates a shallow copy of a map to avoid mutation.
func copyMap[K comparable, V any](m map[K]V) map[K]V {
	result := make(map[K]V, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

func setTurnState(s State, turnID, state string) State {
	ts := copyMap(s.TurnState)
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

// MergeStatus merges a status reply into the state and returns the result.
// This is a pure function that creates new maps.
func MergeStatus(s State, status *wire.StatusReply) State {
	s.Status = status
	s.Run.RosterHash = status.RosterHash
	s.Run.RosterPath = status.RosterPath
	s.Run.RosterChanged = status.RosterFileChanged
	s.Run.ProvisionalDL = status.DeadlinesProvisional
	s.Run.DefaultPreset = status.DefaultPreset

	// Populate participants from status
	participants := copyMap(s.Participants)
	for _, p := range status.Participants {
		var obsAt *time.Time
		if p.ProbeObservedAt != nil {
			t, _ := time.Parse(time.RFC3339, *p.ProbeObservedAt)
			obsAt = &t
		}
		participants[p.ID] = ParticipantInfo{
			ID:                       p.ID,
			Model:                    p.Model,
			ReportedModel:            p.ReportedModel,
			Identity:                 p.Identity,
			State:                    p.State,
			Detail:                   getDetail(p.Detail),
			Pool:                     p.Pool,
			EligibleRoles:            p.EligibleRoles,
			ProbeObservedAt:          obsAt,
			ContextBound:             p.ContextBound,
			OutputBound:              p.OutputBound,
			BytesPerToken:            p.BytesPerToken,
			BytesPerTokenSource:      p.BytesPerTokenSource,
			ContextSource:            p.ContextSource,
			EligibleRequiredReviewer: p.EligibleRequiredReviewer,
			Estimate:                 p.Estimate,
		}
	}
	s.Participants = participants

	// The open turn's roles show their participant's state as of this reply.
	if ti, ok := s.Turns[s.CurrentTurn]; ok {
		turns := copyMap(s.Turns)
		if p, ok := participants[ti.Author]; ok && ti.Author != "" {
			ti.AuthorState = p.State
		}
		states := copyMap(ti.ReviewerStates)
		for id := range states {
			if p, ok := participants[id]; ok {
				states[id] = p.State
			}
		}
		ti.ReviewerStates = states
		turns[s.CurrentTurn] = ti
		s.Turns = turns
	}

	// Populate pools from status
	pools := make(map[string]wire.Pool)
	for _, pool := range status.Pools {
		pools[pool.Name] = pool
	}
	s.Pools = pools

	// Populate orphaned holds
	s.OrphanedHolds = status.OrphanedHolds

	// Populate quick profiles
	quickProfiles := make(map[string]wire.QuickProfile)
	for _, qp := range status.QuickProfiles {
		quickProfiles[qp.Name] = qp
	}
	s.QuickProfiles = quickProfiles

	return s
}

// MergeTurn merges a turn reply into the state and returns the result.
// This is a pure function. It also populates author and reviewers from attempts.
// The now parameter is used to calculate absolute deadlines from relative values.
func MergeTurn(s State, turnID string, turn *wire.TurnReply, now time.Time) State {
	turns := copyMap(s.Turns)
	ti, ok := turns[turnID]
	if !ok {
		ti = TurnInfo{ID: turnID}
	}

	// A reply fetched while the turn was open may land after the terminal
	// event; never step back from a terminal state.
	if !isTerminalState(ti.State) || isTerminalState(turn.State) {
		ti.State = turn.State
	}
	ti.Preset = turn.Preset
	ti.QuickProfile = turn.QuickProfile
	ti.TerminalReason = turn.TerminalReason
	ti.CandidateHash = turn.CandidateHash
	// The reply's figure sums the attempts so far; before any exists keep the
	// estimate the submit reported.
	if turn.EstimatedPromptTokens > 0 {
		ti.EstimatedTokens = turn.EstimatedPromptTokens
	}

	if turn.Budget.Limit != nil {
		ti.LimitTokens = *turn.Budget.Limit
	}

	if turn.Deadlines.Turn > 0 {
		ti.DeadlineDur = time.Duration(turn.Deadlines.Turn) * time.Millisecond
		if !ti.SubmittedAt.IsZero() {
			deadline := ti.SubmittedAt.Add(ti.DeadlineDur)
			ti.TurnDeadline = &deadline
		}
	}

	// Author and reviewers come from the attempts; what events already
	// established stays, and the reply only adds to it.
	reviewers := append([]string(nil), ti.Reviewers...)
	states := copyMap(ti.ReviewerStates)
	for _, ar := range turn.Attempts {
		switch ar.Phase {
		case "author":
			if ti.Author == "" {
				ti.Author = ar.Participant
				ti.AuthorState = s.Participants[ar.Participant].State
			}
		case "review":
			if _, seen := states[ar.Participant]; !seen {
				reviewers = append(reviewers, ar.Participant)
				states[ar.Participant] = s.Participants[ar.Participant].State
			}
		}
	}
	ti.Reviewers = reviewers
	ti.ReviewerStates = states

	turns[turnID] = ti
	s.Turns = turns

	// Merge attempts from turn reply
	attempts := copyMap(s.Attempts)
	for _, ar := range turn.Attempts {
		deadline, _ := time.Parse(time.RFC3339, ar.Deadline)
		ai := AttemptInfo{
			ID:              ar.ID,
			TurnID:          turnID,
			Participant:     ar.Participant,
			Phase:           ar.Phase,
			Epoch:           ar.Epoch,
			Deadline:        deadline,
			MaxOutputTokens: ar.MaxOutputTokens,
			EstimatedTokens: ar.EstimatedPromptTokens,
			Status:          ar.Status,
			TransportStatus: ar.TransportStatus,
			RepairOf:        ar.RepairOf,
			ScrubCount:      ar.ScrubCount,
		}
		if ar.Usage != nil {
			ai.ReportedPrompt = ar.Usage.PromptTokens
			ai.ReportedCompletion = ar.Usage.CompletionTokens
		}
		attempts[ar.ID] = ai
	}
	s.Attempts = attempts

	verdicts := copyMap(s.Verdicts)
	for _, v := range turn.Verdicts {
		verdicts[v.ID] = VerdictInfo{ID: v.ID, AttemptID: v.AttemptID, Parsed: v.Parsed, Class: v.ValidationStatus}
		if ai, ok := attempts[v.AttemptID]; ok {
			ai.VerdictClass = v.ValidationStatus
			ai.VerdictParsed = v.Parsed
			attempts[v.AttemptID] = ai
		}
	}
	s.Verdicts = verdicts

	// Merge blocks from turn reply
	blocks := copyMap(s.Blocks)
	for _, ob := range turn.OpenBlocks {
		blocks[ob.ID] = BlockInfo{
			ID:       ob.ID,
			Severity: ob.Severity,
			State:    ob.State,
		}
	}
	s.Blocks = blocks

	return s
}

func getDetail(detail *string) string {
	if detail == nil {
		return ""
	}
	return *detail
}
