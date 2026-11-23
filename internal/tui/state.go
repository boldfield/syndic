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
	DeclaredBilling          string
	SpendingControl          string
	FailingStep              string
	ServerStatus             string
	RetryCommand             string
	ProbeAtStart             bool
	AcceptUnknownTruncation  bool
	InDefaultPreset          bool
}

// ProbeSubjectInfo holds information about a probe target that isn't a participant.
type ProbeSubjectInfo struct {
	Kind                string
	Identity            string
	Endpoint            string
	Model               string
	State               string
	Detail              string
	ProbeObservedAt     *time.Time
	ProbeAtStart        bool
	FailingStep         string
	ServerStatus        string
	RetryCommand        string
	ReportedModel       string
	ContextBound        *int64
	OutputBound         *int64
	BytesPerToken       *float64
	BytesPerTokenSource string
}

// TurnInfo holds information about the current turn.
type TurnInfo struct {
	ID              string
	State           string
	Preset          string
	QuickProfile    *string
	ModeOverride    bool // true if /mode set the preset for this turn
	SubmittedAt     time.Time
	SubmitSeq       int64         // journal seq of turn_submitted; orders turns submitted within one second
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
	Round           int    // revisions started so far: the larger of eventRounds and recordRound
	MaxRounds       int    // the round limit the turn runs with; 0 when not known or the turn has no loop
	InputRef        string // content ref of what the user typed, once the record says
	ContextFrom     string // first earlier turn the turn's history included, once the record says
	ReviewCount     *int   // number of accepted verdicts for this turn, from conversation info

	// The two round sources each count from the turn's start, so Round takes
	// the larger rather than adding them: a record fetched between replayed
	// events must not count a revision twice.
	eventRounds     int   // revision_dispatched events applied
	lastRevisionSeq int64 // seq of the last revision_dispatched applied; a resent frame is not counted again
	recordRound     int   // the latest turn record's round count
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
	ProbeSubjects       map[string]ProbeSubjectInfo
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
	NextPreset          string // set by /mode: the preset of the next submit only
	QuickInvalid        string // "quick: invalid <path>" when the project file cannot be used
	ModeSubmissionID    string // submission id of the last submit that carried a /mode preset

	// RoundLimit is the limit this process sends with a submit, when it set one.
	RoundLimit *int

	// ContextFrom is the turn /trim set as the start of the context later
	// submits see, sent as their context_from_turn; earlier turns of the chat
	// are not in context. Empty when this process set none, in which case
	// the newest turn record's trim point holds.
	ContextFrom string

	// Scope limits which turns the chat shows to the open conversation's. It
	// is nil when the app was not started on a conversation.
	Scope *TurnScope
	// TurnConversation is the conversation each turn belongs to, as far as
	// turn_submitted events and submit replies say. It names the conversation
	// of a running turn the chat no longer shows.
	TurnConversation map[string]string
}

// withTurnConversation records that the turn belongs to the conversation,
// without mutating m.
func withTurnConversation(m map[string]string, turnID, conversationID string) map[string]string {
	if turnID == "" || conversationID == "" || m[turnID] == conversationID {
		return m
	}
	out := copyMap(m)
	out[turnID] = conversationID
	return out
}

// TurnScope is the set of turns that belong to the open conversation.
type TurnScope struct {
	ConversationID string          // empty until the first submit creates the conversation
	Turns          map[string]bool // turn ids in the conversation
	Submissions    map[string]bool // submission ids this process sent
	Order          []string        // turn ids, oldest first, as far as they are known
}

// add puts a turn into the scope, once.
func (sc *TurnScope) add(turnID string) {
	if sc.Turns[turnID] {
		return
	}
	sc.Turns[turnID] = true
	sc.Order = append(sc.Order, turnID)
}

// maxRounds is the round limit a new turn runs with: the one this process
// sends, else the roster's default.
func (s State) maxRounds() int {
	switch {
	case s.RoundLimit != nil:
		return *s.RoundLimit
	case s.Status != nil:
		return s.Status.Revision.Default
	}
	return 0
}

