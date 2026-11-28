package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CompletionStatus represents the normalized completion status of a request.
type CompletionStatus string

const (
	StatusComplete    CompletionStatus = "complete"
	StatusToolCalls   CompletionStatus = "tool_calls"
	StatusTruncated   CompletionStatus = "truncated"
	StatusRefused     CompletionStatus = "refused"
	StatusInterrupted CompletionStatus = "interrupted"
	StatusError       CompletionStatus = "error"
)

// Tri represents a three-valued logic state.
type Tri string

const (
	TriYes     Tri = "yes"
	TriNo      Tri = "no"
	TriUnknown Tri = "unknown"
)

// Transport status constants for HTTP responses and states.
const (
	TransportStatusOK               = "ok"
	TransportStatusUnauthorized     = "unauthorized"
	TransportStatusModelUnavailable = "model_unavailable"
	TransportStatusUnreachable      = "unreachable"
	TransportStatusTimeout          = "timeout"
	TransportStatusReset            = "reset"
	TransportStatusCancelled        = "cancelled"
	TransportStatusMalformedBody    = "malformed_body"
	TransportStatusBodyTooLarge     = "body_too_large"
)

// HTTPStatusString returns a transport status string for a non-2xx HTTP response code.
func HTTPStatusString(code int) string {
	if code >= 200 && code < 300 {
		return TransportStatusOK
	}
	return fmt.Sprintf("http_%d", code)
}

// ScrubMarker is the marker used to replace redacted secrets.
const ScrubMarker = "[REDACTED-SECRET]"

// Calibration records calibration information for token budgeting.
type Calibration struct {
	Available            bool
	BytesPerToken        float64
	ObservedPromptTokens int
	BaselinePromptTokens int
	BaselineBytes        int
}

// ModelIdentity represents the identity of a model as observed from the server.
type ModelIdentity struct {
	Kind  string // "digest", "revision", or "unknown"
	Value string
}

// Comparable with ==, so we implement no methods for it.

// NewUnknownIdentity returns an unknown model identity.
func NewUnknownIdentity() ModelIdentity {
	return ModelIdentity{
		Kind:  "unknown",
		Value: "",
	}
}

// NewDigestIdentity returns a digest identity. It returns an error if value is empty or equals requestedModel.
func NewDigestIdentity(value, requestedModel string) (ModelIdentity, error) {
	if value == "" {
		return ModelIdentity{}, ErrInvalidIdentity
	}
	if value == requestedModel {
		return ModelIdentity{}, ErrInvalidIdentity
	}
	return ModelIdentity{
		Kind:  "digest",
		Value: value,
	}, nil
}

// NewRevisionIdentity returns a revision identity. It returns an error if value is empty or equals requestedModel.
func NewRevisionIdentity(value, requestedModel string) (ModelIdentity, error) {
	if value == "" {
		return ModelIdentity{}, ErrInvalidIdentity
	}
	if value == requestedModel {
		return ModelIdentity{}, ErrInvalidIdentity
	}
	return ModelIdentity{
		Kind:  "revision",
		Value: value,
	}, nil
}

// Message represents a single message in the conversation.
type Message struct {
	Role       string // "system", "user", "assistant", or "tool"
	Content    string
	ToolCalls  []ToolCall // Set on assistant messages carrying tool calls
	ToolCallID string     // Set on tool messages to reference the tool_call_id being answered
}

// Tool represents a tool definition sent in a request.
type Tool struct {
	Type     string
	Function ToolFunction
}

// ToolFunction represents a function tool's definition.
type ToolFunction struct {
	Name        string
	Description string
	Parameters  map[string]interface{} // JSON schema for parameters
}

// ToolCall represents a tool call in a response.
type ToolCall struct {
	ID       string
	Type     string
	Function ToolCallFunction
}

// ToolCallFunction represents the function being called.
type ToolCallFunction struct {
	Name      string
	Arguments string // JSON string of arguments
}

// AttemptEnvelope contains attempt-specific metadata.
type AttemptEnvelope struct {
	Participant   string
	TurnID        string
	Phase         string
	Epoch         int
	AttemptID     string
	CandidateHash string // may be empty
}

// Comparable with ==, so we implement no methods for it.

// CompleteRequest is the input to the Complete adapter method.
type CompleteRequest struct {
	Envelope        AttemptEnvelope
	Model           string
	Messages        []Message
	MaxOutputTokens int
	Tools           []Tool
	Deadline        time.Time
	// BeforeSend is an optional callback invoked before the HTTP request is sent.
	// It allows the engine to journal the attempt before dispatch and distinguish
	// ErrNotSent (pre-send failures) from other errors. The callback receives the
	// request and should return an error if the attempt cannot be journaled; in that
	// case, Complete returns the error wrapped in ErrNotSent.
	BeforeSend func(*CompleteRequest) error
}

// CompleteResult is the output of the Complete adapter method.
type CompleteResult struct {
	Envelope        AttemptEnvelope
	RawOutput       string
	Text            string
	ScrubCount      int
	Identity        ModelIdentity
	ReportedModel   string
	Status          CompletionStatus
	NativeFinish    string
	ToolCalls       []ToolCall
	Usage           *Usage
	TransportStatus string
	// FailureClass, ServerWait and NoRetry are set by the adapter on every
	// failed send, a Go error included.
	FailureClass FailureClass
	ServerWait   ServerWait
	NoRetry      bool
}

