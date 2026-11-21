package wire

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	ProtocolVersion     = 1
	MaxFrameBytes       = 1 << 20 // 1 MiB
	MaxContentPageBytes = 262144  // 256 KiB
)

var ErrFrameTooLarge = errors.New("frame exceeds 1 MiB")

type GroupMember struct {
	EndpointName string `json:"endpoint_name"`
	Model        string `json:"model"`
}

type Frame struct {
	V    int             `json:"v"`
	ID   string          `json:"id,omitempty"`
	Type string          `json:"type"`
	Seq  int64           `json:"seq,omitempty"`
	Kind string          `json:"kind,omitempty"`
	At   string          `json:"at,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

type Writer struct {
	mu       sync.Mutex
	w        io.Writer
	observer func(int)
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

func (w *Writer) SetObserver(fn func(encodedLen int)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observer = fn
}

func (w *Writer) Write(f Frame) error {
	f.V = ProtocolVersion
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}

	// Add newline
	data = append(data, '\n')

	if len(data) > MaxFrameBytes {
		return ErrFrameTooLarge
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.w.Write(data); err != nil {
		return err
	}

	if w.observer != nil {
		w.observer(len(data))
	}

	return nil
}

type Reader struct {
	r        *bufio.Reader
	tooLarge bool
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, MaxFrameBytes+1)}
}

func (r *Reader) Read() (Frame, error) {
	if r.tooLarge {
		return Frame{}, ErrFrameTooLarge
	}

	line, err := r.r.ReadSlice('\n')
	if err != nil {
		if err == bufio.ErrBufferFull {
			r.tooLarge = true
			return Frame{}, ErrFrameTooLarge
		}
		return Frame{}, err
	}

	// Check encoded length including newline before modifying
	if len(line) > MaxFrameBytes {
		r.tooLarge = true
		return Frame{}, ErrFrameTooLarge
	}

	// Remove newline
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}

	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, err
	}

	if f.V != ProtocolVersion {
		return Frame{}, errors.New("unsupported protocol version")
	}

	return f, nil
}

type Error struct {
	Code         string          `json:"code"`
	Detail       *string         `json:"detail,omitempty"`
	TurnID       *string         `json:"turn_id,omitempty"`
	Participant  *string         `json:"participant,omitempty"`
	QuickProfile *string         `json:"quick_profile,omitempty"`
	Budget       *string         `json:"budget,omitempty"`
	Admission    *int64          `json:"admission,omitempty"`
	Estimated    *int64          `json:"estimated,omitempty"`
	Limit        *int64          `json:"limit,omitempty"`
	Missing      *string         `json:"missing,omitempty"`
	Holds        json.RawMessage `json:"holds,omitempty"`
	Pool         *string         `json:"pool,omitempty"`
	AttemptID    *string         `json:"attempt_id,omitempty"`
	LastSeq      *int64          `json:"last_seq,omitempty"`
}

func (e *Error) Error() string {
	return e.Code
}

type Hold struct {
	Pool      string        `json:"pool"`
	AttemptID string        `json:"attempt_id"`
	Group     []GroupMember `json:"group"`
}

type PoolHold struct {
	AttemptID string        `json:"attempt_id"`
	HeldSince string        `json:"held_since"`
	Group     []GroupMember `json:"group"`
}

func NewRequest(id, typ string, body any) (Frame, error) {
	var rawBody json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return Frame{}, err
		}
		rawBody = b
	}
	return Frame{
		ID:   id,
		Type: typ,
		Body: rawBody,
	}, nil
}

func NewReply(id string, body any) (Frame, error) {
	var rawBody json.RawMessage
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return Frame{}, err
		}
		rawBody = b
	}
	return Frame{
		ID:   id,
		Type: "reply",
		Body: rawBody,
	}, nil
}

func NewError(id string, e *Error) (Frame, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return Frame{}, err
	}
	return Frame{
		ID:   id,
		Type: "error",
		Body: body,
	}, nil
}

func NewEvent(seq int64, kind string, at time.Time, body json.RawMessage) Frame {
	return Frame{
		Type: "event",
		Seq:  seq,
		Kind: kind,
		At:   at.Format(time.RFC3339),
		Body: body,
	}
}

func EncodePage(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func DecodePage(data string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(data)
}

// Request/reply body types

type HelloRequest struct {
	Client        string `json:"client"`
	ClientVersion string `json:"client_version"`
	Protocol      int    `json:"protocol"`
}

type HelloReply struct {
	EngineVersion string `json:"engine_version"`
	Protocol      int    `json:"protocol"`
	RunID         string `json:"run_id"`
	RunFirstSeq   int64  `json:"run_first_seq"`
	RosterHash    string `json:"roster_hash"`
	ManifestHash  string `json:"manifest_hash"`
}

type StatusRequest struct{}

type Participant struct {
	ID                       string   `json:"id"`
	Model                    string   `json:"model"`
	Endpoint                 string   `json:"endpoint"`
	ReportedModel            string   `json:"reported_model"`
	Identity                 string   `json:"identity"`
	State                    string   `json:"state"`
	Detail                   *string  `json:"detail,omitempty"`
	Pool                     string   `json:"pool"`
	EligibleRoles            []string `json:"eligible_roles"`
	ProbeObservedAt          *string  `json:"probe_observed_at,omitempty"`
	ContextBound             *int64   `json:"context_bound,omitempty"`
	OutputBound              *int64   `json:"output_bound,omitempty"`
	DeclaredBilling          string   `json:"declared_billing"`
	SpendingControl          string   `json:"spending_control,omitempty"`
	BytesPerToken            *float64 `json:"bytes_per_token,omitempty"`
	BytesPerTokenSource      string   `json:"bytes_per_token_source"`
	ContextSource            string   `json:"context_source"`
	EligibleRequiredReviewer string   `json:"eligible_required_reviewer"`
	Estimate                 string   `json:"estimate"`
}

type Pool struct {
	Name        string     `json:"name"`
	Concurrency int        `json:"concurrency"`
	Holds       []PoolHold `json:"holds"`
}

type Budgets struct {
	CalibrationSafety       *float64 `json:"calibration_safety,omitempty"`
	MessageOverheadTokens   *int64   `json:"message_overhead_tokens,omitempty"`
	RequestOverheadTokens   *int64   `json:"request_overhead_tokens,omitempty"`
	ContextMargin           *float64 `json:"context_margin,omitempty"`
	AuthorMaxOutputTokens   *int64   `json:"author_max_output_tokens,omitempty"`
	ReviewerMaxOutputTokens *int64   `json:"reviewer_max_output_tokens,omitempty"`
	DeliveryMaxTokens       *int64   `json:"delivery_max_tokens,omitempty"`
	TurnMaxEstimatedTokens  *int64   `json:"turn_max_estimated_tokens,omitempty"`
}

type QuickProfile struct {
	Name             string   `json:"name"`
	Preset           string   `json:"preset"`
	Drafts           bool     `json:"drafts"`
	Budgets          *Budgets `json:"budgets"`
	DefinitionSHA256 string   `json:"definition_sha256"`
}

type StatusReply struct {
	RunID                string         `json:"run_id"`
	RunStartedAt         string         `json:"run_started_at"`
	RosterPath           string         `json:"roster_path"`
	RosterHash           string         `json:"roster_hash"`
	RosterFileChanged    bool           `json:"roster_file_changed"`
	DefaultPreset        string         `json:"default_preset"`
	Presets              []string       `json:"presets"`
	DeadlinesProvisional bool           `json:"deadlines_provisional"`
	Participants         []Participant  `json:"participants"`
	Pools                []Pool         `json:"pools"`
	OrphanedHolds        []Hold         `json:"orphaned_holds"`
	QuickProfiles        []QuickProfile `json:"quick_profiles"`
	TurnsOpen            []string       `json:"turns_open"`
	EngineStartedAt      string         `json:"engine_started_at"`
}

type SubmitRequest struct {
	SubmissionID     string  `json:"submission_id"`
	Input            string  `json:"input"`
	Preset           *string `json:"preset,omitempty"`
	QuickProfile     *string `json:"quick_profile,omitempty"`
	AllowProvisional *bool   `json:"allow_provisional,omitempty"`
}

type SubmitReply struct {
	TurnID          string  `json:"turn_id"`
	Preset          string  `json:"preset"`
	QuickProfile    *string `json:"quick_profile,omitempty"`
	EstimatedTokens int64   `json:"estimated_tokens"`
}

type CancelRequest struct {
	TurnID string `json:"turn_id"`
}

type CancelReply struct {
	Accepted bool `json:"accepted"`
}

type OverrideRequest struct {
	TurnID string `json:"turn_id"`
	Reason string `json:"reason"`
}

type OverrideReply struct {
	State string `json:"state"`
}

type ReleaseRequest struct {
	Pool      string  `json:"pool"`
	AttemptID *string `json:"attempt_id,omitempty"`
	Reason    string  `json:"reason"`
}

type ReleaseReply struct {
	Released []string `json:"released"`
}

type TurnRequest struct {
	TurnID string `json:"turn_id"`
}

type TurnDeadlines struct {
	Attempt int64 `json:"attempt"`
	Turn    int64 `json:"turn"`
}

type AttemptRecord struct {
	ID                    string         `json:"id"`
	Participant           string         `json:"participant"`
	Phase                 string         `json:"phase"`
	Epoch                 int64          `json:"epoch"`
	Deadline              string         `json:"deadline"`
	MaxOutputTokens       int64          `json:"max_output_tokens"`
	EstimatedPromptTokens int64          `json:"estimated_prompt_tokens"`
	RepairOf              *string        `json:"repair_of,omitempty"`
	Status                string         `json:"status"`
	ScrubCount            int            `json:"scrub_count"`
	TransportStatus       string         `json:"transport_status"`
	Usage                 *ReportedUsage `json:"usage,omitempty"`
	Quarantined           bool           `json:"quarantined,omitempty"`
}

type ReportedUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
}

type VerdictRecord struct {
	ID               string `json:"id"`
	AttemptID        string `json:"attempt_id"`
	Parsed           bool   `json:"parsed"`
	ValidationStatus string `json:"validation_status"`
	Reason           string `json:"reason,omitempty"`
}

type OpenBlock struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	State    string `json:"state"`
	Evidence string `json:"evidence,omitempty"`
	Remedy   string `json:"remedy,omitempty"`
}

type BudgetRecord struct {
	Projected *int64 `json:"projected,omitempty"`
	Reserved  *int64 `json:"reserved,omitempty"`
	Charged   *int64 `json:"charged,omitempty"`
	Limit     *int64 `json:"limit,omitempty"`
	Exhausted bool   `json:"exhausted"`
}

type TurnReply struct {
	State                 string          `json:"state"`
	Mode                  string          `json:"mode"`
	Preset                string          `json:"preset"`
	QuickProfile          *string         `json:"quick_profile,omitempty"`
	Deadlines             TurnDeadlines   `json:"deadlines"`
	CandidateHash         *string         `json:"candidate_hash,omitempty"`
	CandidateID           *string         `json:"candidate_id,omitempty"`
	Attempts              []AttemptRecord `json:"attempts"`
	ScrubCount            int             `json:"scrub_count"`
	EstimatedPromptTokens int64           `json:"estimated_prompt_tokens"`
	ReportedUsage         *ReportedUsage  `json:"reported_usage,omitempty"`
	Verdicts              []VerdictRecord `json:"verdicts"`
	OpenBlocks            []OpenBlock     `json:"open_blocks"`
	TerminalReason        *string         `json:"terminal_reason,omitempty"`
	Budget                BudgetRecord    `json:"budget"`
}

type TranscriptRequest struct {
	TurnID string  `json:"turn_id"`
	Cursor *string `json:"cursor,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
}

