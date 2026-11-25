package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/contracttest"
)

var logBuffer bytes.Buffer

func TestMain(m *testing.M) {
	log.SetOutput(&logBuffer)
	code := m.Run()
	log.SetOutput(os.Stderr)

	logOutput := logBuffer.String()
	secretsToCheck := []string{"super-secret-token"}
	for _, secret := range secretsToCheck {
		if strings.Contains(logOutput, secret) {
			fmt.Fprintf(os.Stderr, "FAIL: log buffer contains unredacted secret: %s\n", secret)
			os.Exit(1)
		}
	}

	os.Exit(code)
}

func intPtr(i int) *int {
	return &i
}

type testBackend struct {
	primaryServer *httptest.Server
	foreignServer *httptest.Server
	primaryHits   atomic.Int32
	foreignHits   atomic.Int32
	secret        string
}

type mockResolver struct {
	secret string
}

func (r *mockResolver) Resolve(ref config.SecretRef) (string, error) {
	if ref.Name == "test-secret" {
		return r.secret, nil
	}
	return "", fmt.Errorf("unknown secret: %s", ref.Name)
}

func (b *testBackend) Setup(t *testing.T, cse contracttest.Case) contracttest.Fixture {
	b.secret = "super-secret-token"
	b.primaryHits.Store(0)
	b.foreignHits.Store(0)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        "",
		Auth:           &config.SecretRef{Source: "env", Name: "test-secret"},
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{secret: b.secret}

	var primaryHandler, foreignHandler http.HandlerFunc

	switch cse {
	case contracttest.Success:
		primaryHandler = b.handleSuccess
		foreignHandler = b.handleForeignUnused

	case contracttest.DeadlineExceeded:
		primaryHandler = b.handleDeadlineExceeded
		foreignHandler = b.handleForeignUnused

	case contracttest.MalformedBody:
		primaryHandler = b.handleMalformedBody
		foreignHandler = b.handleForeignUnused

	case contracttest.ConnectionReset:
		primaryHandler = b.handleConnectionReset
		foreignHandler = b.handleForeignUnused

	case contracttest.Truncated:
		primaryHandler = b.handleTruncated
		foreignHandler = b.handleForeignUnused

	case contracttest.Refused:
		primaryHandler = b.handleRefused
		foreignHandler = b.handleForeignUnused

	case contracttest.AuthFailure:
		primaryHandler = b.handleAuthFailure
		foreignHandler = b.handleForeignUnused

	case contracttest.ServerError:
		primaryHandler = b.handleServerError
		foreignHandler = b.handleForeignUnused

	case contracttest.IdentityMismatch:
		primaryHandler = b.handleIdentityMismatch
		foreignHandler = b.handleForeignUnused

	case contracttest.RedirectOtherHost:
		primaryHandler = b.handleRedirectOtherHost
		foreignHandler = b.handleForeignRedirected

	case contracttest.SecretEcho:
		primaryHandler = b.handleSecretEcho
		foreignHandler = b.handleForeignUnused

	default:
		t.Fatalf("unknown case: %s", cse)
	}

	b.primaryServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.primaryHits.Add(1)
		primaryHandler(w, r)
	}))

	b.foreignServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.foreignHits.Add(1)
		foreignHandler(w, r)
	}))

	t.Cleanup(func() {
		b.primaryServer.Close()
		b.foreignServer.Close()
	})

	profile.BaseURL = b.primaryServer.URL

	adapter := New(profile, resolver)

	expected := transport.Expected{
		Identity:      transport.NewUnknownIdentity(),
		ReportedModel: "contract-model",
	}

	if cse == contracttest.SecretEcho {
		expected.ReportedModel = "different-model"
	}

	return contracttest.Fixture{
		Adapter:     adapter,
		Secret:      b.secret,
		Expected:    expected,
		PrimaryHits: func() int { return int(b.primaryHits.Load()) },
		ForeignHits: func() int { return int(b.foreignHits.Load()) },
	}
}

func (b *testBackend) handleSuccess(w http.ResponseWriter, r *http.Request) {
	resp := ResponseBody{
		Model: "contract-model",
		Choices: []ChoiceBody{
			{
				Message: ChoiceMessage{
					Role:    "assistant",
					Content: "This is the response.",
				},
				FinishReason: "stop",
			},
		},
		Usage: &UsageData{
			PromptTokens:     intPtr(10),
			CompletionTokens: intPtr(5),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *testBackend) handleDeadlineExceeded(w http.ResponseWriter, r *http.Request) {
	time.Sleep(1 * time.Second)
	w.WriteHeader(http.StatusInternalServerError)
}

func (b *testBackend) handleMalformedBody(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"invalid": "json"`))
}

func (b *testBackend) handleConnectionReset(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = conn.Close()
}

func (b *testBackend) handleTruncated(w http.ResponseWriter, r *http.Request) {
	resp := ResponseBody{
		Model: "contract-model",
		Choices: []ChoiceBody{
			{
				Message: ChoiceMessage{
					Role:    "assistant",
					Content: "Response was truncated.",
				},
				FinishReason: "length",
			},
		},
		Usage: &UsageData{
			PromptTokens:     intPtr(10),
			CompletionTokens: intPtr(5),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *testBackend) handleRefused(w http.ResponseWriter, r *http.Request) {
	resp := ResponseBody{
		Model: "contract-model",
		Choices: []ChoiceBody{
			{
				Message: ChoiceMessage{
					Role:    "assistant",
					Content: "I cannot respond to this.",
				},
				FinishReason: "content_filter",
			},
		},
		Usage: &UsageData{
			PromptTokens:     intPtr(10),
			CompletionTokens: intPtr(5),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *testBackend) handleAuthFailure(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error": "unauthorized"}`))
}

