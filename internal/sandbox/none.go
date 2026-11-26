package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

// NoneName is the unsandboxed backend's name in the run manifest.
const NoneName = "none"

// NoneNotice is the line the participants reply, the engine's start log and
// the run manifest carry under sandbox = "none" (v7 delta, section 3, "The
// opt-out").
const NoneNotice = "exec: sandbox none; filesystem and network confinement are unenforced"

// containProcessGroup names the process-group container.
const containProcessGroup = "process_group"

func init() {
	containers[containProcessGroup] = func() Container { return &processGroup{} }
}

// None is the backend for tools.exec.sandbox = "none" (v7 delta, section 3,
// "The opt-out"). A command runs as the engine's user, in the scratch
// directory, with the fixed environment, through a supervisor, in a new
// process group, and is terminated by terminating that process group.
// Nothing else is enforced. Filesystem and network confinement are
// unenforced: a command can read and write the live workspace and any other
// file the engine's user can, including protected paths and syndic's own
// state; can use the network without restriction; and can leave processes
// running after the call. Capabilities F, N1, N2 and P do not hold; D does.
type None struct {
	// Supervisor starts each command's supervisor.
	Supervisor Supervisor
}

// NewNone returns the unsandboxed backend.
func NewNone(sup Supervisor) *None {
	return &None{Supervisor: sup}
}

// Name implements Backend.
func (*None) Name() string { return NoneName }

// Config implements Backend.
func (*None) Config() map[string]string {
	return map[string]string{"containment": containProcessGroup}
}

// Capabilities implements Backend: D only.
func (*None) Capabilities() (CapSet, string) {
	return Caps(D), NoneNotice
}

// Start implements Backend. The Process it returns confirms empty when the
// command's process group is empty, and only then: it reports on the
// process group only, and is blind to any descendant that left it by
// starting a new session or process group or by detaching.
func (n *None) Start(ctx context.Context, spec Spec) (Process, error) {
	return n.Supervisor.start(ctx, containProcessGroup, spec)
}

// processGroup contains a command in a new process group of which it is the
// leader. It contains nothing that leaves the group.
type processGroup struct {
	cmd *exec.Cmd
}

func (g *processGroup) Prepare(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	g.cmd = cmd
	return nil
}

// group is the process group ID: the command leads its own group.
func (g *processGroup) group() int {
	return g.cmd.Process.Pid
}

func (g *processGroup) Signal(sig syscall.Signal) error {
	err := syscall.Kill(-g.group(), sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (g *processGroup) Remains() bool {
	return !errors.Is(syscall.Kill(-g.group(), 0), syscall.ESRCH)
}
