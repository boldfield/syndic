package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// chatInput is everything the chat tab needs beyond the State.
type chatInput struct {
	answer  func(turnID string) answerInput // what the answer part of one turn needs
	inputs  map[string]string               // what the user typed, for the turns this process submitted
	content *ContentCache                   // bodies of the inputs of the other turns
}

// notInContext marks a turn that /trim dropped from the history later turns see.
const notInContext = "not in context"

var dimmed = lipgloss.NewStyle().Faint(true)

// chatTurns lists the turns of the chat, oldest first.
func chatTurns(s State) []string {
	pos := map[string]int{}
	if s.Scope != nil {
		for i, id := range s.Scope.Order {
			pos[id] = i
		}
	}
	var ids []string
	for id := range s.Turns {
		if s.inScope(id) {
			ids = append(ids, id)
		}
	}
	// The conversation's own order comes first; turns it does not list, such
	// as those of a chat without a scope, follow in submission order.
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.Turns[ids[i]], s.Turns[ids[j]]
		pa, oka := pos[ids[i]]
		pb, okb := pos[ids[j]]
		switch {
		case oka && okb && pa != pb:
			return pa < pb
		case oka != okb:
			return oka
		case a.SubmitSeq != b.SubmitSeq:
			return a.SubmitSeq < b.SubmitSeq
		case !a.SubmittedAt.Equal(b.SubmittedAt):
			return a.SubmittedAt.Before(b.SubmittedAt)
		}
		return ids[i] < ids[j]
	})
	return ids
}

// trimmedTurns is the set of turns before the trim point.
func trimmedTurns(s State, ids []string) map[string]bool {
	out := map[string]bool{}
	if s.ContextFrom == "" {
		return out
	}
	found := false
	for _, id := range ids {
		if id == s.ContextFrom {
			found = true
		}
	}
	if !found {
		return out
	}
	for _, id := range ids {
		if id == s.ContextFrom {
			break
		}
		out[id] = true
	}
	return out
}

// chatRefs lists the content refs the chat is waiting on, for every turn.
func chatRefs(s State, in chatInput) []string {
	var refs []string
	for _, id := range chatTurns(s) {
		refs = append(refs, answerRefs(s, id, in.answer(id))...)
		if _, ok := in.inputs[id]; !ok && s.Turns[id].InputRef != "" {
			refs = append(refs, s.Turns[id].InputRef)
		}
	}
	return refs
}

// renderChat returns the whole conversation, oldest turn first and unclipped;
// the App scrolls it. Each turn shows what the user typed, then its outcome.
func renderChat(s State, in chatInput) string {
	lines, _ := chatLines(s, in)
	return strings.Join(lines, "\n")
}

// chatLines is renderChat by line, with the index of the newest turn's first line.
func chatLines(s State, in chatInput) (lines []string, lastTurn int) {
	ids := chatTurns(s)
	trimmed := trimmedTurns(s, ids)
	for _, id := range ids {
		lastTurn = len(lines)
		ai := in.answer(id)
		var turn []string
		text, ok := in.inputs[id]
		if !ok && s.Turns[id].InputRef != "" && in.content != nil {
			text, ok = body(in.content, s.Turns[id].InputRef), true
		}
		if ok {
			turn = append(turn, "you: "+strings.ReplaceAll(text, "\n", "\n     "))
		} else {
			turn = append(turn, "you: (input not loaded)")
		}
		turn = append(turn, strings.Split(renderAnswer(s, id, ai), "\n")...)
		if trimmed[id] {
			turn = append(turn, notInContext)
			if !ai.plainMode {
				for i, l := range turn {
					turn[i] = dimmed.Render(l)
				}
			}
		}
		lines = append(lines, turn...)
	}
	return lines, lastTurn
}