func (b *testBackend) handleServerError(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error": "internal server error"}`))
}

func (b *testBackend) handleIdentityMismatch(w http.ResponseWriter, r *http.Request) {
	resp := ResponseBody{
		Model: "different-model",
		Choices: []ChoiceBody{
			{
				Message: ChoiceMessage{
					Role:    "assistant",
					Content: "Response from different model.",
				},
				FinishReason: "stop",
			},
		},
		Usage: &UsageData{
			PromptTokens:     intPtr(10),
			CompletionTokens: intPtr(5),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *testBackend) handleRedirectOtherHost(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, b.foreignServer.URL+"/chat/completions", http.StatusFound)
}

func (b *testBackend) handleForeignRedirected(w http.ResponseWriter, r *http.Request) {
	resp := ResponseBody{
		Model: "contract-model",
		Choices: []ChoiceBody{
			{
				Message: ChoiceMessage{
					Role:    "assistant",
					Content: "This should not be reached.",
				},
				FinishReason: "stop",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *testBackend) handleSecretEcho(w http.ResponseWriter, r *http.Request) {
	resp := ResponseBody{
		Model: "contract-model",
		Choices: []ChoiceBody{
			{
				Message: ChoiceMessage{
					Role:    "assistant",
					Content: "The secret is: " + b.secret,
				},
				FinishReason: "stop",
			},
		},
		Usage: &UsageData{
			PromptTokens:     intPtr(10),
			CompletionTokens: intPtr(5),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *testBackend) handleForeignUnused(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
}

func TestContractOpenAI(t *testing.T) {
	backend := &testBackend{}
	contracttest.Run(t, backend)
}

func TestTableTestCases(t *testing.T) {
	tests := []struct {
		name                    string
		setupFunc               func(*testing.T) (*Adapter, transport.CompleteRequest)
		expectedStatus          transport.CompletionStatus
		expectedTransportStatus string
	}{
		{
			name: "404_model_unavailable",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_tokens",
				}
				adapter := New(profile, &mockResolver{})

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusModelUnavailable,
		},
		{
			name: "missing_finish_reason",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Role:    "assistant",
									Content: "Response",
								},
								FinishReason: "",
							},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_tokens",
				}
				adapter := New(profile, &mockResolver{})

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusOK,
		},
		{
			name: "no_choices",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model:   "test-model",
						Choices: []ChoiceBody{},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_tokens",
				}
				adapter := New(profile, &mockResolver{})

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusMalformedBody,
		},
		{
			name: "max_tokens_field_selection",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Role:    "assistant",
									Content: "Response",
								},
								FinishReason: "stop",
							},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_completion_tokens",
				}
				adapter := New(profile, &mockResolver{})

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusComplete,
			expectedTransportStatus: transport.TransportStatusOK,
		},
		{
			name: "oversize_body_default_limit",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(make([]byte, 17*1024*1024))
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_tokens",
				}
				adapter := New(profile, &mockResolver{})

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusBodyTooLarge,
		},
		{
			name: "oversize_body_custom_limit",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(make([]byte, 1024*1024))
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_tokens",
				}
				adapter := NewWithOptions(profile, &mockResolver{}, WithMaxBodySize(512*1024))

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusBodyTooLarge,
		},
		{
			name: "truncated_with_verdict_envelope",
			setupFunc: func(t *testing.T) (*Adapter, transport.CompleteRequest) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "contract-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Role:    "assistant",
									Content: `{"verdict": "approve", "candidate": "test", "identity": "test"}`,
								},
								FinishReason: "length",
							},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				}))
				t.Cleanup(server.Close)

				profile := config.EndpointProfile{
					Name:           "test",
					BaseURL:        server.URL,
					Auth:           nil,
					ExtraHeaders:   map[string]config.SecretRef{},
					Dialect:        "chat_completions_v1",
					MaxTokensField: "max_tokens",
				}
				adapter := New(profile, &mockResolver{})

				return adapter, transport.CompleteRequest{
					Envelope: transport.AttemptEnvelope{
						Participant:   "test",
						TurnID:        "turn1",
						Phase:         "phase1",
						Epoch:         1,
						AttemptID:     "attempt1",
						CandidateHash: "hash1",
					},
					Model: "test-model",
					Messages: []transport.Message{
						{Role: "user", Content: "test"},
					},
					MaxOutputTokens: 100,
					Deadline:        time.Now().Add(2 * time.Second),
				}
			},
			expectedStatus:          transport.StatusTruncated,
			expectedTransportStatus: transport.TransportStatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, req := tt.setupFunc(t)
			result, err := adapter.Complete(context.Background(), req)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Status != tt.expectedStatus {
				t.Errorf("status got %q, want %q", result.Status, tt.expectedStatus)
			}

			if result.TransportStatus != tt.expectedTransportStatus {
				t.Errorf("transport status got %q, want %q", result.TransportStatus, tt.expectedTransportStatus)
			}
		})
	}
}

