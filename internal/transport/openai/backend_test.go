package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/contracttest"
)

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

	profile := &config.EndpointProfile{
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
			PromptTokens:     10,
			CompletionTokens: 5,
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
			PromptTokens:     10,
			CompletionTokens: 5,
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
			PromptTokens:     10,
			CompletionTokens: 5,
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
			PromptTokens:     10,
			CompletionTokens: 5,
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
			PromptTokens:     10,
			CompletionTokens: 5,
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
		setupFunc               func(*testing.T, *Adapter) (transport.CompleteRequest, transport.Expected)
		expectedStatus          transport.CompletionStatus
		expectedTransportStatus string
		verifyScrubbing         bool
	}{
		{
			name: "404_model_unavailable",
			setupFunc: func(t *testing.T, a *Adapter) (transport.CompleteRequest, transport.Expected) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
				t.Cleanup(server.Close)

				a.profile.BaseURL = server.URL
				return transport.CompleteRequest{
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
					}, transport.Expected{
						Identity:      transport.NewUnknownIdentity(),
						ReportedModel: "",
					}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusModelUnavailable,
		},
		{
			name: "missing_finish_reason",
			setupFunc: func(t *testing.T, a *Adapter) (transport.CompleteRequest, transport.Expected) {
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

				a.profile.BaseURL = server.URL
				return transport.CompleteRequest{
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
					}, transport.Expected{
						Identity:      transport.NewUnknownIdentity(),
						ReportedModel: "test-model",
					}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusOK,
		},
		{
			name: "no_choices",
			setupFunc: func(t *testing.T, a *Adapter) (transport.CompleteRequest, transport.Expected) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model:   "test-model",
						Choices: []ChoiceBody{},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				}))
				t.Cleanup(server.Close)

				a.profile.BaseURL = server.URL
				return transport.CompleteRequest{
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
					}, transport.Expected{
						Identity:      transport.NewUnknownIdentity(),
						ReportedModel: "test-model",
					}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusMalformedBody,
		},
		{
			name: "max_tokens_field_selection",
			setupFunc: func(t *testing.T, a *Adapter) (transport.CompleteRequest, transport.Expected) {
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

				a.profile.BaseURL = server.URL
				a.profile.MaxTokensField = "max_completion_tokens"

				return transport.CompleteRequest{
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
					}, transport.Expected{
						Identity:      transport.NewUnknownIdentity(),
						ReportedModel: "test-model",
					}
			},
			expectedStatus:          transport.StatusComplete,
			expectedTransportStatus: transport.TransportStatusOK,
		},
		{
			name: "oversize_body",
			setupFunc: func(t *testing.T, a *Adapter) (transport.CompleteRequest, transport.Expected) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(make([]byte, 17*1024*1024))
				}))
				t.Cleanup(server.Close)

				a.profile.BaseURL = server.URL
				return transport.CompleteRequest{
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
					}, transport.Expected{
						Identity:      transport.NewUnknownIdentity(),
						ReportedModel: "",
					}
			},
			expectedStatus:          transport.StatusError,
			expectedTransportStatus: transport.TransportStatusBodyTooLarge,
		},
		{
			name: "truncated_with_verdict_content",
			setupFunc: func(t *testing.T, a *Adapter) (transport.CompleteRequest, transport.Expected) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					resp := ResponseBody{
						Model: "contract-model",
						Choices: []ChoiceBody{
							{
								Message: ChoiceMessage{
									Role:    "assistant",
									Content: `{"approval": true, "confidence": 0.95}`,
								},
								FinishReason: "length",
							},
						},
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(resp)
				}))
				t.Cleanup(server.Close)

				a.profile.BaseURL = server.URL
				return transport.CompleteRequest{
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
					}, transport.Expected{
						Identity:      transport.NewUnknownIdentity(),
						ReportedModel: "contract-model",
					}
			},
			expectedStatus:          transport.StatusTruncated,
			expectedTransportStatus: transport.TransportStatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &config.EndpointProfile{
				Name:           "test",
				BaseURL:        "http://localhost",
				Auth:           nil,
				ExtraHeaders:   map[string]config.SecretRef{},
				Dialect:        "chat_completions_v1",
				MaxTokensField: "max_tokens",
			}
			resolver := &mockResolver{}
			adapter := New(profile, resolver)

			req, _ := tt.setupFunc(t, adapter)
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
	profile := &config.EndpointProfile{
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

	profile := &config.EndpointProfile{
		Name:           "test",
		BaseURL:        server.URL,
		Auth:           &config.SecretRef{Source: "env", Name: "test-secret"},
		ExtraHeaders:   map[string]config.SecretRef{},
		Dialect:        "chat_completions_v1",
		MaxTokensField: "max_tokens",
	}

	resolver := &mockResolver{secret: "secret-value"}
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

	result, _ := adapter.Complete(context.Background(), req)

	if strings.Contains(result.RawOutput, "secret-value") {
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

	profile := &config.EndpointProfile{
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
				PromptTokens:     15,
				CompletionTokens: 10,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := &config.EndpointProfile{
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

			profile := &config.EndpointProfile{
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
				PromptTokens:     10,
				CompletionTokens: 5,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	profile := &config.EndpointProfile{
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
