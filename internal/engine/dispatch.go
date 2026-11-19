package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// dispatchRetryDelay is the fixed delay for retrying an unreachable attempt.
// Tests can shorten this via export_test.go.
var dispatchRetryDelay = 100 * time.Millisecond

// dispatchFailureOutcome describes how a transport failure should be handled.
type dispatchFailureOutcome struct {
	shouldRetry   bool
	openHold      bool
	attemptStatus string
}

// dispatchFailureTable maps each TransportStatus to its handling outcome.
var dispatchFailureTable = map[string]func() dispatchFailureOutcome{
	transport.TransportStatusUnreachable: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{shouldRetry: true}
	},
	transport.TransportStatusReset: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	},
	transport.TransportStatusTimeout: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	},
	transport.TransportStatusUnauthorized: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{}
	},
	transport.TransportStatusModelUnavailable: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{}
	},
	transport.TransportStatusMalformedBody: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusError),
		}
	},
	transport.TransportStatusBodyTooLarge: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusError),
		}
	},
	transport.TransportStatusCancelled: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	},
	transport.TransportStatusOK: func() dispatchFailureOutcome {
		return dispatchFailureOutcome{}
	},
}

// decideDispatchFailure maps a transport failure to its handling decision.
// It returns: shouldRetry (attempt should retry after delay), openHold (capacity
// hold should be opened), newStatus (status to set on attempt, empty if none),
// and terminalReason (reason part - without role prefix; caller adds the role prefix).
func decideDispatchFailure(role string, participant string, retryCount int, status string, goErr error) (shouldRetry, openHold bool, newStatus, terminalReason string) {
	// Handle Go error cases first
	if goErr != nil {
		if errors.Is(goErr, transport.ErrNotSent) {
			if role == "author" {
				return false, false, string(transport.StatusError), fmt.Sprintf("not_sent:%v", goErr)
			}
			return false, false, string(transport.StatusError), fmt.Sprintf("%s:not_sent:%v", participant, goErr)
		}
		// Other error: request may have reached the server
		if role == "author" {
			return false, true, string(transport.StatusInterrupted), fmt.Sprintf("adapter_error:%v", goErr)
		}
		return false, true, string(transport.StatusInterrupted), fmt.Sprintf("%s:adapter_error:%v", participant, goErr)
	}

	// Handle TransportStatus cases
	handler, exists := dispatchFailureTable[status]
	if !exists {
		// Unknown status or http_<code>: treat as error, no retry, no hold
		if len(status) > 5 && status[:5] == "http_" {
			if role == "author" {
				return false, false, string(transport.StatusError), status
			}
			return false, false, string(transport.StatusError), fmt.Sprintf("%s:%s", participant, status)
		}
		if role == "author" {
			return false, false, string(transport.StatusError), status
		}
		return false, false, string(transport.StatusError), fmt.Sprintf("%s:%s", participant, status)
	}

	outcome := handler()

	// For unreachable: retry only on first attempt
	if outcome.shouldRetry && status == transport.TransportStatusUnreachable {
		if retryCount == 0 {
			return true, false, "", ""
		}
		// Second unreachable: end the turn
		if role == "author" {
			terminalReason = "unreachable"
		} else {
			terminalReason = fmt.Sprintf("%s:unreachable", participant)
		}
		return false, false, string(transport.StatusError), terminalReason
	}

	// For other failures, construct terminal reason
	terminalReason = ""
	if !outcome.shouldRetry && status != transport.TransportStatusOK {
		if role == "author" {
			terminalReason = status
		} else {
			terminalReason = fmt.Sprintf("%s:%s", participant, status)
		}
	}

	return false, outcome.openHold, outcome.attemptStatus, terminalReason
}
