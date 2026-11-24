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
	scroll   int // first visible command index
}

const maxVisibleCommands = 5

// buildCommandList returns the commands matching prefix, sorted alphabetically,
// with their current values where applicable. It derives the list from the
// registered commands and their help text to avoid duplication.
func buildCommandList(prefix string, a *App) []Command {
	prefix = strings.ToLower(prefix)
	if !strings.HasPrefix(prefix, "/") {
		return nil
	}
	prefix = strings.TrimPrefix(prefix, "/")

	var out []Command

	// Build help map from commandHelp
	helpMap := make(map[string]string)
	for _, line := range commandHelp {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) >= 2 {
			name := strings.TrimPrefix(parts[0], "/")
			helpMap[name] = line
		}
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

	// Match commands from the registered commands map
	for name := range commands {
		if strings.HasPrefix(name, prefix) {
			help := ""
			if line, ok := helpMap[name]; ok {
				help = line
			}
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

// commandListLines renders the visible command list as strings, each with name, help,
// and optional value. Only shows up to maxVisibleCommands starting from scroll position.
func commandListLines(commands []Command, width int, scroll int) []string {
	if len(commands) == 0 {
		return nil
	}

	var out []string
	end := min(scroll+maxVisibleCommands, len(commands))
	for i := scroll; i < end; i++ {
		cmd := commands[i]
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
