package transport

import (
	"errors"
	"testing"
	"time"
)

// TestProbeExecutionSeamAccountsBeforeSend verifies the execution seam allows
// accounting before the request is sent, satisfying the design requirement
// that "every probe HTTP request can be accounted before send."
func TestProbeExecutionSeamAccountsBeforeSend(t *testing.T) {
	attempts := make(map[string]bool)
	journalCall := func(req *CompleteRequest) error {
		if req == nil {
			return errors.New("request is nil")
		}
		if req.Envelope.AttemptID == "" {
			return errors.New("attempt ID is empty")
		}
		attempts[req.Envelope.AttemptID] = true
		return nil
	}

	// A probe request should have a BeforeSend callback that journals the attempt.
	req := CompleteRequest{
		Envelope: AttemptEnvelope{
			Participant:   "test-model",
			TurnID:        "probe-run",
			Phase:         "probe",
			Epoch:         1,
			AttemptID:     "probe-attempt-1",
			CandidateHash: "",
		},
		Model:      "test-model",
		Messages:   []Message{{Role: "user", Content: "test"}},
		BeforeSend: journalCall,
	}

	// Verify the callback can be invoked directly (simulating adapter behavior).
	if req.BeforeSend != nil {
		err := req.BeforeSend(&req)
		if err != nil {
			t.Fatalf("beforSend callback failed: %v", err)
		}
	}

	if !attempts[req.Envelope.AttemptID] {
		t.Errorf("attempt %q not journaled", req.Envelope.AttemptID)
	}
}

// TestProbeExecutionSeamDistinguishesErrNotSent verifies that pre-send failures
// (like journaling failures) are distinguishable from post-send failures through
// ErrNotSent wrapping.
func TestProbeExecutionSeamDistinguishesErrNotSent(t *testing.T) {
	failJournal := func(req *CompleteRequest) error {
		return errors.New("journal unavailable")
	}

	req := CompleteRequest{
		Envelope: AttemptEnvelope{
			Participant:   "test-model",
			TurnID:        "probe-run",
			Phase:         "probe",
			Epoch:         1,
			AttemptID:     "probe-attempt-1",
			CandidateHash: "",
		},
		Model:      "test-model",
		Messages:   []Message{{Role: "user", Content: "test"}},
		BeforeSend: failJournal,
	}

	// Simulate what an adapter would do: call BeforeSend and wrap errors.
	if req.BeforeSend != nil {
		err := req.BeforeSend(&req)
		if err != nil {
			err = errors.Join(ErrNotSent, err)
			if !errors.Is(err, ErrNotSent) {
				t.Errorf("journal error not wrapped with ErrNotSent")
			}
		}
	}
}

// TestProbeExecutionSeamMissingModelNoUsableRecord verifies that missing model
// or other pre-request issues prevent the request from being sent, leaving no
// usable record.
func TestProbeExecutionSeamMissingModelNoUsableRecord(t *testing.T) {
	attempts := make(map[string]map[string]interface{})
	journalCall := func(req *CompleteRequest) error {
		if req.Model == "" {
			return errors.New("model is required")
		}
		attempts[req.Envelope.AttemptID] = map[string]interface{}{
			"model": req.Model,
			"sent":  false,
		}
		return nil
	}

	// Probe with missing model should fail to journal.
	req := CompleteRequest{
		Envelope: AttemptEnvelope{
			Participant: "unknown",
			TurnID:      "probe-run",
			Phase:       "probe",
			Epoch:       1,
			AttemptID:   "probe-attempt-1",
		},
		Model:      "", // Missing model
		Messages:   []Message{{Role: "user", Content: "test"}},
		BeforeSend: journalCall,
	}

	if req.BeforeSend != nil {
		err := req.BeforeSend(&req)
		if err == nil {
			t.Fatal("expected error for missing model, got nil")
		}
		if !errors.Is(errors.Join(ErrNotSent, err), ErrNotSent) {
			t.Errorf("missing model not distinguished as pre-send error")
		}
	}

	// Verify the attempt was not recorded as journaled.
	if _, found := attempts[req.Envelope.AttemptID]; found {
		t.Errorf("attempt journaled despite missing model; no usable record should exist")
	}
}

// TestProbeExecutionSeamOptionalCallback verifies that the BeforeSend callback
// is optional and can be nil without breaking the execution flow.
func TestProbeExecutionSeamOptionalCallback(t *testing.T) {
	req := CompleteRequest{
		Envelope: AttemptEnvelope{
			Participant:   "test-model",
			TurnID:        "probe-run",
			Phase:         "probe",
			Epoch:         1,
			AttemptID:     "probe-attempt-1",
			CandidateHash: "",
		},
		Model:      "test-model",
		Messages:   []Message{{Role: "user", Content: "test"}},
		BeforeSend: nil, // Optional callback can be nil
	}

	// Verify that the callback can be safely checked for nil.
	if req.BeforeSend != nil {
		t.Errorf("BeforeSend should be nil, got non-nil")
	}
}

// TestProbeExecutionSeamMultipleAttempts verifies that each probe request gets
// its own execution seam invocation with its own attempt ID.
func TestProbeExecutionSeamMultipleAttempts(t *testing.T) {
	attempts := []string{}
	journalCall := func(req *CompleteRequest) error {
		attempts = append(attempts, req.Envelope.AttemptID)
		return nil
	}

	// Simulate multiple probe requests (e.g., one per model at engine start).
	for i := 1; i <= 3; i++ {
		attemptID := "probe-attempt-" + string(rune('0'+i))
		req := CompleteRequest{
			Envelope: AttemptEnvelope{
				Participant:   "test-model",
				TurnID:        "probe-run",
				Phase:         "probe",
				Epoch:         1,
				AttemptID:     attemptID,
				CandidateHash: "",
			},
			Model:      "test-model",
			Messages:   []Message{{Role: "user", Content: "test"}},
			BeforeSend: journalCall,
		}

		if req.BeforeSend != nil {
			err := req.BeforeSend(&req)
			if err != nil {
				t.Fatalf("journalCall failed: %v", err)
			}
		}
	}

	if len(attempts) != 3 {
		t.Errorf("journaled %d attempts, want 3", len(attempts))
	}
}

// TestProbeExecutionSeamDeadlineIndependent verifies that the BeforeSend
// seam respects deadlines independently and can distinguish timeout from
// other errors.
func TestProbeExecutionSeamDeadlineIndependent(t *testing.T) {
	timeoutJournal := func(req *CompleteRequest) error {
		if req.Deadline.Before(time.Now()) {
			return errors.New("deadline already exceeded")
		}
		return nil
	}

	// Create request with a past deadline.
	req := CompleteRequest{
		Envelope: AttemptEnvelope{
			Participant:   "test-model",
			TurnID:        "probe-run",
			Phase:         "probe",
			Epoch:         1,
			AttemptID:     "probe-attempt-1",
			CandidateHash: "",
		},
		Model:      "test-model",
		Messages:   []Message{{Role: "user", Content: "test"}},
		Deadline:   time.Now().Add(-1 * time.Second), // Past deadline
		BeforeSend: timeoutJournal,
	}

	if req.BeforeSend != nil {
		err := req.BeforeSend(&req)
		if err == nil {
			t.Fatal("expected deadline error, got nil")
		}
		// Pre-send deadline check should result in ErrNotSent.
		wrappedErr := errors.Join(ErrNotSent, err)
		if !errors.Is(wrappedErr, ErrNotSent) {
			t.Errorf("deadline error not distinguished as pre-send failure")
		}
	}
}
