package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/charmbracelet/lipgloss"
)

// answerInput is everything the answer tab needs beyond the State.
type answerInput struct {
	content        *ContentCache
	drafts         bool
	candidateID    string
	openBlocks     []wire.OpenBlock
	overrideReason string // the reason the human gave; the turn record only says "override"
	plainMode      bool   // true when colors should not be used
	rawMode        bool   // true when Markdown rendering is disabled
	width          int    // terminal width; content is wrapped and indented to it when positive
	now            time.Time
}

// answerTurn picks the turn the answer tab is about: the open turn, else the
// most recently submitted one.
func answerTurn(s State) string {
	if s.CurrentTurn != "" {
		return s.CurrentTurn
	}
	best := ""
	for id, ti := range s.Turns {
		if !s.inScope(id) {
			continue
		}
		if best == "" {
			best = id
			continue
		}
		b := s.Turns[best]
		if ti.SubmitSeq > b.SubmitSeq || (ti.SubmitSeq == b.SubmitSeq && (ti.SubmittedAt.After(b.SubmittedAt) || (ti.SubmittedAt.Equal(b.SubmittedAt) && id > best))) {
			best = id
		}
	}
	return best
}

// authorAttempt is the latest completed author attempt of a turn, or "".
func authorAttempt(s State, turnID string) string {
	best := ""
	for id, ai := range s.Attempts {
		if ai.TurnID != turnID || ai.Phase != "author" || ai.Status != "complete" {
			continue
		}
		if best == "" || ai.Epoch > s.Attempts[best].Epoch || (ai.Epoch == s.Attempts[best].Epoch && id > best) {
			best = id
		}
	}
	return best
}

// answerRefs lists the content refs the answer tab needs for the turn.
func answerRefs(s State, turnID string, in answerInput) []string {
	ti, ok := s.Turns[turnID]
	if !ok {
		return nil
	}
	switch ti.State {
	case "approved", "incomplete", "overridden", "cancelled":
		if ti.CandidateHash != nil && in.candidateID != "" {
			return []string{"candidate:" + in.candidateID}
		}
	case "solo_complete":
		if id := authorAttempt(s, turnID); id != "" {
			return []string{"attempt:" + id}
		}
	default:
		if in.drafts {
			if id := authorAttempt(s, turnID); id != "" {
				return []string{"attempt:" + id}
			}
		}
	}
	return nil
}

// bodyRaw is the text behind a ref, unescaped: loading, a one-line error, or
// the content. It must reach the terminal only through shown or safeLine.
func bodyRaw(cc *ContentCache, ref string) string {
	item := cc.Get(ref)
	switch {
	case item.Err != nil:
		return "error: " + firstLine(item.Err.Error())
	case !item.Ready:
		return "loading…"
	}
	return strings.TrimRight(item.Data, "\n")
}

// shownAnswer renders answer text with optional Markdown rendering.
// It respects rawMode to disable Markdown, and plainMode to disable it in plain terminals.
func shownAnswer(raw string, width int, in answerInput) string {
	return renderAnswerMarkdown(raw, width, in.rawMode, in.plainMode)
}

