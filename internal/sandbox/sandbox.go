// Package sandbox is the contract between the exec tool and an OS sandbox
// backend: the capabilities a backend enforces, the interface it implements,
// the policy object recorded in the manifest, and the fixed environment a
// command receives. No real mechanism lives here.
//
// Design reference: docs/design/v7-delta-exec-tool.md, section 3 and the
// "Sandbox" paragraph of section 9. The package imports nothing from the
// engine, the journal or the configuration package; callers pass plain values.
package sandbox

import (
	"context"
	"io"
	"sort"
	"time"
)

// Capability is one of the properties of section 3's capability contract.
type Capability string

const (
	// F is filesystem confinement: read/write/execute in the scratch and
	// per-call temporary directories, read/execute only in the system
	// locations, nothing else.
	F Capability = "F"
	// N1 is no network: no connection of any family, no name resolution,
	// the host's loopback included.
	N1 Capability = "N1"
	// N2 is proxy only: as N1, except connections to the one proxy listener
	// made for this call (section 4).
	N2 Capability = "N2"
	// P is process containment: the command and every descendant can be
	// terminated, none survives the call, none can signal outside.
	P Capability = "P"
	// D is descriptors: stdin on /dev/null, stdout and stderr on the one
	// pipe, no other descriptor of the engine.
	D Capability = "D"
)

// CapSet is a set of capabilities.
type CapSet map[Capability]struct{}

// NewCapSet returns a set holding the given capabilities.
func NewCapSet(caps ...Capability) CapSet {
	s := CapSet{}
	for _, c := range caps {
		s[c] = struct{}{}
	}
	return s
}

// Has reports whether c is in the set.
func (s CapSet) Has(c Capability) bool {
	_, ok := s[c]
	return ok
}

// Sorted returns the members in lexical order.
func (s CapSet) Sorted() []Capability {
	out := make([]Capability, 0, len(s))
	for c := range s {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Missing returns, in lexical order, the capabilities a backend must enforce
// and does not. F, P, D and N1 are always required; N2 only when a network
// list is in force (section 3, "Capability contract").
func Missing(enforced CapSet, networkListInForce bool) []Capability {
	required := []Capability{D, F, N1, P}
	if networkListInForce {
		required = []Capability{D, F, N1, N2, P}
	}
	var missing []Capability
	for _, c := range required {
		if !enforced.Has(c) {
			missing = append(missing, c)
		}
	}
	return missing
}

// Supported reports whether a backend enforcing the given set may run
// commands under sandbox = "required". The engine never runs with a weaker
// set (section 3).
func Supported(enforced CapSet, networkListInForce bool) bool {
	return len(Missing(enforced, networkListInForce)) == 0
}

// ProxyListener is the one per-call proxy listener a command may reach (N2,
// section 4).
type ProxyListener struct {
	// Network and Address locate the listener ("unix" and a socket path, or
	// "tcp" and a loopback host:port).
	Network string
	Address string
	// URL is the proxy URL, credential included, carried by the proxy
	// environment variables.
	URL string
}

// Spec is what one command needs from a backend.
type Spec struct {
	// Argv is the argument vector, the shell invocation included.
	Argv []string
	// Scratch is the caller's scratch directory: read, write, execute (F).
	Scratch string
	// Tmp is the per-call temporary directory: read, write, execute (F). It
	// is also the command's HOME.
	Tmp string
	// Env is the command's complete environment, KEY=VALUE, from BuildEnv.
	Env []string
	// SystemLocations are the closed list of read-and-execute locations: the
	// directories of tools.exec.path and tools.exec.read_only_paths (F).
	SystemLocations []string
	// Timeout is the bound of section 2. The engine enforces it; a backend
	// may use it as information only.
	Timeout time.Duration
	// Proxy, when non-nil, is the one listener the command may connect to
	// (N2). Nil means no network at all (N1).
	Proxy *ProxyListener
}

// Termination selects the step of the termination sequence (section 3,
// "Containment and termination").
type Termination int

const (
	// Graceful signals everything contained with SIGTERM.
	Graceful Termination = iota
	// Forced signals everything that remains with SIGKILL.
	Forced
)

func (t Termination) String() string {
	if t == Forced {
		return "forced"
	}
	return "graceful"
}

// ExitStatus is how the shell ended.
type ExitStatus struct {
	// Code is the shell's exit code; meaningful when Signal is zero.
	Code int
	// Signal is the signal number when the shell ended by a signal, else 0.
	Signal int
	// Terminated is true when the engine terminated the command.
	Terminated bool
}

// Report is what a backend enforces on this host.
type Report struct {
	// Enforced is the set of capabilities enforced.
	Enforced CapSet
	// Reason says why the backend is unsupported (kernel or library below
	// its stated minimum, a missing mechanism). Empty when supported.
	Reason string
}

// Backend is a mechanism on a platform at a stated configuration (section 3).
type Backend interface {
	// Name is the backend's name, recorded in the manifest.
	Name() string
	// Config is the backend's configuration, recorded in the manifest with
	// the name. Keys and values are plain strings.
	Config() map[string]string
	// Capabilities states what the backend enforces on this host. A backend
	// states the minimum kernel or library version it needs and reports an
	// empty or weaker set, with the Reason, below it (section 3).
	Capabilities() Report
	// Start launches Spec.Argv contained by the backend: standard input on
	// /dev/null, standard output and error on one pipe, no other engine
	// descriptor (D). It must not return a Process that is not contained. If
	// ctx ends before Start returns, no process is left running.
	Start(ctx context.Context, spec Spec) (Process, error)
}

// Process is one started, contained command.
type Process interface {
	// Output is the single pipe carrying standard output and standard error
	// together. It reaches EOF only when every holder of the write end,
	// descendants included, is gone.
	Output() io.Reader
	// Wait blocks until the shell ends and returns its exit status. A
	// descendant may outlive the shell; Wait does not wait for it.
	Wait() (ExitStatus, error)
	// Terminate begins termination of everything contained, the shell and
	// every descendant including ones that started a new session or
	// detached (P). Graceful sends SIGTERM, Forced SIGKILL. It does not
	// block and is safe to call more than once and after exit.
	Terminate(Termination)
	// ConfirmEmpty returns nil only when the backend can state that nothing
	// contained remains, or the context's error when ctx ends first. A
	// result is delivered only after it returns nil (section 3,
	// "Containment and termination").
	ConfirmEmpty(ctx context.Context) error
}
