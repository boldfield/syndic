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
	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/openai"
)

const version = "dev"

func main() {
	if isSupervise(os.Args[1:]) {
		os.Exit(supervise(os.Stderr))
	}
	if isSelfTestHelper(os.Args[1:]) {
		os.Exit(sandbox.SelfTestHelper(os.Args[2:], os.Stdout))
	}
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
	probeMissing := fs.Bool("probe-missing", false, "at start, also probe participants on endpoints declared metered or unknown that have no usable record")
	probeRawDir := fs.String("probe-raw-dir", "", "directory for raw records of start-time probes (default: $XDG_DATA_HOME/syndic/evidence-raw)")
	probeExportDir := fs.String("probe-export-dir", "", "directory for redacted exports of start-time probes (default: ./docs/evidence)")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "syndicd version %s\n", version)
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
	*probeRawDir = resolveProbeRawDir(*probeRawDir)
	*probeExportDir = resolveProbeExportDir(*probeExportDir)

	// Get XDG dirs for lockfile resolution
	xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR")
	xdgStateHome := os.Getenv("XDG_STATE_HOME")

	// Convert to absolute path for the roster (required for manifest)
	rosterAbs, err := filepath.Abs(*rosterPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "resolve roster path: %v\n", err)
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
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// Acquire the lock
	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		var heldErr *lockfile.HeldError
		if errors.As(err, &heldErr) {
			_, _ = fmt.Fprintf(stderr, "an engine with PID %d is already running\n", heldErr.Holder.PID)
			return 3
		}
		_, _ = fmt.Fprintf(stderr, "acquire lock: %v\n", err)
		return 1
	}
	defer func() {
		_ = lock.Release()
	}()

	// Create journal directory if it doesn't exist
	journalDir := filepath.Dir(*journalPath)
	if err := os.MkdirAll(journalDir, 0700); err != nil {
		_, _ = fmt.Fprintf(stderr, "create journal directory: %v\n", err)
		return 1
	}

	// Open the journal
	db, err := journal.Open(*journalPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open journal: %v\n", err)
		return 1
	}
	defer func() {
		_ = db.Close()
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

	// The control socket opens as soon as run_opened is committed, before
	// any probe, so a client attaching during probing gets hello state
	// "probing" and the probe events. The engine is created first and the
	// server is wired to it from the hook.
	var srv *server.Server
	serveDone := make(chan error, 1)
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()

	// SearchProviders is left unset: no web_search vendor has been chosen, so
	// this build supports no provider and web_search is not offered whatever
	// [tools.web.search] configures. The operator-selected integration is
	// injected here, as a tools.SearchProvider, when one is chosen.
	execOpts, err := execOptions(paths.Dir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "exec options: %v\n", err)
		return 1
	}
	eng, err := engine.New(engine.Options{
		Exec:          execOpts,
		Store:         store,
		Hub:           eventHub,
		RosterPath:    rosterAbs,
		EngineVersion: version,
		NewAdapter:    newAdapterFunc,

		ProbeMissing:   *probeMissing,
		ProbeRawDir:    *probeRawDir,
		ProbeExportDir: *probeExportDir,
		JournalPath:    absPath(*journalPath),
		SecretsDir:     absPath(*secretsDir),
		OnRunOpened: func() error {
			if err := srv.Listen(); err != nil {
				return fmt.Errorf("server listen: %w", err)
			}
			go func() {
				serveDone <- srv.Serve(serveCtx)
			}()
			return nil
		},
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "create engine: %v\n", err)
		return 1
	}
	srv = server.New(server.Options{
		Paths:  paths,
		Engine: eng,
		Store:  store,
		Hub:    eventHub,
	})

	// Start the engine
	if err := eng.Start(); err != nil {
		_ = srv.Close()
		var rosterErr *engine.RosterError
		if errors.As(err, &rosterErr) {
			_, _ = fmt.Fprintf(stderr, "%v\n", err)
			return 2
		}
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// Log external spending control settings at start
	logExternalSpendingControl(stderr, eng)

	// Print startup info
	hello := eng.Hello()
	_, _ = fmt.Fprintf(stdout, "%s %s %s\n", paths.Socket, hello.RunID, rosterAbs)

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

				// Wait for engine stop to complete, listening for a second signal
				for {
					select {
					case <-signals:
						sigCount++
						// Second signal: cancel the stop context
						cancelStop()
					case err := <-stopDone:
						if err != nil {
							_, _ = fmt.Fprintf(stderr, "stop engine: %v\n", err)
						}
						// A second signal that arrived while Stop was finishing
						// still counts.
						select {
						case <-signals:
							sigCount++
						default:
						}
						if sigCount > 1 {
							// We got a second signal, skip waits and return 130
							_ = srv.Close()
							waitServe(serveDone)
							return 130
						}
						// First signal completed normally. Serve sees the
						// engine stopping, tells the clients and returns on
						// its own; closing the server first would cut the
						// notice short.
						select {
						case err := <-serveDone:
							if err != nil {
								_, _ = fmt.Fprintf(stderr, "serve: %v\n", err)
							}
						case <-time.After(5 * time.Second):
							cancelServe()
							_ = srv.Close()
							waitServe(serveDone)
						}
						_ = srv.Close()
						return 0
					}
				}
			}

		case serveErr := <-serveDone:
			// Server stopped on its own (or was cancelled)
			// Stop the engine gracefully
			stopCtx, cancelStop := context.WithCancel(context.Background())
			defer cancelStop()

			// Start engine stop in a goroutine so we can listen for signals
			stopDone := make(chan error, 1)
			go func() {
				stopDone <- eng.Stop(stopCtx)
			}()

			// Listen for a signal while engine is stopping
			for {
				select {
				case <-signals:
					sigCount++
					// Signal during Serve error stop
					cancelStop()
				case err := <-stopDone:
					if err != nil {
						_, _ = fmt.Fprintf(stderr, "stop engine: %v\n", err)
					}
					if sigCount > 0 {
						// serveDone was already consumed; do not wait on it.
						_ = srv.Close()
						return 130
					}
					// Serve errored, return 1 if Serve had an error, otherwise 0
					_ = srv.Close()
					if serveErr != nil {
						return 1
					}
					return 0
				}
			}
		}
	}
}

// logExternalSpendingControl logs all endpoints with external spending control
// acceptance, once at start.
func logExternalSpendingControl(w io.Writer, eng *engine.Engine) {
	status, err := eng.Status()
	if err != nil {
		return
	}

	logged := make(map[string]bool)
	for _, p := range status.Participants {
		if p.DeclaredBilling == "metered" && p.SpendingControl == "external" && !logged[p.Endpoint] {
			_, _ = fmt.Fprintf(w, "external spending control: %s (metered)\n", p.Endpoint)
			logged[p.Endpoint] = true
		}
	}
}

// waitServe waits a bounded time for Serve to return after the server is
// closed, so the journal is not closed under it.
func waitServe(serveDone <-chan error) {
	select {
	case err := <-serveDone:
		_ = err
	case <-time.After(3 * time.Second):
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

// resolveProbeRawDir and resolveProbeExportDir default the start-time probe
// evidence directories to those of `syndic probe`.
func resolveProbeRawDir(custom string) string {
	if custom != "" {
		return custom
	}
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(xdgData, "syndic", "evidence-raw")
}

func resolveProbeExportDir(custom string) string {
	if custom != "" {
		return custom
	}
	return "./docs/evidence"
}

// absPath resolves p against the working directory, or returns it as given
// when that fails, so a printed retry command works from anywhere.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
