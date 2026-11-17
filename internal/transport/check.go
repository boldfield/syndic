package transport

import "errors"

// CheckResult is the single gate a result must pass before it can carry a verdict that counts.
// It returns a joined error containing all applicable errors, or nil if the result is valid.
func CheckResult(req CompleteRequest, res CompleteResult, expected Expected) error {
	var errs []error

	// Check envelope match
	if res.Envelope != req.Envelope {
		errs = append(errs, ErrEnvelopeMismatch)
	}

	// Check identity and reported model match
	identityMatches := res.Identity == expected.Identity
	reportedModelMatches := res.ReportedModel == expected.ReportedModel

	// Special case: unknown identity equals unknown identity
	if !identityMatches && res.Identity.Kind == "unknown" && expected.Identity.Kind == "unknown" {
		identityMatches = true
	}

	if !identityMatches || !reportedModelMatches {
		errs = append(errs, ErrIdentityMismatch)
	}

	// Check status is complete
	if res.Status != StatusComplete {
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
