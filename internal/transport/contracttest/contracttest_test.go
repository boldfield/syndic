package contracttest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// stubAdapter is a test adapter that returns canned responses.
type stubAdapter struct {
	cse Case
}

func (a *stubAdapter) Probe(ctx context.Context, endpoint, model string) (transport.CapabilityRecord, error) {
	return transport.CapabilityRecord{}, transport.ErrNotImplemented
}

func (a *stubAdapter) Complete(ctx context.Context, req transport.CompleteRequest) (transport.CompleteResult, error) {
	switch a.cse {
	case Success:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "Success response",
			Text:            "Success response",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "contract-model",
			Status:          transport.StatusComplete,
			NativeFinish:    "stop",
			TransportStatus: transport.TransportStatusOK,
		}, nil

	case DeadlineExceeded:
		time.Sleep(100 * time.Millisecond)
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "contract-model",
			Status:          transport.StatusInterrupted,
			NativeFinish:    "",
			TransportStatus: transport.TransportStatusTimeout,
		}, nil

	case MalformedBody:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "Malformed response",
			Text:            "",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "contract-model",
			Status:          transport.StatusError,
			NativeFinish:    "",
			TransportStatus: transport.TransportStatusMalformedBody,
		}, nil

	case ConnectionReset:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "contract-model",
			Status:          transport.StatusInterrupted,
			NativeFinish:    "",
			TransportStatus: transport.TransportStatusReset,
		}, nil

	case Truncated:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "Truncated response",
			Text:            "Truncated response",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "contract-model",
			Status:          transport.StatusTruncated,
			NativeFinish:    "length",
			TransportStatus: transport.TransportStatusOK,
		}, nil

	case Refused:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "Content refused",
			Text:            "Content refused",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "contract-model",
			Status:          transport.StatusRefused,
			NativeFinish:    "content_filter",
			TransportStatus: transport.TransportStatusOK,
		}, nil

	case AuthFailure:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "",
			Status:          transport.StatusError,
			NativeFinish:    "",
			TransportStatus: transport.TransportStatusUnauthorized,
		}, nil

	case ServerError:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "",
			Status:          transport.StatusError,
			NativeFinish:    "",
			TransportStatus: "http_500",
		}, nil

	case IdentityMismatch:
		wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "Response",
			Text:            "Response",
			ScrubCount:      0,
			Identity:        wrongIdentity,
			ReportedModel:   "contract-model",
			Status:          transport.StatusComplete,
			NativeFinish:    "stop",
			TransportStatus: transport.TransportStatusOK,
		}, nil

	case RedirectOtherHost:
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       "",
			Text:            "",
			ScrubCount:      0,
			Identity:        transport.NewUnknownIdentity(),
			ReportedModel:   "",
			Status:          transport.StatusError,
			NativeFinish:    "",
			TransportStatus: "http_301",
		}, nil

	case SecretEcho:
		secret := "test-secret-value"
		responseWithSecret := "Response with " + secret + " embedded"
		scrubbed := strings.ReplaceAll(responseWithSecret, secret, transport.ScrubMarker)
		wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
		return transport.CompleteResult{
			Envelope:        req.Envelope,
			RawOutput:       scrubbed,
			Text:            scrubbed,
			ScrubCount:      1,
			Identity:        wrongIdentity,
			ReportedModel:   "contract-model",
			Status:          transport.StatusComplete,
			NativeFinish:    "stop",
			TransportStatus: transport.TransportStatusOK,
		}, nil

	default:
		return transport.CompleteResult{}, transport.ErrNotImplemented
	}
}

func (a *stubAdapter) Cancel(ctx context.Context, attemptID string) (transport.CancelStatus, error) {
	return transport.CancelStatus{}, transport.ErrNotImplemented
}

func (a *stubAdapter) Usage(ctx context.Context) (*transport.Usage, error) {
	return nil, transport.ErrUsageUnavailable
}

// stubBackend provides stub fixtures for contract testing.
type stubBackend struct{}

func (b *stubBackend) Setup(t *testing.T, cse Case) Fixture {
	secret := "test-secret-value"
	expectedIdentity := transport.NewUnknownIdentity()

	return Fixture{
		Adapter:  &stubAdapter{cse: cse},
		Secret:   secret,
		Expected: transport.Expected{Identity: expectedIdentity, ReportedModel: "contract-model"},
		PrimaryHits: func() int {
			if cse == ServerError {
				return 1
			}
			return 0
		},
		ForeignHits: func() int { return 0 },
	}
}

// TestContractTestSuite runs the contract test suite against the stub backend.
func TestContractTestSuite(t *testing.T) {
	backend := &stubBackend{}
	Run(t, backend)
}

