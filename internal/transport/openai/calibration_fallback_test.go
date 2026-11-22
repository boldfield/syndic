package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

// baselineStub answers chat requests by user-content length: empty, ".",
// the 4096-byte calibration text, or anything else (probe steps).
type baselineStub struct {
	mu          sync.Mutex
	userLens    []int
	refuseEmpty bool
	refuseDot   bool
	dropEmpty   bool // close the connection without a response for the empty baseline
}

func (s *baselineStub) lens() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.userLens...)
}

func (s *baselineStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		user := ""
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			mm := m.(map[string]any)
			if mm["role"] != "system" {
				user, _ = mm["content"].(string)
			}
		}
		s.mu.Lock()
		s.userLens = append(s.userLens, len(user))
		s.mu.Unlock()
		switch {
		case user == "" && s.dropEmpty:
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		case user == "" && s.refuseEmpty, user == "." && s.refuseDot:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"user messages must have non-empty content"}`))
			return
		}
		tokens := 1
		switch len(user) {
		case 0:
			tokens = 7
		case 1:
			tokens = 8
		case 4096:
			tokens = 1000
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "m1",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": tokens, "completion_tokens": 1},
		})
	})
}

func runBaselineProbe(t *testing.T, s *baselineStub, opts transport.ProbeOptions) (transport.CapabilityRecord, error) {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	a := New(config.EndpointProfile{Name: "test", BaseURL: srv.URL}, &mockResolver{secret: "tok-123"})
	return a.ProbeWithOptions(context.Background(), "test", "m1", opts)
}

func calibrationLens(lens []int) []int {
	var out []int
	for _, n := range lens {
		if n == 0 || n == 1 || n == 4096 {
			out = append(out, n)
		}
	}
	return out
}

func rawStatus(t *testing.T, rec transport.CapabilityRecord, step string) (float64, bool) {
	t.Helper()
	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	e, ok := raw[step]
	if !ok {
		return 0, false
	}
	st, _ := e["status"].(float64)
	return st, true
}

func TestCalibrationFallbackNonEmptyBaseline(t *testing.T) {
	s := &baselineStub{refuseEmpty: true}
	rec, err := runBaselineProbe(t, s, transport.ProbeOptions{AssumedContextLen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	got := calibrationLens(s.lens())
	if len(got) < 3 || got[0] != 0 || got[1] != 1 || got[2] != 4096 {
		t.Fatalf("calibration request sequence: got %v, want [0 1 4096]", got)
	}
	cal := rec.Calibration
	if !cal.Available || cal.BaselineBytes != 1 || cal.BaselinePromptTokens != 8 {
		t.Fatalf("calibration: %+v", cal)
	}
	if want := 4095.0 / float64(1000-8); cal.BytesPerToken != want {
		t.Errorf("BytesPerToken: got %v, want %v", cal.BytesPerToken, want)
	}
	if rec.OverLength == "not_run:no_calibration" {
		t.Errorf("over-length test did not run: %s", rec.OverLength)
	}
	if st, ok := rawStatus(t, rec, "calibration_baseline"); !ok || st != 400 {
		t.Errorf("calibration_baseline raw status: %v %v", st, ok)
	}
	if st, ok := rawStatus(t, rec, "calibration_baseline_nonempty"); !ok || st != 200 {
		t.Errorf("calibration_baseline_nonempty raw status: %v %v", st, ok)
	}
}

func TestCalibrationEmptyBaselineAccepted(t *testing.T) {
	s := &baselineStub{}
	rec, err := runBaselineProbe(t, s, transport.ProbeOptions{DisableOverLength: true})
	if err != nil {
		t.Fatal(err)
	}
	got := calibrationLens(s.lens())
	if len(got) != 2 || got[0] != 0 || got[1] != 4096 {
		t.Fatalf("calibration request sequence: got %v, want [0 4096]", got)
	}
	cal := rec.Calibration
	if !cal.Available || cal.BaselineBytes != 0 {
		t.Fatalf("calibration: %+v", cal)
	}
	if want := 4096.0 / float64(1000-7); cal.BytesPerToken != want {
		t.Errorf("BytesPerToken: got %v, want %v", cal.BytesPerToken, want)
	}
	if _, ok := rawStatus(t, rec, "calibration_baseline_nonempty"); ok {
		t.Error("fallback step recorded although the empty baseline succeeded")
	}
}

func TestCalibrationBothBaselinesRefused(t *testing.T) {
	s := &baselineStub{refuseEmpty: true, refuseDot: true}
	rec, err := runBaselineProbe(t, s, transport.ProbeOptions{AssumedContextLen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Error("calibration available although both baselines were refused")
	}
	if rec.OverLength != "not_run:no_calibration" {
		t.Errorf("OverLength: got %q", rec.OverLength)
	}
	if _, ok := rawStatus(t, rec, "calibration_baseline_nonempty"); !ok {
		t.Error("fallback step missing from raw record")
	}
}

func TestCalibrationNoFallbackWithoutResponse(t *testing.T) {
	s := &baselineStub{dropEmpty: true}
	_, _ = runBaselineProbe(t, s, transport.ProbeOptions{DisableOverLength: true})
	for _, n := range s.lens() {
		if n == 1 {
			t.Fatal("fallback baseline sent after the empty baseline failed with no response")
		}
	}
}

func TestCalibrationSkippedSendsNoBaselines(t *testing.T) {
	s := &baselineStub{refuseEmpty: true}
	_, err := runBaselineProbe(t, s, transport.ProbeOptions{DisableCalibration: true, DisableOverLength: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := calibrationLens(s.lens()); len(got) != 0 {
		t.Fatalf("calibration requests sent with calibration disabled: %v", got)
	}
}
