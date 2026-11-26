package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SupervisorArg is the hidden subcommand of syndicd that runs the
// supervisor. It is not documented in the command's help: the engine
// starts it, never a user.
const SupervisorArg = "__supervise"

// The descriptors the engine gives the supervisor beyond the three standard
// ones, which are all /dev/null.
const (
	// supervisorControlFD is the supervisor's end of the control pipe, a
	// Unix stream socket. The supervisor refuses to run without it.
	supervisorControlFD = 3
	// supervisorOutputFD is the write end of the command's one pipe.
	supervisorOutputFD = 4
)

// Container is how a backend contains one command inside the supervisor and
// enumerates what it contains (v7 delta, section 3, P). The supervisor knows
// no platform mechanism; it drives the backend's Container.
type Container interface {
	// Prepare adjusts the command before it starts.
	Prepare(cmd *exec.Cmd) error
	// Signal signals everything contained with sig. It is called only after
	// the command has started; after everything has ended it is not an
	// error.
	Signal(sig syscall.Signal) error
	// Remains reports whether anything contained remains. It is called only
	// after the command itself has exited and been reaped.
	Remains() bool
}

// containers maps a container name, as the engine sends it to the
// supervisor, to a constructor given the container's configuration as the
// engine sent it. Backends register theirs from init.
var containers = map[string]func(config json.RawMessage) (Container, error){}

// launch says how a backend's supervisor is started and what it contains.
type launch struct {
	// container names the Container the supervisor uses.
	container string
	// config is the container's configuration, sent to the supervisor as
	// JSON; nil for none.
	config any
	// attr, when not nil, adjusts the supervisor's process attributes
	// before it starts, for example to start it in new namespaces.
	attr func(*syscall.SysProcAttr)
}

// Supervisor says how the engine starts a command's supervisor
// (v7 delta, section 3, P; section 8, "Engine stop and recovery").
type Supervisor struct {
	// Command is the supervisor's argument vector: the syndicd binary
	// followed by SupervisorArg.
	Command []string
	// Sequence is what the supervisor runs by itself when the engine goes.
	Sequence Sequence
}

// supervisorConfig is the first message on the control pipe, from the
// engine. Every message is one JSON value.
type supervisorConfig struct {
	Argv            []string      `json:"argv"`
	Dir             string        `json:"dir"`
	Env             []string      `json:"env"`
	Container       string        `json:"container"`
	KillGrace       time.Duration `json:"kill_grace"`
	CleanupDeadline time.Duration `json:"cleanup_deadline"`
	// ContainerConfig is the container's configuration.
	ContainerConfig json.RawMessage `json:"container_config,omitempty"`
	// Probe asks the supervisor to prepare its container and report, as
	// started or start_failed, whether it could, and to run no command.
	Probe bool `json:"probe,omitempty"`
}

// supervisorRequest is a later message from the engine.
type supervisorRequest struct {
	// Signal asks the supervisor to signal everything contained.
	Signal int `json:"signal"`
}

// Events the supervisor reports to the engine.
const (
	eventStarted     = "started"      // the command started; PID set
	eventStartFailed = "start_failed" // the command did not start; Error set
	eventExit        = "exit"         // the shell exited; Status set
	eventSequence    = "sequence"     // the supervisor ran the sequence itself; Confirmed set
	eventEmpty       = "empty"        // nothing contained remains
)

// supervisorEvent is a message from the supervisor.
type supervisorEvent struct {
	Event     string      `json:"event"`
	PID       int         `json:"pid,omitempty"`
	Error     string      `json:"error,omitempty"`
	Status    *ExitStatus `json:"status,omitempty"`
	Confirmed bool        `json:"confirmed,omitempty"`
}

// errSupervisorLost is returned when the supervisor ends without reporting.
var errSupervisorLost = errors.New("sandbox: supervisor ended without reporting")

