package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