// shownPlain renders non-answer text as plain safe text, without Markdown rendering.
// This is used for TerminalReason, evidence, remedy, and override messages.
func shownPlain(raw string, width int) string {
	return strings.Join(SafeTextWithPrefix(raw, width), "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// renderAnswer returns the answer tab for a turn, unclipped; the App scrolls it.
func renderAnswer(s State, turnID string, in answerInput) string {
	ti, ok := s.Turns[turnID]
	if !ok {
		return ""
	}
	var lines []string
	candidate := func() {
		if refs := answerRefs(s, turnID, in); len(refs) > 0 {
			lines = append(lines, shownAnswer(bodyRaw(in.content, refs[0]), in.width, in))
		} else if ti.CandidateHash != nil {
			lines = append(lines, "loading…")
		}
	}

	switch ti.State {
	case "approved":
		candidate()
		if ti.CandidateHash != nil {
			hash := *ti.CandidateHash
			hash = hash[:min(12, len(hash))]
			by := strings.Join(approvers(s, turnID), ", ")
			banner := "approved by " + by
			if ti.Round > 0 {
				if ti.Round == 1 {
					banner += " after 1 revision"
				} else {
					banner += fmt.Sprintf(" after %d revisions", ti.Round)
				}
			}
			banner += " against " + hash
			reviews := -1
			if ti.ReviewCount != nil {
				reviews = *ti.ReviewCount
			}
			lines = append(lines, outcomeLine("✓", approvedMark, banner, reviews, in.plainMode))
		}

	case "solo_complete":
		reviews := -1
		if ti.ReviewCount != nil {
			reviews = *ti.ReviewCount
		}
		lines = append(lines, outcomeLine("·", soloMark, "solo", reviews, in.plainMode))
		if refs := answerRefs(s, turnID, in); len(refs) > 0 {
			lines = append(lines, shownAnswer(bodyRaw(in.content, refs[0]), in.width, in))
		} else {
			lines = append(lines, "loading…")
		}

	case "incomplete":
		if ti.TerminalReason != nil {
			lines = append(lines, shownPlain(*ti.TerminalReason, in.width))
		}
		lines = append(lines, blockLines(in.openBlocks, in.width)...)
		if ti.CandidateHash != nil {
			reviews := -1
			if ti.ReviewCount != nil {
				reviews = *ti.ReviewCount
			}
			lines = append(lines, outcomeLine("!", incompleteMark, "INCOMPLETE, not approved", reviews, in.plainMode))
		}
		lines = append(lines, noApprovedAnswer(ti.State))
		if ti.CandidateHash != nil {
			candidate()
		}

	case "overridden":
		reason := in.overrideReason
		if reason == "" && ti.TerminalReason != nil && *ti.TerminalReason != "override" {
			reason = *ti.TerminalReason
		}
		reviews := -1
		if ti.ReviewCount != nil {
			reviews = *ti.ReviewCount
		}
		reviewsSuffix := ""
		if reviews >= 0 {
			if reviews == 1 {
				reviewsSuffix = " · 1 review"
			} else {
				reviewsSuffix = fmt.Sprintf(" · %d reviews", reviews)
			}
		}
		banner := "HUMAN OVERRIDE: " + reason + reviewsSuffix
		wrapped := shownPlain(banner, in.width)
		wrappedLines := strings.Split(wrapped, "\n")
		if len(wrappedLines) > 0 {
			first := strings.TrimPrefix(wrappedLines[0], "  ")
			if in.plainMode {
				lines = append(lines, "! "+first)
			} else {
				lines = append(lines, incompleteMark.Render("! "+first))
			}
			lines = append(lines, wrappedLines[1:]...)
		}
		candidate()

	case "cancelled":
		reviews := -1
		if ti.ReviewCount != nil {
			reviews = *ti.ReviewCount
		}
		if ti.CandidateHash == nil {
			lines = append(lines, outcomeLine("×", cancelledMark, "cancelled before candidate", reviews, in.plainMode), noApprovedAnswer(ti.State))
			break
		}
		lines = append(lines, outcomeLine("×", cancelledMark, "cancelled before candidate", reviews, in.plainMode))
		lines = append(lines, noApprovedAnswer(ti.State))
		lines = append(lines, blockLines(in.openBlocks, in.width)...)
		candidate()

	default:
		if refs := answerRefs(s, turnID, in); in.drafts && len(refs) > 0 {
			draftText := outcomeLine("!", draftBanner, "UNREVIEWED DRAFT", -1, in.plainMode)
			lines = append(lines, draftText, shownAnswer(bodyRaw(in.content, refs[0]), in.width, in))
		}
		lines = append(lines, progressLine(ti, in.now))
	}
	return strings.Join(lines, "\n")
}

// noApprovedAnswer is the line later turns see in place of the answer of a
// turn that ended without one.
func noApprovedAnswer(state string) string {
	return "no approved answer (" + state + ")"
}

// progressLine is the one line a running turn shows: phase, round,
// elapsed time and participant states, the last of which a narrow terminal
// cuts first.
func progressLine(ti TurnInfo, now time.Time) string {
	parts := []string{"turn in progress: " + ti.State}
	if ti.MaxRounds > 0 {
		parts = append(parts, fmt.Sprintf("round %d/%d", ti.Round, ti.MaxRounds))
	}
	if !ti.SubmittedAt.IsZero() && !now.IsZero() {
		parts = append(parts, formatDuration(max(now.Sub(ti.SubmittedAt), 0)))
	}
	if ti.Author != "" {
		parts = append(parts, ti.Author+" "+ti.AuthorState)
	}
	if len(ti.Reviewers) > 0 {
		for _, id := range ti.Reviewers {
			parts = append(parts, id+" "+ti.ReviewerStates[id])
		}
	}
	return safeLine(strings.Join(parts, ", "))
}

// outcomeMark styles for outcome lines (light/dark pairs)
var (
	approvedMark   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "2", Dark: "10"}) // green
	incompleteMark = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "3", Dark: "11"}) // yellow
	cancelledMark  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "1", Dark: "9"})  // red
	soloMark       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "8", Dark: "7"})  // gray
	draftBanner    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "1", Dark: "9"})  // red
)

// outcomeLine formats a single outcome line with mark, banner, and optional reviews count.
// mark is a single character (✓, !, ×, ·), and reviews is the review count (-1 means no count).
// Returns the line with safe text, applying colour when appropriate.
func outcomeLine(mark string, style lipgloss.Style, banner string, reviews int, plainMode bool) string {
	reviewsSuffix := ""
	if reviews >= 0 {
		if reviews == 1 {
			reviewsSuffix = " · 1 review"
		} else {
			reviewsSuffix = fmt.Sprintf(" · %d reviews", reviews)
		}
	}
	if plainMode {
		return safeLine(mark + " " + banner + reviewsSuffix)
	}
	return style.Render(mark + " " + safeLine(banner+reviewsSuffix))
}

// blockLines lists open blocks with severity, evidence and remedy.
// These are always rendered as plain safe text, never with Markdown.
func blockLines(blocks []wire.OpenBlock, width int) []string {
	var lines []string
	for _, b := range blocks {
		if b.State != "open" {
			continue
		}
		lines = append(lines, safeLine(fmt.Sprintf("[%s] %s", b.Severity, b.ID)))
		if b.Evidence != "" {
			lines = append(lines, shownLabelledPlain("evidence: ", b.Evidence, width))
		}
		if b.Remedy != "" {
			lines = append(lines, shownLabelledPlain("remedy: ", b.Remedy, width))
		}
	}
	return lines
}

// shownLabelledPlain renders a labelled value as plain safe text without Markdown.
func shownLabelledPlain(label, raw string, width int) string {
	if width <= 0 {
		return "  " + label + ToSafeText([]byte(raw))
	}
	return shownPlain(label+raw, width)
}

// approvers are the reviewers with a valid verdict on the turn.
func approvers(s State, turnID string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s.Verdicts {
		ai, ok := s.Attempts[v.AttemptID]
		if !ok || ai.TurnID != turnID || ai.Phase != "review" || v.Class != "valid" || seen[ai.Participant] {
			continue
		}
		seen[ai.Participant] = true
		out = append(out, ai.Participant)
	}
	sort.Strings(out)
	return out
}
