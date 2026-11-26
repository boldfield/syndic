// Package jcs produces the JSON Canonicalization Scheme's bytes (RFC 8785)
// for a JSON text and refuses a text with a duplicate object member name.
//
// It parses for itself because encoding/json silently keeps the last of two
// duplicate members. The parser keeps an explicit stack and is bounded by a
// caller-given nesting limit, so no input makes it recurse without limit.
package jcs

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// DefaultMaxDepth is the nesting limit Canonicalize and Equal apply.
const DefaultMaxDepth = 64

// Errors returned, wrapped with a byte offset, by Canonicalize.
var (
	ErrSyntax          = errors.New("jcs: not a single JSON value")
	ErrDuplicateMember = errors.New("jcs: duplicate object member name")
	ErrNumber          = errors.New("jcs: number not finite or not representable")
	ErrUTF8            = errors.New("jcs: invalid UTF-8")
	ErrSurrogate       = errors.New("jcs: lone surrogate escape")
	ErrDepth           = errors.New("jcs: nesting too deep")
)

// Canonicalize returns the RFC 8785 bytes of the JSON value in text, with
// nesting limited to DefaultMaxDepth.
func Canonicalize(text []byte) ([]byte, error) {
	return CanonicalizeLimit(text, DefaultMaxDepth)
}

// Equal reports whether a and b have the same canonical bytes. Either text
// being refused makes them unequal.
func Equal(a, b []byte) bool {
	ca, err := Canonicalize(a)
	if err != nil {
		return false
	}
	cb, err := Canonicalize(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ca, cb)
}

type member struct {
	name  []uint16
	value []byte
}

type frame struct {
	object  bool
	members []member
	seen    map[string]struct{}
	items   [][]byte
	key     string
	keyU16  []uint16
}

type parser struct {
	text []byte
	pos  int
}

func (p *parser) errf(base error, format string, args ...any) error {
	return fmt.Errorf("%w at offset %d: %s", base, p.pos, fmt.Sprintf(format, args...))
}

func (p *parser) skipWS() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

// CanonicalizeLimit is Canonicalize with a caller-chosen nesting limit. A
// top-level array or object is at depth 1; a text nested deeper than
// maxDepth is refused.
func CanonicalizeLimit(text []byte, maxDepth int) ([]byte, error) {
	if !utf8.Valid(text) {
		return nil, fmt.Errorf("%w", ErrUTF8)
	}
	p := &parser{text: text}
	var stack []*frame
	var out []byte

	for {
		// Expect a value.
		p.skipWS()
		if p.pos >= len(text) {
			return nil, p.errf(ErrSyntax, "unexpected end of text")
		}
		var val []byte
		var haveVal bool
		switch c := text[p.pos]; c {
		case '{', '[':
			if len(stack) >= maxDepth {
				return nil, p.errf(ErrDepth, "limit %d", maxDepth)
			}
			p.pos++
			f := &frame{object: c == '{'}
			if f.object {
				f.seen = map[string]struct{}{}
			}
			stack = append(stack, f)
			p.skipWS()
			closer := byte(']')
			if f.object {
				closer = '}'
			}
			if p.pos < len(text) && text[p.pos] == closer {
				p.pos++
				val = closeFrame(f)
				stack = stack[:len(stack)-1]
				haveVal = true
			} else if f.object {
				if err := p.readKey(f); err != nil {
					return nil, err
				}
			}
		case '"':
			s, err := p.readString()
			if err != nil {
				return nil, err
			}
			val = appendString(nil, s)
			haveVal = true
		case 't', 'f', 'n':
			lit := map[byte]string{'t': "true", 'f': "false", 'n': "null"}[c]
			if !bytes.HasPrefix(text[p.pos:], []byte(lit)) {
				return nil, p.errf(ErrSyntax, "invalid literal")
			}
			p.pos += len(lit)
			val = []byte(lit)
			haveVal = true
		default:
			var err error
			val, err = p.readNumber()
			if err != nil {
				return nil, err
			}
			haveVal = true
		}
		if !haveVal {
			continue
		}

		// A value is complete; attach it, closing containers as needed.
		for {
			if len(stack) == 0 {
				out = val
				p.skipWS()
				if p.pos != len(text) {
					return nil, p.errf(ErrSyntax, "trailing data after value")
				}
				return out, nil
			}
			f := stack[len(stack)-1]
			if f.object {
				f.members = append(f.members, member{name: f.keyU16, value: val})
			} else {
				f.items = append(f.items, val)
			}
			p.skipWS()
			if p.pos >= len(text) {
				return nil, p.errf(ErrSyntax, "unexpected end of text")
			}
			c := text[p.pos]
			closer := byte(']')
			if f.object {
				closer = '}'
			}
			if c == ',' {
				p.pos++
				if f.object {
					p.skipWS()
					if err := p.readKey(f); err != nil {
						return nil, err
					}
				}
				break // expect the next value
			}
			if c != closer {
				return nil, p.errf(ErrSyntax, "expected ',' or '%c'", closer)
			}
			p.pos++
			val = closeFrame(f)
			stack = stack[:len(stack)-1]
		}
	}
}

