package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// macDirs makes the directories of one call under a resolved root.
func macDirs(t *testing.T) (root, scratch, tmp string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scratch, tmp = filepath.Join(root, "scratch"), filepath.Join(root, "tmp")
	for _, d := range []string{scratch, tmp, filepath.Join(root, "bin"), filepath.Join(root, "lib")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, scratch, tmp
}

func macSystem(root string) SystemLocations {
	return SystemLocations{
		Path:          []string{filepath.Join(root, "bin"), "", "relative"},
		ReadOnlyPaths: []string{filepath.Join(root, "lib"), filepath.Join(root, "missing")},
		Devices:       []string{"/dev/null"},
	}
}

// wantMacProfile is the profile for macSystem(root), with or without the
// listener line.
func wantMacProfile(root, listener string) string {
	return `(version 1)
(deny default)
(deny file-read* (with errno ENOENT))
(allow process-fork)
(allow signal (target same-sandbox))
(allow sysctl-read)
(allow file-read-metadata)
(allow file-read* process-exec
  (subpath "` + root + `/bin")
  (subpath "` + root + `/lib")
  (subpath "` + root + `/scratch")
  (subpath "` + root + `/tmp"))
(allow file-read*
  (literal "/dev/null")
  (literal "/")
  (literal "/private/var/select/sh")
  (subpath "/private/var/db/dyld")
  (subpath "/System/Volumes/Preboot/Cryptexes")
  (subpath "/System/Cryptexes"))
(allow file-write*
  (subpath "` + root + `/scratch")
  (subpath "` + root + `/tmp")
  (literal "/dev/null"))
(deny file-write*
  (literal "` + root + `/tmp/allow")
  (literal "` + root + `/tmp")
  (literal "` + root + `/scratch"))
(deny file-read* (literal "` + root + `/private/deny"))
` + listener
}

func TestMacProfileNoListener(t *testing.T) {
	root, scratch, tmp := macDirs(t)
	got, skipped, err := macProfile(scratch, tmp, macSystem(root), filepath.Join(tmp, "allow"), filepath.Join(root, "private", "deny"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := wantMacProfile(root, ""); got != want {
		t.Errorf("profile:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "network") || strings.Contains(got, "mach-lookup") {
		t.Errorf("profile has a network or mach-lookup rule:\n%s", got)
	}
	if len(skipped) != 1 || skipped[0] != filepath.Join(root, "missing") {
		t.Errorf("skipped %q, want the missing read-only path", skipped)
	}
}

func TestMacProfileListener(t *testing.T) {
	root, scratch, tmp := macDirs(t)
	for _, addr := range []string{"127.0.0.1:48213", "localhost:48213", "[::1]:48213"} {
		got, _, err := macProfile(scratch, tmp, macSystem(root), filepath.Join(tmp, "allow"), filepath.Join(root, "private", "deny"), &Listener{Network: "tcp", Address: addr})
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if want := wantMacProfile(root, `(allow network-outbound (remote tcp "localhost:48213"))`+"\n"); got != want {
			t.Errorf("%s: profile:\n%s\nwant:\n%s", addr, got, want)
		}
	}
	for _, l := range []Listener{
		{Network: "unix", Address: "/tmp/proxy.sock"},
		{Network: "tcp", Address: "192.0.2.1:8080"},
		{Network: "tcp", Address: "example.com:8080"},
		{Network: "tcp", Address: "127.0.0.1:0"},
		{Network: "tcp", Address: "127.0.0.1"},
	} {
		if _, _, err := macProfile(scratch, tmp, macSystem(root), filepath.Join(tmp, "allow"), filepath.Join(root, "deny"), &l); err == nil {
			t.Errorf("listener %+v accepted", l)
		}
	}
}

func TestMacProfileQuoteAndSymlink(t *testing.T) {
	root, scratch, tmp := macDirs(t)
	quoted := filepath.Join(root, `say "hi" \ there`)
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	for _, d := range []string{quoted, target} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	sys := SystemLocations{ReadOnlyPaths: []string{quoted, link}}
	got, _, err := macProfile(scratch, tmp, sys, filepath.Join(tmp, "allow"), filepath.Join(root, "deny"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `(subpath "` + root + `/say \"hi\" \\ there")`; !strings.Contains(got, want) {
		t.Errorf("profile lacks the escaped path %s:\n%s", want, got)
	}
	if want := `(subpath "` + target + `")`; !strings.Contains(got, want) {
		t.Errorf("profile lacks the resolved link %s:\n%s", want, got)
	}
	if strings.Contains(got, link) {
		t.Errorf("profile names the link itself:\n%s", got)
	}

	// The scratch directory through a link is written resolved too.
	scratchLink := filepath.Join(root, "scratch-link")
	if err := os.Symlink(scratch, scratchLink); err != nil {
		t.Fatal(err)
	}
	got, _, err = macProfile(scratchLink, tmp, SystemLocations{}, filepath.Join(tmp, "allow"), filepath.Join(root, "deny"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, scratchLink) || !strings.Contains(got, `(subpath "`+scratch+`")`) {
		t.Errorf("scratch link not resolved:\n%s", got)
	}
}

func TestMacProfileRefusesUnescapable(t *testing.T) {
	root, scratch, tmp := macDirs(t)
	// Invalid UTF-8 cannot be a directory name on every file system, so it
	// is tried as a token, which is written but not resolved.
	if _, _, err := macProfile(scratch, tmp, SystemLocations{}, filepath.Join(tmp, "bad\xffutf8"), filepath.Join(root, "deny"), nil); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("invalid UTF-8: err %v, want a refusal", err)
	}
	for _, name := range []string{"new\nline", "tab\there", "bell\x07", "del\x7f"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := macProfile(scratch, tmp, SystemLocations{ReadOnlyPaths: []string{dir}}, filepath.Join(tmp, "allow"), filepath.Join(root, "deny"), nil)
		if err == nil || !strings.Contains(err.Error(), "cannot be written in a profile") {
			t.Errorf("path %q: err %v, want a refusal", name, err)
		}
	}
	// The tokens are paths of the profile too.
	if _, _, err := macProfile(scratch, tmp, SystemLocations{}, filepath.Join(tmp, "allow\n"), filepath.Join(root, "deny"), nil); err == nil {
		t.Error("an unescapable allow token accepted")
	}
}

func TestMacFixedDirs(t *testing.T) {
	for _, tc := range []struct {
		token, scratch, tmp string
		want                []string
	}{
		{"/r/tmp/allow", "/r/scratch", "/r/tmp", []string{"/r/tmp", "/r/scratch"}},
		// A temporary directory inside the scratch directory: every
		// directory between them is fixed too.
		{"/r/s/a/tmp/allow", "/r/s", "/r/s/a/tmp", []string{"/r/s/a/tmp", "/r/s/a", "/r/s"}},
		// A scratch directory inside the temporary directory.
		{"/r/tmp/allow", "/r/tmp/s", "/r/tmp", []string{"/r/tmp", "/r/tmp/s"}},
		{"/allow", "/", "/r/tmp", []string{"/", "/r/tmp"}},
	} {
		got := macFixedDirs(tc.token, tc.scratch, tc.tmp)
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("macFixedDirs(%s, %s, %s) = %q, want %q", tc.token, tc.scratch, tc.tmp, got, tc.want)
		}
	}
}

func TestMacProfileRefusesDenyTokenInside(t *testing.T) {
	root, scratch, tmp := macDirs(t)
	for _, deny := range []string{filepath.Join(tmp, "deny"), filepath.Join(root, "lib", "x", "deny")} {
		if _, _, err := macProfile(scratch, tmp, macSystem(root), filepath.Join(tmp, "allow"), deny, nil); err == nil {
			t.Errorf("deny token %s inside an allowed location accepted", deny)
		}
	}
}

func TestMacProfileRefusesRelative(t *testing.T) {
	root, scratch, tmp := macDirs(t)
	for _, sys := range []SystemLocations{{ReadOnlyPaths: []string{"usr"}}, {Devices: []string{"dev/null"}}} {
		if _, _, err := macProfile(scratch, tmp, sys, filepath.Join(tmp, "allow"), filepath.Join(root, "deny"), nil); err == nil {
			t.Errorf("relative location accepted: %+v", sys)
		}
	}
	if _, _, err := macProfile("scratch", tmp, SystemLocations{}, filepath.Join(tmp, "allow"), filepath.Join(root, "deny"), nil); err == nil {
		t.Error("relative scratch directory accepted")
	}
}

func TestWriteMacCall(t *testing.T) {
	_, scratch, tmp := macDirs(t)
	files, err := writeMacCall(Spec{ScratchDir: scratch, TempDir: tmp, System: SystemLocations{Devices: []string{"/dev/null"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{files.Profile, files.AllowToken, files.DenyToken} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%v", err)
		}
	}
	if filepath.Dir(files.AllowToken) != tmp {
		t.Errorf("allow token %s is not in the temporary directory", files.AllowToken)
	}
	for _, p := range []string{files.Profile, files.DenyToken} {
		if strings.HasPrefix(p, tmp+"/") || strings.HasPrefix(p, scratch+"/") {
			t.Errorf("%s is inside the call's directories", p)
		}
	}
	b, err := os.ReadFile(files.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `(deny file-read* (literal "`+files.DenyToken+`"))`) {
		t.Errorf("profile does not deny its deny token:\n%s", b)
	}
	removeMacCall(files)
	for _, p := range []string{files.Dir, files.AllowToken} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s remains after removal (%v)", p, err)
		}
	}
}
