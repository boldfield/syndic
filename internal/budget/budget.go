package budget

import (
	"encoding/json"
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
	if !isFinite(x) {
		panic("budget: value must be finite")
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(x, 'g', -1, 64))
	if !ok {
		panic("budget: cannot represent value")
	}
	return r
}

var (
	maxInt = big.NewInt(math.MaxInt)
	minInt = big.NewInt(math.MinInt)
)

// clampInt converts q to an int, saturating at math.MaxInt and math.MinInt
// rather than wrapping, so an out-of-range bound can never come back small.
func clampInt(q *big.Int) int {
	if q.Cmp(maxInt) > 0 {
		return math.MaxInt
	}
	if q.Cmp(minInt) < 0 {
		return math.MinInt
	}
	return int(q.Int64())
}

// addSat returns a + b, saturating at math.MaxInt and math.MinInt.
func addSat(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	if b < 0 && a < math.MinInt-b {
		return math.MinInt
	}
	return a + b
}

// ratCeil returns ceil(r), saturated to the int range.
func ratCeil(r *big.Rat) int {
	q := new(big.Int).Div(r.Num(), r.Denom()) // floor for a positive denominator
	if !r.IsInt() {
		q.Add(q, big.NewInt(1))
	}
	return clampInt(q)
}

// ratFloor returns floor(r), saturated to the int range.
func ratFloor(r *big.Rat) int {
	return clampInt(new(big.Int).Div(r.Num(), r.Denom()))
}

// ceilMul returns ceil(n × rate) exactly.
func ceilMul(n int, rate float64) int {
	return ratCeil(new(big.Rat).Mul(big.NewRat(int64(n), 1), decimalRat(rate)))
}

// ceilDiv returns ceil(n / rate) exactly.
func ceilDiv(n int, rate float64) int {
	return ratCeil(new(big.Rat).Quo(big.NewRat(int64(n), 1), decimalRat(rate)))
}

// PromptBytes returns the counted prompt bytes of one request: the messages
// and the tool definitions sent with them. It is the sum of
//
//  1. each message's content, len(Content), for every role;
//  2. for each "assistant" message, the length of the JSON encoding of its
//     tool_calls array as the Chat Completions adapter sends it, an array of
//     {"id", "type", "function": {"name", "arguments"}}; zero with no calls;
//  3. for each "tool" message, len(ToolCallID);
//  4. the length of the JSON encoding of the whole tools array as the adapter
//     sends it, an array of {"type", "function": {"name", "description",
//     "parameters"}}; zero with no definitions.
//
// The encoding is encoding/json with default settings. Nothing else is
// counted: the JSON framing of the message objects is covered by the declared
// overheads. A part that cannot be encoded, which the adapter could not send
// either, counts as math.MaxInt. The sum saturates at math.MaxInt.
func PromptBytes(msgs []transport.Message, tools []transport.Tool) int {
	sum := toolsBytes(tools)
	for _, msg := range msgs {
		sum = addSat(sum, messageBytes(msg))
	}
	return sum
}

// messageBytes is the counted bytes of one message: items 1 to 3 of
// PromptBytes.
func messageBytes(msg transport.Message) int {
	n := len(msg.Content)
	switch msg.Role {
	case "assistant":
		n = addSat(n, toolCallsBytes(msg.ToolCalls))
	case "tool":
		n = addSat(n, len(msg.ToolCallID))
	}
	return n
}

// toolCallsBytes is item 2 of PromptBytes.
func toolCallsBytes(calls []transport.ToolCall) int {
	if len(calls) == 0 {
		return 0
	}
	shaped := make([]map[string]any, len(calls))
	for i, call := range calls {
		shaped[i] = map[string]any{
			"id":   call.ID,
			"type": call.Type,
			"function": map[string]any{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			},
		}
	}
	return encodedLen(shaped)
}

// toolsBytes is item 4 of PromptBytes.
func toolsBytes(tools []transport.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	shaped := make([]map[string]any, len(tools))
	for i, tool := range tools {
		shaped[i] = map[string]any{
			"type": tool.Type,
			"function": map[string]any{
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
				"parameters":  tool.Function.Parameters,
			},
		}
	}
	return encodedLen(shaped)
}

// encodedLen is the length of v's default encoding/json encoding, or
// math.MaxInt when v cannot be encoded.
func encodedLen(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return math.MaxInt
	}
	return len(b)
}

// AdmissionTokens computes the token count for messages using an admission bound formula.
// It calculates: sum of (ceil(len(content in bytes) × tokensPerByteMax) + MessageTokens) + RequestTokens.
// It is RequestAdmissionTokens with no tool definitions.
// Panics if tokensPerByteMax is not greater than zero (including NaN).
// Float arguments are taken at their shortest decimal form and must be finite.
// A result beyond the int range saturates at math.MaxInt.
func AdmissionTokens(msgs []transport.Message, tokensPerByteMax float64, o Overheads) int {
	return RequestAdmissionTokens(msgs, nil, tokensPerByteMax, o)
}

