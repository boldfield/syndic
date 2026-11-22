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
	StepKindError
	StepKindNotSent
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
	Err                error
	Usage              *transport.Usage
	// Build, when set on a reply step, computes Text from the request, so a
	// reply can carry values that exist only after dispatch (an attempt id
	// or a candidate hash).
	Build func(req transport.CompleteRequest) string
}

// CompleteCall records a single Complete call with all its details.
type CompleteCall struct {
	Request transport.CompleteRequest
}

// Adapter is a scripted fake adapter for testing.
type Adapter struct {
	script               []Step
	defaultIdentity      transport.ModelIdentity
	defaultReportedModel string
	capability           transport.CapabilityRecord
	resolvedSecrets      []string
	cancelUnconfirmed    bool
	usageAvailable       bool
	blockingCancel       bool
	ProbeUncertainStep   string

	mu             sync.Mutex
	calls          []CompleteCall
	current        int
	cancels        []string
	hangingAttempt map[string]context.CancelFunc
	cancelBlocked  chan struct{} // closed to let blocking cancels proceed
	releaseOnce    sync.Once
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
		cancelUnconfirmed:    opts.CancelUnconfirmed,
		usageAvailable:       opts.UsageAvailable,
		blockingCancel:       opts.BlockingCancel,
		ProbeUncertainStep:   opts.ProbeUncertainStep,
		hangingAttempt:       make(map[string]context.CancelFunc),
		cancelBlocked:        make(chan struct{}),
	}
}

// Options holds configuration for the fake adapter.
type Options struct {
	DefaultIdentity      transport.ModelIdentity
	DefaultReportedModel string
	Capability           transport.CapabilityRecord
	ResolvedSecrets      []string
	CancelUnconfirmed    bool
	UsageAvailable       bool
	BlockingCancel       bool   // when true, Cancel blocks on cancelBlocked until released
	ProbeUncertainStep   string // probe step that fails after its BeforeSend hook as ErrUncertain
}

// Probe returns the configured capability record.
// When opts.BeforeSend is provided, it is called before each request the real
// adapter would send: models, max_tokens, max_completion_tokens (whenever the
// configured capability's MaxTokensField is not max_tokens), system_role,
// the two calibration requests (unless disabled), and over_length (only when
// calibration is enabled and available and AssumedContextLen > 0).
// ProbeUncertainStep, when set, makes the named step fail after its hook ran,
// as a sent request of uncertain completion (transport.ErrUncertain).
func (a *Adapter) Probe(ctx context.Context, endpoint, model string, opts transport.ProbeOptions) (transport.CapabilityRecord, error) {
	steps := []string{"models", "max_tokens"}
	if a.capability.MaxTokensField != "max_tokens" {
		steps = append(steps, "max_completion_tokens")
	}
	steps = append(steps, "system_role")
	if !opts.DisableCalibration {
		steps = append(steps, "calibration_baseline", "calibration")
		if !opts.DisableOverLength && a.capability.Calibration.Available && opts.AssumedContextLen > 0 {
			steps = append(steps, "over_length")
		}
	}
	for _, step := range steps {
		if opts.BeforeSend != nil {
			if err := opts.BeforeSend(&transport.ProbeRequest{
				Model:   model,
				Step:    step,
				Timeout: opts.Timeout,
			}); err != nil {
				return transport.CapabilityRecord{}, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
			}
		}
		if a.ProbeUncertainStep == step {
			return transport.CapabilityRecord{}, fmt.Errorf("%w: fake probe failure after send at %s", transport.ErrUncertain, step)
		}
	}
	// Like the real adapter, the record names the endpoint and model probed.
	rec := a.capability
	if rec.Endpoint == "" {
		rec.Endpoint = endpoint
	}
	if rec.Model == "" {
		rec.Model = model
	}
	if rec.ReportedModel == "" || rec.MaxTokensField == "" {
		return rec, fmt.Errorf("%w: missing ReportedModel or MaxTokensField", transport.ErrIncompleteProbe)
	}
	return rec, nil
}

