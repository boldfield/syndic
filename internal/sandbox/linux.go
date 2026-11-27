//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// LinuxNamespacesName is the namespace backend's name in the run manifest.
const LinuxNamespacesName = "linux-namespaces"

// NamespaceWorkdir is the fixed path at which the namespace backend mounts
// the scratch directory, and the command's working directory.
const NamespaceWorkdir = "/scratch"

// Minimum kernel of the namespace backend: 5.8 gives each proc mount its
// own instance and options, which the per-call proc relies on, and is later
// than ambient capabilities (4.3) and unprivileged pivot_root (3.8).
const (
	minKernelMajor = 5
	minKernelMinor = 8
)

// sandboxID is the one identity, user and group, mapped inside the user
// namespace. It is not 0: the command is unprivileged there too.
const sandboxID = 1000

// namespaceCloneFlags are the namespaces the supervisor starts in.
const namespaceCloneFlags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNET

// namespacesUsed names them for the manifest.
const namespacesUsed = "user,mount,pid,network"

// probeTimeout bounds the support probe.
const probeTimeout = 10 * time.Second

// containNamespaces names the namespace container.
const containNamespaces = "linux_namespaces"

// LinuxNamespaces is the Linux backend of the v7 delta, section 3,
// "Backends": user, mount, PID and network namespaces. It enforces F, N1, P
// and D, and N2 when a call has a proxy listener.
//
// Each command's supervisor starts in a new user, mount, PID and network
// namespace, as process 1 of the PID namespace; the command is its child,
// and nothing in the namespace outlives the supervisor. The engine's user is
// mapped to one unprivileged identity and no other. The command's root is a
// read-only tmpfs holding only the scratch directory at NamespaceWorkdir,
// the per-call temporary directory at its own path, the system locations
// read-only at their own paths, the device nodes, and a proc of the new PID
// namespace; the host's root is detached. The network namespace has no
// interface up, loopback included, unless the call has a proxy listener:
// then its Unix socket is mounted in the root and a forwarder on the
// namespace's own loopback, its one interface, reaches it. The command
// starts with no capability and no_new_privs set.
//
// Where namespaces cannot be created the backend is unsupported, with the
// reason; it never falls back to anything weaker.
type LinuxNamespaces struct {
	sup     Supervisor
	sys     SystemLocations
	release string
	skipped []string
	reason  string // empty when supported
}

// namespaceDeps are the host interfaces support detection uses, replaced
// in tests.
type namespaceDeps struct {
	// kernel returns the kernel release, as uname reports it.
	kernel func() (string, error)
	// probe builds the sandbox once in a throwaway supervisor.
	probe func(ctx context.Context, b *LinuxNamespaces) error
	// sysctl reads a value under /proc/sys; ok is false when absent.
	sysctl func(name string) (value string, ok bool)
}

var hostNamespaceDeps = namespaceDeps{
	kernel: func() (string, error) {
		var u unix.Utsname
		if err := unix.Uname(&u); err != nil {
			return "", err
		}
		return unix.ByteSliceToString(u.Release[:]), nil
	},
	probe: func(ctx context.Context, b *LinuxNamespaces) error { return b.probe(ctx) },
	sysctl: func(name string) (string, bool) {
		b, err := os.ReadFile(filepath.Join("/proc/sys", strings.ReplaceAll(name, ".", "/")))
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(b)), true
	},
}

// NewLinuxNamespaces returns the namespace backend for the given system
// locations, after testing whether it can build its sandbox on this host:
// it does so by creating the namespaces and the root in a throwaway
// supervisor. When it cannot, the backend reports itself unsupported, and
// Start fails.
func NewLinuxNamespaces(ctx context.Context, sup Supervisor, sys SystemLocations) *LinuxNamespaces {
	return newLinuxNamespaces(ctx, sup, sys, hostNamespaceDeps)
}

