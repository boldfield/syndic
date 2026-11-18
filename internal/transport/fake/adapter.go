package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// StepKind represents the type of step in a script.
type StepKind int

const (
	StepKindReply StepKind = iota
	StepKindHang
	StepKindEcho
)

// Step represents a single step in a script that determines what Complete returns.
type Step struct {
	Kind               StepKind
	Text               string
	RawOutput          string
	Status             transport.CompletionStatus
	NativeFinishReason string
	TransportStatus    string
	Identity           transport.ModelIdentity
	ReportedModel      string
	Delay              time.Duration
	EchoedString       string
}

// Adapter is a scripted fake adapter for testing.
type Adapter struct {
	script               []Step
	defaultIdentity      transport.ModelIdentity
	defaultReportedModel string
	capability           transport.CapabilityRecord
	resolvedSecrets      []string
	cancelConfirmed      bool
	usageAvailable       bool

	mu      sync.Mutex
	calls   [][]transport.Message
	current int
}

// New creates a new fake adapter with the given script and options.
func New(script []Step, opts Options) *Adapter {
	if opts.DefaultIdentity == (transport.ModelIdentity{}) {
		opts.DefaultIdentity = transport.NewUnknownIdentity()
	}
	if opts.DefaultReportedModel == "" {
		opts.DefaultReportedModel = "unknown"
	}

	return &Adapter{
		script:               script,
		defaultIdentity:      opts.DefaultIdentity,
		defaultReportedModel: opts.DefaultReportedModel,
		capability:           opts.Capability,
		resolvedSecrets:      opts.ResolvedSecrets,
		cancelConfirmed:      opts.CancelConfirmed,
		usageAvailable:       opts.UsageAvailable,
	}
}

// Options holds configuration for the fake adapter.
type Options struct {
	DefaultIdentity      transport.ModelIdentity
	DefaultReportedModel string
	Capability           transport.CapabilityRecord
	ResolvedSecrets      []string
	CancelConfirmed      bool
	UsageAvailable       bool
}

// Probe returns the configured capability record.
func (a *Adapter) Probe(ctx context.Context, endpoint, model string) (transport.CapabilityRecord, error) {
	return a.capability, nil
}

// Complete consumes one step from the script and returns the result.
func (a *Adapter) Complete(ctx context.Context, req transport.CompleteRequest) (transport.CompleteResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Record the call
	a.calls = append(a.calls, req.Messages)

	// Check if script is exhausted
	if a.current >= len(a.script) {
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        a.defaultIdentity,
			ReportedModel:   "",
			Status:          transport.StatusError,
			NativeFinish:    "",
			TransportStatus: "script_exhausted",
		}, nil
	}

	step := a.script[a.current]
	a.current++

	identity := step.Identity
	if identity == (transport.ModelIdentity{}) {
		identity = a.defaultIdentity
	}

	reportedModel := step.ReportedModel
	if reportedModel == "" {
		reportedModel = a.defaultReportedModel
	}

	switch step.Kind {
	case StepKindReply:
		rawOutput := step.RawOutput
		if rawOutput == "" {
			rawOutput = step.Text
		}
		scrubbed, count := transport.Scrub(rawOutput, a.resolvedSecrets)
		text := step.Text
		if text != "" {
			text, _ = transport.Scrub(text, a.resolvedSecrets)
		}
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       scrubbed,
			Text:            text,
			ScrubCount:      count,
			Identity:        identity,
			ReportedModel:   reportedModel,
			Status:          step.Status,
			NativeFinish:    step.NativeFinishReason,
			TransportStatus: step.TransportStatus,
		}, nil

	case StepKindEcho:
		responseText := fmt.Sprintf("Echo: %s", step.EchoedString)
		scrubbed, count := transport.Scrub(responseText, a.resolvedSecrets)
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       scrubbed,
			Text:            scrubbed,
			ScrubCount:      count,
			Identity:        identity,
			ReportedModel:   reportedModel,
			Status:          step.Status,
			NativeFinish:    step.NativeFinishReason,
			TransportStatus: step.TransportStatus,
		}, nil

	case StepKindHang:
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}

		select {
		case <-ctx.Done():
			ts := step.TransportStatus
			if ctx.Err() == context.DeadlineExceeded {
				ts = transport.TransportStatusTimeout
			} else if ctx.Err() == context.Canceled {
				ts = transport.TransportStatusCancelled
			}
			return transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        identity,
				ReportedModel:   reportedModel,
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: ts,
			}, nil
		default:
			// Context not done, treat as timeout
			return transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        identity,
				ReportedModel:   reportedModel,
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusTimeout,
			}, nil
		}

	default:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        a.defaultIdentity,
			ReportedModel:   "",
			Status:          transport.StatusError,
			NativeFinish:    "",
			TransportStatus: "unknown_step_kind",
		}, nil
	}
}

// Cancel records a cancellation request.
func (a *Adapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	confirmed := transport.TriUnknown
	if a.cancelConfirmed {
		confirmed = transport.TriYes
	}
	return transport.CancelStatus{
		Requested:       true,
		ServerConfirmed: confirmed,
	}, nil
}

// Usage returns usage information if available.
func (a *Adapter) Usage(ctx context.Context) (*transport.Usage, error) {
	if !a.usageAvailable {
		return nil, transport.ErrUsageUnavailable
	}
	return &transport.Usage{
		Prompt:     10,
		Completion: 5,
	}, nil
}

// Calls returns the message arrays for all Complete calls, in order.
func (a *Adapter) Calls() [][]transport.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([][]transport.Message, len(a.calls))
	copy(result, a.calls)
	return result
}