type TranscriptItem struct {
	ID      string `json:"id"`
	Ref     string `json:"ref"`
	SHA256  string `json:"sha256"`
	ByteLen int64  `json:"byte_len"`
}

type TranscriptReply struct {
	Items      []TranscriptItem `json:"items"`
	NextCursor *string          `json:"next_cursor,omitempty"`
}

type ContentRequest struct {
	Ref    string `json:"ref"`
	Offset int64  `json:"offset"`
	Limit  int64  `json:"limit"`
}

type ContentReply struct {
	Ref    string `json:"ref"`
	Offset int64  `json:"offset"`
	Data   string `json:"data"`
	Total  int64  `json:"total"`
	SHA256 string `json:"sha256"`
}

type SubscribeRequest struct {
	SinceSeq int64 `json:"since_seq"`
}

type SubscribeReply struct {
	OK          bool  `json:"ok"`
	LiveFromSeq int64 `json:"live_from_seq"`
}

type UnsubscribeRequest struct{}

type UnsubscribeReply struct {
	OK bool `json:"ok"`
}

// Event body types

type EvEngineStarted struct {
	RunID             string   `json:"run_id"`
	ManifestHash      string   `json:"manifest_hash"`
	RosterHash        string   `json:"roster_hash"`
	RecoveredAttempts int      `json:"recovered_attempts"`
	UncertainCapacity []string `json:"uncertain_capacity"`
	OrphanedHolds     []Hold   `json:"orphaned_holds"`
	Unprobed          []string `json:"unprobed"`
}

