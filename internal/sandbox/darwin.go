package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MacOSSandboxExecName is the macOS backend's name in the run manifest.
const MacOSSandboxExecName = "macos-sandbox-exec"

// sandboxExecPath is the program that applies a profile and runs the
// command under it.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// containSandboxProfile names the profile-membership container.
const containSandboxProfile = "sandbox_profile"

// macAllowTokenName is the allow token's name inside the call's temporary
// directory.
const macAllowTokenName = ".syndic-sandbox-member"

// Fixed entries of every profile: what any program needs to start on macOS
// (the dynamic loader's caches and the system cryptexes, the shell selector,
// the root itself), read only. They are written as they are: each is already
// a path with no symbolic link in it on macOS.
var (
	macFixedReadLiterals = []string{"/", "/private/var/select/sh"}
	macFixedReadSubpaths = []string{"/private/var/db/dyld", "/System/Volumes/Preboot/Cryptexes", "/System/Cryptexes"}
	macFixedWriteLiteral = "/dev/null"
)

// MacOSSandboxExec is the macOS backend of the v7 delta, section 3,
// "Backends": a sandbox-exec profile generated for each call. It enforces F,
// N1, N2, P and D.
//
// The profile denies by default, and a read it does not allow fails as if
// the path did not exist. It allows reading and executing in the
// directories of the PATH, the read-only paths, the scratch directory and
// the temporary directory; reading the device nodes and the fixed entries
// a program needs to start; writing the scratch and temporary directories
// and /dev/null; signals within the same sandbox only; and, with a proxy
// listener, one outbound connection to that listener's loopback port. It
// allows no mach-lookup, so a command cannot ask the system to start a
// process outside the sandbox.
//
// macOS has no process namespace, so the call's processes are identified by
// the profile they inherit and cannot drop: a member is a sandboxed process
// that may read the call's allow token, inside its temporary directory, and
// may not read its deny token, outside every allowed location. The
// supervisor runs outside the profile and is never a member.
//
// Where the mechanism does not behave as stated, the backend is unsupported,
// with the reason; it never falls back to anything weaker.
type MacOSSandboxExec struct {
	sup     Supervisor
	sys     SystemLocations
	version string
	skipped []string
	reason  string // empty when supported
}

// NewMacOSSandboxExec returns the macOS backend for the given system
// locations, after testing that the mechanism works on this host: it runs
// one throwaway command under a generated profile and checks that the
// membership test identifies it and nothing else. When it does not, the
// backend reports itself unsupported, and Start fails.
func NewMacOSSandboxExec(ctx context.Context, sup Supervisor, sys SystemLocations) *MacOSSandboxExec {
	b := &MacOSSandboxExec{sup: sup, sys: sys}
	b.skipped = macSkippedPaths(sys)
	b.version, b.reason = macSupport(ctx, b)
	return b
}

// Name implements Backend.
func (*MacOSSandboxExec) Name() string { return MacOSSandboxExecName }

// Config implements Backend: the fixed entries of every profile, the
// containment, the macOS version observed, and the configured paths
// skipped because they do not exist on the host, the lists as JSON arrays.
func (b *MacOSSandboxExec) Config() map[string]string {
	list := func(s []string) string {
		j, _ := json.Marshal(orEmpty(s))
		return string(j)
	}
	return map[string]string{
		"sandbox_exec":        sandboxExecPath,
		"containment":         "profile_membership",
		"macos_version":       b.version,
		"fixed_read_literals": list(macFixedReadLiterals),
		"fixed_read_subpaths": list(macFixedReadSubpaths),
		"fixed_write_literal": macFixedWriteLiteral,
		"skipped_paths":       list(b.skipped),
	}
}

// Capabilities implements Backend: F, N1, N2, P and D when the mechanism
// works here, and nothing otherwise.
func (b *MacOSSandboxExec) Capabilities() (CapSet, string) {
	if b.reason != "" {
		return 0, b.reason
	}
	return Caps(F, N1, N2, P, D), ""
}

// macContainer is the container configuration the engine sends the
// supervisor: the files of one call.
type macContainer struct {
	// Profile is the profile file, inside Dir.
	Profile string `json:"profile"`
	// AllowToken and DenyToken are the membership tokens.
	AllowToken string `json:"allow_token"`
	DenyToken  string `json:"deny_token"`
	// Dir is the call's private directory, outside every allowed
	// location, holding the profile and the deny token.
	Dir string `json:"dir"`
}

