package tui

import (
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/boldfield/syndic/internal/wire"
)

// commandEntry is one row of the command list.
const helpColumn = 26

type commandEntry struct {
	name  string // without the slash
	usage string // the command as /help writes it, with its arguments
	desc  string
}

// commandEntries is the command catalogue, taken from commandHelp so the list
// and /help cannot drift apart. A help line is the usage padded to
// helpColumn, then the description.
func commandEntries() []commandEntry {
	out := make([]commandEntry, 0, len(commandHelp))
	for _, line := range commandHelp {
		usage, desc := line, ""
		if len(line) > helpColumn {
			usage, desc = line[:helpColumn], line[helpColumn:]
		}
		usage = strings.TrimSpace(usage)
		name, _, _ := strings.Cut(strings.TrimPrefix(usage, "/"), " ")
		out = append(out, commandEntry{name: name, usage: usage, desc: strings.TrimSpace(desc)})
	}
	return out
}

// commandValue is the current value of a command that has one, or "".
func (a *App) commandValue(name string) string {
	switch name {
	case "drafts":
		return onOff(a.draftsOn(a.state.CurrentTurn))
	case "raw":
		return onOff(a.rawMode)
	case "rounds":
		if a.roundsIsSet {
			return strconv.Itoa(a.rounds)
		}
		if n := a.state.maxRounds(); n > 0 {
			return strconv.Itoa(n)
		}
	case "mode":
		if a.state.NextPreset != "" {
			return a.state.NextPreset
		}
		if a.state.Status != nil {
			return a.state.Status.DefaultPreset
		}
	case "quick":
		if qs := a.quickSelection; qs != nil {
			if sel := qs.selected(a.quickProfiles()); sel != nil {
				return *sel
			}
			if qs.Override != nil {
				return *qs.Override
			}
		}
	}
	return ""
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// commandListMatches is the commands the input line completes to: the input
// begins with "/" and has no space, and a command's name begins with the rest.
func commandListMatches(line string) []commandEntry {
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, " \t\n") {
		return nil
	}
	prefix := strings.TrimPrefix(line, "/")
	var out []commandEntry
	for _, e := range commandEntries() {
		if strings.HasPrefix(e.name, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// listState is the command list's state beyond the input line itself. Both
// fields name the input they were set for, so any edit of the input drops
// them and the list stays a function of the input.
type listState struct {
	sel          int    // selected row, -1 for none
	selFor       string // the input sel applies to
	dismissedFor string // the input the list was closed on
	dismissed    bool
}

// commandListOpen reports whether the list is showing. It needs room, and the
// reviews drawer closes it.
func (a *App) commandListOpen() bool {
	if a.reviewsDrawer.IsOpen() || a.width < regionMinWidth || a.height < regionMinHeight {
		return false
	}
	line := a.input.Value()
	if a.list.dismissed && a.list.dismissedFor == line {
		return false
	}
	return len(commandListMatches(line)) > 0
}

// forgetStaleList drops a dismissal once the input is no longer the one it
// closed the list on, so typing the same text again later opens the list.
func (a *App) forgetStaleList() {
	if a.list.dismissed && a.list.dismissedFor != a.input.Value() {
		a.list.dismissed = false
	}
	if a.list.selFor != a.input.Value() {
		a.list.sel, a.list.selFor = -1, ""
	}
}

// commandListSel is the selected row, or -1.
func (a *App) commandListSel() int {
	if a.list.selFor != a.input.Value() {
		return -1
	}
	return a.list.sel
}

// closeCommandList closes the list until the input changes.
func (a *App) closeCommandList() {
	a.list.dismissed, a.list.dismissedFor = true, a.input.Value()
}

// moveCommandSel moves the selection by one row, without changing the input.
func (a *App) moveCommandSel(delta int) {
	n := len(commandListMatches(a.input.Value()))
	sel := a.commandListSel()
	switch {
	case sel < 0 && delta > 0:
		sel = 0
	case sel < 0:
		sel = n - 1
	default:
		sel = min(max(sel+delta, 0), n-1)
	}
	a.list.sel, a.list.selFor = sel, a.input.Value()
}

// completeCommand puts the selected command (the first when none is selected)
// in the input, followed by a space, which closes the list.
func (a *App) completeCommand() {
	matches := commandListMatches(a.input.Value())
	if len(matches) == 0 {
		return
	}
	sel := max(a.commandListSel(), 0)
	a.input.SetValue("/" + matches[sel].name + " ")
	_ = a.updateInput(nil)
}

// commandListLines is the list as drawn in rows lines: a window of the matches
// that always holds the selected row, each as marker, usage, description and
// value in brackets, which comes before the description so a narrow screen
// cuts the description and not the value.
func (a *App) commandListLines(rows int) []string {
	matches := commandListMatches(a.input.Value())
	sel := a.commandListSel()
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
		line := marker + padRight(e.usage, helpColumn)
		if v := a.commandValue(e.name); v != "" {
			line += "[" + v + "] "
		}
		line += e.desc
		out = append(out, line)
	}
	return out
}

// shortHelpLine is one line of the keys that apply now.
func (a *App) shortHelpLine() string {
	switch {
	case a.commandListOpen():
		return "↑ ↓ select · Tab complete · Esc close · Enter run"
	case a.tab == tabChat:
		return "Tab tabs · PgUp PgDn scroll · / commands · Ctrl+O reviews · ? hide"
	default:
		return "Tab tabs · PgUp PgDn scroll · / commands · Alt+Enter newline · ? hide"
	}
}

func padRight(s string, n int) string {
	if w := runewidth.StringWidth(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s + " "
}

// quickProfiles is the roster's quick profiles, or none before connecting.
func (a *App) quickProfiles() []wire.QuickProfile {
	if a.state.Status == nil {
		return nil
	}
	return a.state.Status.QuickProfiles
}
