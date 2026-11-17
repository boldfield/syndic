package transport

import (
	"errors"
	"testing"
	"time"
)

func TestCheckResult(t *testing.T) {
	now := time.Now()
	envelope := AttemptEnvelope{
		Participant:   "participant1",
		TurnID:        "turn1",
		Phase:         "phase1",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "hash1",
	}

	identity := ModelIdentity{Kind: "digest", Value: "v123"}
	reportedModel := "gpt-4"
	expected := Expected{
		Identity:      identity,
		ReportedModel: reportedModel,
	}

	tests := []struct {
		name        string
		req         CompleteRequest
		res         CompleteResult
		expected    Expected
		wantError   bool
		wantErrors  []error
	}{
		{
			name: "clean result",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope:        envelope,
				RawOutput:       "output",
				Text:            "output",
				ScrubCount:      0,
				Identity:        identity,
				ReportedModel:   reportedModel,
				Status:          StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: TransportStatusOK,
			},
			expected:  expected,
			wantError: false,
		},
		{
			name: "envelope mismatch only",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope: AttemptEnvelope{
					Participant:   "participant2",
					TurnID:        "turn1",
					Phase:         "phase1",
					Epoch:         1,
					AttemptID:     "attempt1",
					CandidateHash: "hash1",
				},
				Identity:        identity,
				ReportedModel:   reportedModel,
				Status:          StatusComplete,
				TransportStatus: TransportStatusOK,
			},
			expected:  expected,
			wantError: true,
			wantErrors: []error{ErrEnvelopeMismatch},
		},
		{
			name: "identity mismatch only",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope:        envelope,
				Identity:        ModelIdentity{Kind: "digest", Value: "v456"},
				ReportedModel:   reportedModel,
				Status:          StatusComplete,
				TransportStatus: TransportStatusOK,
			},
			expected:  expected,
			wantError: true,
			wantErrors: []error{ErrIdentityMismatch},
		},
		{
			name: "reported model mismatch",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope:        envelope,
				Identity:        identity,
				ReportedModel:   "gpt-3.5",
				Status:          StatusComplete,
				TransportStatus: TransportStatusOK,
			},
			expected:  expected,
			wantError: true,
			wantErrors: []error{ErrIdentityMismatch},
		},
		{
			name: "status not complete only",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope:        envelope,
				Identity:        identity,
				ReportedModel:   reportedModel,
				Status:          StatusTruncated,
				TransportStatus: TransportStatusOK,
			},
			expected:  expected,
			wantError: true,
			wantErrors: []error{ErrStatusNotComplete},
		},
		{
			name: "all three faults",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope: AttemptEnvelope{
					Participant:   "participant2",
					TurnID:        "turn1",
					Phase:         "phase1",
					Epoch:         1,
					AttemptID:     "attempt1",
					CandidateHash: "hash1",
				},
				Identity:        ModelIdentity{Kind: "digest", Value: "v456"},
				ReportedModel:   "gpt-3.5",
				Status:          StatusRefused,
				TransportStatus: TransportStatusOK,
			},
			expected:  expected,
			wantError: true,
			wantErrors: []error{ErrEnvelopeMismatch, ErrIdentityMismatch, ErrStatusNotComplete},
		},
		{
			name: "unknown identity matches unknown identity",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope:        envelope,
				Identity:        NewUnknownIdentity(),
				ReportedModel:   reportedModel,
				Status:          StatusComplete,
				TransportStatus: TransportStatusOK,
			},
			expected: Expected{
				Identity:      NewUnknownIdentity(),
				ReportedModel: reportedModel,
			},
			wantError: false,
		},
		{
			name: "unknown identity with different reported model",
			req: CompleteRequest{
				Envelope: envelope,
				Model:    "gpt-4",
				Messages: []Message{},
				Deadline: now,
			},
			res: CompleteResult{
				Envelope:        envelope,
				Identity:        NewUnknownIdentity(),
				ReportedModel:   "different-model",
				Status:          StatusComplete,
				TransportStatus: TransportStatusOK,
			},
			expected: Expected{
				Identity:      NewUnknownIdentity(),
				ReportedModel: reportedModel,
			},
			wantError: true,
			wantErrors: []error{ErrIdentityMismatch},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckResult(tt.req, tt.res, tt.expected)
			if (err != nil) != tt.wantError {
				t.Errorf("CheckResult() error = %v, wantError %v", err, tt.wantError)
				return
			}

			if tt.wantError {
				// Check that all expected errors are present
				for _, wantErr := range tt.wantErrors {
					if !errors.Is(err, wantErr) {
						t.Errorf("CheckResult() error %v does not contain %v", err, wantErr)
					}
				}
			}
		})
	}
}
