package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

// allProbeSteps is the full request sequence when the server rejects max_tokens,
// so every probe request path is exercised.
var allProbeSteps = []string{"models", "max_tokens", "max_completion_tokens", "system_role", "calibration_baseline", "calibration", "over_length"}

// newDispatchServer rejects max_tokens (forcing the max_completion_tokens
// follow-up), honors the system role, and reports prompt tokens by content size
// so calibration is available. omitModel drops the response model field.
func newDispatchServer(t *testing.T, hits *atomic.Int32, omitModel bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if _, ok := body["max_tokens"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"use max_completion_tokens"}`))
			return
		}
		resp := map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "PING"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1 + len(raw)/100},
		}
		if !omitModel {
			resp["model"] = "m"
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// failingTransport fails the nth (1-based) round trip with a post-dispatch error.
type failingTransport struct {
	failAt int
	calls  atomic.Int32
}

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if int(f.calls.Add(1)) == f.failAt {
		return nil, errors.New("connection reset after dispatch")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func dispatchProbeOpts(before func(*transport.ProbeRequest) error) transport.ProbeOptions {
	return transport.ProbeOptions{Timeout: 10 * time.Second, AssumedContextLen: 100, BeforeSend: before}
}

func newDispatchAdapter(srv *httptest.Server) *Adapter {
	return New(config.EndpointProfile{Name: "test", BaseURL: srv.URL}, &mockResolver{secret: "s"})
}

func TestProbeBeforeSendMatchesEveryRequest(t *testing.T) {
	var hits atomic.Int32
	srv := newDispatchServer(t, &hits, false)
	var steps []string
	rec, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(r *transport.ProbeRequest) error {
		steps = append(steps, r.Step)
		return nil
	}))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !reflect.DeepEqual(steps, allProbeSteps) {
		t.Fatalf("hook steps = %v, want %v", steps, allProbeSteps)
	}
	if int(hits.Load()) != len(steps) {
		t.Fatalf("server hits = %d, hook calls = %d", hits.Load(), len(steps))
	}
	if rec.ReportedModel == "" || rec.MaxTokensField != "max_completion_tokens" {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestProbeBeforeSendFailureIsNotSentOnEveryStep(t *testing.T) {
	for idx, step := range allProbeSteps {
		t.Run(step, func(t *testing.T) {
			var hits atomic.Int32
			srv := newDispatchServer(t, &hits, false)
			calls := 0
			_, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(r *transport.ProbeRequest) error {
				calls++
				if r.Step == step {
					return errors.New("journal full")
				}
				return nil
			}))
			if !errors.Is(err, transport.ErrNotSent) {
				t.Fatalf("err = %v, want ErrNotSent", err)
			}
			if errors.Is(err, transport.ErrUncertain) {
				t.Fatalf("pre-send failure must not be uncertain: %v", err)
			}
			if calls != idx+1 || int(hits.Load()) != idx {
				t.Fatalf("hook calls = %d (want %d), server hits = %d (want %d)", calls, idx+1, hits.Load(), idx)
			}
		})
	}
}

func TestProbePostSendFailureIsUncertainOnEveryStep(t *testing.T) {
	for idx, step := range allProbeSteps {
		t.Run(step, func(t *testing.T) {
			var hits atomic.Int32
			srv := newDispatchServer(t, &hits, false)
			a := newDispatchAdapter(srv)
			a.client = &http.Client{Transport: &failingTransport{failAt: idx + 1}}
			calls := 0
			_, err := a.ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(*transport.ProbeRequest) error {
				calls++
				return nil
			}))
			if !errors.Is(err, transport.ErrUncertain) {
				t.Fatalf("err = %v, want ErrUncertain", err)
			}
			if errors.Is(err, transport.ErrNotSent) {
				t.Fatalf("post-send failure must not be ErrNotSent: %v", err)
			}
			if calls != idx+1 {
				t.Fatalf("hook calls = %d, want %d (probe must stop at the failed request)", calls, idx+1)
			}
		})
	}
}

func TestProbeMissingModelIsIncompleteNotUsable(t *testing.T) {
	var hits atomic.Int32
	srv := newDispatchServer(t, &hits, true)
	_, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(nil))
	if !errors.Is(err, transport.ErrIncompleteProbe) {
		t.Fatalf("err = %v, want ErrIncompleteProbe", err)
	}
	if errors.Is(err, transport.ErrNotSent) || strings.Contains(fmt.Sprint(err), "not sent") {
		t.Fatalf("incomplete probe was sent; must not be ErrNotSent: %v", err)
	}
}

func TestProbeNoWorkingMaxTokensFieldIsIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", transport.ProbeOptions{Timeout: 5 * time.Second, DisableCalibration: true, DisableOverLength: true})
	if !errors.Is(err, transport.ErrIncompleteProbe) {
		t.Fatalf("err = %v, want ErrIncompleteProbe", err)
	}
}

// truncatedBodyServer advertises a longer body than it sends, then drops the
// connection, on the request for the given 1-based step.
func newTruncatingServer(t *testing.T, inner http.Handler, failAt int) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(n.Add(1)) != failAt {
			inner.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usage":`))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeTruncatedBodyIsUncertainOnEveryStep(t *testing.T) {
	for idx, step := range allProbeSteps {
		t.Run(step, func(t *testing.T) {
			var hits atomic.Int32
			inner := newDispatchServer(t, &hits, false)
			srv := newTruncatingServer(t, inner.Config.Handler, idx+1)
			calls := 0
			_, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(*transport.ProbeRequest) error {
				calls++
				return nil
			}))
			if !errors.Is(err, transport.ErrUncertain) {
				t.Fatalf("err = %v, want ErrUncertain", err)
			}
			if errors.Is(err, transport.ErrNotSent) {
				t.Fatalf("post-send failure must not be ErrNotSent: %v", err)
			}
			if calls != idx+1 {
				t.Fatalf("hook calls = %d, want %d", calls, idx+1)
			}
		})
	}
}

