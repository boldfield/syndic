package tui

import (
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// conversationListState is the conversation list's state beyond the fetched items.
type conversationListState struct {
	sel          int    // selected row, -1 for none
	selFor       string // the filter text sel applies to
	dismissedFor string // the filter text the list was closed on
	dismissed    bool
	filter       string // the current filter text ("/conversations", "/conversations x", etc)
	items        []wire.ConversationSummary
	currentWS    string // workspace of the app
}

// conversationListOpen reports whether the conversation list is showing.
// It needs room, the reviews drawer closed, a fetched list, and no dismissal.
func (a *App) conversationListOpen() bool {
	if a.reviewsDrawer.IsOpen() || a.width < regionMinWidth || a.height < regionMinHeight {
		return false
	}
	if !a.convListActive() {
		return false
	}
	if a.convList.dismissed && a.convList.dismissedFor == a.convList.filter {
		return false
	}
	return len(a.conversationListMatches()) > 0
}

// convListActive reports whether the conversation list is being activated
// (user typed /conversations with an optional filter).
func (a *App) convListActive() bool {
	line := a.input.Value()
	if !strings.HasPrefix(line, "/conversations") {
		return false
	}
	rest := strings.TrimPrefix(line, "/conversations")
	// Either end of line or a space followed by filter text
	return rest == "" || strings.HasPrefix(rest, " ")
}

// forgetStaleConvList resets selection when the input filter changes.
func (a *App) forgetStaleConvList() {
	filter := a.convListFilter()
	if a.convList.dismissed && a.convList.dismissedFor != filter {
		a.convList.dismissed = false
	}
	if a.convList.selFor != filter {
		a.convList.sel, a.convList.selFor = -1, ""
	}
	a.convList.filter = filter
}

// convListFilter extracts the filter text from the input.
// If input is "/conversations abc", returns "abc".
// If input is "/conversations", returns "".
func (a *App) convListFilter() string {
	line := strings.TrimSpace(a.input.Value())
	if !strings.HasPrefix(line, "/conversations") {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "/conversations"))
	return rest
}

// conversationListMatches returns conversations matching the current filter.
func (a *App) conversationListMatches() []conversationEntry {
	filter := a.convListFilter()
	filter = strings.ToLower(filter)
	var out []conversationEntry

	// Add "new" entry
	if filter == "" || strings.HasPrefix("new", filter) {
		out = append(out, conversationEntry{
			ID:         "",
			IsNew:      true,
			Selectable: true,
		})
	}

	// Add existing conversations
	for _, c := range a.convList.items {
		if !convMatchesFilter(c, filter, a.workDir) {
			continue
		}
		selectable := c.Workspace == nil || *c.Workspace == a.workDir
		out = append(out, conversationEntry{
			ID:         c.ID,
			Summary:    &c,
			Selectable: selectable,
		})
	}
	return out
}

// convMatchesFilter checks if a conversation matches the filter text.
func convMatchesFilter(c wire.ConversationSummary, filter string, workspace string) bool {
	if filter == "" {
		return true
	}
	// Match against ID prefix, preview, or workspace
	if strings.Contains(strings.ToLower(c.ID), filter) {
		return true
	}
	if strings.Contains(strings.ToLower(c.Preview), filter) {
		return true
	}
	if c.Workspace != nil && strings.Contains(strings.ToLower(*c.Workspace), filter) {
		return true
	}
	return false
}

// conversationEntry is one row of the conversation list.
type conversationEntry struct {
	ID         string
	IsNew      bool
	Summary    *wire.ConversationSummary
	Selectable bool
}

// convListSel returns the selected row index, or -1.
func (a *App) convListSel() int {
	if a.convList.selFor != a.convListFilter() {
		return -1
	}
	return a.convList.sel
}

// closeConversationList closes the list until the input changes.
func (a *App) closeConversationList() {
	a.convList.dismissed, a.convList.dismissedFor = true, a.convListFilter()
}

// moveConversationSel moves the selection by one row, skipping non-selectable entries.
func (a *App) moveConversationSel(delta int) {
	matches := a.conversationListMatches()
	sel := a.convListSel()
	if len(matches) == 0 {
		return
	}

	if sel < 0 {
		if delta > 0 {
			sel = 0
		} else {
			sel = len(matches) - 1
		}
	} else {
		sel = sel + delta
	}

	// Clamp to valid range
	sel = min(max(sel, 0), len(matches)-1)

	// Skip non-selectable entries
	for sel >= 0 && sel < len(matches) && !matches[sel].Selectable {
		if delta > 0 {
			sel++
		} else {
			sel--
		}
	}

	// Clamp again after skipping
	sel = min(max(sel, 0), len(matches)-1)

	a.convList.sel, a.convList.selFor = sel, a.convListFilter()
}

// conversationListLines renders the conversation list in the given number of rows.
func (a *App) conversationListLines(rows int) []string {
	matches := a.conversationListMatches()
	sel := a.convListSel()
	first := 0
	if sel >= rows {
		first = sel - rows + 1
	}

	var out []string
	for i := first; i < len(matches) && len(out) < rows; i++ {
		e := matches[i]
		marker := "  "
		if i == sel {
			marker = "› "
		}

		var line string
		if e.IsNew {
			line = marker + "new"
		} else {
			// Format: ID, activity, turns, preview
			c := e.Summary
			mark := " "
			if c.ID == a.conversationID {
				mark = "*"
			}

			running := ""
			if c.Running {
				running = ", running"
			}

			activityStr := activity(c.LastSubmittedAt)
			turnCountStr := fmt.Sprintf("%d", c.TurnCount)
			preview := safeLine(c.Preview)

			// If not selectable (different workspace), apply dimming
			if !e.Selectable {
				line = marker + mark + " " + dimText(shortID(c.ID)) + "  " +
					dimText(activityStr) + "  " + dimText(turnCountStr) + " turns" + running + "  " + dimText(preview)
			} else {
				line = marker + mark + " " + shortID(c.ID) + "  " +
					activityStr + "  " + turnCountStr + " turns" + running + "  " + preview
			}
		}

		line = ansi.Truncate(line, max(a.width, 1), "…")
		out = append(out, line)
	}
	return out
}

// dimText applies a dim style to text (using ANSI escape codes).
// This will be replaced with actual styling if Bubble Tea provides it.
func dimText(s string) string {
	// For now, we'll just return the text as-is and rely on rendering elsewhere
	// In a real implementation, this would wrap with ANSI codes or use Lipgloss
	return s
}

// enterConversationSelection switches to or creates the selected conversation.
func (a *App) enterConversationSelection() tea.Cmd {
	matches := a.conversationListMatches()
	sel := a.convListSel()
	if sel < 0 || sel >= len(matches) {
		return nil
	}

	entry := matches[sel]
	a.closeConversationList()

	if entry.IsNew {
		a.openConversation("", nil)
		a.say("new conversation; the next submit creates it" + a.runningElsewhere())
		return nil
	}

	// Switch to the selected conversation
	if a.cl == nil {
		a.say("engine: not connected")
		return nil
	}

	gen, cl, ctx, id := a.gen, a.cl, a.ctx, entry.ID
	return func() tea.Msg {
		conv, err := cl.Conversation(ctx, id)
		return switchMsg{gen: gen, conv: conv, err: err}
	}
}

// convListNKey handles 'n' pressed while the conversation list is open.
// It creates a new conversation.
func (a *App) convListNKey() {
	a.openConversation("", nil)
	a.say("new conversation; the next submit creates it" + a.runningElsewhere())
	a.closeConversationList()
}
