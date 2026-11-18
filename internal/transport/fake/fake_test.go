package fake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/contracttest"
)

// Backend provides fixtures for contract tests.
type Backend struct {
	secret string
}

// NewBackend creates a new test backend.
func NewBackend() *Backend {
	return &Backend{secret: "test-secret-value"}
}

// Setup returns a fixture ready to test the given case.
func (b *Backend) Setup(t *testing.T, cse contracttest.Case) contracttest.Fixture {
	secret := b.secret
	expectedIdentity := transport.NewUnknownIdentity()
	var adapter *Adapter
	var primaryHits func() int
	var foreignHits func() int

	switch cse {
	case contracttest.Success:
		script := []Step{
			{
				Kind:               StepKindReply,
				Text:               "Success response",
				Status:             transport.StatusComplete,
				NativeFinishReason: "stop",
				TransportStatus:    transport.TransportStatusOK,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.DeadlineExceeded:
		script := []Step{
			{
				Kind:  StepKindHang,
				Delay: 100 * time.Millisecond,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.MalformedBody:
		script := []Step{
			{
				Kind:            StepKindReply,
				RawOutput:       "Malformed response",
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusMalformedBody,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.ConnectionReset:
		script := []Step{
			{
				Kind:            StepKindReply,
				Status:          transport.StatusInterrupted,
				TransportStatus: transport.TransportStatusReset,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.Truncated:
		script := []Step{
			{
				Kind:               StepKindReply,
				Text:               "Truncated response",
				Status:             transport.StatusTruncated,
				NativeFinishReason: "length",
				TransportStatus:    transport.TransportStatusOK,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.Refused:
		script := []Step{
			{
				Kind:               StepKindReply,
				Text:               "Content refused",
				Status:             transport.StatusRefused,
				NativeFinishReason: "content_filter",
				TransportStatus:    transport.TransportStatusOK,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.AuthFailure:
		script := []Step{
			{
				Kind:            StepKindReply,
				Status:          transport.StatusError,
				TransportStatus: transport.TransportStatusUnauthorized,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "",
		})

	case contracttest.ServerError:
		script := []Step{
			{
				Kind:            StepKindReply,
				Status:          transport.StatusError,
				TransportStatus: "http_500",
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "",
		})
		primaryHits = func() int { return 1 }
		foreignHits = func() int { return 0 }

	case contracttest.IdentityMismatch:
		wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
		script := []Step{
			{
				Kind:               StepKindReply,
				Text:               "Response",
				Status:             transport.StatusComplete,
				NativeFinishReason: "stop",
				TransportStatus:    transport.TransportStatusOK,
				Identity:           wrongIdentity,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
		})

	case contracttest.RedirectOtherHost:
		script := []Step{
			{
				Kind:            StepKindReply,
				Status:          transport.StatusError,
				TransportStatus: "http_301",
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "",
		})
		primaryHits = func() int { return 0 }
		foreignHits = func() int { return 0 }

	case contracttest.SecretEcho:
		wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
		script := []Step{
			{
				Kind:               StepKindEcho,
				EchoedString:       secret,
				Status:             transport.StatusComplete,
				NativeFinishReason: "stop",
				TransportStatus:    transport.TransportStatusOK,
				Identity:           wrongIdentity,
			},
		}
		adapter = New(script, Options{
			DefaultIdentity:      expectedIdentity,
			DefaultReportedModel: "contract-model",
			ResolvedSecrets:      []string{secret},
		})

	default:
		t.Fatalf("unknown case: %q", cse)
	}

	if primaryHits == nil {
		primaryHits = func() int { return 0 }
	}
	if foreignHits == nil {
		foreignHits = func() int { return 0 }
	}

	return contracttest.Fixture{
		Adapter:     adapter,
		Secret:      secret,
		Expected:    transport.Expected{Identity: expectedIdentity, ReportedModel: "contract-model"},
		PrimaryHits: primaryHits,
		ForeignHits: foreignHits,
	}
}

// TestContractTestSuite runs the contract test suite against the fake backend.
func TestContractTestSuite(t *testing.T) {
	backend := NewBackend()
	contracttest.Run(t, backend)
}

// TestScriptExhausted tests behavior when script is exhausted.
func TestScriptExhausted(t *testing.T) {
	script := []Step{
		{
			Kind:            StepKindReply,
			Text:            "First response",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test-turn",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test-attempt",
			CandidateHash: "test-hash",
		},
		Model:           "test-model",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// First call should work
	result1, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if result1.Status != transport.StatusComplete {
		t.Errorf("first result status: %q, want complete", result1.Status)
	}

	// Second call should return error status
	result2, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if result2.Status != transport.StatusError {
		t.Errorf("second result status: %q, want error", result2.Status)
	}
	if result2.TransportStatus != "script_exhausted" {
		t.Errorf("second transport status: %q, want script_exhausted", result2.TransportStatus)
	}
}

// TestAllStepKinds tests all step kinds in a single script.
func TestAllStepKinds(t *testing.T) {
	script := []Step{
		{
			Kind:               StepKindReply,
			Text:               "First reply",
			Status:             transport.StatusComplete,
			NativeFinishReason: "stop",
			TransportStatus:    transport.TransportStatusOK,
		},
		{
			Kind:  StepKindHang,
			Delay: 10 * time.Millisecond,
		},
		{
			Kind:            StepKindEcho,
			EchoedString:    "echo-value",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{})

	baseReq := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(5 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test reply
	result1, err := adapter.Complete(ctx, baseReq)
	if err != nil {
		t.Fatalf("reply step: %v", err)
	}
	if result1.Status != transport.StatusComplete || result1.Text != "First reply" {
		t.Errorf("reply result: status=%q text=%q, want complete First reply", result1.Status, result1.Text)
	}

	// Test hang
	req2 := baseReq
	req2.Deadline = time.Now().Add(100 * time.Millisecond)
	ctx2, cancel2 := context.WithDeadline(context.Background(), req2.Deadline)
	defer cancel2()

	result2, err := adapter.Complete(ctx2, req2)
	if err != nil {
		t.Fatalf("hang step: %v", err)
	}
	if result2.Status != transport.StatusInterrupted || result2.TransportStatus != transport.TransportStatusTimeout {
		t.Errorf("hang result: status=%q transport=%q, want interrupted timeout",
			result2.Status, result2.TransportStatus)
	}

	// Test echo
	result3, err := adapter.Complete(ctx, baseReq)
	if err != nil {
		t.Fatalf("echo step: %v", err)
	}
	if result3.Status != transport.StatusComplete {
		t.Errorf("echo result status: %q, want complete", result3.Status)
	}
	if !strings.Contains(result3.Text, "echo-value") {
		t.Errorf("echo result text does not contain echo-value: %q", result3.Text)
	}
}

// TestHangWithCancel tests hang behavior when context is cancelled.
func TestHangWithCancel(t *testing.T) {
	script := []Step{
		{
			Kind:  StepKindHang,
			Delay: 1 * time.Second,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(5 * time.Second),
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("hang with cancel: %v", err)
	}
	if result.Status != transport.StatusInterrupted || result.TransportStatus != transport.TransportStatusCancelled {
		t.Errorf("result: status=%q transport=%q, want interrupted cancelled",
			result.Status, result.TransportStatus)
	}
}

// TestEchoWithSecrets tests echo step with secret scrubbing.
func TestEchoWithSecrets(t *testing.T) {
	secret := "my-secret-token"
	script := []Step{
		{
			Kind:            StepKindEcho,
			EchoedString:    secret,
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{ResolvedSecrets: []string{secret}})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("echo with secret: %v", err)
	}

	if result.ScrubCount != 1 {
		t.Errorf("scrub count: %d, want 1", result.ScrubCount)
	}
	if strings.Contains(result.Text, secret) {
		t.Errorf("text contains secret: %q", result.Text)
	}
	if !strings.Contains(result.Text, transport.ScrubMarker) {
		t.Errorf("text does not contain scrub marker")
	}
}

// TestEchoWithoutSecrets tests echo step when echoed string is not a secret.
func TestEchoWithoutSecrets(t *testing.T) {
	secret := "my-secret-token"
	script := []Step{
		{
			Kind:            StepKindEcho,
			EchoedString:    "public-value",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{ResolvedSecrets: []string{secret}})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("echo without secret: %v", err)
	}

	if result.ScrubCount != 0 {
		t.Errorf("scrub count: %d, want 0", result.ScrubCount)
	}
	if !strings.Contains(result.Text, "public-value") {
		t.Errorf("text does not contain public-value: %q", result.Text)
	}
}

// TestCalls tests the Calls accessor.
func TestCalls(t *testing.T) {
	script := []Step{
		{
			Kind:            StepKindReply,
			Text:            "Response 1",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
		{
			Kind:            StepKindReply,
			Text:            "Response 2",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{})

	baseReq := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model: "test",
		Messages: []transport.Message{
			{Role: "system", Content: "System message"},
			{Role: "user", Content: "User message"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// First call
	_, err := adapter.Complete(ctx, baseReq)
	if err != nil {
		t.Fatalf("first complete: %v", err)
	}

	// Modify messages for second call
	req2 := baseReq
	req2.Messages = []transport.Message{
		{Role: "system", Content: "System message"},
		{Role: "user", Content: "Second user message"},
	}

	_, err = adapter.Complete(ctx, req2)
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}

	// Check Calls()
	calls := adapter.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls count: %d, want 2", len(calls))
	}

	if len(calls[0].Request.Messages) != 2 || calls[0].Request.Messages[1].Content != "User message" {
		t.Errorf("first call: %v", calls[0].Request.Messages)
	}

	if len(calls[1].Request.Messages) != 2 || calls[1].Request.Messages[1].Content != "Second user message" {
		t.Errorf("second call: %v", calls[1].Request.Messages)
	}
}

// TestVerdictFixtures tests the verdict helper functions.
func TestVerdictFixtures(t *testing.T) {
	hash := "test-candidate-hash"
	attemptID := "test-attempt-id"

	tests := []struct {
		name               string
		verdict            string
		hasVerdictEnvelope bool
	}{
		{
			name:               "approve",
			verdict:            VerdictApprove(hash, attemptID),
			hasVerdictEnvelope: true,
		},
		{
			name:               "block",
			verdict:            VerdictBlock(hash, attemptID, "b1", "high", "evidence", "remedy", "reason"),
			hasVerdictEnvelope: true,
		},
		{
			name:               "prose approve",
			verdict:            VerdictProseApprove(),
			hasVerdictEnvelope: false,
		},
		{
			name:               "approve with changes",
			verdict:            VerdictApproveWithChanges(hash, attemptID),
			hasVerdictEnvelope: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hasVerdictEnvelope {
				if !strings.Contains(tt.verdict, "VERDICT v1") {
					t.Errorf("missing VERDICT v1 marker")
				}
				if !strings.Contains(tt.verdict, "END VERDICT") {
					t.Errorf("missing END VERDICT marker")
				}
			}
		})
	}
}

// TestVerdictWrongHashDifferent verifies VerdictWrongHash returns a different hash.
func TestVerdictWrongHashDifferent(t *testing.T) {
	hash := "test-hash"
	attemptID := "test-attempt"
	verdict := VerdictWrongHash(hash, attemptID)

	if strings.Contains(verdict, "candidate: test-hash\n") {
		t.Errorf("VerdictWrongHash returned the same hash as input")
	}
	if !strings.Contains(verdict, "candidate: test-hash-stale\n") {
		t.Errorf("VerdictWrongHash did not return expected different hash")
	}

	// Also test with a hash that is "wrong-hash"
	wrongHash := "wrong-hash"
	verdict2 := VerdictWrongHash(wrongHash, attemptID)
	if strings.Contains(verdict2, "candidate: wrong-hash\n") {
		t.Errorf("VerdictWrongHash hardcoded wrong-hash when input is 'wrong-hash'")
	}
	if !strings.Contains(verdict2, "candidate: wrong-hash-stale\n") {
		t.Errorf("VerdictWrongHash did not properly derive different hash from 'wrong-hash'")
	}
}

// TestVerdictFixtureSmokeTest validates well-formed verdicts per delta §6.
func TestVerdictFixtureSmokeTest(t *testing.T) {
	hash := "test-hash"
	attemptID := "test-attempt"

	testCases := []struct {
		name           string
		verdict        string
		requiredFields []string
	}{
		{
			name:           "well-formed approve",
			verdict:        VerdictApprove(hash, attemptID),
			requiredFields: []string{"candidate", "attempt", "verdict", "reason"},
		},
		{
			name:           "well-formed block",
			verdict:        VerdictBlock(hash, attemptID, "b1", "high", "evidence", "remedy", "reason"),
			requiredFields: []string{"candidate", "attempt", "verdict", "reason", "severity", "block_id", "evidence", "remedy"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(tc.verdict, "\n")
			var verdictStart, verdictEnd int

			// Find exact VERDICT v1 line
			for i, line := range lines {
				if line == "VERDICT v1" {
					verdictStart = i
					break
				}
			}

			// Find exact END VERDICT line after VERDICT v1
			found := false
			for i := verdictStart + 1; i < len(lines); i++ {
				if lines[i] == "END VERDICT" {
					verdictEnd = i
					found = true
					break
				}
			}

			if verdictStart < 0 || !found {
				t.Fatal("no exact VERDICT v1 or END VERDICT line found")
			}

			// Check each required field appears exactly once
			for _, field := range tc.requiredFields {
				count := 0
				for i := verdictStart + 1; i < verdictEnd; i++ {
					if strings.HasPrefix(lines[i], field+":") {
						count++
					}
				}
				if count != 1 {
					t.Errorf("field %q appears %d times, expected exactly 1", field, count)
				}
			}
		})
	}

	// Test VerdictBlockWithEndMarker has exact END VERDICT markers
	t.Run("block with end marker", func(t *testing.T) {
		verdict := VerdictBlockWithEndMarker(hash, attemptID)
		lines := strings.Split(verdict, "\n")

		var firstEnd, secondEnd int
		foundVerdictStart := false
		foundFirstEnd := false

		for i, line := range lines {
			if line == "VERDICT v1" && !foundVerdictStart {
				foundVerdictStart = true
			} else if line == "END VERDICT" && foundVerdictStart && !foundFirstEnd {
				firstEnd = i
				foundFirstEnd = true
			} else if line == "END VERDICT" && foundFirstEnd {
				secondEnd = i
				break
			}
		}

		if !foundVerdictStart || !foundFirstEnd || secondEnd == 0 {
			t.Fatal("block with end marker does not have exactly two END VERDICT markers")
		}

		// Verify text exists between the two END VERDICT markers
		if firstEnd+1 >= secondEnd {
			t.Error("no text between first and second END VERDICT markers")
		}
	})
}

// TestRaceCondition tests concurrent access safety.
func TestRaceCondition(t *testing.T) {
	script := []Step{
		{
			Kind:            StepKindReply,
			Text:            "Response",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// This test should pass with -race flag
	_, _ = adapter.Complete(ctx, req)
	calls := adapter.Calls()
	if len(calls) != 1 {
		t.Errorf("calls: %d, want 1", len(calls))
	}
}

// TestHangWithoutCancel verifies hang blocks until deadline.
func TestHangWithoutCancel(t *testing.T) {
	script := []Step{
		{
			Kind: StepKindHang,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(100 * time.Millisecond),
	}

	ctx, cancel := context.WithDeadline(context.Background(), req.Deadline)
	defer cancel()

	start := time.Now()
	result, err := adapter.Complete(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("hang without cancel: %v", err)
	}
	if result.Status != transport.StatusInterrupted {
		t.Errorf("status: %q, want interrupted", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusTimeout {
		t.Errorf("transport status: %q, want timeout", result.TransportStatus)
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("elapsed %v, want ~100ms", elapsed)
	}
}

// TestHangWithBackgroundContextRespectReqDeadline verifies hang respects req.Deadline even with context.Background().
func TestHangWithBackgroundContextRespectReqDeadline(t *testing.T) {
	script := []Step{
		{
			Kind: StepKindHang,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(100 * time.Millisecond),
	}

	start := time.Now()
	result, err := adapter.Complete(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("hang with background context: %v", err)
	}
	if result.Status != transport.StatusInterrupted {
		t.Errorf("status: %q, want interrupted", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusTimeout {
		t.Errorf("transport status: %q, want timeout", result.TransportStatus)
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("elapsed %v, want ~100ms", elapsed)
	}
}

// TestHangWithCancelDriven verifies Cancel unblocks a hanging attempt.
func TestHangWithCancelDriven(t *testing.T) {
	script := []Step{
		{
			Kind: StepKindHang,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "a1",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(10 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan bool)
	go func() {
		result, err := adapter.Complete(ctx, req)
		if err != nil {
			t.Errorf("hang with cancel: %v", err)
		}
		if result.Status != transport.StatusInterrupted {
			t.Errorf("status: %q, want interrupted", result.Status)
		}
		if result.TransportStatus != transport.TransportStatusCancelled {
			t.Errorf("transport status: %q, want cancelled", result.TransportStatus)
		}
		done <- true
	}()

	time.Sleep(50 * time.Millisecond)
	_, err := adapter.Cancel(context.Background(), "a1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	<-done
}

// TestCancelDefault verifies Cancel default is ServerConfirmed = yes.
func TestCancelDefault(t *testing.T) {
	script := []Step{}
	adapter := New(script, Options{CancelUnconfirmed: false})

	status, err := adapter.Cancel(context.Background(), "test-id")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if status.ServerConfirmed != transport.TriYes {
		t.Errorf("ServerConfirmed: %q, want yes (CancelUnconfirmed: false)", status.ServerConfirmed)
	}

	adapter2 := New(script, Options{CancelUnconfirmed: true})
	status2, err := adapter2.Cancel(context.Background(), "test-id")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if status2.ServerConfirmed != transport.TriUnknown {
		t.Errorf("ServerConfirmed: %q, want unknown (CancelUnconfirmed: true)", status2.ServerConfirmed)
	}

	adapter3 := New(script, Options{})
	status3, err := adapter3.Cancel(context.Background(), "test-id")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if status3.ServerConfirmed != transport.TriYes {
		t.Errorf("ServerConfirmed: %q, want yes (default)", status3.ServerConfirmed)
	}
}

// TestReplyWithDelay verifies reply steps respect Delay.
func TestReplyWithDelay(t *testing.T) {
	script := []Step{
		{
			Kind:            StepKindReply,
			Text:            "Reply with delay",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
			Delay:           50 * time.Millisecond,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	result, err := adapter.Complete(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("reply with delay: %v", err)
	}
	if result.Text != "Reply with delay" {
		t.Errorf("text: %q, want 'Reply with delay'", result.Text)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("elapsed %v, want ~50ms", elapsed)
	}
}

// TestEchoWithDelay verifies echo steps respect Delay.
func TestEchoWithDelay(t *testing.T) {
	script := []Step{
		{
			Kind:            StepKindEcho,
			EchoedString:    "echo-value",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
			Delay:           50 * time.Millisecond,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	result, err := adapter.Complete(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("echo with delay: %v", err)
	}
	if !strings.Contains(result.Text, "echo-value") {
		t.Errorf("text does not contain echo-value: %q", result.Text)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("elapsed %v, want ~50ms", elapsed)
	}
}

// TestStepKindValidation tests unknown step kind handling.
func TestStepKindValidation(t *testing.T) {
	adapter := New([]Step{}, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "test",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result.Status != transport.StatusError {
		t.Errorf("status: %q, want error", result.Status)
	}
	if result.TransportStatus != "script_exhausted" {
		t.Errorf("transport status: %q, want script_exhausted", result.TransportStatus)
	}
}

// TestCallsMutation verifies Calls() returns deep copies that don't affect stored data.
func TestCallsMutation(t *testing.T) {
	script := []Step{
		{
			Kind:            StepKindReply,
			Text:            "Response",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
		},
	}
	adapter := New(script, Options{})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model: "test",
		Messages: []transport.Message{
			{Role: "user", Content: "original message"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _ = adapter.Complete(ctx, req)

	calls := adapter.Calls()
	calls[0].Request.Messages[0].Content = "mutated message"

	calls2 := adapter.Calls()
	if calls2[0].Request.Messages[0].Content != "original message" {
		t.Errorf("mutation affected stored calls: %q", calls2[0].Request.Messages[0].Content)
	}
}
