package main

import (
	"bytes"
	"context"
	"flag"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// parseV6Flags parses args as the tui command's start-up flags.
func parseV6Flags(t *testing.T, args ...string) startupFlags {
	t.Helper()
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	f, err := parseStartupFlags(fs, args)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return f
}

// v6FlagCases are every combination of --notify and --mouse.
var v6FlagCases = []struct {
	args          []string
	notify, mouse bool
}{
	{nil, false, false},
	{[]string{"--notify"}, true, false},
	{[]string{"--mouse"}, false, true},
	{[]string{"--notify", "--mouse"}, true, true},
}

// --notify and --mouse are off unless given, and each is its own switch.
func TestV6CoverageNotifyAndMouseFlags(t *testing.T) {
	for _, tc := range v6FlagCases {
		if f := parseV6Flags(t, tc.args...); f.notify != tc.notify || f.mouse != tc.mouse {
			t.Errorf("%v: notify %v, mouse %v; want %v, %v", tc.args, f.notify, f.mouse, tc.notify, tc.mouse)
		}
	}
}

// v6FlagApp is the App cmdTUI builds from the parsed flags, on a colour
// terminal of 100x30.
func v6FlagApp(t *testing.T, f startupFlags) *tui.App {
	t.Helper()
	opts := append(f.appOptions(), tui.WithTerminalCapabilities(tui.TerminalCapabilities{Color: true, AltScreen: true}))
	a := tui.NewApp(context.Background(), "", opts...)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return a
}

func v6Screen(a *tui.App) []string { return strings.Split(ansi.Strip(a.View()), "\n") }

// Each parsed flag changes its own App setting and no other: --notify turns
// notify on (the command list shows its value), and --mouse makes a click on
// a tab switch to it.
func TestV6CoverageNotifyAndMouseFlagsTakeEffect(t *testing.T) {
	for _, tc := range v6FlagCases {
		f := parseV6Flags(t, tc.args...)

		a := v6FlagApp(t, f)
		for _, r := range "/notify" {
			a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		want := "[off]"
		if tc.notify {
			want = "[on]"
		}
		var notifyLine string
		for _, row := range v6Screen(a) {
			if strings.Contains(row, "/notify on|off") {
				notifyLine = row
			}
		}
		if !strings.Contains(notifyLine, want) {
			t.Errorf("%v: notify entry %q, want value %s", tc.args, notifyLine, want)
		}

		a = v6FlagApp(t, f)
		bar := v6Screen(a)[0]
		x := strings.Index(bar, "transcript")
		if x < 0 {
			t.Fatalf("%v: no transcript tab in %q", tc.args, bar)
		}
		a.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: ansi.StringWidth(bar[:x]) + 2, Y: 0})
		if got := strings.Contains(v6Screen(a)[0], "[transcript]"); got != tc.mouse {
			t.Errorf("%v: after clicking the transcript tab, switched = %v, want %v (bar %q)", tc.args, got, tc.mouse, v6Screen(a)[0])
		}
	}
}

// quitModel ends its program at once.
type quitModel struct{}

func (quitModel) Init() tea.Cmd                       { return tea.Quit }
func (quitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return quitModel{}, nil }
func (quitModel) View() string                        { return "" }

// mouseCellMotionOn is the sequence that starts mouse capture.
const mouseCellMotionOn = "\x1b[?1002h"

// The program cmdTUI runs captures the mouse with --mouse on a colour
// terminal, and never without it or with colour off.
func TestV6CoverageMouseFlagCapturesTheMouse(t *testing.T) {
	colour := tui.TerminalCapabilities{Color: true}
	for _, tc := range []struct {
		args []string
		caps tui.TerminalCapabilities
		want bool
	}{
		{nil, colour, false},
		{[]string{"--notify"}, colour, false},
		{[]string{"--mouse"}, colour, true},
		{[]string{"--notify", "--mouse"}, colour, true},
		{[]string{"--mouse"}, tui.TerminalCapabilities{}, false},
	} {
		f := parseV6Flags(t, tc.args...)
		var out bytes.Buffer
		opts := append(f.programOptions(tc.caps), tea.WithInput(nil), tea.WithOutput(&out))
		if _, err := tea.NewProgram(quitModel{}, opts...).Run(); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got := strings.Contains(out.String(), mouseCellMotionOn); got != tc.want {
			t.Errorf("%v colour=%v: mouse captured = %v, want %v (output %q)", tc.args, tc.caps.Color, got, tc.want, out.String())
		}
	}
}

// /help lists every flag the command parses, taken from its FlagSet.
func TestV6CoverageHelpListsEveryParsedFlag(t *testing.T) {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	if _, err := parseStartupFlags(fs, nil); err != nil {
		t.Fatal(err)
	}
	help := strings.Join(tui.HelpFlagLines(), "\n")
	fs.VisitAll(func(f *flag.Flag) {
		if !strings.Contains(help, "--"+f.Name) {
			t.Errorf("/help does not list --%s", f.Name)
		}
	})
}
