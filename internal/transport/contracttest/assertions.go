package contracttest

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// AssertSuccess validates a successful completion.
func AssertSuccess(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("success: envelope mismatch")
	}
	if result.Status != transport.StatusComplete {
		return fmt.Errorf("success: status is %q, want complete", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusOK {
		return fmt.Errorf("success: transport status is %q, want ok", result.TransportStatus)
	}
	if result.ScrubCount != 0 {
		return fmt.Errorf("success: scrub count is %d, want 0", result.ScrubCount)
	}
	if result.Text == "" {
		return errors.New("success: text is empty")
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err != nil {
		return fmt.Errorf("success: check result failed: %w", err)
	}
	return nil
}

// AssertDeadlineExceeded validates a deadline exceeded case.
func AssertDeadlineExceeded(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	if result.Envelope != req.Envelope {
		return errors.New("deadline_exceeded: envelope mismatch")
	}
	if result.Status != transport.StatusInterrupted {
		return fmt.Errorf("deadline_exceeded: status is %q, want interrupted", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusTimeout {
		return fmt.Errorf("deadline_exceeded: transport status is %q, want timeout", result.TransportStatus)
	}
	if elapsed > 500*time.Millisecond+req.Deadline.Sub(time.Now().Add(-elapsed)) {
		return fmt.Errorf("deadline_exceeded: took %v, want within %v + 500ms", elapsed, req.Deadline.Sub(time.Now().Add(-elapsed)))
	}
	return nil
}

// AssertMalformedBody validates a malformed body case.
func AssertMalformedBody(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("malformed_body: envelope mismatch")
	}
	if result.Status != transport.StatusError {
		return fmt.Errorf("malformed_body: status is %q, want error", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusMalformedBody {
		return fmt.Errorf("malformed_body: transport status is %q, want malformed_body", result.TransportStatus)
	}
	if result.RawOutput == "" {
		return errors.New("malformed_body: raw output is empty")
	}
	if result.Text != "" {
		return errors.New("malformed_body: text must be empty")
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("malformed_body: check result should fail")
	}
	return nil
}

// AssertConnectionReset validates a connection reset case.
func AssertConnectionReset(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("connection_reset: envelope mismatch")
	}
	if result.Status != transport.StatusInterrupted {
		return fmt.Errorf("connection_reset: status is %q, want interrupted", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusReset {
		return fmt.Errorf("connection_reset: transport status is %q, want reset", result.TransportStatus)
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("connection_reset: check result should fail")
	}
	return nil
}

// AssertTruncated validates a truncated case.
func AssertTruncated(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("truncated: envelope mismatch")
	}
	if result.Status != transport.StatusTruncated {
		return fmt.Errorf("truncated: status is %q, want truncated", result.Status)
	}
	if result.NativeFinish == "" {
		return errors.New("truncated: native finish is empty")
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("truncated: check result should fail")
	}
	return nil
}

// AssertRefused validates a refused case.
func AssertRefused(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("refused: envelope mismatch")
	}
	if result.Status != transport.StatusRefused {
		return fmt.Errorf("refused: status is %q, want refused", result.Status)
	}
	if result.NativeFinish == "" {
		return errors.New("refused: native finish is empty")
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("refused: check result should fail")
	}
	return nil
}

// AssertAuthFailure validates an auth failure case.
func AssertAuthFailure(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("auth_failure: envelope mismatch")
	}
	if result.Status != transport.StatusError {
		return fmt.Errorf("auth_failure: status is %q, want error", result.Status)
	}
	if result.TransportStatus != transport.TransportStatusUnauthorized {
		return fmt.Errorf("auth_failure: transport status is %q, want unauthorized", result.TransportStatus)
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("auth_failure: check result should fail")
	}
	return nil
}

// AssertServerError validates a server error case.
func AssertServerError(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("server_error: envelope mismatch")
	}
	if result.Status != transport.StatusError {
		return fmt.Errorf("server_error: status is %q, want error", result.Status)
	}
	if !strings.HasPrefix(result.TransportStatus, "http_5") {
		return fmt.Errorf("server_error: transport status is %q, want http_5xx", result.TransportStatus)
	}
	if fixture.PrimaryHits != nil {
		if primaryHits := fixture.PrimaryHits(); primaryHits != 1 {
			return fmt.Errorf("server_error: primary hits is %d, want 1", primaryHits)
		}
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("server_error: check result should fail")
	}
	return nil
}

// AssertIdentityMismatch validates an identity mismatch case.
func AssertIdentityMismatch(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("identity_mismatch: envelope mismatch")
	}
	if result.Status != transport.StatusComplete {
		return fmt.Errorf("identity_mismatch: status is %q, want complete", result.Status)
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err != transport.ErrIdentityMismatch {
		return fmt.Errorf("identity_mismatch: check result returned %v, want ErrIdentityMismatch", err)
	}
	return nil
}

// AssertRedirectOtherHost validates a redirect to other host case.
func AssertRedirectOtherHost(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("redirect_other_host: envelope mismatch")
	}
	if result.Status != transport.StatusError {
		return fmt.Errorf("redirect_other_host: status is %q, want error", result.Status)
	}
	if fixture.ForeignHits != nil {
		if foreignHits := fixture.ForeignHits(); foreignHits != 0 {
			return fmt.Errorf("redirect_other_host: foreign hits is %d, want 0", foreignHits)
		}
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("redirect_other_host: check result should fail")
	}
	return nil
}

// AssertSecretEcho validates that secrets are properly scrubbed.
func AssertSecretEcho(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture) error {
	if result.Envelope != req.Envelope {
		return errors.New("secret_echo: envelope mismatch")
	}

	secret := fixture.Secret
	if secret == "" {
		return errors.New("secret_echo: fixture secret is empty")
	}

	// Raw output must not contain the secret
	if strings.Contains(result.RawOutput, secret) {
		return errors.New("secret_echo: raw output contains secret")
	}

	// Raw output must contain the scrub marker exactly once
	scrubMarkerCount := strings.Count(result.RawOutput, transport.ScrubMarker)
	if scrubMarkerCount != 1 {
		return fmt.Errorf("secret_echo: scrub marker appears %d times, want 1", scrubMarkerCount)
	}

	// Text must not contain the secret
	if strings.Contains(result.Text, secret) {
		return errors.New("secret_echo: text contains secret")
	}

	// Scrub count must be 1
	if result.ScrubCount != 1 {
		return fmt.Errorf("secret_echo: scrub count is %d, want 1", result.ScrubCount)
	}

	return nil
}