func TestConnectionRefused(t *testing.T) {
	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        "http://127.0.0.1:1",
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{}
	adapter := New(profile, resolver)

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(1 * time.Second),
	}

	result, err := adapter.Complete(context.Background(), req)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != transport.StatusError {
		t.Errorf("status got %q, want error", result.Status)
	}

	if result.TransportStatus != transport.TransportStatusUnreachable {
		t.Errorf("transport status got %q, want unreachable", result.TransportStatus)
	}
}

func TestUnparseableBaseURL(t *testing.T) {
	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        "http://[::1",
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{}
	adapter := New(profile, resolver)

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(1 * time.Second),
	}

	result, err := adapter.Complete(context.Background(), req)

	if err == nil {
		t.Error("expected error for unparseable BaseURL, got nil")
	}

	if result.Identity != transport.NewUnknownIdentity() {
		t.Errorf("identity got %+v, want %+v", result.Identity, transport.NewUnknownIdentity())
	}
}

func TestSecretLogging(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Response",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           &config.SecretRef{Source: "env", Name: "test-secret"},
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{secret: "super-secret-token"}
	adapter := New(profile, resolver)

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	result, err := adapter.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result.RawOutput, "super-secret-token") {
		t.Error("raw output contains unredacted secret")
	}
}

func TestCancel(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-done
	}))
	defer server.Close()
	defer close(done)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{}
	adapter := New(profile, resolver)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(10 * time.Second),
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancelStatus, _ := adapter.Cancel(ctx, "attempt1")
		if !cancelStatus.Requested {
			t.Error("cancel did not find in-flight request")
		}
	}()

	result, _ := adapter.Complete(ctx, req)
	if result.Status != transport.StatusInterrupted {
		t.Errorf("status got %q, want interrupted", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusCancelled {
		t.Errorf("transport status got %q, want cancelled", result.TransportStatus)
	}
}

func TestUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Response",
					},
					FinishReason: "stop",
				},
			},
			Usage: &UsageData{
				PromptTokens:     intPtr(15),
				CompletionTokens: intPtr(10),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{}
	adapter := New(profile, resolver)

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	_, _ = adapter.Complete(context.Background(), req)

	usage, err := adapter.Usage(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if usage.Prompt != 15 {
		t.Errorf("prompt tokens got %d, want 15", usage.Prompt)
	}
	if usage.Completion != 10 {
		t.Errorf("completion tokens got %d, want 10", usage.Completion)
	}
}

func TestRequestBodyFormat(t *testing.T) {
	tests := []struct {
		name           string
		maxTokensField string
		wantKeys       map[string]bool
	}{
		{
			name:           "default_max_tokens",
			maxTokensField: "",
			wantKeys: map[string]bool{
				"model":      true,
				"messages":   true,
				"stream":     true,
				"max_tokens": true,
			},
		},
		{
			name:           "max_completion_tokens",
			maxTokensField: "max_completion_tokens",
			wantKeys: map[string]bool{
				"model":                 true,
				"messages":              true,
				"stream":                true,
				"max_completion_tokens": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody map[string]interface{}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &capturedBody)

				resp := ResponseBody{
					Model: "test-model",
					Choices: []ChoiceBody{
						{
							Message: ChoiceMessage{
								Role:    "assistant",
								Content: "Response",
							},
							FinishReason: "stop",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			profile := config.EndpointProfile{
				Name:           "test",
				BaseURL:        server.URL,
				Auth:           nil,
				ExtraHeaders:   map[string]config.SecretRef{},
				Dialect:        "chat_completions_v1",
				MaxTokensField: tt.maxTokensField,
			}

			resolver := &mockResolver{}
			adapter := New(profile, resolver)

			req := transport.CompleteRequest{
				Envelope: transport.AttemptEnvelope{
					Participant:   "test",
					TurnID:        "turn1",
					Phase:         "phase1",
					Epoch:         1,
					AttemptID:     "attempt1",
					CandidateHash: "hash1",
				},
				Model: "test-model",
				Messages: []transport.Message{
					{Role: "user", Content: "test"},
				},
				MaxOutputTokens: 100,
				Deadline:        time.Now().Add(2 * time.Second),
			}

			_, _ = adapter.Complete(context.Background(), req)

			bodyKeys := make(map[string]bool)
			for k := range capturedBody {
				bodyKeys[k] = true
			}

			if len(bodyKeys) != len(tt.wantKeys) {
				t.Errorf("body has %d keys, want %d", len(bodyKeys), len(tt.wantKeys))
			}

			for key := range tt.wantKeys {
				if !bodyKeys[key] {
					t.Errorf("missing key: %s", key)
				}
			}

			for key := range bodyKeys {
				if !tt.wantKeys[key] {
					t.Errorf("unexpected key: %s", key)
				}
			}
		})
	}
}

func BenchmarkComplete(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Response",
					},
					FinishReason: "stop",
				},
			},
			Usage: &UsageData{
				PromptTokens:     intPtr(10),
				CompletionTokens: intPtr(5),
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{}
	adapter := New(profile, resolver)

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(10 * time.Second),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = adapter.Complete(context.Background(), req)
	}
}

