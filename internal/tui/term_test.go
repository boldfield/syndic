package tui

import (
	"context"
	"strings"
	"testing"
)

func TestDetectTerminalCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		term    string
		noColor bool
		want    TerminalCapabilities
	}{
		{
			name:    "TERM=dumb",
			term:    "dumb",
			noColor: false,
			want: TerminalCapabilities{
				Color:     false,
				AltScreen: false,
			},
		},
		{
			name:    "TERM unset",
			term:    "",
			noColor: false,
			want: TerminalCapabilities{
				Color:     false,
				AltScreen: false,
			},
		},
		{
			name:    "NO_COLOR with xterm",
			term:    "xterm-256color",
			noColor: true,
			want: TerminalCapabilities{
				Color:     false,
				AltScreen: true,
			},
		},
		{
			name:    "Normal terminal",
			term:    "xterm-256color",
			noColor: false,
			want: TerminalCapabilities{
				Color:     true,
				AltScreen: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TERM", tt.term)
			if tt.noColor {
				t.Setenv("NO_COLOR", "1")
			} else {
				t.Setenv("NO_COLOR", "")
			}

			got := DetectTerminalCapabilities()
			if got != tt.want {
				t.Errorf("DetectTerminalCapabilities() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAppTerminalCapabilities(t *testing.T) {
	ctx := context.Background()

	caps := TerminalCapabilities{Color: false, AltScreen: true}
	app := NewApp(ctx, "/tmp/test.sock", WithTerminalCapabilities(caps))
	if app.terminalCapabilities != caps {
		t.Errorf("Expected %v, got %v", caps, app.terminalCapabilities)
	}
}

func TestPlainModeDraftBanner(t *testing.T) {
	t.Setenv("TERM", "dumb")

	cc := NewContentCache()
	cc.Store("attempt:1", "sample candidate text", nil)

	in := answerInput{
		plainMode:   true,
		drafts:      true,
		content:     cc,
		candidateID: "",
		openBlocks:  nil,
	}

	state := NewState()
	state.Turns["turn1"] = TurnInfo{
		State:         "author_dispatched",
		CandidateHash: nil,
	}
	state.Attempts["attempt:1"] = AttemptInfo{
		ID:     "attempt:1",
		TurnID: "turn1",
		Phase:  "author",
		Status: "complete",
		Epoch:  0,
	}

	rendered := renderAnswer(state, "turn1", in)

	if rendered == "" {
		t.Error("renderAnswer returned empty string")
	}

	if !strings.Contains(rendered, "!! UNREVIEWED DRAFT !!") {
		t.Errorf("Expected plain draft banner with !! in: %q", rendered)
	}
}
