package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	subcommand, args, ok := routeArgs(os.Args[1:])
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
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
	case "mcp":
		os.Exit(cmdMCP(args))
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		os.Exit(2)
	}
}

// routeArgs picks the subcommand. No arguments, or arguments that start with
// a flag, run the interactive command, which is also named tui. Any other
// first argument that is not a subcommand is unknown.
func routeArgs(args []string) (subcommand string, rest []string, ok bool) {
	if len(args) == 0 {
		return "tui", nil, true
	}
	switch first := args[0]; {
	case first == "tui" || first == "probe" || first == "conversations" || first == "mcp":
		return first, args[1:], true
	case strings.HasPrefix(first, "-"):
		return "tui", args, true
	}
	return "", nil, false
}