// Test Probe with fully cooperative server
// Test Probe table test for various server behaviors
func TestProbeBehaviorTable(t *testing.T) {
	tests := []struct {
		name           string
		setupServer    func(*testing.T, *http.ServeMux) string
		expectedFields map[string]any
	}{
		{
			name: "models_404",
			setupServer: func(t *testing.T, m *http.ServeMux) string {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{Message: ChoiceMessage{Content: "test"}, FinishReason: "stop"},
						},
						Usage: &UsageData{PromptTokens: intPtr(10), CompletionTokens: intPtr(5)},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
				return ""
			},
			expectedFields: map[string]any{
				"ModelsEndpoint": transport.TriNo,
			},
		},
		{
			name: "max_tokens_fallback",
			setupServer: func(t *testing.T, m *http.ServeMux) string {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				callCount := 0
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					callCount++
					var reqBody map[string]any
					_ = json.NewDecoder(r.Body).Decode(&reqBody)

					if _, ok := reqBody["max_tokens"]; ok {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error": "max_tokens not supported"}`))
						return
					}

					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{Message: ChoiceMessage{Content: "test"}, FinishReason: "stop"},
						},
						Usage: &UsageData{PromptTokens: intPtr(10), CompletionTokens: intPtr(5)},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
				return ""
			},
			expectedFields: map[string]any{
				"MaxTokensField": "max_completion_tokens",
			},
		},
		{
			name: "no_usage_block",
			setupServer: func(t *testing.T, m *http.ServeMux) string {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{Message: ChoiceMessage{Content: "test"}, FinishReason: "stop"},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
				return ""
			},
			expectedFields: map[string]any{
				"UsageAvailable": transport.TriNo,
			},
		},
		{
			name: "system_role_ignored",
			setupServer: func(t *testing.T, m *http.ServeMux) string {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{Message: ChoiceMessage{Content: "Response ignored system role"}, FinishReason: "stop"},
						},
						Usage: &UsageData{PromptTokens: intPtr(10), CompletionTokens: intPtr(5)},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
				return ""
			},
			expectedFields: map[string]any{
				"SystemRoleHonored": transport.TriNo,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			tt.setupServer(t, mux)
			server := httptest.NewServer(mux)
			defer server.Close()

			profile := config.EndpointProfile{
				Name:           "test",
				BaseURL:        server.URL,
				Auth:           nil,
				ExtraHeaders:   map[string]config.SecretRef{},
				Dialect:        "chat_completions_v1",
				MaxTokensField: "",
			}

			adapter := New(profile, &mockResolver{})
			record, err := adapter.Probe(context.Background(), "test", "test-model", transport.ProbeOptions{})

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			for field, expectedVal := range tt.expectedFields {
				switch field {
				case "ModelsEndpoint":
					if record.ModelsEndpoint != expectedVal {
						t.Errorf("%s: got %q, want %q", field, record.ModelsEndpoint, expectedVal)
					}
				case "MaxTokensField":
					if record.MaxTokensField != expectedVal {
						t.Errorf("%s: got %q, want %q", field, record.MaxTokensField, expectedVal)
					}
				case "UsageAvailable":
					if record.UsageAvailable != expectedVal {
						t.Errorf("%s: got %q, want %q", field, record.UsageAvailable, expectedVal)
					}
				case "SystemRoleHonored":
					if record.SystemRoleHonored != expectedVal {
						t.Errorf("%s: got %q, want %q", field, record.SystemRoleHonored, expectedVal)
					}
				}
			}
		})
	}
}

// Test Probe error cases
func TestProbeErrors(t *testing.T) {
	tests := []struct {
		name        string
		setupServer func() string
		expectedErr error
	}{
		{
			name: "unauthorized",
			setupServer: func() string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
				}))
				return server.URL
			},
			expectedErr: transport.ErrUnauthorized,
		},
		{
			name: "model_unavailable",
			setupServer: func() string {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/models" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				return server.URL
			},
			expectedErr: transport.ErrModelUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverURL := tt.setupServer()
			server, _ := http.NewRequest("GET", serverURL, nil)
			_ = server

			profile := config.EndpointProfile{
				Name:           "test",
				BaseURL:        serverURL,
				Auth:           nil,
				ExtraHeaders:   map[string]config.SecretRef{},
				Dialect:        "chat_completions_v1",
				MaxTokensField: "max_tokens",
			}

			adapter := New(profile, &mockResolver{})
			_, err := adapter.Probe(context.Background(), "test", "test-model", transport.ProbeOptions{})

			if err == nil {
				t.Errorf("expected error %v, got nil", tt.expectedErr)
			} else if !errors.Is(err, tt.expectedErr) {
				t.Errorf("got error %v, want %v", err, tt.expectedErr)
			}
		})
	}
}