// Start implements Backend. It writes the call's profile and tokens, and
// the supervisor removes them once nothing contained remains.
func (b *MacOSSandboxExec) Start(ctx context.Context, spec Spec) (Process, error) {
	if b.reason != "" {
		return nil, fmt.Errorf("sandbox: %s", b.reason)
	}
	files, err := writeMacCall(spec)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	p, err := b.sup.start(ctx, launch{container: containSandboxProfile, config: files}, spec)
	if err != nil {
		removeMacCall(files)
		return nil, err
	}
	return p, nil
}

// writeMacCall creates a call's private directory, its two tokens and its
// profile.
func writeMacCall(spec Spec) (files macContainer, err error) {
	if !filepath.IsAbs(spec.TempDir) {
		return macContainer{}, errors.New("the temporary directory must be absolute")
	}
	dir, err := os.MkdirTemp("", "syndic-sbx-")
	if err != nil {
		return macContainer{}, err
	}
	files.Dir = dir
	defer func() {
		if err != nil {
			removeMacCall(files)
		}
	}()
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return files, err
	}
	tmp, err := filepath.EvalSymlinks(spec.TempDir)
	if err != nil {
		return files, fmt.Errorf("temporary directory: %w", err)
	}
	files.DenyToken = filepath.Join(dir, "deny")
	if err := createToken(files.DenyToken); err != nil {
		return files, err
	}
	allow := filepath.Join(tmp, macAllowTokenName)
	if err := createToken(allow); err != nil {
		return files, err
	}
	files.AllowToken = allow
	profile, _, err := macProfile(spec.ScratchDir, spec.TempDir, spec.System, files.AllowToken, files.DenyToken, spec.Proxy)
	if err != nil {
		return files, err
	}
	files.Profile = filepath.Join(dir, "profile.sb")
	if err := os.WriteFile(files.Profile, []byte(profile), 0o600); err != nil {
		return files, err
	}
	return files, nil
}

// createToken creates an empty file that must not already exist.
func createToken(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	if err != nil {
		return fmt.Errorf("membership token: %w", err)
	}
	return f.Close()
}

// removeMacCall removes what writeMacCall created.
func removeMacCall(files macContainer) {
	if files.AllowToken != "" {
		_ = os.Remove(files.AllowToken)
	}
	if files.Dir != "" {
		_ = os.RemoveAll(files.Dir)
	}
}

// macProfile generates the profile of one call: the scratch and temporary
// directories, the system locations, the two membership tokens, and the
// proxy listener or none. Every path is written with its symbolic links
// resolved and escaped for the profile language; a path that cannot be
// written safely is refused. A configured location that does not exist is
// left out and returned in skipped. The deny token must lie outside every
// allowed location.
func macProfile(scratch, tmp string, sys SystemLocations, allowToken, denyToken string, proxy *Listener) (profile string, skipped []string, err error) {
	if !filepath.IsAbs(scratch) || !filepath.IsAbs(tmp) {
		return "", nil, errors.New("scratch and temporary directories must be absolute")
	}
	scratchR, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return "", nil, fmt.Errorf("scratch directory: %w", err)
	}
	tmpR, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return "", nil, fmt.Errorf("temporary directory: %w", err)
	}
	var exec []string // read and execute, as subpaths
	addExec := func(p string) error {
		r, err := filepath.EvalSymlinks(p)
		if errors.Is(err, os.ErrNotExist) {
			skipped = append(skipped, p)
			return nil
		}
		if err != nil {
			return err
		}
		exec = append(exec, r)
		return nil
	}
	for _, dir := range sys.Path {
		// An empty or relative PATH entry names a place under the
		// working directory, not a location to allow.
		if !filepath.IsAbs(dir) {
			continue
		}
		if err := addExec(dir); err != nil {
			return "", nil, err
		}
	}
	for _, p := range sys.ReadOnlyPaths {
		if !filepath.IsAbs(p) {
			return "", nil, fmt.Errorf("read-only path %q is not absolute", p)
		}
		if err := addExec(p); err != nil {
			return "", nil, err
		}
	}
	exec = append(exec, scratchR, tmpR)
	var devices []string
	for _, d := range sys.Devices {
		if !filepath.IsAbs(d) {
			return "", nil, fmt.Errorf("device %q is not absolute", d)
		}
		r, err := filepath.EvalSymlinks(d)
		if errors.Is(err, os.ErrNotExist) {
			skipped = append(skipped, d)
			continue
		}
		if err != nil {
			return "", nil, err
		}
		devices = append(devices, r)
	}
	for _, p := range exec {
		if denyToken == p || strings.HasPrefix(denyToken, strings.TrimSuffix(p, "/")+"/") {
			return "", nil, fmt.Errorf("the deny token %s lies inside the allowed location %s", denyToken, p)
		}
	}
	port := 0
	if proxy != nil {
		if port, err = loopbackPort(*proxy); err != nil {
			return "", nil, err
		}
	}

	var sb strings.Builder
	var bad error
	q := func(p string) string {
		s, err := sbplString(p)
		if err != nil && bad == nil {
			bad = err
		}
		return s
	}
	sb.WriteString("(version 1)\n(deny default)\n")
	// A read outside the allowed locations finds no such file rather than
	// a denial, as F requires of a protected path; the allows below
	// override it.
	sb.WriteString("(deny file-read* (with errno ENOENT))\n")
	sb.WriteString("(allow process-fork)\n")
	sb.WriteString("(allow signal (target same-sandbox))\n")
	sb.WriteString("(allow sysctl-read)\n")
	sb.WriteString("(allow file-read-metadata)\n")
	sb.WriteString("(allow file-read* process-exec")
	for _, p := range exec {
		sb.WriteString("\n  (subpath " + q(p) + ")")
	}
	sb.WriteString(")\n")
	sb.WriteString("(allow file-read*")
	for _, p := range devices {
		sb.WriteString("\n  (literal " + q(p) + ")")
	}
	for _, p := range macFixedReadLiterals {
		sb.WriteString("\n  (literal " + q(p) + ")")
	}
	for _, p := range macFixedReadSubpaths {
		sb.WriteString("\n  (subpath " + q(p) + ")")
	}
	sb.WriteString(")\n")
	sb.WriteString("(allow file-write*\n  (subpath " + q(scratchR) + ")\n  (subpath " + q(tmpR) + ")\n  (literal " + q(macFixedWriteLiteral) + "))\n")
	// The allow token is inside the writable temporary directory: were the
	// command able to remove or rename it, or any writable directory that
	// holds it, the token would leave the path the membership test checks,
	// no member would be found and the command's processes would escape the
	// scan. The scratch and temporary directories themselves are fixed too;
	// what is created inside them is checked against its own path.
	sb.WriteString("(deny file-write*\n  (literal " + q(allowToken) + ")")
	for _, p := range macFixedDirs(allowToken, scratchR, tmpR) {
		sb.WriteString("\n  (literal " + q(p) + ")")
	}
	sb.WriteString(")\n")
	sb.WriteString("(deny file-read* (literal " + q(denyToken) + "))\n")
	if port != 0 {
		sb.WriteString(`(allow network-outbound (remote tcp "localhost:` + strconv.Itoa(port) + `"))` + "\n")
	}
	if bad != nil {
		return "", nil, bad
	}
	return sb.String(), skipped, nil
}

