//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeNamespaceDeps reports the given kernel and probe outcome, and reads
// sysctls from the map.
func fakeNamespaceDeps(release string, probeErr error, sysctls map[string]string) (namespaceDeps, *int) {
	probes := 0
	return namespaceDeps{
		kernel: func() (string, error) { return release, nil },
		probe: func(context.Context, *LinuxNamespaces) error {
			probes++
			return probeErr
		},
		sysctl: func(name string) (string, bool) {
			v, ok := sysctls[name]
			return v, ok
		},
	}, &probes
}

func TestNamespacesUnsupportedWhenRefused(t *testing.T) {
	refused := fmt.Errorf("sandbox: start supervisor: fork/exec /usr/bin/syndicd: %w", syscall.ENOSPC)
	for _, tc := range []struct {
		name    string
		sysctls map[string]string
		want    string
	}{
		{"max_user_namespaces", map[string]string{"user.max_user_namespaces": "0"}, "user.max_user_namespaces = 0 forbids unprivileged user namespaces"},
		{"unprivileged_userns_clone", map[string]string{"kernel.unprivileged_userns_clone": "0", "user.max_user_namespaces": "63000"}, "kernel.unprivileged_userns_clone = 0 forbids"},
		{"apparmor", map[string]string{"kernel.apparmor_restrict_unprivileged_userns": "1"}, "kernel.apparmor_restrict_unprivileged_userns = 1 forbids"},
		{"no setting", map[string]string{"kernel.apparmor_restrict_unprivileged_userns": "0"}, "no space left on device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, probes := fakeNamespaceDeps("6.8.0-1017-azure", refused, tc.sysctls)
			b := newLinuxNamespaces(context.Background(), Supervisor{}, SystemLocations{}, deps)
			enforced, reason := b.Capabilities()
			if enforced != 0 {
				t.Errorf("enforced %v, want none", enforced)
			}
			if !strings.Contains(reason, "cannot create the sandbox") || !strings.Contains(reason, tc.want) {
				t.Errorf("reason %q, want it to name the refusal and %q", reason, tc.want)
			}
			if *probes != 1 {
				t.Errorf("probed %d times, want 1", *probes)
			}
			if _, err := b.Start(context.Background(), Spec{Argv: []string{"true"}}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Start: %v, want the reason", err)
			}
		})
	}
}

func TestNamespacesBelowMinimumKernel(t *testing.T) {
	deps, probes := fakeNamespaceDeps("5.4.0-150-generic", nil, nil)
	b := newLinuxNamespaces(context.Background(), Supervisor{}, SystemLocations{}, deps)
	enforced, reason := b.Capabilities()
	if enforced != 0 || reason != "linux-namespaces: kernel 5.4.0-150-generic is below the minimum 5.8" {
		t.Errorf("Capabilities() = %v, %q", enforced, reason)
	}
	if *probes != 0 {
		t.Errorf("probed below the minimum")
	}
}

func TestNamespacesSupported(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	deps, _ := fakeNamespaceDeps("6.8.0-1017-azure", nil, nil)
	b := newLinuxNamespaces(context.Background(), Supervisor{}, SystemLocations{ReadOnlyPaths: []string{dir, missing}}, deps)
	enforced, reason := b.Capabilities()
	if enforced != Caps(F, N1, N2, P, D) || reason != "" {
		t.Errorf("Capabilities() = %v, %q; want F, N1, N2, P and D", enforced, reason)
	}
	if b.Name() != "linux-namespaces" {
		t.Errorf("Name() = %q", b.Name())
	}
	want := map[string]string{
		"namespaces":     "user,mount,pid,network",
		"kernel":         "6.8.0-1017-azure",
		"minimum_kernel": "5.8",
		"skipped_paths":  `["` + missing + `"]`,
	}
	if got := b.Config(); !reflect.DeepEqual(got, want) {
		t.Errorf("Config() = %v, want %v", got, want)
	}
}

func TestParseKernel(t *testing.T) {
	for release, want := range map[string][2]int{
		"6.8.0-1017-azure": {6, 8},
		"5.15.0":           {5, 15},
		"4.19":             {4, 19},
		"6.18.39-talos":    {6, 18},
		"5.10-rc1":         {5, 10},
	} {
		major, minor, ok := parseKernel(release)
		if !ok || major != want[0] || minor != want[1] {
			t.Errorf("parseKernel(%q) = %d, %d, %v", release, major, minor, ok)
		}
	}
	for _, bad := range []string{"", "6", "x.y", "6.x"} {
		if _, _, ok := parseKernel(bad); ok {
			t.Errorf("parseKernel(%q) ok", bad)
		}
	}
}

// planFixture makes a scratch directory, a temporary directory, two system
// directories, a symbolic link to one of them, and names a missing path.
type planFixture struct {
	scratch, tmp, usr, bin, link, missing string
}

