package command

import (
	"math"
	"strings"
	"testing"
)

func maxFramingResult() (*Result, View) {
	hex := strings.Repeat("f", 64)
	return &Result{
			ExitCode: math.MinInt, Signal: math.MinInt, TimedOut: true, Truncated: true, CaptureComplete: true,
			OutputSHA256: hex, OutputBytesKept: math.MinInt, OutputBytesTotal: math.MinInt64,
			DurationMS: math.MinInt64, TimeoutSEffective: math.MinInt, Shell: string(Bash),
		}, View{
			SnapshotVersion: math.MinInt, OverlaySHA256: hex, ModesSHA256: hex,
		}
}

func TestRenderResultFields(t *testing.T) {
	res := &Result{ExitCode: 130, Signal: 2, TimedOut: true, Truncated: true, CaptureComplete: true, Output: "hi",
		OutputSHA256: "abc", OutputBytesKept: 2, OutputBytesTotal: 9, DurationMS: 5, TimeoutSEffective: 120, Shell: "/bin/sh"}
	got := RenderResult(res, View{SnapshotVersion: 3, OverlaySHA256: "o", ModesSHA256: "m"})
	for _, want := range []string{"exit_code: 130\n", "signal: 2\n", "timed_out: true\n", "truncated: true\n", "capture_complete: true\n",
		"output_sha256: abc\n", "output_bytes_kept: 2\n", "output_bytes_total: 9\n", "duration_ms: 5\n",
		"timeout_s_effective: 120\n", "shell: /bin/sh\n", "snapshot_version=3 overlay_sha256=o modes_sha256=m\nhi"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(RenderResult(&Result{}, View{}), "signal:") {
		t.Error("signal rendered when zero")
	}
}

func TestRenderResultFieldsWithinReserve(t *testing.T) {
	res, view := maxFramingResult()
	if n := len(RenderResult(res, view)); n > FramingBytes {
		t.Fatalf("fields are %d bytes, reserve is %d", n, FramingBytes)
	}
}
