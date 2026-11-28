package mcp

// This file holds the credentials the engine has for MCP servers and the
// scrub of what servers return (v9 delta, section 7, "No ambient authority",
// "Credentials" and "Scrub"; section 3, last sentence). A static credential
// belongs to one server and is attached only to requests to that server's
// configured origin. Every credential value the engine holds, static or
// issued, and every value of a local server's env, is in the scrub list, and
// server output is scrubbed with it before it reaches a log, the journal or a
// model. Issued tokens are another task's; they join the list through Add.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/boldfield/syndic/internal/transport"
)

// Credential is a static credential for one server: the headers the roster's
// secret yields. It holds the secret's value and is never journaled or shown.
type Credential struct {
	origin string
	header map[string][]string
	// values are every string that carries the secret: each header value,
	// the token alone for a bearer credential, and the JSON-escaped forms.
	values []string
}

// ParseStaticCredential reads the value of a server's `auth` secret for the
// server whose configured url is serverURL. A value that is a JSON object of
// header names to string values is a header set; any other non-empty value is
// a bearer token, sent as `Authorization: Bearer <value>`.
func ParseStaticCredential(serverURL, value string) (*Credential, error) {
	origin, err := originOf(serverURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("mcp: static credential is empty")
	}
	c := &Credential{origin: origin, header: map[string][]string{}}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") {
		var set map[string]string
		if err := json.Unmarshal([]byte(trimmed), &set); err != nil {
			return nil, errors.New("mcp: static credential header set is not a JSON object of strings")
		}
		if len(set) == 0 {
			return nil, errors.New("mcp: static credential header set is empty")
		}
		for name, v := range set {
			if name == "" || strings.ContainsAny(name, " \t\r\n:") || v == "" || strings.ContainsAny(v, "\r\n") {
				return nil, errors.New("mcp: static credential header set holds an unusable header")
			}
			c.header[name] = append(c.header[name], v)
			c.values = append(c.values, v)
			// A value of the form <scheme> <credentials> also carries the
			// credentials alone, which a server may echo without the scheme.
			if _, creds, ok := strings.Cut(v, " "); ok && strings.TrimSpace(creds) != "" {
				c.values = append(c.values, strings.TrimSpace(creds))
			}
		}
	} else {
		if strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("mcp: static credential holds a line break")
		}
		c.header["Authorization"] = []string{"Bearer " + value}
		c.values = append(c.values, value, "Bearer "+value)
	}
	return c, nil
}

// String keeps the credential's value out of anything that formats it, a log
// line or an error included.
func (c *Credential) String() string { return "mcp.Credential(" + c.origin + ")" }

// GoString is String, for the %#v verb.
func (c *Credential) GoString() string { return c.String() }

// MarshalJSON keeps the credential's value out of a journal row or event that
// encodes it.
func (c *Credential) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"origin": c.origin})
}

// Values returns every string that carries the credential, for the scrub list.
func (c *Credential) Values() []string {
	return append([]string(nil), c.values...)
}

// HeaderFor returns the headers to attach to a request to target: the
// credential's own when target is the configured server's origin, and nil for
// any other destination, a redirect target and another server included. The
// scope is the configured server's origin (scheme, host, port); a Transport
// belongs to one server, so one origin is one server's.
func (c *Credential) HeaderFor(target string) map[string][]string {
	o, err := originOf(target)
	if err != nil || o != c.origin {
		return nil
	}
	h := make(map[string][]string, len(c.header))
	for k, v := range c.header {
		h[k] = append([]string(nil), v...)
	}
	return h
}

// originOf returns scheme, host and port of rawURL, with the scheme's default
// port made explicit so two spellings of one origin compare equal. Package mcp
// imports no network package, so the URL is read by hand: a URL with
// credentials, a backslash or a control byte is not usable.
func originOf(rawURL string) (string, error) {
	bad := fmt.Errorf("mcp: %q is not a usable server URL", rawURL)
	scheme, rest, ok := strings.Cut(rawURL, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		return "", bad
	}
	for _, r := range rawURL {
		if r <= ' ' || r == 0x7f || r == '\\' {
			return "", bad
		}
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" || strings.Contains(rest, "@") {
		return "", bad
	}
	host, port := rest, ""
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 {
			return "", bad
		}
		host, port = rest[:end+1], rest[end+1:]
		if port != "" && !strings.HasPrefix(port, ":") {
			return "", bad
		}
		port = strings.TrimPrefix(port, ":")
	} else if i := strings.LastIndex(rest, ":"); i >= 0 {
		host, port = rest[:i], rest[i+1:]
	}
	if host == "" || host == "[]" || strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "", bad
	}
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", bad
	} else {
		port = strconv.Itoa(n)
	}
	return scheme + "://" + strings.ToLower(host) + ":" + port, nil
}

