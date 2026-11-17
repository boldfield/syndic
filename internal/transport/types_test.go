package transport

import (
	"testing"
)

func TestNewDigestIdentity(t *testing.T) {
	tests := []struct {
		name           string
		value          string
		requestedModel string
		wantError      bool
		wantKind       string
		wantValue      string
	}{
		{
			name:           "valid digest",
			value:          "sha256:abc123",
			requestedModel: "gpt-4",
			wantError:      false,
			wantKind:       "digest",
			wantValue:      "sha256:abc123",
		},
		{
			name:           "empty value",
			value:          "",
			requestedModel: "gpt-4",
			wantError:      true,
		},
		{
			name:           "value equals requested model",
			value:          "gpt-4",
			requestedModel: "gpt-4",
			wantError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewDigestIdentity(tt.value, tt.requestedModel)
			if (err != nil) != tt.wantError {
				t.Errorf("NewDigestIdentity() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if !tt.wantError {
				if got.Kind != tt.wantKind {
					t.Errorf("NewDigestIdentity() Kind = %v, want %v", got.Kind, tt.wantKind)
				}
				if got.Value != tt.wantValue {
					t.Errorf("NewDigestIdentity() Value = %v, want %v", got.Value, tt.wantValue)
				}
			}
		})
	}
}

func TestNewRevisionIdentity(t *testing.T) {
	tests := []struct {
		name           string
		value          string
		requestedModel string
		wantError      bool
		wantKind       string
		wantValue      string
	}{
		{
			name:           "valid revision",
			value:          "v1.2.3",
			requestedModel: "gpt-4",
			wantError:      false,
			wantKind:       "revision",
			wantValue:      "v1.2.3",
		},
		{
			name:           "empty value",
			value:          "",
			requestedModel: "gpt-4",
			wantError:      true,
		},
		{
			name:           "value equals requested model",
			value:          "gpt-4",
			requestedModel: "gpt-4",
			wantError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewRevisionIdentity(tt.value, tt.requestedModel)
			if (err != nil) != tt.wantError {
				t.Errorf("NewRevisionIdentity() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if !tt.wantError {
				if got.Kind != tt.wantKind {
					t.Errorf("NewRevisionIdentity() Kind = %v, want %v", got.Kind, tt.wantKind)
				}
				if got.Value != tt.wantValue {
					t.Errorf("NewRevisionIdentity() Value = %v, want %v", got.Value, tt.wantValue)
				}
			}
		})
	}
}

func TestNewUnknownIdentity(t *testing.T) {
	got := NewUnknownIdentity()
	if got.Kind != "unknown" {
		t.Errorf("NewUnknownIdentity() Kind = %v, want unknown", got.Kind)
	}
	if got.Value != "" {
		t.Errorf("NewUnknownIdentity() Value = %v, want empty string", got.Value)
	}
}

func TestModelIdentityEquality(t *testing.T) {
	id1 := ModelIdentity{Kind: "digest", Value: "abc123"}
	id2 := ModelIdentity{Kind: "digest", Value: "abc123"}
	id3 := ModelIdentity{Kind: "digest", Value: "xyz789"}

	if id1 != id2 {
		t.Errorf("ModelIdentity equality: id1 != id2")
	}
	if id1 == id3 {
		t.Errorf("ModelIdentity equality: id1 == id3 (should not be equal)")
	}

	unknown1 := NewUnknownIdentity()
	unknown2 := NewUnknownIdentity()
	if unknown1 != unknown2 {
		t.Errorf("ModelIdentity equality: unknown1 != unknown2")
	}
}

func TestAttemptEnvelopeEquality(t *testing.T) {
	env1 := AttemptEnvelope{
		Participant:   "participant1",
		TurnID:        "turn1",
		Phase:         "phase1",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "hash1",
	}
	env2 := AttemptEnvelope{
		Participant:   "participant1",
		TurnID:        "turn1",
		Phase:         "phase1",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "hash1",
	}
	env3 := AttemptEnvelope{
		Participant:   "participant2",
		TurnID:        "turn1",
		Phase:         "phase1",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "hash1",
	}

	if env1 != env2 {
		t.Errorf("AttemptEnvelope equality: env1 != env2")
	}
	if env1 == env3 {
		t.Errorf("AttemptEnvelope equality: env1 == env3 (should not be equal)")
	}
}
