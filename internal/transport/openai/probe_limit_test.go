package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

const limitRefusal = `{"error":{"message":"Could not finish the message because max_tokens or model output limit was reached. Please try again with higher max_tokens."}}`

type limitReq struct {
	field   string
	limit   int
	content string
	system  bool
}

type limitServer struct {
	mu   sync.Mutex
	reqs []limitReq
	// reply decides the response for a request.
	reply func(r limitReq) (int, string)
}

func (s *limitServer) sent() []limitReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]limitReq(nil), s.reqs...)
}

func (s *limitServer) count(field string, limit int) int {
	n := 0
	for _, r := range s.sent() {
		if r.field == field && r.limit == limit {
			n++
		}
	}
	return n
}

func newLimitServer(t *testing.T, reply func(r limitReq) (int, string)) (*limitServer, *Adapter) {
	t.Helper()
	s := &limitServer{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		lr := limitReq{}
		for _, f := range []string{"max_tokens", "max_completion_tokens"} {
			if v, ok := req[f].(float64); ok {
				lr.field, lr.limit = f, int(v)
			}
		}
		msgs, _ := req["messages"].([]any)
		for _, m := range msgs {
			mm := m.(map[string]any)
			c, _ := mm["content"].(string)
			if mm["role"] == "system" {
				lr.system = true
			} else {
				lr.content = c
			}
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, lr)
		s.mu.Unlock()
		status, body := s.reply(lr)
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	profile := config.EndpointProfile{
		Name:    "test",
		BaseURL: srv.URL,
		Auth:    &config.SecretRef{Source: "env", Name: "test-secret"},
	}
	return s, New(profile, &mockResolver{secret: "tok-123"})
}

func okBody(r limitReq) string {
	tokens := 1
	if len(r.content) > 1 {
		tokens = 1000
	}
	answer := "ok"
	if r.system {
		answer = "PING"
	}
	b, _ := json.Marshal(map[string]any{
		"model":   "m1",
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": tokens},
	})
	return string(b)
}

// strictLimit refuses limits below 256.
func strictLimit(r limitReq) (int, string) {
	if r.limit < 256 {
		return 400, limitRefusal
	}
	return 200, okBody(r)
}

func rawSteps(t *testing.T, rec transport.CapabilityRecord) map[string]map[string]any {
	t.Helper()
	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestIsOutputLimitTooSmall(t *testing.T) {
	if !isOutputLimitTooSmall(400, []byte("Output Limit Was Reached")) {
		t.Error("case-insensitive match expected")
	}
	for _, st := range []int{200, 429, 500} {
		if isOutputLimitTooSmall(st, []byte(limitRefusal)) {
			t.Errorf("status %d must not match", st)
		}
	}
	if isOutputLimitTooSmall(400, []byte(`{"error":"unsupported parameter"}`)) {
		t.Error("unrelated 400 must not match")
	}
}

func TestProbeRetriesTooSmallLimit(t *testing.T) {
	s, a := newLimitServer(t, strictLimit)
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MaxTokensField != "max_tokens" || rec.ReportedModel == "" {
		t.Errorf("field %q reported %q", rec.MaxTokensField, rec.ReportedModel)
	}
	raw := rawSteps(t, rec)
	for step, status := range map[string]float64{"max_tokens": 400, "max_tokens_retry": 200} {
		e, ok := raw[step]
		if !ok {
			t.Fatalf("missing raw step %s", step)
		}
		if e["status"] != status {
			t.Errorf("%s status %v, want %v", step, e["status"], status)
		}
		if req, _ := e["request"].(string); !strings.Contains(req, `"max_tokens"`) {
			t.Errorf("%s request body missing: %v", step, e["request"])
		}
	}
	if s.count("max_tokens", 1) != 0 {
		t.Error("a limit of 1 was sent")
	}
	if !rec.Calibration.Available || rec.Calibration.BaselineBytes != 0 {
		t.Errorf("calibration: %+v", rec.Calibration)
	}
	for _, step := range []string{"calibration_baseline_retry", "calibration_retry"} {
		if _, ok := raw[step]; !ok {
			t.Errorf("missing raw step %s", step)
		}
	}
	if _, ok := raw["calibration_baseline_nonempty"]; ok {
		t.Error("one-byte fallback should not run")
	}
}

func TestProbeFieldAlwaysTooSmallFallsToCompletionTokens(t *testing.T) {
	s, a := newLimitServer(t, func(r limitReq) (int, string) {
		if r.field == "max_tokens" {
			return 400, limitRefusal
		}
		return 200, okBody(r)
	})
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true, DisableCalibration: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MaxTokensField != "max_completion_tokens" {
		t.Errorf("field %q", rec.MaxTokensField)
	}
	got := s.sent()[:3]
	want := []limitReq{{field: "max_tokens", limit: 16}, {field: "max_tokens", limit: 256}, {field: "max_completion_tokens", limit: 16}}
	for i := range want {
		if got[i].field != want[i].field || got[i].limit != want[i].limit {
			t.Errorf("request %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if s.count("max_completion_tokens", 256) != 0 {
		t.Error("max_completion_tokens needed no retry")
	}
}

func TestProbeUnsupportedParameterNotRetried(t *testing.T) {
	s, a := newLimitServer(t, func(r limitReq) (int, string) {
		if r.field == "max_tokens" {
			return 400, `{"error":"unsupported parameter: max_tokens"}`
		}
		return 200, okBody(r)
	})
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true, DisableCalibration: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MaxTokensField != "max_completion_tokens" {
		t.Errorf("field %q", rec.MaxTokensField)
	}
	if s.count("max_tokens", 256) != 0 {
		t.Error("retry sent for unsupported parameter")
	}
	if _, ok := rawSteps(t, rec)["max_tokens_retry"]; ok {
		t.Error("max_tokens_retry recorded")
	}
}

func TestProbeNoRetryWhenSixteenWorks(t *testing.T) {
	s, a := newLimitServer(t, func(r limitReq) (int, string) { return 200, okBody(r) })
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range s.sent() {
		if r.limit == 256 {
			t.Errorf("retry sent: %+v", r)
		}
	}
	for step := range rawSteps(t, rec) {
		if strings.HasSuffix(step, "_retry") {
			t.Errorf("retry step recorded: %s", step)
		}
	}
}

func TestProbeRetryOtherOutcomes(t *testing.T) {
	for _, status := range []int{429, 500} {
		s, a := newLimitServer(t, func(r limitReq) (int, string) { return status, limitRefusal })
		_, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{})
		if !errors.Is(err, transport.ErrIncompleteProbe) {
			t.Errorf("status %d: err %v", status, err)
		}
		if s.count("max_tokens", 256) != 0 {
			t.Errorf("status %d triggered a retry", status)
		}
	}
	// A 400 on the retry does not record the field; max_completion_tokens is tried.
	s, a := newLimitServer(t, func(r limitReq) (int, string) {
		if r.field == "max_tokens" {
			if r.limit == 16 {
				return 400, limitRefusal
			}
			return 400, `{"error":"nope"}`
		}
		return 200, okBody(r)
	})
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true, DisableCalibration: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.MaxTokensField != "max_completion_tokens" || s.count("max_completion_tokens", 16) != 1 {
		t.Errorf("field %q", rec.MaxTokensField)
	}
}

func TestProbeBeforeSendRefusesRetry(t *testing.T) {
	s, a := newLimitServer(t, strictLimit)
	_, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{
		BeforeSend: func(pr *transport.ProbeRequest) error {
			if pr.Step == "max_tokens_retry" {
				return errors.New("refused")
			}
			return nil
		},
	})
	if !errors.Is(err, transport.ErrNotSent) {
		t.Fatalf("err %v, want ErrNotSent", err)
	}
	if s.count("max_tokens", 256) != 0 {
		t.Error("retry was sent")
	}
	var pe *transport.ProbeStepError
	if !errors.As(err, &pe) || pe.Step != "max_tokens_retry" {
		t.Errorf("step error %v, want Step max_tokens_retry", err)
	}
}

func TestCalibrationRetryFailureStep(t *testing.T) {
	emptyTooSmall := func(r limitReq) (int, string) {
		if r.content == "" && !r.system && r.limit < 256 {
			return 400, limitRefusal
		}
		return 200, okBody(r)
	}
	var pe *transport.ProbeStepError

	_, a := newLimitServer(t, emptyTooSmall)
	_, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{
		DisableOverLength: true,
		BeforeSend: func(pr *transport.ProbeRequest) error {
			if pr.Step == "calibration_baseline_retry" {
				return errors.New("refused")
			}
			return nil
		},
	})
	if !errors.Is(err, transport.ErrNotSent) || !errors.As(err, &pe) || pe.Step != "calibration_baseline_retry" {
		t.Errorf("err %v, want ErrNotSent at calibration_baseline_retry", err)
	}

	_, a = newLimitServer(t, emptyTooSmall)
	a.client.Transport = failOnRetryLimit{http.DefaultTransport}
	_, err = a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true})
	if !errors.Is(err, transport.ErrUncertain) || !errors.As(err, &pe) || pe.Step != "calibration_baseline_retry" {
		t.Errorf("err %v, want ErrUncertain at calibration_baseline_retry", err)
	}
}

