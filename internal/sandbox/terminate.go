package sandbox

import (
	"context"
	"syscall"
	"time"
)

// Default bounds of the termination sequence: tools.exec.kill_grace_s and
// tools.exec.cleanup_deadline_s (v7 delta, section 3, "Containment and
// termination").
const (
	DefaultKillGrace       = 5 * time.Second
	DefaultCleanupDeadline = 10 * time.Second
)

// Terminable is the part of a Process the termination sequence drives. Every
// Process is one; the supervisor drives its own containment through it too.
type Terminable interface {
	// Terminate signals everything contained with sig, without waiting.
	Terminate(sig syscall.Signal) error
	// ConfirmEmpty returns nil only once nothing contained remains.
	ConfirmEmpty(ctx context.Context) error
}

// Sequence is the one termination sequence of the v7 delta, section 3,
// "Containment and termination", for every cause: shell exit, timeout,
// cancellation, any terminal turn transition and engine stop.
type Sequence struct {
	// KillGrace is the wait between SIGTERM and SIGKILL,
	// tools.exec.kill_grace_s.
	KillGrace time.Duration
	// CleanupDeadline bounds the wait for confirmation after SIGKILL,
	// tools.exec.cleanup_deadline_s.
	CleanupDeadline time.Duration
}

// Run terminates p and reports whether its containment was confirmed empty.
// A false result is the design's "containment unverified": a value for the
// caller to act on, not an error to retry.
//
// revoke, when not nil, is called first, at the moment termination begins
// and before any wait, so that the caller can revoke the call's proxy
// listener and credential. Then:
//
//  1. everything contained is signalled with SIGTERM;
//  2. the sequence waits up to KillGrace for the containment to be
//     confirmed empty, and returns true without sending SIGKILL if it is.
//     When shellExited is true, the termination is for normal shell exit and
//     this wait is skipped;
//  3. what remains is signalled with SIGKILL;
//  4. the sequence waits at most CleanupDeadline for ConfirmEmpty.
//
// Run never waits beyond those bounds, even for a ConfirmEmpty that ignores
// its context: such a call is abandoned to finish on its own. Errors from
// Terminate do not change the sequence; only confirmation does.
func (s Sequence) Run(p Terminable, shellExited bool, revoke func()) (confirmed bool) {
	if revoke != nil {
		revoke()
	}
	_ = p.Terminate(syscall.SIGTERM)
	if !shellExited && s.KillGrace > 0 && confirmedWithin(p, s.KillGrace) {
		return true
	}
	_ = p.Terminate(syscall.SIGKILL)
	return confirmedWithin(p, s.CleanupDeadline)
}

// confirmedWithin reports whether p confirms its containment empty within d.
// ConfirmEmpty runs on its own goroutine, so that one that does not honour
// its context cannot hold the caller past d.
func confirmedWithin(p Terminable, d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.ConfirmEmpty(ctx) }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case err := <-result:
		return err == nil
	case <-timer.C:
		// A confirmation that raced the deadline still counts.
		select {
		case err := <-result:
			return err == nil
		default:
			return false
		}
	}
}
