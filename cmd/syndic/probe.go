package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/openai"
)

func cmdProbe(fs *flag.FlagSet, args []string) int {
	rosterPath := fs.String("roster", "", "path to roster TOML file (default: ./syndic.toml or $XDG_CONFIG_HOME/syndic/syndic.toml)")
	endpointName := fs.String("endpoint", "", "endpoint profile name")
	modelName := fs.String("model", "", "model name")
	rawDir := fs.String("raw-dir", "", "directory for raw records (default: $XDG_DATA_HOME/syndic/evidence-raw)")
	exportDir := fs.String("export-dir", "", "directory for redacted exports (default: ./docs/evidence)")
	journalPath := fs.String("journal", "", "path to journal database (default: $XDG_DATA_HOME/syndic/journal.sqlite)")
	secretsDir := fs.String("secrets-dir", "", "directory for file secret references (default: $XDG_CONFIG_HOME/syndic/secrets)")
	contextTokens := fs.Int("context-tokens", 0, "assumed context length for over-length test")
	skipOverLength := fs.Bool("skip-overlength", false, "do not run the over-length test")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Validate required flags
	if *endpointName == "" || *modelName == "" {
		fmt.Fprintln(os.Stderr, "error: --endpoint and --model are required")
		return 2
	}

	// Set defaults for paths
	roster := resolveRosterPath(*rosterPath)
	raw := resolveRawDir(*rawDir)
	export := resolveExportDir(*exportDir)
	jnl := resolveJournalPath(*journalPath)
	secrets := resolveSecretsDir(*secretsDir)

	// Load and validate roster
	r, err := config.Load(roster)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load roster: %v\n", err)
		return 2
	}

	// Find the endpoint profile
	var endpointProfile *config.EndpointProfile
	for i := range r.Endpoints {
		if r.Endpoints[i].Name == *endpointName {
			endpointProfile = &r.Endpoints[i]
			break
		}
	}
	if endpointProfile == nil {
		fmt.Fprintf(os.Stderr, "error: endpoint %q not found in roster\n", *endpointName)
		return 2
	}

	// Check if --raw-dir is inside a git worktree
	if isInsideGitWorktree(raw) {
		return 4
	}

	// Create adapter with EnvFileResolver
	resolver := config.NewEnvFileResolver(secrets)
	adapter := openai.New(*endpointProfile, resolver)

	// Build probe options
	probeOpts := openai.ProbeOptions{
		Timeout:           60 * time.Second,
		DisableOverLength: *skipOverLength,
		AssumedContextLen: 131072,
	}
	if *contextTokens > 0 {
		probeOpts.AssumedContextLen = *contextTokens
	}

	// Run probe
	ctx, cancel := context.WithTimeout(context.Background(), probeOpts.Timeout)
	defer cancel()

	record, err := adapter.ProbeWithOptions(ctx, *endpointName, *modelName, probeOpts)
	if err != nil {
		exitCode := getOperationalErrorCode(err)
		if exitCode == 3 {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return 3
		}
		fmt.Fprintf(os.Stderr, "error: probe failed: %v\n", err)
		return 1
	}

	// Write raw record
	rawPath, err := evidence.WriteRaw(record, raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to write raw record: %v\n", err)
		return 1
	}

	// Redact and write export
	exp, err := evidence.Redact(record, *endpointProfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to redact export: %v\n", err)
		return 1
	}

	exportPath, err := evidence.WriteExport(exp, export)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to write export: %v\n", err)
		return 1
	}

	// Open journal and create run
	db, err := journal.Open(jnl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to open journal: %v\n", err)
		return 1
	}
	defer db.Close()

	store := journal.NewStore(db)

	// Create manifest from export (not raw record)
	manifestData, err := json.Marshal(exp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to marshal manifest: %v\n", err)
		return 1
	}

	runID, _, err := store.CreateRun(manifestData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create run: %v\n", err)
		return 1
	}

	// Append probe_completed event
	eventPayload, err := json.Marshal(map[string]string{"run_id": runID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to marshal event: %v\n", err)
		return 1
	}

	if err := store.AppendEvent("probe_completed", eventPayload); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to append event: %v\n", err)
		return 1
	}

	// Print summary
	printSummary(*endpointName, *modelName, record, rawPath, exportPath)

	return 0
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

func resolveRawDir(custom string) string {
	if custom != "" {
		return custom
	}
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return filepath.Join(xdgData, "syndic", "evidence-raw")
}

func resolveExportDir(custom string) string {
	if custom != "" {
		return custom
	}
	return "./docs/evidence"
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

func isInsideGitWorktree(path string) bool {
	// Walk up from path to find the nearest existing ancestor
	current := path
	for {
		// Check if .git exists in this directory
		gitPath := filepath.Join(current, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			// Found a git directory
			return true
		}

		// Try parent directory
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root
			break
		}
		current = parent
	}
	return false
}

func getOperationalErrorCode(err error) int {
	// Check if it's one of the operational errors
	if strings.Contains(err.Error(), transport.ErrUnreachable.Error()) ||
		strings.Contains(err.Error(), transport.ErrUnauthorized.Error()) ||
		strings.Contains(err.Error(), transport.ErrModelUnavailable.Error()) {
		return 3
	}
	return 1
}

func printSummary(endpoint, model string, record transport.CapabilityRecord, rawPath, exportPath string) {
	fmt.Printf("Probe completed for %s/%s\n", endpoint, model)
	fmt.Printf("  Auth mode:        %s\n", record.AuthMode)
	fmt.Printf("  Identity:         %s\n", record.Identity)
	fmt.Printf("  Reported model:   %s\n", record.ReportedModel)
	if record.ContextBound != nil {
		fmt.Printf("  Max context:      %d tokens\n", *record.ContextBound)
	}
	fmt.Printf("  Usage available:  %s\n", record.UsageAvailable)
	fmt.Printf("  System role:      %s\n", record.SystemRoleHonored)
	fmt.Printf("  Over-length:      %s\n", record.OverLength)
	fmt.Printf("  Eligible reviewer: %s\n", record.EligibleRequiredReviewer)
	if record.LatencyMillis != nil {
		fmt.Printf("  Latency:          %d ms\n", *record.LatencyMillis)
	}
	fmt.Printf("  Raw record:       %s\n", rawPath)
	fmt.Printf("  Export:           %s\n", exportPath)
}