type EvEngineStopping struct {
	RunID string `json:"run_id"`
}

type EvTurnSubmitted struct {
	TurnID          string  `json:"turn_id"`
	SubmissionID    string  `json:"submission_id"`
	Preset          string  `json:"preset"`
	QuickProfile    *string `json:"quick_profile,omitempty"`
	EstimatedTokens int64   `json:"estimated_tokens"`
}

type EvTurnState struct {
	TurnID string  `json:"turn_id"`
	State  string  `json:"state"`
	Reason *string `json:"reason,omitempty"`
}

type EvParticipantState struct {
	Participant string  `json:"participant"`
	State       string  `json:"state"`
	Detail      *string `json:"detail,omitempty"`
}

type EvAttemptBegun struct {
	AttemptID             string  `json:"attempt_id"`
	Participant           string  `json:"participant"`
	Phase                 string  `json:"phase"`
	Epoch                 int64   `json:"epoch"`
	Deadline              string  `json:"deadline"`
	MaxOutputTokens       int64   `json:"max_output_tokens"`
	EstimatedPromptTokens int64   `json:"estimated_prompt_tokens"`
	RepairOf              *string `json:"repair_of,omitempty"`
}

type EvAttemptCompleted struct {
	AttemptID       string         `json:"attempt_id"`
	Status          string         `json:"status"`
	ScrubCount      int            `json:"scrub_count"`
	TransportStatus string         `json:"transport_status"`
	Usage           *ReportedUsage `json:"usage,omitempty"`
}