// start starts a supervisor that runs spec contained as l says, and returns
// the command's Process once it has started.
func (s Supervisor) start(ctx context.Context, l launch, spec Spec) (*supervisedProcess, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("sandbox: empty argument vector")
	}
	return s.run(ctx, l, supervisorConfig{
		Argv:            spec.Argv,
		Dir:             spec.ScratchDir,
		Env:             spec.Env,
		KillGrace:       s.Sequence.KillGrace,
		CleanupDeadline: s.Sequence.CleanupDeadline,
	})
}

// probe starts a throwaway supervisor that prepares the container l
// describes and runs nothing, and returns nil only if the preparation
// succeeded.
func (s Supervisor) probe(ctx context.Context, l launch) error {
	p, err := s.run(ctx, l, supervisorConfig{Probe: true})
	if err != nil {
		return err
	}
	p.abandon()
	<-p.reaped
	return nil
}

// run starts a supervisor, sends it cfg completed from l, and returns once
// it reports the start.
func (s Supervisor) run(ctx context.Context, l launch, cfg supervisorConfig) (*supervisedProcess, error) {
	if len(s.Command) == 0 {
		return nil, errors.New("sandbox: no supervisor command")
	}
	cfg.Container = l.container
	if l.config != nil {
		b, err := json.Marshal(l.config)
		if err != nil {
			return nil, fmt.Errorf("sandbox: container configuration: %w", err)
		}
		cfg.ContainerConfig = b
	}
	ctl, childCtl, err := controlPair()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = ctl.Close()
		_ = childCtl.Close()
		return nil, err
	}
	// Standard input, output and error of the supervisor are /dev/null; it
	// never writes to the command's pipe itself.
	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	cmd.ExtraFiles = []*os.File{childCtl, pw} // supervisorControlFD, supervisorOutputFD
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	// The supervisor treats SIGTERM as the engine's death. The signal is
	// sent when the thread that started it exits, which a Go program's
	// threads do only for a goroutine locked to its thread: start is not
	// to be called from one.
	setParentDeathSignal(cmd.SysProcAttr, syscall.SIGTERM)
	if l.attr != nil {
		l.attr(cmd.SysProcAttr)
	}
	err = cmd.Start()
	_ = childCtl.Close()
	_ = pw.Close()
	if err != nil {
		_ = ctl.Close()
		_ = pr.Close()
		return nil, fmt.Errorf("sandbox: start supervisor: %w", err)
	}

	p := &supervisedProcess{
		cmd:     cmd,
		ctl:     ctl,
		out:     pr,
		started: make(chan struct{}),
		exited:  make(chan struct{}),
		empty:   make(chan struct{}),
		done:    make(chan struct{}),
		reaped:  make(chan struct{}),
	}
	go p.read()
	go p.reap()

	if err := p.send(cfg); err != nil {
		p.abandon()
		return nil, fmt.Errorf("sandbox: configure supervisor: %w", err)
	}
	select {
	case <-p.started:
		if p.startErr != nil {
			p.abandon()
			return nil, p.startErr
		}
		return p, nil
	case <-p.done:
		p.abandon()
		return nil, errSupervisorLost
	case <-ctx.Done():
		p.abandon()
		if cfg.Probe {
			// A probe has nothing to terminate but itself.
			_ = cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
}

// controlPair returns the two ends of a control pipe, both close-on-exec.
// The engine's end is non-blocking, so that closing it interrupts a read.
func controlPair() (engine, child *os.File, err error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox: control pipe: %w", err)
	}
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		_ = syscall.Close(fds[0])
		_ = syscall.Close(fds[1])
		return nil, nil, fmt.Errorf("sandbox: control pipe: %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "supervisor-control"), os.NewFile(uintptr(fds[1]), "supervisor-control"), nil
}

// supervisedProcess is the engine's handle on a command run by a
// supervisor.
type supervisedProcess struct {
	cmd *exec.Cmd
	ctl *os.File
	out *os.File

	sendMu sync.Mutex

	mu       sync.Mutex
	pid      int // the command's process ID
	startErr error
	status   ExitStatus
	// confirmed is the outcome of a sequence the supervisor ran itself, nil
	// if it ran none. Nothing reads it yet: it is kept for the journaling of
	// an unverified containment, which is later work.
	confirmed *bool

	started chan struct{} // closed on started or start_failed
	exited  chan struct{} // closed on exit
	empty   chan struct{} // closed on empty
	done    chan struct{} // closed when the control pipe has ended
	reaped  chan struct{} // closed when the supervisor has been waited for
}

func (p *supervisedProcess) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	_, err = p.ctl.Write(b)
	return err
}

