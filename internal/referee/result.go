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
	text   string
	detail string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("referee: %s", e.detail)
}

// Parse parses a referee response text. It returns ParsedResult with Decision set
// to either DecisionAllow or DecisionDeny, depending on the response text.
// In case of any error, the result decision is DecisionDeny and an error is returned.
func Parse(text string, finishStatus transport.CompletionStatus, hasToolCalls bool) (ParsedResult, error) {
	// Reject if truncated, refused, or contains tool calls.
	if finishStatus == transport.StatusTruncated {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			text:   text,
			detail: "truncated response",
		}
	}

	if finishStatus == transport.StatusRefused {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			text:   text,
			detail: "refused response",
		}
	}

	if hasToolCalls {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			text:   text,
			detail: "response contains tool calls",
		}
	}

	text = strings.TrimSpace(text)

	if text == "" {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			text:   "",
			detail: "empty response",
		}
	}

	lines := strings.Split(text, "\n")
	if len(lines) == 0 || len(lines[0]) == 0 {
		return ParsedResult{Decision: DecisionDeny}, &ParseError{
			text:   text,
			detail: "no content",
		}
	}

	firstLine := strings.TrimSpace(lines[0])

	// Strict: only allow exactly one line.
	if len(lines) > 1 {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "" {
				return ParsedResult{Decision: DecisionDeny}, &ParseError{
					text:   text,
					detail: "response must be exactly one line",
				}
			}
		}
	}

	if strings.HasPrefix(firstLine, "allow ") {
		reason := strings.TrimPrefix(firstLine, "allow ")
		if reason == "" {
			return ParsedResult{Decision: DecisionDeny}, &ParseError{
				text:   text,
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
				text:   text,
				detail: "deny without reason",
			}
		}
		return ParsedResult{
			Decision: DecisionDeny,
			Reason:   reason,
		}, nil
	}

	return ParsedResult{Decision: DecisionDeny}, &ParseError{
		text:   text,
		detail: "first line must start with 'allow ' or 'deny '",
	}
}
