package tui

// v6 TUI module imports: Lip Gloss, Bubbles, Glamour, Huh, Harmonica and bubblezone
// are imported here to prevent go mod tidy from removing them, since they are
// not yet used in the codebase. Later tasks will replace this with actual use
// of these modules.

import (
	_ "github.com/charmbracelet/bubbles"
	_ "github.com/charmbracelet/glamour"
	_ "github.com/charmbracelet/harmonica"
	_ "github.com/charmbracelet/huh"
	_ "github.com/lrstanley/bubblezone"
)
