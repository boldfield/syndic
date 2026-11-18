package budget

import (
	"math"

	"github.com/boldfield/syndic/internal/transport"
)

type Overheads struct {
	MessageTokens int
	RequestTokens int
}

// ceilSafe rounds a float up to the nearest integer, snapping to the nearest integer
// when the result is within relative floating-point error of an integer
// (to avoid floating-point errors that round mathematically integral results up by one).
// Uses relative tolerance to distinguish representation error from genuine fractions.
func ceilSafe(x float64) int {
	r := math.Round(x)
	// Snap only if error is within relative epsilon: use max(1.0, abs(r)) to avoid
	// division issues near zero. This catches products like 100*1.1=110.00000000000001
	// but not values like 1.0000000005 which are genuinely fractional.
	if math.Abs(x-r) < math.Max(1.0, math.Abs(r))*1e-12 {
		return int(r)
	}
	return int(math.Ceil(x))
}

// floorSafe rounds a float down to the nearest integer, snapping to the nearest integer
// when the result is within relative floating-point error of an integer
// (to avoid floating-point errors that round mathematically integral results down by one).
// Uses relative tolerance to distinguish representation error from genuine fractions.
func floorSafe(x float64) int {
	r := math.Round(x)
	// Snap only if error is within relative epsilon: use max(1.0, abs(r)) to avoid
	// division issues near zero. This catches products like 1000*0.1=100.0 but not
	// values like 0.9999999995 which are genuinely fractional.
	if math.Abs(x-r) < math.Max(1.0, math.Abs(r))*1e-12 {
		return int(r)
	}
	return int(math.Floor(x))
}

// AdmissionTokens computes the token count for messages using an admission bound formula.
// It calculates: sum of (ceil(len(content in bytes) × tokensPerByteMax) + MessageTokens) + RequestTokens.
// Panics if tokensPerByteMax is not greater than zero (including NaN).
func AdmissionTokens(msgs []transport.Message, tokensPerByteMax float64, o Overheads) int {
	if !(tokensPerByteMax > 0) {
		panic("tokensPerByteMax must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		bytes := len(msg.Content)
		sum += ceilSafe(float64(bytes)*tokensPerByteMax) + o.MessageTokens
	}
	return sum + o.RequestTokens
}

// EstimateTokens computes an estimate of token count for messages.
// It calculates: sum of (ceil(len(content in bytes) / bytesPerToken) + MessageTokens) + RequestTokens.
// Panics if bytesPerToken is not greater than zero (including NaN).
func EstimateTokens(msgs []transport.Message, bytesPerToken float64, o Overheads) int {
	if !(bytesPerToken > 0) {
		panic("bytesPerToken must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		bytes := len(msg.Content)
		sum += ceilSafe(float64(bytes)/bytesPerToken) + o.MessageTokens
	}
	return sum + o.RequestTokens
}

// EffectiveBytesPerToken computes the effective bytes per token by multiplying
// a calibrated value by a calibration safety factor.
// It calculates: calibrated × calibrationSafety.
func EffectiveBytesPerToken(calibrated float64, calibrationSafety float64) float64 {
	return calibrated * calibrationSafety
}

// UsableBound computes the usable context bound given a context margin.
// It calculates: floor(contextBound × (1 - contextMargin)).
func UsableBound(contextBound int, contextMargin float64) int {
	return floorSafe(float64(contextBound) * (1 - contextMargin))
}

// Fits determines whether admission tokens plus max output tokens fit within the usable bound.
// It returns true if admissionTokens + maxOutputTokens ≤ usableBound.
func Fits(admissionTokens, maxOutputTokens, usableBound int) bool {
	return admissionTokens+maxOutputTokens <= usableBound
}

// ClampOutput returns the configured output value, or the outputBound if it is non-nil and smaller.
func ClampOutput(configured int, outputBound *int) int {
	if outputBound != nil && *outputBound < configured {
		return *outputBound
	}
	return configured
}

// StandInBytes computes the size in bytes of a stand-in text placeholder.
// It calculates: ceil(maxOutputTokens × bytesPerToken).
// Panics if bytesPerToken is not greater than zero (including NaN).
func StandInBytes(maxOutputTokens int, bytesPerToken float64) int {
	if !(bytesPerToken > 0) {
		panic("bytesPerToken must be greater than zero")
	}

	return ceilSafe(float64(maxOutputTokens) * bytesPerToken)
}
