package budget

import (
	"math"
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
		{
			name:             "exact integer: 3 bytes at 1.0 tokens/byte",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 3))}},
			tokensPerByteMax: 1.0,
			o:                Overheads{MessageTokens: 0, RequestTokens: 0},
			want:             3, // 3 * 1.0 = 3.0, should stay 3 not round up
		},
		{
			name:             "rounding safety: 100 bytes at 1.1 tokens/byte",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 100))}},
			tokensPerByteMax: 1.1,
			o:                Overheads{MessageTokens: 0, RequestTokens: 0},
			want:             110, // 100 * 1.1 = 110.00000000000001, should be 110 not 111
		},
		{
			name: "two messages with custom overheads",
			msgs: []transport.Message{
				{Role: "user", Content: string(make([]byte, 50))},
				{Role: "assistant", Content: string(make([]byte, 75))},
			},
			tokensPerByteMax: 1.0,
			o:                Overheads{MessageTokens: 10, RequestTokens: 20},
			want:             165, // (50 + 10) + (75 + 10) + 20 = 60 + 85 + 20 = 165
		},
		{
			name:             "near-integer fractional value: 1 byte at 1.0000000005",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 1))}},
			tokensPerByteMax: 1.0000000005,
			o:                Overheads{MessageTokens: 0, RequestTokens: 0},
			want:             2, // ceil(1.0000000005) = 2, not snapped to 1
		},
		{
			name:             "large magnitude off-by-one: 10MB at 1.1 bytes",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 10000000))}},
			tokensPerByteMax: 1.1,
			o:                Overheads{MessageTokens: 0, RequestTokens: 0},
			want:             11000000, // 10M * 1.1 = 11M exactly
		},
		{
			name:             "large magnitude genuinely fractional: 10M at 1.0000000000005",
			msgs:             []transport.Message{{Role: "user", Content: string(make([]byte, 10000000))}},
			tokensPerByteMax: 1.0000000000005,
			o:                Overheads{MessageTokens: 0, RequestTokens: 0},
			want:             10000001, // ceil(10M * 1.0000000000005) = ceil(10M + 5e-6) = 10,000,001, not snapped to 10M
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdmissionTokens(tt.msgs, tt.tokensPerByteMax, tt.o)
			if got != tt.want {
				t.Errorf("AdmissionTokens() = %d, want %d", got, tt.want)
			}
			if got := RequestAdmissionTokens(tt.msgs, nil, tt.tokensPerByteMax, tt.o); got != tt.want {
				t.Errorf("RequestAdmissionTokens(no tools) = %d, want %d", got, tt.want)
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
		{name: "NaN", tokensPerByteMax: math.NaN()},
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
		{
			name:          "exact integer: 4096 bytes at 4.0 bytes/token",
			msgs:          []transport.Message{{Role: "user", Content: string(make([]byte, 4096))}},
			bytesPerToken: 4.0,
			o:             Overheads{MessageTokens: 0, RequestTokens: 0},
			want:          1024, // 4096 / 4.0 = 1024.0, should stay 1024 not round up
		},
		{
			name:          "multibyte UTF-8 characters",
			msgs:          []transport.Message{{Role: "user", Content: "日本語"}}, // 3 characters, 9 bytes
			bytesPerToken: 3.0,
			o:             Overheads{MessageTokens: 8, RequestTokens: 16},
			want:          27, // ceil(9 / 3.0) + 8 + 16 = 3 + 8 + 16 = 27
		},
		{
			name: "two messages with custom overheads",
			msgs: []transport.Message{
				{Role: "user", Content: string(make([]byte, 60))},
				{Role: "assistant", Content: string(make([]byte, 90))},
			},
			bytesPerToken: 3.0,
			o:             Overheads{MessageTokens: 10, RequestTokens: 20},
			want:          90, // ceil(60/3) + 10 + ceil(90/3) + 10 + 20 = 20 + 10 + 30 + 10 + 20 = 90
		},
		{
			name:          "near-integer fractional value: 1 byte at 0.9999999995 bytes/token",
			msgs:          []transport.Message{{Role: "user", Content: string(make([]byte, 1))}},
			bytesPerToken: 0.9999999995,
			o:             Overheads{MessageTokens: 0, RequestTokens: 0},
			want:          2, // ceil(1/0.9999999995) = ceil(1.0000000005...) = 2, not snapped to 1
		},
		{
			name:          "large magnitude exact: 3MB at 0.3 bytes/token",
			msgs:          []transport.Message{{Role: "user", Content: string(make([]byte, 3000000))}},
			bytesPerToken: 0.3,
			o:             Overheads{MessageTokens: 0, RequestTokens: 0},
			want:          10000000, // 3M / 0.3 = 10M exactly
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.msgs, tt.bytesPerToken, tt.o)
			if got != tt.want {
				t.Errorf("EstimateTokens() = %d, want %d", got, tt.want)
			}
			if got := RequestEstimateTokens(tt.msgs, nil, tt.bytesPerToken, tt.o); got != tt.want {
				t.Errorf("RequestEstimateTokens(no tools) = %d, want %d", got, tt.want)
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
		{name: "NaN", bytesPerToken: math.NaN()},
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
		{
			name:          "rounding safety: 1000 with 0.9 margin",
			contextBound:  1000,
			contextMargin: 0.9,
			want:          100, // floor(1000 * 0.1) = floor(100.0), should be 100 not 99
		},
		{
			name:          "rounding safety: 100 with 0.9 margin",
			contextBound:  100,
			contextMargin: 0.9,
			want:          10, // floor(100 * 0.1) = floor(10.0), should be 10 not 9
		},
		{
			name:          "near-integer fractional value: 1 with 0.0000000005 margin",
			contextBound:  1,
			contextMargin: 0.0000000005,
			want:          0, // floor(1 * (1 - 0.0000000005)) = floor(0.9999999995) = 0, not snapped to 1
		},
		{
			name:          "large magnitude exact: 10M with 0.7 margin",
			contextBound:  10000000,
			contextMargin: 0.7,
			want:          3000000, // floor(10M * 0.3) = floor(3M) = 3M exactly
		},
		{
			name:          "large magnitude genuinely fractional: 10M with 0.0000000000005 margin",
			contextBound:  10000000,
			contextMargin: 0.0000000000005,
			want:          9999999, // floor(10M * (1 - 5e-13)) = floor(10M * 0.9999999999995) = floor(9,999,999.9995) = 9,999,999, not snapped to 10M
		},
		{name: "high margin 10000/0.9461", contextBound: 10000, contextMargin: 0.9461, want: 539},
		{name: "high margin 100000/0.9471", contextBound: 100000, contextMargin: 0.9471, want: 5290},
		{name: "high margin 100000/0.9999", contextBound: 100000, contextMargin: 0.9999, want: 10},
		{name: "high margin 330000/0.9451", contextBound: 330000, contextMargin: 0.9451, want: 18117},
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
		{
			name:            "saturated admission never fits",
			admissionTokens: math.MaxInt,
			maxOutputTokens: 1,
			usableBound:     math.MaxInt,
			want:            false,
		},
		{
			name:            "near-max admission does not wrap",
			admissionTokens: math.MaxInt - 10,
			maxOutputTokens: 4096,
			usableBound:     29491,
			want:            false,
		},
		{
			name:            "saturated admission with zero output fits a max bound",
			admissionTokens: math.MaxInt,
			maxOutputTokens: 0,
			usableBound:     math.MaxInt,
			want:            true,
		},
		{
			name:            "sum below MinInt fits",
			admissionTokens: math.MinInt,
			maxOutputTokens: -1,
			usableBound:     math.MinInt,
			want:            true,
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
			want:            10240, // ceil(4096 * 2.5) = ceil(10240.0) = 10240
		},
		{
			name:            "rounding safety: 100 tokens at 0.07 bytes/token",
			maxOutputTokens: 100,
			bytesPerToken:   0.07,
			want:            7, // ceil(100 * 0.07) = ceil(7.0), should be 7 not 8
		},
		{
			name:            "near-integer fractional value: 1 token at 1.0000000005 bytes/token",
			maxOutputTokens: 1,
			bytesPerToken:   1.0000000005,
			want:            2, // ceil(1.0000000005) = 2, not snapped to 1
		},
		{
			name:            "large magnitude genuinely fractional: 10M tokens at 1.0000000000005 bytes/token",
			maxOutputTokens: 10000000,
			bytesPerToken:   1.0000000000005,
			want:            10000001, // ceil(10M * 1.0000000000005) = ceil(10M + 5e-6) = 10,000,001, not snapped to 10M
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
		{name: "NaN", bytesPerToken: math.NaN()},
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

func TestTinyPositiveCeilsToOne(t *testing.T) {
	msg := []transport.Message{{Role: "user", Content: "x"}}
	if got := AdmissionTokens(msg, 5e-16, Overheads{}); got != 1 {
		t.Errorf("AdmissionTokens = %d, want 1", got)
	}
	if got := EstimateTokens(msg, 2e15, Overheads{}); got != 1 {
		t.Errorf("EstimateTokens = %d, want 1", got)
	}
	if got := StandInBytes(1, 5e-16); got != 1 {
		t.Errorf("StandInBytes = %d, want 1", got)
	}
}

func TestAdjacentFloatsAreNotSwallowed(t *testing.T) {
	msg := []transport.Message{{Role: "user", Content: "x"}}
	if got := AdmissionTokens(msg, math.Nextafter(1, 2), Overheads{}); got != 2 {
		t.Errorf("AdmissionTokens = %d, want 2", got)
	}
	if got := EstimateTokens(msg, math.Nextafter(1, 0), Overheads{}); got != 2 {
		t.Errorf("EstimateTokens = %d, want 2", got)
	}
	if got := StandInBytes(1, math.Nextafter(1, 2)); got != 2 {
		t.Errorf("StandInBytes = %d, want 2", got)
	}
	if got := UsableBound(10_000_000, 5e-16); got != 9_999_999 {
		t.Errorf("UsableBound = %d, want 9999999", got)
	}
}

func TestDecimalIntegralProducts(t *testing.T) {
	msg := []transport.Message{{Role: "user", Content: string(make([]byte, 100))}}
	if got := AdmissionTokens(msg, 1.1, Overheads{}); got != 110 {
		t.Errorf("AdmissionTokens = %d, want 110", got)
	}
	if got := StandInBytes(100, 0.07); got != 7 {
		t.Errorf("StandInBytes = %d, want 7", got)
	}
	for _, c := range []struct {
		b    int
		m    float64
		want int
	}{{1000, 0.9, 100}, {100, 0.9, 10}, {10000, 0.9461, 539}, {100000, 0.9471, 5290}, {100000, 0.9999, 10}, {330000, 0.9451, 18117}} {
		if got := UsableBound(c.b, c.m); got != c.want {
			t.Errorf("UsableBound(%d,%v) = %d, want %d", c.b, c.m, got, c.want)
		}
	}
}

func TestEffectiveBytesPerTokenChained(t *testing.T) {
	for _, c := range []struct {
		calibrated, safety float64
		n, want            int
	}{
		{1.07, 0.51, 10000, 5457},
		{1.07, 0.51, 100000, 54570},
		{1.07, 0.54, 10000, 5778},
	} {
		bpt := EffectiveBytesPerToken(c.calibrated, c.safety)
		if got := StandInBytes(c.n, bpt); got != c.want {
			t.Errorf("StandInBytes(%d, EffectiveBytesPerToken(%v, %v)) = %d, want %d", c.n, c.calibrated, c.safety, got, c.want)
		}
	}
}

// TestEffectiveBytesPerTokenSweep checks the chained path against exact
// rational arithmetic over calibrations c/100 and safeties s/100.
func TestEffectiveBytesPerTokenSweep(t *testing.T) {
	ns := []int{1, 100, 1000, 4096, 10000, 100000}
	for c := 100; c <= 800; c++ {
		for s := 50; s <= 100; s++ {
			bpt := EffectiveBytesPerToken(float64(c)/100, float64(s)/100)
			for _, n := range ns {
				// ceil(n × c × s / 10000) in exact integers.
				num := n * c * s
				want := (num + 9999) / 10000
				if got := StandInBytes(n, bpt); got != want {
					t.Fatalf("StandInBytes(%d, EffectiveBytesPerToken(%v, %v)) = %d, want %d", n, float64(c)/100, float64(s)/100, got, want)
				}
				msg := []transport.Message{{Role: "user", Content: string(make([]byte, n))}}
				// ceil(n × 10000 / (c × s)) in exact integers.
				wantEst := (n*10000 + c*s - 1) / (c * s)
				if got := EstimateTokens(msg, bpt, Overheads{}); got != wantEst {
					t.Fatalf("EstimateTokens(%d B, EffectiveBytesPerToken(%v, %v)) = %d, want %d", n, float64(c)/100, float64(s)/100, got, wantEst)
				}
			}
		}
	}
}

func TestOverflowSaturates(t *testing.T) {
	msg := []transport.Message{{Role: "user", Content: string(make([]byte, 100))}}
	if got := StandInBytes(4096, 1e300); got != math.MaxInt {
		t.Errorf("StandInBytes = %d, want MaxInt", got)
	}
	if got := EstimateTokens(msg, 1e-300, Overheads{MessageTokens: 8, RequestTokens: 16}); got != math.MaxInt {
		t.Errorf("EstimateTokens = %d, want MaxInt", got)
	}
	if got := AdmissionTokens(msg, 1e20, Overheads{MessageTokens: 8, RequestTokens: 16}); got != math.MaxInt {
		t.Errorf("AdmissionTokens = %d, want MaxInt", got)
	}
	two := []transport.Message{msg[0], msg[0]}
	if got := AdmissionTokens(two, 1e17, Overheads{}); got != math.MaxInt {
		t.Errorf("AdmissionTokens of two messages = %d, want MaxInt", got)
	}
	if got := UsableBound(math.MaxInt, -1e300); got != math.MaxInt {
		t.Errorf("UsableBound = %d, want MaxInt", got)
	}
	if got := UsableBound(math.MaxInt, 1e300); got != math.MinInt {
		t.Errorf("UsableBound = %d, want MinInt", got)
	}
}

func TestSaturatedAdmissionDoesNotFit(t *testing.T) {
	msg := []transport.Message{{Role: "user", Content: string(make([]byte, 100))}}
	admission := AdmissionTokens(msg, 1e20, Overheads{MessageTokens: 8, RequestTokens: 16})
	if admission != math.MaxInt {
		t.Fatalf("AdmissionTokens = %d, want MaxInt", admission)
	}
	if Fits(admission, 4096, 29491) {
		t.Errorf("Fits(saturated admission, 4096, 29491) = true, want false")
	}
	if Fits(admission, 4096, UsableBound(32768, 0.10)) {
		t.Errorf("Fits(saturated admission, 4096, UsableBound) = true, want false")
	}
}
