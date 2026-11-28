package contracttest

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// AssertSuccess validates a successful completion.
func AssertSuccess(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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
	const deadlineDuration = 300 * time.Millisecond
	allowedDuration := deadlineDuration + 500*time.Millisecond
	if elapsed > allowedDuration {
		return fmt.Errorf("deadline_exceeded: took %v, want within %v", elapsed, allowedDuration)
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("deadline_exceeded: check result should fail")
	}
	return nil
}

// AssertMalformedBody validates a malformed body case.
func AssertMalformedBody(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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
func AssertConnectionReset(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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
func AssertTruncated(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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
func AssertRefused(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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
func AssertAuthFailure(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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
func AssertServerError(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	if result.Envelope != req.Envelope {
		return errors.New("server_error: envelope mismatch")
	}
	if result.Status != transport.StatusError {
		return fmt.Errorf("server_error: status is %q, want error", result.Status)
	}
	if !strings.HasPrefix(result.TransportStatus, "http_5") {
		return fmt.Errorf("server_error: transport status is %q, want http_5xx", result.TransportStatus)
	}
	if fixture.PrimaryHits == nil {
		return errors.New("server_error: primary hits hook required")
	}
	if primaryHits := fixture.PrimaryHits(); primaryHits != 1 {
		return fmt.Errorf("server_error: primary hits is %d, want 1", primaryHits)
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("server_error: check result should fail")
	}
	return nil
}

// AssertIdentityMismatch validates an identity mismatch case.
func AssertIdentityMismatch(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
	if result.Envelope != req.Envelope {
		return errors.New("identity_mismatch: envelope mismatch")
	}
	if result.Status != transport.StatusComplete {
		return fmt.Errorf("identity_mismatch: status is %q, want complete", result.Status)
	}
	err := transport.CheckResult(req, result, fixture.Expected)
	if !errors.Is(err, transport.ErrIdentityMismatch) {
		return fmt.Errorf("identity_mismatch: check result returned %v, want ErrIdentityMismatch", err)
	}
	return nil
}

// AssertRedirectOtherHost validates a redirect to other host case.
func AssertRedirectOtherHost(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
	if result.Envelope != req.Envelope {
		return errors.New("redirect_other_host: envelope mismatch")
	}
	if result.Status != transport.StatusError {
		return fmt.Errorf("redirect_other_host: status is %q, want error", result.Status)
	}
	if fixture.ForeignHits == nil {
		return errors.New("redirect_other_host: foreign hits hook required")
	}
	if foreignHits := fixture.ForeignHits(); foreignHits != 0 {
		return fmt.Errorf("redirect_other_host: foreign hits is %d, want 0", foreignHits)
	}
	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("redirect_other_host: check result should fail")
	}
	return nil
}

// AssertSecretEcho validates that secrets are properly scrubbed.
func AssertSecretEcho(req transport.CompleteRequest, result transport.CompleteResult, fixture Fixture, elapsed time.Duration) error {
	_ = elapsed
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

	if err := transport.CheckResult(req, result, fixture.Expected); err == nil {
		return errors.New("secret_echo: check result should fail")
	}

	return nil
}

// AssertClassConsistent validates that a result's failure class agrees with
// its TransportStatus: a transport-ok result has none, and every other
// TransportStatus has the class section 7 of the v10 delta gives it.
func AssertClassConsistent(result transport.CompleteResult) error {
	ts := result.TransportStatus
	var want transport.FailureClass
	switch {
	case ts == transport.TransportStatusOK || ts == "":
		want = transport.FailureClassNone
	case ts == transport.TransportStatusUnreachable:
		want = transport.FailureClassUnreachable
	case ts == transport.TransportStatusTimeout || ts == transport.TransportStatusReset:
		want = transport.FailureClassUncertain
	case ts == transport.TransportStatusMalformedBody:
		want = transport.FailureClassBadResponse
	case ts == transport.TransportStatusUnauthorized || ts == transport.TransportStatusModelUnavailable:
		want = transport.FailureClassRejectedConfig
	case strings.HasPrefix(ts, "http_"):
		code, err := strconv.Atoi(strings.TrimPrefix(ts, "http_"))
		if err != nil {
			return fmt.Errorf("class: transport status %q is not http_<code>", ts)
		}
		want = transport.Classify(transport.ClassifyInput{Received: true, HTTPStatus: code, TransportStatus: ts})
	default:
		if result.FailureClass == transport.FailureClassNone {
			return fmt.Errorf("class: failed result with transport status %q carries no class", ts)
		}
		return nil
	}
	if result.FailureClass != want {
		return fmt.Errorf("class: transport status %q has class %q, want %q", ts, result.FailureClass, want)
	}
	return nil
}
