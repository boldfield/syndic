package openai

import (
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/transport"
)

// Headers that state a wait or forbid a retry (v10 delta section 7).
const (
	headerRetryAfterMs = "Retry-After-Ms"
	headerRetryAfter   = "Retry-After"
	headerShouldRetry  = "X-Should-Retry"
)

// rateLimitResets pairs each reset header with the remaining header that must
// be 0 for it to be read.
var rateLimitResets = []struct{ reset, remaining string }{
	{"X-Ratelimit-Reset-Requests", "X-Ratelimit-Remaining-Requests"},
	{"X-Ratelimit-Reset-Tokens", "X-Ratelimit-Remaining-Tokens"},
	{"X-Ratelimit-Reset-Project-Tokens", "X-Ratelimit-Remaining-Project-Tokens"},
}

// durationUnits are the unit suffixes a reset duration may carry, in
// nanoseconds. Longer suffixes come first so "ms" is not read as "m".
var durationUnits = []struct {
	suffix string
	nanos  int64
}{
	{"ns", 1},
	{"us", int64(time.Microsecond)},
	{"µs", int64(time.Microsecond)},
	{"μs", int64(time.Microsecond)},
	{"ms", int64(time.Millisecond)},
	{"s", int64(time.Second)},
	{"m", int64(time.Minute)},
	{"h", int64(time.Hour)},
}

var (
	bigMaxMillis = new(big.Int).SetInt64(math.MaxInt64)
	ratMilli     = big.NewRat(int64(time.Millisecond), 1)
)

// readServerWait reads the wait a response states. retry-after-ms and
// Retry-After are read on any status and the larger is kept; only on a 429
// with neither are the x-ratelimit-reset-* headers read. A value of the wrong
// form, negative, or a date in the past is absent; a value of the right form
// too large for a signed 64-bit count of milliseconds is unbounded. A wait
// that falls between two milliseconds is rounded up, never shortened. No
// header value leaves this function other than as the parsed wait.
func readServerWait(status int, h http.Header, now time.Time) transport.ServerWait {
	w := maxWait(parseRetryAfterMs(h.Get(headerRetryAfterMs)), parseRetryAfter(h.Get(headerRetryAfter), now))
	if w.Kind != transport.ServerWaitAbsent || status != http.StatusTooManyRequests {
		return w
	}
	for _, r := range rateLimitResets {
		if strings.TrimSpace(h.Get(r.remaining)) != "0" {
			continue
		}
		w = maxWait(w, parseResetDuration(h.Get(r.reset)))
	}
	return w
}

// readNoRetry reports whether a response forbids a retry with
// x-should-retry: false.
func readNoRetry(h http.Header) bool {
	return strings.TrimSpace(h.Get(headerShouldRetry)) == "false"
}

// parseRetryAfterMs reads retry-after-ms, an integer count of milliseconds.
func parseRetryAfterMs(v string) transport.ServerWait {
	n, ok := parseDigits(strings.TrimSpace(v))
	if !ok {
		return transport.ServerWait{}
	}
	return waitFromMillis(new(big.Rat).SetInt(n))
}

// parseRetryAfter reads Retry-After as delay seconds or an HTTP date
// (RFC 9110 section 10.2.3), the date measured against now.
func parseRetryAfter(v string, now time.Time) transport.ServerWait {
	v = strings.TrimSpace(v)
	if v == "" {
		return transport.ServerWait{}
	}
	if n, ok := parseDigits(v); ok {
		ms := new(big.Int).Mul(n, big.NewInt(1000))
		return waitFromMillis(new(big.Rat).SetInt(ms))
	}
	t, err := http.ParseTime(v)
	if err != nil || t.Before(now) {
		return transport.ServerWait{}
	}
	// Subtract in big arithmetic: time.Time.Sub saturates at about 292 years,
	// which would hand back a shorter wait than the date states.
	nanos := new(big.Int).Sub(unixNanos(t), unixNanos(now))
	return waitFromMillis(new(big.Rat).SetFrac(nanos, big.NewInt(int64(time.Millisecond))))
}

// parseResetDuration reads an x-ratelimit-reset-* value. Only a duration with
// unit suffixes (1s, 6m0s, 250ms) is read; a bare number has no unit and is
// absent, as is a signed value.
func parseResetDuration(v string) transport.ServerWait {
	v = strings.TrimSpace(v)
	if v == "" {
		return transport.ServerWait{}
	}
	total := new(big.Rat)
	for v != "" {
		i := 0
		for i < len(v) && (isDigit(v[i]) || v[i] == '.') {
			i++
		}
		num, ok := parseDecimal(v[:i])
		if !ok {
			return transport.ServerWait{}
		}
		v = v[i:]
		unit := int64(0)
		for _, u := range durationUnits {
			if strings.HasPrefix(v, u.suffix) {
				unit = u.nanos
				v = v[len(u.suffix):]
				break
			}
		}
		if unit == 0 {
			return transport.ServerWait{}
		}
		total.Add(total, num.Mul(num, big.NewRat(unit, 1)))
	}
	return waitFromMillis(total.Quo(total, ratMilli))
}

// waitFromMillis turns a non-negative count of milliseconds into a wait,
// rounding a fraction up and failing closed to unbounded when the count does
// not fit a signed 64-bit integer.
func waitFromMillis(ms *big.Rat) transport.ServerWait {
	q, r := new(big.Int).QuoRem(ms.Num(), ms.Denom(), new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if q.Cmp(bigMaxMillis) > 0 {
		return transport.ServerWait{Kind: transport.ServerWaitUnbounded}
	}
	return transport.ServerWait{Kind: transport.ServerWaitDuration, Millis: q.Int64()}
}

// maxWait returns the larger of two waits. Unbounded is larger than any
// duration, and any duration, zero included, is larger than absent.
func maxWait(a, b transport.ServerWait) transport.ServerWait {
	if a.Kind != b.Kind {
		if a.Kind > b.Kind {
			return a
		}
		return b
	}
	if b.Millis > a.Millis {
		return b
	}
	return a
}

// parseDigits reads a non-empty run of ASCII digits of any length.
func parseDigits(s string) (*big.Int, bool) {
	if s == "" {
		return nil, false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return nil, false
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	return n, ok
}

// parseDecimal reads digits with at most one decimal point and at least one
// digit, such as 1, 1.5, .5 or 1.
func parseDecimal(s string) (*big.Rat, bool) {
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return nil, false
	}
	for _, p := range []string{intPart, frac} {
		for i := 0; i < len(p); i++ {
			if !isDigit(p[i]) {
				return nil, false
			}
		}
	}
	n, ok := new(big.Int).SetString(intPart+frac, 10)
	if !ok {
		return nil, false
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(frac))), nil)
	return new(big.Rat).SetFrac(n, scale), true
}

// unixNanos is t as nanoseconds since the Unix epoch, without overflow.
func unixNanos(t time.Time) *big.Int {
	n := new(big.Int).Mul(big.NewInt(t.Unix()), big.NewInt(int64(time.Second)))
	return n.Add(n, big.NewInt(int64(t.Nanosecond())))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
