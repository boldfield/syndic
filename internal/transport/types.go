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
	Role    string // "system", "user", or "assistant"
	Content string
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
	Deadline        time.Time
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
	Usage           *Usage
	TransportStatus string
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
	MaxTokensField           string
	FinishReasons            map[string]CompletionStatus
	OverLength               string // "rejected:context", "rejected:other", "accepted_whole", "markers_missing", "errored", "not_run:no_calibration", "not_run:no_context"
	OverLengthClassifier     string // the classification rule used for rejections (e.g., "matches context_length_exceeded or maximum context length, case-insensitive")
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

// Adapter is the interface that every participant route implements.
type Adapter interface {
	// Probe returns the capability record for the endpoint and model.
	Probe(ctx context.Context, endpoint, model string) (CapabilityRecord, error)

	// Complete sends a request and returns the result.
	// Returns a Go error only for requests the adapter refuses to send.
	// Every transport outcome, including failures, is a CompleteResult with the appropriate status.
	Complete(ctx context.Context, req CompleteRequest) (CompleteResult, error)

	// Cancel records a cancellation request.
	Cancel(ctx context.Context, attemptID string) (CancelStatus, error)

	// Usage returns the usage for the endpoint, or ErrUsageUnavailable when not available.
	Usage(ctx context.Context) (*Usage, error)
}