type EvAttemptUnknown struct {
	AttemptID string `json:"attempt_id"`
}

type EvMessageAppended struct {
	MsgID   string `json:"msg_id"`
	TurnID  string `json:"turn_id"`
	Phase   string `json:"phase"`
	Sender  string `json:"sender"`
	SHA256  string `json:"sha256"`
	ByteLen int64  `json:"byte_len"`
}

type EvCandidateFrozen struct {
	TurnID        string `json:"turn_id"`
	CandidateHash string `json:"candidate_hash"`
}

type EvVerdictRecorded struct {
	VerdictID        string `json:"verdict_id"`
	AttemptID        string `json:"attempt_id"`
	Parsed           bool   `json:"parsed"`
	ValidationStatus string `json:"validation_status"`
}

type EvVerdictsDelivered struct {
	TurnID     string   `json:"turn_id"`
	Recipients []string `json:"recipients"`
}

type EvBlockOpened struct {
	BlockID  string `json:"block_id"`
	Severity string `json:"severity"`
}

type EvBlockState struct {
	BlockID string `json:"block_id"`
	State   string `json:"state"`
}

type EvCapacity struct {
	Pool          string  `json:"pool"`
	HeldByAttempt string  `json:"held_by_attempt"`
	State         string  `json:"state"`
	How           *string `json:"how,omitempty"`
	Reason        *string `json:"reason,omitempty"`
}

type EvDeadline struct {
	TurnID      string  `json:"turn_id"`
	Kind        string  `json:"kind"`
	AttemptID   *string `json:"attempt_id,omitempty"`
	RemainingMs int64   `json:"remaining_ms"`
}

type EvBudget struct {
	TurnID      string  `json:"turn_id"`
	Budget      string  `json:"budget"`
	Participant *string `json:"participant,omitempty"`
	Admission   *int64  `json:"admission,omitempty"`
	Estimated   *int64  `json:"estimated,omitempty"`
	Limit       int64   `json:"limit"`
}

