package referee

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantDecision Decision
		wantReason   string
		wantErr      bool
	}{
		{
			name:         "valid allow",
			text:         "allow This matches the user's request",
			wantDecision: DecisionAllow,
			wantReason:   "This matches the user's request",
			wantErr:      false,
		},
		{
			name:         "valid deny",
			text:         "deny Unrelated to the user's input",
			wantDecision: DecisionDeny,
			wantReason:   "Unrelated to the user's input",
			wantErr:      false,
		},
		{
			name:         "allow with multiline (uses first line)",
			text:         "allow Good match\nignored line",
			wantDecision: DecisionAllow,
			wantReason:   "Good match",
			wantErr:      false,
		},
		{
			name:         "deny with multiline (uses first line)",
			text:         "deny Suspicious request\nextra line",
			wantDecision: DecisionDeny,
			wantReason:   "Suspicious request",
			wantErr:      false,
		},
		{
			name:         "allow with extra spaces trimmed",
			text:         "  allow The reason  ",
			wantDecision: DecisionAllow,
			wantReason:   "The reason",
			wantErr:      false,
		},
		{
			name:         "allow without reason",
			text:         "allow",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "allow with only spaces",
			text:         "allow   ",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "deny without reason",
			text:         "deny",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "empty string",
			text:         "",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "whitespace only",
			text:         "   \n  ",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "wrong prefix",
			text:         "maybe This is good",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "case sensitive allow",
			text:         "Allow This is good",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "case sensitive deny",
			text:         "Deny This is bad",
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "allow with complex reason",
			text:         "allow This URL retrieves documentation for the requested library version",
			wantDecision: DecisionAllow,
			wantReason:   "This URL retrieves documentation for the requested library version",
			wantErr:      false,
		},
		{
			name:         "deny with complex reason",
			text:         "deny Attempting to fetch from a third-party service not mentioned in the request",
			wantDecision: DecisionDeny,
			wantReason:   "Attempting to fetch from a third-party service not mentioned in the request",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.text)

			if tt.wantErr && err == nil {
				t.Errorf("Parse(%q) got no error, want error", tt.text)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Parse(%q) got error %v, want nil", tt.text, err)
			}

			if result.Decision != tt.wantDecision {
				t.Errorf("decision = %q, want %q", result.Decision, tt.wantDecision)
			}

			if result.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", result.Reason, tt.wantReason)
			}
		})
	}
}

func TestParseResultDefaults(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"invalid prefix", "maybe yes"},
		{"missing reason", "allow"},
		{"wrong case", "Allow good"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _ := Parse(tt.text)
			if result.Decision != DecisionDeny {
				t.Errorf("malformed input should default to deny, got %q", result.Decision)
			}
		})
	}
}

func TestParseErrorType(t *testing.T) {
	_, err := Parse("invalid")
	if err == nil {
		t.Fatal("expected error for invalid input")
	}

	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("error type = %T, want *ParseError", err)
	}

	errStr := parseErr.Error()
	if !containsString(errStr, "referee") {
		t.Errorf("error string does not contain 'referee': %s", errStr)
	}
}

func containsString(text, substring string) bool {
	for i := 0; i <= len(text)-len(substring); i++ {
		if text[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