// Test Probe with bearer token scrubbing
func TestProbeSecretScrubbing(t *testing.T) {
	secret := "super-secret-token-12345"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		var reqBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&reqBody)

		messages, ok := reqBody["messages"].([]any)
		var promptTokens int
		if ok && len(messages) > 0 {
			msg, _ := messages[len(messages)-1].(map[string]any)
			content, _ := msg["content"].(string)
			// Return different token counts for calibration requests
			if content == "" {
				promptTokens = 1 // baseline calibration
			} else if len(content) == 4096 {
				promptTokens = 100 // calibration text
			} else if len(content) > 100000 {
				promptTokens = 50 // over-length probe
			} else {
				promptTokens = 10 // other requests
			}
		}

		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Content: "Response contains " + secret,
					},
					FinishReason: "stop",
				},
			},
			Usage: &UsageData{PromptTokens: intPtr(promptTokens), CompletionTokens: intPtr(5)},
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Secret", secret)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           &config.SecretRef{Source: "env", Name: "test-secret"},
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "",
	}

	resolver := &mockResolver{secret: secret}
	adapter := New(profile, resolver)

	record, err := adapter.ProbeWithOptions(context.Background(), "test", "test-model", transport.ProbeOptions{
		AssumedContextLen: 131072,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rawStr := string(record.Raw)
	if strings.Contains(rawStr, secret) {
		t.Errorf("Raw record contains unredacted secret")
	}
	if !strings.Contains(rawStr, "Response contains") {
		t.Errorf("Raw record does not contain the echoed response: %s", rawStr)
	}
	var steps map[string]map[string]any
	if err := json.Unmarshal(record.Raw, &steps); err != nil {
		t.Fatalf("Raw is not a JSON object: %v", err)
	}
	for _, k := range []string{"models", "max_tokens", "system_role", "calibration_baseline", "calibration", "over_length"} {
		if _, ok := steps[k]; !ok {
			t.Errorf("Raw missing step %q: %s", k, rawStr)
		}
	}
}

// Test Probe with over-length disabled option
func TestProbeOverLengthDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Content: "PING response",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "",
	}

	adapter := New(profile, &mockResolver{})

	opts := transport.ProbeOptions{
		Timeout:           10 * time.Second,
		DisableOverLength: true,
		AssumedContextLen: 131072,
	}

	record, err := adapter.ProbeWithOptions(context.Background(), "test", "test-model", opts)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if record.OverLength != "not_run:skipped" {
		t.Errorf("OverLength: got %q, want not_run:skipped when disabled", record.OverLength)
	}
}

// Test Probe with over-length markers
func TestProbeOverLengthMarkers(t *testing.T) {
	tests := []struct {
		name              string
		setupServer       func(*http.ServeMux)
		expectedOverLengt string
		expectedEligible  transport.Tri
	}{
		{
			name: "both_markers_returned",
			setupServer: func(m *http.ServeMux) {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					var reqBody map[string]any
					_ = json.NewDecoder(r.Body).Decode(&reqBody)

					messages, ok := reqBody["messages"].([]any)
					var requestContent string
					var promptTokens int
					if ok && len(messages) > 0 {
						msg, _ := messages[len(messages)-1].(map[string]any)
						requestContent, _ = msg["content"].(string)
						// Return different token counts for calibration requests
						if requestContent == "" {
							promptTokens = 1 // baseline calibration
						} else if len(requestContent) == 4096 {
							promptTokens = 100 // calibration text (matches expected size)
						} else if len(requestContent) > 100000 {
							promptTokens = 50 // over-length probe
						} else {
							promptTokens = 10 // other requests
						}
					}

					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Content: "Found markers: " + requestContent,
								},
								FinishReason: "stop",
							},
						},
						Usage: &UsageData{
							PromptTokens:     intPtr(promptTokens),
							CompletionTokens: intPtr(5),
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
			},
			expectedOverLengt: "accepted_whole",
			expectedEligible:  transport.TriUnknown,
		},
		{
			name: "head_marker_only",
			setupServer: func(m *http.ServeMux) {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					var reqBody map[string]any
					_ = json.NewDecoder(r.Body).Decode(&reqBody)

					messages, ok := reqBody["messages"].([]any)
					var promptTokens int
					if ok && len(messages) > 0 {
						msg, _ := messages[len(messages)-1].(map[string]any)
						content, _ := msg["content"].(string)
						// Return different token counts for calibration requests
						if content == "" {
							promptTokens = 1 // baseline calibration
						} else if len(content) == 4096 {
							promptTokens = 100 // calibration text
						} else if len(content) > 100000 {
							promptTokens = 50 // over-length probe
						} else {
							promptTokens = 10 // other requests
						}
						// Extract first 32 hex chars (head marker) for over-length probe
						if len(content) >= 32 {
							headMarker := content[:32]
							resp := ResponseBody{
								Model: "test-model",
								Choices: []ChoiceBody{
									{
										Message: ChoiceMessage{
											Content: "Found head marker: " + headMarker,
										},
										FinishReason: "stop",
									},
								},
								Usage: &UsageData{
									PromptTokens:     intPtr(promptTokens),
									CompletionTokens: intPtr(5),
								},
							}
							w.Header().Set("Content-Type", "application/json")
							_ = json.NewEncoder(w).Encode(resp)
							return
						}
					}

					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Content: "No markers in response",
								},
								FinishReason: "stop",
							},
						},
						Usage: &UsageData{
							PromptTokens:     intPtr(promptTokens),
							CompletionTokens: intPtr(5),
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
			},
			expectedOverLengt: "markers_missing",
			expectedEligible:  transport.TriUnknown,
		},
		{
			name: "no_markers",
			setupServer: func(m *http.ServeMux) {
				m.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				})
				m.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
					var reqBody map[string]any
					_ = json.NewDecoder(r.Body).Decode(&reqBody)

					messages, ok := reqBody["messages"].([]any)
					var promptTokens int
					if ok && len(messages) > 0 {
						msg, _ := messages[len(messages)-1].(map[string]any)
						content, _ := msg["content"].(string)
						// Return different token counts for calibration requests
						if content == "" {
							promptTokens = 1 // baseline calibration
						} else if len(content) == 4096 {
							promptTokens = 100 // calibration text
						} else if len(content) > 100000 {
							promptTokens = 50 // over-length probe
						} else {
							promptTokens = 10 // other requests
						}
					}

					resp := ResponseBody{
						Model: "test-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Content: "Response without markers",
								},
								FinishReason: "stop",
							},
						},
						Usage: &UsageData{
							PromptTokens:     intPtr(promptTokens),
							CompletionTokens: intPtr(5),
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				})
			},
			expectedOverLengt: "markers_missing",
			expectedEligible:  transport.TriUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			tt.setupServer(mux)
			server := httptest.NewServer(mux)
			defer server.Close()

			profile := config.EndpointProfile{
				Name:           "test",
				BaseURL:        server.URL,
				Auth:           nil,
				ExtraHeaders:   map[string]config.SecretRef{},
				Dialect:        "chat_completions_v1",
				MaxTokensField: "",
			}

			adapter := New(profile, &mockResolver{})
			record, err := adapter.ProbeWithOptions(context.Background(), "test", "test-model", transport.ProbeOptions{
				AssumedContextLen: 131072,
			})

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if record.OverLength != tt.expectedOverLengt {
				t.Errorf("OverLength: got %q, want %q", record.OverLength, tt.expectedOverLengt)
			}

			if record.EligibleRequiredReviewer != tt.expectedEligible {
				t.Errorf("EligibleRequiredReviewer: got %q, want %q", record.EligibleRequiredReviewer, tt.expectedEligible)
			}
		})
	}
}

