package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/mcp/auth"
	"github.com/boldfield/syndic/internal/secretstore"
)

// loginTimeout bounds the wait for the operator to approve in a browser.
const loginTimeout = 10 * time.Minute

// cmdMCP runs `syndic mcp <verb>`.
func cmdMCP(args []string) int {
	if len(args) == 0 || args[0] != "login" {
		fmt.Fprintln(os.Stderr, "usage: syndic mcp login [options] <server>")
		return 2
	}
	fs := flag.NewFlagSet("mcp login", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: syndic mcp login [options] <server>\n")
		fs.PrintDefaults()
	}
	return cmdMCPLogin(fs, args[1:])
}

// cmdMCPLogin is `syndic mcp login <server>`: the authorization-code flow for
// one remote server, run in the operator's terminal.
func cmdMCPLogin(fs *flag.FlagSet, args []string) int {
	rosterPath := fs.String("roster", "", "path to roster TOML file (default: ./syndic.toml or $XDG_CONFIG_HOME/syndic/syndic.toml)")
	secretsDir := fs.String("secrets-dir", "", "directory for the secret store (default: $XDG_CONFIG_HOME/syndic/secrets)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	name := fs.Arg(0)

	r, err := config.Load(resolveRosterPath(*rosterPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load roster: %v\n", err)
		return 2
	}
	if err := r.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: roster validation failed:\n%v\n", err)
		return 2
	}
	var srv config.MCPServer
	ok := false
	if r.MCP != nil {
		srv, ok = r.MCP.Servers[name]
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "error: no mcp server %q in the roster\n", name)
		return 2
	}
	if srv.Transport != "http" {
		fmt.Fprintf(os.Stderr, "error: mcp server %q is not a remote server; there is nothing to log in to\n", name)
		return 2
	}

	dir := resolveSecretsDir(*secretsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "error: secret store: %v\n", err)
		return 1
	}
	store, err := secretstore.Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: secret store: %v\n", err)
		return 1
	}
	d, err := auth.NewDiscoverer(auth.Config{URL: srv.URL, AddressAllow: srv.AddressAllow, AuthOrigins: srv.AuthOrigins})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	_, err = d.Login(ctx, auth.LoginOptions{
		ClientID: srv.ClientID,
		Scopes:   srv.Scopes,
		Store:    store,
		Announce: func(u string) {
			fmt.Printf("Open this URL in a browser to log in to %s:\n\n%s\n\nWaiting for the response on the loopback address...\n", name, u)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Logged in to %s. The login has a new generation.\n", name)
	return 0
}
