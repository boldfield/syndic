package main

import "testing"

func TestCmdMCPUsage(t *testing.T) {
	if got := cmdMCP(nil); got != 2 {
		t.Errorf("cmdMCP(nil) = %d, want 2", got)
	}
	if got := cmdMCP([]string{"logout"}); got != 2 {
		t.Errorf("cmdMCP(logout) = %d, want 2", got)
	}
	if got := cmdMCP([]string{"login"}); got != 2 {
		t.Errorf("cmdMCP(login) without a server = %d, want 2", got)
	}
}
