package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

var markerRe = regexp.MustCompile(`<<<(?:HEAD|TAIL)_\d+>>>`)
var hexMarkerRe = regexp.MustCompile(`[0-9a-f]{32}`)

// probeServer is a configurable fake endpoint that counts requests per step.
type probeServer struct {
	mu     sync.Mutex
	counts map[string]int

	modelsStatus int
	// rejectField maps a max-tokens field name to true when it should 400.
	rejectField map[string]bool
	chatStatus  int // when non-zero, every chat request gets this status
	noUsage     bool
	overLength  string // "both", "head", "error", "error:context"
	// calibration response control: prompt tokens for baseline and calibration
	calibrationBaseline      int  // 0 means use default (1)
	calibrationObserved      int  // 0 means use default (based on text size)
	calibrationStatus        int  // non-zero means return this status code for all calibration requests
	calibrationTextStatus    int  // non-zero means return this status code for calibration text request only (overrides calibrationStatus for text)
	echoAuthInResponse       bool // when true, echo Authorization header in response for calibration requests
	omitBaselinePromptTokens bool // when true, omit prompt_tokens from baseline response usage
	omitTextPromptTokens     bool // when true, omit prompt_tokens from calibration text response usage
	truncateCalibrationText  bool // when true, the calibration text response is cut short after its headers
	// calibration request tracking
	calibrationRequests []*map[string]any // captured request bodies for calibration requests
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

		if isCalibration || isCalibrationText {
			p.mu.Lock()
			reqCopy := req
			p.calibrationRequests = append(p.calibrationRequests, &reqCopy)
			p.mu.Unlock()
		}

		if isCalibrationText && p.truncateCalibrationText {
			if conn, buf, err := w.(http.Hijacker).Hijack(); err == nil {
				_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"usage\":")
				_ = buf.Flush()
				_ = conn.Close()
			}
			return
		}

		// Handle calibration-specific error responses
		if isCalibrationText && p.calibrationTextStatus != 0 {
			w.WriteHeader(p.calibrationTextStatus)
			return
		}
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
			// Try to find old-style markers first, then hex markers (new style)
			markers := markerRe.FindAllString(user, -1)
			if len(markers) == 0 {
				// Try to find new hex markers (32-char hex strings)
				markers = hexMarkerRe.FindAllString(user, -1)
				if len(markers) > 0 {
					markers = markers[:2] // Take only first two (head and tail)
				}
			}
			switch p.overLength {
			case "error":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"too long"}`))
				return
			case "error:context":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"context_length_exceeded"}`))
				return
			case "head":
				if len(markers) > 0 {
					answer = markers[0]
				}
			default:
				if len(markers) >= 2 {
					answer = strings.Join(markers[:2], " ")
				} else if len(markers) > 0 {
					answer = markers[0]
				}
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
			usage := map[string]any{"completion_tokens": 1}
			// Conditionally include prompt_tokens based on the request type
			if isCalibration && !p.omitBaselinePromptTokens {
				usage["prompt_tokens"] = promptTokens
			} else if isCalibrationText && !p.omitTextPromptTokens {
				usage["prompt_tokens"] = promptTokens
			} else if !isCalibration && !isCalibrationText {
				// Always include prompt_tokens for non-calibration requests
				usage["prompt_tokens"] = promptTokens
			}
			resp["usage"] = usage
		}
		// Echo Authorization header in response for calibration requests when testing scrubbing
		if p.echoAuthInResponse && (isCalibration || isCalibrationText) {
			resp["auth"] = r.Header.Get("Authorization")
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
		"OverLength":               {rec.OverLength, "accepted_whole"},
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
		{"fallback_to_max_completion_tokens", &probeServer{calibrationBaseline: 1, calibrationObserved: 1034, rejectField: map[string]bool{"max_tokens": true}}, false, 6, "max_completion_tokens", "accepted_whole", transport.TriUnknown},
		{"both_rejected", &probeServer{calibrationBaseline: 1, calibrationObserved: 1034, rejectField: map[string]bool{"max_tokens": true, "max_completion_tokens": true}}, true, 3, "", "not_run", transport.TriUnknown},
		{"server_error_no_followup", &probeServer{chatStatus: 500}, true, 2, "", "not_run", transport.TriUnknown},
		{"over_length_other_rejected", &probeServer{calibrationBaseline: 1, calibrationObserved: 1034, overLength: "error"}, false, 5, "max_tokens", "rejected:other", transport.TriUnknown},
		{"over_length_markers_missing", &probeServer{calibrationBaseline: 1, calibrationObserved: 1034, overLength: "head"}, false, 5, "max_tokens", "markers_missing", transport.TriUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, a := newProbeServer(tt.p)
			defer srv.Close()
			// Enable calibration for tests that need it (when not skipping over-length)
			disableCalibration := tt.skipOver
			rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
				Timeout: 0, DisableOverLength: tt.skipOver, DisableCalibration: disableCalibration, AssumedContextLen: 131072,
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
	// Verify that probe itself succeeds despite calibration failure
	if rec.ModelsEndpoint == transport.TriUnknown {
		t.Errorf("Probe failed: ModelsEndpoint is still unknown")
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
	// Return 500 to calibration text request only (baseline should succeed)
	p := &probeServer{calibrationTextStatus: http.StatusInternalServerError}
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

	// Compare with disabled calibration to verify rest of record is unchanged
	p2 := &probeServer{}
	srv2, a2 := newProbeServer(p2)
	defer srv2.Close()
	recDisabled, errDisabled := a2.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength:  true,
		DisableCalibration: true,
	})
	if errDisabled != nil {
		t.Fatal(errDisabled)
	}

	// Compare all fields except Calibration, Raw, LatencyMillis, and ObservedAt (which legitimately differ)
	// Use reflect.DeepEqual for comprehensive comparison
	rec.Calibration = transport.Calibration{} // zero out for comparison
	rec.Raw = nil
	rec.LatencyMillis = nil
	rec.ObservedAt = recDisabled.ObservedAt // use same timestamp for comparison
	recDisabled.Calibration = transport.Calibration{}
	recDisabled.Raw = nil
	recDisabled.LatencyMillis = nil
	if !reflect.DeepEqual(rec, recDisabled) {
		t.Errorf("Records differ (excluding Calibration, Raw, LatencyMillis, ObservedAt).\nWith error:\n%+v\nWithout:\n%+v", rec, recDisabled)
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
	if _, ok := raw["calibration_baseline"]; ok {
		t.Errorf("Raw should not contain calibration_baseline step when disabled")
	}
	if len(p.calibrationRequests) != 0 {
		t.Errorf("Server received %d calibration requests, want 0", len(p.calibrationRequests))
	}
}

func TestCalibrationTextLength(t *testing.T) {
	if len([]byte(calibrationText)) != 4096 {
		t.Errorf("calibrationText length: got %d, want 4096", len([]byte(calibrationText)))
	}
}

func TestCalibrationRequestBody(t *testing.T) {
	p := &probeServer{calibrationBaseline: 10, calibrationObserved: 1034}
	srv, a := newProbeServer(p)
	defer srv.Close()
	_, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.calibrationRequests) != 2 {
		t.Fatalf("Expected 2 calibration requests, got %d", len(p.calibrationRequests))
	}

	// Check baseline request (empty message)
	baselineReq := p.calibrationRequests[0]
	msgs, _ := (*baselineReq)["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("No messages in baseline request")
	}
	msg := msgs[0].(map[string]any)
	content, _ := msg["content"].(string)
	if content != "" {
		t.Errorf("Baseline request content: got %q, want empty", content)
	}

	// Check calibration text request
	calibReq := p.calibrationRequests[1]
	msgs, _ = (*calibReq)["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("No messages in calibration request")
	}
	msg = msgs[0].(map[string]any)
	content, _ = msg["content"].(string)
	if len([]byte(content)) != 4096 {
		t.Errorf("Calibration request text length: got %d, want 4096", len([]byte(content)))
	}
	if content != calibrationText {
		t.Errorf("Calibration request text does not match calibrationText constant")
	}
}

