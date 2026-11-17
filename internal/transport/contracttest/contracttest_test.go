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
		name     string
		cse      Case
		result   transport.CompleteResult
		setupFn  func(*Fixture)
		wantErr  bool
		wantCase bool
	}{
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
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
				ReportedModel:   "",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: transport.TransportStatusOK,
			},
			wantErr:  true,
			wantCase: true,
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
				ReportedModel:   "",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_500",
			},
			setupFn: func(f *Fixture) {
				f.PrimaryHits = func() int { return 2 }
			},
			wantErr:  true,
			wantCase: true,
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
			wantErr:  true,
			wantCase: true,
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
				ReportedModel:   "",
				Status:          transport.StatusError,
				NativeFinish:    "",
				TransportStatus: "http_301",
			},
			setupFn: func(f *Fixture) {
				f.ForeignHits = func() int { return 1 }
			},
			wantErr:  true,
			wantCase: true,
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
			},
			wantErr:  true,
			wantCase: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFixture := fixture
			if tt.setupFn != nil {
				tt.setupFn(&testFixture)
			}
			err := assertCase(tt.cse, req, tt.result, testFixture, 100*time.Millisecond)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, want error %v", err != nil, tt.wantErr)
			}
			if tt.wantErr && tt.wantCase && err != nil {
				if !strings.HasPrefix(err.Error(), string(tt.cse)+":") {
					t.Errorf("error message %q doesn't start with case name %q", err.Error(), string(tt.cse)+":")
				}
			}
		})
	}
}
