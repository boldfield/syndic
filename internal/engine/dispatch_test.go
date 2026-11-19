package engine

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

func TestHandleTransportFailure(t *testing.T) {
	tests := []struct {
		name           string
		status         string
		goErr          error
		wantRetry      bool
		wantHold       bool
		wantStatus     string
		wantHTTPPrefix string
	}{
		// Transport status: unreachable
		{
			name:       "unreachable_first_attempt",
			status:     transport.TransportStatusUnreachable,
			goErr:      nil,
			wantRetry:  true,
			wantHold:   false,
			wantStatus: "",
		},
		// Transport status: reset
		{
			name:       "reset",
			status:     transport.TransportStatusReset,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   true,
			wantStatus: string(transport.StatusInterrupted),
		},
		// Transport status: timeout
		{
			name:       "timeout",
			status:     transport.TransportStatusTimeout,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   true,
			wantStatus: string(transport.StatusInterrupted),
		},
		// Transport status: unauthorized
		{
			name:       "unauthorized",
			status:     transport.TransportStatusUnauthorized,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: "",
		},
		// Transport status: model_unavailable
		{
			name:       "model_unavailable",
			status:     transport.TransportStatusModelUnavailable,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: "",
		},
		// Transport status: malformed_body
		{
			name:       "malformed_body",
			status:     transport.TransportStatusMalformedBody,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: string(transport.StatusError),
		},
		// Transport status: body_too_large
		{
			name:       "body_too_large",
			status:     transport.TransportStatusBodyTooLarge,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: string(transport.StatusError),
		},
		// Transport status: cancelled
		{
			name:       "cancelled",
			status:     transport.TransportStatusCancelled,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   true,
			wantStatus: string(transport.StatusInterrupted),
		},
		// HTTP error codes
		{
			name:       "http_500",
			status:     "http_500",
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: string(transport.StatusError),
		},
		{
			name:       "http_503",
			status:     "http_503",
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: string(transport.StatusError),
		},
		// Go errors
		{
			name:       "go_error_not_sent",
			status:     "",
			goErr:      transport.ErrNotSent,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: string(transport.StatusError),
		},
		{
			name:       "go_error_other",
			status:     "",
			goErr:      errors.New("some adapter error"),
			wantRetry:  false,
			wantHold:   true,
			wantStatus: string(transport.StatusInterrupted),
		},
		// Transport status: ok (should not trigger special handling)
		{
			name:       "ok",
			status:     transport.TransportStatusOK,
			goErr:      nil,
			wantRetry:  false,
			wantHold:   false,
			wantStatus: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := handleTransportFailure(tt.status, tt.goErr)

			if outcome.shouldRetry != tt.wantRetry {
				t.Errorf("shouldRetry: got %v, want %v", outcome.shouldRetry, tt.wantRetry)
			}
			if outcome.openHold != tt.wantHold {
				t.Errorf("openHold: got %v, want %v", outcome.openHold, tt.wantHold)
			}
			if outcome.attemptStatus != tt.wantStatus {
				t.Errorf("attemptStatus: got %q, want %q", outcome.attemptStatus, tt.wantStatus)
			}
		})
	}
}

