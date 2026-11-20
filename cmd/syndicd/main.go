package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/openai"
)

const version = "dev"

func main() {
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	exitCode := run(os.Args[1:], os.Stdout, os.Stderr, sigChan)
	os.Exit(exitCode)
}

func run(args []string, stdout, stderr io.Writer, signals <-chan os.Signal) int {
	fs := flag.NewFlagSet("syndicd", flag.ContinueOnError)
	fs.SetOutput(stderr)

	rosterPath := fs.String("roster", "", "path to roster TOML file")
	journalPath := fs.String("journal", "", "path to journal database")
	secretsDir := fs.String("secrets-dir", "", "directory for file secret references")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintf(stdout, "syndicd version %s\n", version)
		return 0
	}

	// Resolve runtime paths
	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	xdgStateHome := os.Getenv("XDG_STATE_HOME")
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	xdgDataHome := os.Getenv("XDG_DATA_HOME")

	homeDir := ""
	if u, err := user.Current(); err == nil {
		homeDir = u.HomeDir
	}

	// Set defaults for paths
	if *rosterPath == "" {
		*rosterPath = resolveRosterPath("", xdgConfigHome, homeDir)
	}
	if *journalPath == "" {
		*journalPath = resolveJournalPath("", xdgDataHome, homeDir)
	}
	if *secretsDir == "" {
		*secretsDir = resolveSecretsDir("", xdgConfigHome, homeDir)
	}

	// Convert to absolute path for the roster (required for manifest)
	rosterAbs, err := filepath.Abs(*rosterPath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve roster path: %v\n", err)
		return 1
	}

	// Resolve runtime paths with lockfile
	paths, err := lockfile.Resolve(func(key string) string {
		switch key {
		case "XDG_RUNTIME_DIR":
			return xdgRuntimeDir
		case "XDG_STATE_HOME":
			return xdgStateHome
		default:
			return ""
		}
	}, homeDir)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// Acquire the lock
	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		var heldErr *lockfile.HeldError
		if errors.As(err, &heldErr) {
			fmt.Fprintf(stderr, "an engine with PID %d is already running\n", heldErr.Holder.PID)
			return 3
		}
		fmt.Fprintf(stderr, "acquire lock: %v\n", err)
		return 1
	}
	defer lock.Release()

	// Create journal directory if it doesn't exist
	journalDir := filepath.Dir(*journalPath)
	if err := os.MkdirAll(journalDir, 0700); err != nil {
		fmt.Fprintf(stderr, "create journal directory: %v\n", err)
		return 1
	}

	// Open the journal
	db, err := journal.Open(*journalPath)
	if err != nil {
		fmt.Fprintf(stderr, "open journal: %v\n", err)
		return 1
	}
	defer db.Close()

	store := journal.NewStore(db)

	// Load and validate the roster
	roster, err := config.Load(rosterAbs)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	if err := roster.Validate(); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}

	// Create the hub
	eventHub := hub.New(store, 1024)

	// NewAdapter creates adapters for the engine
	newAdapterFunc := func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
		if p.Route != "api" {
			return nil, "", fmt.Errorf("unsupported route: %s", p.Route)
		}
		resolver := config.NewEnvFileResolver(*secretsDir)
		return openai.New(ep, resolver), openai.AdapterVersion, nil
	}

	// Create the engine
	eng, err := engine.New(engine.Options{
		Store:         store,
		Hub:           eventHub,
		RosterPath:    rosterAbs,
		EngineVersion: version,
		NewAdapter:    newAdapterFunc,
	})
	if err != nil {
		fmt.Fprintf(stderr, "create engine: %v\n", err)
		return 1
	}

	// Start the engine
	if err := eng.Start(); err != nil {
		var rosterErr *engine.RosterError
		if errors.As(err, &rosterErr) {
			fmt.Fprintf(stderr, "%v\n", err)
			return 2
		}
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// Create and start the server
	srv := server.New(server.Options{
		Paths:  paths,
		Engine: eng,
		Store:  store,
		Hub:    eventHub,
	})

	if err := srv.Listen(); err != nil {
		fmt.Fprintf(stderr, "server listen: %v\n", err)
		eng.Stop(context.Background())
		return 1
	}

	// Start server in a goroutine
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(context.Background())
	}()

	// Print startup info
	hello := eng.Hello()
	fmt.Fprintf(stdout, "%s %s %s\n", paths.Socket, hello.RunID, rosterAbs)

	// Wait for signals and handle graceful shutdown
	for {
		select {
		case sig := <-signals:
			if sig == syscall.SIGINT || sig == syscall.SIGTERM {
				// First signal: graceful stop
				ctx := context.Background()
				if err := eng.Stop(ctx); err != nil {
					fmt.Fprintf(stderr, "stop engine: %v\n", err)
					return 1
				}
				return 0
			}

		case err := <-serveDone:
			if err != nil {
				fmt.Fprintf(stderr, "serve: %v\n", err)
				eng.Stop(context.Background())
				return 1
			}
			return 0
		}
	}
}

func resolveRosterPath(custom, xdgConfigHome, homeDir string) string {
	if custom != "" {
		return custom
	}

	if xdgConfigHome != "" {
		return filepath.Join(xdgConfigHome, "syndic", "syndic.toml")
	}

	if homeDir != "" {
		return filepath.Join(homeDir, ".config", "syndic", "syndic.toml")
	}

	return "./syndic.toml"
}

func resolveJournalPath(custom, xdgDataHome, homeDir string) string {
	if custom != "" {
		return custom
	}

	if xdgDataHome != "" {
		return filepath.Join(xdgDataHome, "syndic", "journal.sqlite")
	}

	if homeDir != "" {
		return filepath.Join(homeDir, ".local", "share", "syndic", "journal.sqlite")
	}

	return "./journal.sqlite"
}

func resolveSecretsDir(custom, xdgConfigHome, homeDir string) string {
	if custom != "" {
		return custom
	}

	if xdgConfigHome != "" {
		return filepath.Join(xdgConfigHome, "syndic", "secrets")
	}

	if homeDir != "" {
		return filepath.Join(homeDir, ".config", "syndic", "secrets")
	}

	return "./secrets"
}
