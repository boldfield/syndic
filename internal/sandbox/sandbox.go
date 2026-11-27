// Package sandbox defines the contract between the engine and an OS sandbox
// backend for the exec tool (v7 delta, section 3): the capability set a backend
// enforces, what one command needs, and the process handle the engine drives
// through the termination sequence. It holds no backend; callers pass plain
// values and the package imports nothing from the engine, the journal or the
// configuration package.
package sandbox

import (
	"context"
	"io"
	"strings"
	"syscall"
	"time"
)

// Capability is one enforcement property of the capability contract
// (v7 delta, section 3, "Capability contract").
type Capability uint8

const (
	// F, filesystem: read, write and execute in the scratch and per-call
	// temporary directories; read and execute only in the system locations;
	// no access to any other path.
	F Capability = 1 << iota
	// N1, no network: no connection of any family and no name resolution,
	// the host's loopback included.
	N1
	// N2, proxy only: as N1 except connections to the call's one proxy
	// listener (section 4).
	N2
	// P, process: every descendant is contained, terminable and cannot
	// survive the call, signal or inspect anything outside its containment,
	// or gain privileges.
	P
	// D, descriptors: standard input on /dev/null, standard output and error
	// on the one pipe, and no other descriptor of the engine.
	D
)

var capabilityNames = []struct {
	c    Capability
	name string
}{
	// Sorted by name, so that Names is sorted.
	{D, "D"},
	{F, "F"},
	{N1, "N1"},
	{N2, "N2"},
	{P, "P"},
}

// CapSet is a set of capabilities.
type CapSet uint8

// Caps builds a set from the given capabilities.
func Caps(cs ...Capability) CapSet {
	var s CapSet
	for _, c := range cs {
		s |= CapSet(c)
	}
	return s
}

// Has reports whether every given capability is in the set.
func (s CapSet) Has(cs ...Capability) bool {
	want := Caps(cs...)
	return s&want == want
}

// Without returns the set less the given capabilities.
func (s CapSet) Without(cs ...Capability) CapSet {
	return s &^ Caps(cs...)
}

// Names returns the names of the capabilities in the set, sorted.
func (s CapSet) Names() []string {
	names := []string{}
	for _, cn := range capabilityNames {
		if s&CapSet(cn.c) != 0 {
			names = append(names, cn.name)
		}
	}
	return names
}

// String renders the set as its sorted names joined by commas.
func (s CapSet) String() string {
	return strings.Join(s.Names(), ",")
}

// Required returns the capabilities a backend must enforce to be supported:
// F, P, D and N1 always, and N2 only when a network list is in force
// (v7 delta, section 3, "Capability contract"; section 4).
func Required(networkList bool) CapSet {
	req := Caps(F, P, D, N1)
	if networkList {
		req |= CapSet(N2)
	}
	return req
}

// Supported reports whether a backend enforcing the given set is supported,
// and returns the required capabilities it lacks.
func Supported(enforced CapSet, networkList bool) (bool, CapSet) {
	missing := Required(networkList) &^ enforced
	return missing == 0, missing
}

// Listener is the one proxy listener a command may connect to under N2
// (v7 delta, section 4, "Per-call listener").
type Listener struct {
	// Network is "unix" or "tcp".
	Network string
	// Address is the socket path or host:port of the listener.
	Address string
}

// SystemLocations is the closed list of locations outside the scratch and
// temporary directories that a command may read and execute but not write
// (v7 delta, section 3, "System locations, a closed list").
type SystemLocations struct {
	// Path holds the directories of tools.exec.path, in order.
	Path []string
	// ReadOnlyPaths holds tools.exec.read_only_paths, in order.
	ReadOnlyPaths []string
	// Devices holds the device nodes a command may use; see DeviceNodes.
	Devices []string
}

// DeviceNodes are the device nodes the system locations always include
// (v7 delta, section 3, "System locations").
var DeviceNodes = []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom"}

