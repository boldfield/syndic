package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
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
		if strings.Contains(l, "long long") && !strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "› ") && !strings.HasPrefix(l, "     ") {
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

	lines := conversationChooserLines(t, hostile, hostile, 80)
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

// recordHostile carries every class the boundary escapes: OSC 8, a clear-screen
// CSI, CR, a valid-UTF-8 C1 (8-bit CSI), bidi override and isolate, an invalid
// byte and a line feed that would break a one-line field.
const recordHostile = "h\x1b]8;;http://x\x1b\\L\x1b[2J\r\u009b\u202e\u2066\xff\n2"

// hostileRecordApp fills the engine-sourced state the status line, the chat,
// the transcript and the participants tab show with recordHostile.
func hostileRecordApp(t *testing.T) *App {
	t.Helper()
	h := recordHostile
	a := chatApp(t)
	a.width, a.height = 120, 60
	a.state.Status.DefaultPreset = h
	a.state.Status.RosterPath = h
	a.state.NextPreset = h
	a.state.Participants[h] = ParticipantInfo{
		ID: h, State: h, Detail: h, Pool: h, ReportedModel: h, Identity: h,
		EligibleRoles: []string{h}, FailingStep: h, ServerStatus: h, RetryCommand: h,
		BytesPerTokenSource: h, ContextSource: h, DeclaredBilling: h,
	}
	a.state.ProbeSubjects[h] = ProbeSubjectInfo{Kind: h, Endpoint: h, Model: h, State: h, Detail: h, FailingStep: h, ServerStatus: h, RetryCommand: h}
	group := []wire.GroupMember{{EndpointName: h, Model: h}}
	a.state.Pools[h] = wire.Pool{Name: h, Concurrency: 1, Holds: []wire.PoolHold{{AttemptID: h, HeldSince: h, Group: group}}}
	a.state.OrphanedHolds = []wire.Hold{{Pool: h, AttemptID: h, Group: group}}

	addTurn(a, "t0", 1, "approved", "q", "answer")
	approveBy(a, "t0", h)
	addTurn(a, "t1", 3, "authoring", "q", "")
	a.state.Turns["t1"] = TurnInfo{
		ID: "t1", State: h, Preset: h, QuickProfile: &h, SubmitSeq: 3, SubmittedAt: chatNow.Add(-time.Minute),
		Author: h, AuthorState: h, Reviewers: []string{h}, ReviewerStates: map[string]string{h: h},
	}
	a.state.CurrentTurn = "t1"
	a.state.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Participant: h, Phase: h, Status: h, TransportStatus: h, VerdictClass: h}
	a.state.Messages["m1"] = MessageInfo{ID: "m1", TurnID: "t1", Sender: h, Phase: h}
	return a
}

func assertSafeLines(t *testing.T, label string, lines []string) {
	t.Helper()
	for _, l := range lines {
		assertSafeRunes(t, label, l)
	}
}

func TestStatusLineEscapesRecordValues(t *testing.T) {
	a := hostileRecordApp(t)
	for _, w := range []int{40, 80, 400} {
		for _, reviews := range []bool{false, true} {
			line := renderStatusLine(a.state, w, chatNow, reviews)
			assertSafeRunes(t, "strip", line)
			if strings.Contains(line, "\n") {
				t.Errorf("w=%d: strip broke onto two lines: %q", w, line)
			}
		}
	}
	if line := renderStrip(a.state, 4000, chatNow); !strings.Contains(line, `[quick:h\x1b]8;;http://x\x1b\L\x1b[2J\x0d\xc2\x9b\u202e\u2066\xff\x0a2]`) {
		t.Errorf("quick profile not shown with visible escapes: %q", line)
	}
	a.state.CurrentTurn = ""
	assertSafeRunes(t, "idle strip", renderStrip(a.state, 400, chatNow))
	assertSafeLines(t, "view", strings.Split(a.View(), "\n"))
}

func TestParticipantsTabEscapesRecordValues(t *testing.T) {
	a := hostileRecordApp(t)
	out := renderParticipants(a.state, 400, 0, recordHostile, chatNow)
	assertSafeLines(t, "participants", strings.Split(out, "\n"))
	for _, want := range []string{`model: h\x1b]8;;`, `probe failed at: h\x1b]8;;`, `server status: h\x1b]8;;`, `\u202e\u2066\xff\x0a2`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing visible escape %q in:\n%s", want, out)
		}
	}
	a.tab = tabParticipants
	assertSafeLines(t, "participants view", strings.Split(a.View(), "\n"))
}

