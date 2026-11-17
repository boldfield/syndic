package transport

import (
	"testing"
)

func TestScrub(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		secrets     []string
		wantScrubbed string
		wantCount   int
	}{
		{
			name:        "no secrets",
			output:      "hello world",
			secrets:     []string{},
			wantScrubbed: "hello world",
			wantCount:   0,
		},
		{
			name:        "one secret once",
			output:      "my token is secret123",
			secrets:     []string{"secret123"},
			wantScrubbed: "my token is " + ScrubMarker,
			wantCount:   1,
		},
		{
			name:        "one secret three times",
			output:      "token token token",
			secrets:     []string{"token"},
			wantScrubbed: ScrubMarker + " " + ScrubMarker + " " + ScrubMarker,
			wantCount:   3,
		},
		{
			name:         "secret prefix of another",
			output:       "My secret is mysecretkey123",
			secrets:      []string{"secret", "mysecretkey123"},
			wantScrubbed: "My " + ScrubMarker + " is " + ScrubMarker,
			wantCount:   2,
		},
		{
			name:        "secret absent",
			output:      "hello world",
			secrets:     []string{"nothere"},
			wantScrubbed: "hello world",
			wantCount:   0,
		},
		{
			name:        "empty secret ignored",
			output:      "hello world",
			secrets:     []string{"", "hello"},
			wantScrubbed: ScrubMarker + " world",
			wantCount:   1,
		},
		{
			name:        "multiple secrets different occurrences",
			output:      "api_key_123 and password_456 api_key_123",
			secrets:     []string{"api_key_123", "password_456"},
			wantScrubbed: ScrubMarker + " and " + ScrubMarker + " " + ScrubMarker,
			wantCount:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scrubbed, count := Scrub(tt.output, tt.secrets)
			if scrubbed != tt.wantScrubbed {
				t.Errorf("Scrub() scrubbed = %q, want %q", scrubbed, tt.wantScrubbed)
			}
			if count != tt.wantCount {
				t.Errorf("Scrub() count = %d, want %d", count, tt.wantCount)
			}
		})
	}
}
