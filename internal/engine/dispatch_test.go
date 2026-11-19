package engine

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

func TestDecideDispatchFailure(t *testing.T) {
	tests := []struct {
		name               string
		role               string
		participant        string
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
			role:               "reviewer",
			participant:        "test_participant",
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
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       1,
			status:           transport.TransportStatusUnreachable,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    "",
			wantReasonPrefix: "test_participant:unreachable",
		},
		// Reset: should end turn with hold
		{
			name:             "reset",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusReset,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     true,
			wantNewStatus:    string(transport.StatusInterrupted),
			wantReasonPrefix: "test_participant:reset",
		},
		// Timeout: should end turn with hold
		{
			name:             "timeout",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusTimeout,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     true,
			wantNewStatus:    string(transport.StatusInterrupted),
			wantReasonPrefix: "test_participant:timeout",
		},
		// Unauthorized: should end turn without hold
		{
			name:             "unauthorized",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusUnauthorized,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    "",
			wantReasonPrefix: "test_participant:unauthorized",
		},
		// Model unavailable: should end turn without hold
		{
			name:             "model_unavailable",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusModelUnavailable,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    "",
			wantReasonPrefix: "test_participant:model_unavailable",
		},
		// Malformed body: should end turn without retry or hold
		{
			name:             "malformed_body",
			role:             "author",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusMalformedBody,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "malformed_body",
		},
		// Body too large: should end turn without retry or hold
		{
			name:             "body_too_large",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusBodyTooLarge,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "test_participant:body_too_large",
		},
		// Cancelled: should end turn with hold
		{
			name:             "cancelled",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           transport.TransportStatusCancelled,
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     true,
			wantNewStatus:    string(transport.StatusInterrupted),
			wantReasonPrefix: "test_participant:cancelled",
		},
		// HTTP error codes
		{
			name:             "http_500",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           "http_500",
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "test_participant:http_500",
		},
		{
			name:             "http_503",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           "http_503",
			goErr:            nil,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "test_participant:http_503",
		},
		// Go errors
		{
			name:             "go_error_not_sent",
			role:             "author",
			participant:      "test_participant",
			retryCount:       0,
			status:           "",
			goErr:            transport.ErrNotSent,
			wantShouldRetry:  false,
			wantOpenHold:     false,
			wantNewStatus:    string(transport.StatusError),
			wantReasonPrefix: "not_sent:",
		},
		// Other Go error: should end turn with hold
		{
			name:             "go_error_other",
			role:             "reviewer",
			participant:      "test_participant",
			retryCount:       0,
			status:           "",
			goErr:            errors.New("network failure"),
			wantShouldRetry:  false,
			wantOpenHold:     true,
			wantNewStatus:    string(transport.StatusInterrupted),
			wantReasonPrefix: "test_participant:adapter_error:",
		},
		// Transport status: ok (should not trigger special handling)
		{
			name:               "ok",
			role:               "reviewer",
			participant:        "test_participant",
			retryCount:         0,
			status:             transport.TransportStatusOK,
			goErr:              nil,
			wantShouldRetry:    false,
			wantOpenHold:       false,
			wantNewStatus:      "",
			wantTerminalReason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldRetry, openHold, newStatus, terminalReason := decideDispatchFailure(tt.role, tt.participant, tt.retryCount, tt.status, tt.goErr)

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
			shouldRetry, openHold, newStatus, terminalReason := decideDispatchFailure("reviewer", "test", 0, status, nil)

			t.Logf("status=%s: shouldRetry=%v, openHold=%v, newStatus=%q, terminalReason=%q",
				status, shouldRetry, openHold, newStatus, terminalReason)
		})
	}
}

func stringStartsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
