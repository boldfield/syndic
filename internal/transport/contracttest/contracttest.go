package contracttest

import (
	"context"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// Case represents a named test case in the contract.
type Case string

const (
	Success           Case = "success"
	DeadlineExceeded  Case = "deadline_exceeded"
	MalformedBody     Case = "malformed_body"
	ConnectionReset   Case = "connection_reset"
	Truncated         Case = "truncated"
	Refused           Case = "refused"
	AuthFailure       Case = "auth_failure"
	ServerError       Case = "server_error"
	IdentityMismatch  Case = "identity_mismatch"
	RedirectOtherHost Case = "redirect_other_host"
	SecretEcho        Case = "secret_echo"
)

// AllCases returns all test cases in order.
func AllCases() []Case {
	return []Case{
		Success,
		DeadlineExceeded,
		MalformedBody,
		ConnectionReset,
		Truncated,
		Refused,
		AuthFailure,
		ServerError,
		IdentityMismatch,
		RedirectOtherHost,
		SecretEcho,
	}
}

// Fixture contains everything needed to test one case.
type Fixture struct {
	Adapter     transport.Adapter
	Secret      string
	Expected    transport.Expected
	PrimaryHits func() int
	ForeignHits func() int
}

// Backend arranges test conditions for a case.
type Backend interface {
	// Setup returns a fixture ready to test the given case.
	// Cleanup is registered with t.Cleanup.
	Setup(t *testing.T, cse Case) Fixture
}

// Run executes the contract test suite against a backend.
func Run(t *testing.T, backend Backend) {
	for _, cse := range AllCases() {
		t.Run(string(cse), func(t *testing.T) {
			fixture := backend.Setup(t, cse)

			// Build standard request
			req := transport.CompleteRequest{
				Envelope: transport.AttemptEnvelope{
					Participant:   "test-participant",
					TurnID:        "test-turn",
					Phase:         "test-phase",
					Epoch:         1,
					AttemptID:     "test-attempt",
					CandidateHash: "test-candidate",
				},
				Model: "contract-model",
				Messages: []transport.Message{
					{Role: "system", Content: "You are a test assistant."},
					{Role: "user", Content: "Test message."},
				},
				MaxOutputTokens: 100,
				Deadline:        time.Now().Add(2 * time.Second),
			}

			// Adjust deadline for deadline_exceeded case
			if cse == DeadlineExceeded {
				req.Deadline = time.Now().Add(300 * time.Millisecond)
			}

			// Send request and measure time
			startTime := time.Now()
			ctx, cancel := context.WithDeadline(context.Background(), req.Deadline)
			defer cancel()

			result, err := fixture.Adapter.Complete(ctx, req)
			elapsed := time.Since(startTime)

			// Check adapter didn't return a Go error
			if err != nil {
				t.Fatalf("adapter returned error: %v", err)
			}

			// Call the appropriate assertion for this case
			err = assertCase(t, cse, req, result, fixture, elapsed)
			if err != nil {
				t.Errorf("assertion failed: %v", err)
			}
		})
	}
}

// assertCase calls the appropriate assertion function for the case.
func assertCase(t *testing.T, cse Case, req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	switch cse {
	case Success:
		return AssertSuccess(req, result, fixture)
	case DeadlineExceeded:
		return AssertDeadlineExceeded(req, result, fixture, elapsed)
	case MalformedBody:
		return AssertMalformedBody(req, result, fixture)
	case ConnectionReset:
		return AssertConnectionReset(req, result, fixture)
	case Truncated:
		return AssertTruncated(req, result, fixture)
	case Refused:
		return AssertRefused(req, result, fixture)
	case AuthFailure:
		return AssertAuthFailure(req, result, fixture)
	case ServerError:
		return AssertServerError(req, result, fixture)
	case IdentityMismatch:
		return AssertIdentityMismatch(req, result, fixture)
	case RedirectOtherHost:
		return AssertRedirectOtherHost(req, result, fixture)
	case SecretEcho:
		return AssertSecretEcho(req, result, fixture)
	default:
		return nil
	}
}
