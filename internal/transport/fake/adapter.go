package fake

import (
	"context"
	"fmt"
	"reflect"
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
	StepKindReferee
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
	// Usage is the usage reported with the result, a failed one included.
	Usage     *transport.Usage
	ToolCalls []transport.ToolCall
	// HTTPStatus is the status of the response the step stands for. When it is
	// set and FailureClass is not, the class is derived with transport.Classify.
	HTTPStatus   int
	FailureClass transport.FailureClass
	ServerWait   transport.ServerWait
	NoRetry      bool
	// Build, when set on a reply step, computes Text from the request, so a
	// reply can carry values that exist only after dispatch (an attempt id
	// or a candidate hash).
	Build func(req transport.CompleteRequest) string
	// Referee fields: for scripting referee decisions
	RefereeVerdict string // "allow" or "deny"
	RefereeReason  string // reason for the decision
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
	probeSteps           map[string]ProbeStep

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
		probeSteps:           opts.ProbeSteps,
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
	// ProbeSteps scripts the response of a probe step by name. A step with no
	// entry answers 200.
	ProbeSteps map[string]ProbeStep
}

// ProbeStep scripts the response a probe step received. When HTTPStatus is set
// and FailureClass is not, the class is derived with transport.Classify.
type ProbeStep struct {
	HTTPStatus   int
	FailureClass transport.FailureClass
	ServerWait   transport.ServerWait
	NoRetry      bool
}

// derive gives the class a scripted response carries: the scripted one, or the
// one transport.Classify gives its status.
func derive(status int, class transport.FailureClass, transportStatus string, err error) transport.FailureClass {
	if class != transport.FailureClassNone || status == 0 {
		return class
	}
	return transport.Classify(transport.ClassifyInput{Received: true, HTTPStatus: status, TransportStatus: transportStatus, Err: err})
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
		ps, scripted := a.probeSteps[step]
		if !scripted {
			ps.HTTPStatus = 200
		}
		resp := transport.ProbeResponse{
			Step:         step,
			Status:       ps.HTTPStatus,
			FailureClass: derive(ps.HTTPStatus, ps.FailureClass, "", nil),
			ServerWait:   ps.ServerWait,
			NoRetry:      ps.NoRetry,
		}
		if opts.AfterResponse != nil && !opts.AfterResponse(resp) {
			return transport.CapabilityRecord{}, &transport.ProbeStepError{
				Step: step, Status: resp.Status, FailureClass: resp.FailureClass,
				ServerWait: resp.ServerWait, NoRetry: resp.NoRetry, Err: transport.ErrProbeStopped,
			}
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
func (a *Adapter) Complete(ctx context.Context, req transport.CompleteRequest) (res transport.CompleteResult, err error) {
	a.mu.Lock()

	// Record the call with a deep copy of the entire request including messages.
	req = cloneRequest(req)
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
	defer func() {
		res.FailureClass = derive(step.HTTPStatus, step.FailureClass, res.TransportStatus, err)
		res.ServerWait, res.NoRetry = step.ServerWait, step.NoRetry
		if res.Usage == nil {
			res.Usage = step.Usage
		}
	}()

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

		// Copy and scrub tool calls from step, if any
		var toolCalls []transport.ToolCall
		if len(step.ToolCalls) > 0 {
			toolCalls = make([]transport.ToolCall, len(step.ToolCalls))
			for i, call := range step.ToolCalls {
				scrubbedArgs, argScrubCount := transport.Scrub(call.Function.Arguments, a.resolvedSecrets)
				toolCalls[i] = transport.ToolCall{
					ID:   call.ID,
					Type: call.Type,
					Function: transport.ToolCallFunction{
						Name:      call.Function.Name,
						Arguments: scrubbedArgs,
					},
				}
				count += argScrubCount
			}
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
			ToolCalls:       toolCalls,
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

// Referee consumes the next scripted step as a referee decision. It fails
// closed: a referee step yields its verdict (anything but "allow" is deny);
// an error, refusal, truncation, tool-call or otherwise malformed reply is a
// deny, and an exhausted script is a deny.
func (a *Adapter) Referee(ctx context.Context) (transport.RefereeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.current >= len(a.script) {
		return transport.RefereeResult{Verdict: "deny", Reason: "script exhausted"}, nil
	}
	step := a.script[a.current]
	a.current++

	switch step.Kind {
	case StepKindReferee:
		switch step.RefereeVerdict {
		case "allow", "deny":
			return transport.RefereeResult{Verdict: step.RefereeVerdict, Reason: step.RefereeReason}, nil
		}
		return transport.RefereeResult{Verdict: "deny", Reason: "malformed verdict"}, nil
	case StepKindError:
		return transport.RefereeResult{Verdict: "deny", Reason: "adapter error"}, nil
	case StepKindReply:
		switch {
		case len(step.ToolCalls) > 0:
			return transport.RefereeResult{Verdict: "deny", Reason: "tool-call reply"}, nil
		case step.Status == transport.StatusRefused:
			return transport.RefereeResult{Verdict: "deny", Reason: "refusal"}, nil
		case step.Status == transport.StatusTruncated:
			return transport.RefereeResult{Verdict: "deny", Reason: "truncation"}, nil
		}
	}
	return transport.RefereeResult{Verdict: "deny", Reason: "unexpected step"}, nil
}

// cloneRequest deep-copies every slice and map a request carries.
func cloneRequest(req transport.CompleteRequest) transport.CompleteRequest {
	req.Messages = append([]transport.Message(nil), req.Messages...)
	for i, m := range req.Messages {
		if m.ToolCalls != nil {
			req.Messages[i].ToolCalls = append([]transport.ToolCall(nil), m.ToolCalls...)
		}
	}
	if req.Tools != nil {
		tools := make([]transport.Tool, len(req.Tools))
		for i, t := range req.Tools {
			t.Function.Parameters = cloneMap(t.Function.Parameters)
			tools[i] = t
		}
		req.Tools = tools
	}
	return req
}

func cloneValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(v)).Interface()
}

// cloneReflect deep-copies maps, slices, arrays, structs, pointers and
// interfaces so typed JSON-schema values ([]string, map[string]string, ...)
// are not aliased. Unexported struct fields cannot be set and are never
// marshaled, so they keep a shallow copy.
func cloneReflect(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			out.SetMapIndex(k, cloneReflect(v.MapIndex(k)))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneReflect(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneReflect(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneReflect(v.Field(i)))
			}
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneReflect(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneReflect(v.Elem()))
		return out
	}
	return v
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
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
		result[i] = CompleteCall{
			Request: cloneRequest(call.Request),
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
