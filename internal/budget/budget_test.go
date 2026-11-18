package budget

import (
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

func TestAdmissionTokens(t *testing.T) {
	tests := []struct {
		name             string
		msgs             []transport.Message
		tokensPerByteMax float64
		o                Overheads
		want             int
	}{
		{
			name:             "one message 100 bytes, 1.0 tokens/byte",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 100))}},
			tokensPerByteMax: 1.0,
			o:                Overheads{MessageTokens: 8, RequestTokens: 16},
			want:             124, // ceil(100 * 1.0) + 8 + 16
		},
		{
			name:             "one message 100 bytes, 0.5 tokens/byte",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 100))}},
			tokensPerByteMax: 0.5,
			o:                Overheads{MessageTokens: 8, RequestTokens: 16},
			want:             74, // ceil(100 * 0.5) + 8 + 16
		},
		{
			name:             "one message 100 bytes, 1.5 tokens/byte",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 100))}},
			tokensPerByteMax: 1.5,
			o:                Overheads{MessageTokens: 8, RequestTokens: 16},
			want:             174, // ceil(100 * 1.5) + 8 + 16 = ceil(150) + 24 = 150 + 24
		},
		{
			name: "two messages",
			msgs: []transport.Message{
				{Role: "user", Content: string(make([]byte, 100))},
				{Role: "assistant", Content: string(make([]byte, 50))},
			},
			tokensPerByteMax: 1.0,
			o:                Overheads{MessageTokens: 8, RequestTokens: 16},
			want:             182, // (100 + 8) + (50 + 8) + 16
		},
		{
			name:             "empty message list",
			msgs:             []transport.Message{},
			tokensPerByteMax: 1.0,
			o:                Overheads{MessageTokens: 8, RequestTokens: 16},
			want:             16, // just RequestTokens
		},
		{
			name:             "multibyte UTF-8 characters",
			msgs:             []transport.Message{{Role: "user", Content: "日本語"}}, // 3 characters, 9 bytes
			tokensPerByteMax: 1.0,
			o:                Overheads{MessageTokens: 8, RequestTokens: 16},
			want:             33, // ceil(9 * 1.0) + 8 + 16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdmissionTokens(tt.msgs, tt.tokensPerByteMax, tt.o)
			if got != tt.want {
				t.Errorf("AdmissionTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAdmissionTokensPanic(t *testing.T) {
	tests := []struct {
		name             string
		tokensPerByteMax float64
	}{
		{name: "zero", tokensPerByteMax: 0},
		{name: "negative", tokensPerByteMax: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("AdmissionTokens() did not panic with %v", tt.name)
				}
			}()
			msgs := []transport.Message{{Role: "user", Content: "test"}}
			AdmissionTokens(msgs, tt.tokensPerByteMax, Overheads{})
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name          string
		msgs          []transport.Message
		bytesPerToken float64
		o             Overheads
		want          int
	}{
		{
			name:          "one message 100 bytes, 3.0 bytes/token",
			msgs:          []transport.Message{{Role: "user", Content: string(make([]byte, 100))}},
			bytesPerToken: 3.0,
			o:             Overheads{MessageTokens: 8, RequestTokens: 16},
			want:          58, // ceil(100 / 3.0) + 8 + 16 = ceil(33.33) + 24 = 34 + 24
		},
		{
			name: "two messages",
			msgs: []transport.Message{
				{Role: "user", Content: string(make([]byte, 100))},
				{Role: "assistant", Content: string(make([]byte, 50))},
			},
			bytesPerToken: 3.0,
			o:             Overheads{MessageTokens: 8, RequestTokens: 16},
			want:          83, // ceil(100/3) + 8 + ceil(50/3) + 8 + 16 = 34 + 8 + 17 + 8 + 16
		},
		{
			name:          "empty message list",
			msgs:          []transport.Message{},
			bytesPerToken: 3.0,
			o:             Overheads{MessageTokens: 8, RequestTokens: 16},
			want:          16, // just RequestTokens
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.msgs, tt.bytesPerToken, tt.o)
			if got != tt.want {
				t.Errorf("EstimateTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEstimateTokensPanic(t *testing.T) {
	tests := []struct {
		name          string
		bytesPerToken float64
	}{
		{name: "zero", bytesPerToken: 0},
		{name: "negative", bytesPerToken: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("EstimateTokens() did not panic with %v", tt.name)
				}
			}()
			msgs := []transport.Message{{Role: "user", Content: "test"}}
			EstimateTokens(msgs, tt.bytesPerToken, Overheads{})
		})
	}
}

func TestEffectiveBytesPerToken(t *testing.T) {
	tests := []struct {
		name              string
		calibrated        float64
		calibrationSafety float64
		want              float64
	}{
		{
			name:              "4.0 * 0.75",
			calibrated:        4.0,
			calibrationSafety: 0.75,
			want:              3.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveBytesPerToken(tt.calibrated, tt.calibrationSafety)
			if got != tt.want {
				t.Errorf("EffectiveBytesPerToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsableBound(t *testing.T) {
	tests := []struct {
		name          string
		contextBound  int
		contextMargin float64
		want          int
	}{
		{
			name:          "32768 with 0.10 margin",
			contextBound:  32768,
			contextMargin: 0.10,
			want:          29491, // floor(32768 * 0.9)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UsableBound(tt.contextBound, tt.contextMargin)
			if got != tt.want {
				t.Errorf("UsableBound() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFits(t *testing.T) {
	tests := []struct {
		name            string
		admissionTokens int
		maxOutputTokens int
		usableBound     int
		want            bool
	}{
		{
			name:            "fits: 29000 + 491 = 29491",
			admissionTokens: 29000,
			maxOutputTokens: 491,
			usableBound:     29491,
			want:            true,
		},
		{
			name:            "does not fit: 29000 + 492 = 29492 > 29491",
			admissionTokens: 29000,
			maxOutputTokens: 492,
			usableBound:     29491,
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fits(tt.admissionTokens, tt.maxOutputTokens, tt.usableBound)
			if got != tt.want {
				t.Errorf("Fits() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClampOutput(t *testing.T) {
	tests := []struct {
		name        string
		configured  int
		outputBound *int
		want        int
	}{
		{
			name:        "no bound",
			configured:  4096,
			outputBound: nil,
			want:        4096,
		},
		{
			name:        "bound is smaller",
			configured:  4096,
			outputBound: intPtr(1024),
			want:        1024,
		},
		{
			name:        "bound is larger",
			configured:  4096,
			outputBound: intPtr(8192),
			want:        4096,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClampOutput(tt.configured, tt.outputBound)
			if got != tt.want {
				t.Errorf("ClampOutput() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStandInBytes(t *testing.T) {
	tests := []struct {
		name            string
		maxOutputTokens int
		bytesPerToken   float64
		want            int
	}{
		{
			name:            "4096 tokens at 2.5 bytes/token",
			maxOutputTokens: 4096,
			bytesPerToken:   2.5,
			want:            10240, // ceil(4096 * 2.5)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StandInBytes(tt.maxOutputTokens, tt.bytesPerToken)
			if got != tt.want {
				t.Errorf("StandInBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStandInBytesPanic(t *testing.T) {
	tests := []struct {
		name          string
		bytesPerToken float64
	}{
		{name: "zero", bytesPerToken: 0},
		{name: "negative", bytesPerToken: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("StandInBytes() did not panic with %v", tt.name)
				}
			}()
			StandInBytes(4096, tt.bytesPerToken)
		})
	}
}

func intPtr(v int) *int {
	return &v
}
