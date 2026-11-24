package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func kt(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// TestCommandCatalogueMatchesRegistry keeps the list, /help and the handlers
// from drifting apart.
func TestCommandCatalogueMatchesRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range commandEntries() {
		seen[e.name] = true
		if _, ok := commands[e.name]; !ok {
			t.Errorf("/%s is listed but has no handler", e.name)
		}
		if e.desc == "" {
			t.Errorf("/%s has no description", e.name)
		}
	}
	for name := range commands {
		if !seen[name] {
			t.Errorf("/%s has a handler but is not listed", name)
		}
	}
	if !seen["raw"] {
		t.Error("/raw is not listed")
	}
}

func TestCommandListMatches(t *testing.T) {
	for line, wantEmpty := range map[string]bool{"": true, "hello": true, "/raw x": true, "/zzz": true, "/": false, "/ra": false} {
		if got := len(commandListMatches(line)) == 0; got != wantEmpty {
			t.Errorf("commandListMatches(%q) empty = %v, want %v", line, got, wantEmpty)
		}
	}
	if m := commandListMatches("/ra"); len(m) != 1 || m[0].name != "raw" {
		t.Errorf("matches for /ra = %v, want only raw", m)
	}
}

func TestCommandListShowsDescriptionAndValue(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	send(r, runes("/ra"))
	v := untilView(t, r, "/raw on", "[off] show answers")
	if strings.Contains(v, "[on]") {
		t.Errorf("raw is off but the list shows a value on:\n%s", v)
	}
	send(r, kt(tea.KeyEsc))
	send(r, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	send(r, runes("/raw on"), kt(tea.KeyEnter))
	r.until(t, "raw on", func(a *App) (string, bool) { return "", a.rawMode })
	r.until(t, "an empty input", func(a *App) (string, bool) { return "", a.input.Value() == "" })
	send(r, runes("/ra"))
	untilView(t, r, "/raw on|off", "[on] show answers")
	send(r, kt(tea.KeyEsc), tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	send(r, runes("/dr"))
	untilView(t, r, "/drafts on|off", "[off]")
}

func TestArrowSelectionLeavesInputUntilTab(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	send(r, runes("/"), kt(tea.KeyDown), kt(tea.KeyDown))
	r.until(t, "the second row selected", func(a *App) (string, bool) {
		return "", a.commandListSel() == 1 && a.input.Value() == "/"
	})
	want := commandListMatches("/")[1].name
	v := untilView(t, r, "› /"+want)
	if strings.Count(v, "\n› ") != 1 {
		t.Errorf("want exactly one selected row:\n%s", v)
	}
	tab := r.curTab(t)
	send(r, kt(tea.KeyTab))
	r.until(t, "the command completed", func(a *App) (string, bool) {
		return "", a.input.Value() == "/"+want+" " && a.tab == tab
	})
	if v := view(t, r); strings.Contains(v, "› /") {
		t.Errorf("the list stayed open after completion:\n%s", v)
	}
}

func (r *rig) curTab(t *testing.T) tab {
	t.Helper()
	var id tab
	r.until(t, "the tab", func(a *App) (string, bool) { id = a.tab; return "", true })
	return id
}

func TestArrowSelectionScrollsPastTheWindow(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	n := len(commandListMatches("/"))
	if n <= maxListLines {
		t.Fatalf("need more than %d commands to test the window, have %d", maxListLines, n)
	}
	send(r, runes("/"), kt(tea.KeyUp))
	last := commandListMatches("/")[n-1].name
	untilView(t, r, "› /"+last)
	send(r, kt(tea.KeyUp), kt(tea.KeyUp), kt(tea.KeyUp), kt(tea.KeyUp), kt(tea.KeyUp), kt(tea.KeyUp))
	prev := commandListMatches("/")[n-7].name
	untilView(t, r, "› /"+prev)
}

func TestEscClosesCommandListAndEditReopens(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	send(r, runes("/r"))
	untilView(t, r, "/raw")
	send(r, kt(tea.KeyEsc))
	r.until(t, "the list closed", func(a *App) (string, bool) { return "", !a.commandListOpen() && a.input.Value() == "/r" })
	send(r, runes("a"))
	r.until(t, "the list open again", func(a *App) (string, bool) { return "", a.commandListOpen() })
}

func TestTabSwitchesTabsWithoutAList(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	before := r.curTab(t)
	send(r, kt(tea.KeyTab))
	r.until(t, "the tab switched", func(a *App) (string, bool) { return "", a.tab != before })
}

func TestQuestionMarkOnlyOnEmptyInput(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	send(r, runes("?"))
	v := untilView(t, r, "? hide")
	if !strings.Contains(v, "Tab tabs") {
		t.Errorf("short help line missing:\n%s", v)
	}
	send(r, runes("?"))
	r.until(t, "short help hidden", func(a *App) (string, bool) { return "", !a.showShortHelp })
	send(r, runes("why"), runes("?"))
	r.until(t, "the question mark typed", func(a *App) (string, bool) {
		return "", a.input.Value() == "why?" && !a.showShortHelp
	})
}

func TestHelpListsEveryCommandAndKey(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	r.typeLine("/help")
	a := r.until(t, "the help panel", func(a *App) (string, bool) { return strings.Join(a.panel, "\n"), len(a.panel) > 0 })
	for _, e := range commandEntries() {
		if !strings.Contains(a, e.usage) {
			t.Errorf("/help lacks %s", e.usage)
		}
	}
	for _, want := range []string{"?  ", "Up, Down (command list)", "Tab (command list open)", "Ctrl+O"} {
		if !strings.Contains(a, want) {
			t.Errorf("/help lacks %q", want)
		}
	}
}

// TestDrawerClosesListAndShortHelp: opening the reviews drawer closes the
// command list and hides short help, and they stay closed afterwards.
func TestDrawerClosesListAndShortHelp(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched" && a.state.Turns[id].State != "open"
	})
	send(r, runes("/r"))
	untilView(t, r, "/raw")
	send(r, kt(tea.KeyCtrlO))
	r.until(t, "the drawer open and list closed", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen() && !a.commandListOpen()
	})
	send(r, kt(tea.KeyEsc))
	r.until(t, "the drawer closed", func(a *App) (string, bool) { return "", !a.reviewsDrawer.IsOpen() })
	if a := r.app; a.commandListOpen() {
		t.Error("the command list came back with the drawer closed")
	}
	send(r, kt(tea.KeyBackspace))
	send(r, kt(tea.KeyBackspace))
	r.until(t, "input cleared", func(a *App) (string, bool) { return "", a.input.Value() == "" })
	send(r, runes("?"))
	untilView(t, r, "? hide")
	send(r, kt(tea.KeyCtrlO))
	r.until(t, "the drawer open and help hidden", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen() && !a.showShortHelp
	})
	send(r, kt(tea.KeyEsc))
	r.until(t, "the drawer closed", func(a *App) (string, bool) { return "", !a.reviewsDrawer.IsOpen() })
	if v := view(t, r); strings.Contains(v, "? hide") {
		t.Errorf("short help came back with the drawer closed:\n%s", v)
	}
	// A list open before the drawer stays closed after it.
	send(r, runes("/r"))
	untilView(t, r, "/raw")
	send(r, kt(tea.KeyEsc))
	r.until(t, "list closed", func(a *App) (string, bool) { return "", !a.commandListOpen() })
}

