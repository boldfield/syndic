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
// when the result is within ULP-scale floating-point error of an integer
// (to avoid floating-point errors that round mathematically integral results up by one).
// Uses a tolerance proportional to the magnitude to handle ULPs correctly.
func ceilSafe(x float64) int {
	r := math.Round(x)
	// Snap only if error is within ~4.5 ULPs: tolerance = 1e-15 * max(1.0, abs(r)).
	// At any magnitude m, this is roughly (1e-15 * m) / (m * 2.22e-16) ≈ 4.5 ULPs.
	// This catches products like 100*1.1=110.00000000000001 but rejects genuinely
	// fractional values like 1.0000000005 and large-magnitude fractions like
	// 10,000,000.000005 (error 5e-6 >> tolerance 1e-8).
	if math.Abs(x-r) < math.Max(1.0, math.Abs(r))*1e-15 {
		return int(r)
	}
	return int(math.Ceil(x))
}

// floorSafe rounds a float down to the nearest integer, snapping to the nearest integer
// when the result is within ULP-scale floating-point error of an integer
// (to avoid floating-point errors that round mathematically integral results down by one).
// Uses a tolerance proportional to the magnitude to handle ULPs correctly.
func floorSafe(x float64) int {
	r := math.Round(x)
	// Snap only if error is within ~4.5 ULPs: tolerance = 1e-15 * max(1.0, abs(r)).
	// At any magnitude m, this is roughly (1e-15 * m) / (m * 2.22e-16) ≈ 4.5 ULPs.
	// This catches products like 1000*0.1=100.0 but rejects genuinely fractional values
	// like 0.9999999995 and large-magnitude near-integer values like 9,999,999.999995
	// (fractional part 0.999995 >> tolerance 1e-8 at magnitude 10M).
	if math.Abs(x-r) < math.Max(1.0, math.Abs(r))*1e-15 {
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
