package command

import (
	"fmt"
	"strings"
)

// FramingBytes is what the configuration reserves between max_output_bytes
// and tools.max_result_bytes for everything around the output in the tool
// result a model receives: the fields RenderResult adds and the engine's
// header, fence, delimiters and label (v7 delta, section 2, "Output").
const FramingBytes = 1024

// View is the workspace state a command ran against (v7 delta, section 2).
type View struct {
	SnapshotVersion int    `json:"snapshot_version"`
	OverlaySHA256   string `json:"overlay_sha256"`
	ModesSHA256     string `json:"modes_sha256"`
}

// RenderResult renders a command's result as the content of a tool result:
// every field of the design's result, then the output verbatim. It adds no
// fence of its own: the author tool loop fences the content once, with a
// delimiter it keeps clear of the content.
func RenderResult(res *Result, view View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\n", res.ExitCode)
	if res.Signal != 0 {
		fmt.Fprintf(&b, "signal: %d\n", res.Signal)
	}
	fmt.Fprintf(&b, "timed_out: %t\ntruncated: %t\ncapture_complete: %t\n", res.TimedOut, res.Truncated, res.CaptureComplete)
	fmt.Fprintf(&b, "output_sha256: %s\noutput_bytes_kept: %d\noutput_bytes_total: %d\n", res.OutputSHA256, res.OutputBytesKept, res.OutputBytesTotal)
	fmt.Fprintf(&b, "duration_ms: %d\ntimeout_s_effective: %d\nshell: %s\n", res.DurationMS, res.TimeoutSEffective, res.Shell)
	fmt.Fprintf(&b, "view: snapshot_version=%d overlay_sha256=%s modes_sha256=%s\n", view.SnapshotVersion, view.OverlaySHA256, view.ModesSHA256)
	b.WriteString(res.Output)
	return b.String()
}
