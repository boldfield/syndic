package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

func TestProbeThrottledFirstRequestStopsWhenRefused(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	var seen []transport.ProbeResponse
	opts := transport.ProbeOptions{
		Timeout: 10 * time.Second,
		AfterResponse: func(r transport.ProbeResponse) bool {
			seen = append(seen, r)
			return false
		},
	}
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", opts)

	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProbeStepError", err)
	}
	if pe.Step != "models" || pe.Status != 429 {
		t.Errorf("step %q status %d, want models 429", pe.Step, pe.Status)
	}
	if pe.FailureClass != transport.FailureClassThrottled {
		t.Errorf("class %q, want throttled", pe.FailureClass)
	}
	if want := (transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: 7000}); pe.ServerWait != want {
		t.Errorf("wait %+v, want %+v", pe.ServerWait, want)
	}
	if !errors.Is(err, transport.ErrProbeStopped) {
		t.Errorf("error %v is not ErrProbeStopped", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("server saw %d requests, want 1", n)
	}
	if len(seen) != 1 || seen[0].Step != "models" || seen[0].FailureClass != transport.FailureClassThrottled {
		t.Errorf("hook saw %+v", seen)
	}
}

func TestProbeStepErrorCarriesResponseClass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", transport.ProbeOptions{Timeout: 10 * time.Second})

	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProbeStepError", err)
	}
	if pe.FailureClass != transport.FailureClassRejectedConfig || !pe.NoRetry || pe.Status != 401 {
		t.Errorf("got class %q noRetry %v status %d", pe.FailureClass, pe.NoRetry, pe.Status)
	}
}

func TestProbeIncompleteReportsLastMaxTokensResponseClass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", transport.ProbeOptions{Timeout: 10 * time.Second})

	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProbeStepError", err)
	}
	if !errors.Is(err, transport.ErrIncompleteProbe) {
		t.Errorf("error %v is not ErrIncompleteProbe", err)
	}
	if pe.FailureClass != transport.FailureClassThrottled {
		t.Errorf("class %q, want throttled", pe.FailureClass)
	}
	if want := (transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: 3000}); pe.ServerWait != want {
		t.Errorf("wait %+v, want %+v", pe.ServerWait, want)
	}
}

func TestProbeUnparsableSuccessBodyIsBadResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	var first transport.ProbeResponse
	opts := transport.ProbeOptions{
		Timeout: 10 * time.Second,
		AfterResponse: func(r transport.ProbeResponse) bool {
			first = r
			return false
		},
	}
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", opts)

	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProbeStepError", err)
	}
	if first.FailureClass != transport.FailureClassBadResponse || pe.FailureClass != transport.FailureClassBadResponse {
		t.Errorf("hook class %q, error class %q, want bad_response", first.FailureClass, pe.FailureClass)
	}
}

// probeCompletionClass serves a valid /models listing and the given 2xx
// completion body, and returns the class the hook saw for the completion.
func probeCompletionClass(t *testing.T, body string) transport.FailureClass {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	var got transport.FailureClass
	opts := transport.ProbeOptions{
		Timeout: 10 * time.Second,
		AfterResponse: func(r transport.ProbeResponse) bool {
			if r.Step != "models" {
				got = r.FailureClass
				return false
			}
			return true
		},
	}
	_, _ = adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", opts)
	return got
}

func TestProbeValidButUnusableCompletionIsBadResponse(t *testing.T) {
	if got := probeCompletionClass(t, `{"choices":[]}`); got != transport.FailureClassBadResponse {
		t.Errorf("class %q, want bad_response", got)
	}
}

func TestProbeOverLimitSuccessBodyIsUncertain(t *testing.T) {
	big := `{"choices":[{"message":{"content":"` + strings.Repeat("a", 1<<20+10) + `"}}]}`
	if got := probeCompletionClass(t, big); got != transport.FailureClassUncertain {
		t.Errorf("class %q, want uncertain", got)
	}
}

func TestProbeSuccessResponseCarriesWaitAndNoRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Header().Set("Retry-After", "5")
		w.Header().Set("x-should-retry", "false")
		// A usable completion that names no model.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	var maxTokens *transport.ProbeResponse
	opts := transport.ProbeOptions{
		Timeout:            10 * time.Second,
		DisableCalibration: true,
		AfterResponse: func(r transport.ProbeResponse) bool {
			if r.Step == "max_tokens" {
				maxTokens = &r
			}
			return true
		},
	}
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", opts)

	want := transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: 5000}
	if maxTokens == nil {
		t.Fatal("hook never saw max_tokens")
	}
	if maxTokens.ServerWait != want || !maxTokens.NoRetry {
		t.Errorf("hook saw wait %+v noRetry %v, want %+v true", maxTokens.ServerWait, maxTokens.NoRetry, want)
	}
	if !errors.Is(err, transport.ErrIncompleteProbe) {
		t.Fatalf("error %v is not ErrIncompleteProbe", err)
	}
	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProbeStepError", err)
	}
	if pe.Step != "max_tokens" || pe.ServerWait != want || !pe.NoRetry {
		t.Errorf("step %q wait %+v noRetry %v, want max_tokens %+v true", pe.Step, pe.ServerWait, pe.NoRetry, want)
	}
}

func TestProbeToolCallsAutoFailureReportsItsOwnStep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["tools"]; ok {
			if _, forced := req["tool_choice"].(map[string]any); forced {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(toolChoiceRefusal))
				return
			}
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"model":"gpt-4","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	adapter := New(config.EndpointProfile{Name: "test", BaseURL: server.URL}, &mockResolver{secret: "test"})
	_, err := adapter.ProbeWithOptions(context.Background(), "test", "gpt-4", transport.ProbeOptions{Timeout: 10 * time.Second, DisableCalibration: true})

	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a ProbeStepError", err)
	}
	if pe.Step != "tool_calls_auto" || pe.FailureClass != transport.FailureClassUncertain {
		t.Errorf("step %q class %q, want tool_calls_auto uncertain", pe.Step, pe.FailureClass)
	}
	if !errors.Is(err, transport.ErrUncertain) {
		t.Errorf("error %v is not ErrUncertain", err)
	}
}