// macFixedDirs returns the directories a command must not rename, remove
// or change: each directory that holds the allow token and lies inside the
// scratch or temporary directory, nearest first, then the scratch and
// temporary directories themselves.
func macFixedDirs(allowToken, scratchR, tmpR string) []string {
	inside := func(p, dir string) bool {
		return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
	}
	var dirs []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}
	for d := filepath.Dir(allowToken); inside(d, scratchR) || inside(d, tmpR); d = filepath.Dir(d) {
		add(d)
		if d == filepath.Dir(d) {
			break
		}
	}
	add(scratchR)
	add(tmpR)
	return dirs
}

// sbplString quotes p as a string of the profile language: a quote and a
// backslash are escaped. A path that is not valid UTF-8 or holds a control
// character is refused: the language's reader would take it otherwise than
// written, or not at all.
func sbplString(p string) (string, error) {
	if !utf8.ValidString(p) {
		return "", fmt.Errorf("path %q is not valid UTF-8 and cannot be written in a profile", p)
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range p {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return "", fmt.Errorf("path %q holds a control character and cannot be written in a profile", p)
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String(), nil
}

// loopbackPort returns the port of a proxy listener, which on macOS must be
// a TCP port on the loopback (v7 delta, section 4, "Per-call listener").
func loopbackPort(l Listener) (int, error) {
	if l.Network != "tcp" {
		return 0, fmt.Errorf("%s: the proxy listener must be a TCP port on the loopback, not %s", MacOSSandboxExecName, l.Network)
	}
	host, portStr, err := net.SplitHostPort(l.Address)
	if err != nil {
		return 0, fmt.Errorf("proxy listener %q: %w", l.Address, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return 0, fmt.Errorf("proxy listener %q is not on the loopback", l.Address)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("proxy listener %q has no valid port", l.Address)
	}
	return port, nil
}

// macSkippedPaths returns the configured locations that do not exist here.
func macSkippedPaths(sys SystemLocations) []string {
	var skipped []string
	for _, group := range [][]string{sys.Path, sys.ReadOnlyPaths, sys.Devices} {
		for _, p := range group {
			if !filepath.IsAbs(p) {
				continue
			}
			if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
				skipped = append(skipped, p)
			}
		}
	}
	return skipped
}
