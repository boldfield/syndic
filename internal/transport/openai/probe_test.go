package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

var markerRe = regexp.MustCompile(`<<<(?:HEAD|TAIL)_\d+>>>`)

// probeServer is a configurable fake endpoint that counts requests per step.
type probeServer struct {
	mu     sync.Mutex
	counts map[string]int

	modelsStatus int
	// rejectField maps a max-tokens field name to true when it should 400.
	rejectField map[string]bool
	chatStatus  int // when non-zero, every chat request gets this status
	noUsage     bool
	overLength  string // "both", "head", "error"
	// calibration response control: prompt tokens for baseline and calibration
	calibrationBaseline int // 0 means use default (1)
	calibrationObserved int // 0 means use default (based on text size)
	calibrationStatus   int // non-zero means return this status code for calibration requests
}

func (p *probeServer) count(k string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.counts[k]
}

func (p *probeServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Server", "fake/1.0")
		if r.URL.Path == "/models" {
			p.mu.Lock()
			p.counts["models"]++
			p.mu.Unlock()
			if p.modelsStatus != 0 {
				w.WriteHeader(p.modelsStatus)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			return
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		p.mu.Lock()
		p.counts["chat"]++
		p.mu.Unlock()
		if p.chatStatus != 0 {
			w.WriteHeader(p.chatStatus)
			return
		}
		for f := range p.rejectField {
			if _, ok := req[f]; ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		msgs, _ := req["messages"].([]any)
		var system, user string
		for _, m := range msgs {
			mm := m.(map[string]any)
			if mm["role"] == "system" {
				system, _ = mm["content"].(string)
			} else {
				user, _ = mm["content"].(string)
			}
		}

		isCalibration := user == ""            // empty = baseline calibration request
		isCalibrationText := len(user) == 4096 // calibration text is exactly 4096 bytes

		if p.calibrationStatus != 0 && (isCalibration || isCalibrationText) {
			w.WriteHeader(p.calibrationStatus)
			return
		}

		answer := "ok"
		promptTokens := 1
		switch {
		case system != "":
			answer = "PING"
		case isCalibration:
			// baseline calibration request
			if p.calibrationBaseline != 0 {
				promptTokens = p.calibrationBaseline
			}
		case isCalibrationText:
			// full calibration text request
			if p.calibrationObserved != 0 {
				promptTokens = p.calibrationObserved
			} else {
				promptTokens = 1034 // default: matches acceptance test
			}
		case len(user) > 100000:
			markers := markerRe.FindAllString(user, -1)
			switch p.overLength {
			case "error":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"too long"}`))
				return
			case "head":
				answer = markers[0]
			default:
				answer = strings.Join(markers, " ")
			}
		}
		resp := map[string]any{
			"model": "m1-reported",
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": answer},
				"finish_reason": "stop",
			}},
		}
		if !p.noUsage {
			resp["usage"] = map[string]any{"prompt_tokens": promptTokens, "completion_tokens": 1}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func newProbeServer(p *probeServer) (*httptest.Server, *Adapter) {
	p.counts = map[string]int{}
	srv := httptest.NewServer(p.handler())
	profile := config.EndpointProfile{
		Name:    "test",
		BaseURL: srv.URL,
		Auth:    &config.SecretRef{Source: "env", Name: "test-secret"},
	}
	return srv, New(profile, &mockResolver{secret: "tok-123"})
}

func TestProbeCooperativeEveryField(t *testing.T) {
	p := &probeServer{}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.Probe(context.Background(), "test", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Endpoint != "test" || rec.Model != "m1" {
		t.Errorf("identity fields: %q %q", rec.Endpoint, rec.Model)
	}
	checks := map[string][2]any{
		"ModelsEndpoint":           {rec.ModelsEndpoint, transport.TriYes},
		"ModelListed":              {rec.ModelListed, transport.TriYes},
		"MaxTokensField":           {rec.MaxTokensField, "max_tokens"},
		"AuthMode":                 {rec.AuthMode, "api-key"},
		"UsageAvailable":           {rec.UsageAvailable, transport.TriYes},
		"SystemRoleHonored":        {rec.SystemRoleHonored, transport.TriYes},
		"OverLength":               {rec.OverLength, "both_markers_returned"},
		"EligibleRequiredReviewer": {rec.EligibleRequiredReviewer, transport.TriUnknown},
		"ReportedModel":            {rec.ReportedModel, "m1-reported"},
		"Streaming":                {rec.Streaming, transport.TriUnknown},
		"CancelObservable":         {rec.CancelObservable, transport.TriNo},
		"ServerVersion":            {rec.ServerVersion, "fake/1.0"},
		"AdapterVersion":           {rec.AdapterVersion, AdapterVersion},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %v, want %v", name, c[0], c[1])
		}
	}
	if rec.Identity != transport.NewUnknownIdentity() {
		t.Errorf("Identity: got %v, want unknown", rec.Identity)
	}
	if rec.ContextBound != nil || rec.OutputBound != nil {
		t.Errorf("bounds should be nil")
	}
	if rec.LatencyMillis == nil {
		t.Errorf("LatencyMillis nil")
	}
	if got := rec.FinishReasons["stop"]; got != transport.StatusComplete {
		t.Errorf("FinishReasons[stop]: got %v", got)
	}
	if len(rec.ToolRestrictions) == 0 || !strings.Contains(rec.ToolRestrictions[0], "no tool") {
		t.Errorf("ToolRestrictions: %v", rec.ToolRestrictions)
	}
	if rec.ObservedAt.IsZero() {
		t.Errorf("ObservedAt zero")
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"models", "max_tokens", "system_role", "over_length", "calibration_baseline", "calibration"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("Raw missing step %q", k)
		}
	}
	if p.count("models") != 1 || p.count("chat") != 5 {
		t.Errorf("request counts: models=%d chat=%d, want 1 and 5", p.count("models"), p.count("chat"))
	}
}

func TestProbeRequestCountsAndVariants(t *testing.T) {
	tests := []struct {
		name      string
		p         *probeServer
		skipOver  bool
		wantChat  int
		wantField string
		wantOver  string
		wantElig  transport.Tri
	}{
		{"fallback_to_max_completion_tokens", &probeServer{rejectField: map[string]bool{"max_tokens": true}}, false, 4, "max_completion_tokens", "both_markers_returned", transport.TriUnknown},
		{"both_rejected", &probeServer{rejectField: map[string]bool{"max_tokens": true, "max_completion_tokens": true}}, true, 3, "", "not_run", transport.TriUnknown},
		{"server_error_no_followup", &probeServer{chatStatus: 500}, true, 2, "", "not_run", transport.TriUnknown},
		{"over_length_errored", &probeServer{overLength: "error"}, false, 3, "max_tokens", "errored", transport.TriUnknown},
		{"head_only", &probeServer{overLength: "head"}, false, 3, "max_tokens", "silent_truncation_likely", transport.TriNo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, a := newProbeServer(tt.p)
			defer srv.Close()
			rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
				Timeout: 0, DisableOverLength: tt.skipOver, DisableCalibration: true, AssumedContextLen: 131072,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.p.count("chat"); got != tt.wantChat {
				t.Errorf("chat requests: got %d, want %d", got, tt.wantChat)
			}
			if tt.p.count("models") != 1 {
				t.Errorf("models requests: %d", tt.p.count("models"))
			}
			if rec.MaxTokensField != tt.wantField {
				t.Errorf("MaxTokensField: got %q, want %q", rec.MaxTokensField, tt.wantField)
			}
			if rec.OverLength != tt.wantOver {
				t.Errorf("OverLength: got %q, want %q", rec.OverLength, tt.wantOver)
			}
			if rec.EligibleRequiredReviewer != tt.wantElig {
				t.Errorf("Eligible: got %v, want %v", rec.EligibleRequiredReviewer, tt.wantElig)
			}
		})
	}
}

func TestProbeModelsVariants(t *testing.T) {
	p := &probeServer{modelsStatus: http.StatusNotFound}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.Probe(context.Background(), "test", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ModelsEndpoint != transport.TriNo {
		t.Errorf("404: ModelsEndpoint %v", rec.ModelsEndpoint)
	}

	// 2xx without a parseable list stays unknown.
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`))
	})
	s2 := httptest.NewServer(mux)
	defer s2.Close()
	a2 := New(config.EndpointProfile{Name: "test", BaseURL: s2.URL}, &mockResolver{})
	rec, err = a2.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{DisableOverLength: true, DisableCalibration: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ModelsEndpoint != transport.TriUnknown || rec.ModelListed != transport.TriUnknown {
		t.Errorf("unparseable list: %v %v", rec.ModelsEndpoint, rec.ModelListed)
	}
	if rec.AuthMode != "none" {
		t.Errorf("AuthMode: %q", rec.AuthMode)
	}
}

func TestProbeStep2ErrorMapping(t *testing.T) {
	for status, want := range map[int]error{
		401: transport.ErrUnauthorized,
		403: transport.ErrUnauthorized,
		404: transport.ErrModelUnavailable,
	} {
		p := &probeServer{chatStatus: status}
		srv, a := newProbeServer(p)
		_, err := a.Probe(context.Background(), "test", "m1")
		srv.Close()
		if !errors.Is(err, want) {
			t.Errorf("status %d: got %v, want %v", status, err, want)
		}
		if p.count("chat") != 1 {
			t.Errorf("status %d: chat requests %d", status, p.count("chat"))
		}
	}
}

func TestProbeClosedPortUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	a := New(config.EndpointProfile{Name: "test", BaseURL: "http://" + addr}, &mockResolver{})
	_, err = a.Probe(context.Background(), "test", "m1")
	if !errors.Is(err, transport.ErrUnreachable) {
		t.Errorf("got %v, want ErrUnreachable", err)
	}
}