// TestShortHelpHidesOnTyping: typing after ? hides the help, so ? then types.
func TestShortHelpHidesOnTyping(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	send(r, runes("?"))
	untilView(t, r, "? hide")
	send(r, runes("a"))
	r.until(t, "the help hidden", func(a *App) (string, bool) { return "", !a.showShortHelp && a.input.Value() == "a" })
	if v := view(t, r); strings.Contains(v, "? hide") {
		t.Errorf("short help still shown:\n%s", v)
	}
	send(r, runes("?"))
	r.until(t, "a typed question mark", func(a *App) (string, bool) { return "", a.input.Value() == "a?" })
}

// listView is the rendered command list for the input, on an App that no
// program goroutine is driving.
func listView(a *App, input string) string {
	a.input.SetValue(input)
	return strings.Join(a.commandListLines(40), "\n")
}

// TestCommandValuesShowDefaults: rows show the value in force, defaults included.
func TestCommandValuesShowDefaults(t *testing.T) {
	a := chatApp(t)
	for input, want := range map[string]string{
		"/rounds": "[2]",
		"/mode":   "[review]",
		"/notify": "[off]",
	} {
		if v := listView(a, input); !strings.Contains(v, want) {
			t.Errorf("%s: row lacks %s:\n%s", input, want, v)
		}
	}
	a.notify = true
	if v := listView(a, "/notify"); !strings.Contains(v, "[on]") {
		t.Errorf("notify on not shown:\n%s", v)
	}
	off := "off"
	a.quickSelection = &QuickSelection{Override: &off}
	if v := listView(a, "/quick"); !strings.Contains(v, "[off]") {
		t.Errorf("quick override not shown:\n%s", v)
	}
}

// TestCommandValueApprovedProjectQuick: an approved project-file profile shows.
func TestCommandValueApprovedProjectQuick(t *testing.T) {
	a := chatApp(t)
	a.state.Status.QuickProfiles = []wire.QuickProfile{{Name: "fast", DefinitionSHA256: "d1"}}
	a.quickSelection = &QuickSelection{
		ProjectProfile: "fast", ProjectFilePath: "/p/quick.toml", ProjectFileSHA: "f1",
		approved: map[string]ApprovedProfile{"/p/quick.toml": {
			FilePath: "/p/quick.toml", FileSHA256: "f1", ProfileName: "fast", ProfileDefinitionSHA256: "d1"}},
	}
	if v := listView(a, "/quick"); !strings.Contains(v, "[fast]") {
		t.Errorf("approved project profile not shown:\n%s", v)
	}
}

// TestCommandValuesAreSafeText: engine-derived values reach the list escaped.
func TestCommandValuesAreSafeText(t *testing.T) {
	hostile := "x\x1b]8;;http://e\x1b\\y\r\u009b31m\u202e\xff"
	a := chatApp(t)
	a.state.NextPreset = hostile
	a.quickSelection = &QuickSelection{Override: &hostile}
	for _, input := range []string{"/mode", "/quick"} {
		v := listView(a, input)
		if !strings.Contains(v, "\\x1b") {
			t.Errorf("%s: value not escaped visibly:\n%q", input, v)
		}
		for _, r := range v {
			if r == '\n' {
				continue
			}
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || bidiChars[r] || r == utf8.RuneError {
				t.Fatalf("%s: raw %U in %q", input, r, v)
			}
		}
	}
}
