package transport

import "errors"

// Sentinel errors.
var (
	ErrUsageUnavailable  = errors.New("usage unavailable")
	ErrNotImplemented    = errors.New("not implemented")
	ErrEnvelopeMismatch  = errors.New("envelope mismatch")
	ErrIdentityMismatch  = errors.New("identity mismatch")
	ErrStatusNotComplete = errors.New("status not complete")
	ErrUnreachable       = errors.New("unreachable")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrModelUnavailable  = errors.New("model unavailable")
	ErrInvalidIdentity   = errors.New("invalid identity")
	ErrNotSent           = errors.New("request not sent")
	ErrUncertain         = errors.New("request sent; completion uncertain")
	ErrIncompleteProbe   = errors.New("incomplete probe: missing model or max-tokens field")
)

// ProbeStepError is a probe failure that names the probe request it failed
// on and the HTTP status that request got, when it got one. It unwraps to the
// underlying sentinel error.
type ProbeStepError struct {
	Step   string
	Status int // 0 when no response was recorded for the step
	Err    error
}

func (e *ProbeStepError) Error() string { return e.Err.Error() }
func (e *ProbeStepError) Unwrap() error { return e.Err }
