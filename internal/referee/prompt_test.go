package referee

import (
	"testing"
)

func TestPromptMessages(t *testing.T) {
	tests := []struct {
		name    string
		input   Payload
		wantLen int
		wantSys bool
		wantUsr bool
	}{
		{
			name: "basic fetch",
			input: Payload{
				UserInput:  "Find me information about Go",
				CallName:   "web_fetch",
				CallURL:    "https://golang.org",
				PriorCalls: []WebCall{},
			},
			wantLen: 2,
			wantSys: true,
			wantUsr: true,
		},
		{
			name: "with prior calls",
			input: Payload{
				UserInput: "Find more info",
				CallName:  "web_search",
				CallURL:   "golang best practices",
				PriorCalls: []WebCall{
					{Name: "web_fetch", URL: "https://example.com"},
				},
			},
			wantLen: 2,
			wantSys: true,
			wantUsr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := PromptMessages(tt.input)
			if len(msgs) != tt.wantLen {
				t.Errorf("got %d messages, want %d", len(msgs), tt.wantLen)
			}

			if tt.wantSys && msgs[0].Role != "system" {
				t.Errorf("first message role = %q, want system", msgs[0].Role)
			}

			if tt.wantUsr && msgs[1].Role != "user" {
				t.Errorf("second message role = %q, want user", msgs[1].Role)
			}

			if tt.wantUsr && msgs[1].Content == "" {
				t.Errorf("user message content is empty")
			}

			if tt.wantUsr && !containsSubstring(msgs[1].Content, tt.input.UserInput) {
				t.Errorf("user message does not contain user input")
			}

			if tt.wantUsr && !containsSubstring(msgs[1].Content, tt.input.CallName) {
				t.Errorf("user message does not contain call name")
			}

			if tt.wantUsr && !containsSubstring(msgs[1].Content, tt.input.CallURL) {
				t.Errorf("user message does not contain URL")
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	tests := []struct {
		name           string
		input          Payload
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "simple payload",
			input: Payload{
				UserInput:  "Find info",
				CallName:   "web_fetch",
				CallURL:    "https://example.com",
				PriorCalls: []WebCall{},
			},
			wantContains: []string{"user input:", "Find info", "proposed call:", "web_fetch", "https://example.com", "prior calls"},
		},
		{
			name: "with prior calls",
			input: Payload{
				UserInput: "More results",
				CallName:  "web_search",
				CallURL:   "query text",
				PriorCalls: []WebCall{
					{Name: "web_fetch", URL: "https://first.com"},
					{Name: "web_search", URL: "earlier query"},
				},
			},
			wantContains: []string{
				"user input:",
				"More results",
				"proposed call:",
				"web_search",
				"query text",
				"prior calls",
				"web_fetch",
				"https://first.com",
				"earlier query",
			},
		},
		{
			name: "no prior calls shows none",
			input: Payload{
				UserInput:  "Test",
				CallName:   "web_fetch",
				CallURL:    "https://test.com",
				PriorCalls: []WebCall{},
			},
			wantContains: []string{"prior calls", "none"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildPrompt(tt.input)

			for _, want := range tt.wantContains {
				if !containsSubstring(prompt, want) {
					t.Errorf("prompt does not contain %q", want)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if containsSubstring(prompt, notWant) {
					t.Errorf("prompt contains %q but should not", notWant)
				}
			}
		})
	}
}

func TestTemplateHash(t *testing.T) {
	hash1 := TemplateHash()
	hash2 := TemplateHash()

	if hash1 == "" {
		t.Errorf("TemplateHash returned empty string")
	}

	if hash1 != hash2 {
		t.Errorf("TemplateHash not idempotent: %q != %q", hash1, hash2)
	}

	const expectedLen = 64
	if len(hash1) != expectedLen {
		t.Errorf("hash length = %d, want %d (SHA-256 hex)", len(hash1), expectedLen)
	}
}

func containsSubstring(text, substring string) bool {
	for i := 0; i <= len(text)-len(substring); i++ {
		if text[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
