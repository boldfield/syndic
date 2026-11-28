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
	// MaxConversationListSize is the maximum number of conversation summaries that may be
	// returned in a ConversationsReply. conversations.limit follows the transcript convention:
	// absent or <= 0 means 200; a positive limit above 200 is clamped to 200. The server returns
	// at most that many items (see ConversationListLimit) and stops early to ensure the encoded reply fits within MaxFrameBytes, returning
	// next_cursor for remaining items. A page must always contain at least one item when
	// available; if a single item exceeds MaxFrameBytes, it is refused with error{code:"frame"}.
	MaxConversationListSize = 200
)

// ConversationListLimit resolves the optional conversations.limit to the page size:
// absent or non-positive yields MaxConversationListSize, larger values are clamped.
func ConversationListLimit(limit *int) int {
	if limit == nil || *limit <= 0 || *limit > MaxConversationListSize {
		return MaxConversationListSize
	}
	return *limit
}

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
	Phase        *string         `json:"phase,omitempty"`
	Admission    *int64          `json:"admission,omitempty"`
	Estimated    *int64          `json:"estimated,omitempty"`
	Limit        *int64          `json:"limit,omitempty"`
	Missing      *string         `json:"missing,omitempty"`
	Holds        json.RawMessage `json:"holds,omitempty"`
	Pool         *string         `json:"pool,omitempty"`
	AttemptID    *string         `json:"attempt_id,omitempty"`
	LastSeq      *int64          `json:"last_seq,omitempty"`
	Ceiling      *int            `json:"ceiling,omitempty"`
	Route        *string         `json:"route,omitempty"`
	// Paths names the change-set paths a base_changed refusal found not at
	// their base.
	Paths []string `json:"paths,omitempty"`
	// Path and Reason say which attachment an attachment refusal is about
	// and why (v6 section 4).
	Path   *string `json:"path,omitempty"`
	Reason *string `json:"reason,omitempty"`
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
	EngineVersion string  `json:"engine_version"`
	Protocol      int     `json:"protocol"`
	RunID         string  `json:"run_id"`
	RunFirstSeq   int64   `json:"run_first_seq"`
	RosterHash    string  `json:"roster_hash"`
	ManifestHash  *string `json:"manifest_hash"`
	State         string  `json:"state"`
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
	FailingStep              *string  `json:"failing_step,omitempty"`
	ServerStatus             *string  `json:"server_status,omitempty"`
	RetryCommand             *string  `json:"retry_command,omitempty"`
	ProbeAtStart             bool     `json:"probe_at_start,omitempty"`
	AcceptUnknownTruncation  bool     `json:"accept_unknown_truncation,omitempty"`
	InDefaultPreset          bool     `json:"in_default_preset,omitempty"`
	// WebOff is why the web tools are not offered to a participant that a
	// preset gives them: its endpoint's referee is missing or has no usable
	// probe record (v5 delta section 6).
	WebOff *string `json:"web_off,omitempty"`
	// SearchOff is why web_search is not offered to a participant that a
	// preset gives web: no provider is configured under [tools.web.search],
	// the named provider is not one this build supports, or the provider host
	// fails a pre-send check or the allowlist (v5 delta section 6).
	SearchOff *string `json:"search_off,omitempty"`
}

