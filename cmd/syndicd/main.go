package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
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
		if _, err := fmt.Fprintf(stdout, "syndicd version %s\n", version); err != nil {
			return 1
		}
		return 0
	}

	// Set defaults for paths
	if *rosterPath == "" {
		*rosterPath = resolveRosterPath(*rosterPath)
	}
	if *journalPath == "" {
		*journalPath = resolveJournalPath(*journalPath)
	}
	if *secretsDir == "" {
		*secretsDir = resolveSecretsDir(*secretsDir)
	}

	// Get XDG dirs for lockfile resolution
	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	xdgStateHome := os.Getenv("XDG_STATE_HOME")

	// Convert to absolute path for the roster (required for manifest)
	rosterAbs, err := filepath.Abs(*rosterPath)
	if err != nil {
		if _, err := fmt.Fprintf(stderr, "resolve roster path: %v\n", err); err != nil {
			return 1
		}
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
	}, os.Getenv("HOME"))
	if err != nil {
		if _, err := fmt.Fprintf(stderr, "%v\n", err); err != nil {
			return 1
		}
		return 1
	}

	// Acquire the lock
	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		var heldErr *lockfile.HeldError
		if errors.As(err, &heldErr) {
			if _, err := fmt.Fprintf(stderr, "an engine with PID %d is already running\n", heldErr.Holder.PID); err != nil {
				return 1
			}
			return 3
		}
		if _, err := fmt.Fprintf(stderr, "acquire lock: %v\n", err); err != nil {
			return 1
		}
		return 1
	}
	defer func() {
		if err := lock.Release(); err != nil {
			fmt.Fprintf(stderr, "release lock: %v\n", err)
		}
	}()

	// Create journal directory if it doesn't exist
	journalDir := filepath.Dir(*journalPath)
	if err := os.MkdirAll(journalDir, 0700); err != nil {
		if _, err := fmt.Fprintf(stderr, "create journal directory: %v\n", err); err != nil {
			return 1
		}
		return 1
	}

	// Open the journal
	db, err := journal.Open(*journalPath)
	if err != nil {
		if _, err := fmt.Fprintf(stderr, "open journal: %v\n", err); err != nil {
			return 1
		}
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			fmt.Fprintf(stderr, "close journal: %v\n", err)
		}
	}()

	store := journal.NewStore(db)

	// Create the hub
	eventHub := hub.New(store, 1024)

	// NewAdapter creates adapters for the engine
	newAdapterFunc := func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
		if p.Route != "api" {
			return nil, "", fmt.Errorf("participant %s: unsupported route: %s", p.ID, p.Route)
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
		if _, err := fmt.Fprintf(stderr, "create engine: %v\n", err); err != nil {
			return 1
		}
		return 1
	}

	// Start the engine
	if err := eng.Start(); err != nil {
		var rosterErr *engine.RosterError
		if errors.As(err, &rosterErr) {
			if _, err := fmt.Fprintf(stderr, "%v\n", err); err != nil {
				return 1
			}
			return 2
		}
		if _, err := fmt.Fprintf(stderr, "%v\n", err); err != nil {
			return 1
		}
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
		if _, err := fmt.Fprintf(stderr, "server listen: %v\n", err); err != nil {
			return 1
		}
		if err := eng.Stop(context.Background()); err != nil {
			fmt.Fprintf(stderr, "stop engine: %v\n", err)
		}
		return 1
	}

	// Start server in a goroutine
	serveDone := make(chan error, 1)
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	go func() {
		serveDone <- srv.Serve(serveCtx)
	}()

	// Print startup info
	hello := eng.Hello()
	if _, err := fmt.Fprintf(stdout, "%s %s %s\n", paths.Socket, hello.RunID, rosterAbs); err != nil {
		return 1
	}

	// Wait for signals and handle graceful shutdown
	sigCount := 0

	for {
		select {
		case <-signals:
			sigCount++
			if sigCount == 1 {
				// First signal: graceful stop
				// Use a cancellable context (engine enforces stop_grace)
				stopCtx, cancelStop := context.WithCancel(context.Background())
				defer cancelStop()

				// Start engine stop in a goroutine so we can listen for a second signal
				stopDone := make(chan error, 1)
				go func() {
					stopDone <- eng.Stop(stopCtx)
				}()

				// Close the server
				cancelServe()

				// Wait for engine stop to complete, listening for a second signal
				for {
					select {
					case <-signals:
						sigCount++
						// Second signal: cancel the stop context
						cancelStop()
						// Keep waiting for stop to actually finish
					case err := <-stopDone:
						if err != nil {
							fmt.Fprintf(stderr, "stop engine: %v\n", err)
						}
						if sigCount > 1 {
							// We got a second signal, return immediately
							return 130
						}
						// First signal completed normally
						// Wait for server to finish
						if err := <-serveDone; err != nil {
							fmt.Fprintf(stderr, "serve: %v\n", err)
						}
						return 0
					}
				}
			}

		case err := <-serveDone:
			// Server stopped on its own
			if err != nil {
				if _, err := fmt.Fprintf(stderr, "serve: %v\n", err); err != nil {
					return 1
				}
			}
			// Stop the engine gracefully
			stopCtx, cancelStop := context.WithCancel(context.Background())
			defer cancelStop()
			if err := eng.Stop(stopCtx); err != nil {
				fmt.Fprintf(stderr, "stop engine: %v\n", err)
			}
			return 0
		}
	}
}

func resolveRosterPath(custom string) string {
	if custom != "" {
		return custom
	}
	if _, err := os.Stat("./syndic.toml"); err == nil {
		return "./syndic.toml"
	}
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(xdgConfig, "syndic", "syndic.toml")
}

func resolveJournalPath(custom string) string {
	if custom != "" {
		return custom
	}
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(xdgData, "syndic", "journal.sqlite")
}

func resolveSecretsDir(custom string) string {
	if custom != "" {
		return custom
	}
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(xdgConfig, "syndic", "secrets")
}