// Test Probe endpoint name validation
func TestProbeEndpointMismatch(t *testing.T) {
	profile := config.EndpointProfile{
		Name:           "myendpoint",
		BaseURL:        "http://localhost:8000",
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{})
	_, err := adapter.Probe(context.Background(), "differentendpoint", "test-model", transport.ProbeOptions{})

	if err == nil {
		t.Errorf("expected error for endpoint mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error should mention endpoint mismatch, got: %v", err)
	}
}

// TestUnresolvableAuthSecret verifies unresolvable auth secret returns ErrNotSent and no request is sent.
func TestUnresolvableAuthSecret(t *testing.T) {
	requestReceived := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           &config.SecretRef{Source: "env", Name: "missing-secret"},
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	failingResolver := &mockResolver{secret: ""}

	adapter := New(profile, failingResolver)

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := adapter.Complete(ctx, req)

	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("error not ErrNotSent: %v", err)
	}
	if requestReceived {
		t.Errorf("request was sent despite auth resolution failure")
	}
}

// TestToolRoleAllowed verifies tool role messages are allowed and sent correctly.
func TestToolRoleAllowed(t *testing.T) {
	requestReceived := false
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Response",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "tool", Content: "tool response", ToolCallID: "call123"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !requestReceived {
		t.Errorf("request was not sent")
	}
	if result.Status != transport.StatusComplete {
		t.Errorf("unexpected status: %v", result.Status)
	}

	// Verify tool message was included in request
	if capturedBody != nil {
		messages, ok := capturedBody["messages"].([]any)
		if !ok || len(messages) == 0 {
			t.Errorf("no messages in request body")
		} else {
			lastMsg := messages[len(messages)-1].(map[string]any)
			if lastMsg["role"] != "tool" {
				t.Errorf("tool message role not sent: %v", lastMsg["role"])
			}
			if lastMsg["tool_call_id"] != "call123" {
				t.Errorf("tool_call_id not sent correctly: %v", lastMsg["tool_call_id"])
			}
		}
	}
}