func TestCalibrationWithSentinelSecret(t *testing.T) {
	p := &probeServer{
		calibrationBaseline: 10,
		calibrationObserved: 1034,
		echoAuthInResponse:  true, // Server echoes Authorization header in response
	}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Calibration.Available {
		t.Fatal("Calibration not available")
	}

	// Check that the raw steps contain scrubbed secrets
	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Raw, &raw); err != nil {
		t.Fatal(err)
	}

	// Convert the entire raw steps JSON to string to check for unscrubbedSecret
	rawJSON := string(rec.Raw)

	// Check calibration_baseline raw step
	if baselineStep, ok := raw["calibration_baseline"]; !ok {
		t.Fatal("calibration_baseline not in raw steps")
	} else {
		// Check that neither the response body nor the whole step contains the secret
		body, _ := baselineStep["body"].(string)
		if strings.Contains(body, "tok-123") {
			t.Errorf("calibration_baseline raw step body contains unscrubbed secret 'tok-123'")
		}
	}

	// Check calibration raw step
	if calibStep, ok := raw["calibration"]; !ok {
		t.Fatal("calibration not in raw steps")
	} else {
		// Check that neither the response body nor the whole step contains the secret
		body, _ := calibStep["body"].(string)
		if strings.Contains(body, "tok-123") {
			t.Errorf("calibration raw step body contains unscrubbed secret 'tok-123'")
		}
	}

	// Check the whole JSON to ensure the secret is scrubbed everywhere
	if strings.Contains(rawJSON, "tok-123") {
		t.Errorf("Raw JSON contains unscrubbed secret 'tok-123'")
	}
}

func TestCalibrationMissingBaselinePromptTokens(t *testing.T) {
	p := &probeServer{
		calibrationBaseline:      10,
		calibrationObserved:      1034,
		omitBaselinePromptTokens: true,
	}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Errorf("Calibration.Available: got true, want false (missing baseline prompt_tokens)")
	}
}

