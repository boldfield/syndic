package command

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// CaptureCutoff is how long the pipe is read after the command has exited or
// been terminated, before it is closed (v7 delta, section 2, "Output").
const CaptureCutoff = time.Second

// captureCutoff is CaptureCutoff; tests shorten it.
var captureCutoff = CaptureCutoff

// readChunk is the size of one read from the pipe.
const readChunk = 32 << 10

// replacementLen is the length of U+FFFD in UTF-8, the rendering of every
// byte that is not part of a valid sequence.
const replacementLen = len(string(utf8.RuneError))

// Output is the captured output of one command (v7 delta, section 2,
// "Output").
type Output struct {
	// Text is the rendering of Kept: the kept bytes decoded as UTF-8 with
	// every byte that is not part of a valid sequence replaced by U+FFFD.
	Text string
	// Kept is the longest prefix of the bytes read whose rendering is at
	// most the limit, for the caller to store under SHA256.
	Kept []byte
	// SHA256 is the lowercase hex SHA-256 of Kept.
	SHA256 string
	// BytesTotal is the number of bytes read before the pipe was closed.
	BytesTotal int64
	// Truncated is true when BytesTotal differs from len(Kept).
	Truncated bool
	// Complete is true only when the pipe was read to end of file.
	Complete bool
}

// Render decodes b as UTF-8, replacing every byte that is not part of a
// valid sequence by U+FFFD, one replacement per byte.
func Render(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.Write(b[:size])
		}
		b = b[size:]
	}
	return sb.String()
}

// keeper holds the longest prefix of a stream whose rendering is at most
// limit, and never more than limit+utf8.UTFMax bytes of it.
//
// The rendering of a prefix is the rendering of its complete units (a valid
// sequence, or one invalid byte as U+FFFD) followed by U+FFFD for each byte
// of a trailing sequence the prefix cuts. The first part never shrinks as the
// prefix grows; the second can, when the cut sequence completes. So the
// keeper decodes complete units from the boundary b, where the rendering so
// far is c, and once a complete unit would take c past the limit no longer
// prefix fits, except one cutting that unit: bytes are then discarded.
type keeper struct {
	limit int
	buf   []byte
	b     int  // end of the last complete unit
	c     int  // rendered length of buf[:b]
	best  int  // the longest fitting prefix found so far
	full  bool // no longer prefix fits; buf is buf[:best]
	peak  int  // the largest capacity buf has had
}

// write appends p to the stream.
func (k *keeper) write(p []byte) {
	for len(p) > 0 && !k.full {
		// While not full, len(buf) <= c+3 <= limit+3, so room >= 1.
		n := min(k.limit+utf8.UTFMax-len(k.buf), len(p))
		k.buf = append(k.buf, p[:n]...)
		k.peak = max(k.peak, cap(k.buf))
		p = p[n:]
		k.advance()
	}
}

// advance decodes the complete units after b and moves best on.
func (k *keeper) advance() {
	for k.b < len(k.buf) {
		rest := k.buf[k.b:]
		if !utf8.FullRune(rest) {
			break
		}
		r, size := utf8.DecodeRune(rest)
		w := size
		if r == utf8.RuneError && size == 1 {
			w = replacementLen
		}
		if k.c+w > k.limit {
			// A prefix ending inside this unit renders each of its bytes
			// there as U+FFFD; one ending after it renders more than c+w.
			k.best = max(k.best, k.b+min(size-1, (k.limit-k.c)/replacementLen))
			k.buf = k.buf[:k.best]
			k.full = true
			return
		}
		k.b += size
		k.c += w
	}
	pending := len(k.buf) - k.b
	k.best = max(k.best, k.b+min(pending, (k.limit-k.c)/replacementLen))
}

// kept returns the longest fitting prefix.
func (k *keeper) kept() []byte {
	return k.buf[:k.best]
}

// capture reads a command's pipe on its own goroutine until end of file, an
// error or stop, keeping what fits and counting the rest.
type capture struct {
	pipe io.ReadCloser
	done chan struct{} // closed when the reader has ended by itself

	mu      sync.Mutex
	k       keeper
	total   int64
	eof     bool
	stopped bool // the pipe was closed here; later reads are not counted
}

// startCapture starts reading pipe, keeping a prefix rendered within limit.
func startCapture(pipe io.ReadCloser, limit int) *capture {
	c := &capture{pipe: pipe, done: make(chan struct{}), k: keeper{limit: limit}}
	go c.read()
	return c
}

func (c *capture) read() {
	defer close(c.done)
	buf := make([]byte, readChunk)
	for {
		n, err := c.pipe.Read(buf)
		c.mu.Lock()
		if c.stopped {
			c.mu.Unlock()
			return
		}
		c.total += int64(n)
		c.k.write(buf[:n])
		if err != nil {
			c.eof = errors.Is(err, io.EOF)
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}
}

// stop closes the pipe unless the reader has already ended. Bytes the reader
// obtains after stop are neither kept nor counted.
func (c *capture) stop() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	_ = c.pipe.Close()
}

// finish waits at most cutoff for the reader to reach end of file, then
// stops it and returns the output. It is called once the command has exited
// or been terminated.
func (c *capture) finish(cutoff time.Duration) Output {
	t := time.NewTimer(cutoff)
	defer t.Stop()
	select {
	case <-c.done:
		_ = c.pipe.Close()
	case <-t.C:
		c.stop()
	}
	return c.output()
}

// output returns what has been captured.
func (c *capture) output() Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := append([]byte{}, c.k.kept()...)
	sum := sha256.Sum256(kept)
	return Output{
		Text:       Render(kept),
		Kept:       kept,
		SHA256:     hex.EncodeToString(sum[:]),
		BytesTotal: c.total,
		Truncated:  c.total != int64(len(kept)),
		Complete:   c.eof,
	}
}

// peak returns the largest number of bytes the keeper has held.
func (c *capture) peak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.k.peak
}