func newPlanFixture(t *testing.T) planFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := planFixture{
		scratch: filepath.Join(dir, "scratch"),
		tmp:     filepath.Join(dir, "tmp-call"),
		usr:     filepath.Join(dir, "usr"),
		bin:     filepath.Join(dir, "usr", "bin"),
		link:    filepath.Join(dir, "lib"),
		missing: filepath.Join(dir, "etc", "pki"),
	}
	for _, d := range []string{f.scratch, f.tmp, f.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("usr", f.link); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPlanMounts(t *testing.T) {
	f := newPlanFixture(t)
	for _, d := range DeviceNodes {
		if _, err := os.Stat(d); err != nil {
			t.Skipf("host lacks %s: %v", d, err)
		}
	}
	ro := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV)
	rw := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	dev := uintptr(unix.MS_NOSUID | unix.MS_NOEXEC)
	proc := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	stated := []nsMount{
		{Kind: "device", Source: "/dev/null", Target: "/dev/null", Flags: dev},
		{Kind: "device", Source: "/dev/random", Target: "/dev/random", Flags: dev},
		{Kind: "device", Source: "/dev/urandom", Target: "/dev/urandom", Flags: dev},
		{Kind: "device", Source: "/dev/zero", Target: "/dev/zero", Flags: dev},
		{Kind: "bind", Source: f.usr, Target: f.link, Flags: ro},
		{Kind: "bind", Source: f.tmp, Target: f.tmp, Flags: rw},
		{Kind: "bind", Source: f.usr, Target: f.usr, Flags: ro},
		{Kind: "bind", Source: f.bin, Target: f.bin, Flags: ro},
		{Kind: "proc", Target: "/proc", Flags: proc},
		{Kind: "bind", Source: f.scratch, Target: "/scratch", Flags: rw},
	}
	for _, tc := range []struct {
		name        string
		readOnly    []string
		wantSkipped []string
	}{
		{"all present", []string{f.usr, f.link}, nil},
		{"one missing", []string{f.usr, f.missing, f.link}, []string{f.missing}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, skipped, err := planMounts(f.scratch, f.tmp, SystemLocations{
				// The PATH repeats a directory and has a relative entry;
				// neither adds a mount.
				Path:          []string{f.bin, f.bin, "bin", ""},
				ReadOnlyPaths: tc.readOnly,
				Devices:       DeviceNodes,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := append([]nsMount{}, stated...)
			sort.Slice(want, func(i, j int) bool { return want[i].Target < want[j].Target })
			if !reflect.DeepEqual(plan.Mounts, want) {
				got, _ := json.MarshalIndent(plan.Mounts, "", "  ")
				t.Errorf("mounts:\n%s", got)
			}
			if !reflect.DeepEqual(skipped, tc.wantSkipped) {
				t.Errorf("skipped %q, want %q", skipped, tc.wantSkipped)
			}
			if plan.Workdir != "/scratch" || plan.Staging != f.tmp {
				t.Errorf("workdir %q, staging %q", plan.Workdir, plan.Staging)
			}
		})
	}
}

func TestPlanMountsRefuses(t *testing.T) {
	f := newPlanFixture(t)
	plain := filepath.Join(f.usr, "file")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	inTmp := filepath.Join(f.tmp, "bin")
	if err := os.Mkdir(inTmp, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		tmp  string
		sys  SystemLocations
		want string
	}{
		{"the root", f.tmp, SystemLocations{ReadOnlyPaths: []string{"/"}}, "root cannot be a mount"},
		{"relative read-only path", f.tmp, SystemLocations{ReadOnlyPaths: []string{"usr"}}, "not absolute"},
		{"a device that is not one", f.tmp, SystemLocations{Devices: []string{plain}}, "not a character device"},
		{"inside the temporary directory", f.tmp, SystemLocations{Path: []string{inTmp}}, inTmp + " lies inside " + f.tmp},
		{"two mounts at one target", f.tmp, SystemLocations{ReadOnlyPaths: []string{f.tmp}}, "two mounts at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := planMounts(f.scratch, tc.tmp, tc.sys)
			if err == nil {
				t.Fatal("planned")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckMountsRefusesMountInsideReadWrite(t *testing.T) {
	_, err := checkMounts([]nsMount{
		{Kind: mountBind, Source: "/s", Target: "/scratch", Flags: flagsReadWrite},
		{Kind: mountBind, Source: "/usr", Target: "/scratch/usr", Flags: flagsReadOnly},
	})
	if err == nil || !strings.Contains(err.Error(), "/scratch/usr lies inside /scratch") {
		t.Errorf("checkMounts: %v", err)
	}
	if _, err := checkMounts([]nsMount{
		{Kind: mountBind, Source: "/usr", Target: "/usr", Flags: flagsReadOnly},
		{Kind: mountBind, Source: "/usr/bin", Target: "/usr/bin", Flags: flagsReadOnly},
	}); err != nil {
		t.Errorf("a read-only bind inside a read-only bind: %v", err)
	}
}

func TestLockedFlags(t *testing.T) {
	if got := lockedFlags(unix.ST_NOSUID | unix.ST_NODEV | unix.ST_RELATIME); got != unix.MS_NOSUID|unix.MS_NODEV|unix.MS_RELATIME {
		t.Errorf("lockedFlags = %#x", got)
	}
	if got := lockedFlags(unix.ST_RDONLY); got != unix.MS_RDONLY|unix.MS_STRICTATIME {
		t.Errorf("lockedFlags = %#x", got)
	}
}