func TestCalibrationFallbackRules(t *testing.T) {
	// Empty content refused with an unrelated 400: fallback runs, no calibration retry.
	s, a := newLimitServer(t, func(r limitReq) (int, string) {
		if r.content == "" && r.field != "" && !r.system {
			return 400, `{"error":"empty content"}`
		}
		return 200, okBody(r)
	})
	// The probe step also sends "test"; only the empty baseline is refused.
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := rawSteps(t, rec)
	if _, ok := raw["calibration_baseline_nonempty"]; !ok {
		t.Error("fallback did not run")
	}
	for step := range raw {
		if strings.HasPrefix(step, "calibration") && strings.HasSuffix(step, "_retry") {
			t.Errorf("unexpected %s", step)
		}
	}
	_ = s

	// Matching 400 at 16 then non-matching 400 at 256: fallback is then sent.
	_, a = newLimitServer(t, func(r limitReq) (int, string) {
		if r.content == "" && !r.system {
			if r.limit == 16 {
				return 400, limitRefusal
			}
			return 400, `{"error":"empty content"}`
		}
		return 200, okBody(r)
	})
	rec, err = a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{DisableOverLength: true})
	if err != nil {
		t.Fatal(err)
	}
	raw = rawSteps(t, rec)
	for _, step := range []string{"calibration_baseline_retry", "calibration_baseline_nonempty"} {
		if _, ok := raw[step]; !ok {
			t.Errorf("missing %s", step)
		}
	}
}

func TestProbeRetryStepsScrubbed(t *testing.T) {
	_, a := newLimitServer(t, func(r limitReq) (int, string) {
		if r.limit < 256 {
			return 400, limitRefusal + " tok-123"
		}
		return 200, okBody(r)
	})
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rec.Raw), "tok-123") {
		t.Error("secret in raw record")
	}
}

// failOnRetryLimit fails any request sent at the retry limit.
type failOnRetryLimit struct{ next http.RoundTripper }

func (f failOnRetryLimit) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return f.next.RoundTrip(req)
	}
	raw, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(strings.NewReader(string(raw)))
	if strings.Contains(string(raw), `":256`) {
		return nil, errors.New("boom")
	}
	return f.next.RoundTrip(req)
}
