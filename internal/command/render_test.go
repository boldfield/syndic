package command

import (
	"math"
	"strings"
	"testing"
)

func maxFramingResult() (*Result, View) {
	hex := strings.Repeat("f", 64)
	return &Result{
			ExitCode: math.MaxInt, Signal: math.MaxInt, TimedOut: false, Truncated: false, CaptureComplete: false,
			OutputSHA256: hex, OutputBytesKept: math.MaxInt, OutputBytesTotal: math.MaxInt64,
			DurationMS: math.MaxInt64, TimeoutSEffective: math.MaxInt, Shell: string(Bash),
		}, View{
			SnapshotVersion: math.MaxInt, OverlaySHA256: hex, ModesSHA256: hex,
		}
}

func TestRenderResultFields(t *testing.T) {
	res := &Result{ExitCode: 130, Signal: 2, TimedOut: true, Truncated: true, CaptureComplete: true, Output: "hi",
		OutputSHA256: "abc", OutputBytesKept: 2, OutputBytesTotal: 9, DurationMS: 5, TimeoutSEffective: 120, Shell: "/bin/sh"}
	got := RenderResult(res, View{SnapshotVersion: 3, OverlaySHA256: "o", ModesSHA256: "m"}, strings.Repeat("a", DelimiterLen))
	for _, want := range []string{"exit_code: 130\n", "signal: 2\n", "timed_out: true\n", "truncated: true\n", "capture_complete: true\n",
		"output_sha256: abc\n", "output_bytes_kept: 2\n", "output_bytes_total: 9\n", "duration_ms: 5\n",
		"timeout_s_effective: 120\n", "shell: /bin/sh\n", "snapshot_version=3 overlay_sha256=o modes_sha256=m\n",
		"BEGIN COMMAND OUTPUT =====\nhi\n=====", "END COMMAND OUTPUT =====\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(RenderResult(&Result{}, View{}, "d"), "signal:") {
		t.Error("signal rendered when zero")
	}
}

func TestRenderResultAtMaximumWithinResultBound(t *testing.T) {
	const maxResultBytes = 65536
	res, view := maxFramingResult()
	res.Output = strings.Repeat("x", maxResultBytes-FramingBytes-1) + "\n"
	got := RenderResult(res, view, strings.Repeat("f", DelimiterLen))
	if len(got) > maxResultBytes {
		t.Fatalf("rendered %d bytes, over %d", len(got), maxResultBytes)
	}
	// An output without a trailing newline costs one more byte; the
	// reserve still covers it.
	res.Output = strings.Repeat("x", maxResultBytes-FramingBytes)
	if got := RenderResult(res, view, strings.Repeat("f", DelimiterLen)); len(got) > maxResultBytes {
		t.Fatalf("rendered %d bytes, over %d", len(got), maxResultBytes)
	}
}

func TestRenderResultFramingWithinReserve(t *testing.T) {
	res, view := maxFramingResult()
	res.TimedOut, res.Truncated, res.CaptureComplete = true, true, true
	if n := len(RenderResult(res, view, strings.Repeat("f", DelimiterLen))); n > FramingBytes {
		t.Fatalf("framing is %d bytes, reserve is %d", n, FramingBytes)
	}
}