// ProbeSubject represents a probe target that is not a participant (e.g., a referee).
// It carries probe status and failure information without implying role capabilities.
type ProbeSubject struct {
	Kind                string   `json:"kind"` // "referee"
	Identity            string   `json:"identity"`
	Endpoint            string   `json:"endpoint"`
	Model               string   `json:"model"`
	State               string   `json:"state"` // "probed", "unprobed", etc.
	Detail              *string  `json:"detail,omitempty"`
	ProbeObservedAt     *string  `json:"probe_observed_at,omitempty"`
	ProbeAtStart        bool     `json:"probe_at_start,omitempty"`
	FailingStep         *string  `json:"failing_step,omitempty"`
	ServerStatus        *string  `json:"server_status,omitempty"`
	RetryCommand        *string  `json:"retry_command,omitempty"`
	ReportedModel       string   `json:"reported_model,omitempty"`
	ContextBound        *int64   `json:"context_bound,omitempty"`
	OutputBound         *int64   `json:"output_bound,omitempty"`
	BytesPerToken       *float64 `json:"bytes_per_token,omitempty"`
	BytesPerTokenSource string   `json:"bytes_per_token_source,omitempty"`
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

type Revision struct {
	Default int `json:"default"`
	Ceiling int `json:"ceiling"`
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
	ProbeSubjects        []ProbeSubject `json:"probe_subjects,omitempty"`
	Pools                []Pool         `json:"pools"`
	OrphanedHolds        []Hold         `json:"orphaned_holds"`
	QuickProfiles        []QuickProfile `json:"quick_profiles"`
	TurnsOpen            []string       `json:"turns_open"`
	EngineStartedAt      string         `json:"engine_started_at"`
	Revision             Revision       `json:"revision"`
}

type SubmitRequest struct {
	SubmissionID     string   `json:"submission_id"`
	Input            string   `json:"input"`
	Preset           *string  `json:"preset,omitempty"`
	QuickProfile     *string  `json:"quick_profile,omitempty"`
	AllowProvisional *bool    `json:"allow_provisional,omitempty"`
	Workspace        string   `json:"workspace"`
	ConversationID   *string  `json:"conversation_id,omitempty"`
	ContextFromTurn  *string  `json:"context_from_turn,omitempty"`
	MaxRounds        *int     `json:"max_rounds,omitempty"`
	Attachments      []string `json:"attachments,omitempty"`
}

type SubmitReply struct {
	TurnID          string  `json:"turn_id"`
	Preset          string  `json:"preset"`
	QuickProfile    *string `json:"quick_profile,omitempty"`
	EstimatedTokens int64   `json:"estimated_tokens"`
	// WorstCaseEstimatedTokens is the projection across every configured
	// revision round; admission uses only EstimatedTokens.
	WorstCaseEstimatedTokens int64  `json:"worst_case_estimated_tokens"`
	ConversationID           string `json:"conversation_id"`
	ContextHash              string `json:"context_hash"`
	// Phases is the first-round projection of a deliberate turn, per phase.
	Phases []PhaseEstimate `json:"phases,omitempty"`
}

// PhaseEstimate is one projected request of a deliberate turn: the estimate of
// its rendered prompt and its output bound.
type PhaseEstimate struct {
	Phase           string `json:"phase"`
	Participant     string `json:"participant"`
	EstimatedTokens int64  `json:"estimated_tokens"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
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

// ApplyRequest asks to apply a turn's change set (v5 delta section 6).
type ApplyRequest struct {
	TurnID string `json:"turn_id"`
}

// ChangePath is one path of a change set with its own outcome: pending,
// applied or conflict.
type ChangePath struct {
	Path    string `json:"path"`
	Outcome string `json:"outcome"`
}

// ApplyReply is the change set's state and per-path outcomes after an apply.
type ApplyReply struct {
	State string       `json:"state"`
	Paths []ChangePath `json:"paths"`
}

// DiscardRequest asks to discard a turn's change set.
type DiscardRequest struct {
	TurnID string `json:"turn_id"`
}

// DiscardReply is the change set's state and per-path outcomes after a
// discard; discarding changes no path outcome.
type DiscardReply struct {
	State string       `json:"state"`
	Paths []ChangePath `json:"paths"`
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
	// ResultIDs and ObservationIDs are the journal ids of the tool results
	// and observations the verdict's reviewer received before giving it. The
	// evidence request lists those records by id with their content hashes.
	ResultIDs      []string `json:"result_ids,omitempty"`
	ObservationIDs []string `json:"observation_ids,omitempty"`
}

// ChangeSetPathRecord is one path of a frozen change set: what it changes,
// the hashes that bind it to its base, and its own outcome. NewRef and
// BaseRef are content refs that the content request pages; BaseRef is absent
// for a created file.
type ChangeSetPathRecord struct {
	Path       string  `json:"path"`
	Op         string  `json:"op"`
	BaseSHA256 string  `json:"base_sha256,omitempty"`
	NewSHA256  string  `json:"new_sha256"`
	NewLen     int64   `json:"new_len"`
	BaseRef    *string `json:"base_ref,omitempty"`
	NewRef     string  `json:"new_ref"`
	Outcome    string  `json:"outcome"`
}

// ChangeSetRecord is a turn's frozen change set with its state.
type ChangeSetRecord struct {
	ID           string                `json:"id"`
	State        string                `json:"state"`
	ChangesHash  string                `json:"changes_hash"`
	SnapshotHash string                `json:"snapshot_hash"`
	Paths        []ChangeSetPathRecord `json:"paths"`
}

type OpenBlock struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	State    string `json:"state"`
	Evidence string `json:"evidence,omitempty"`
	Remedy   string `json:"remedy,omitempty"`
}

type BudgetRecord struct {
	Projected          *int64 `json:"projected,omitempty"`
	WorstCaseProjected *int64 `json:"worst_case_projected,omitempty"`
	Reserved           *int64 `json:"reserved,omitempty"`
	Charged            *int64 `json:"charged,omitempty"`
	Limit              *int64 `json:"limit,omitempty"`
	Exhausted          bool   `json:"exhausted"`
	// Phases is the first-round projection of a deliberate turn, per phase.
	Phases []PhaseEstimate `json:"phases,omitempty"`
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
	// InputRef is the content ref of what the user typed, when it is journaled.
	InputRef *string `json:"input_ref,omitempty"`
	// Round is the revision round of the newest candidate: 0 for the first.
	Round int `json:"round,omitempty"`
	// MaxRounds is the round limit the turn was submitted with; absent for a
	// turn that has no revision loop.
	MaxRounds *int `json:"max_rounds,omitempty"`
	// ContextFromTurn is the first earlier turn the turn's history included:
	// the context_from_turn it was submitted with, or the conversation's first
	// turn when nothing was trimmed. Absent for a turn with no history.
	ContextFromTurn *string `json:"context_from_turn,omitempty"`
	// ChangeSet is the turn's frozen change set, absent when it has none.
	ChangeSet *ChangeSetRecord `json:"change_set,omitempty"`
}

// ContentRef is the content ref of stored tool evidence with the given
// SHA-256: the ref the content request pages.
func ContentRef(sha256 string) string { return "content:" + sha256 }

// EvidenceRequest asks for a page of a turn's tool, referee, evidence and
// change-set events, oldest first.
type EvidenceRequest struct {
	TurnID string  `json:"turn_id"`
	Cursor *string `json:"cursor,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
}

// EvidenceItem is one journaled event of the turn. Data is the event's
// payload as journaled: it names bytes by SHA-256 (args_sha256, sha256,
// reason_sha256, base_sha256, new_sha256) and never carries them, so an item
// stays small whatever the evidence's size; ContentRef of a hash gives the
// ref that pages the bytes.
type EvidenceItem struct {
	Seq  int64           `json:"seq"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// EvidenceReply is one page of EvidenceItems in journal order. NextCursor
// resumes after the last item; absent when the page is the last.
type EvidenceReply struct {
	Items      []EvidenceItem `json:"items"`
	NextCursor *string        `json:"next_cursor,omitempty"`
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

type ConversationsRequest struct {
	Workspace *string `json:"workspace,omitempty"`
	Cursor    *string `json:"cursor,omitempty"`
	Limit     *int    `json:"limit,omitempty"`
}

type ConversationSummary struct {
	ID              string  `json:"id"`
	CreatedAt       string  `json:"created_at"`
	Workspace       *string `json:"workspace"`
	LastSubmittedAt string  `json:"last_submitted_at"`
	TurnCount       int     `json:"turn_count"`
	Running         bool    `json:"running"`
	Preview         string  `json:"preview"`
}

type ConversationsReply struct {
	Items      []ConversationSummary `json:"items"`
	NextCursor *string               `json:"next_cursor,omitempty"`
}

type ConversationRequest struct {
	ID string `json:"id"`
}

type ConversationTurnSummary struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	ReviewCount *int   `json:"review_count,omitempty"`
}

type ConversationReply struct {
	ID string `json:"id"`
	// Turns is the complete turn list; paging for the turn list is not specified in the design.
	// The frame cap (MaxFrameBytes) bounds the reply size; a turn list that does not fit
	// is refused with error{code: "frame"} as specified in the v4 delta's Framing section.
	Turns []ConversationTurnSummary `json:"turns"`
}

// Event body types

type EvRunOpened struct {
	RunID      string `json:"run_id"`
	RosterHash string `json:"roster_hash"`
}

type EvProbeStarted struct {
	Participant string `json:"participant"`
}

type EvProbeFinished struct {
	Participant string  `json:"participant"`
	Outcome     string  `json:"outcome"`
	Detail      *string `json:"detail,omitempty"`
}

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
	ConversationID  string  `json:"conversation_id"`
}

type EvConversationCreated struct {
	ConversationID string `json:"conversation_id"`
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
	RequestHello         = "hello"
	RequestStatus        = "status"
	RequestSubmit        = "submit"
	RequestCancel        = "cancel"
	RequestOverride      = "override"
	RequestRelease       = "release"
	RequestTurn          = "turn"
	RequestTranscript    = "transcript"
	RequestContent       = "content"
	RequestSubscribe     = "subscribe"
	RequestUnsubscribe   = "unsubscribe"
	RequestConversations = "conversations"
	RequestConversation  = "conversation"
	RequestReviews       = "reviews"
	RequestApply         = "apply"
	RequestDiscard       = "discard"
	RequestEvidence      = "evidence"
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
	ErrorCodeProbing             = "probing"
	ErrorCodeWorkspaceMismatch   = "workspace_mismatch"
	ErrorCodeMaxRounds           = "max_rounds"
	ErrorCodeEgress              = "egress"
	ErrorCodeBaseChanged         = "base_changed"
	ErrorCodeWorkspaceMissing    = "workspace_missing"
	ErrorCodeNotAppliable        = "not_appliable"
	ErrorCodeAttachment          = "attachment"
)

// String constants for event kinds
const (
	EventKindRunOpened              = "run_opened"
	EventKindProbeStarted           = "probe_started"
	EventKindProbeFinished          = "probe_finished"
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
	EventKindConversationCreated    = "conversation_created"

	// Tool, referee, evidence and change-set state (v5 delta section 6). The
	// web referee's decision has a kind of its own: referee_decision is the
	// bash referee's (v7 delta).
	EventKindToolCallAccepted   = "tool_call_accepted"
	EventKindToolResult         = "tool_result"
	EventKindObservation        = "observation"
	EventKindStaged             = "staged"
	EventKindUnstaged           = "unstaged"
	EventKindWebRefereeDecision = "web_referee_decision"
	EventKindRefereeDecision    = "referee_decision"
	EventKindVerdictEvidence    = "verdict_evidence"
	EventKindChangeSetFrozen    = "change_set_frozen"
	EventKindApplyStarted       = "apply_started"
	EventKindApplyFile          = "apply_file"
	EventKindApplyFinished      = "apply_finished"
	EventKindApplyRecovered     = "apply_recovered"
	EventKindChangeSetDiscarded = "change_set_discarded"
)

// EvidenceEventKinds are the event kinds an evidence request returns.
var EvidenceEventKinds = []string{
	EventKindToolCallAccepted,
	EventKindToolResult,
	EventKindObservation,
	EventKindStaged,
	EventKindUnstaged,
	EventKindWebRefereeDecision,
	EventKindRefereeDecision,
	EventKindVerdictEvidence,
	EventKindChangeSetFrozen,
	EventKindApplyStarted,
	EventKindApplyFile,
	EventKindApplyFinished,
	EventKindApplyRecovered,
	EventKindChangeSetDiscarded,
}

var EventKinds = []string{
	EventKindRunOpened,
	EventKindProbeStarted,
	EventKindProbeFinished,
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
	EventKindConversationCreated,
	EventKindToolCallAccepted,
	EventKindToolResult,
	EventKindObservation,
	EventKindStaged,
	EventKindUnstaged,
	EventKindWebRefereeDecision,
	EventKindRefereeDecision,
	EventKindVerdictEvidence,
	EventKindChangeSetFrozen,
	EventKindApplyStarted,
	EventKindApplyFile,
	EventKindApplyFinished,
	EventKindApplyRecovered,
	EventKindChangeSetDiscarded,
}
