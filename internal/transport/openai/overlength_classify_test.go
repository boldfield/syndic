package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/transport"
)

const classifySecret = "sk-classify-secret-value"

func probeWithRefusal(t *testing.T, status int, body string) transport.CapabilityRecord {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		msgs, _ := req["messages"].([]any)
		var user string
		for _, m := range msgs {
			mm := m.(map[string]any)
			if mm["role"] != "system" {
				user, _ = mm["content"].(string)
			}
		}
		if len(user) > 100000 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		promptTokens := 1
		if user == "" {
			promptTokens = 10
		} else if len(user) == 4096 {
			promptTokens = 1034
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "m1",
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": promptTokens, "completion_tokens": 1},
		})
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	a := New(config.EndpointProfile{
		Name:    "test",
		BaseURL: s.URL,
		Auth:    &config.SecretRef{Name: "test-secret"},
	}, &mockResolver{secret: classifySecret})
	rec, err := a.ProbeWithOptions(context.Background(), "test", "m1", transport.ProbeOptions{AssumedContextLen: 131072})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	return rec
}

func TestOverLengthClassificationMatrix(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
		detail string
	}{
		{"prompt_too_long", 400, `{"error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, "rejected:context", "prompt is too long: 250000 tokens > 200000 maximum"},
		{"error_code", 400, `{"error":{"code":"context_length_exceeded","message":"x"}}`, "rejected:context", "x"},
		{"error_type", 400, `{"error":{"type":"Context_Length_Exceeded","message":"y"}}`, "rejected:context", "y"},
		{"top_level_code", 400, `{"code":"context_length_exceeded","message":"z"}`, "rejected:context", "z"},
		{"top_level_type", 413, `{"type":"context_length_exceeded"}`, "rejected:context", `{"type":"context_length_exceeded"}`},
		{"413_plain", 413, `This model's maximum context length is 8192 tokens`, "rejected:context", `This model's maximum context length is 8192 tokens`},
		{"empty_content", 400, `{"error":{"message":"messages.0: user messages must have non-empty content"}}`, "rejected:other", "messages.0: user messages must have non-empty content"},
		{"429_too_many_tokens", 429, `too many tokens`, "rejected:other", "too many tokens"},
		{"401_context_window", 401, `context window`, "rejected:other", "context window"},
		{"500_context_window", 500, `context window`, "rejected:other", "context window"},
		{"400_empty", 400, ``, "rejected:other", ""},
		{"400_not_json", 400, `<html>bad gateway</html>`, "rejected:other", "<html>bad gateway</html>"},
		{"400_json_array", 400, `[1,2]`, "rejected:other", "[1,2]"},
		{"error_string", 400, `{"error":"nope"}`, "rejected:other", `{"error":"nope"}`},
		{"secret_in_message", 400, `{"error":{"message":"context window exceeded for key ` + classifySecret + `"}}`, "rejected:context", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := probeWithRefusal(t, tt.status, tt.body)
			if rec.OverLength != tt.want {
				t.Errorf("OverLength: got %q, want %q", rec.OverLength, tt.want)
			}
			if strings.Contains(rec.OverLengthDetail, classifySecret) {
				t.Errorf("detail leaks secret: %q", rec.OverLengthDetail)
			}
			if tt.name != "secret_in_message" && rec.OverLengthDetail != tt.detail {
				t.Errorf("detail: got %q, want %q", rec.OverLengthDetail, tt.detail)
			}
			if len(rec.OverLengthDetail) > 200 || !utf8.ValidString(rec.OverLengthDetail) {
				t.Errorf("detail not bounded valid UTF-8: %q", rec.OverLengthDetail)
			}
			if tt.name == "prompt_too_long" && rec.EligibleRequiredReviewer != transport.TriYes {
				t.Errorf("eligibility: got %v, want yes", rec.EligibleRequiredReviewer)
			}
		})
	}
}

func TestOverLengthPhrasesMixedCase(t *testing.T) {
	for _, phrase := range contextLengthPhrases {
		mixed := strings.ToUpper(phrase[:1]) + phrase[1:]
		for _, body := range []string{mixed, strings.ToUpper(phrase)} {
			got, _ := classifyRejectionError(400, []byte(`{"error":{"message":"`+body+`"}}`))
			if got != "rejected:context" {
				t.Errorf("phrase %q: got %q", body, got)
			}
		}
	}
}

func TestOverLengthRuleNamesEveryPhrase(t *testing.T) {
	for _, phrase := range contextLengthPhrases {
		if !strings.Contains(overLengthClassificationRule, phrase) {
			t.Errorf("rule does not name %q", phrase)
		}
	}
}

func TestOverLengthDetailTruncatesOnRuneBoundary(t *testing.T) {
	msg := strings.Repeat("é", 150) // 300 bytes
	rec := probeWithRefusal(t, 400, `{"error":{"message":"context window `+msg+`"}}`)
	if rec.OverLength != "rejected:context" {
		t.Fatalf("OverLength: got %q", rec.OverLength)
	}
	if len(rec.OverLengthDetail) > 200 || len(rec.OverLengthDetail) < 198 || !utf8.ValidString(rec.OverLengthDetail) {
		t.Errorf("bad detail length %d: %q", len(rec.OverLengthDetail), rec.OverLengthDetail)
	}
}

func TestOverLengthDetailExportedAndOldExportLoads(t *testing.T) {
	exp := evidence.Export{OverLengthDetail: "why"}
	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"over_length_detail":"why"`) {
		t.Errorf("detail not exported: %s", data)
	}
	var old evidence.Export
	if err := json.Unmarshal([]byte(`{"over_length":"rejected:other","over_length_classifier":"r"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.OverLengthDetail != "" {
		t.Errorf("unexpected detail %q", old.OverLengthDetail)
	}
}

func TestOverLengthDetailSecretAcrossCut(t *testing.T) {
	for pad := 170; pad <= 200; pad += 5 {
		msg := "context window " + strings.Repeat("a", pad) + classifySecret
		rec := probeWithRefusal(t, 400, `{"error":{"message":"`+msg+`"}}`)
		for n := 8; n <= len(classifySecret); n++ {
			if strings.Contains(rec.OverLengthDetail, classifySecret[:n]) {
				t.Fatalf("pad %d: detail leaks secret prefix %q: %q", pad, classifySecret[:n], rec.OverLengthDetail)
			}
		}
		if len(rec.OverLengthDetail) > 200 {
			t.Errorf("pad %d: detail too long", pad)
		}
	}
}
