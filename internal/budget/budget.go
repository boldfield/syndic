package budget

import (
	"math"
	"math/big"
	"strconv"

	"github.com/boldfield/syndic/internal/transport"
)

// Overheads are the declared per-message and per-request token overheads
// (message_overhead_tokens and request_overhead_tokens).
type Overheads struct {
	MessageTokens int
	RequestTokens int
}

// decimalRat returns the exact rational value of the shortest decimal string
// that round-trips to x (so 1.1 is 11/10, not its binary approximation).
// Arithmetic on these values is exact, so a product that is mathematically an
// integer in decimal never rounds by one, and adjacent representable
// fractional inputs are never swallowed by a tolerance.
func decimalRat(x float64) *big.Rat {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		panic("budget: value must be finite")
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(x, 'g', -1, 64))
	if !ok {
		panic("budget: cannot represent value")
	}
	return r
}

// ratCeil returns ceil(r).
func ratCeil(r *big.Rat) int {
	q := new(big.Int).Div(r.Num(), r.Denom()) // floor for a positive denominator
	if !r.IsInt() {
		q.Add(q, big.NewInt(1))
	}
	return int(q.Int64())
}

// ratFloor returns floor(r).
func ratFloor(r *big.Rat) int {
	return int(new(big.Int).Div(r.Num(), r.Denom()).Int64())
}

// ceilMul returns ceil(n × rate) exactly.
func ceilMul(n int, rate float64) int {
	return ratCeil(new(big.Rat).Mul(big.NewRat(int64(n), 1), decimalRat(rate)))
}

// ceilDiv returns ceil(n / rate) exactly.
func ceilDiv(n int, rate float64) int {
	return ratCeil(new(big.Rat).Quo(big.NewRat(int64(n), 1), decimalRat(rate)))
}

// AdmissionTokens computes the token count for messages using an admission bound formula.
// It calculates: sum of (ceil(len(content in bytes) × tokensPerByteMax) + MessageTokens) + RequestTokens.
// Panics if tokensPerByteMax is not greater than zero (including NaN).
// Float arguments are taken at their shortest decimal form and must be finite.
func AdmissionTokens(msgs []transport.Message, tokensPerByteMax float64, o Overheads) int {
	if !(tokensPerByteMax > 0) {
		panic("tokensPerByteMax must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		bytes := len(msg.Content)
		sum += ceilMul(bytes, tokensPerByteMax) + o.MessageTokens
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
		sum += ceilDiv(bytes, bytesPerToken) + o.MessageTokens
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
	f := new(big.Rat).Sub(big.NewRat(1, 1), decimalRat(contextMargin))
	return ratFloor(f.Mul(f, big.NewRat(int64(contextBound), 1)))
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

	return ceilMul(maxOutputTokens, bytesPerToken)
}