// read dispatches the supervisor's events until the control pipe ends, and
// then closes the engine's end: nothing more can arrive on it, and a later
// send fails with os.ErrClosed rather than holding a descriptor open.
func (p *supervisedProcess) read() {
	defer close(p.done)
	defer func() { _ = p.ctl.Close() }()
	dec := json.NewDecoder(p.ctl)
	var startedOnce, exitedOnce, emptyOnce sync.Once
	for {
		var ev supervisorEvent
		if err := dec.Decode(&ev); err != nil {
			startedOnce.Do(func() {
				p.mu.Lock()
				p.startErr = errSupervisorLost
				p.mu.Unlock()
				close(p.started)
			})
			return
		}
		switch ev.Event {
		case eventStarted:
			startedOnce.Do(func() {
				p.mu.Lock()
				p.pid = ev.PID
				p.mu.Unlock()
				close(p.started)
			})
		case eventStartFailed:
			startedOnce.Do(func() {
				p.mu.Lock()
				p.startErr = fmt.Errorf("sandbox: start command: %s", ev.Error)
				p.mu.Unlock()
				close(p.started)
			})
		case eventExit:
			if ev.Status == nil {
				continue
			}
			exitedOnce.Do(func() {
				p.mu.Lock()
				p.status = *ev.Status
				p.mu.Unlock()
				close(p.exited)
			})
		case eventSequence:
			p.mu.Lock()
			confirmed := ev.Confirmed
			p.confirmed = &confirmed
			p.mu.Unlock()
		case eventEmpty:
			emptyOnce.Do(func() { close(p.empty) })
		}
	}
}

// reap waits for the supervisor once the control pipe has ended.
func (p *supervisedProcess) reap() {
	<-p.done
	_ = p.cmd.Wait()
	close(p.reaped)
}

// abandon closes the engine's ends, as the engine's death would: the
// supervisor then terminates whatever it started by itself.
func (p *supervisedProcess) abandon() {
	_ = p.ctl.Close()
	_ = p.out.Close()
}

func (p *supervisedProcess) isEmpty() bool {
	select {
	case <-p.empty:
		return true
	default:
		return false
	}
}

func (p *supervisedProcess) Output() io.ReadCloser { return p.out }

