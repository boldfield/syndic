package openai

import (
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestReadServerWait(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dur := func(d time.Duration) transport.ServerWait { return durationWait(d.Milliseconds()) }
	absent := transport.ServerWait{}
	huge := strings.Repeat("9", 40)
	cases := []struct {
		name   string
		status int
		h      http.Header
		want   transport.ServerWait
	}{
		{"ms", 503, hdr("retry-after-ms", "1500"), dur(1500 * time.Millisecond)},
		{"seconds", 503, hdr("Retry-After", "7"), dur(7 * time.Second)},
		{"date", 503, hdr("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat)), dur(90 * time.Second)},
		{"both larger", 429, hdr("retry-after-ms", "2000", "Retry-After", "5"), dur(5 * time.Second)},
		{"both larger ms", 429, hdr("retry-after-ms", "9000", "Retry-After", "5"), dur(9 * time.Second)},
		{"zero", 429, hdr("Retry-After", "0"), dur(0)},
		{"zero ms", 429, hdr("retry-after-ms", "0"), dur(0)},
		{"malformed", 429, hdr("Retry-After", "soon"), absent},
		{"negative", 429, hdr("Retry-After", "-3", "retry-after-ms", "-1"), absent},
		{"past date", 503, hdr("Retry-After", now.Add(-time.Hour).Format(http.TimeFormat)), absent},
		{"two days", 503, hdr("Retry-After", "172800"), dur(48 * time.Hour)},
		{"beyond Duration, fits ms", 503, hdr("retry-after-ms", "9223372036855"), durationWait(9223372036855)},
		{"int64 ms max", 503, hdr("retry-after-ms", "9223372036854775807"), durationWait(math.MaxInt64)},
		{"int64 ms overflow", 503, hdr("retry-after-ms", "9223372036854775808"), unboundedWait()},
		{"seconds at int64 ms limit", 503, hdr("Retry-After", "9223372036854775"), durationWait(9223372036854775000)},
		{"seconds past int64 ms limit", 503, hdr("Retry-After", "9223372036854776"), unboundedWait()},
		{"far future date", 503, hdr("Retry-After", "Fri, 01 Jan 9999 00:00:00 GMT"), durationWait(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() - now.UnixMilli())},
		{"reset beyond Duration", 429, hdr("x-ratelimit-reset-tokens", "3000000h", "x-ratelimit-remaining-tokens", "0"), durationWait(3000000 * 3600000)},
		{"reset rounds up", 429, hdr("x-ratelimit-reset-tokens", "1.0005s", "x-ratelimit-remaining-tokens", "0"), durationWait(1001)},
		{"too long", 503, hdr("Retry-After", huge), unboundedWait()},
		{"too long ms", 503, hdr("retry-after-ms", huge), unboundedWait()},
		{"unbounded beats duration", 503, hdr("Retry-After", huge, "retry-after-ms", "5"), unboundedWait()},
		{"malformed one, other kept", 503, hdr("Retry-After", "x", "retry-after-ms", "5"), dur(5 * time.Millisecond)},
		{"reset qualifies", 429, hdr("x-ratelimit-reset-requests", "1s", "x-ratelimit-remaining-requests", "0"), dur(time.Second)},
		{"reset largest", 429, hdr(
			"x-ratelimit-reset-requests", "1s", "x-ratelimit-remaining-requests", "0",
			"x-ratelimit-reset-tokens", "6m0s", "x-ratelimit-remaining-tokens", "0",
			"x-ratelimit-reset-project-tokens", "250ms", "x-ratelimit-remaining-project-tokens", "0"), dur(6 * time.Minute)},
		{"reset remaining nonzero", 429, hdr("x-ratelimit-reset-tokens", "6m0s", "x-ratelimit-remaining-tokens", "5"), absent},
		{"reset remaining missing", 429, hdr("x-ratelimit-reset-tokens", "6m0s"), absent},
		{"reset bare number", 429, hdr("x-ratelimit-reset-tokens", "30", "x-ratelimit-remaining-tokens", "0"), absent},
		{"reset bare zero", 429, hdr("x-ratelimit-reset-tokens", "0", "x-ratelimit-remaining-tokens", "0"), absent},
		{"reset ms unit", 429, hdr("x-ratelimit-reset-tokens", "250ms", "x-ratelimit-remaining-tokens", "0"), dur(250 * time.Millisecond)},
		{"reset not on 503", 503, hdr("x-ratelimit-reset-tokens", "1s", "x-ratelimit-remaining-tokens", "0"), absent},
		{"reset not when retry header parsed", 429, hdr("Retry-After", "1", "x-ratelimit-reset-tokens", "1m", "x-ratelimit-remaining-tokens", "0"), dur(time.Second)},
		{"reset negative", 429, hdr("x-ratelimit-reset-tokens", "-1s", "x-ratelimit-remaining-tokens", "0"), absent},
		{"reset overflow", 429, hdr("x-ratelimit-reset-tokens", huge+"s", "x-ratelimit-remaining-tokens", "0"), unboundedWait()},
		{"none", 429, hdr(), absent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readServerWait(c.status, c.h, now); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestReadNoRetry(t *testing.T) {
	if !readNoRetry(hdr("x-should-retry", "false")) {
		t.Fatal("false should set NoRetry")
	}
	for _, v := range []string{"", "true", "no"} {
		if readNoRetry(hdr("x-should-retry", v)) {
			t.Fatalf("%q should not set NoRetry", v)
		}
	}
}
