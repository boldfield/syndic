package main

import (
	"context"
	"strings"

	"bytes"
	"flag"
	"github.com/boldfield/syndic/internal/tui"
	"testing"
)

// --notify and --mouse are off unless given, and each is its own switch.
func TestV6CoverageNotifyAndMouseFlags(t *testing.T) {
	parse := func(args ...string) startupFlags {
		t.Helper()
		fs := flag.NewFlagSet("tui", flag.ContinueOnError)
		fs.SetOutput(&bytes.Buffer{})
		f, err := parseStartupFlags(fs, args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return f
	}
	for _, tc := range []struct {
		args          []string
		notify, mouse bool
	}{
		{nil, false, false},
		{[]string{"--notify"}, true, false},
		{[]string{"--mouse"}, false, true},
		{[]string{"--notify", "--mouse"}, true, true},
	} {
		if f := parse(tc.args...); f.notify != tc.notify || f.mouse != tc.mouse {
			t.Errorf("%v: notify %v, mouse %v; want %v, %v", tc.args, f.notify, f.mouse, tc.notify, tc.mouse)
		}
	}
}

// The flags reach the TUI as options at start-up: none when absent, one each
// when given, and the resulting options build an App.
func TestV6CoverageNotifyAndMouseFlagsBecomeOptions(t *testing.T) {
	for _, tc := range []struct {
		f    startupFlags
		want int
	}{
		{startupFlags{}, 0},
		{startupFlags{notify: true}, 1},
		{startupFlags{mouse: true}, 1},
		{startupFlags{notify: true, mouse: true}, 2},
	} {
		opts := tc.f.appOptions()
		if len(opts) != tc.want {
			t.Errorf("%+v: %d options, want %d", tc.f, len(opts), tc.want)
		}
		if tui.NewApp(context.Background(), "", opts...) == nil {
			t.Errorf("%+v: no app", tc.f)
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