func newLinuxNamespaces(ctx context.Context, sup Supervisor, sys SystemLocations, deps namespaceDeps) *LinuxNamespaces {
	b := &LinuxNamespaces{sup: sup, sys: sys}
	_, b.skipped, _ = systemMounts(sys)
	release, err := deps.kernel()
	if err != nil {
		b.reason = fmt.Sprintf("%s: cannot read the kernel version: %v", LinuxNamespacesName, err)
		return b
	}
	b.release = release
	major, minor, ok := parseKernel(release)
	if !ok {
		b.reason = fmt.Sprintf("%s: cannot parse kernel release %q", LinuxNamespacesName, release)
		return b
	}
	if major < minKernelMajor || major == minKernelMajor && minor < minKernelMinor {
		b.reason = fmt.Sprintf("%s: kernel %s is below the minimum %d.%d", LinuxNamespacesName, release, minKernelMajor, minKernelMinor)
		return b
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := deps.probe(ctx, b); err != nil {
		b.reason = fmt.Sprintf("%s: cannot create the sandbox: %v", LinuxNamespacesName, err)
		if why := namespaceRestrictions(deps.sysctl); why != "" {
			b.reason += "; " + why
		}
	}
	return b
}

// namespaceRestrictions names the kernel settings that forbid unprivileged
// user namespaces on this host, or returns "" when none does.
func namespaceRestrictions(sysctl func(string) (string, bool)) string {
	var why []string
	for _, r := range []struct{ name, forbids string }{
		{"kernel.unprivileged_userns_clone", "0"},
		{"user.max_user_namespaces", "0"},
		{"kernel.apparmor_restrict_unprivileged_userns", "1"},
	} {
		if v, ok := sysctl(r.name); ok && v == r.forbids {
			why = append(why, fmt.Sprintf("%s = %s forbids unprivileged user namespaces", r.name, v))
		}
	}
	return strings.Join(why, "; ")
}

// parseKernel returns the major and minor numbers of a kernel release such
// as "6.8.0-1017-azure".
func parseKernel(release string) (major, minor int, ok bool) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	digits := parts[1]
	if i := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		digits = digits[:i]
	}
	minor, err = strconv.Atoi(digits)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// probe builds the full sandbox for the backend's system locations around
// two throwaway directories, in a supervisor that runs nothing.
func (b *LinuxNamespaces) probe(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "syndic-nsprobe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	scratch, tmp := filepath.Join(dir, "scratch"), filepath.Join(dir, "tmp")
	for _, d := range []string{scratch, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	plan, _, err := planMounts(scratch, tmp, b.sys)
	if err != nil {
		return err
	}
	return b.sup.probe(ctx, b.launch(plan))
}

// Name implements Backend.
func (*LinuxNamespaces) Name() string { return LinuxNamespacesName }

// Config implements Backend: the namespaces used, the kernel observed and
// its stated minimum, and the configured paths skipped because they do not
// exist on the host, as a JSON array.
func (b *LinuxNamespaces) Config() map[string]string {
	skipped, _ := json.Marshal(orEmpty(b.skipped))
	return map[string]string{
		"namespaces":     namespacesUsed,
		"kernel":         b.release,
		"minimum_kernel": fmt.Sprintf("%d.%d", minKernelMajor, minKernelMinor),
		"skipped_paths":  string(skipped),
	}
}

// Capabilities implements Backend: F, N1, N2, P and D when the sandbox can
// be built here, and nothing otherwise.
func (b *LinuxNamespaces) Capabilities() (CapSet, string) {
	if b.reason != "" {
		return 0, b.reason
	}
	return Caps(F, N1, N2, P, D), ""
}

// Start implements Backend.
func (b *LinuxNamespaces) Start(ctx context.Context, spec Spec) (Process, error) {
	if b.reason != "" {
		return nil, fmt.Errorf("sandbox: %s", b.reason)
	}
	plan, _, err := planMounts(spec.ScratchDir, spec.TempDir, spec.System)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	if spec.Proxy != nil {
		if plan, err = planProxy(plan, *spec.Proxy); err != nil {
			return nil, fmt.Errorf("sandbox: %w", err)
		}
	}
	return b.sup.start(ctx, b.launch(plan), spec)
}

func (b *LinuxNamespaces) launch(plan nsPlan) launch {
	return launch{
		container: containNamespaces,
		config:    plan,
		attr: func(a *syscall.SysProcAttr) {
			a.Cloneflags |= namespaceCloneFlags
			a.UidMappings = []syscall.SysProcIDMap{{ContainerID: sandboxID, HostID: os.Getuid(), Size: 1}}
			a.GidMappings = []syscall.SysProcIDMap{{ContainerID: sandboxID, HostID: os.Getgid(), Size: 1}}
			a.GidMappingsEnableSetgroups = false
			// The supervisor runs as sandboxID, which keeps no capability
			// across exec but an ambient one: it needs CAP_SYS_ADMIN in its
			// own user namespace to build the root, and CAP_NET_ADMIN to bring
			// the loopback up for a proxy listener, and drops it before the
			// command starts.
			a.AmbientCaps = []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_NET_ADMIN}
		},
	}
}

// Kinds of mount in a plan.
const (
	mountBind   = "bind"   // a directory or file of the host, bound
	mountDevice = "device" // a device node of the host, bound
	mountProc   = "proc"   // a fresh proc of the new PID namespace
)

// nsMount is one mount of the command's root.
type nsMount struct {
	Kind string `json:"kind"`
	// Source is the host path, symbolic links resolved; empty for proc.
	Source string `json:"source,omitempty"`
	// Target is the path in the command's root.
	Target string `json:"target"`
	// Flags are the MS_RDONLY, MS_NOSUID, MS_NODEV and MS_NOEXEC flags
	// the mount carries, beyond any the host's mount locks.
	Flags uintptr `json:"flags"`
}

