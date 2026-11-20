package tui

import (
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func TestModelFamily(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"llama31_70b", "llama3.1:70b", "llama"},
		{"llama32_8b", "llama3.2:8b", "llama"},
		{"gpt4o", "gpt-4o", "gpt"},
		{"gpt41", "gpt-4.1", "gpt"},
		{"qwen25", "qwen2.5:32b", "qwen"},
		{"colon separator", "model:1.0", "model"},
		{"dash separator", "model-v2", "model"},
		{"dot separator", "model.v3", "model"},
		{"digit separator", "model123", "model"},
		{"no separator", "llama", "llama"},
		{"uppercase normalized", "LLaMA3.1:70b", "llama"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelFamily(tt.input)
			if got != tt.expected {
				t.Errorf("modelFamily(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestModelFamilyMatch(t *testing.T) {
	tests := []struct {
		name     string
		model1   string
		model2   string
		expected bool
	}{
		{"llama31 and llama32 same family", "llama3.1:70b", "llama3.2:8b", true},
		{"gpt4o and gpt41 same family", "gpt-4o", "gpt-4.1", true},
		{"qwen and llama different family", "qwen2.5:32b", "llama3", false},
		{"empty names different", "", "", false},
		{"one empty different", "model1", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fam1 := modelFamily(tt.model1)
			fam2 := modelFamily(tt.model2)
			got := fam1 == fam2 && fam1 != ""
			if got != tt.expected {
				t.Errorf("modelFamily(%q)=%q == modelFamily(%q)=%q is %v, want %v",
					tt.model1, fam1, tt.model2, fam2, got, tt.expected)
			}
		})
	}
}

func TestRenderParticipantsNoParticipants(t *testing.T) {
	s := NewState()
	output := renderParticipants(s, 80, 24, "")
	if output != "no participants" {
		t.Errorf("renderParticipants with no participants = %q, want 'no participants'", output)
	}
}

func TestRenderParticipantsBasic(t *testing.T) {
	s := NewState()
	s.Participants = map[string]ParticipantInfo{
		"author-1": {
			ID:            "author-1",
			State:         "idle",
			Pool:          "default",
			ReportedModel: "llama3.1:70b",
		},
		"reviewer-1": {
			ID:            "reviewer-1",
			State:         "idle",
			Pool:          "default",
			ReportedModel: "llama3.2:8b",
		},
	}
	s.Pools = map[string]wire.Pool{
		"default": {
			Name:        "default",
			Concurrency: 1,
			Holds:       []wire.PoolHold{},
		},
	}

	output := renderParticipants(s, 80, 24, "")

	// Check that output contains participants
	if output == "" {
		t.Error("renderParticipants returned empty string")
	}
	if !contains(output, "author-1") {
		t.Error("output does not contain author-1")
	}
	if !contains(output, "reviewer-1") {
		t.Error("output does not contain reviewer-1")
	}
	if !contains(output, "default") {
		t.Error("output does not contain pool name 'default'")
	}
}

func TestRenderParticipantsWithTurn(t *testing.T) {
	s := NewState()
	s.Participants = map[string]ParticipantInfo{
		"author-1": {
			ID:            "author-1",
			State:         "idle",
			Pool:          "default",
			ReportedModel: "llama3.1:70b",
		},
		"reviewer-1": {
			ID:            "reviewer-1",
			State:         "idle",
			Pool:          "default",
			ReportedModel: "llama3.2:8b",
		},
	}
	s.Pools = map[string]wire.Pool{
		"default": {
			Name:        "default",
			Concurrency: 1,
			Holds:       []wire.PoolHold{},
		},
	}
	s.CurrentTurn = "turn-1"
	s.Turns = map[string]TurnInfo{
		"turn-1": {
			ID:     "turn-1",
			Author: "author-1",
			State:  "submitted",
		},
	}

	output := renderParticipants(s, 80, 24, "")

	// Check that author line is shown
	if !contains(output, "author: author-1") {
		t.Error("output does not contain author line")
	}
	if !contains(output, "llama3.1:70b") {
		t.Error("output does not contain author's model")
	}
}

func TestRenderParticipantsWithOrphanedHolds(t *testing.T) {
	s := NewState()
	s.Participants = map[string]ParticipantInfo{
		"author-1": {
			ID:            "author-1",
			State:         "idle",
			Pool:          "default",
			ReportedModel: "llama3.1:70b",
		},
	}
	s.Pools = map[string]wire.Pool{
		"default": {
			Name:        "default",
			Concurrency: 1,
			Holds:       []wire.PoolHold{},
		},
	}
	s.OrphanedHolds = []wire.Hold{
		{
			Pool:      "old-pool",
			AttemptID: "attempt-1",
			Group:     []wire.GroupMember{{EndpointName: "ep1", Model: "model1"}},
		},
	}

	output := renderParticipants(s, 80, 24, "")

	if !contains(output, "orphaned holds") {
		t.Error("output does not contain orphaned holds section")
	}
	if !contains(output, "old-pool") {
		t.Error("output does not contain old pool name")
	}
	if !contains(output, "/release") {
		t.Error("output does not contain /release hint")
	}
}

func TestTruncateStringRuneSafe(t *testing.T) {
	tests := []struct {
		input    string
		maxRunes int
		want     string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel"},
		{"", 5, ""},
		{"x", 1, "x"},
		{"🎉hello", 2, "🎉h"},
	}

	for _, tt := range tests {
		got := truncateStringRuneSafe(tt.input, tt.maxRunes)
		if got != tt.want {
			t.Errorf("truncateStringRuneSafe(%q, %d) = %q, want %q", tt.input, tt.maxRunes, got, tt.want)
		}
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		input  string
		length int
		want   string
	}{
		{"hello", 10, "hello     "},
		{"hello", 5, "hello"},
		{"hello", 3, "hello"},
		{"", 3, "   "},
	}

	for _, tt := range tests {
		got := padRight(tt.input, tt.length)
		if got != tt.want {
			t.Errorf("padRight(%q, %d) = %q, want %q", tt.input, tt.length, got, tt.want)
		}
	}
}

func TestFormatAgeAt(t *testing.T) {
	tests := []struct {
		name     string
		offset   time.Duration
		expected string
	}{
		{"30 seconds ago", -30 * time.Second, "30s"},
		{"5 minutes ago", -5 * time.Minute, "5m"},
		{"2 hours ago", -2 * time.Hour, "2h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			timestamp := now.Add(tt.offset).Format(time.RFC3339)
			got := formatAgeAt(timestamp, func() time.Time { return now })
			if got != tt.expected {
				t.Errorf("formatAgeAt(%q) = %q, want %q", timestamp, got, tt.expected)
			}
		})
	}
}

func TestFormatAgeAtInvalid(t *testing.T) {
	got := formatAgeAt("invalid-timestamp", time.Now)
	if got != "unknown" {
		t.Errorf("formatAgeAt(invalid) = %q, want 'unknown'", got)
	}
}
