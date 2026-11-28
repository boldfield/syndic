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