// RefereeResult is the output of a referee decision on a tool call.
type RefereeResult struct {
	Verdict string // "allow" or "deny"
	Reason  string
}

// Usage records token usage information.
type Usage struct {
	Prompt     int
	Completion int
}

// CancelStatus records the state of a cancellation request.
type CancelStatus struct {
	Requested       bool
	ServerConfirmed Tri
}

// CapabilityRecord records the capabilities of an endpoint and model.
type CapabilityRecord struct {
	Endpoint                 string
	Model                    string
	AuthMode                 string // "api-key", "subscription", "none", "unknown"
	Identity                 ModelIdentity
	ReportedModel            string
	ContextBound             *int
	OutputBound              *int
	Streaming                Tri
	CancelObservable         Tri
	UsageAvailable           Tri
	ModelsEndpoint           Tri
	ModelListed              Tri
	SystemRoleHonored        Tri
	EligibleRequiredReviewer Tri
	ToolCalls                Tri
	MaxTokensField           string
	FinishReasons            map[string]CompletionStatus
	OverLength               string // "rejected:context", "rejected:other", "accepted_whole", "markers_missing", "errored", "not_run:no_calibration", "not_run:no_context"
	OverLengthClassifier     string // the classification rule describing how rejections are classified by status and body
	OverLengthFinishReason   string // the finish reason from the over-length response (empty if no response)
	OverLengthDetail         string // the server's refusal message (scrubbed, at most 200 bytes) when the over-length request was refused
	LatencyMillis            *int
	ToolRestrictions         []string
	ServerVersion            string
	AdapterVersion           string
	Calibration              Calibration
	ObservedAt               time.Time
	// Raw is the unredacted evidence from the probe. It is local-only and never
	// exported or committed; it may contain sensitive server details.
	Raw json.RawMessage
}

// Expected records what was expected for a participant at probe time.
type Expected struct {
	Identity      ModelIdentity
	ReportedModel string
}

// ProbeRequest contains details passed to BeforeSend callbacks during probing.
type ProbeRequest struct {
	Model   string
	Step    string // e.g., "max_tokens", "system_role", "over_length"
	Timeout time.Duration
	// Redirect is 0 for the step's initial request and n for the nth
	// redirect the client follows; each is a separate network request.
	Redirect int
}

// ProbeOptions are options for probing that may be passed through the Adapter interface.
type ProbeOptions struct {
	Timeout            time.Duration
	DisableOverLength  bool
	DisableCalibration bool
	AssumedContextLen  int
	// BeforeSend is an optional callback invoked before each probe HTTP request.
	// It allows the engine to journal the probe request before dispatch and distinguish
	// ErrNotSent (pre-send failures) from other errors.
	BeforeSend func(*ProbeRequest) error
	// AfterResponse is an optional callback the adapter calls after each probe
	// response and before the next probe request. It returns false to refuse
	// the next request; the probe then stops with that step's error.
	AfterResponse func(ProbeResponse) bool
}

// ProbeResponse is what AfterResponse is told of one probe response.
type ProbeResponse struct {
	Step         string
	Status       int
	FailureClass FailureClass
	ServerWait   ServerWait
	NoRetry      bool
}

// ModelsCheck is the result of one CheckModels request.
type ModelsCheck struct {
	// Status is the HTTP status, 0 when no response was received.
	Status int
	// FailureClass, ServerWait and NoRetry are read as for any response.
	FailureClass FailureClass
	ServerWait   ServerWait
	NoRetry      bool
	// OK is true on a 2xx.
	OK bool
	// Usable is false when the route answers 404, 405 or 501.
	Usable bool
	// Listed is true when the 2xx body parsed as a model list within the
	// bound and showed no further page; Models is then the listed ids.
	Listed bool
	Models []string
}

// Adapter is the interface that every participant route implements.
type Adapter interface {
	// CheckModels sends one bounded GET on the endpoint's models route. It is
	// not inference, sends no completion request and writes no evidence.
	CheckModels(ctx context.Context) (ModelsCheck, error)

	// Probe returns the capability record for the endpoint and model.
	// When opts.BeforeSend is provided, it is called before each probe HTTP request,
	// allowing pre-send journaling; ErrNotSent failures are distinguished from post-send errors.
	Probe(ctx context.Context, endpoint, model string, opts ProbeOptions) (CapabilityRecord, error)

	// Complete sends a request and returns the result.
	// Returns a Go error only for requests the adapter refuses to send.
	// Every transport outcome, including failures, is a CompleteResult with the appropriate status.
	Complete(ctx context.Context, req CompleteRequest) (CompleteResult, error)

	// Cancel records a cancellation request.
	Cancel(ctx context.Context, attemptID string) (CancelStatus, error)

	// Usage returns the usage for the endpoint, or ErrUsageUnavailable when not available.
	Usage(ctx context.Context) (*Usage, error)
}
