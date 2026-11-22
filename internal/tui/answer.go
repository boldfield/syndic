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

// body renders the text behind a ref: loading, a one-line error, or the text.
func body(cc *ContentCache, ref string) string {
	item := cc.Get(ref)
	switch {
	case item.Err != nil:
		return "error: " + firstLine(item.Err.Error())
	case !item.Ready:
		return "loading…"
	}
	return strings.TrimRight(item.Data, "\n")
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
			lines = append(lines, body(in.content, refs[0]))
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
			lines = append(lines, fmt.Sprintf("approved by %s against %s", by, hash))
		}

	case "solo_complete":
		lines = append(lines, "UNREVIEWED (solo)")
		if refs := answerRefs(s, turnID, in); len(refs) > 0 {
			lines = append(lines, body(in.content, refs[0]))
		} else {
			lines = append(lines, "loading…")
		}

	case "incomplete":
		if ti.TerminalReason != nil {
			lines = append(lines, *ti.TerminalReason)
		}
		lines = append(lines, blockLines(in.openBlocks)...)
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
		lines = append(lines, "HUMAN OVERRIDE: "+reason)
		candidate()

	case "cancelled":
		if ti.CandidateHash == nil {
			lines = append(lines, "cancelled before candidate", noApprovedAnswer(ti.State))
			break
		}
		lines = append(lines, "CANCELLED, not approved", noApprovedAnswer(ti.State))
		lines = append(lines, blockLines(in.openBlocks)...)
		candidate()

	default:
		if refs := answerRefs(s, turnID, in); in.drafts && len(refs) > 0 {
			draftText := "UNREVIEWED DRAFT"
			if in.plainMode {
				draftText = "!! UNREVIEWED DRAFT !!"
			} else {
				draftText = draftBanner.Render(draftText)
			}
			lines = append(lines, draftText, body(in.content, refs[0]))
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
	return strings.Join(parts, ", ")
}

var draftBanner = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

// blockLines lists open blocks with severity, evidence and remedy.
func blockLines(blocks []wire.OpenBlock) []string {
	var lines []string
	for _, b := range blocks {
		if b.State != "open" {
			continue
		}
		lines = append(lines, fmt.Sprintf("[%s] %s", b.Severity, b.ID))
		if b.Evidence != "" {
			lines = append(lines, "  evidence: "+b.Evidence)
		}
		if b.Remedy != "" {
			lines = append(lines, "  remedy: "+b.Remedy)
		}
	}
	return lines
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
