package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// retryDelay is the fixed delay for retrying an unreachable attempt.
// Package tests can shorten this via Clock in Options.
var retryDelay = 100 * time.Millisecond

// dispatchFailureOutcome describes how a transport failure should be handled.
type dispatchFailureOutcome struct {
	// shouldRetry indicates whether the attempt should be retried immediately
	// (after retryDelay). Only for unreachable on first attempt.
	shouldRetry bool
	// openHold indicates whether a capacity hold should be opened.
	// True for reset, timeout, and adapter_error.
	openHold bool
	// attemptStatus is the status to set on the attempt. Empty means no change.
	attemptStatus string
	// terminalReason is the terminal reason to set on the turn, if any.
	// When set, the turn ends incomplete with this reason.
	terminalReason string
}

// transportStatusHandlers maps each TransportStatus to its handling outcome.
// This is the core dispatch failure decision table.
var transportStatusHandlers = map[string]func() dispatchFailureOutcome{
	transport.TransportStatusUnreachable: func() dispatchFailureOutcome {
		// Unreachable: retry once if this is the first attempt.
		// If already retried or second unreachable, fail the turn.
		// No hold. The retry decision is made by the caller based on retry_count.
		return dispatchFailureOutcome{
			shouldRetry: true, // Caller checks retry_count before retrying
		}
	},
	transport.TransportStatusReset: func() dispatchFailureOutcome {
		// Reset: request may have been sent. Never retry.
		// Open capacity hold.
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	},
	transport.TransportStatusTimeout: func() dispatchFailureOutcome {
		// Timeout: request may have been sent. Never retry.
		// Open capacity hold.
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	},
	transport.TransportStatusUnauthorized: func() dispatchFailureOutcome {
		// Unauthorized: turn ends incomplete at once. No retry, no hold.
		return dispatchFailureOutcome{
			// Terminal reason is set by caller based on status
		}
	},
	transport.TransportStatusModelUnavailable: func() dispatchFailureOutcome {
		// Model unavailable: turn ends incomplete at once. No retry, no hold.
		return dispatchFailureOutcome{
			// Terminal reason is set by caller based on status
		}
	},
	transport.TransportStatusMalformedBody: func() dispatchFailureOutcome {
		// Malformed body: attempt ends with error, turn ends incomplete.
		// No retry, no hold.
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusError),
		}
	},
	transport.TransportStatusBodyTooLarge: func() dispatchFailureOutcome {
		// Body too large: attempt ends with error, turn ends incomplete.
		// No retry, no hold.
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusError),
		}
	},
	transport.TransportStatusCancelled: func() dispatchFailureOutcome {
		// Cancelled: only follows engine's own Cancel.
		// If arrives unexpectedly, treat as reset.
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	},
	transport.TransportStatusOK: func() dispatchFailureOutcome {
		// OK with non-complete status is handled by CheckResult gate,
		// not by this table. No special handling needed here.
		return dispatchFailureOutcome{}
	},
}

// handleTransportFailure determines the outcome of a transport failure.
// It is called when Complete returns a successful CompleteResult with a failure status,
// or when it returns a Go error.
//
// For TransportStatus failures, it returns the outcome directly.
// For Go errors, it checks whether the error wraps transport.ErrNotSent:
//   - ErrNotSent: attempt ends with error status and no hold
//   - Other errors: attempt ends with interrupted status and opens a hold
//
// The retry decision for unreachable is made by the caller based on the attempt's retry_count.
func handleTransportFailure(status string, goErr error) dispatchFailureOutcome {
	// Handle Go error cases first
	if goErr != nil {
		if errors.Is(goErr, transport.ErrNotSent) {
			// Request was not sent: error status, no hold
			return dispatchFailureOutcome{
				attemptStatus: string(transport.StatusError),
			}
		}
		// Other error: request may have reached the server
		// interrupted status, open hold
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusInterrupted),
			openHold:      true,
		}
	}

	// Handle TransportStatus cases
	handler, exists := transportStatusHandlers[status]
	if !exists {
		// Unknown status or http_<code>: treat as error, no retry, no hold
		if len(status) > 5 && status[:5] == "http_" {
			return dispatchFailureOutcome{
				attemptStatus: string(transport.StatusError),
			}
		}
		// Should not reach here with valid statuses
		return dispatchFailureOutcome{
			attemptStatus: string(transport.StatusError),
		}
	}

	return handler()
}

// buildTransportFailureReason constructs the terminal reason for a transport failure.
// It is called after handleTransportFailure to determine what reason string to use.
func buildTransportFailureReason(participant string, status string) string {
	// Transport status includes the full status as reported by the adapter
	return fmt.Sprintf("reviewer_failed:%s:%s", participant, status)
}

// DecideDispatchFailure analyzes a transport failure and returns what should be done.
// This is the main entry point for dispatch failure handling.
//
// Parameters:
//   - participant: the participant ID for the attempt
//   - retryCount: the current retry count on the attempt (0 for first attempt)
//   - status: the TransportStatus from the result
//   - goErr: a Go error from Complete, or nil if Complete succeeded with a result
//
// Returns:
//   - shouldRetry: whether to retry this attempt
//   - openHold: whether to open a capacity hold
//   - newStatus: the status to set on the attempt, or empty to keep current
//   - terminalReason: the terminal reason if the turn should end, or empty to continue
func DecideDispatchFailure(participant string, retryCount int, status string, goErr error) (shouldRetry, openHold bool, newStatus, terminalReason string) {
	outcome := handleTransportFailure(status, goErr)

	// Determine retry eligibility for unreachable
	shouldRetry = false
	if outcome.shouldRetry && status == transport.TransportStatusUnreachable {
		// Only retry if this is the first attempt (retryCount == 0)
		if retryCount == 0 {
			shouldRetry = true
		} else {
			// Second unreachable: end the turn
			terminalReason = buildTransportFailureReason(participant, status)
		}
	}

	// For non-retry failures, construct the terminal reason
	if !outcome.shouldRetry && terminalReason == "" && status != transport.TransportStatusOK {
		if status == transport.TransportStatusUnauthorized || status == transport.TransportStatusModelUnavailable {
			// These end the turn immediately
			terminalReason = buildTransportFailureReason(participant, status)
		} else if status == transport.TransportStatusReset || status == transport.TransportStatusTimeout ||
			status == transport.TransportStatusCancelled {
			// These end the turn with the status as reason (cancelled is unexpected here, treated as reset)
			terminalReason = buildTransportFailureReason(participant, status)
		} else if status == transport.TransportStatusMalformedBody || status == transport.TransportStatusBodyTooLarge ||
			(len(status) > 5 && status[:5] == "http_") {
			// These end the turn with the status as reason
			terminalReason = buildTransportFailureReason(participant, status)
		} else if goErr != nil {
			// Go error cases: format the status appropriately
			if errors.Is(goErr, transport.ErrNotSent) {
				terminalReason = fmt.Sprintf("reviewer_failed:%s:not_sent:%v", participant, goErr)
			} else {
				terminalReason = fmt.Sprintf("reviewer_failed:%s:adapter_error:%v", participant, goErr)
			}
		}
	}

	openHold = outcome.openHold
	newStatus = outcome.attemptStatus

	return
}
