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

// A literal backslash-x or backslash-u followed by multibyte text is ordinary
// content; wrapping must never cut a rune in half or emit invalid UTF-8.
func TestSafeTextWithPrefix_LiteralEscapeLookalikes(t *testing.T) {
	inputs := []string{"abcd\\xaÛ", "abcd\\x日", "C:\\users\\日本語\\u00é", "\\u\\x\\", "a\\x\x9b日\u009b\\u"}
	for _, in := range inputs {
		for w := 1; w <= 14; w++ {
			for _, l := range SafeTextWithPrefix(in, w) {
				if !utf8.ValidString(l) {
					t.Fatalf("w=%d %q: invalid UTF-8 line %q", w, in, l)
				}
				for _, r := range l {
					if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || bidiChars[r] {
						t.Fatalf("w=%d %q: control %U in %q", w, in, r, l)
					}
				}
				if got := runewidth.StringWidth(l); got > max(w, 8) {
					t.Fatalf("w=%d %q: line %q is %d cells", w, in, l, got)
				}
			}
		}
	}
}

func TestAppViewHostileSourcesAreSafeAndIndented(t *testing.T) {
	hostile := "x \x1b]8;;http://e\x1b\\link\x1b[2J\r\u009b31m\u202e\xff " + strings.Repeat("long ", 20)
	a := chatApp(t)
	a.width = 40
	a.workDir = "/tmp/\x1b]8;;http://w\x1b\\dir\u202e"
	addTurn(a, "t1", 1, "approved", hostile, hostile)
	a.openBlocksByTurn["t1"] = []wire.OpenBlock{{ID: "b\x1b", State: "open", Severity: "high", Evidence: hostile, Remedy: hostile}}
	a.state.Turns["t2"] = TurnInfo{ID: "t2", State: "incomplete", SubmitSeq: 2, TerminalReason: &hostile}
	a.state.TurnState["t2"] = "incomplete"
	a.inputs["t2"] = hostile

	view := a.View()
	if h := a.conversationHeader(); !strings.Contains(h, "workspace /tmp/\\x1b]8;;http://w\\x1b") || strings.ContainsAny(h, "\x1b\u202e") {
		t.Errorf("workspace path not escaped visibly: %q", h)
	}
	for _, l := range strings.Split(view, "\n") {
		for _, r := range l {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || bidiChars[r] {
				t.Fatalf("control %U on line %q", r, l)
			}
		}
		if strings.Contains(l, "long long") && !strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "you: ") && !strings.HasPrefix(l, "     ") {
			t.Errorf("content line not indented: %q", l)
		}
		if runewidth.StringWidth(l) > 40 {
			t.Errorf("line wider than the terminal: %q", l)
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

func TestPanelsAndBannersEscapeHostileText(t *testing.T) {
	hostile := "p\x1b]8;;http://x\x1b\\L\x1b[2J\u202e"

	lines := conversationLines([]wire.ConversationSummary{{ID: "c1", Preview: hostile}}, "", hostile, 80)
	assertSafeRunes(t, "conversations", strings.Join(lines, "\n"))

	reason := "block:" + hostile
	tr := &wire.TurnReply{
		State:          "incomplete",
		TerminalReason: &reason,
		OpenBlocks:     []wire.OpenBlock{{ID: hostile, Severity: hostile, Evidence: hostile, Remedy: hostile}},
	}
	assertSafeRunes(t, "why", strings.Join(whyLines("t1", tr, nil), "\n"))

	prof := hostile
	s := State{UnapprovedProfile: &prof, QuickInvalid: "quick: invalid /w/" + hostile}
	assertSafeRunes(t, "banners", strings.Join(renderBanners(s), "\n"))

	a := chatApp(t)
	a.width = 60
	a.refusal = "trim: " + hostile
	a.workDir = "/w/" + hostile
	a.state.UnapprovedProfile = &prof
	a.state.QuickInvalid = "quick: invalid /w/" + hostile
	for _, l := range strings.Split(a.View(), "\n") {
		assertSafeRunes(t, "view", l)
	}
}
