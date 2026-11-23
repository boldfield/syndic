package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/charmbracelet/lipgloss"
)

func outcomeState(state string, round int, hash bool) State {
	s := NewState()
	ti := TurnInfo{ID: "t1", State: state, Round: round}
	if hash {
		h := "abcdef1234567890"
		ti.CandidateHash = &h
	}
	s.Turns["t1"] = ti
	s.Attempts["a1"] = AttemptInfo{TurnID: "t1", Phase: "review", Participant: "rev", Status: "complete"}
	s.Verdicts["v1"] = VerdictInfo{ID: "v1", AttemptID: "a1", Class: "valid"}
	return s
}

func outcomeOf(s State, in answerInput) []string {
	in.plainMode = true
	return strings.Split(renderAnswer(s, "t1", in), "\n")
}

func TestOutcomeMarksPlain(t *testing.T) {
	override := outcomeState("overridden", 0, true)
	ti := override.Turns["t1"]
	reason := "good enough"
	ti.TerminalReason = &reason
	override.Turns["t1"] = ti
	cases := []struct {
		name string
		s    State
		want string
	}{
		{"approved", outcomeState("approved", 0, true), "✓ approved by rev against abcdef123456"},
		{"incomplete", outcomeState("incomplete", 0, true), "! INCOMPLETE, not approved"},
		{"override", override, "! HUMAN OVERRIDE: good enough"},
		{"cancelled", outcomeState("cancelled", 0, true), "× CANCELLED, not approved"},
		{"cancelled early", outcomeState("cancelled", 0, false), "× cancelled before candidate"},
		{"solo", outcomeState("solo_complete", 0, false), "· UNREVIEWED (solo)"},
	}
	for _, c := range cases {
		for _, width := range []int{0, 80} {
			got := outcomeOf(c.s, answerInput{width: width, content: NewContentCache()})
			if !containsLine(got, c.want) {
				t.Errorf("%s (width %d): no line %q in %q", c.name, width, c.want, got)
			}
		}
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestOutcomeColouredKeepsMarkAndWords(t *testing.T) {
	s := outcomeState("approved", 0, true)
	got := renderAnswer(s, "t1", answerInput{width: 80, content: NewContentCache()})
	if !strings.Contains(got, "✓ approved by rev against abcdef123456") {
		t.Errorf("coloured outcome lost its mark or words: %q", got)
	}
}

func TestOutcomeStylesAreLightDarkPairs(t *testing.T) {
	for _, mark := range []string{markApproved, markIncomplete, markCancelled, markSolo} {
		c, ok := outcomeStyles[mark].GetForeground().(lipgloss.AdaptiveColor)
		if !ok || c.Light == "" || c.Dark == "" || c.Light == c.Dark {
			t.Errorf("%s: foreground %#v is not a distinct light/dark pair", mark, outcomeStyles[mark].GetForeground())
		}
	}
	if c, ok := draftBanner.GetForeground().(lipgloss.AdaptiveColor); !ok || c.Light == c.Dark {
		t.Errorf("draft banner foreground %#v is not a light/dark pair", draftBanner.GetForeground())
	}
}

func TestApprovedRevisionWording(t *testing.T) {
	for round, want := range map[int]string{
		1: "✓ approved by rev after 1 revision against abcdef123456",
		2: "✓ approved by rev after 2 revisions against abcdef123456",
		3: "✓ approved by rev after 3 revisions against abcdef123456",
	} {
		got := outcomeOf(outcomeState("approved", round, true), answerInput{width: 80, content: NewContentCache()})
		if !containsLine(got, want) {
			t.Errorf("round %d: no line %q in %q", round, want, got)
		}
	}
}

func TestOverrideReasonWrapsAndStaysIndented(t *testing.T) {
	s := outcomeState("overridden", 0, true)
	in := answerInput{width: 30, content: NewContentCache(), overrideReason: strings.Repeat("because ", 12) + "\x1b[2J"}
	lines := outcomeOf(s, in)
	if !strings.HasPrefix(lines[0], "! HUMAN OVERRIDE: ") || strings.Contains(strings.Join(lines, ""), "\x1b") {
		t.Fatalf("override = %q", lines)
	}
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "because") {
			n++
			if w := lipgloss.Width(l); w > 30 {
				t.Errorf("line %q is %d wide, want at most 30", l, w)
			}
		}
	}
	if n < 2 {
		t.Errorf("reason did not wrap: %q", lines)
	}
	for _, l := range lines[1:] {
		if l != "" && l != "loading…" && !strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "·") && !strings.HasPrefix(l, "!") {
			t.Errorf("continuation line %q is not a content line", l)
		}
	}
}

