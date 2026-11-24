package referee

import (
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/transport"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

type ParsedResult struct {
	Decision Decision
	Reason   string
}

type ParseError struct {
	detail string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("referee: %s", e.detail)
}

// Parse parses a referee response text. It returns ParsedResult with Decision set
// to either DecisionAllow or DecisionDeny, depending on the response text.
// In case of any error, the result decision is DecisionDeny and an error is returned.
// finishStatus must be StatusComplete; any other status causes a deny.
// hasToolCalls indicates if the response contains tool calls, which also causes a deny.
func Parse(text string, finishStatus transport.CompletionStatus, hasToolCalls bool) (ParsedResult, error) {
	// Only StatusComplete is acceptable; deny all other statuses.
	if finishStatus != transport.StatusComplete {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			detail: fmt.Sprintf("non-complete finish status: %v", finishStatus),
		}
	}

	// Reject if contains tool calls.
	if hasToolCalls {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			detail: "response contains tool calls",
		}
	}

	text = strings.TrimSpace(text)

	if text == "" {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			detail: "empty response",
		}
	}

	// Check for control characters (CR, etc) which indicate malformed input.
	if strings.ContainsAny(text, "\r\t") {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			detail: "response contains control characters",
		}
	}

	// Split on both \r\n and \n to be strict about line breaks.
	lines := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r'
	})

	if len(lines) == 0 {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			detail: "no content",
		}
	}

	firstLine := lines[0]

	// Strict: only allow exactly one non-empty line.
	if len(lines) > 1 {
		for i := 1; i < len(lines); i++ {
			if lines[i] != "" {
				return ParsedResult{Decision: DecisionDeny}, &ParseError{
					detail: "response must be exactly one line",
				}
			}
		}
	}

	if strings.HasPrefix(firstLine, "allow ") {
		reason := strings.TrimPrefix(firstLine, "allow ")
		if reason == "" {
			return ParsedResult{Decision: DecisionDeny}, &ParseError{
				detail: "allow without reason",
			}
		}
		return ParsedResult{
			Decision: DecisionAllow,
			Reason:   reason,
		}, nil
	}

	if strings.HasPrefix(firstLine, "deny ") {
		reason := strings.TrimPrefix(firstLine, "deny ")
		if reason == "" {
			return ParsedResult{Decision: DecisionDeny}, &ParseError{
				detail: "deny without reason",
			}
		}
		return ParsedResult{
			Decision: DecisionDeny,
			Reason:   reason,
		}, nil
	}

	return ParsedResult{Decision: DecisionDeny}, &ParseError{
		detail: "first line must start with 'allow ' or 'deny '",
	}
}