// ScrubList is the engine's list of credential values. Safe for concurrent use.
type ScrubList struct {
	mu     sync.Mutex
	values map[string]bool
}

// NewScrubList returns an empty list.
func NewScrubList() *ScrubList {
	return &ScrubList{values: map[string]bool{}}
}

// Add puts values in the list, with the JSON-escaped form of each, since
// server output is often JSON text. Empty values are ignored.
func (l *ScrubList) Add(values ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range values {
		if v == "" {
			continue
		}
		l.values[v] = true
		if b, err := json.Marshal(v); err == nil {
			if esc := string(b[1 : len(b)-1]); esc != "" {
				l.values[esc] = true
			}
		}
	}
}

// AddCredential puts every value of a static credential in the list.
func (l *ScrubList) AddCredential(c *Credential) {
	if c != nil {
		l.Add(c.values...)
	}
}

// AddEnv puts every value of a local server's env in the list.
func (l *ScrubList) AddEnv(env map[string]string) {
	for _, v := range env {
		l.Add(v)
	}
}

// Scrub returns s with every listed value replaced by the scrub marker, and
// how many were replaced. Call it on a server's results, definitions, error
// text and standard error before any of it reaches a log, the journal or a
// model.
func (l *ScrubList) Scrub(s string) (string, int) {
	l.mu.Lock()
	vals := make([]string, 0, len(l.values))
	for v := range l.values {
		vals = append(vals, v)
	}
	l.mu.Unlock()
	return transport.Scrub(s, vals)
}

// ScrubBytes is Scrub over bytes.
func (l *ScrubList) ScrubBytes(b []byte) ([]byte, int) {
	s, n := l.Scrub(string(b))
	if n == 0 {
		return b, 0
	}
	return []byte(s), n
}

// ScrubCut is Scrub for output that was cut at a bound: b ends where the
// bound fell, so a listed value may run past it. Such a value no longer
// matches, so its prefix would survive Scrub. ScrubCut drops the longest
// suffix of b that begins a listed value, and moves the cut back to the start
// of any whole listed value it would split, then scrubs what is left. Output is
// only ever dropped at the cut, so no part of a listed value survives.
func (l *ScrubList) ScrubCut(b []byte) ([]byte, int) {
	l.mu.Lock()
	vals := make([]string, 0, len(l.values))
	for v := range l.values {
		vals = append(vals, v)
	}
	l.mu.Unlock()
	s := string(b)
	cut := len(s)
	for _, v := range vals {
		for n := min(len(v)-1, len(s)); n > 0 && len(s)-n < cut; n-- {
			if s[len(s)-n:] == v[:n] {
				cut = len(s) - n
				break
			}
		}
	}
	for moved := true; moved; {
		moved = false
		for _, v := range vals {
			for at := 0; at < cut; {
				i := strings.Index(s[at:], v)
				if i < 0 || at+i >= cut {
					break
				}
				if start := at + i; start+len(v) > cut {
					cut, moved = start, true
					break
				}
				at += i + 1
			}
		}
	}
	out, n := transport.Scrub(s[:cut], vals)
	return []byte(out), n
}

// ScrubStderr scrubs a local server's kept standard error; cut reports that
// the sink dropped output at its bound. It fits wire.StderrSink.SetScrub.
func (l *ScrubList) ScrubStderr(kept []byte, cut bool) []byte {
	if cut {
		out, _ := l.ScrubCut(kept)
		return out
	}
	out, _ := l.ScrubBytes(kept)
	return out
}

// Origin returns the configured origin the credential is scoped to.
func (c *Credential) Origin() string { return c.origin }
