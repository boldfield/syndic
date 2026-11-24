package referee

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		finish       transport.CompletionStatus
		toolCalls    bool
		wantDecision Decision
		wantReason   string
		wantErr      bool
	}{
		{
			name:         "valid allow",
			text:         "allow This matches the user's request",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionAllow,
			wantReason:   "This matches the user's request",
			wantErr:      false,
		},
		{
			name:         "valid deny",
			text:         "deny Unrelated to the user's input",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "Unrelated to the user's input",
			wantErr:      false,
		},
		{
			name:         "allow with extra spaces trimmed",
			text:         "  allow The reason  ",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionAllow,
			wantReason:   "The reason",
			wantErr:      false,
		},
		{
			name:         "allow without reason",
			text:         "allow",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "allow with only spaces",
			text:         "allow   ",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "deny without reason",
			text:         "deny",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "empty string",
			text:         "",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "whitespace only",
			text:         "   \n  ",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "wrong prefix",
			text:         "maybe This is good",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "case sensitive allow",
			text:         "Allow This is good",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "case sensitive deny",
			text:         "Deny This is bad",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "allow with complex reason",
			text:         "allow This URL retrieves documentation for the requested library version",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionAllow,
			wantReason:   "This URL retrieves documentation for the requested library version",
			wantErr:      false,
		},
		{
			name:         "deny with complex reason",
			text:         "deny Attempting to fetch from a third-party service not mentioned in the request",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "Attempting to fetch from a third-party service not mentioned in the request",
			wantErr:      false,
		},
		{
			name:         "multiline reject",
			text:         "allow Good match\nignored line",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "truncated deny",
			text:         "allow This matches",
			finish:       transport.StatusTruncated,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "refused deny",
			text:         "allow Good",
			finish:       transport.StatusRefused,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "tool calls deny",
			text:         "allow Good",
			finish:       transport.StatusComplete,
			toolCalls:    true,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "single line with trailing whitespace",
			text:         "allow Good reason  \n",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionAllow,
			wantReason:   "Good reason",
			wantErr:      false,
		},
		// New tests for finish status strictness
		{
			name:         "interrupted status deny",
			text:         "allow This matches",
			finish:       transport.StatusInterrupted,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "error status deny",
			text:         "allow This matches",
			finish:       transport.StatusError,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		// New tests for control character rejection
		{
			name:         "carriage return deny",
			text:         "allow ok\rdeny",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
		{
			name:         "tab character deny",
			text:         "allow\tgood",
			finish:       transport.StatusComplete,
			toolCalls:    false,
			wantDecision: DecisionDeny,
			wantReason:   "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.text, tt.finish, tt.toolCalls)

			if tt.wantErr && err == nil {
				t.Errorf("Parse(%q, %q, %v) got no error, want error", tt.text, tt.finish, tt.toolCalls)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Parse(%q, %q, %v) got error %v, want nil", tt.text, tt.finish, tt.toolCalls, err)
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
			result, _ := Parse(tt.text, transport.StatusComplete, false)
			if result.Decision != DecisionDeny {
				t.Errorf("malformed input should default to deny, got %q", result.Decision)
			}
		})
	}
}

func TestParseErrorType(t *testing.T) {
	_, err := Parse("invalid", transport.StatusComplete, false)
	if err == nil {
		t.Fatal("expected error for invalid input")
	}

	parseErr, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("error type = %T, want *ParseError", err)
	}

	errStr := parseErr.Error()
	if !strings.Contains(errStr, "referee") {
		t.Errorf("error string does not contain 'referee': %s", errStr)
	}
}