// readKey reads `"name" :` into f, refusing a duplicate name.
func (p *parser) readKey(f *frame) error {
	if p.pos >= len(p.text) || p.text[p.pos] != '"' {
		return p.errf(ErrSyntax, "expected member name")
	}
	start := p.pos
	name, err := p.readString()
	if err != nil {
		return err
	}
	if _, dup := f.seen[name]; dup {
		p.pos = start
		return p.errf(ErrDuplicateMember, "%q", name)
	}
	f.seen[name] = struct{}{}
	f.key = name
	f.keyU16 = utf16.Encode([]rune(name))
	p.skipWS()
	if p.pos >= len(p.text) || p.text[p.pos] != ':' {
		return p.errf(ErrSyntax, "expected ':'")
	}
	p.pos++
	return nil
}

func closeFrame(f *frame) []byte {
	var b []byte
	if !f.object {
		b = append(b, '[')
		for i, it := range f.items {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, it...)
		}
		return append(b, ']')
	}
	sort.Slice(f.members, func(i, j int) bool {
		return lessUTF16(f.members[i].name, f.members[j].name)
	})
	b = append(b, '{')
	for i, m := range f.members {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendString(b, string(utf16.Decode(m.name)))
		b = append(b, ':')
		b = append(b, m.value...)
	}
	return append(b, '}')
}

func lessUTF16(a, b []uint16) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func (p *parser) readHex4() (rune, error) {
	if p.pos+4 > len(p.text) {
		return 0, p.errf(ErrSyntax, "short \\u escape")
	}
	var r rune
	for i := 0; i < 4; i++ {
		h := hexVal(p.text[p.pos+i])
		if h < 0 {
			return 0, p.errf(ErrSyntax, "bad \\u escape")
		}
		r = r<<4 | rune(h)
	}
	p.pos += 4
	return r, nil
}

// readString reads a JSON string at p.pos and returns its unescaped value.
func (p *parser) readString() (string, error) {
	p.pos++ // opening quote
	var b []byte
	for {
		if p.pos >= len(p.text) {
			return "", p.errf(ErrSyntax, "unterminated string")
		}
		c := p.text[p.pos]
		switch {
		case c == '"':
			p.pos++
			return string(b), nil
		case c < 0x20:
			return "", p.errf(ErrSyntax, "unescaped control character")
		case c != '\\':
			b = append(b, c)
			p.pos++
		default:
			p.pos++
			if p.pos >= len(p.text) {
				return "", p.errf(ErrSyntax, "unterminated escape")
			}
			e := p.text[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				b = append(b, e)
			case 'b':
				b = append(b, '\b')
			case 'f':
				b = append(b, '\f')
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'u':
				escStart := p.pos - 2
				r, err := p.readHex4()
				if err != nil {
					return "", err
				}
				switch {
				case r >= 0xDC00 && r <= 0xDFFF:
					p.pos = escStart
					return "", p.errf(ErrSurrogate, "low surrogate without high")
				case r >= 0xD800 && r <= 0xDBFF:
					if p.pos+2 > len(p.text) || p.text[p.pos] != '\\' || p.text[p.pos+1] != 'u' {
						p.pos = escStart
						return "", p.errf(ErrSurrogate, "high surrogate without low")
					}
					p.pos += 2
					lo, err := p.readHex4()
					if err != nil {
						return "", err
					}
					if lo < 0xDC00 || lo > 0xDFFF {
						p.pos = escStart
						return "", p.errf(ErrSurrogate, "high surrogate without low")
					}
					r = utf16.DecodeRune(r, lo)
				}
				b = utf8.AppendRune(b, r)
			default:
				return "", p.errf(ErrSyntax, "invalid escape")
			}
		}
	}
}

