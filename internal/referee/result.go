package referee

import (
	"fmt"
	"strings"
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

func Parse(text string) (ParsedResult, error) {
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
