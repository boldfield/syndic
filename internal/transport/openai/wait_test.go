package openai

import (
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

var waitNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func absent() transport.ServerWait { return transport.ServerWait{} }

func unbounded() transport.ServerWait {
	return transport.ServerWait{Kind: transport.ServerWaitUnbounded}
}

func millis(ms int64) transport.ServerWait {
	return transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: ms}
}

func headers(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

// A7: retry-after-ms, Retry-After in seconds and as a date each yield the
// stated wait; with both, the larger; a malformed, negative or past value is
// absent; an explicit 0 is a wait of zero and is not absent.
func TestReadServerWaitRetryHeaders(t *testing.T) {
	cases := []struct {
		name   string
		status int
		h      http.Header
		want   transport.ServerWait
	}{
		{"none", 503, headers(), absent()},
		{"retry-after-ms", 429, headers("retry-after-ms", "1500"), millis(1500)},
		{"retry-after seconds", 503, headers("Retry-After", "30"), millis(30000)},
		{"retry-after date", 503, headers("Retry-After", "Thu, 01 Jan 2026 00:01:00 GMT"), millis(60000)},
		{"retry-after date rfc850", 503, headers("Retry-After", "Thursday, 01-Jan-26 00:00:05 GMT"), millis(5000)},
		{"retry-after date asctime", 503, headers("Retry-After", "Thu Jan  1 00:00:07 2026"), millis(7000)},
		{"date equal to now", 503, headers("Retry-After", "Thu, 01 Jan 2026 00:00:00 GMT"), millis(0)},
		{"both, ms larger", 429, headers("retry-after-ms", "2500", "Retry-After", "2"), millis(2500)},
		{"both, seconds larger", 429, headers("retry-after-ms", "1500", "Retry-After", "2"), millis(2000)},
		{"both, one malformed", 429, headers("retry-after-ms", "soon", "Retry-After", "2"), millis(2000)},
		{"explicit zero ms", 429, headers("retry-after-ms", "0"), millis(0)},
		{"explicit zero seconds", 503, headers("Retry-After", "0"), millis(0)},
		{"whitespace trimmed", 503, headers("Retry-After", " 3 "), millis(3000)},
		{"read on any status", 500, headers("Retry-After", "4"), millis(4000)},
		{"malformed ms", 429, headers("retry-after-ms", "1.5"), absent()},
		{"malformed seconds", 503, headers("Retry-After", "soon"), absent()},
		{"seconds with unit", 503, headers("Retry-After", "5s"), absent()},
		{"empty", 503, headers("Retry-After", ""), absent()},
		{"negative ms", 429, headers("retry-after-ms", "-100"), absent()},
		{"negative seconds", 503, headers("Retry-After", "-5"), absent()},
		{"signed seconds", 503, headers("Retry-After", "+5"), absent()},
		{"past date", 503, headers("Retry-After", "Wed, 31 Dec 2025 23:59:59 GMT"), absent()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readServerWait(c.status, c.h, waitNow); got != c.want {
				t.Fatalf("readServerWait = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A date in the past by less than a millisecond is past, not a wait of zero.
func TestReadServerWaitSubMillisecondPast(t *testing.T) {
	now := waitNow.Add(500 * time.Microsecond)
	h := headers("Retry-After", "Thu, 01 Jan 2026 00:00:00 GMT")
	if got := readServerWait(503, h, now); got != absent() {
		t.Fatalf("readServerWait = %+v, want absent", got)
	}
}

// A date less than a whole millisecond ahead is rounded up, never shortened.
func TestReadServerWaitDateRoundsUp(t *testing.T) {
	now := waitNow.Add(-1500 * time.Microsecond)
	h := headers("Retry-After", "Thu, 01 Jan 2026 00:00:00 GMT")
	if got := readServerWait(503, h, now); got != millis(2) {
		t.Fatalf("readServerWait = %+v, want 2ms", got)
	}
}

// A8: Retry-After: 172800 yields a wait of two days exactly; a run of digits
// too long for 64 bits yields unbounded.
func TestReadServerWaitRange(t *testing.T) {
	maxMs := "9223372036854775807"
	cases := []struct {
		name string
		h    http.Header
		want transport.ServerWait
	}{
		{"two days", headers("Retry-After", "172800"), millis(2 * 24 * 60 * 60 * 1000)},
		// Past time.Duration's ~292 years but well inside int64 milliseconds.
		{"ms beyond time.Duration", headers("retry-after-ms", "9223372036855"), millis(9223372036855)},
		{"ms at int64 max", headers("retry-after-ms", maxMs), millis(math.MaxInt64)},
		{"ms one past int64 max", headers("retry-after-ms", "9223372036854775808"), unbounded()},
		{"ms digits too long", headers("retry-after-ms", "99999999999999999999999999"), unbounded()},
		{"seconds beyond time.Duration", headers("Retry-After", "10000000000"), millis(10000000000000)},
		{"seconds at int64 ms max", headers("Retry-After", "9223372036854775"), millis(9223372036854775000)},
		{"seconds past int64 ms", headers("Retry-After", "9223372036854776"), unbounded()},
		{"seconds digits too long", headers("Retry-After", "99999999999999999999999999"), unbounded()},
		{"unbounded beats duration", headers("retry-after-ms", "5", "Retry-After", "99999999999999999999"), unbounded()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readServerWait(503, c.h, waitNow); got != c.want {
				t.Fatalf("readServerWait = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A date far in the future is kept exactly, not saturated to time.Duration.
func TestReadServerWaitFarFutureDate(t *testing.T) {
	h := headers("Retry-After", "Fri, 01 Jan 9999 00:00:00 GMT")
	date := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	want := millis(date.UnixMilli() - waitNow.UnixMilli())
	if got := readServerWait(503, h, waitNow); got != want {
		t.Fatalf("readServerWait = %+v, want %+v", got, want)
	}
}

// A9: the reset headers yield a wait only on a 429 with no retry header, only
// for a limit whose remaining is 0, and only from a duration with units; a
// bare number yields none.
func TestReadServerWaitResetHeaders(t *testing.T) {
	cases := []struct {
		name   string
		status int
		h      http.Header
		want   transport.ServerWait
	}{
		{"requests exhausted", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s"), millis(1000)},
		{"tokens exhausted", 429, headers(
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s"), millis(360000)},
		{"project tokens exhausted", 429, headers(
			"x-ratelimit-remaining-project-tokens", "0", "x-ratelimit-reset-project-tokens", "250ms"), millis(250)},
		{"largest of exhausted", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s"), millis(360000)},
		{"only exhausted limits count", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s",
			"x-ratelimit-remaining-tokens", "12", "x-ratelimit-reset-tokens", "6m0s"), millis(1000)},
		{"remaining not zero", 429, headers(
			"x-ratelimit-remaining-requests", "3", "x-ratelimit-reset-requests", "1s"), absent()},
		{"remaining missing", 429, headers("x-ratelimit-reset-requests", "1s"), absent()},
		{"fractional units", 429, headers(
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "1.5s"), millis(1500)},
		{"hours and minutes", 429, headers(
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "1h2m3s"), millis(3723000)},
		{"sub-millisecond rounds up", 429, headers(
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "1500us"), millis(2)},
		{"zero duration", 429, headers(
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "0s"), millis(0)},
		{"bare number", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1"), absent()},
		{"bare zero", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "0"), absent()},
		{"bare number ignored, unit read", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "60",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "2s"), millis(2000)},
		{"trailing number", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s5"), absent()},
		{"negative duration", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "-1s"), absent()},
		{"unknown unit", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1d"), absent()},
		{"duration too long", 429, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "99999999999999999999h"), unbounded()},
		{"not on 503", 503, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s"), absent()},
		{"not on 500", 500, headers(
			"x-ratelimit-remaining-requests", "0", "x-ratelimit-reset-requests", "1s"), absent()},
		{"not when retry-after parsed", 429, headers("Retry-After", "1",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s"), millis(1000)},
		{"not when retry-after zero", 429, headers("Retry-After", "0",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s"), millis(0)},
		{"not when retry-after-ms parsed", 429, headers("retry-after-ms", "10",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s"), millis(10)},
		{"read when retry header malformed", 429, headers("Retry-After", "soon",
			"x-ratelimit-remaining-tokens", "0", "x-ratelimit-reset-tokens", "6m0s"), millis(360000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readServerWait(c.status, c.h, waitNow); got != c.want {
				t.Fatalf("readServerWait = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A11: x-should-retry: false sets NoRetry.
func TestReadNoRetry(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want bool
	}{
		{"false", headers("x-should-retry", "false"), true},
		{"false trimmed", headers("x-should-retry", " false "), true},
		{"true", headers("x-should-retry", "true"), false},
		{"absent", headers(), false},
		{"other", headers("x-should-retry", "no"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readNoRetry(c.h); got != c.want {
				t.Fatalf("readNoRetry = %v, want %v", got, c.want)
			}
		})
	}
}
