package budget

import (
	"math"

	"github.com/boldfield/syndic/internal/transport"
)

type Overheads struct {
	MessageTokens int
	RequestTokens int
}

func AdmissionTokens(msgs []transport.Message, tokensPerByteMax float64, o Overheads) int {
	if tokensPerByteMax <= 0 {
		panic("tokensPerByteMax must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		bytes := len(msg.Content)
		sum += int(math.Ceil(float64(bytes)*tokensPerByteMax)) + o.MessageTokens
	}
	return sum + o.RequestTokens
}

func EstimateTokens(msgs []transport.Message, bytesPerToken float64, o Overheads) int {
	if bytesPerToken <= 0 {
		panic("bytesPerToken must be greater than zero")
	}

	sum := 0
	for _, msg := range msgs {
		bytes := len(msg.Content)
		sum += int(math.Ceil(float64(bytes)/bytesPerToken)) + o.MessageTokens
	}
	return sum + o.RequestTokens
}

func EffectiveBytesPerToken(calibrated float64, calibrationSafety float64) float64 {
	return calibrated * calibrationSafety
}

func UsableBound(contextBound int, contextMargin float64) int {
	return int(math.Floor(float64(contextBound) * (1 - contextMargin)))
}

func Fits(admissionTokens, maxOutputTokens, usableBound int) bool {
	return admissionTokens+maxOutputTokens <= usableBound
}

func ClampOutput(configured int, outputBound *int) int {
	if outputBound != nil && *outputBound < configured {
		return *outputBound
	}
	return configured
}

func StandInBytes(maxOutputTokens int, bytesPerToken float64) int {
	if bytesPerToken <= 0 {
		panic("bytesPerToken must be greater than zero")
	}

	return int(math.Ceil(float64(maxOutputTokens) * bytesPerToken))
}