// TestAssertionFailures verifies that assertions correctly reject wrong results.
func TestAssertionFailures(t *testing.T) {
	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "test",
			Phase:         "test",
			Epoch:         1,
			AttemptID:     "test",
			CandidateHash: "test",
		},
		Model:           "contract-model",
		Messages:        []transport.Message{{Role: "user", Content: "test"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	fixture := Fixture{
		Adapter:     &stubAdapter{},
		Secret:      "test-secret",
		Expected:    transport.Expected{Identity: transport.NewUnknownIdentity(), ReportedModel: "contract-model"},
		PrimaryHits: func() int { return 1 },
		ForeignHits: func() int { return 0 },
	}

	tests := []struct {
		name    string
		cse     Case
		result  transport.CompleteResult
		elapsed time.Duration
		setupFn func(*Fixture)
	}{
		// Positive cases: conforming results return nil error
		{
			name: "success: conforming result",
			cse:  Success,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "deadline_exceeded: conforming result",
			cse:  DeadlineExceeded,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusTimeout,
			},
		},
		{
			name: "malformed_body: conforming result",
			cse:  MalformedBody,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "error details",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusMalformedBody,
			},
		},
		{
			name: "connection_reset: conforming result",
			cse:  ConnectionReset,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusReset,
			},
		},
		{
			name: "truncated: conforming result",
			cse:  Truncated,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "partial output",
				Text:            "partial output",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusTruncated,
				NativeFinish:    "length",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "refused: conforming result",
			cse:  Refused,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "refused",
				Text:            "refused",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusRefused,
				NativeFinish:    "content_filter",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "auth_failure: conforming result",
			cse:  AuthFailure,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusUnauthorized,
			},
		},
		{
			name: "server_error: conforming result",
			cse:  ServerError,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_500",
			},
		},
		{
			name: "identity_mismatch: conforming result",
			cse:  IdentityMismatch,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
				f.Expected = transport.Expected{Identity: wrongIdentity, ReportedModel: "contract-model"}
			},
		},
		{
			name: "redirect_other_host: conforming result",
			cse:  RedirectOtherHost,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_301",
			},
		},
		{
			name: "secret_echo: conforming result",
			cse:  SecretEcho,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "Response with [REDACTED-SECRET] embedded",
				Text:            "Response with [REDACTED-SECRET] embedded",
				ScrubCount:      1,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
				f.Expected = transport.Expected{Identity: wrongIdentity, ReportedModel: "contract-model"}
			},
		},

		// Negative cases: errors for each case
		{
			name: "success: wrong status",
			cse:  Success,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "success: empty text",
			cse:  Success,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "success: envelope mismatch",
			cse:  Success,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "deadline_exceeded: wrong status",
			cse:  DeadlineExceeded,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusTimeout,
			},
		},
		{
			name: "deadline_exceeded: envelope mismatch",
			cse:  DeadlineExceeded,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusTimeout,
			},
		},
		{
			name: "malformed_body: wrong transport status",
			cse:  MalformedBody,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "malformed_body: envelope mismatch",
			cse:  MalformedBody,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "error details",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusMalformedBody,
			},
		},
		{
			name: "connection_reset: wrong transport status",
			cse:  ConnectionReset,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusTimeout,
			},
		},
		{
			name: "connection_reset: envelope mismatch",
			cse:  ConnectionReset,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusReset,
			},
		},
		{
			name: "truncated: empty native finish",
			cse:  Truncated,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusTruncated,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "truncated: wrong status (complete instead of truncated)",
			cse:  Truncated,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "partial output",
				Text:            "partial output",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "length",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "truncated: envelope mismatch",
			cse:  Truncated,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "partial output",
				Text:            "partial output",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusTruncated,
				NativeFinish:    "length",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "refused: empty native finish",
			cse:  Refused,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusRefused,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "refused: envelope mismatch",
			cse:  Refused,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "refused",
				Text:            "refused",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusRefused,
				NativeFinish:    "content_filter",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "auth_failure: wrong transport status",
			cse:  AuthFailure,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "auth_failure: envelope mismatch",
			cse:  AuthFailure,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusUnauthorized,
			},
		},
		{
			name: "server_error: wrong hit count",
			cse:  ServerError,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_500",
			},
			setupFn: func(f *Fixture) {
				f.PrimaryHits = func() int { return 2 }
			},
		},
		{
			name: "server_error: nil primary hits hook",
			cse:  ServerError,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_500",
			},
			setupFn: func(f *Fixture) {
				f.PrimaryHits = nil
			},
		},
		{
			name: "server_error: envelope mismatch",
			cse:  ServerError,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_500",
			},
		},
		{
			name: "identity_mismatch: check result not identity mismatch",
			cse:  IdentityMismatch,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "identity_mismatch: envelope mismatch",
			cse:  IdentityMismatch,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "redirect_other_host: foreign hits not 0",
			cse:  RedirectOtherHost,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_301",
			},
			setupFn: func(f *Fixture) {
				f.ForeignHits = func() int { return 1 }
			},
		},
		{
			name: "redirect_other_host: nil foreign hits hook",
			cse:  RedirectOtherHost,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_301",
			},
			setupFn: func(f *Fixture) {
				f.ForeignHits = nil
			},
		},
		{
			name: "redirect_other_host: envelope mismatch",
			cse:  RedirectOtherHost,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_301",
			},
		},
		{
			name: "secret_echo: secret not scrubbed",
			cse:  SecretEcho,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "Response with test-secret embedded",
				Text:            "Response with test-secret embedded",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				f.Secret = "test-secret"
				wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
				f.Expected = transport.Expected{Identity: wrongIdentity, ReportedModel: "contract-model"}
			},
		},
		{
			name: "secret_echo: scrub count 0 with secret correctly scrubbed",
			cse:  SecretEcho,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "Response with [REDACTED-SECRET] embedded",
				Text:            "Response with [REDACTED-SECRET] embedded",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
				f.Expected = transport.Expected{Identity: wrongIdentity, ReportedModel: "contract-model"}
			},
		},
		{
			name: "secret_echo: check result passes (must fail)",
			cse:  SecretEcho,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "Response with [REDACTED-SECRET] embedded",
				Text:            "Response with [REDACTED-SECRET] embedded",
				ScrubCount:      1,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				f.Secret = "test-secret"
			},
		},
		{
			name: "secret_echo: envelope mismatch",
			cse:  SecretEcho,
			result: transport.CompleteResult{
				Envelope: transport.AttemptEnvelope{
					Participant:   "wrong",
					TurnID:        "test",
					Phase:         "test",
					Epoch:         1,
					AttemptID:     "test",
					CandidateHash: "test",
				},
				RawOutput:       "Response with [REDACTED-SECRET] embedded",
				Text:            "Response with [REDACTED-SECRET] embedded",
				ScrubCount:      1,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
				f.Expected = transport.Expected{Identity: wrongIdentity, ReportedModel: "contract-model"}
			},
		},

		// Additional negative cases to verify specific assertions
		{
			name: "success: scrub count not 0",
			cse:  Success,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      1,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
		},
		{
			name: "success: transport status not ok",
			cse:  Success,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "text",
				Text:            "text",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: "http_200",
			},
		},
		{
			name: "malformed_body: empty raw output",
			cse:  MalformedBody,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusMalformedBody,
			},
		},
		{
			name: "malformed_body: non-empty text",
			cse:  MalformedBody,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "error details",
				Text:            "error message",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusMalformedBody,
			},
		},
		{
			name:    "deadline_exceeded: exceeded timeout bound",
			cse:     DeadlineExceeded,
			elapsed: 900 * time.Millisecond,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "",
				Text:            "",
				ScrubCount:      0,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusInterrupted,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusTimeout,
			},
		},
		{
			name: "secret_echo: text contains secret (raw output correctly scrubbed)",
			cse:  SecretEcho,
			result: transport.CompleteResult{
				Envelope:        req.Envelope,
				RawOutput:       "Response with [REDACTED-SECRET] embedded",
				Text:            "Response with test-secret embedded",
				ScrubCount:      1,
				Identity:        transport.NewUnknownIdentity(),
				ReportedModel:   "contract-model",
				Status:          transport.StatusComplete,
				NativeFinish:    "stop",
				TransportStatus: transport.TransportStatusOK,
			},
			setupFn: func(f *Fixture) {
				f.Secret = "test-secret"
				wrongIdentity, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
				f.Expected = transport.Expected{Identity: wrongIdentity, ReportedModel: "contract-model"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFixture := fixture
			if tt.setupFn != nil {
				tt.setupFn(&testFixture)
			}
			elapsed := tt.elapsed
			if elapsed == 0 {
				elapsed = 100 * time.Millisecond
			}
			err := assertCase(tt.cse, req, tt.result, testFixture, elapsed)

			// Positive cases should return nil
			if strings.Contains(tt.name, "conforming") {
				if err != nil {
					t.Errorf("conforming result should not error, got: %v", err)
				}
				return
			}

			// Negative cases should return an error with case name prefix
			if err == nil {
				t.Errorf("expected error for wrong result, got nil")
			} else if !strings.HasPrefix(err.Error(), string(tt.cse)+":") {
				t.Errorf("error message %q doesn't start with case name %q", err.Error(), string(tt.cse)+":")
			}
		})
	}
}
