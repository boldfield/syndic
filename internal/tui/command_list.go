package tui

import (
	"sort"
	"strings"
)

// Command describes a command available on the input prompt.
type Command struct {
	Name  string // the command name without the /
	Help  string // one-line description
	Value string // current value, if any
}

// commandListState manages the command list that appears when the input
// starts with "/" and contains no space.
type commandListState struct {
	commands []Command
	selected int // index into commands; -1 if none selected
}

// buildCommandList returns the commands matching prefix, sorted alphabetically,
// with their current values where applicable.
func buildCommandList(prefix string, a *App) []Command {
	prefix = strings.ToLower(prefix)
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	prefix = strings.TrimPrefix(prefix, "/")

	var out []Command

	// All available commands with their descriptions
	allCmds := map[string]string{
		"new":           "start a new conversation",
		"conversations": "list this directory's conversations",
		"switch":        "open the conversation with this id or prefix",
		"trim":          "leave the turns before this one out of later history",
		"rounds":        "show, or set for this process, the revisions per turn",
		"budget":        "tokens reserved, charged and the limit of the turn",
		"mode":          "use the preset for the next submit only",
		"quick":         "select or deselect a quick profile for this process",
		"participants":  "show the participants tab",
		"cancel":        "cancel the open turn",
		"why":           "why the current or last turn ended, with the detail",
		"override":      "accept an incomplete turn's candidate, with a reason",
		"release":       "release held capacity, after a confirmation",
		"drafts":        "show or hide the author's unreviewed draft",
		"raw":           "show answers unrendered (on) or as Markdown (off)",
		"notify":        "turn on|off the terminal bell for completed turns",
		"mouse":         "turn on|off mouse capture in the TUI",
		"resume":        "reconnect to the engine",
		"quit":          "leave; an open turn keeps running",
		"help":          "show this list of commands and keys",
	}

	// Get current values for toggle commands
	values := map[string]string{
		"drafts": func() string {
			if a.drafts != nil {
				if *a.drafts {
					return "on"
				}
				return "off"
			}
			return ""
		}(),
		"raw": func() string {
			if a.rawMode {
				return "on"
			}
			return "off"
		}(),
	}

	// Match commands
	for name, help := range allCmds {
		if strings.HasPrefix(name, prefix) {
			cmd := Command{Name: name, Help: help}
			if val, ok := values[name]; ok && val != "" {
				cmd.Value = val
			}
			out = append(out, cmd)
		}
	}

	// Sort alphabetically
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	return out
}

// commandListLines renders the command list as strings, each with name, help,
// and optional value.
func commandListLines(commands []Command, width int) []string {
	if len(commands) == 0 {
		return nil
	}

	var out []string
	for _, cmd := range commands {
		line := "/" + cmd.Name
		if cmd.Value != "" {
			line += " (" + cmd.Value + ")"
		}
		line += " — " + cmd.Help
		out = append(out, line)
	}
	return out
}

// shortHelpLines returns the help text for the keys that apply now.
func shortHelpLines() []string {
	return []string{
		"?: show/hide this help  Tab: complete or switch tabs  ↑↓: select command  Enter: submit  Esc: close",
	}
}