// inScope reports whether the chat may show the turn.
func (s State) inScope(turnID string) bool {
	return s.Scope == nil || s.Scope.Turns[turnID]
}

// NewState returns a fresh State.
func NewState() State {
	return State{
		TurnState:     make(map[string]string),
		Participants:  make(map[string]ParticipantInfo),
		ProbeSubjects: make(map[string]ProbeSubjectInfo),
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
			s.TurnConversation = withTurnConversation(s.TurnConversation, body.TurnID, body.ConversationID)
			if sc := s.Scope; sc != nil && ((sc.ConversationID != "" && body.ConversationID == sc.ConversationID) || sc.Submissions[body.SubmissionID]) {
				sc.add(body.TurnID)
			}
			if s.inScope(body.TurnID) {
				s.CurrentTurn = body.TurnID
			}
			turns := copyMap(s.Turns)
			turns[body.TurnID] = TurnInfo{
				ID:              body.TurnID,
				State:           "submitted",
				Preset:          body.Preset,
				QuickProfile:    body.QuickProfile,
				ModeOverride:    s.ModeSubmissionID != "" && s.ModeSubmissionID == body.SubmissionID,
				SubmittedAt:     frameTime,
				SubmitSeq:       f.Seq,
				EstimatedTokens: body.EstimatedTokens,
				ReviewerStates:  make(map[string]string),
				MaxRounds:       s.maxRounds(),
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
				if body.State == "revision_dispatched" && f.Seq > ti.lastRevisionSeq {
					ti.eventRounds++
					ti.lastRevisionSeq = f.Seq
					ti.Round = max(ti.eventRounds, ti.recordRound)
				}
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
					} else if body.State == "queued" && ti.Author != body.Participant {
						ti = withQueuedParticipant(ti, body.Participant)
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

// withQueuedParticipant adds a participant the scheduler queued before its
// attempt began to the turn's roster, so the progress line marks it queued.
// The engine runs one turn at a time, so a queued dispatch is the open
// turn's: its author while the turn is authoring, else one of its reviewers.
func withQueuedParticipant(ti TurnInfo, id string) TurnInfo {
	switch ti.State {
	case "submitted", "author_dispatched":
		if ti.Author == "" {
			ti.Author = id
			ti.AuthorState = "queued"
		}
	case "candidate_frozen", "reviewers_dispatched":
		ti.Reviewers = append(append([]string(nil), ti.Reviewers...), id)
		ti.ReviewerStates = copyMap(ti.ReviewerStates)
		ti.ReviewerStates[id] = "queued"
	}
	return ti
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
		failingStep := ""
		if p.FailingStep != nil {
			failingStep = *p.FailingStep
		}
		serverStatus := ""
		if p.ServerStatus != nil {
			serverStatus = *p.ServerStatus
		}
		retryCommand := ""
		if p.RetryCommand != nil {
			retryCommand = *p.RetryCommand
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
			DeclaredBilling:          p.DeclaredBilling,
			SpendingControl:          p.SpendingControl,
			FailingStep:              failingStep,
			ServerStatus:             serverStatus,
			RetryCommand:             retryCommand,
			ProbeAtStart:             p.ProbeAtStart,
			AcceptUnknownTruncation:  p.AcceptUnknownTruncation,
			InDefaultPreset:          p.InDefaultPreset,
		}
	}
	s.Participants = participants

	// Populate probe subjects from status
	subjects := make(map[string]ProbeSubjectInfo, len(status.ProbeSubjects))
	for _, subj := range status.ProbeSubjects {
		var obsAt *time.Time
		if subj.ProbeObservedAt != nil {
			t, _ := time.Parse(time.RFC3339, *subj.ProbeObservedAt)
			obsAt = &t
		}
		detail := ""
		if subj.Detail != nil {
			detail = *subj.Detail
		}
		failingStep := ""
		if subj.FailingStep != nil {
			failingStep = *subj.FailingStep
		}
		serverStatus := ""
		if subj.ServerStatus != nil {
			serverStatus = *subj.ServerStatus
		}
		retryCommand := ""
		if subj.RetryCommand != nil {
			retryCommand = *subj.RetryCommand
		}
		key := subj.Kind + ":" + subj.Endpoint + ":" + subj.Model
		subjects[key] = ProbeSubjectInfo{
			Kind:                subj.Kind,
			Identity:            subj.Identity,
			Endpoint:            subj.Endpoint,
			Model:               subj.Model,
			State:               subj.State,
			Detail:              detail,
			ProbeObservedAt:     obsAt,
			ProbeAtStart:        subj.ProbeAtStart,
			FailingStep:         failingStep,
			ServerStatus:        serverStatus,
			RetryCommand:        retryCommand,
			ReportedModel:       subj.ReportedModel,
			ContextBound:        subj.ContextBound,
			OutputBound:         subj.OutputBound,
			BytesPerToken:       subj.BytesPerToken,
			BytesPerTokenSource: subj.BytesPerTokenSource,
		}
	}
	s.ProbeSubjects = subjects

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

	// A terminal reply is the authoritative final record. A reply fetched
	// while the turn was open is a snapshot that events may already have
	// outrun, so it only supplies what events do not carry (limit, deadline)
	// and adds records the TUI does not know yet; it never overwrites.
	// An incomplete turn is overridden by an event; a reply read before the
	// override can arrive after it and must not reopen the turn as incomplete.
	final := isTerminalState(turn.State) && (ti.State != "overridden" || turn.State != "incomplete")
	if final {
		ti.State = turn.State
		ti.Preset = turn.Preset
		ti.QuickProfile = turn.QuickProfile
		ti.TerminalReason = turn.TerminalReason
		ti.CandidateHash = turn.CandidateHash
	} else {
		if ti.State == "" {
			ti.State = turn.State
		}
		if ti.Preset == "" {
			ti.Preset = turn.Preset
		}
		if ti.QuickProfile == nil {
			ti.QuickProfile = turn.QuickProfile
		}
		if ti.CandidateHash == nil {
			ti.CandidateHash = turn.CandidateHash
		}
	}
	// The reply's figure sums the attempts it holds, while turn_submitted
	// carries the whole-turn projection. A terminal reply is authoritative; an
	// open one only fills a missing estimate.
	// The turn record is where a reopened or reattached turn gets its round
	// count, limit and input; events only cover what this process watched.
	ti.recordRound = max(ti.recordRound, turn.Round)
	ti.Round = max(ti.eventRounds, ti.recordRound)
	switch {
	case turn.Mode == "solo":
		ti.MaxRounds = 0
	case turn.MaxRounds != nil:
		ti.MaxRounds = *turn.MaxRounds
	}
	if turn.InputRef != nil {
		ti.InputRef = *turn.InputRef
	}
	if turn.ContextFromTurn != nil {
		ti.ContextFrom = *turn.ContextFromTurn
	}
	if turn.EstimatedPromptTokens > 0 && (final || ti.EstimatedTokens == 0) {
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
		if _, known := attempts[ar.ID]; known && !final {
			continue
		}
		attempts[ar.ID] = ai
	}
	s.Attempts = attempts

	verdicts := copyMap(s.Verdicts)
	for _, v := range turn.Verdicts {
		if _, known := verdicts[v.ID]; known && !final {
			continue
		}
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
		if _, known := blocks[ob.ID]; known && !final {
			continue
		}
		blocks[ob.ID] = BlockInfo{
			ID:       ob.ID,
			Severity: ob.Severity,
			State:    ob.State,
		}
	}
	s.Blocks = blocks

	return s
}

// Remaining is the time left to the turn deadline at now, or zero when no
// deadline is known or it has passed.
func (ti TurnInfo) Remaining(now time.Time) time.Duration {
	if ti.TurnDeadline == nil {
		return 0
	}
	if d := ti.TurnDeadline.Sub(now); d > 0 {
		return d
	}
	return 0
}

func getDetail(detail *string) string {
	if detail == nil {
		return ""
	}
	return *detail
}
