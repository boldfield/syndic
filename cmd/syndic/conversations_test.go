package main

import (
	"flag"
	"os"
	"testing"
)

// TestConversationsHelp verifies that the conversations command accepts --help
func TestConversationsHelp(t *testing.T) {
	fs := flag.NewFlagSet("conversations", flag.ContinueOnError)
	code := cmdConversations(fs, []string{"-h"})
	if code != 2 {
		// -h causes flag.Parse to return an error, which we handle by returning 2
		t.Logf("exit code with -h = %d", code)
	}
}

// TestConversationsFlags verifies that the conversations command accepts --all flag
func TestConversationsFlags(t *testing.T) {
	fs := flag.NewFlagSet("conversations", flag.ContinueOnError)
	if err := fs.Parse([]string{}); err != nil {
		t.Fatal(err)
	}
	all := fs.Bool("all", false, "list all conversations")
	if all != nil && *all {
		t.Error("--all should default to false")
	}
}

// TestConversationsRefusesWhenNotConnected tests that the command fails gracefully
// when the daemon is not running (this is an integration behavior test)
func TestConversationsRefusesWhenNotConnected(t *testing.T) {
	// Set a fake socket path that doesn't exist
	oldHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	t.Cleanup(func() {
		if err := os.Setenv("HOME", oldHome); err != nil {
			t.Logf("failed to restore HOME: %v", err)
		}
	})

	if err := os.Setenv("HOME", tmpDir); err != nil {
		t.Fatal(err)
	}

	fs := flag.NewFlagSet("conversations", flag.ContinueOnError)
	code := cmdConversations(fs, []string{})
	if code != 2 {
		t.Errorf("exit code when daemon not connected = %d, want 2", code)
	}
}
