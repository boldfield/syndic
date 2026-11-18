package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: syndic <subcommand> [options]")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "probe":
		probeCmd := flag.NewFlagSet("probe", flag.ContinueOnError)
		probeCmd.Usage = func() {
			fmt.Fprintf(os.Stderr, "usage: syndic probe [options]\n")
			probeCmd.PrintDefaults()
		}
		exitCode := cmdProbe(probeCmd, os.Args[2:])
		os.Exit(exitCode)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}
