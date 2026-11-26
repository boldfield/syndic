package command

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand"
	"testing"
	"time"
)

// longestFitting is the definition of the kept prefix, by brute force: the
// longest prefix of b whose rendering is at most limit.
func longestFitting(b []byte, limit int) int {
	for n := len(b); n > 0; n-- {
		if len(Render(b[:n])) <= limit {
			return n
		}
	}
	return 0
}

func TestRender(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"€", "€"},
		{"a\xffb", "a\uFFFDb"},
		{"\xe2\x82", "\uFFFD\uFFFD"},           // a cut sequence: one replacement per byte
		{"\xe2\x82A", "\uFFFD\uFFFDA"},         // an interrupted sequence
		{"\xed\xa0\x80", "\uFFFD\uFFFD\uFFFD"}, // a surrogate is not valid
		{"\uFFFD", "\uFFFD"},                   // a literal U+FFFD is kept
	} {
		if got := Render([]byte(tc.in)); got != tc.want {
			t.Errorf("Render(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestKeeperMatchesDefinition feeds random streams, in random chunks, and
// compares the kept prefix with the brute-force definition.
func TestKeeperMatchesDefinition(t *testing.T) {
	alphabet := [][]byte{
		[]byte("a"), []byte("é"), []byte("€"), []byte("😀"),
		{0xff}, {0xe2}, {0xe2, 0x82}, {0xf0, 0x9f}, {0x80}, {0xc0}, {0xed, 0xa0},
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		var stream []byte
		for n := rng.Intn(20); n > 0; n-- {
			stream = append(stream, alphabet[rng.Intn(len(alphabet))]...)
		}
		limit := rng.Intn(30)
		k := keeper{limit: limit}
		rest := stream
		for len(rest) > 0 {
			n := 1 + rng.Intn(len(rest))
			k.write(rest[:n])
			rest = rest[n:]
		}
		want := longestFitting(stream, limit)
		if got := k.kept(); !bytes.Equal(got, stream[:want]) {
			t.Fatalf("stream %x, limit %d: kept %x, want %x", stream, limit, got, stream[:want])
		}
		if len(Render(k.kept())) > limit {
			t.Fatalf("stream %x, limit %d: rendering longer than the limit", stream, limit)
		}
	}
}

func TestKeeperCases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
		limit  int
		kept   string
	}{
		{"under the limit", "abc", 10, "abc"},
		{"at the limit", "abcde", 5, "abcde"},
		{"over the limit", "abcdef", 5, "abcde"},
		{"invalid bytes render longer", "\xff\xff\xff\xff", 10, "\xff\xff\xff"},
		{"a cut splits a sequence", "ab😀", 5, "ab\xf0"},
		// The prefix cutting € renders longer than the whole of €.
		{"a completed sequence fits again", "\xe2\x82\xac", 3, "\xe2\x82\xac"},
		{"a four-byte sequence cut after one byte", "😀", 3, "\xf0"},
		{"zero limit", "abc", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Byte by byte and all at once give the same prefix.
			for _, chunk := range []int{1, len(tc.stream)} {
				k := keeper{limit: tc.limit}
				for s := []byte(tc.stream); len(s) > 0; {
					n := min(chunk, len(s))
					k.write(s[:n])
					s = s[n:]
				}
				if got := string(k.kept()); got != tc.kept {
					t.Errorf("chunk %d: kept %q, want %q", chunk, got, tc.kept)
				}
			}
		})
	}
}

// generator yields n bytes of a repeating pattern without holding them.
type generator struct {
	pattern []byte
	left    int64
	off     int
}

func (g *generator) Read(p []byte) (int, error) {
	if g.left == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), g.left))
	for i := 0; i < n; {
		c := copy(p[i:n], g.pattern[g.off:])
		g.off = (g.off + c) % len(g.pattern)
		i += c
	}
	g.left -= int64(n)
	return n, nil
}

func (g *generator) Close() error { return nil }

// TestCaptureGigabyte reads a gigabyte, mixing valid and invalid UTF-8,
// without holding more than a small multiple of the limit.
func TestCaptureGigabyte(t *testing.T) {
	const total = 1 << 30
	limit := DefaultMaxOutputBytes
	c := startCapture(&generator{pattern: []byte("line €\xff\n"), left: total}, limit)
	select {
	case <-c.done:
	case <-time.After(2 * time.Minute):
		t.Fatal("capture blocked")
	}
	out := c.finish(captureCutoff)
	if !out.Complete || !out.Truncated || out.BytesTotal != total {
		t.Fatalf("complete %v truncated %v total %d", out.Complete, out.Truncated, out.BytesTotal)
	}
	if len(out.Text) > limit {
		t.Errorf("rendering %d bytes, limit %d", len(out.Text), limit)
	}
	if peak := c.peak(); peak > 2*(limit+4) {
		t.Errorf("held %d bytes, limit %d", peak, limit)
	}
	sum := sha256.Sum256(out.Kept)
	if out.SHA256 != hex.EncodeToString(sum[:]) {
		t.Error("output_sha256 does not match the kept bytes")
	}
}
