package openai

import (
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

var (
	digitsOnly = regexp.MustCompile(`^[0-9]+$`)
	// unitDuration is the shape of a duration with unit suffixes, as ParseDuration reads it, unsigned.
	unitDuration = regexp.MustCompile(`^([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h)(([0-9]+(\.[0-9]*)?|\.[0-9]+)(ns|us|µs|μs|ms|s|m|h))*$`)
)

// resetLimits pairs each rate-limit reset header with its remaining header.
var resetLimits = [...][2]string{
	{"x-ratelimit-reset-requests", "x-ratelimit-remaining-requests"},
	{"x-ratelimit-reset-tokens", "x-ratelimit-remaining-tokens"},
	{"x-ratelimit-reset-project-tokens", "x-ratelimit-remaining-project-tokens"},
}

func durationWait(ms int64) transport.ServerWait {
	return transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: ms}
}

func unboundedWait() transport.ServerWait {
	return transport.ServerWait{Kind: transport.ServerWaitUnbounded}
}

// largerWait returns the larger of two waits; absent is smaller than any wait.
func largerWait(a, b transport.ServerWait) transport.ServerWait {
	switch {
	case a.Kind == transport.ServerWaitUnbounded || b.Kind == transport.ServerWaitUnbounded:
		return unboundedWait()
	case a.Kind == transport.ServerWaitAbsent:
		return b
	case b.Kind == transport.ServerWaitAbsent:
		return a
	case b.Millis > a.Millis:
		return b
	}
	return a
}

// parseCount reads a run of decimal digits as a count of unit, with unit
// milliseconds per count. A run that does not fit a signed 64-bit count of
// milliseconds is unbounded.
func parseCount(v string, unit int64) transport.ServerWait {
	v = strings.TrimSpace(v)
	if !digitsOnly.MatchString(v) {
		return transport.ServerWait{}
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n > uint64(math.MaxInt64/unit) {
		return unboundedWait()
	}
	return durationWait(int64(n) * unit)
}

func parseRetryAfter(v string, now time.Time) transport.ServerWait {
	v = strings.TrimSpace(v)
	if digitsOnly.MatchString(v) {
		return parseCount(v, 1000)
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return transport.ServerWait{}
	}
	// A date has a four-digit year, so the millisecond difference cannot overflow.
	ms := t.UnixMilli() - now.UnixMilli()
	if ms < 0 {
		return transport.ServerWait{}
	}
	return durationWait(ms)
}

// unitMillis is the number of milliseconds in each duration unit.
var unitMillis = map[string]*big.Rat{
	"ns": big.NewRat(1, 1000000),
	"us": big.NewRat(1, 1000),
	"µs": big.NewRat(1, 1000),
	"μs": big.NewRat(1, 1000),
	"ms": big.NewRat(1, 1),
	"s":  big.NewRat(1000, 1),
	"m":  big.NewRat(60000, 1),
	"h":  big.NewRat(3600000, 1),
}

var durationPart = regexp.MustCompile(`([0-9]*\.?[0-9]*)(ns|us|µs|μs|ms|s|m|h)`)

// parseResetDuration reads only a duration with unit suffixes; a bare number
// is ignored. The sum is rounded up to whole milliseconds, never shortened.
func parseResetDuration(v string) transport.ServerWait {
	v = strings.TrimSpace(v)
	if !unitDuration.MatchString(v) {
		return transport.ServerWait{}
	}
	total := new(big.Rat)
	for _, m := range durationPart.FindAllStringSubmatch(v, -1) {
		n, ok := new(big.Rat).SetString(m[1])
		if !ok {
			return transport.ServerWait{}
		}
		total.Add(total, n.Mul(n, unitMillis[m[2]]))
	}
	ms := new(big.Int).Div(total.Num(), total.Denom())
	if new(big.Int).Mod(total.Num(), total.Denom()).Sign() != 0 {
		ms.Add(ms, big.NewInt(1))
	}
	if !ms.IsInt64() {
		return unboundedWait()
	}
	return durationWait(ms.Int64())
}

// readServerWait reads the wait a response states, by the design's rules.
func readServerWait(status int, h http.Header, now time.Time) transport.ServerWait {
	var w transport.ServerWait
	if v := h.Get("retry-after-ms"); v != "" {
		w = largerWait(w, parseCount(v, 1))
	}
	if v := h.Get("Retry-After"); v != "" {
		w = largerWait(w, parseRetryAfter(v, now))
	}
	if w.Kind != transport.ServerWaitAbsent || status != http.StatusTooManyRequests {
		return w
	}
	for _, p := range resetLimits {
		rem := strings.TrimSpace(h.Get(p[1]))
		if !digitsOnly.MatchString(rem) {
			continue
		}
		if n, err := strconv.ParseUint(rem, 10, 64); err != nil || n != 0 {
			continue
		}
		w = largerWait(w, parseResetDuration(h.Get(p[0])))
	}
	return w
}

// readNoRetry reports whether the response carries x-should-retry: false.
func readNoRetry(h http.Header) bool {
	return strings.EqualFold(strings.TrimSpace(h.Get("x-should-retry")), "false")
}