// TestToolRoundtrip verifies tool definitions are sent, tool_calls are decoded with correct
// IDs and arguments (scrubbed), multiple calls are ordered, and tool_call_id is sent correctly.
func TestToolRoundtrip(t *testing.T) {
	requestReceived := false
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Verify tool definitions were sent with correct schema
		tools, ok := capturedBody["tools"].([]any)
		if !ok || len(tools) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "expected 2 tools, got %d", len(tools))
			return
		}

		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Using tools",
						ToolCalls: []ResponseToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: ResponseToolCallFunction{
									Name:      "read_file",
									Arguments: `{"path":"/etc/passwd","secret":"super-secret-token"}`,
								},
							},
							{
								ID:   "call_2",
								Type: "function",
								Function: ResponseToolCallFunction{
									Name:      "web_fetch",
									Arguments: `{"url":"https://example.com","auth":"super-secret-token"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           &config.SecretRef{Source: "env", Name: "test-secret"},
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: "super-secret-token"})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "Use these tools"},
		},
		Tools: []transport.Tool{
			{
				Type: "function",
				Function: transport.ToolFunction{
					Name:        "read_file",
					Description: "Read a file",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]string{"type": "string"},
						},
					},
				},
			},
			{
				Type: "function",
				Function: transport.ToolFunction{
					Name:        "web_fetch",
					Description: "Fetch a URL",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"url": map[string]string{"type": "string"},
						},
					},
				},
			},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !requestReceived {
		t.Errorf("request was not sent")
	}
	if result.Status != transport.StatusToolCalls {
		t.Errorf("unexpected status: %v, want %v", result.Status, transport.StatusToolCalls)
	}

	// Verify tool definitions were sent with complete metadata
	if capturedBody != nil {
		tools, ok := capturedBody["tools"].([]any)
		if !ok || len(tools) != 2 {
			t.Errorf("tool definitions not sent correctly")
		} else {
			// Check first tool metadata
			tool1, ok := tools[0].(map[string]any)
			if !ok {
				t.Errorf("tool 0 is not a map")
			} else {
				if tool1["type"] != "function" {
					t.Errorf("tool 0 type: got %v, want function", tool1["type"])
				}
				if fn, ok := tool1["function"].(map[string]any); ok {
					if fn["name"] != "read_file" {
						t.Errorf("tool 0 function name: got %v, want read_file", fn["name"])
					}
					if fn["description"] != "Read a file" {
						t.Errorf("tool 0 function description: got %v, want 'Read a file'", fn["description"])
					}
					if params, ok := fn["parameters"].(map[string]any); ok {
						if params["type"] != "object" {
							t.Errorf("tool 0 parameters type: got %v, want object", params["type"])
						}
					} else {
						t.Errorf("tool 0 parameters not a map")
					}
				} else {
					t.Errorf("tool 0 function not a map")
				}
			}
			// Check second tool metadata
			tool2, ok := tools[1].(map[string]any)
			if !ok {
				t.Errorf("tool 1 is not a map")
			} else {
				if tool2["type"] != "function" {
					t.Errorf("tool 1 type: got %v, want function", tool2["type"])
				}
				if fn, ok := tool2["function"].(map[string]any); ok {
					if fn["name"] != "web_fetch" {
						t.Errorf("tool 1 function name: got %v, want web_fetch", fn["name"])
					}
					if fn["description"] != "Fetch a URL" {
						t.Errorf("tool 1 function description: got %v, want 'Fetch a URL'", fn["description"])
					}
				} else {
					t.Errorf("tool 1 function not a map")
				}
			}
		}
	}

	// Verify tool_calls are decoded with correct IDs and order
	if len(result.ToolCalls) != 2 {
		t.Errorf("expected 2 tool calls, got %d", len(result.ToolCalls))
	} else {
		// Check first call
		if result.ToolCalls[0].ID != "call_1" {
			t.Errorf("first call ID: %q, want call_1", result.ToolCalls[0].ID)
		}
		if result.ToolCalls[0].Function.Name != "read_file" {
			t.Errorf("first call name: %q, want read_file", result.ToolCalls[0].Function.Name)
		}
		// Verify arguments are scrubbed: secret should be replaced
		if strings.Contains(result.ToolCalls[0].Function.Arguments, "super-secret-token") {
			t.Errorf("first call arguments contain unredacted secret")
		}
		// But the structure should be preserved
		if !strings.Contains(result.ToolCalls[0].Function.Arguments, "path") {
			t.Errorf("first call arguments missing 'path' field")
		}

		// Check second call
		if result.ToolCalls[1].ID != "call_2" {
			t.Errorf("second call ID: %q, want call_2", result.ToolCalls[1].ID)
		}
		if result.ToolCalls[1].Function.Name != "web_fetch" {
			t.Errorf("second call name: %q, want web_fetch", result.ToolCalls[1].Function.Name)
		}
		// Verify arguments are scrubbed
		if strings.Contains(result.ToolCalls[1].Function.Arguments, "super-secret-token") {
			t.Errorf("second call arguments contain unredacted secret")
		}
		// But the structure should be preserved
		if !strings.Contains(result.ToolCalls[1].Function.Arguments, "url") {
			t.Errorf("second call arguments missing 'url' field")
		}
	}
}

// TestToolMessageEmptyCallID verifies that tool messages with empty ToolCallID are rejected with ErrNotSent.
func TestToolMessageEmptyCallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should not reach here
		t.Errorf("request should not be sent for tool message with empty ToolCallID")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "tool", Content: "tool response", ToolCallID: ""}, // Empty ToolCallID
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := adapter.Complete(ctx, req)

	if err == nil {
		t.Errorf("expected error for empty ToolCallID, got nil")
	} else if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("expected ErrNotSent, got %T: %v", err, err)
	}
}

// TestEmptyContentPreserved verifies that empty content is preserved for user/system/tool messages.
func TestEmptyContentPreserved(t *testing.T) {
	requestReceived := false
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = true
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Response",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: ""}, // Empty user content
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := adapter.Complete(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !requestReceived {
		t.Errorf("request was not sent")
	}

	// Verify empty content was preserved
	if capturedBody != nil {
		messages, ok := capturedBody["messages"].([]any)
		if !ok || len(messages) == 0 {
			t.Errorf("no messages in request body")
		} else {
			firstMsg := messages[0].(map[string]any)
			content, hasContent := firstMsg["content"]
			if !hasContent {
				t.Errorf("empty content was omitted from user message")
			}
			if content != "" {
				t.Errorf("empty content was not preserved; got: %q", content)
			}
		}
	}
}

// TestAdapterVersionEqualsProbeVersion verifies adapter.AdapterVersion matches probe record.
func TestAdapterVersionEqualsProbeVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.WriteHeader(http.StatusNotFound)
		case "/chat/completions":
			resp := ResponseBody{
				Model: "test-model",
				Choices: []ChoiceBody{
					{
						Message: ChoiceMessage{
							Role:    "assistant",
							Content: "Response",
						},
						FinishReason: "stop",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	record, err := adapter.Probe(context.Background(), "test", "test-model", transport.ProbeOptions{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	if record.AdapterVersion != AdapterVersion {
		t.Errorf("probe record AdapterVersion: %q, want %q", record.AdapterVersion, AdapterVersion)
	}

	if AdapterVersion != "openai-v2" {
		t.Errorf("AdapterVersion: %q, want openai-v2", AdapterVersion)
	}
}

// TestMidBodyFailureUnwrapped verifies mid-body read failures are not wrapped with ErrNotSent.
func TestMidBodyFailureUnwrapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte(`{"incomplete": "resp`))
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := adapter.Complete(ctx, req)

	if err != nil && errors.Is(err, transport.ErrNotSent) {
		t.Errorf("mid-body failure incorrectly wrapped with ErrNotSent")
	}
}

// TestBeforeSendCalled verifies BeforeSend callback is invoked before the request is sent.
func TestBeforeSendCalled(t *testing.T) {
	callCount := 0
	callback := func(req *transport.CompleteRequest) error {
		callCount++
		return nil
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg1","model":"test","choices":[{"message":{"content":"response"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
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
	if result.Text != "response" {
		t.Errorf("response text: %q, want 'response'", result.Text)
	}
}

// TestBeforeSendError verifies BeforeSend error is wrapped in ErrNotSent and no request is sent.
func TestBeforeSendError(t *testing.T) {
	callErr := errors.New("journaling failed")
	callback := func(req *transport.CompleteRequest) error {
		return callErr
	}

	hitCount := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg1","model":"test","choices":[{"message":{"content":"response"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
		BeforeSend:      callback,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := adapter.Complete(ctx, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("error not wrapped with ErrNotSent: %v", err)
	}
	if !errors.Is(err, callErr) {
		t.Errorf("original error lost: %v", err)
	}
	if hitCount.Load() != 0 {
		t.Errorf("HTTP server hit count: %d, want 0 (request should not be sent)", hitCount.Load())
	}
}

// TestBeforeSendNil verifies nil BeforeSend callback is handled gracefully.
func TestBeforeSendNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg1","model":"test","choices":[{"message":{"content":"response"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "test"},
		},
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
	if result.Text != "response" {
		t.Errorf("response text: %q, want 'response'", result.Text)
	}
}

// TestAssistantMessageWithToolCalls verifies that assistant messages with tool_calls
// are properly reserialized and sent in the request body.
func TestAssistantMessageWithToolCalls(t *testing.T) {
	capturedBody := map[string]any{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Verify messages include assistant message with tool_calls
		messages, ok := capturedBody["messages"].([]any)
		if !ok || len(messages) < 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "expected at least 2 messages")
			return
		}

		// Check that last message is assistant with tool_calls
		lastMsg, ok := messages[len(messages)-1].(map[string]any)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "last message is not a map")
			return
		}

		if lastMsg["role"] != "assistant" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "last message role is not assistant: %v", lastMsg["role"])
			return
		}

		toolCalls, hasToolCalls := lastMsg["tool_calls"]
		if !hasToolCalls {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "last message has no tool_calls")
			return
		}

		tcList, ok := toolCalls.([]any)
		if !ok || len(tcList) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "tool_calls is not a list of 1 items")
			return
		}

		// Respond with plain text
		resp := ResponseBody{
			Model: "test-model",
			Choices: []ChoiceBody{
				{
					Message: ChoiceMessage{
						Role:    "assistant",
						Content: "Response",
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	profile := config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           nil,
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	adapter := New(profile, &mockResolver{secret: ""})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant:   "test",
			TurnID:        "turn1",
			Phase:         "phase1",
			Epoch:         1,
			AttemptID:     "attempt1",
			CandidateHash: "hash1",
		},
		Model: "test-model",
		Messages: []transport.Message{
			{Role: "user", Content: "user request"},
			{
				Role:    "assistant",
				Content: "Using tool",
				ToolCalls: []transport.ToolCall{
					{
						ID: "call_1",
						Function: transport.ToolCallFunction{
							Name:      "read_file",
							Arguments: `{"path":"/etc/file"}`,
						},
					},
				},
			},
		},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(2 * time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := adapter.Complete(ctx, req)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result.Status != transport.StatusComplete {
		t.Errorf("unexpected status: %v, want %v", result.Status, transport.StatusComplete)
	}
	if result.Text != "Response" {
		t.Errorf("unexpected text: %q, want 'Response'", result.Text)
	}
}
