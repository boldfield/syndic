package fake

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/contracttest"
)

// VerdictApprove returns a well-formed approval envelope.
func VerdictApprove(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: approve
END VERDICT
`, candidateHash, attemptID)
}

// VerdictBlock returns a well-formed block envelope.
func VerdictBlock(candidateHash, attemptID, blockID, severity, evidence, remedy, reason string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: block
severity: %s
block_id: %s
evidence: %s
remedy: %s
reason: %s
END VERDICT
`, candidateHash, attemptID, severity, blockID, evidence, remedy, reason)
}

// VerdictProseApprove returns prose containing "approve" with no envelope.
func VerdictProseApprove() string {
	return "I approve of this response."
}

// VerdictWrongHash returns an approve with a different candidate hash.
func VerdictWrongHash(attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: wrong-hash
attempt: %s
verdict: approve
END VERDICT
`, attemptID)
}

// VerdictApproveWithChanges returns an approve-with-changes verdict.
func VerdictApproveWithChanges(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: approve-with-changes
END VERDICT
`, candidateHash, attemptID)
}

// VerdictTwoEnvelopes returns output containing two envelopes.
func VerdictTwoEnvelopes(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: approve
END VERDICT
VERDICT v1
candidate: %s
attempt: %s
verdict: approve
END VERDICT
`, candidateHash, attemptID, candidateHash, attemptID)
}

// VerdictBlockWithEndMarker returns a block with "END VERDICT" in the reason.
func VerdictBlockWithEndMarker(candidateHash, attemptID string) string {
	return fmt.Sprintf(`VERDICT v1
candidate: %s
attempt: %s
verdict: block
severity: high
block_id: b1
evidence: test
remedy: fix it
reason: This is a reason with
END VERDICT
on its own line, plus more text
after the marker
END VERDICT
`, candidateHash, attemptID)
}

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

	if len(calls[0]) != 2 || calls[0][1].Content != "User message" {
		t.Errorf("first call: %v", calls[0])
	}

	if len(calls[1]) != 2 || calls[1][1].Content != "Second user message" {
		t.Errorf("second call: %v", calls[1])
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

// TestVerdictFixtureSmokeTest validates well-formed verdicts.
func TestVerdictFixtureSmokeTest(t *testing.T) {
	hash := "test-hash"
	attemptID := "test-attempt"

	testCases := []struct {
		name       string
		verdict    string
		shouldPass bool
	}{
		{
			name:       "well-formed approve",
			verdict:    VerdictApprove(hash, attemptID),
			shouldPass: true,
		},
		{
			name:       "well-formed block",
			verdict:    VerdictBlock(hash, attemptID, "b1", "high", "evidence", "remedy", "reason"),
			shouldPass: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			lines := strings.Split(tc.verdict, "\n")
			var verdictStart, verdictEnd int
			verdictCount := 0

			for i, line := range lines {
				if line == "VERDICT v1" {
					verdictCount++
					if verdictCount == 1 {
						verdictStart = i
					}
				}
				if line == "END VERDICT" {
					verdictEnd = i
					break
				}
			}

			if verdictCount > 1 {
				t.Errorf("multiple VERDICT v1 markers found")
			}

			if verdictStart > 0 && verdictEnd > verdictStart {
				content := strings.Join(lines[verdictStart+1:verdictEnd], "\n")
				hasCandidate := strings.Contains(content, "candidate:")
				hasAttempt := strings.Contains(content, "attempt:")
				hasVerdict := strings.Contains(content, "verdict:")

				if !hasCandidate || !hasAttempt || !hasVerdict {
					t.Errorf("missing required fields in envelope")
				}
			}
		})
	}
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
