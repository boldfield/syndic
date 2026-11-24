package tui

import (
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// convChooser is the state of the open /conversations list. Filtering is a
// sub-mode: "/" starts it, and only then do letters, "n" included, go to the
// filter instead of acting as keys.
type convChooser struct {
	items     []wire.ConversationSummary
	loaded    bool   // the list has arrived, empty or not
	err       error  // why it could not be read
	filtering bool   // letters edit the filter
	filter    string // case-insensitive substring of id, preview or workspace
	sel       int    // index into visible(), -1 for none
}

// selectable reports whether the conversation may be chosen: it belongs to
// this directory, or to none (a null workspace matches any directory).
func (a *App) selectable(c wire.ConversationSummary) bool {
	return c.Workspace == nil || *c.Workspace == a.workDir
}

// visible is the items the filter keeps, in list order.
func (c *convChooser) visible() []wire.ConversationSummary {
	if c.filter == "" {
		return c.items
	}
	needle := strings.ToLower(c.filter)
	var out []wire.ConversationSummary
	for _, it := range c.items {
		ws := ""
		if it.Workspace != nil {
			ws = *it.Workspace
		}
		hay := strings.ToLower(it.ID + "\n" + safeLine(it.Preview) + "\n" + safeLine(ws))
		if strings.Contains(hay, needle) {
			out = append(out, it)
		}
	}
	return out
}

// firstSelectable is the index of the first row that can be chosen, or -1.
func (a *App) firstSelectable(rows []wire.ConversationSummary) int {
	for i, r := range rows {
		if a.selectable(r) {
			return i
		}
	}
	return -1
}

// resetSel selects the first row that can be chosen.
func (a *App) resetSel() {
	a.conv.sel = a.firstSelectable(a.conv.visible())
}

// moveConversationSel moves the selection to the next row in the direction
// that can be chosen; with none that way it stays where it is.
func (a *App) moveConversationSel(delta int) {
	rows := a.conv.visible()
	for i := a.conv.sel + delta; i >= 0 && i < len(rows); i += delta {
		if a.selectable(rows[i]) {
			a.conv.sel = i
			return
		}
	}
	if a.conv.sel < 0 || a.conv.sel >= len(rows) {
		a.resetSel()
	}
}

// closeConversations closes the list.
func (a *App) closeConversations() { a.conv = nil }

// conversationKey handles a key while the list is open; every key but the
// detach keys is the list's.
func (a *App) conversationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	c := a.conv
	k := msg.String()
	switch k {
	case "ctrl+c", "ctrl+d":
		return nil, nil, false
	case "esc":
		a.closeConversations()
		return a, nil, true
	case "up":
		a.moveConversationSel(-1)
		return a, nil, true
	case "down":
		a.moveConversationSel(1)
		return a, nil, true
	case "enter":
		return a, a.enterConversationSelection(), true
	}
	if c.filtering {
		switch {
		case k == "backspace":
			if r := []rune(c.filter); len(r) > 0 {
				c.filter = string(r[:len(r)-1])
				a.resetSel()
			}
		case msg.Type == tea.KeyRunes && !msg.Paste:
			c.filter += string(msg.Runes)
			a.resetSel()
		case msg.Type == tea.KeySpace:
			c.filter += " "
			a.resetSel()
		}
		return a, nil, true
	}
	switch k {
	case "/":
		c.filtering = true
	case "n":
		a.closeConversations()
		return a, cmdNew(a, nil), true
	}
	return a, nil, true
}

// enterConversationSelection switches to the selected conversation. A
// running turn is not cancelled: opening another conversation leaves it be.
func (a *App) enterConversationSelection() tea.Cmd {
	rows := a.conv.visible()
	if a.conv.sel < 0 || a.conv.sel >= len(rows) || !a.selectable(rows[a.conv.sel]) {
		return nil
	}
	id := rows[a.conv.sel].ID
	a.closeConversations()
	return cmdSwitch(a, []string{id})
}

// conversationRow is one conversation on one line: id prefix, last activity,
// turn count, preview, and for another directory's the workspace.
func (a *App) conversationRow(c wire.ConversationSummary) string {
	mark := " "
	if c.ID == a.conversationID {
		mark = "*"
	}
	running := ""
	if c.Running {
		running = ", running"
	}
	line := fmt.Sprintf("%s %-8s  %s  %d turns%s  %s", mark, shortID(c.ID), activity(c.LastSubmittedAt), c.TurnCount, running, safeLine(c.Preview))
	if !a.selectable(c) {
		line += "  [" + safeLine(*c.Workspace) + "]"
	}
	return line
}

// conversationLines draws the list in at most rows lines of width: a title,
// a window of the rows that always holds the selected one, and the keys.
func (a *App) conversationLines(rows, width int) []string {
	c := a.conv
	width = max(width, 1)
	title := "conversations in " + safeLine(a.workDir) + ", latest activity first (* is open):"
	if c.filtering || c.filter != "" {
		title = "filter: " + safeLine(c.filter)
		if c.filtering {
			title += "▏"
		}
	}
	keys := "↑ ↓ select · Enter switch · n new · / filter · Esc close"
	if c.filtering {
		keys = "type to filter · ↑ ↓ select · Enter switch · Esc close"
	}
	out := []string{ansi.Truncate(title, width, "…")}
	body := max(rows-2, 0)
	vis := c.visible()
	switch {
	case c.err != nil:
		out = append(out, ansi.Truncate("  "+formatRefusal(c.err), width, "…"))
	case !c.loaded:
		out = append(out, "  loading…")
	case len(vis) == 0:
		out = append(out, "  no conversations")
	default:
		first := 0
		if c.sel >= body {
			first = c.sel - body + 1
		}
		for i := first; i < len(vis) && i < first+body; i++ {
			marker := "  "
			if i == c.sel {
				marker = "› "
			}
			line := ansi.Truncate(marker+a.conversationRow(vis[i]), width, "…")
			if !a.selectable(vis[i]) {
				line = dimmed.Render(line)
			}
			out = append(out, line)
		}
	}
	out = append(out, ansi.Truncate(keys, width, "…"))
	if len(out) > rows && rows > 0 {
		out = append(out[:rows-1], out[len(out)-1])
	}
	return out
}
