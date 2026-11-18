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
)