// Spec is everything one command needs from its backend.
type Spec struct {
	// Argv is the argument vector; Argv[0] is the shell.
	Argv []string
	// ScratchDir is the caller's scratch directory and the command's working
	// directory (sections 2 and 5).
	ScratchDir string
	// TempDir is the per-call temporary directory, fresh and empty. The
	// sandbox allows it and not its parent (section 5).
	TempDir string
	// Env is the complete environment, as built by Env (section 3,
	// "Environment"). The backend passes exactly this and nothing else.
	Env []string
	// System is the closed list of read-only locations.
	System SystemLocations
	// Timeout is the effective timeout of the call (section 2, "Bounds"). The
	// engine runs the termination sequence at the bound; a backend may use it
	// only as an outer limit for its own supervisor.
	Timeout time.Duration
	// Proxy is the listener to permit under N2, or nil for none, in which
	// case N1 holds.
	Proxy *Listener
	// ServerPipes, set for a local MCP server (v9 delta, section 6,
	// "Descriptors"), makes standard input and standard output two separate
	// pipes to the engine and standard error a third, in place of a command's
	// /dev/null input and one merged output pipe.
	ServerPipes bool
}

// Backend is a sandbox mechanism on a platform at a stated configuration
// (v7 delta, section 3, "Capability contract" and "Backends").
type Backend interface {
	// Name names the backend for the run manifest (section 3, "Manifest").
	Name() string
	// Config is the backend's configuration as recorded in the run manifest
	// (section 3, "Manifest"), including any minimum kernel or library
	// version it relies on.
	Config() map[string]string
	// Capabilities returns the capabilities the backend enforces on this
	// host. When the set is not supported (see Supported), reason says why,
	// for example a kernel below the backend's stated minimum. A backend
	// never reports a capability it cannot enforce here (section 3,
	// "Capability contract").
	Capabilities() (enforced CapSet, reason string)
	// Start starts the command under the sandbox. Standard input is
	// /dev/null, standard output and error are one pipe, no terminal is
	// allocated and no other descriptor is inherited (sections 2 and 3, D).
	// Every process the command starts is contained until ConfirmEmpty
	// returns nil (section 3, P).
	Start(ctx context.Context, spec Spec) (Process, error)
}

// Process is one running command under a backend.
type Process interface {
	// Output is the one pipe carrying standard output and standard error
	// together, in the order written (section 2, "Invocation"). It reaches
	// end of file only when every holder of its write end has closed it;
	// the engine may Close it itself after the one-second cutoff (section 2,
	// "Output").
	Output() io.ReadCloser
	// Wait blocks until the shell exits and returns its status. It does not
	// wait for descendants or for the pipe (section 2, "Result").
	Wait() (ExitStatus, error)
	// Terminate signals everything contained with sig, without waiting. The
	// engine calls it with SIGTERM and then SIGKILL; on normal shell exit it
	// skips the grace wait between them (section 3, "Containment and
	// termination"). Calling it after everything has ended is not an error.
	Terminate(sig syscall.Signal) error
	// ConfirmEmpty returns nil only when the backend can state that nothing
	// contained remains, and returns the context's error if the context ends
	// first. The engine bounds ctx by tools.exec.cleanup_deadline_s and
	// delivers a result only after nil (section 3, "Containment and
	// termination", "Containment unverified").
	ConfirmEmpty(ctx context.Context) error
}

// ExitStatus is how the shell ended (section 2, "Result").
type ExitStatus struct {
	// Code is the exit code; 128 plus Signal when the shell ended by a
	// signal.
	Code int
	// Signal is the number of the signal that ended the shell, or 0.
	Signal int
	// Terminated is true when the shell ended after the engine began
	// termination through Terminate.
	Terminated bool
}

// SignalStatus returns the status of a shell ended by sig.
func SignalStatus(sig syscall.Signal, terminated bool) ExitStatus {
	return ExitStatus{Code: 128 + int(sig), Signal: int(sig), Terminated: terminated}
}