type EvBudgetEstimateExceeded struct {
	TurnID    string `json:"turn_id"`
	AttemptID string `json:"attempt_id"`
	Estimated int64  `json:"estimated"`
	Reported  int64  `json:"reported"`
}

type EvBudgetExhausted struct {
	TurnID  string `json:"turn_id"`
	Charged int64  `json:"charged"`
	Limit   int64  `json:"limit"`
}

type EvOverride struct {
	TurnID string `json:"turn_id"`
	Reason string `json:"reason"`
}

// String constants for request types
const (
	RequestHello       = "hello"
	RequestStatus      = "status"
	RequestSubmit      = "submit"
	RequestCancel      = "cancel"
	RequestOverride    = "override"
	RequestRelease     = "release"
	RequestTurn        = "turn"
	RequestTranscript  = "transcript"
	RequestContent     = "content"
	RequestSubscribe   = "subscribe"
	RequestUnsubscribe = "unsubscribe"
)

// String constants for error codes
const (
	ErrorCodeProtocol            = "protocol"
	ErrorCodeStopping            = "stopping"
	ErrorCodeBusy                = "busy"
	ErrorCodeOrphanedHolds       = "orphaned_holds"
	ErrorCodeQuickProfileUnknown = "quick_profile_unknown"
	ErrorCodePresetInvalid       = "preset_invalid"
	ErrorCodeUnprobed            = "unprobed"
	ErrorCodePolicy              = "policy"
	ErrorCodeBudget              = "budget"
	ErrorCodeProvisional         = "provisional"
	ErrorCodeNotIncomplete       = "not_incomplete"
	ErrorCodeReasonRequired      = "reason_required"
	ErrorCodeNoHold              = "no_hold"
	ErrorCodeAmbiguous           = "ambiguous"
	ErrorCodeNoSuchHold          = "no_such_hold"
	ErrorCodeFrame               = "frame"
	ErrorCodeLagging             = "lagging"
	ErrorCodeSubmissionConflict  = "submission_conflict"
	ErrorCodeInternal            = "internal"
	ErrorCodeNotOpen             = "not_open"
	ErrorCodeNotFound            = "not_found"
	ErrorCodeBadRequest          = "bad_request"
)

// String constants for event kinds
const (
	EventKindEngineStarted          = "engine_started"
	EventKindEngineStopping         = "engine_stopping"
	EventKindTurnSubmitted          = "turn_submitted"
	EventKindTurnState              = "turn_state"
	EventKindParticipantState       = "participant_state"
	EventKindAttemptBegun           = "attempt_begun"
	EventKindAttemptCompleted       = "attempt_completed"
	EventKindAttemptUnknown         = "attempt_unknown"
	EventKindMessageAppended        = "message_appended"
	EventKindCandidateFrozen        = "candidate_frozen"
	EventKindVerdictRecorded        = "verdict_recorded"
	EventKindVerdictsDelivered      = "verdicts_delivered"
	EventKindBlockOpened            = "block_opened"
	EventKindBlockState             = "block_state"
	EventKindCapacity               = "capacity"
	EventKindDeadline               = "deadline"
	EventKindBudget                 = "budget"
	EventKindBudgetEstimateExceeded = "budget_estimate_exceeded"
	EventKindBudgetExhausted        = "budget_exhausted"
	EventKindOverride               = "override"
)

var EventKinds = []string{
	EventKindEngineStarted,
	EventKindEngineStopping,
	EventKindTurnSubmitted,
	EventKindTurnState,
	EventKindParticipantState,
	EventKindAttemptBegun,
	EventKindAttemptCompleted,
	EventKindAttemptUnknown,
	EventKindMessageAppended,
	EventKindCandidateFrozen,
	EventKindVerdictRecorded,
	EventKindVerdictsDelivered,
	EventKindBlockOpened,
	EventKindBlockState,
	EventKindCapacity,
	EventKindDeadline,
	EventKindBudget,
	EventKindBudgetEstimateExceeded,
	EventKindBudgetExhausted,
	EventKindOverride,
}
