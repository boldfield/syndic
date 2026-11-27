package transport

import (
	"errors"
	"time"
)

// FailureClass is the class of a failed send to a model endpoint. The empty
// value means no class: either the send did not fail, or the engine cancelled
// it before any response arrived.
type FailureClass string

const (
	FailureClassNone            FailureClass = ""
	FailureClassLocal           FailureClass = "local"
	FailureClassUnreachable     FailureClass = "unreachable"
	FailureClassThrottled       FailureClass = "throttled"
	FailureClassQuota           FailureClass = "quota"
	FailureClassServerFault     FailureClass = "server_fault"
	FailureClassUncertain       FailureClass = "uncertain"
	FailureClassRejectedConfig  FailureClass = "rejected_config"
	FailureClassRejectedRequest FailureClass = "rejected_request"
	FailureClassBadResponse     FailureClass = "bad_response"
)

// ServerWaitKind says whether a server stated a wait.
type ServerWaitKind int

const (
	// ServerWaitAbsent means the server stated no wait. It is the zero value.
	ServerWaitAbsent ServerWaitKind = iota
	// ServerWaitDuration means the server stated Duration; zero is a duration.
	ServerWaitDuration
	// ServerWaitUnbounded means the server stated a wait too large to hold.
	ServerWaitUnbounded
)

// ServerWait is a server-stated wait: absent, a duration, or unbounded.
type ServerWait struct {
	Kind     ServerWaitKind
	Duration time.Duration
}

// BodyRead is how the read of a 2xx response body ended.
type BodyRead int

const (
	// BodyParsed means the body was read whole and parsed. It is the zero value.
	BodyParsed BodyRead = iota
	// BodyUnparsable means the body was read whole and could not be parsed.
	BodyUnparsable
	// BodyReset means the read ended in a connection reset.
	BodyReset
	// BodyTimeout means the read ended in a timeout.
	BodyTimeout
	// BodyOverLimit means the read passed the body limit before the end.
	BodyOverLimit
)

// ClassifyInput is what was observed of one send.
type ClassifyInput struct {
	// Received is true when the response's status line and headers were read.
	Received bool
	// HTTPStatus is the response status; meaningful only when Received.
	HTTPStatus int
	// TransportStatus is the adapter's TransportStatus string.
	TransportStatus string
	// Err is the Go error of the send, if any.
	Err error
	// EngineCancelled is true when the engine itself cancelled the request.
	EngineCancelled bool
	// RedirectFollowed is true when the adapter followed a redirect.
	RedirectFollowed bool
	// QuotaMarker is true when the response body carries a quota marker.
	QuotaMarker bool
	// BodyRead is how a 2xx body read ended.
	BodyRead BodyRead
	// BadGatewayServerFault is the endpoint's bad_gateway = "server_fault" choice.
	BadGatewayServerFault bool
}

// Classify gives the class of a send's result, applying precedence rules 1 to
// 6 of the v10 delta in order. It is pure.
func Classify(in ClassifyInput) FailureClass {
	if !in.Received {
		// Rule 1.
		if in.EngineCancelled {
			return FailureClassNone
		}
		// Rule 2. After a followed redirect a request was sent, so rule 3 applies.
		if !in.RedirectFollowed {
			if in.TransportStatus == TransportStatusUnreachable {
				return FailureClassUnreachable
			}
			if errors.Is(in.Err, ErrNotSent) {
				return FailureClassLocal
			}
		}
		// Rule 3.
		return FailureClassUncertain
	}
	// Rule 4.
	if in.QuotaMarker {
		return FailureClassQuota
	}
	// Rule 5.
	if in.HTTPStatus < 200 || in.HTTPStatus >= 300 {
		return classifyStatus(in.HTTPStatus, in.BadGatewayServerFault)
	}
	// Rule 6.
	switch in.BodyRead {
	case BodyReset, BodyTimeout, BodyOverLimit:
		return FailureClassUncertain
	case BodyUnparsable:
		return FailureClassBadResponse
	}
	return FailureClassNone
}

// classifyStatus is the status table of section 7 for a non-2xx status.
func classifyStatus(code int, badGatewayServerFault bool) FailureClass {
	switch code {
	case 429, 503, 529:
		return FailureClassThrottled
	case 502:
		if badGatewayServerFault {
			return FailureClassServerFault
		}
		return FailureClassUncertain
	case 504, 408:
		return FailureClassUncertain
	case 501, 505, 401, 403, 404:
		return FailureClassRejectedConfig
	case 402:
		return FailureClassQuota
	}
	switch {
	case code >= 500 && code < 600:
		return FailureClassServerFault
	case code >= 400 && code < 500:
		return FailureClassRejectedRequest
	case code >= 300 && code < 400:
		return FailureClassRejectedConfig
	}
	return FailureClassUncertain
}