// closeFailingTransport fails Body.Close on the nth (1-based) round trip after
// the body has been delivered intact.
type closeFailingTransport struct {
	failAt int
	calls  atomic.Int32
}

type closeFailBody struct{ io.ReadCloser }

func (b closeFailBody) Close() error {
	_ = b.ReadCloser.Close()
	return errors.New("close failed after dispatch")
}

func (c *closeFailingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && int(c.calls.Add(1)) == c.failAt {
		resp.Body = closeFailBody{resp.Body}
	}
	return resp, err
}

func TestProbeBodyCloseFailureIsUncertainOnEveryStep(t *testing.T) {
	for idx, step := range allProbeSteps {
		t.Run(step, func(t *testing.T) {
			var hits atomic.Int32
			srv := newDispatchServer(t, &hits, false)
			a := newDispatchAdapter(srv)
			a.client = &http.Client{Transport: &closeFailingTransport{failAt: idx + 1}}
			_, err := a.ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(*transport.ProbeRequest) error { return nil }))
			if !errors.Is(err, transport.ErrUncertain) {
				t.Fatalf("err = %v, want ErrUncertain", err)
			}
			if errors.Is(err, transport.ErrNotSent) {
				t.Fatalf("post-send failure must not be ErrNotSent: %v", err)
			}
		})
	}
}

// newRedirectingServer answers every request without a hop query with a
// same-host 307 to the same path, then lets inner serve the redirected request.
// hits counts every network request, redirects included.
func newRedirectingServer(t *testing.T, inner http.Handler, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("hop") == "" {
			http.Redirect(w, r, r.URL.Path+"?hop=1", http.StatusTemporaryRedirect)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeBeforeSendAccountsEveryRedirect(t *testing.T) {
	var innerHits, hits atomic.Int32
	srv := newRedirectingServer(t, newDispatchServer(t, &innerHits, false).Config.Handler, &hits)
	var steps []string
	var redirects []int
	rec, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(r *transport.ProbeRequest) error {
		steps = append(steps, r.Step)
		redirects = append(redirects, r.Redirect)
		return nil
	}))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var wantSteps []string
	var wantRedirects []int
	for _, step := range allProbeSteps {
		wantSteps = append(wantSteps, step, step)
		wantRedirects = append(wantRedirects, 0, 1)
	}
	if !reflect.DeepEqual(steps, wantSteps) || !reflect.DeepEqual(redirects, wantRedirects) {
		t.Fatalf("hook steps = %v redirects = %v, want %v %v", steps, redirects, wantSteps, wantRedirects)
	}
	if int(hits.Load()) != len(steps) {
		t.Fatalf("network requests = %d, hook calls = %d", hits.Load(), len(steps))
	}
	if rec.ReportedModel == "" || rec.MaxTokensField != "max_completion_tokens" {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestProbeRedirectBeforeSendFailureIsUncertainOnEveryStep(t *testing.T) {
	for idx, step := range allProbeSteps {
		t.Run(step, func(t *testing.T) {
			var innerHits, hits atomic.Int32
			srv := newRedirectingServer(t, newDispatchServer(t, &innerHits, false).Config.Handler, &hits)
			calls := 0
			_, err := newDispatchAdapter(srv).ProbeWithOptions(context.Background(), "test", "m", dispatchProbeOpts(func(r *transport.ProbeRequest) error {
				calls++
				if r.Step == step && r.Redirect > 0 {
					return errors.New("journal full")
				}
				return nil
			}))
			if !errors.Is(err, transport.ErrUncertain) {
				t.Fatalf("err = %v, want ErrUncertain", err)
			}
			if errors.Is(err, transport.ErrNotSent) {
				t.Fatalf("initial request was sent; must not be ErrNotSent: %v", err)
			}
			// Every earlier step sent two requests; this step sent only its
			// initial request, and the refused redirect never left the process.
			if want := 2*idx + 2; calls != want {
				t.Fatalf("hook calls = %d, want %d", calls, want)
			}
			if want := 2*idx + 1; int(hits.Load()) != want {
				t.Fatalf("network requests = %d, want %d", hits.Load(), want)
			}
		})
	}
}
