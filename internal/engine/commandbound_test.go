package engine

import (
	"crypto/rand"
	"math"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/command"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
)

// maxFramingResult is the result whose fields render longest: signed
// integers at math.MinInt, one byte longer than math.MaxInt.
func maxFramingResult() (*command.Result, command.View) {
	hex := strings.Repeat("f", 64)
	return &command.Result{
			ExitCode: math.MinInt, Signal: math.MinInt, TimedOut: true, Truncated: true, CaptureComplete: true,
			OutputSHA256: hex, OutputBytesKept: math.MinInt, OutputBytesTotal: math.MinInt64,
			DurationMS: math.MinInt64, TimeoutSEffective: math.MinInt, Shell: string(command.Bash),
		}, command.View{
			SnapshotVersion: math.MinInt, OverlaySHA256: hex, ModesSHA256: hex,
		}
}

// TestCommandResultWithinResultBound builds the longest model-visible
// rendering of a command result (the engine's note, call and status header,
// fence and delimiters around the longest fields) with the output at
// max_output_bytes, and checks the whole is within tools.max_result_bytes
// and the framing within the reserve.
func TestCommandResultWithinResultBound(t *testing.T) {
	res, view := maxFramingResult()
	maxOutput := tools.DefaultMaxResultBytes - command.FramingBytes
	for _, output := range []string{strings.Repeat("x", maxOutput-1) + "\n", strings.Repeat("x", maxOutput)} {
		res.Output = output
		e := &Engine{rand: rand.Reader}
		out := toolStepOutcome{results: []toolCallOutcome{{
			call:      journal.ToolCall{Name: "bash"},
			status:    journal.ToolCallError,
			content:   []byte(command.RenderResult(res, view)),
			errorCode: tools.ErrCodeSnapshotConflict,
		}}}
		msgs, _, err := e.toolStepMessages(transport.CompleteRequest{}, transport.CompleteResult{}, out, false)
		if err != nil {
			t.Fatal(err)
		}
		got := msgs[len(msgs)-1].Content
		if len(got) > tools.DefaultMaxResultBytes {
			t.Fatalf("rendered %d bytes, over %d", len(got), tools.DefaultMaxResultBytes)
		}
		if n := len(got) - len(output); n > command.FramingBytes {
			t.Fatalf("framing is %d bytes, reserve is %d", n, command.FramingBytes)
		}
	}
}
