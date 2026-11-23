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
	out := renderAnswerMarkdown(raw, width, in.rawMode, in.plainMode)
	if width <= 0 {
		// Unwrapped text still gets the content indent, so no answer line
		// starts in column 0.
		out = contentPrefix + strings.ReplaceAll(out, "\n", "\n"+contentPrefix)
	}
	return out
}

// Outcome marks, one per kind of outcome line (v6 section 1, Chat lines).
const (
	markApproved   = "✓"
	markIncomplete = "!"
	markCancelled  = "×"
	markSolo       = "·"
)

// outcomeStyles are the outcome colours as light and dark pairs, chosen from
// the terminal background. Every outcome also has its mark and its words, so
// no meaning rests on colour.
var outcomeStyles = map[string]lipgloss.Style{
	markApproved:   lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"}),
	markIncomplete: lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"}),
	markCancelled:  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "203"}),
	markSolo:       lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "245"}),
}

// outcomeLines returns an outcome as lines: the mark and a space in column 0
// of the first line, then the text wrapped to width, every later line a
// content line. The text is escaped here, in both modes, and the whole line
// is coloured unless plain.
func outcomeLines(mark, raw string, in answerInput) []string {
	return styledOutcomeLines(mark, raw, in, outcomeStyles[mark])
}

func styledOutcomeLines(mark, raw string, in answerInput, style lipgloss.Style) []string {
	var lines []string
	if in.width > 0 {
		lines = SafeTextWithPrefix(raw, in.width)
	} else {
		lines = []string{contentPrefix + safeLine(raw)}
	}
	lines[0] = mark + " " + strings.TrimPrefix(lines[0], contentPrefix)
	if !in.plainMode {
		for i, l := range lines {
			lines[i] = style.Render(l)
		}
	}
	return lines
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
			switch {
			case ti.Round == 1:
				by += " after 1 revision"
			case ti.Round > 1:
				by += fmt.Sprintf(" after %d revisions", ti.Round)
			}
			lines = append(lines, outcomeLines(markApproved, fmt.Sprintf("approved by %s against %s", by, hash), in)...)
		}

	case "solo_complete":
		lines = append(lines, outcomeLines(markSolo, "UNREVIEWED (solo)", in)...)
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
			lines = append(lines, outcomeLines(markIncomplete, "INCOMPLETE, not approved", in)...)
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
		lines = append(lines, outcomeLines(markIncomplete, "HUMAN OVERRIDE: "+reason, in)...)
		candidate()

	case "cancelled":
		if ti.CandidateHash == nil {
			lines = append(lines, outcomeLines(markCancelled, "cancelled before candidate", in)...)
			lines = append(lines, noApprovedAnswer(ti.State))
			break
		}
		lines = append(lines, outcomeLines(markCancelled, "CANCELLED, not approved", in)...)
		lines = append(lines, noApprovedAnswer(ti.State))
		lines = append(lines, blockLines(in.openBlocks, in.width)...)
		candidate()

	default:
		if refs := answerRefs(s, turnID, in); in.drafts && len(refs) > 0 {
			draftText := "UNREVIEWED DRAFT"
			if in.plainMode {
				draftText = "!! UNREVIEWED DRAFT !!"
			}
			lines = append(lines, styledOutcomeLines(markSolo, draftText, in, draftBanner)...)
			lines = append(lines, shownAnswer(bodyRaw(in.content, refs[0]), in.width, in))
		}
		lines = append(lines, renderProgress(s, in.now, in.plainMode))
	}
	return strings.Join(lines, "\n")
}

// noApprovedAnswer is the line later turns see in place of the answer of a
// turn that ended without one.
func noApprovedAnswer(state string) string {
	return "no approved answer (" + state + ")"
}

var draftBanner = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "203"})

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
