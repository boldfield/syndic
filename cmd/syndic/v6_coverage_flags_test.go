package main

import (
	"bytes"
	"flag"
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
