package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/sandbox"
)

// Tests that the run manifest of an engine start records how commands are
// confined (v7 delta, section 3, "Manifest" and "The opt-out"; section 4).

// runSandboxMember returns the exec_sandbox member of the started run's
// stored manifest, member by member.
func runSandboxMember(t *testing.T, h *enginetest.Harness) map[string]json.RawMessage {
	t.Helper()
	run, err := h.Store.GetRun(h.Engine.Hello().RunID)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(run.Manifest, &top); err != nil {
		t.Fatal(err)
	}
	var member map[string]json.RawMessage
	if err := json.Unmarshal(top["exec_sandbox"], &member); err != nil {
		t.Fatalf("exec_sandbox: %v: %s", err, run.Manifest)
	}
	return member
}

// runSandboxPreset returns the record of preset name in member.
func runSandboxPreset(t *testing.T, member map[string]json.RawMessage, name string) map[string]json.RawMessage {
	t.Helper()
	var presets map[string]map[string]json.RawMessage
	if err := json.Unmarshal(member["presets"], &presets); err != nil {
		t.Fatalf("presets: %v: %s", err, member["presets"])
	}
	rec, ok := presets[name]
	if !ok {
		t.Fatalf("no preset %q in %s", name, member["presets"])
	}
	return rec
}

func TestRunManifestRecordsSandboxPolicy(t *testing.T) {
	roster := execRoster(`
[tools.exec]
path = "/opt/bin:/usr/bin"
read_only_paths = ["/usr"]
env_passthrough = ["GOPATH"]
network_allow = ["proxy.golang.org"]
`)
	h := enginetest.New(t, roster, nil, execOpts(t, passingBackend(allCaps), fakeShell(t, "bash"))...)
	member := runSandboxMember(t, h)
	var presets map[string]map[string]json.RawMessage
	if err := json.Unmarshal(member["presets"], &presets); err != nil || len(presets) != 1 {
		t.Fatalf("presets = %s, want one exec preset (%v)", member["presets"], err)
	}
	var rec map[string]json.RawMessage
	for _, r := range presets {
		rec = r
	}

	want := sandbox.Policy{
		Path:          "/opt/bin:/usr/bin",
		ReadOnlyPaths: []string{"/usr"},
		Devices:       sandbox.DeviceNodes,
		EnvNames:      append(append([]string{"GOPATH"}, sandbox.FixedNames...), sandbox.ProxyVars...),
		Capabilities:  allCaps,
		NetworkAllow:  []string{"proxy.golang.org"},
	}
	canonical, err := want.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(rec["policy"]) != string(canonical) {
		t.Fatalf("policy = %s, want %s", rec["policy"], canonical)
	}
	var hash string
	if err := json.Unmarshal(rec["policy_hash"], &hash); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(rec["policy"])
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("policy_hash = %s, want SHA-256 of the policy's canonical JSON", hash)
	}
	if string(member["backend"]) != `"fake"` || string(member["proxy"]) != "true" || string(rec["proxy"]) != "true" {
		t.Fatalf("backend = %s, proxy = %s, preset proxy = %s", member["backend"], member["proxy"], rec["proxy"])
	}
	if got := string(rec["network_allow"]); got != `["proxy.golang.org"]` {
		t.Fatalf("network_allow = %s", got)
	}
	if _, ok := member["notice"]; ok {
		t.Fatalf("notice under sandbox = required")
	}
	if n := h.Engine.ExecSandboxNotice(); n != "" {
		t.Fatalf("start-log notice under sandbox = required: %q", n)
	}
}

func TestRunManifestCarriesSandboxNoneLine(t *testing.T) {
	// exec in a solo preset is refused under sandbox = "none".
	roster := refereeRoster(`
[presets.execreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "exec"]

[tools.exec]
sandbox = "none"
`)
	h := enginetest.New(t, roster, nil, append(refereeOpts(), enginetest.WithExec(engine.ExecOptions{
		Shells: []string{fakeShell(t, "bash")},
	}))...)
	const line = "exec: sandbox none; filesystem and network confinement are unenforced"
	member := runSandboxMember(t, h)
	if string(member["notice"]) != `"`+line+`"` {
		t.Fatalf("manifest notice = %s, want %q", member["notice"], line)
	}
	if string(member["backend"]) != `"none"` {
		t.Fatalf("backend = %s", member["backend"])
	}
	var policy struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(runSandboxPreset(t, member, "execreview")["policy"], &policy); err != nil || !slices.Equal(policy.Capabilities, []string{"D"}) {
		t.Fatalf("capabilities = %v (%v)", policy.Capabilities, err)
	}
	if n := h.Engine.ExecSandboxNotice(); n != line {
		t.Fatalf("start-log notice = %q, want %q", n, line)
	}
}

func TestRunManifestHasNoSandboxWithoutExec(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	run, err := h.Store.GetRun(h.Engine.Hello().RunID)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(run.Manifest, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["exec_sandbox"]; ok {
		t.Fatalf("exec_sandbox without any preset granting exec: %s", top["exec_sandbox"])
	}
}
