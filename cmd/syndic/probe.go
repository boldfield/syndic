package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
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
	skipCalibration := fs.Bool("skip-calibration", false, "do not run calibration")

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

	// Validate roster
	if err := r.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: roster validation failed:\n%v\n", err)
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

	// Check if endpoint has metered billing without external spending control
	if endpointProfile.DeclaredBilling == "metered" && endpointProfile.SpendingControl != "external" {
		fmt.Fprintf(os.Stderr, "error: endpoint %q has metered billing; no spending ceiling is configured (use spending_control = \"external\")\n", *endpointName)
		return 2
	}

	// Check if --raw-dir is inside a git worktree
	if evidence.InsideGitWorktree(raw) {
		fmt.Fprintf(os.Stderr, "error: --raw-dir is inside a git worktree\n")
		return 4
	}

	// Create adapter with EnvFileResolver
	resolver := config.NewEnvFileResolver(secrets)
	adapter := openai.New(*endpointProfile, resolver)

	// Resolve assumed context length: --context-tokens, then participant context_tokens, else 0
	assumedContextLen := *contextTokens
	if assumedContextLen == 0 {
		for _, p := range r.Participants {
			if p.Endpoint == *endpointName && p.Model == *modelName && p.ContextTokens != nil {
				assumedContextLen = *p.ContextTokens
				break
			}
		}
	}

	// Build probe options
	probeOpts := transport.ProbeOptions{
		Timeout:            60 * time.Second,
		DisableOverLength:  *skipOverLength,
		DisableCalibration: *skipCalibration,
		AssumedContextLen:  assumedContextLen,
	}

	// Run probe with per-request timeouts, not an outer deadline
	record, err := adapter.ProbeWithOptions(context.Background(), *endpointName, *modelName, probeOpts)

	// Check for incomplete probe (which still returns a record for diagnosis)
	isIncompleteProbe := errors.Is(err, transport.ErrIncompleteProbe)
	if err != nil && !isIncompleteProbe {
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

	// For incomplete probes, write raw and export for diagnosis but skip the usable manifest
	if isIncompleteProbe {
		fmt.Fprintf(os.Stderr, "note: incomplete probe written for diagnosis only (raw: %s, export: %s)\n", rawPath, exportPath)
		return 1
	}

	// Create ProbeManifest with fingerprint and calibration
	probeManifest, err := evidence.NewProbeManifest(exp, *endpointProfile, *modelName, record.AdapterVersion, record.Calibration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create probe manifest: %v\n", err)
		return 1
	}

	manifestData, err := json.Marshal(probeManifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to marshal manifest: %v\n", err)
		return 1
	}

	// Resolve runtime paths
	paths, err := lockfile.Resolve(os.Getenv, os.Getenv("HOME"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to resolve runtime paths: %v\n", err)
		return 1
	}

	// Acquire engine lock (non-blocking)
	lock, err := lockfile.Acquire(paths.Lock, time.Now())
	if err != nil {
		var heldErr *lockfile.HeldError
		if errors.As(err, &heldErr) {
			fmt.Fprintf(os.Stderr, "error: engine with PID %d holds the journal; stop it before recording the probe\n", heldErr.Holder.PID)
			return 5
		}
		fmt.Fprintf(os.Stderr, "error: failed to acquire engine lock: %v\n", err)
		return 1
	}

	lockReleased := false
	defer func() {
		if !lockReleased {
			if err := lock.Release(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to release engine lock: %v\n", err)
			}
		}
	}()

	// Create journal parent directory
	if err := os.MkdirAll(filepath.Dir(jnl), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to create journal directory: %v\n", err)
		return 1
	}

	// Open journal and create run
	db, err := journal.Open(jnl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to open journal: %v\n", err)
		return 1
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: close journal: %v\n", cerr)
		}
	}()

	store := journal.NewStore(db)

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

	// Release lock after journal operations are complete
	if err := lock.Release(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to release engine lock: %v\n", err)
	}
	lockReleased = true

	// Print summary
	printSummary(*endpointName, *modelName, record, rawPath, exportPath, probeManifest.Calibration)

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

func getOperationalErrorCode(err error) int {
	if errors.Is(err, transport.ErrUnreachable) ||
		errors.Is(err, transport.ErrUnauthorized) ||
		errors.Is(err, transport.ErrModelUnavailable) {
		return 3
	}
	return 1
}

func printSummary(endpoint, model string, record transport.CapabilityRecord, rawPath, exportPath string, cal evidence.CalibrationRecord) {
	fmt.Printf("Probe completed for %s/%s\n", endpoint, model)
	fmt.Printf("  Auth mode:        %s\n", record.AuthMode)
	fmt.Printf("  Identity:         %s\n", record.Identity)
	fmt.Printf("  Reported model:   %s\n", record.ReportedModel)
	if record.ContextBound != nil {
		fmt.Printf("  Max context:      %d tokens\n", *record.ContextBound)
	}
	fmt.Printf("  Max-tokens field: %s\n", record.MaxTokensField)
	fmt.Printf("  Usage available:  %s\n", record.UsageAvailable)
	fmt.Printf("  System role:      %s\n", record.SystemRoleHonored)
	if len(record.ToolRestrictions) > 0 {
		fmt.Printf("  Tool calls:       %s (restrictions: %s)\n", record.ToolCalls, strings.Join(record.ToolRestrictions, ", "))
	} else {
		fmt.Printf("  Tool calls:       %s\n", record.ToolCalls)
	}
	fmt.Printf("  Over-length:      %s\n", record.OverLength)
	if record.OverLengthDetail != "" {
		fmt.Printf("    Reason:         %s\n", record.OverLengthDetail)
	}
	fmt.Printf("  Eligible reviewer: %s\n", record.EligibleRequiredReviewer)
	if record.LatencyMillis != nil {
		fmt.Printf("  Latency:          %d ms\n", *record.LatencyMillis)
	}
	if cal.Available {
		note := ""
		if cal.BaselineBytes > 0 {
			note = " (non-empty baseline)"
		}
		fmt.Printf("  Calibration:      %.2f bytes/token%s\n", cal.BytesPerToken, note)
	} else {
		fmt.Printf("  Calibration:      unavailable\n")
	}
	fmt.Printf("  Raw record:       %s\n", rawPath)
	fmt.Printf("  Export:           %s\n", exportPath)
}