func TestCalibrationMissingTextPromptTokens(t *testing.T) {
	p := &probeServer{
		calibrationBaseline:  10,
		calibrationObserved:  1034,
		omitTextPromptTokens: true,
	}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Calibration.Available {
		t.Errorf("Calibration.Available: got true, want false (missing text prompt_tokens)")
	}
}

func TestCalibrationResponseReadFailure(t *testing.T) {
	p := &probeServer{truncateCalibrationText: true}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		DisableOverLength: true,
	})
	if err != nil {
		t.Fatalf("probe should still succeed: %v", err)
	}
	if rec.Calibration != (transport.Calibration{}) {
		t.Errorf("Calibration: got %+v, want zero value", rec.Calibration)
	}
}

func TestProbeOverLengthNoContext(t *testing.T) {
	p := &probeServer{calibrationBaseline: 10, calibrationObserved: 1034}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		AssumedContextLen: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OverLength != "not_run:no_context" {
		t.Errorf("OverLength: got %q, want not_run:no_context", rec.OverLength)
	}
	if rec.EligibleRequiredReviewer != transport.TriUnknown {
		t.Errorf("EligibleRequiredReviewer: got %v, want unknown", rec.EligibleRequiredReviewer)
	}
}

func TestProbeOverLengthNoCalibration(t *testing.T) {
	p := &probeServer{calibrationBaseline: 100, calibrationObserved: 100}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		AssumedContextLen: 131072,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OverLength != "not_run:no_calibration" {
		t.Errorf("OverLength: got %q, want not_run:no_calibration", rec.OverLength)
	}
	if rec.EligibleRequiredReviewer != transport.TriUnknown {
		t.Errorf("EligibleRequiredReviewer: got %v, want unknown", rec.EligibleRequiredReviewer)
	}
}

func TestProbeOverLengthMarkerEmbedded(t *testing.T) {
	p := &probeServer{
		calibrationBaseline: 10,
		calibrationObserved: 1034,
		overLength:          "both",
	}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		AssumedContextLen: 131072,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OverLength != "accepted_whole" {
		t.Errorf("OverLength: got %q, want accepted_whole (markers without delimiters)", rec.OverLength)
	}
}

func TestProbeOverLengthContextRejection(t *testing.T) {
	p := &probeServer{
		calibrationBaseline: 10,
		calibrationObserved: 1034,
		overLength:          "error:context",
	}
	srv, a := newProbeServer(p)
	defer srv.Close()

	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
		AssumedContextLen: 131072,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.OverLength != "rejected:context" {
		t.Errorf("OverLength: got %q, want rejected:context", rec.OverLength)
	}
	if rec.EligibleRequiredReviewer != transport.TriYes {
		t.Errorf("EligibleRequiredReviewer: got %v, want yes", rec.EligibleRequiredReviewer)
	}
}

func TestProbeOverLengthOtherRejections(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantElig   transport.Tri
	}{
		{"auth_401", 401, `{"error":"invalid api token"}`, transport.TriUnknown},
		{"forbidden_403", 403, `{"error":"forbidden"}`, transport.TriUnknown},
		{"rate_limit_429", 429, `{"error":"too many requests"}`, transport.TriUnknown},
		{"policy_error_400", 400, `{"error":"content policy violation, max_tokens not allowed"}`, transport.TriUnknown},
		{"generic_400", 400, `{"error":"bad request"}`, transport.TriUnknown},
		{"generic_413", 413, `{"error":"payload too large"}`, transport.TriUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Server", "fake/1.0")
				_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
			})
			mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Server", "fake/1.0")
				body, _ := io.ReadAll(r.Body)
				var req map[string]any
				_ = json.Unmarshal(body, &req)
				msgs, _ := req["messages"].([]any)
				var user string
				for _, m := range msgs {
					mm := m.(map[string]any)
					if mm["role"] != "system" {
						user, _ = mm["content"].(string)
					}
				}

				if len(user) > 100000 {
					w.WriteHeader(tt.statusCode)
					_, _ = w.Write([]byte(tt.body))
					return
				}

				promptTokens := 1
				if user == "" {
					promptTokens = 10
				} else if len(user) == 4096 {
					promptTokens = 100
				}

				resp := map[string]any{
					"model": "m1",
					"choices": []any{map[string]any{
						"message":       map[string]any{"role": "assistant", "content": "ok"},
						"finish_reason": "stop",
					}},
					"usage": map[string]any{
						"prompt_tokens":     promptTokens,
						"completion_tokens": 1,
					},
				}
				_ = json.NewEncoder(w).Encode(resp)
			})
			s := httptest.NewServer(mux)
			defer s.Close()
			a := New(config.EndpointProfile{Name: "test", BaseURL: s.URL}, &mockResolver{})
			rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", ProbeOptions{
				AssumedContextLen: 131072,
			})
			if err != nil {
				t.Fatalf("probe failed: %v", err)
			}
			if rec.OverLength != "rejected:other" {
				t.Errorf("OverLength: got %q, want rejected:other", rec.OverLength)
			}
			if rec.EligibleRequiredReviewer != transport.TriUnknown {
				t.Errorf("EligibleRequiredReviewer: got %v, want unknown", rec.EligibleRequiredReviewer)
			}
		})
	}
}