// appendString appends s as an RFC 8785 string literal.
func appendString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\t':
			b = append(b, '\\', 't')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

// readNumber reads a JSON number and returns its ECMAScript serialisation.
func (p *parser) readNumber() ([]byte, error) {
	t := p.text
	start := p.pos
	i := start
	if i < len(t) && t[i] == '-' {
		i++
	}
	digits := func() int {
		n := 0
		for i < len(t) && t[i] >= '0' && t[i] <= '9' {
			i++
			n++
		}
		return n
	}
	switch {
	case i < len(t) && t[i] == '0':
		i++
	case i < len(t) && t[i] >= '1' && t[i] <= '9':
		digits()
	default:
		return nil, p.errf(ErrSyntax, "unexpected character")
	}
	if i < len(t) && t[i] == '.' {
		i++
		if digits() == 0 {
			return nil, p.errf(ErrSyntax, "missing fraction digits")
		}
	}
	if i < len(t) && (t[i] == 'e' || t[i] == 'E') {
		i++
		if i < len(t) && (t[i] == '+' || t[i] == '-') {
			i++
		}
		if digits() == 0 {
			return nil, p.errf(ErrSyntax, "missing exponent digits")
		}
	}
	lit := string(t[start:i])
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, p.errf(ErrNumber, "%s overflows a double", lit)
	}
	if f == 0 && hasNonZeroDigit(lit) {
		return nil, p.errf(ErrNumber, "%s underflows a double", lit)
	}
	p.pos = i
	return appendNumber(nil, f), nil
}

// hasNonZeroDigit reports whether the mantissa of lit has a digit other than 0.
func hasNonZeroDigit(lit string) bool {
	for i := 0; i < len(lit); i++ {
		c := lit[i]
		if c == 'e' || c == 'E' {
			return false
		}
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}

// appendNumber appends f as ECMAScript Number::toString does (RFC 8785 3.2.2.3).
func appendNumber(b []byte, f float64) []byte {
	if f == 0 {
		return append(b, '0') // also -0
	}
	if f < 0 {
		b = append(b, '-')
		f = -f
	}
	// Shortest round-tripping digits: "d[.ddd]e±XX".
	e := strconv.AppendFloat(nil, f, 'e', -1, 64)
	x := bytes.IndexByte(e, 'e')
	exp, _ := strconv.Atoi(string(e[x+1:]))
	digs := bytes.Replace(e[:x], []byte("."), nil, 1)
	k := len(digs)
	n := exp + 1
	switch {
	case k <= n && n <= 21:
		b = append(b, digs...)
		b = append(b, bytes.Repeat([]byte{'0'}, n-k)...)
	case 0 < n && n <= 21:
		b = append(b, digs[:n]...)
		b = append(b, '.')
		b = append(b, digs[n:]...)
	case -6 < n && n <= 0:
		b = append(b, '0', '.')
		b = append(b, bytes.Repeat([]byte{'0'}, -n)...)
		b = append(b, digs...)
	default:
		b = append(b, digs[0])
		if k > 1 {
			b = append(b, '.')
			b = append(b, digs[1:]...)
		}
		b = append(b, 'e')
		if n-1 < 0 {
			b = append(b, '-')
		} else {
			b = append(b, '+')
		}
		b = strconv.AppendInt(b, int64(abs(n-1)), 10)
	}
	return b
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