func (p *supervisedProcess) Wait() (ExitStatus, error) {
	select {
	case <-p.exited:
	case <-p.done:
		select {
		case <-p.exited:
		default:
			return ExitStatus{}, errSupervisorLost
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, nil
}

func (p *supervisedProcess) Terminate(sig syscall.Signal) error {
	if p.isEmpty() {
		return nil
	}
	if err := p.send(supervisorRequest{Signal: int(sig)}); err != nil {
		// The supervisor may have reported empty and exited since.
		<-p.done
		if p.isEmpty() {
			return nil
		}
		return fmt.Errorf("sandbox: signal supervisor: %w", err)
	}
	return nil
}

func (p *supervisedProcess) ConfirmEmpty(ctx context.Context) error {
	select {
	case <-p.empty:
		return nil
	case <-p.done:
		if p.isEmpty() {
			return nil
		}
		return errSupervisorLost
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Supervise is the body of the hidden subcommand: the supervisor of one
// command (v7 delta, section 3, P). It refuses to run, returning 2, unless
// started by the engine with a control pipe on descriptor 3.
//
// It reads the command from the control pipe and starts it as its child,
// with standard input on /dev/null and standard output and error on the one
// pipe, and no other descriptor: every descriptor above 2, the control pipe
// included, is closed when the command starts. It reports the start, the
// shell's exit status and, once the backend's Container reports nothing
// remaining, that the containment is empty, then exits. It signals the
// containment when the engine asks. When the control pipe reaches end of
// file, as it does when the engine dies by any cause, or on SIGTERM, it runs
// the termination sequence itself; if that ends unverified it goes on
// signalling with SIGKILL until nothing remains.
func Supervise(stderr io.Writer) int {
	if !isSocket(supervisorControlFD) || !isOpen(supervisorOutputFD) {
		_, _ = fmt.Fprintf(stderr, "syndicd: %s is internal to the engine and is not run by hand\n", SupervisorArg)
		return 2
	}
	if err := closeOnExecAll(); err != nil {
		_, _ = fmt.Fprintf(stderr, "syndicd: supervisor: %v\n", err)
		return 1
	}
	// Caught, not ignored: an ignored signal would stay ignored in the
	// command. SIGINT and SIGHUP from a terminal are the engine's business.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	ctl := os.NewFile(supervisorControlFD, "supervisor-control")
	out := os.NewFile(supervisorOutputFD, "output")
	dec := json.NewDecoder(ctl)
	var cfg supervisorConfig
	if err := dec.Decode(&cfg); err != nil {
		_, _ = fmt.Fprintf(stderr, "syndicd: supervisor: read configuration: %v\n", err)
		return 2
	}
	s := &supervisor{
		ctl:    ctl,
		seq:    Sequence{KillGrace: cfg.KillGrace, CleanupDeadline: cfg.CleanupDeadline},
		exited: make(chan struct{}),
		empty:  make(chan struct{}),
	}
	defer s.release()
	if cfg.Probe {
		_ = out.Close()
		if err := probeContainer(cfg); err != nil {
			s.report(supervisorEvent{Event: eventStartFailed, Error: err.Error()})
			return 1
		}
		s.report(supervisorEvent{Event: eventStarted})
		return 0
	}
	if err := s.startCommand(cfg, out); err != nil {
		s.report(supervisorEvent{Event: eventStartFailed, Error: err.Error()})
		return 1
	}
	s.report(supervisorEvent{Event: eventStarted, PID: s.cmd.Process.Pid})

	engineGone := make(chan struct{})
	go func() {
		defer close(engineGone)
		for {
			var req supervisorRequest
			if err := dec.Decode(&req); err != nil {
				return
			}
			if req.Signal != 0 {
				_ = s.Terminate(syscall.Signal(req.Signal))
			}
		}
	}()
	go s.wait()
	go s.watch()

	for {
		select {
		case <-s.empty:
			s.report(supervisorEvent{Event: eventEmpty})
			return 0
		case sig := <-sigs:
			if sig != syscall.SIGTERM {
				continue
			}
			s.terminateAlone()
			return 0
		case <-engineGone:
			s.terminateAlone()
			return 0
		}
	}
}

// supervisor is the supervisor's own state, and the Terminable the
// sequence drives when the supervisor runs it.
type supervisor struct {
	ctl       *os.File
	seq       Sequence
	container Container
	cmd       *exec.Cmd

	sendMu sync.Mutex

	mu        sync.Mutex
	signalled bool // a signal was sent: the shell's exit is a termination
	killing   bool // SIGKILL was sent: go on sending it while anything remains

	exited chan struct{} // the shell has exited and been reaped
	empty  chan struct{} // nothing contained remains
}

func (s *supervisor) startCommand(cfg supervisorConfig, out *os.File) error {
	defer func() { _ = out.Close() }()
	if len(cfg.Argv) == 0 {
		return errors.New("empty argument vector")
	}
	container, err := newContainer(cfg)
	if err != nil {
		return err
	}
	s.container = container
	s.cmd = &exec.Cmd{
		Args: cfg.Argv,
		Dir:  cfg.Dir,
		// Exactly the given environment; a nil Env would pass the
		// supervisor's own.
		Env:    append([]string{}, cfg.Env...),
		Stdout: out,
		Stderr: out,
	}
	if err := s.container.Prepare(s.cmd); err != nil {
		return err
	}
	// Resolved after Prepare, which may change what the command sees and
	// what runs first.
	path, err := lookPath(s.cmd.Args[0], cfg.Env)
	if err != nil {
		return err
	}
	s.cmd.Path = path
	return s.cmd.Start()
}

// releaser is a Container that holds files of its own, released once
// nothing it contains remains.
type releaser interface {
	release()
}

// release releases the container's files, if it has any.
func (s *supervisor) release() {
	if r, ok := s.container.(releaser); ok {
		r.release()
	}
}

// newContainer constructs the container cfg names.
func newContainer(cfg supervisorConfig) (Container, error) {
	newC, ok := containers[cfg.Container]
	if !ok {
		return nil, fmt.Errorf("unknown container %q", cfg.Container)
	}
	return newC(cfg.ContainerConfig)
}

// probeContainer prepares the container cfg names around no command.
func probeContainer(cfg supervisorConfig) error {
	c, err := newContainer(cfg)
	if err != nil {
		return err
	}
	return c.Prepare(&exec.Cmd{})
}

func (s *supervisor) report(ev supervisorEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	// The engine may be gone; there is no one else to tell.
	_, _ = s.ctl.Write(b)
}

// wait reaps the shell and reports its status.
func (s *supervisor) wait() {
	_ = s.cmd.Wait()
	s.mu.Lock()
	terminated := s.signalled
	s.mu.Unlock()
	st := ExitStatus{Code: s.cmd.ProcessState.ExitCode()}
	if ws, ok := s.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		st = SignalStatus(ws.Signal(), terminated)
	} else {
		st.Terminated = terminated
	}
	s.report(supervisorEvent{Event: eventExit, Status: &st})
	close(s.exited)
}

// watch waits for the shell, then polls the container until nothing
// remains, sending SIGKILL again on each poll once a SIGKILL has been asked
// for.
func (s *supervisor) watch() {
	<-s.exited
	interval := time.Millisecond
	for s.container.Remains() {
		s.mu.Lock()
		killing := s.killing
		s.mu.Unlock()
		if killing {
			_ = s.container.Signal(syscall.SIGKILL)
		}
		time.Sleep(interval)
		if interval < 50*time.Millisecond {
			interval *= 2
		}
	}
	close(s.empty)
}

// terminateAlone runs the sequence without the engine, and does not return
// until nothing remains.
func (s *supervisor) terminateAlone() {
	shellExited := false
	select {
	case <-s.exited:
		shellExited = true
	default:
	}
	confirmed := s.seq.Run(s, shellExited, nil)
	s.report(supervisorEvent{Event: eventSequence, Confirmed: confirmed})
	<-s.empty
	s.report(supervisorEvent{Event: eventEmpty})
}

// Terminate implements Terminable.
func (s *supervisor) Terminate(sig syscall.Signal) error {
	s.mu.Lock()
	s.signalled = true
	if sig == syscall.SIGKILL {
		s.killing = true
	}
	s.mu.Unlock()
	select {
	case <-s.empty:
		return nil
	default:
	}
	return s.container.Signal(sig)
}

// ConfirmEmpty implements Terminable.
func (s *supervisor) ConfirmEmpty(ctx context.Context) error {
	select {
	case <-s.empty:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// lookPath resolves name as a shell would, against the PATH of env rather
// than the supervisor's own.
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

func isSocket(fd int) bool {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return false
	}
	return uint32(st.Mode)&syscall.S_IFMT == syscall.S_IFSOCK
}

func isOpen(fd int) bool {
	var st syscall.Stat_t
	return syscall.Fstat(fd, &st) == nil
}

// closeOnExecAll marks every descriptor above 2 close-on-exec, so that the
// command inherits none of them: neither the control pipe nor anything the
// engine leaked to the supervisor.
func closeOnExecAll() error {
	// Names only: on macOS the reader's own transient descriptor is listed
	// and cannot be stat'ed once the read ends.
	d, err := os.Open("/dev/fd")
	if err != nil {
		return fmt.Errorf("list descriptors: %w", err)
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return fmt.Errorf("list descriptors: %w", err)
	}
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd <= 2 {
			continue
		}
		syscall.CloseOnExec(fd)
	}
	return nil
}
