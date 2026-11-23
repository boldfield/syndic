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

// shownWithMode is the one way untrusted text enters the answer and chat tabs: escaped
// once, and wrapped and indented as content lines when the width is known.
// When rawMode is true, only safe text escaping is applied; Markdown rendering is skipped.
func shownWithMode(raw string, width int, rawMode bool) string {
	return RenderMarkdown(raw, width, rawMode)
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
			lines = append(lines, shownWithMode(bodyRaw(in.content, refs[0]), in.width, in.rawMode))
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
			if ti.Round > 0 {
				by += fmt.Sprintf(" after %d revisions", ti.Round)
			}
			lines = append(lines, safeLine(fmt.Sprintf("approved by %s against %s", by, hash)))
		}

	case "solo_complete":
		lines = append(lines, "UNREVIEWED (solo)")
		if refs := answerRefs(s, turnID, in); len(refs) > 0 {
			lines = append(lines, shownWithMode(bodyRaw(in.content, refs[0]), in.width, in.rawMode))
		} else {
			lines = append(lines, "loading…")
		}

	case "incomplete":
		if ti.TerminalReason != nil {
			// Escape terminal reason which may contain untrusted content
			lines = append(lines, shownWithMode(*ti.TerminalReason, in.width, in.rawMode))
		}
		lines = append(lines, blockLines(in.openBlocks, in.width, in.rawMode)...)
		if ti.CandidateHash != nil {
			lines = append(lines, "INCOMPLETE, not approved")
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
		lines = append(lines, shownWithMode("HUMAN OVERRIDE: "+reason, in.width, in.rawMode))
		candidate()

	case "cancelled":
		if ti.CandidateHash == nil {
			lines = append(lines, "cancelled before candidate", noApprovedAnswer(ti.State))
			break
		}
		lines = append(lines, "CANCELLED, not approved", noApprovedAnswer(ti.State))
		lines = append(lines, blockLines(in.openBlocks, in.width, in.rawMode)...)
		candidate()

	default:
		if refs := answerRefs(s, turnID, in); in.drafts && len(refs) > 0 {
			draftText := "UNREVIEWED DRAFT"
			if in.plainMode {
				draftText = "!! UNREVIEWED DRAFT !!"
			} else {
				draftText = draftBanner.Render(draftText)
			}
			lines = append(lines, draftText, shownWithMode(bodyRaw(in.content, refs[0]), in.width, in.rawMode))
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

var draftBanner = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

// blockLines lists open blocks with severity, evidence and remedy.
func blockLines(blocks []wire.OpenBlock, width int, rawMode bool) []string {
	var lines []string
	for _, b := range blocks {
		if b.State != "open" {
			continue
		}
		lines = append(lines, safeLine(fmt.Sprintf("[%s] %s", b.Severity, b.ID)))
		if b.Evidence != "" {
			lines = append(lines, shownLabelledWithMode("evidence: ", b.Evidence, width, rawMode))
		}
		if b.Remedy != "" {
			lines = append(lines, shownLabelledWithMode("remedy: ", b.Remedy, width, rawMode))
		}
	}
	return lines
}

// shownLabelledWithMode is shown for a labelled value; without a width the label is
// indented in the legacy way.
func shownLabelledWithMode(label, raw string, width int, rawMode bool) string {
	if width <= 0 {
		return "  " + label + ToSafeText([]byte(raw))
	}
	return shownWithMode(label+raw, width, rawMode)
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
