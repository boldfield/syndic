package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var subcommand string
	var args []string

	if len(os.Args) < 2 {
		// Default to tui with no subcommand
		subcommand = "tui"
		args = []string{}
	} else {
		subcommand = os.Args[1]
		args = os.Args[2:]
	}

	switch subcommand {
	case "tui":
		tuiCmd := flag.NewFlagSet("tui", flag.ContinueOnError)
		tuiCmd.Usage = func() {
			fmt.Fprintf(os.Stderr, "usage: syndic tui [options]\n")
			tuiCmd.PrintDefaults()
		}
		exitCode := cmdTUI(tuiCmd, args)
		os.Exit(exitCode)
	case "probe":
		probeCmd := flag.NewFlagSet("probe", flag.ContinueOnError)
		probeCmd.Usage = func() {
			fmt.Fprintf(os.Stderr, "usage: syndic probe [options]\n")
			probeCmd.PrintDefaults()
		}
		exitCode := cmdProbe(probeCmd, args)
		os.Exit(exitCode)
	case "conversations":
		convCmd := flag.NewFlagSet("conversations", flag.ContinueOnError)
		convCmd.Usage = func() {
			fmt.Fprintf(os.Stderr, "usage: syndic conversations [options]\n")
			convCmd.PrintDefaults()
		}
		exitCode := cmdConversations(convCmd, args)
		os.Exit(exitCode)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		os.Exit(2)
	}
}
