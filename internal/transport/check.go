package transport

import "errors"

// CheckResult is the single gate a result must pass before it can carry a verdict that counts.
// It returns a joined error containing all applicable errors, or nil if the result is valid.
// Tool steps (finish status tool_calls with one or more ToolCalls) are accepted as valid.
// Calls under any other finish status are rejected as malformed.
func CheckResult(req CompleteRequest, res CompleteResult, expected Expected) error {
	var errs []error

	// Check envelope match
	if res.Envelope != req.Envelope {
		errs = append(errs, ErrEnvelopeMismatch)
	}

	// Check identity and reported model match
	identityMatches := res.Identity == expected.Identity
	reportedModelMatches := res.ReportedModel == expected.ReportedModel

	if !identityMatches || !reportedModelMatches {
		errs = append(errs, ErrIdentityMismatch)
	}

	// Check status: must be complete or tool_calls, but only tool_calls
	// is valid when it carries calls and only complete is valid when it doesn't.
	// Calls under any other finish status (length, refusal, stop) are malformed.
	hasToolCalls := len(res.ToolCalls) > 0

	if res.Status == StatusToolCalls {
		if !hasToolCalls {
			errs = append(errs, errors.New("tool_calls status without tool calls"))
		}
	} else if hasToolCalls {
		// Calls under non-tool_calls finish status is malformed
		errs = append(errs, errors.New("tool calls under non-tool_calls finish status"))
	} else if res.Status != StatusComplete {
		errs = append(errs, ErrStatusNotComplete)
	}

	if len(errs) == 0 {
		return nil
	}

	if len(errs) == 1 {
		return errs[0]
	}

	return errors.Join(errs...)
}
