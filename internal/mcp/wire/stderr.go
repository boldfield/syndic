package wire

import "sync"

// StderrSink keeps the first bytes of a local server's standard error, up to
// its bound, and drops the rest with a count. Write never fails, so whatever
// copies the server's standard error into it keeps draining the pipe. It is
// safe for concurrent use.
type StderrSink struct {
	mu      sync.Mutex
	max     int
	kept    []byte
	dropped int64
	scrub   func([]byte) []byte
	tail    func([]byte) int
}

// NewStderrSink returns a sink that keeps at most limit bytes; a limit of zero
// or less takes mcp.max_stderr_bytes.
func NewStderrSink(limit int) *StderrSink {
	if limit <= 0 {
		limit = MaxStderrBytes
	}
	return &StderrSink{max: limit}
}

// Write keeps what fits under the bound and counts the rest as dropped.
func (s *StderrSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	room := s.max - len(s.kept)
	if room > len(p) {
		room = len(p)
	}
	s.kept = append(s.kept, p[:room]...)
	s.dropped += int64(len(p) - room)
	return len(p), nil
}

// SetScrub sets a function applied to what Bytes returns, so credential values
// in a server's standard error do not leave the sink.
func (s *StderrSink) SetScrub(f func([]byte) []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scrub = f
}

// SetTailGuard sets a function that reports how many trailing bytes of its
// argument begin a value the scrub would remove. When output was dropped at the
// bound, Bytes cuts that many bytes, so a value split by the bound leaves no
// prefix behind.
func (s *StderrSink) SetTailGuard(f func([]byte) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = f
}

// Bytes returns a copy of what was kept, scrubbed when a scrub is set.
func (s *StderrSink) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]byte(nil), s.kept...)
	if s.tail != nil && s.dropped > 0 {
		out = out[:len(out)-s.tail(out)]
	}
	if s.scrub != nil {
		return s.scrub(out)
	}
	return out
}

// Dropped returns how many bytes were dropped past the bound.
func (s *StderrSink) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}
