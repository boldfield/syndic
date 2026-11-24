package fake

import (
	"context"
	"errors"
	"strings"
	"sync"
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
			verdictStart, verdictEnd := -1, -1
			startCount := 0

			// Find exact VERDICT v1 line; exactly one is allowed
			for i, line := range lines {
				if line == "VERDICT v1" {
					startCount++
					if verdictStart < 0 {
						verdictStart = i
					}
				}
			}
			if verdictStart < 0 {
				t.Fatal("no exact VERDICT v1 line found")
			}
			if startCount != 1 {
				t.Fatalf("VERDICT v1 appears %d times, expected exactly 1", startCount)
			}

			// Find exact END VERDICT line after VERDICT v1
			for i := verdictStart + 1; i < len(lines); i++ {
				if lines[i] == "END VERDICT" {
					verdictEnd = i
					break
				}
			}
			if verdictEnd < 0 {
				t.Fatal("no exact END VERDICT line found after VERDICT v1")
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

	hang := New([]Step{
		{Kind: StepKindHang},
		{Kind: StepKindReply, Text: "r1", Status: transport.StatusComplete, TransportStatus: transport.TransportStatusOK},
		{Kind: StepKindReply, Text: "r2", Status: transport.StatusComplete, TransportStatus: transport.TransportStatusOK},
	}, Options{})
	hangReq := req
	hangReq.Envelope.AttemptID = "hang"
	hangReq.Deadline = time.Now().Add(5 * time.Second)

	var wg sync.WaitGroup
	hangDone := make(chan transport.CompleteResult, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, _ := hang.Complete(ctx, hangReq)
		hangDone <- res
	}()

	// Wait until the hanging call has been recorded.
	for i := 0; i < 500 && len(hang.Calls()) == 0; i++ {
		time.Sleep(2 * time.Millisecond)
	}

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := req
			r.Envelope.AttemptID = "other"
			_, _ = hang.Complete(ctx, r)
			_ = hang.Calls()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = hang.Calls()
		_, _ = hang.Cancel(ctx, "hang")
	}()
	wg.Wait()

	res := <-hangDone
	if res.Status != transport.StatusInterrupted || res.TransportStatus != transport.TransportStatusCancelled {
		t.Errorf("hang result: %v/%v, want interrupted/cancelled", res.Status, res.TransportStatus)
	}
	if n := len(hang.Calls()); n != 3 {
		t.Errorf("calls: %d, want 3", n)
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

// TestCancelBeforeHangEndsIt verifies a Cancel that arrives before the hang
// starts still ends it.
func TestCancelBeforeHangEndsIt(t *testing.T) {
	adapter := New([]Step{{Kind: StepKindHang}}, Options{})
	if _, err := adapter.Cancel(context.Background(), "a1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := adapter.Complete(ctx, transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{AttemptID: "a1"},
		Deadline: time.Now().Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result.TransportStatus != transport.TransportStatusCancelled {
		t.Errorf("transport status: %q, want cancelled", result.TransportStatus)
	}
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

// TestStepKindError verifies error step returns given error and records call.
func TestStepKindError(t *testing.T) {
	testErr := errors.New("test error")
	script := []Step{
		{
			Kind: StepKindError,
			Err:  testErr,
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

	result, err := adapter.Complete(ctx, req)

	if !errors.Is(err, testErr) {
		t.Errorf("error: %v, want %v", err, testErr)
	}
	if errors.Is(err, transport.ErrNotSent) {
		t.Errorf("error incorrectly wrapped with ErrNotSent")
	}
	if result.Envelope != req.Envelope {
		t.Errorf("result envelope mismatch")
	}

	calls := adapter.Calls()
	if len(calls) != 1 {
		t.Errorf("calls count: %d, want 1", len(calls))
	}
}

// TestStepKindNotSent verifies not-sent step wraps error with ErrNotSent.
func TestStepKindNotSent(t *testing.T) {
	testErr := errors.New("network error")
	script := []Step{
		{
			Kind: StepKindNotSent,
			Err:  testErr,
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

	result, err := adapter.Complete(ctx, req)

	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("error not wrapped with ErrNotSent: %v", err)
	}
	if !errors.Is(err, testErr) {
		t.Errorf("original error lost: %v", err)
	}
	if result.Envelope != req.Envelope {
		t.Errorf("result envelope mismatch")
	}

	calls := adapter.Calls()
	if len(calls) != 1 {
		t.Errorf("calls count: %d, want 1", len(calls))
	}
}

// TestStepKindNotSentNilError verifies not-sent with nil error returns bare ErrNotSent.
func TestStepKindNotSentNilError(t *testing.T) {
	script := []Step{
		{
			Kind: StepKindNotSent,
			Err:  nil,
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

	result, err := adapter.Complete(ctx, req)

	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("error: %v, want ErrNotSent", err)
	}
	if result.Envelope != req.Envelope {
		t.Errorf("result envelope mismatch")
	}
}

// TestStepKindErrorZeroValue verifies error step returns zero-value result with only Envelope.
func TestStepKindErrorZeroValue(t *testing.T) {
	testErr := errors.New("test error")
	script := []Step{
		{
			Kind: StepKindError,
			Err:  testErr,
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

	result, _ := adapter.Complete(ctx, req)

	if result.RawOutput != "" || result.Text != "" || result.ScrubCount != 0 {
		t.Errorf("result fields not zero: RawOutput=%q, Text=%q, ScrubCount=%d",
			result.RawOutput, result.Text, result.ScrubCount)
	}
	if result.Identity != (transport.ModelIdentity{}) {
		t.Errorf("result.Identity not zero")
	}
	if result.ReportedModel != "" {
		t.Errorf("result.ReportedModel not zero: %q", result.ReportedModel)
	}
	if result.Status != (transport.CompletionStatus("")) {
		t.Errorf("result.Status not zero: %q", result.Status)
	}
	if result.NativeFinish != "" {
		t.Errorf("result.NativeFinish not zero: %q", result.NativeFinish)
	}
	if result.TransportStatus != "" {
		t.Errorf("result.TransportStatus not zero: %q", result.TransportStatus)
	}
	if result.Usage != nil {
		t.Errorf("result.Usage not nil")
	}
}

// TestReplyWithUsageSet verifies reply step returns Usage when set.
func TestReplyWithUsageSet(t *testing.T) {
	usage := &transport.Usage{Prompt: 10, Completion: 5}
	script := []Step{
		{
			Kind:            StepKindReply,
			Text:            "Response",
			Status:          transport.StatusComplete,
			TransportStatus: transport.TransportStatusOK,
			Usage:           usage,
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

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if result.Usage == nil {
		t.Errorf("result.Usage is nil, want %v", usage)
	} else if result.Usage.Prompt != usage.Prompt || result.Usage.Completion != usage.Completion {
		t.Errorf("result.Usage mismatch: %v, want %v", result.Usage, usage)
	}
}

// TestReplyWithoutUsage verifies reply step returns nil Usage when not set.
func TestReplyWithoutUsage(t *testing.T) {
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

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if result.Usage != nil {
		t.Errorf("result.Usage is not nil: %v", result.Usage)
	}
}

// TestBeforeSendCalled verifies BeforeSend callback is invoked before the reply.
func TestBeforeSendCalled(t *testing.T) {
	callCount := 0
	callback := func(req *transport.CompleteRequest) error {
		callCount++
		return nil
	}

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
		BeforeSend:      callback,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if callCount != 1 {
		t.Errorf("BeforeSend call count: %d, want 1", callCount)
	}
	if result.Text != "Response" {
		t.Errorf("result text: %q, want Response", result.Text)
	}
}

// TestBeforeSendError verifies BeforeSend error is wrapped in ErrNotSent.
func TestBeforeSendError(t *testing.T) {
	callErr := errors.New("journaling failed")
	callback := func(req *transport.CompleteRequest) error {
		return callErr
	}

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
		BeforeSend:      callback,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("error not wrapped with ErrNotSent: %v", err)
	}
	if !errors.Is(err, callErr) {
		t.Errorf("original error lost: %v", err)
	}
	if result.TransportStatus != "not_sent" {
		t.Errorf("transport status: %q, want not_sent", result.TransportStatus)
	}
}

// TestBeforeSendNil verifies nil BeforeSend callback is handled gracefully.
func TestBeforeSendNil(t *testing.T) {
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
		BeforeSend:      nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("complete with nil callback: %v", err)
	}
	if result.Text != "Response" {
		t.Errorf("result text: %q, want Response", result.Text)
	}
}

func probeSteps(t *testing.T, a *Adapter, opts transport.ProbeOptions) ([]string, error) {
	t.Helper()
	var steps []string
	opts.BeforeSend = func(r *transport.ProbeRequest) error {
		steps = append(steps, r.Step)
		return nil
	}
	_, err := a.Probe(context.Background(), "ep", "m", opts)
	return steps, err
}

func TestProbeSequenceMatchesRealAdapter(t *testing.T) {
	cal := transport.Calibration{Available: true}
	cases := []struct {
		name string
		cap  transport.CapabilityRecord
		opts transport.ProbeOptions
		want []string
	}{
		{"no calibration data skips over_length", transport.CapabilityRecord{ReportedModel: "m", MaxTokensField: "max_tokens"}, transport.ProbeOptions{AssumedContextLen: 100},
			[]string{"models", "max_tokens", "system_role", "calibration_baseline", "calibration"}},
		{"max_completion_tokens adds a request", transport.CapabilityRecord{ReportedModel: "m", MaxTokensField: "max_completion_tokens", Calibration: cal}, transport.ProbeOptions{AssumedContextLen: 100},
			[]string{"models", "max_tokens", "max_completion_tokens", "system_role", "calibration_baseline", "calibration", "over_length"}},
		{"no context length skips over_length", transport.CapabilityRecord{ReportedModel: "m", MaxTokensField: "max_tokens", Calibration: cal}, transport.ProbeOptions{},
			[]string{"models", "max_tokens", "system_role", "calibration_baseline", "calibration"}},
		{"no working max field sends both and is incomplete", transport.CapabilityRecord{ReportedModel: "m"}, transport.ProbeOptions{AssumedContextLen: 100},
			[]string{"models", "max_tokens", "max_completion_tokens", "system_role", "calibration_baseline", "calibration"}},
		{"calibration disabled skips over_length", transport.CapabilityRecord{ReportedModel: "m", MaxTokensField: "max_tokens", Calibration: cal}, transport.ProbeOptions{DisableCalibration: true, AssumedContextLen: 100},
			[]string{"models", "max_tokens", "system_role"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := probeSteps(t, New(nil, Options{Capability: tc.cap}), tc.opts)
			if tc.cap.MaxTokensField == "" {
				if !errors.Is(err, transport.ErrIncompleteProbe) {
					t.Fatalf("err = %v, want ErrIncompleteProbe", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("steps = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProbeUncertainStepAfterSend(t *testing.T) {
	a := New(nil, Options{Capability: transport.CapabilityRecord{ReportedModel: "m", MaxTokensField: "max_tokens"}, ProbeUncertainStep: "system_role"})
	steps, err := probeSteps(t, a, transport.ProbeOptions{})
	if !errors.Is(err, transport.ErrUncertain) || errors.Is(err, transport.ErrNotSent) {
		t.Fatalf("err = %v, want ErrUncertain only", err)
	}
	if strings.Join(steps, ",") != "models,max_tokens,system_role" {
		t.Fatalf("steps = %v", steps)
	}
}

func TestProbeHookFailureIsNotSent(t *testing.T) {
	a := New(nil, Options{})
	_, err := a.Probe(context.Background(), "ep", "m", transport.ProbeOptions{BeforeSend: func(*transport.ProbeRequest) error { return errors.New("no slot") }})
	if !errors.Is(err, transport.ErrNotSent) || errors.Is(err, transport.ErrUncertain) {
		t.Fatalf("err = %v, want ErrNotSent only", err)
	}
}

func TestProbeMissingModelIsIncomplete(t *testing.T) {
	a := New(nil, Options{Capability: transport.CapabilityRecord{MaxTokensField: "max_tokens"}})
	if _, err := a.Probe(context.Background(), "ep", "m", transport.ProbeOptions{}); !errors.Is(err, transport.ErrIncompleteProbe) {
		t.Fatalf("err = %v, want ErrIncompleteProbe", err)
	}
}

func TestCompleteWithToolCalls(t *testing.T) {
	identity := transport.NewUnknownIdentity()
	envelope := transport.AttemptEnvelope{
		Participant:   "participant",
		TurnID:        "turn1",
		Phase:         "author",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "",
	}
	toolCalls := []transport.ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			Function: transport.ToolCallFunction{
				Name:      "read_file",
				Arguments: `{"path":"/etc/passwd"}`,
			},
		},
		{
			ID:   "call_2",
			Type: "function",
			Function: transport.ToolCallFunction{
				Name:      "web_fetch",
				Arguments: `{"url":"https://example.com"}`,
			},
		},
	}
	script := []Step{
		{
			Kind:               StepKindReply,
			Status:             transport.StatusToolCalls,
			NativeFinishReason: "tool_calls",
			TransportStatus:    transport.TransportStatusOK,
			ToolCalls:          toolCalls,
		},
	}
	adapter := New(script, Options{
		DefaultIdentity:      identity,
		DefaultReportedModel: "test-model",
	})

	req := transport.CompleteRequest{
		Envelope: envelope,
		Model:    "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "read a file"},
		},
		MaxOutputTokens: 100,
		Tools: []transport.Tool{
			{
				Type: "function",
				Function: transport.ToolFunction{
					Name:        "read_file",
					Description: "Read file contents",
					Parameters:  map[string]interface{}{"path": "string"},
				},
			},
		},
		Deadline: time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete with tool calls: %v", err)
	}

	if result.Status != transport.StatusToolCalls {
		t.Errorf("result status: %q, want tool_calls", result.Status)
	}

	if len(result.ToolCalls) != 2 {
		t.Fatalf("result ToolCalls length: %d, want 2", len(result.ToolCalls))
	}

	if result.ToolCalls[0].ID != "call_1" {
		t.Errorf("first call ID: %q, want call_1", result.ToolCalls[0].ID)
	}

	if result.ToolCalls[1].Function.Name != "web_fetch" {
		t.Errorf("second call name: %q, want web_fetch", result.ToolCalls[1].Function.Name)
	}

	if result.NativeFinish != "tool_calls" {
		t.Errorf("native finish: %q, want tool_calls", result.NativeFinish)
	}
}

// TestCompleteWithToolCallsSecretScrubbing verifies tool call arguments are scrubbed.
func TestCompleteWithToolCallsSecretScrubbing(t *testing.T) {
	secret := "my-secret-api-key"
	identity := transport.NewUnknownIdentity()
	envelope := transport.AttemptEnvelope{
		Participant:   "participant",
		TurnID:        "turn1",
		Phase:         "author",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "",
	}
	toolCalls := []transport.ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			Function: transport.ToolCallFunction{
				Name:      "read_file",
				Arguments: `{"path":"/etc/passwd","key":"` + secret + `"}`,
			},
		},
		{
			ID:   "call_2",
			Type: "function",
			Function: transport.ToolCallFunction{
				Name:      "web_fetch",
				Arguments: `{"url":"https://example.com","auth":"` + secret + `"}`,
			},
		},
	}
	script := []Step{
		{
			Kind:               StepKindReply,
			Status:             transport.StatusToolCalls,
			NativeFinishReason: "tool_calls",
			TransportStatus:    transport.TransportStatusOK,
			ToolCalls:          toolCalls,
		},
	}
	adapter := New(script, Options{
		DefaultIdentity:      identity,
		DefaultReportedModel: "test-model",
		ResolvedSecrets:      []string{secret},
	})

	req := transport.CompleteRequest{
		Envelope: envelope,
		Model:    "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "read a file"},
		},
		MaxOutputTokens: 100,
		Tools: []transport.Tool{
			{
				Type: "function",
				Function: transport.ToolFunction{
					Name:        "read_file",
					Description: "Read file contents",
					Parameters:  map[string]interface{}{"path": "string"},
				},
			},
		},
		Deadline: time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete with tool calls: %v", err)
	}

	if result.ScrubCount != 2 {
		t.Errorf("scrub count: %d, want 2 (one per tool call argument)", result.ScrubCount)
	}

	for i, call := range result.ToolCalls {
		if strings.Contains(call.Function.Arguments, secret) {
			t.Errorf("tool call %d arguments contain secret: %q", i, call.Function.Arguments)
		}
		if !strings.Contains(call.Function.Arguments, transport.ScrubMarker) {
			t.Errorf("tool call %d arguments missing scrub marker: %q", i, call.Function.Arguments)
		}
	}
}

// TestToolStepIsClassified verifies that tool steps (StatusToolCalls with ToolCalls) are classified correctly.
func TestToolStepIsClassified(t *testing.T) {
	identity := transport.NewUnknownIdentity()
	envelope := transport.AttemptEnvelope{
		Participant:   "participant",
		TurnID:        "turn1",
		Phase:         "author",
		Epoch:         1,
		AttemptID:     "attempt1",
		CandidateHash: "",
	}
	toolCalls := []transport.ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			Function: transport.ToolCallFunction{
				Name:      "read_file",
				Arguments: `{"path":"/etc/passwd"}`,
			},
		},
	}
	script := []Step{
		{
			Kind:               StepKindReply,
			Status:             transport.StatusToolCalls,
			NativeFinishReason: "tool_calls",
			TransportStatus:    transport.TransportStatusOK,
			ToolCalls:          toolCalls,
		},
	}
	adapter := New(script, Options{
		DefaultIdentity:      identity,
		DefaultReportedModel: "test-model",
	})

	req := transport.CompleteRequest{
		Envelope: envelope,
		Model:    "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "read a file"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete with tool calls: %v", err)
	}

	// Verify it's a tool step
	if !transport.IsToolStep(result) {
		t.Errorf("IsToolStep returned false for a StatusToolCalls result with calls")
	}

	// Verify the gate would pass (CheckResult returns nil for valid tool steps)
	req.Envelope.Participant = "participant"
	expected := transport.Expected{Identity: identity, ReportedModel: "test-model"}
	gate := transport.CheckResult(req, result, expected)
	if gate != nil {
		t.Errorf("CheckResult failed for valid tool step: %v", gate)
	}
}

// TestPlainReplyNilToolCalls verifies that plain replies have nil ToolCalls, not empty slice.
func TestPlainReplyNilToolCalls(t *testing.T) {
	identity := transport.NewUnknownIdentity()
	script := []Step{
		{
			Kind:               StepKindReply,
			Text:               "Plain response",
			Status:             transport.StatusComplete,
			NativeFinishReason: "stop",
			TransportStatus:    transport.TransportStatusOK,
			ToolCalls:          nil,
		},
	}
	adapter := New(script, Options{
		DefaultIdentity:      identity,
		DefaultReportedModel: "test-model",
	})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "participant",
			TurnID:        "turn1",
			Phase:         "author",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "hello"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Plain reply should have nil ToolCalls, not empty slice
	if result.ToolCalls != nil {
		t.Errorf("ToolCalls for plain reply: %v, want nil", result.ToolCalls)
	}
	// IsToolStep should return false for a plain reply
	if transport.IsToolStep(result) {
		t.Errorf("IsToolStep returned true for plain reply with nil ToolCalls")
	}
}

// TestRefereeScriptingAllow verifies referee decision allow is scripted correctly.
func TestRefereeScriptingAllow(t *testing.T) {
	identity := transport.NewUnknownIdentity()
	script := []Step{
		{
			Kind:           StepKindReferee,
			RefereeVerdict: "allow",
			RefereeReason:  "URL matches policy",
		},
	}
	adapter := New(script, Options{
		DefaultIdentity:      identity,
		DefaultReportedModel: "test-model",
	})

	result, err := adapter.Referee(context.Background())
	if err != nil {
		t.Fatalf("Referee: %v", err)
	}

	if result.Verdict != "allow" {
		t.Errorf("Verdict: %q, want %q", result.Verdict, "allow")
	}
	if result.Reason != "URL matches policy" {
		t.Errorf("Reason: %q, want %q", result.Reason, "URL matches policy")
	}
}

// TestRefereeScriptingDeny verifies referee decision deny is scripted correctly.
func TestRefereeScriptingDeny(t *testing.T) {
	identity := transport.NewUnknownIdentity()
	script := []Step{
		{
			Kind:           StepKindReferee,
			RefereeVerdict: "deny",
			RefereeReason:  "URL contains sensitive data",
		},
	}
	adapter := New(script, Options{
		DefaultIdentity:      identity,
		DefaultReportedModel: "test-model",
	})

	result, err := adapter.Referee(context.Background())
	if err != nil {
		t.Fatalf("Referee: %v", err)
	}

	if result.Verdict != "deny" {
		t.Errorf("Verdict: %q, want %q", result.Verdict, "deny")
	}
	if result.Reason != "URL contains sensitive data" {
		t.Errorf("Reason: %q, want %q", result.Reason, "URL contains sensitive data")
	}
}

// TestRefereeFailsClosed verifies every non-verdict shape yields a deny.
func TestRefereeFailsClosed(t *testing.T) {
	reply := func(st transport.CompletionStatus) Step {
		return Step{Kind: StepKindReply, Text: "allow", Status: st, TransportStatus: transport.TransportStatusOK}
	}
	toolReply := reply(transport.StatusToolCalls)
	toolReply.ToolCalls = []transport.ToolCall{{ID: "c"}}
	cases := []struct {
		name   string
		step   Step
		reason string
	}{
		{"refusal", reply(transport.StatusRefused), "refusal"},
		{"truncation", reply(transport.StatusTruncated), "truncation"},
		{"tool call", toolReply, "tool-call reply"},
		{"adapter error", Step{Kind: StepKindError, Err: errors.New("boom")}, "adapter error"},
		{"malformed verdict", Step{Kind: StepKindReferee, RefereeVerdict: "maybe"}, "malformed verdict"},
		{"plain reply", reply(transport.StatusComplete), "unexpected step"},
		{"not sent", Step{Kind: StepKindNotSent}, "unexpected step"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			adapter := New([]Step{c.step}, Options{DefaultIdentity: transport.NewUnknownIdentity(), DefaultReportedModel: "m"})
			res, err := adapter.Referee(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Verdict != "deny" || res.Reason != c.reason {
				t.Fatalf("got %+v, want deny %q", res, c.reason)
			}
		})
	}
	adapter := New(nil, Options{DefaultIdentity: transport.NewUnknownIdentity(), DefaultReportedModel: "m"})
	if res, _ := adapter.Referee(context.Background()); res.Verdict != "deny" {
		t.Fatalf("exhausted script: %+v", res)
	}
}

// TestRequestDeepCopyIsolation verifies recorded requests share no
// tool-bearing slices or maps with the caller or with Calls results.
func TestRequestDeepCopyIsolation(t *testing.T) {
	adapter := New([]Step{{Kind: StepKindReply, Text: "ok", Status: transport.StatusComplete, TransportStatus: transport.TransportStatusOK}},
		Options{DefaultIdentity: transport.NewUnknownIdentity(), DefaultReportedModel: "m"})
	req := transport.CompleteRequest{
		Messages: []transport.Message{{Role: "assistant", ToolCalls: []transport.ToolCall{{ID: "c1"}}}},
		Tools: []transport.Tool{{Type: "function", Function: transport.ToolFunction{
			Name:       "f",
			Parameters: map[string]interface{}{"properties": map[string]interface{}{"a": "string"}},
		}}},
	}
	if _, err := adapter.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Messages[0].ToolCalls[0].ID = "mutated"
	req.Tools[0].Function.Parameters["properties"].(map[string]interface{})["a"] = "mutated"
	got := adapter.Calls()
	if got[0].Request.Messages[0].ToolCalls[0].ID != "c1" ||
		got[0].Request.Tools[0].Function.Parameters["properties"].(map[string]interface{})["a"] != "string" {
		t.Fatalf("caller mutation leaked into history: %+v", got[0].Request)
	}
	got[0].Request.Messages[0].ToolCalls[0].ID = "mutated2"
	got[0].Request.Tools[0].Function.Parameters["properties"].(map[string]interface{})["a"] = "mutated2"
	again := adapter.Calls()
	if again[0].Request.Messages[0].ToolCalls[0].ID != "c1" ||
		again[0].Request.Tools[0].Function.Parameters["properties"].(map[string]interface{})["a"] != "string" {
		t.Fatalf("Calls result mutation leaked into history: %+v", again[0].Request)
	}
}
