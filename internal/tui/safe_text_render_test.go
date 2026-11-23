package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/mattn/go-runewidth"
)

// hostileInputs mixes model text, user text, filenames, event details and
// attachment-like bytes: ESC/CSI, OSC 8, cursor movement, CR, valid-UTF-8 C1,
// bidi, DEL and invalid UTF-8.
var hostileInputs = []string{
	"model \x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\ text",
	"user \x1b[2J\x1b[H\rhome\u009b31m\u0085next",
	"../\u202efdp.exe\x00\x7f",
	"event \u2066iso\u2069 \x07bell",
	"attach \x00\x01\xff\xfe\x80\x9f binary \xc3\x28",
	"wide 日本語日本語日本語 and 👨‍👩‍👧 \t tabbed",
}

func assertSafeRunes(t *testing.T, label, s string) {
	t.Helper()
	for _, r := range s {
		switch {
		case r == '\n':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, bidiChars[r]:
			t.Errorf("%s: raw control rune %U in %q", label, r, s)
		}
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s: invalid UTF-8 in %q", label, s)
	}
}

func TestSafeTextWithPrefix_HostileProperty(t *testing.T) {
	for _, in := range hostileInputs {
		for _, w := range []int{1, 3, 8, 9, 12, 20, 80} {
			for i, line := range SafeTextWithPrefix(in, w) {
				assertSafeRunes(t, "SafeTextWithPrefix", line)
				if !strings.HasPrefix(line, "  ") {
					t.Errorf("w=%d line %d unprefixed: %q", w, i, line)
				}
				if got := runewidth.StringWidth(line); got > max(w, minContentWidth) {
					t.Errorf("w=%d line %d is %d cells: %q", w, i, got, line)
				}
			}
		}
	}
}

func TestSafeTextWithPrefix_Idempotent(t *testing.T) {
	for _, in := range hostileInputs {
		once := SafeTextWithPrefix(in, 20)
		twice := SafeTextWithPrefix(ToSafeText([]byte(in)), 20)
		if strings.Join(once, "\n") != strings.Join(twice, "\n") {
			t.Errorf("not idempotent for %q", in)
		}
	}
}

func TestTranscriptHostileErrorAndBlocks(t *testing.T) {
	hostile := "boom \x1b]8;;http://x\x1b\\link\x1b[2J\u202e"
	cc := NewContentCache()
	cc.Store("attempt:a1", "", errors.New(hostile))
	cc.Store("attempt:a2", "body "+hostileInputs[0], nil)
	s := State{Attempts: map[string]AttemptInfo{
		"a1": {ID: "a1", TurnID: "t1", Phase: "author", Status: "complete"},
		"a2": {ID: "a2", TurnID: "t1", Phase: "author", Status: "complete"},
	}}
	in := transcriptInput{
		items: []wire.TranscriptItem{
			{Ref: "attempt:a1", ByteLen: 10},
			{Ref: "attempt:a2", ByteLen: 10},
		},
		content: cc,
	}
	lines := transcriptLines(s, "t1", in, 60)
	out := strings.Join(lines, "\n")
	assertSafeRunes(t, "transcript", out)
	if !strings.Contains(out, "could not load: boom \\x1b]8;;http://x") {
		t.Errorf("error detail not visibly escaped: %q", out)
	}

	rows := []blockRow{{id: "v1:b\x1b", severity: "hi\x07", state: "open", evidence: hostile, remedy: hostileInputs[1]}}
	for _, l := range blockRowLines(rows, 40) {
		assertSafeRunes(t, "block", l)
	}
}

func TestFormatRefusalEscapesDetail(t *testing.T) {
	detail := "bad \x1b]8;;http://x\x1b\\file\u202e"
	got := formatRefusal(&wire.Error{Code: "refused", Detail: &detail})
	assertSafeRunes(t, "refusal", got)
	if !strings.Contains(got, "\\x1b") {
		t.Errorf("escape not visible: %q", got)
	}
	assertSafeRunes(t, "plain error", formatRefusal(errors.New(detail)))
}

func TestChatEchoesUserTextSafely(t *testing.T) {
	s := State{Turns: map[string]TurnInfo{"t1": {ID: "t1", State: "open"}}}
	out := renderChat(s, chatInput{inputs: map[string]string{"t1": hostileInputs[1]}, answer: func(string) answerInput { return answerInput{} }})
	assertSafeRunes(t, "chat", out)
}
