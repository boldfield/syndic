package contracttest

import (
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// isolationBase returns a conforming result and fixture for a case, so that
// each isolation row below can differ from it in exactly one thing.
func isolationBase(cse Case, req transport.CompleteRequest) (transport.CompleteResult, Fixture) {
	r := transport.CompleteResult{
		Envelope:        req.Envelope,
		Identity:        transport.NewUnknownIdentity(),
		ReportedModel:   "contract-model",
		Status:          transport.StatusError,
		TransportStatus: transport.TransportStatusOK,
	}
	f := Fixture{
		Secret:      "test-secret",
		Expected:    transport.Expected{Identity: transport.NewUnknownIdentity(), ReportedModel: "contract-model"},
		PrimaryHits: func() int { return 1 },
		ForeignHits: func() int { return 0 },
	}
	mismatched := func() {
		wrong, _ := transport.NewDigestIdentity("wrong-digest", "contract-model")
		f.Expected = transport.Expected{Identity: wrong, ReportedModel: "contract-model"}
	}
	switch cse {
	case Success:
		r.Status, r.Text, r.RawOutput, r.NativeFinish = transport.StatusComplete, "ok", "ok", "stop"
	case DeadlineExceeded:
		r.Status, r.TransportStatus = transport.StatusInterrupted, transport.TransportStatusTimeout
	case MalformedBody:
		r.TransportStatus, r.RawOutput = transport.TransportStatusMalformedBody, "garbage"
	case ConnectionReset:
		r.Status, r.TransportStatus = transport.StatusInterrupted, transport.TransportStatusReset
	case Truncated:
		r.Status, r.NativeFinish, r.Text, r.RawOutput = transport.StatusTruncated, "length", "part", "part"
	case Refused:
		r.Status, r.NativeFinish = transport.StatusRefused, "content_filter"
	case AuthFailure:
		r.TransportStatus = transport.TransportStatusUnauthorized
	case ServerError:
		r.TransportStatus = "http_500"
	case IdentityMismatch:
		r.Status, r.Text, r.RawOutput, r.NativeFinish = transport.StatusComplete, "ok", "ok", "stop"
		mismatched()
	case RedirectOtherHost:
		r.TransportStatus = "http_301"
	case SecretEcho:
		r.Status, r.NativeFinish = transport.StatusComplete, "stop"
		r.RawOutput = "Response with " + transport.ScrubMarker + " embedded"
		r.Text = r.RawOutput
		r.ScrubCount = 1
		mismatched()
	}
	return r, f
}

// TestAssertionIsolation feeds each assertion a result that differs from a
// conforming one in exactly one field, so every individual check is load-bearing.
func TestAssertionIsolation(t *testing.T) {
	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant: "p", TurnID: "t", Phase: "ph", Epoch: 1, AttemptID: "a", CandidateHash: "h",
		},
		Model:           "contract-model",
		Messages:        []transport.Message{{Role: "user", Content: "hi"}},
		MaxOutputTokens: 10,
		Deadline:        time.Now().Add(2 * time.Second),
	}
	wrongIdentity, _ := transport.NewDigestIdentity("other-digest", "contract-model")

	rows := []struct {
		name    string
		cse     Case
		elapsed time.Duration
		mutate  func(*transport.CompleteResult, *Fixture)
		wantMsg string
	}{
		// Status checks in non-approvable cases, using a non-complete wrong status.
		{"deadline_exceeded wrong non-complete status", DeadlineExceeded, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusError }, "status is"},
		{"malformed_body wrong non-complete status", MalformedBody, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusInterrupted }, "status is"},
		{"connection_reset wrong non-complete status", ConnectionReset, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusError }, "status is"},
		{"truncated wrong non-complete status", Truncated, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusInterrupted }, "status is"},
		{"refused wrong non-complete status", Refused, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusError }, "status is"},
		{"auth_failure wrong non-complete status", AuthFailure, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusInterrupted }, "status is"},
		{"server_error wrong non-complete status", ServerError, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusInterrupted }, "status is"},
		{"redirect_other_host wrong non-complete status", RedirectOtherHost, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusInterrupted }, "status is"},

		// Transport status checks.
		{"deadline_exceeded wrong transport status", DeadlineExceeded, 0, func(r *transport.CompleteResult, f *Fixture) { r.TransportStatus = transport.TransportStatusReset }, "transport status"},
		{"server_error non-5xx transport status", ServerError, 0, func(r *transport.CompleteResult, f *Fixture) { r.TransportStatus = "http_404" }, "http_5xx"},

		// Identity mismatch: envelope and status are not covered by CheckResult's identity error.
		{"identity_mismatch envelope mismatch", IdentityMismatch, 0, func(r *transport.CompleteResult, f *Fixture) { r.Envelope.AttemptID = "other" }, "envelope mismatch"},
		{"identity_mismatch non-complete status", IdentityMismatch, 0, func(r *transport.CompleteResult, f *Fixture) { r.Status = transport.StatusTruncated }, "status is"},

		// Success approval path.
		{"success wrong identity", Success, 0, func(r *transport.CompleteResult, f *Fixture) { r.Identity = wrongIdentity }, "check result"},
		{"success wrong reported model", Success, 0, func(r *transport.CompleteResult, f *Fixture) { r.ReportedModel = "other-model" }, "check result"},

		// secret_echo.
		{"secret_echo raw output contains secret alongside marker", SecretEcho, 0, func(r *transport.CompleteResult, f *Fixture) {
			r.RawOutput = "test-secret " + transport.ScrubMarker
		}, "raw output contains secret"},
		{"secret_echo no scrub marker", SecretEcho, 0, func(r *transport.CompleteResult, f *Fixture) { r.RawOutput = "Response with embedded" }, "scrub marker"},
		{"secret_echo two scrub markers", SecretEcho, 0, func(r *transport.CompleteResult, f *Fixture) {
			r.RawOutput = transport.ScrubMarker + " " + transport.ScrubMarker
		}, "scrub marker"},
		{"secret_echo empty fixture secret", SecretEcho, 0, func(r *transport.CompleteResult, f *Fixture) { f.Secret = "" }, "fixture secret is empty"},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			result, fixture := isolationBase(row.cse, req)
			elapsed := row.elapsed
			if elapsed == 0 {
				elapsed = 100 * time.Millisecond
			}
			// The unmutated row must conform, so the mutation is the only difference.
			if err := assertCase(row.cse, req, result, fixture, elapsed); err != nil {
				t.Fatalf("base result should conform, got: %v", err)
			}
			row.mutate(&result, &fixture)
			err := assertCase(row.cse, req, result, fixture, elapsed)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), string(row.cse)+":") {
				t.Errorf("error %q does not name case %q", err, row.cse)
			}
			if !strings.Contains(err.Error(), row.wantMsg) {
				t.Errorf("error %q does not mention %q", err, row.wantMsg)
			}
		})
	}
}
