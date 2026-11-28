package transport

import "errors"

// Sentinel errors.
var (
	ErrUsageUnavailable          = errors.New("usage unavailable")
	ErrNotImplemented            = errors.New("not implemented")
	ErrEnvelopeMismatch          = errors.New("envelope mismatch")
	ErrIdentityMismatch          = errors.New("identity mismatch")
	ErrStatusNotComplete         = errors.New("status not complete")
	ErrUnreachable               = errors.New("unreachable")
	ErrUnauthorized              = errors.New("unauthorized")
	ErrModelUnavailable          = errors.New("model unavailable")
	ErrInvalidIdentity           = errors.New("invalid identity")
	ErrNotSent                   = errors.New("request not sent")
	ErrUncertain                 = errors.New("request sent; completion uncertain")
	ErrIncompleteProbe           = errors.New("incomplete probe: missing model or max-tokens field")
	ErrToolCallsWithoutStatus    = errors.New("tool_calls status without tool calls")
	ErrToolCallsUnderOtherStatus = errors.New("tool calls under non-tool_calls finish status")
	ErrProbeStopped              = errors.New("probe stopped before its next request")
)

// ProbeStepError is a probe failure that names the probe request it failed
// on and the HTTP status that request got, when it got one. It unwraps to the
// underlying sentinel error.
type ProbeStepError struct {
	Step   string
	Status int // 0 when no response was recorded for the step
	// FailureClass, ServerWait and NoRetry are read from the step's response
	// by the functions Complete uses; a step with no response is classed from
	// its error.
	FailureClass FailureClass
	ServerWait   ServerWait
	NoRetry      bool
	Err          error
}

func (e *ProbeStepError) Error() string { return e.Err.Error() }
func (e *ProbeStepError) Unwrap() error { return e.Err }
