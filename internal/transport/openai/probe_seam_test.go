package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

// TestProbeBeforeSendCallback verifies that probe HTTP requests go through
// BeforeSend callback before sending, allowing the engine to journal the
// attempt. This satisfies design v5 delta section 1a: "every probe request
// goes through the capacity bookkeeping...it is written as an attempt row
// before it is sent".
func TestProbeBeforeSendCallback(t *testing.T) {
	attemptedSteps := []string{}
	beforeSendCallback := func(req *ProbeRequest) error {
		attemptedSteps = append(attemptedSteps, req.Step)
		return nil
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			// Return valid response with model field and max_tokens working
			_, _ = w.Write([]byte(`{
				"model": "gpt-4",
				"choices": [{"message":{"content":"test"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":10}
			}`))
		}
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:    "test",
		BaseURL: server.URL,
	}
	adapter := New(profile, &mockResolver{secret: "test"})

	opts := ProbeOptions{
		Timeout:            10 * time.Second,
		BeforeSend:         beforeSendCallback,
		AssumedContextLen:  100,
		DisableCalibration: false,
	}
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", opts)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	// Verify that BeforeSend was called for probe steps
	if len(attemptedSteps) == 0 {
		t.Fatalf("no probe steps attempted")
	}

	// Should have called BeforeSend for models and max_tokens at minimum
	expectedSteps := []string{"models", "max_tokens"}
	for _, expected := range expectedSteps {
		found := false
		for _, actual := range attemptedSteps {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected probe step %q not found in %v", expected, attemptedSteps)
		}
	}
}

// TestProbeBeforeSendFailureReturnsErrNotSent verifies that pre-send failures
// (like journaling failures) are distinguishable from post-send failures via
// ErrNotSent wrapping. This satisfies design v5 delta section 1a.
func TestProbeBeforeSendFailureReturnsErrNotSent(t *testing.T) {
	beforeSendCallback := func(req *ProbeRequest) error {
		if req.Step == "models" {
			return errors.New("journaling failed")
		}
		return nil
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:    "test",
		BaseURL: server.URL,
	}
	adapter := New(profile, &mockResolver{secret: "test"})

	opts := ProbeOptions{
		Timeout:    10 * time.Second,
		BeforeSend: beforeSendCallback,
	}
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", opts)
	if err == nil {
		t.Fatal("expected error from BeforeSend failure")
	}
	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("BeforeSend failure should wrap with ErrNotSent, got %v", err)
	}
}

// TestProbeTransportStatusNotSent verifies that the openai adapter sets
// TransportStatus to not_sent when BeforeSend fails in Complete, consistent
// with the fake adapter.
func TestCompleteBeforeSendTransportStatus(t *testing.T) {
	failureCallback := func(req *transport.CompleteRequest) error {
		return errors.New("journal unavailable")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	profile := config.EndpointProfile{
		Name:    "test",
		BaseURL: server.URL,
	}
	adapter := New(profile, &mockResolver{secret: "test"})

	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant: "test",
			TurnID:      "turn1",
			AttemptID:   "attempt1",
		},
		Model:      "gpt-4",
		Messages:   []transport.Message{{Role: "user", Content: "test"}},
		BeforeSend: failureCallback,
	}

	result, err := adapter.Complete(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from BeforeSend")
	}

	if !errors.Is(err, transport.ErrNotSent) {
		t.Errorf("expected error to include ErrNotSent, got %v", err)
	}

	if result.TransportStatus != "not_sent" {
		t.Errorf("expected TransportStatus not_sent, got %s", result.TransportStatus)
	}
}
