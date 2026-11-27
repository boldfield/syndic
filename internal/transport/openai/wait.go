package openai

import (
	"math"
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

func durationWait(d time.Duration) transport.ServerWait {
	return transport.ServerWait{Kind: transport.ServerWaitDuration, Duration: d}
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
	case b.Duration > a.Duration:
		return b
	}
	return a
}

// parseCount reads a run of decimal digits as a count of unit, with unit
// nanoseconds per count. A run that does not fit a Duration is unbounded.
func parseCount(v string, unit time.Duration) transport.ServerWait {
	v = strings.TrimSpace(v)
	if !digitsOnly.MatchString(v) {
		return transport.ServerWait{}
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n > uint64(math.MaxInt64/int64(unit)) {
		return unboundedWait()
	}
	return durationWait(time.Duration(n) * unit)
}

func parseRetryAfter(v string, now time.Time) transport.ServerWait {
	v = strings.TrimSpace(v)
	if digitsOnly.MatchString(v) {
		return parseCount(v, time.Second)
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return transport.ServerWait{}
	}
	d := t.Sub(now)
	if d < 0 {
		return transport.ServerWait{}
	}
	return durationWait(d)
}

// parseResetDuration reads only a duration with unit suffixes; a bare number is ignored.
func parseResetDuration(v string) transport.ServerWait {
	v = strings.TrimSpace(v)
	if !unitDuration.MatchString(v) {
		return transport.ServerWait{}
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return unboundedWait()
	}
	return durationWait(d)
}

// readServerWait reads the wait a response states, by the design's rules.
func readServerWait(status int, h http.Header, now time.Time) transport.ServerWait {
	var w transport.ServerWait
	if v := h.Get("retry-after-ms"); v != "" {
		w = largerWait(w, parseCount(v, time.Millisecond))
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