func TestTranscriptHeadersEscapeRecordValues(t *testing.T) {
	a := hostileRecordApp(t)
	in := transcriptInput{
		items: []wire.TranscriptItem{
			{Ref: "attempt:a1"},
			{Ref: "msg:m1"},
			{Ref: "attempt:" + recordHostile},
			{Ref: "msg:" + recordHostile},
		},
		content: NewContentCache(),
	}
	for _, w := range []int{20, 60, 200} {
		lines := transcriptLines(a.state, "t1", in, w)
		assertSafeLines(t, "transcript", lines)
		if w == 200 && !strings.Contains(strings.Join(lines, "\n"), `attempt a1 · h\x1b]8;;`) {
			t.Errorf("attempt header not visibly escaped:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func TestChatProgressAndOutcomeEscapeRecordValues(t *testing.T) {
	a := hostileRecordApp(t)
	view := a.View()
	assertSafeLines(t, "chat", strings.Split(view, "\n"))
	for _, want := range []string{`approved by h\x1b]8;;`, `turn in progress: h\x1b]8;;`} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in:\n%s", want, view)
		}
	}
}

func TestPanelsAndPromptsEscapeEveryLine(t *testing.T) {
	a := hostileRecordApp(t)
	a.panel = a.provisionalLines()
	view := a.View()
	assertSafeLines(t, "provisional", strings.Split(view, "\n"))
	if !strings.Contains(view, `placeholder`) || !strings.Contains(view, `h\x1b]8;;`) {
		t.Errorf("provisional prompt not shown with the roster path escaped:\n%s", view)
	}
	// Any panel line, whoever set it, reaches the terminal as safe text.
	a.panel = []string{"turn " + recordHostile + " is still running", "state: " + recordHostile}
	assertSafeLines(t, "panel", strings.Split(a.View(), "\n"))

	// The /trim preview echoes what the user typed before any panel wraps it.
	a.inputs["t0"] = recordHostile
	assertSafeLines(t, "trim preview", a.trimPreview([]string{"t0", "t1"}, "t1"))
}

// assertNoInjectedRow fails when a line feed in a value opened a row of its
// own: the injected text must only appear behind the visible \x0a escape.
func assertNoInjectedRow(t *testing.T, label string, lines []string) {
	t.Helper()
	for _, l := range strings.Split(strings.Join(lines, "\n"), "\n") {
		if i := strings.Index(l, "INJECTED"); i >= 0 && !strings.Contains(l, `\x0aINJECTED`) {
			t.Errorf("%s: injected row at column %d: %q", label, i, l)
		}
	}
}

func TestOneLineFieldsEscapeLineFeeds(t *testing.T) {
	inj := "/safe\nINJECTED"

	a := chatApp(t)
	a.width, a.height = 120, 40
	a.workDir = inj
	assertNoInjectedRow(t, "header", []string{a.conversationHeader()})
	a.conversationID = "c1234567890"
	assertNoInjectedRow(t, "header open", []string{a.conversationHeader()})

	lines := conversationChooserLines(t, inj, inj, 120)
	assertNoInjectedRow(t, "conversations", lines)

	prof := inj
	assertNoInjectedRow(t, "banners", renderBanners(State{UnapprovedProfile: &prof, QuickInvalid: "quick: invalid " + inj}))

	assertNoInjectedRow(t, "blocks", blockLines([]wire.OpenBlock{{ID: inj, Severity: inj, State: "open"}}, 80))
	assertNoInjectedRow(t, "block rows", blockRowLines([]blockRow{{id: inj, severity: inj, state: inj}}, 80))

	reason := "gate: " + inj
	tr := &wire.TurnReply{State: "incomplete", TerminalReason: &reason, OpenBlocks: []wire.OpenBlock{{ID: inj, Severity: inj, State: "open", Evidence: inj}}}
	assertNoInjectedRow(t, "why", strings.Split(renderPanel(whyLines("t1", tr, nil), 80), "\n"))
	assertNoInjectedRow(t, "panel", strings.Split(renderPanel([]string{"detail: " + inj}, 80), "\n"))

	detail := inj
	assertNoInjectedRow(t, "refusal", []string{formatRefusal(&wire.Error{Code: "c", Detail: &detail, Pool: &detail})})

	a.quickSelection = &QuickSelection{ProjectFilePath: inj, ProjectProfile: inj}
	a.requestApproval(wire.QuickProfile{Name: inj, Preset: inj, DefinitionSHA256: "d"})
	if len(a.panel) == 0 {
		t.Fatal("approval prompt not shown")
	}
	assertNoInjectedRow(t, "approval", a.panel)

	a.state.UnapprovedProfile = &prof
	a.state.QuickInvalid = "quick: invalid " + inj
	view := strings.Split(a.View(), "\n")
	assertNoInjectedRow(t, "view", view)
	assertSafeLines(t, "view", view)
}

// conversationChooserLines draws a list of one conversation of this
// directory and one of another, whose preview and workspace are hostile.
func conversationChooserLines(t *testing.T, preview, workspace string, width int) []string {
	t.Helper()
	a := chatApp(t)
	a.workDir = workspace
	other := workspace + "/other"
	a.conv = &convChooser{loaded: true, items: []wire.ConversationSummary{
		{ID: "c1", Preview: preview},
		{ID: "c2", Preview: preview, Workspace: &other},
	}}
	a.resetSel()
	return a.conversationLines(10, width)
}