// Complete consumes one step from the script and returns the result.
func (a *Adapter) Complete(ctx context.Context, req transport.CompleteRequest) (transport.CompleteResult, error) {
	a.mu.Lock()

	// Record the call with a deep copy of the entire request including messages.
	msgCopy := make([]transport.Message, len(req.Messages))
	copy(msgCopy, req.Messages)
	req.Messages = msgCopy
	a.calls = append(a.calls, CompleteCall{
		Request: req,
	})

	// Check if script is exhausted.
	if a.current >= len(a.script) {
		a.mu.Unlock()
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

	a.mu.Unlock()

	if req.BeforeSend != nil {
		if err := req.BeforeSend(&req); err != nil {
			return transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        a.defaultIdentity,
				ReportedModel:   "",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "not_sent",
			}, fmt.Errorf("%w: %w", transport.ErrNotSent, err)
		}
	}

	switch step.Kind {
	case StepKindReply:
		if step.Build != nil {
			step.Text = step.Build(req)
		}
		// Apply delay for reply steps.
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}

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
			Usage:           step.Usage,
		}, nil

	case StepKindEcho:
		// Apply delay for echo steps.
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}

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
			Usage:           step.Usage,
		}, nil

	case StepKindError:
		return transport.CompleteResult{
			Envelope: req.Envelope,
		}, step.Err

	case StepKindNotSent:
		if step.Err == nil {
			return transport.CompleteResult{
				Envelope: req.Envelope,
			}, transport.ErrNotSent
		}
		return transport.CompleteResult{
			Envelope: req.Envelope,
		}, fmt.Errorf("%w: %w", transport.ErrNotSent, step.Err)

	case StepKindHang:
		// Block until context is done, Cancel is called, or req.Deadline passes.
		attemptID := req.Envelope.AttemptID
		hangCtx := ctx
		if !req.Deadline.IsZero() {
			var cancel context.CancelFunc
			hangCtx, cancel = context.WithDeadline(ctx, req.Deadline)
			defer cancel()
		}
		hangCtxCancel, cancelFunc := context.WithCancel(hangCtx)
		a.mu.Lock()
		a.hangingAttempt[attemptID] = cancelFunc
		// A Cancel that arrived before the hang registered still ends it.
		for _, id := range a.cancels {
			if id == attemptID {
				cancelFunc()
				break
			}
		}
		a.mu.Unlock()

		<-hangCtxCancel.Done()

		a.mu.Lock()
		delete(a.hangingAttempt, attemptID)
		a.mu.Unlock()

		ts := transport.TransportStatusTimeout
		if hangCtxCancel.Err() == context.Canceled {
			ts = transport.TransportStatusCancelled
		} else if hangCtxCancel.Err() == context.DeadlineExceeded {
			ts = transport.TransportStatusTimeout
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

// Cancel records a cancellation request and unblocks any hanging attempt with that ID.
func (a *Adapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels = append(a.cancels, attemptID)

	confirmed := transport.TriYes
	if a.cancelUnconfirmed {
		confirmed = transport.TriUnknown
	}

	if cancel, exists := a.hangingAttempt[attemptID]; exists {
		cancel()
	}

	if a.blockingCancel {
		a.mu.Unlock()
		<-a.cancelBlocked
		a.mu.Lock()
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

// ReleaseBlockingCancel releases blocking Cancel calls. Only call this if
// BlockingCancel was enabled in Options.
func (a *Adapter) ReleaseBlockingCancel() {
	a.releaseOnce.Do(func() { close(a.cancelBlocked) })
}

// Calls returns deep copies of all CompleteCall records received, in order.
func (a *Adapter) Calls() []CompleteCall {
	a.mu.Lock()
	defer a.mu.Unlock()

	result := make([]CompleteCall, len(a.calls))
	for i, call := range a.calls {
		msgCopy := make([]transport.Message, len(call.Request.Messages))
		copy(msgCopy, call.Request.Messages)
		requestCopy := call.Request
		requestCopy.Messages = msgCopy
		result[i] = CompleteCall{
			Request: requestCopy,
		}
	}
	return result
}

// CancelCalls returns the attempt id of every Cancel call received, in order.
func (a *Adapter) CancelCalls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.cancels...)
}
