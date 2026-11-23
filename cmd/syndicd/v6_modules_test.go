package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyndicdNotDependsOnTUIModules(t *testing.T) {
	// This test ensures that cmd/syndicd does not depend on the v6 TUI modules.
	// The TUI modules are for presentation only and must not be used by the engine.

	forbiddenModules := []string{
		"github.com/charmbracelet/bubbles",
		"github.com/charmbracelet/glamour",
		"github.com/charmbracelet/huh",
		"github.com/charmbracelet/harmonica",
		"github.com/charmbracelet/lipgloss",
		"github.com/lrstanley/bubblezone",
	}

	// Find the module root by looking for go.mod
	modRoot := findModuleRoot()
	if modRoot == "" {
		t.Fatal("could not find module root (go.mod)")
	}

	// Check if syndicd imports any of the forbidden modules
	cmd := exec.Command("go", "list", "-deps", "github.com/boldfield/syndic/cmd/syndicd")
	cmd.Dir = modRoot
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps failed: %v\nstderr: %s", err, errOut.String())
	}

	deps := out.String()
	for _, mod := range forbiddenModules {
		for _, line := range strings.Split(deps, "\n") {
			if line == mod {
				t.Errorf("cmd/syndicd should not depend on %s", mod)
			}
		}
	}
}

func findModuleRoot() string {
	// Start from the current working directory and walk up to find go.mod
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
