package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// chatInput is everything the chat tab needs beyond the State.
type chatInput struct {
	answer               func(turnID string) answerInput // what the answer part of one turn needs
	inputs               map[string]string               // what the user typed, for the turns this process submitted
	content              *ContentCache                   // bodies of the inputs of the other turns
	width                int                             // terminal width; user text is wrapped and indented to it when positive
	selectedDrawerTurnID string                          // ID of the selected turn in the drawer, or empty if drawer not open
	missedTerminalEvents map[string]string               // turn ids -> outcome, for reconnect notices
	missedTerminalTimes  map[string]string               // turn ids -> timestamp, for reconnect notices
}

// userMark begins a user input in column 0.
const userMark = "› "

// notInContext marks a turn that /trim dropped from the history later turns see.
const notInContext = "not in context"

var dimmed = lipgloss.NewStyle().Faint(true)

// selectedMark takes the place of userMark on the drawer's selected input in
// a plain terminal. It is as wide as userMark, so the chat is not reflowed.
const selectedMark = "» "

// markSelected shows an input's first line, which begins with userMark, as the
// drawer's selected input: styled when color is available, otherwise with
// selectedMark in place of userMark.
func markSelected(l string, plain bool) string {
	if plain {
		return selectedMark + strings.TrimPrefix(l, userMark)
	}
	return selectedInput.Render(l)
}

var selectedInput = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "33", Dark: "75"})

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

// trimmedTurns is the set of turns before the trim point: the one this
// process set for its next submit, else the one the newest turn that has a
// history was submitted with.
func trimmedTurns(s State, ids []string) map[string]bool {
	out := map[string]bool{}
	from := s.ContextFrom
	for i := len(ids) - 1; from == "" && i >= 0; i-- {
		from = s.Turns[ids[i]].ContextFrom
	}
	if from == "" || !slices.Contains(ids, from) {
		return out
	}
	for _, id := range ids {
		if id == from {
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
	lines, lastTurn, _ = chatLinesWithStarts(s, in)
	return lines, lastTurn
}

// chatLinesWithStarts is chatLines with the index of each turn's first line,
// oldest first, so a view can count the turns above it.
func chatLinesWithStarts(s State, in chatInput) (lines []string, lastTurn int, starts []int) {
	lines, _, lastTurn, starts = chatRows(s, in)
	return lines, lastTurn, starts
}

// chatRows is chatLinesWithStarts with, for every line, the item it belongs to and the
// characters of that item it shows. Item ids are stable across renders and widths; the
// characters are counted in runes of the item's own text: a row's indent or mark and the
// break between rows are not counted, so a character keeps its offset at every width. An
// empty row counts as one character, the line break it stands for.
func chatRows(s State, in chatInput) (lines []string, rows []FollowRow, lastTurn int, starts []int) {
	ids := chatTurns(s)
	trimmed := trimmedTurns(s, ids)
	for _, id := range ids {
		lastTurn = len(lines)
		starts = append(starts, lastTurn)
		ai := in.answer(id)
		isSelected := id == in.selectedDrawerTurnID
		var turn []chatItem
		text, ok := in.inputs[id]
		if !ok && s.Turns[id].InputRef != "" && in.content != nil {
			text, ok = bodyRaw(in.content, s.Turns[id].InputRef), true
		}
		var input []string
		if ok {
			if in.width > 0 {
				// The mark takes the place of the first line's indent; the
				// continuation lines are plain content lines.
				for i, l := range SafeTextWithPrefix(text, in.width) {
					if i == 0 {
						l = userMark + strings.TrimPrefix(l, contentPrefix)
					}
					if isSelected && i == 0 {
						l = markSelected(l, ai.plainMode)
					} else if isSelected && !ai.plainMode {
						l = selectedInput.Render(l)
					}
					input = append(input, l)
				}
			} else {
				lines := strings.Split(ToSafeText([]byte(text)), "\n")
				lines[0] = userMark + lines[0]
				if isSelected {
					lines[0] = markSelected(lines[0], ai.plainMode)
				}
				input = append(input, strings.Join(lines, "\n"+contentPrefix))
			}
		} else {
			l := userMark + "(input not loaded)"
			if isSelected {
				l = markSelected(l, ai.plainMode)
			}
			input = append(input, l)
		}
		turn = append(turn, chatItem{"input", input})
		answer := answerItems(s, id, ai)
		if len(answer) == 0 {
			// A turn with nothing to show still takes one blank line.
			answer = []chatItem{{"answer", []string{""}}}
		}
		turn = append(turn, answer...)
		// Add reconnect notice if this turn has a missed terminal event
		if outcome, ok := in.missedTerminalEvents[id]; ok {
			if at, okTime := in.missedTerminalTimes[id]; okTime {
				noticeLine := fmt.Sprintf("reconnected: turn ended while detached: %s at %s", outcome, at)
				if !ai.plainMode {
					noticeLine = dimmed.Render(noticeLine)
				}
				turn = append(turn, chatItem{"reconnected", []string{noticeLine}})
			}
		}
		if trimmed[id] {
			turn = append(turn, chatItem{"not-in-context", []string{notInContext}})
			if !ai.plainMode {
				for _, it := range turn {
					for i, l := range it.lines {
						it.lines[i] = dimmed.Render(l)
					}
				}
			}
		}
		seen := map[string]int{}
		for _, it := range turn {
			name := it.id
			if seen[name]++; seen[name] > 1 {
				name = fmt.Sprintf("%s#%d", name, seen[name])
			}
			id := id + "/" + name
			char := 0
			for _, l := range strings.Split(strings.Join(it.lines, "\n"), "\n") {
				n := rowChars(l)
				if n == 0 {
					// A wrap never makes an empty row, so an empty row is a line break of
					// the item's own text at every width. It holds that break as its one
					// character, so it has an origin of its own and the next row does not
					// share it.
					n = 1
				}
				lines = append(lines, l)
				rows = append(rows, FollowRow{Item: id, Char: char, End: char + n})
				char += n
			}
		}
	}
	return lines, rows, lastTurn, starts
}

// rowChars counts the runes of text a drawn row holds, without its indent or mark.
// The drawer's selectedMark is a mark too, so selecting an input does not move its
// characters.
func rowChars(l string) int {
	l = ansi.Strip(l)
	switch {
	case strings.HasPrefix(l, userMark):
		l = strings.TrimPrefix(l, userMark)
	case strings.HasPrefix(l, selectedMark):
		l = strings.TrimPrefix(l, selectedMark)
	default:
		l = strings.TrimPrefix(l, contentPrefix)
	}
	return utf8.RuneCountInString(l)
}