func TestProbeNoUsageBlock(t *testing.T) {
	p := &probeServer{noUsage: true}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.Probe(context.Background(), "test", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.UsageAvailable != transport.TriNo {
		t.Errorf("UsageAvailable: %v", rec.UsageAvailable)
	}
}

func TestCalibrationSuccessful(t *testing.T) {
	p := &probeServer{calibrationBaseline: 10, calibrationObserved: 1034}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Calibration.Available {
		t.Errorf("Calibration.Available: got false, want true")
	}
	if rec.Calibration.BaselinePromptTokens != 10 {
		t.Errorf("BaselinePromptTokens: got %d, want 10", rec.Calibration.BaselinePromptTokens)
	}
	if rec.Calibration.ObservedPromptTokens != 1034 {
		t.Errorf("ObservedPromptTokens: got %d, want 1034", rec.Calibration.ObservedPromptTokens)
	}
	if rec.Calibration.BytesPerToken != 4.0 {
		t.Errorf("BytesPerToken: got %f, want 4.0", rec.Calibration.BytesPerToken)
	}
}

func TestCalibrationNoUsage(t *testing.T) {
	p := &probeServer{noUsage: true}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Errorf("Calibration.Available: got true, want false")
	}
}

func TestCalibrationSameTokenCount(t *testing.T) {
	p := &probeServer{calibrationBaseline: 100, calibrationObserved: 100}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Errorf("Calibration.Available: got true, want false (same count)")
	}
}

func TestCalibrationServerError(t *testing.T) {
	p := &probeServer{calibrationStatus: http.StatusInternalServerError}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Errorf("Calibration.Available: got true, want false (500 error)")
	}
}

func TestCalibrationDisabled(t *testing.T) {
	p := &probeServer{calibrationBaseline: 10, calibrationObserved: 1034}
	srv, a := newProbeServer(p)
	defer srv.Close()
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength:  true,
		DisableCalibration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Errorf("Calibration.Available: got true, want false (disabled)")
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["calibration"]; ok {
		t.Errorf("Raw should not contain calibration step when disabled")
	}
}
