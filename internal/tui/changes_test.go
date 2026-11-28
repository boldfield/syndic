package tui

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func TestRenderChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		cs   *wire.ChangeSetRecord
		want string
	}{
		{
			name: "nil change set",
			cs:   nil,
			want: "no change set available",
		},
		{
			name: "empty change set",
			cs: &wire.ChangeSetRecord{
				State:        "appliable",
				ChangesHash:  "abc123def456",
				SnapshotHash: "def789ghi012",
				Paths:        []wire.ChangeSetPathRecord{},
			},
			want: "Change Set State: appliable",
		},
		{
			name: "change set with paths",
			cs: &wire.ChangeSetRecord{
				State:        "applied",
				ChangesHash:  "abc123def456",
				SnapshotHash: "def789ghi012",
				Paths: []wire.ChangeSetPathRecord{
					{
						Path:       "file.txt",
						Op:         "modify",
						BaseSHA256: "oldsha256hash",
						NewSHA256:  "newsha256hash",
						NewLen:     1024,
						Outcome:    "applied",
					},
					{
						Path:      "new.go",
						Op:        "create",
						NewSHA256: "newsha256hash",
						NewLen:    2048,
						Outcome:   "applied",
					},
				},
			},
			want: "applied",
		},
		{
			name: "partial apply state",
			cs: &wire.ChangeSetRecord{
				State:       "partial",
				ChangesHash: "abc123def456",
				Paths: []wire.ChangeSetPathRecord{
					{
						Path:       "applied_file.txt",
						Op:         "modify",
						BaseSHA256: "oldsha",
						NewSHA256:  "newsha",
						NewLen:     100,
						Outcome:    "applied",
					},
					{
						Path:       "pending_file.txt",
						Op:         "modify",
						BaseSHA256: "oldsha2",
						NewSHA256:  "newsha2",
						NewLen:     200,
						Outcome:    "pending",
					},
					{
						Path:       "conflict_file.txt",
						Op:         "modify",
						BaseSHA256: "oldsha3",
						NewSHA256:  "newsha3",
						NewLen:     300,
						Outcome:    "conflict",
					},
				},
			},
			want: "partial",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderChanges(tc.cs, 80, 24)
			if tc.cs == nil {
				if got != tc.want {
					t.Errorf("renderChanges(nil) = %q, want %q", got, tc.want)
				}
			} else if !strings.Contains(got, tc.want) {
				t.Errorf("renderChanges result doesn't contain %q.\nGot:\n%s", tc.want, got)
			}
		})
	}
}

// TestFormatOutcome tests that path outcomes are properly formatted for display.
func TestFormatOutcome(t *testing.T) {
	tests := []struct {
		outcome string
		want    string
	}{
		{"applied", "✓ applied"},
		{"pending", "pending"},
		{"conflict", "✗ conflict"},
		{"not applied", "not applied"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			if got := formatOutcome(tt.outcome); got != tt.want {
				t.Errorf("formatOutcome(%q) = %q, want %q", tt.outcome, got, tt.want)
			}
		})
	}
}