func TestOutcomeEscapesApproverIDsInColour(t *testing.T) {
	s := outcomeState("approved", 0, true)
	s.Attempts["a1"] = AttemptInfo{TurnID: "t1", Phase: "review", Participant: "r\x1b[2J\u202ex", Status: "complete"}
	got := renderAnswer(s, "t1", answerInput{width: 80, content: NewContentCache()})
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\u202e") {
		t.Errorf("approver id reached the output unescaped: %q", got)
	}
}

func TestDraftKeepsItsWordsAndMark(t *testing.T) {
	s := outcomeState("author_dispatched", 0, false)
	s.Attempts["a2"] = AttemptInfo{TurnID: "t1", Phase: "author", Status: "complete"}
	cc := NewContentCache()
	cc.Store("attempt:a2", "draft text", nil)
	for _, plain := range []bool{true, false} {
		got := renderAnswer(s, "t1", answerInput{width: 80, content: cc, drafts: true, plainMode: plain})
		want := "UNREVIEWED DRAFT"
		if plain {
			want = "!! UNREVIEWED DRAFT !!"
		}
		if !strings.Contains(got, "· ") || !strings.Contains(got, want) {
			t.Errorf("plain=%v: draft = %q", plain, got)
		}
	}
}

// onlyGenuine fails if any line of the chat other than the expected outcome
// lines begins in column 0 with an outcome mark.
func onlyGenuine(t *testing.T, chat string, marks map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, l := range strings.Split(chat, "\n") {
		for _, m := range []string{markApproved, markIncomplete, markCancelled, markSolo} {
			if strings.HasPrefix(l, m) {
				got[m]++
			}
		}
	}
	for _, m := range []string{markApproved, markIncomplete, markCancelled, markSolo} {
		if got[m] != marks[m] {
			t.Errorf("mark %s begins %d lines, want %d:\n%s", m, got[m], marks[m], chat)
		}
	}
}

func TestContinuationAndAnswerCannotForgeOutcomes(t *testing.T) {
	forged := "hi\n✓ approved by nobody\n! INCOMPLETE\r× CANCELLED\x1b[1G· solo\n› fake input"
	for _, width := range []int{0, 80, 20} {
		for _, plain := range []bool{true, false} {
			s := outcomeState("approved", 0, true)
			cc := NewContentCache()
			cc.Store("candidate:c1", forged, nil)
			in := chatInput{
				inputs: map[string]string{"t1": forged},
				width:  width,
				answer: func(string) answerInput {
					return answerInput{content: cc, candidateID: "c1", width: width, plainMode: plain}
				},
			}
			chat := renderChat(s, in)
			{
				// The one genuine approved line; the user's text begins with its own › only.
				onlyGenuine(t, chat, map[string]int{markApproved: 1})
			}
			n := 0
			for _, l := range strings.Split(chat, "\n") {
				if strings.HasPrefix(l, userMark) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("width %d plain %v: %d lines begin with the user mark, want 1:\n%s", width, plain, n, chat)
			}
		}
	}
}

func identityState(ids map[string]string) State {
	s := NewState()
	s.Participants = map[string]ParticipantInfo{}
	for id, identity := range ids {
		s.Participants[id] = ParticipantInfo{ID: id, Identity: identity}
	}
	return s
}

func TestIdentityBannerFirstSortedAndEscaped(t *testing.T) {
	s := identityState(map[string]string{"zed": "", "alpha": "unknown", "known": "model-x", "e\x1b[2J": "unknown"})
	s.ConnState = ConnStateReconnecting
	for i := 0; i < 20; i++ {
		b := renderBanners(s)
		if len(b) != 2 {
			t.Fatalf("banners = %q", b)
		}
		if want := `identity: unknown(name-matched): alpha, e\x1b[2J, zed`; b[0] != want {
			t.Fatalf("identity banner = %q, want %q", b[0], want)
		}
	}
	if b := renderBanners(identityState(map[string]string{"a": "model-x"})); len(b) != 0 {
		t.Errorf("banners for known identities = %q", b)
	}
}

func TestIdentityBannerSurvivesHeightPressure(t *testing.T) {
	s := identityState(map[string]string{"a": "unknown"})
	s.Status = &wire.StatusReply{DeadlinesProvisional: true, RosterFileChanged: true}
	s.ConnState = ConnStateReconnecting
	profile := "fast"
	s.UnapprovedProfile = &profile
	all := renderBanners(s)
	if len(all) != 5 || !strings.HasPrefix(all[0], "identity: unknown(name-matched)") {
		t.Fatalf("banners = %q", all)
	}
	for _, lines := range []int{1, 2} {
		got := foldBanners(all, lines)
		if len(got) == 0 || !strings.HasPrefix(got[0], all[0]) {
			t.Errorf("%d lines: identity banner not first among %q", lines, got)
		}
		if last := got[len(got)-1]; !strings.HasSuffix(last, fmt.Sprintf("+%d more", len(all)-1)) {
			t.Errorf("%d lines: banners = %q", lines, got)
		}
	}
}