func TestDecideDispatchFailure(t *testing.T) {
	tests := []struct {
		name               string
		retryCount         int
		status             string
		goErr              error
		wantShouldRetry    bool
		wantOpenHold       bool
		wantNewStatus      string
		wantTerminalReason string
		wantReasonPrefix   string
	}{
		// Unreachable on first attempt: should retry
		{
			name:               "unreachable_first_attempt",
			retryCount:         0,
			status:             transport.TransportStatusUnreachable,
			goErr:              nil,
			wantShouldRetry:    true,
			wantOpenHold:       false,
			wantNewStatus:      "",
			wantTerminalReason: "",
		},
		// Unreachable on second attempt: should end turn
		{
			name:             "unreachable_second_attempt",
			retryCount:       1,
			status:           transport.TransportStatusUnreachable,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    "",
			wantReasonPrefix: "reviewer_failed:test_participant:unreachable",
		},
		// Reset: should end turn with hold
		{
			name:             "reset",
			retryCount:       0,
			status:           transport.TransportStatusReset,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     true,
			wantNewStatus:    string(transport.StatusInterrupted),
			wantReasonPrefix: "reviewer_failed:test_participant:reset",
		},
		// Unauthorized: should end turn without hold
		{
			name:             "unauthorized",
			retryCount:       0,
			status:           transport.TransportStatusUnauthorized,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    "",
			wantReasonPrefix: "reviewer_failed:test_participant:unauthorized",
		},
		// Malformed body: should end turn without retry or hold
		{
			name:             "malformed_body",
			retryCount:       0,
			status:           transport.TransportStatusMalformedBody,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "reviewer_failed:test_participant:malformed_body",
		},
		// ErrNotSent: should end turn without retry or hold
		{
			name:             "go_error_not_sent",
			retryCount:       0,
			status:           "",
			goErr:            transport.ErrNotSent,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "reviewer_failed:test_participant:not_sent:",
		},
		// Other Go error: should end turn with hold
		{
			name:             "go_error_other",
			retryCount:       0,
			status:           "",
			goErr:            errors.New("network failure"),
			wantShouldRetry:  false,
			wantOpenHold:     true,
			wantNewStatus:    string(transport.StatusInterrupted),
			wantReasonPrefix: "reviewer_failed:test_participant:adapter_error:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldRetry, openHold, newStatus, terminalReason := DecideDispatchFailure("test_participant", tt.retryCount, tt.status, tt.goErr)

			if shouldRetry != tt.wantShouldRetry {
				t.Errorf("shouldRetry: got %v, want %v", shouldRetry, tt.wantShouldRetry)
			}
			if openHold != tt.wantOpenHold {
				t.Errorf("openHold: got %v, want %v", openHold, tt.wantOpenHold)
			}
			if newStatus != tt.wantNewStatus {
				t.Errorf("newStatus: got %q, want %q", newStatus, tt.wantNewStatus)
			}

			// For terminal reason, check prefix if specified
			if tt.wantReasonPrefix != "" && !stringStartsWith(terminalReason, tt.wantReasonPrefix) {
				t.Errorf("terminalReason: got %q, want prefix %q", terminalReason, tt.wantReasonPrefix)
			} else if tt.wantTerminalReason != "" && terminalReason != tt.wantTerminalReason {
				t.Errorf("terminalReason: got %q, want %q", terminalReason, tt.wantTerminalReason)
			}
		})
	}
}

func TestBuildTransportFailureReason(t *testing.T) {
	tests := []struct {
		participant string
		status      string
		want        string
	}{
		{"reviewer1", transport.TransportStatusUnreachable, "reviewer_failed:reviewer1:unreachable"},
		{"reviewer1", transport.TransportStatusReset, "reviewer_failed:reviewer1:reset"},
		{"author", transport.TransportStatusUnauthorized, "reviewer_failed:author:unauthorized"},
		{"reviewer2", "http_503", "reviewer_failed:reviewer2:http_503"},
	}

	for _, tt := range tests {
		t.Run(tt.participant+"_"+tt.status, func(t *testing.T) {
			got := buildTransportFailureReason(tt.participant, tt.status)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAllTransportStatusConstantsHandled(t *testing.T) {
	statuses := []string{
		transport.TransportStatusOK,
		transport.TransportStatusUnauthorized,
		transport.TransportStatusModelUnavailable,
		transport.TransportStatusUnreachable,
		transport.TransportStatusTimeout,
		transport.TransportStatusReset,
		transport.TransportStatusCancelled,
		transport.TransportStatusMalformedBody,
		transport.TransportStatusBodyTooLarge,
	}

	for _, status := range statuses {
		t.Run("status_"+status, func(t *testing.T) {
			// Should not panic and should return valid results
			shouldRetry, openHold, newStatus, terminalReason := DecideDispatchFailure("test", 0, status, nil)

			// Log to show we handled it (actual assertions depend on the specific status)
			t.Logf("status=%s: shouldRetry=%v, openHold=%v, newStatus=%q, terminalReason=%q",
				status, shouldRetry, openHold, newStatus, terminalReason)
		})
	}
}

func stringStartsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