// nsPlan is how the supervisor builds the command's root.
type nsPlan struct {
	// Staging is a host directory, the temporary directory, over which the
	// supervisor mounts the tmpfs it builds the root from.
	Staging string `json:"staging"`
	// Mounts are the mounts of the root, a parent before its children.
	Mounts []nsMount `json:"mounts"`
	// Workdir is the command's working directory.
	Workdir string `json:"workdir"`
	// Proxy is the call's listener, or nil for none.
	Proxy *nsProxy `json:"proxy,omitempty"`
}

// Mount flags of each kind of mount.
const (
	flagsReadWrite = unix.MS_NOSUID | unix.MS_NODEV
	flagsReadOnly  = unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV
	flagsDevice    = unix.MS_NOSUID | unix.MS_NOEXEC
	flagsProc      = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
)

// planMounts computes the mounts of a command's root: the scratch directory
// read-write at NamespaceWorkdir, the temporary directory read-write at its
// own path, each directory of the PATH and each read-only path read-only at
// its own path, the device nodes, and proc. A configured location that does
// not exist on the host is left out and returned in skipped.
func planMounts(scratch, tmp string, sys SystemLocations) (plan nsPlan, skipped []string, err error) {
	if !filepath.IsAbs(scratch) || !filepath.IsAbs(tmp) {
		return nsPlan{}, nil, errors.New("scratch and temporary directories must be absolute")
	}
	scratchSrc, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return nsPlan{}, nil, fmt.Errorf("scratch directory: %w", err)
	}
	tmpSrc, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return nsPlan{}, nil, fmt.Errorf("temporary directory: %w", err)
	}
	mounts := []nsMount{
		{Kind: mountBind, Source: scratchSrc, Target: NamespaceWorkdir, Flags: flagsReadWrite},
		{Kind: mountBind, Source: tmpSrc, Target: filepath.Clean(tmp), Flags: flagsReadWrite},
		{Kind: mountProc, Target: "/proc", Flags: flagsProc},
	}
	system, skipped, err := systemMounts(sys)
	if err != nil {
		return nsPlan{}, nil, err
	}
	mounts = append(mounts, system...)
	sort.SliceStable(mounts, func(i, j int) bool { return mounts[i].Target < mounts[j].Target })
	mounts, err = checkMounts(mounts)
	if err != nil {
		return nsPlan{}, nil, err
	}
	return nsPlan{Staging: tmpSrc, Mounts: mounts, Workdir: NamespaceWorkdir}, skipped, nil
}

// systemMounts returns the mounts of the system locations, and the
// configured locations that do not exist on the host.
func systemMounts(sys SystemLocations) (mounts []nsMount, skipped []string, err error) {
	add := func(kind, path string, flags uintptr) error {
		src, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			skipped = append(skipped, path)
			return nil
		}
		if err != nil {
			return err
		}
		if kind == mountDevice {
			fi, err := os.Stat(src)
			if err != nil {
				return err
			}
			if fi.Mode()&os.ModeCharDevice == 0 {
				return fmt.Errorf("%s is not a character device", path)
			}
		}
		mounts = append(mounts, nsMount{Kind: kind, Source: src, Target: filepath.Clean(path), Flags: flags})
		return nil
	}
	for _, dir := range sys.Path {
		// An empty or relative PATH entry names a place under the
		// working directory, not a location to mount.
		if !filepath.IsAbs(dir) {
			continue
		}
		if err := add(mountBind, dir, flagsReadOnly); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range sys.ReadOnlyPaths {
		if !filepath.IsAbs(p) {
			return nil, nil, fmt.Errorf("read-only path %q is not absolute", p)
		}
		if err := add(mountBind, p, flagsReadOnly); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range sys.Devices {
		if !filepath.IsAbs(d) {
			return nil, nil, fmt.Errorf("device %q is not absolute", d)
		}
		if err := add(mountDevice, d, flagsDevice); err != nil {
			return nil, nil, err
		}
	}
	return mounts, skipped, nil
}

// checkMounts drops exact duplicates from mounts sorted by target, and
// refuses the root as a target, two different mounts at one target, and a
// mount inside anything but a read-only bind: building its mount point
// would write into the mount it lies in.
func checkMounts(mounts []nsMount) ([]nsMount, error) {
	var out []nsMount
	for _, m := range mounts {
		if m.Target == "/" {
			return nil, errors.New("the root cannot be a mount")
		}
		if n := len(out); n > 0 && out[n-1].Target == m.Target {
			if out[n-1] != m {
				return nil, fmt.Errorf("two mounts at %s", m.Target)
			}
			continue
		}
		for _, p := range out {
			if strings.HasPrefix(m.Target, p.Target+"/") && (p.Kind != mountBind || p.Flags&unix.MS_RDONLY == 0) {
				return nil, fmt.Errorf("%s lies inside %s", m.Target, p.Target)
			}
		}
		out = append(out, m)
	}
	return out, nil
}