// RequestAdmissionTokens computes the admission bound of one request: its
// messages and the tool definitions sent with them, counted by PromptBytes.
// It calculates: sum over messages of (ceil(message bytes × tokensPerByteMax)
// + MessageTokens) + ceil(definition bytes × tokensPerByteMax) + RequestTokens.
// The definitions are not a message and add no MessageTokens.
// Panics if tokensPerByteMax is not greater than zero (including NaN).
// Float arguments are taken at their shortest decimal form and must be finite.
// A result beyond the int range saturates at math.MaxInt.
func RequestAdmissionTokens(msgs []transport.Message, tools []transport.Tool, tokensPerByteMax float64, o Overheads) int {
	if !(tokensPerByteMax > 0) {
		panic("tokensPerByteMax must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		sum = addSat(sum, addSat(ceilMul(messageBytes(msg), tokensPerByteMax), o.MessageTokens))
	}
	sum = addSat(sum, ceilMul(toolsBytes(tools), tokensPerByteMax))
	return addSat(sum, o.RequestTokens)
}

// EstimateTokens computes an estimate of token count for messages.
// It calculates: sum of (ceil(len(content in bytes) / bytesPerToken) + MessageTokens) + RequestTokens.
// It is RequestEstimateTokens with no tool definitions.
// Panics if bytesPerToken is not greater than zero (including NaN).
// Float arguments are taken at their shortest decimal form and must be finite.
// A result beyond the int range saturates at math.MaxInt.
func EstimateTokens(msgs []transport.Message, bytesPerToken float64, o Overheads) int {
	return RequestEstimateTokens(msgs, nil, bytesPerToken, o)
}

// RequestEstimateTokens computes the estimate of one request: its messages
// and the tool definitions sent with them, counted by PromptBytes.
// It calculates: sum over messages of (ceil(message bytes / bytesPerToken)
// + MessageTokens) + ceil(definition bytes / bytesPerToken) + RequestTokens.
// The definitions are not a message and add no MessageTokens.
// Panics if bytesPerToken is not greater than zero (including NaN).
// Float arguments are taken at their shortest decimal form and must be finite.
// A result beyond the int range saturates at math.MaxInt.
func RequestEstimateTokens(msgs []transport.Message, tools []transport.Tool, bytesPerToken float64, o Overheads) int {
	if !(bytesPerToken > 0) {
		panic("bytesPerToken must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		sum = addSat(sum, addSat(ceilDiv(messageBytes(msg), bytesPerToken), o.MessageTokens))
	}
	sum = addSat(sum, ceilDiv(toolsBytes(tools), bytesPerToken))
	return addSat(sum, o.RequestTokens)
}

// EffectiveBytesPerToken computes the effective bytes per token by multiplying
// a calibrated value by a calibration safety factor.
// It calculates: calibrated × calibrationSafety.
// It applies to a probe calibration only; a roster-declared value is used as
// declared, and the caller decides which case applies.
// The product is taken exactly over the shortest decimal forms of the inputs
// and then rounded to the nearest float64, so 1.07 × 0.51 is 0.5457 rather
// than its binary approximation and the downstream ceilings stay integer-safe.
// A non-finite input (NaN or ±Inf) is not validated here: the result is the
// plain float64 product, which the downstream functions then reject or treat
// as their own preconditions dictate.
func EffectiveBytesPerToken(calibrated float64, calibrationSafety float64) float64 {
	if !isFinite(calibrated) || !isFinite(calibrationSafety) {
		return calibrated * calibrationSafety
	}
	f, _ := new(big.Rat).Mul(decimalRat(calibrated), decimalRat(calibrationSafety)).Float64()
	return f
}

func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}

// UsableBound computes the usable context bound given a context margin.
// It calculates: floor(contextBound × (1 - contextMargin)).
// contextMargin is expected in [0, 1), as the configuration validator enforces;
// a margin outside that range yields a bound above contextBound or below zero.
// contextMargin is taken at its shortest decimal form and must be finite.
// A result beyond the int range saturates at math.MaxInt or math.MinInt.
func UsableBound(contextBound int, contextMargin float64) int {
	f := new(big.Rat).Sub(big.NewRat(1, 1), decimalRat(contextMargin))
	return ratFloor(f.Mul(f, big.NewRat(int64(contextBound), 1)))
}

// Fits determines whether admission tokens plus max output tokens fit within the usable bound.
// It returns true if admissionTokens + maxOutputTokens ≤ usableBound.
// The sum is compared exactly: a sum beyond the int range does not wrap, so a
// saturated admission bound never fits.
func Fits(admissionTokens, maxOutputTokens, usableBound int) bool {
	if maxOutputTokens > 0 && admissionTokens > math.MaxInt-maxOutputTokens {
		return false // sum exceeds math.MaxInt, which is at least usableBound
	}
	if maxOutputTokens < 0 && admissionTokens < math.MinInt-maxOutputTokens {
		return true // sum is below math.MinInt, which is at most usableBound
	}
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
// bytesPerToken is taken at its shortest decimal form and must be finite.
// A result beyond the int range saturates at math.MaxInt.
func StandInBytes(maxOutputTokens int, bytesPerToken float64) int {
	if !(bytesPerToken > 0) {
		panic("bytesPerToken must be greater than zero")
	}

	return ceilMul(maxOutputTokens, bytesPerToken)
}
