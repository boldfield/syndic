package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/sandbox"
)

// Tests of the run manifest's record of how commands are confined (v7 delta,
// section 3, "Manifest" and "The opt-out"; section 4, last bullet).

func execTestRoster(ex *config.ExecConfig) *config.Roster {
	return &config.Roster{
		Participants: []config.Participant{
			{ID: "p1", Endpoint: "ep1", Model: "m1", Route: "api"},
			{ID: "p2", Endpoint: "ep1", Model: "m2", Route: "api"},
		},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets: map[string]config.Preset{
			"default": {Mode: "solo", Author: "p1"},
			"exec":    {Mode: "review", Author: "p1", RequiredReviewers: []string{"p2"}, Tools: []string{"read", "exec"}},
		},
		Pools: map[string]config.Pool{},
		Tools: &config.ToolsConfig{Exec: ex},
	}
}

// buildSandboxMember builds the manifest with the record and returns its
// exec_sandbox member, member by member.
func buildSandboxMember(t *testing.T, roster *config.Roster, sb *Sandbox) map[string]json.RawMessage {
	t.Helper()
	db, cleanup := openTestDB(t)
	defer cleanup()
	built, err := Build(Input{
		Store:          journal.NewStore(db),
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(config.Participant) string { return "1.0" },
		Sandbox:        sb,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(built.JSON, &top); err != nil {
		t.Fatal(err)
	}
	raw, ok := top["exec_sandbox"]
	if !ok {
		t.Fatalf("manifest has no exec_sandbox: %s", built.JSON)
	}
	var member map[string]json.RawMessage
	if err := json.Unmarshal(raw, &member); err != nil {
		t.Fatal(err)
	}
	return member
}

// presetMember returns the record of preset name in the exec_sandbox member,
// member by member.
func presetMember(t *testing.T, member map[string]json.RawMessage, name string) map[string]json.RawMessage {
	t.Helper()
	var presets map[string]map[string]json.RawMessage
	if err := json.Unmarshal(member["presets"], &presets); err != nil {
		t.Fatalf("presets: %v: %s", err, member["presets"])
	}
	p, ok := presets[name]
	if !ok {
		t.Fatalf("no preset %q in %s", name, member["presets"])
	}
	return p
}

// checkPolicyHash fails unless the record's policy_hash is SHA-256 of its
// policy object as the manifest carries it, and that object is want's
// canonical JSON.
func checkPolicyHash(t *testing.T, rec map[string]json.RawMessage, want sandbox.Policy) {
	t.Helper()
	var hash string
	if err := json.Unmarshal(rec["policy_hash"], &hash); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(rec["policy"])
	if hash != hex.EncodeToString(sum[:]) {
		t.Errorf("policy_hash = %s, want SHA-256 of %s", hash, rec["policy"])
	}
	canonical, err := want.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(rec["policy"]) != string(canonical) {
		t.Errorf("policy = %s, want %s", rec["policy"], canonical)
	}
}

func TestBuild_SandboxPolicyObjectAndHash(t *testing.T) {
	ex := &config.ExecConfig{
		Path:           "/opt/bin:/usr/bin",
		ReadOnlyPaths:  []string{"/usr", "/etc/ssl"},
		EnvPassthrough: []string{"GOPATH", "CARGO_HOME"},
		NetworkAllow:   []string{"proxy.golang.org", "*.crates.io"},
	}
	roster := execTestRoster(ex)
	caps := sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D)
	member := buildSandboxMember(t, roster, NewSandbox(roster, sandbox.NewFake(caps)))
	rec := presetMember(t, member, "exec")

	var policy struct {
		Path          string   `json:"path"`
		ReadOnlyPaths []string `json:"read_only_paths"`
		Devices       []string `json:"devices"`
		EnvNames      []string `json:"env_names"`
		Capabilities  []string `json:"capabilities"`
		NetworkAllow  []string `json:"network_allow"`
	}
	if err := json.Unmarshal(rec["policy"], &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Path != ex.Path {
		t.Errorf("path = %q, want %q", policy.Path, ex.Path)
	}
	if !slices.Equal(policy.ReadOnlyPaths, ex.ReadOnlyPaths) {
		t.Errorf("read_only_paths = %v, want %v", policy.ReadOnlyPaths, ex.ReadOnlyPaths)
	}
	if !slices.Equal(policy.Devices, sandbox.DeviceNodes) {
		t.Errorf("devices = %v, want %v", policy.Devices, sandbox.DeviceNodes)
	}
	wantEnv := []string{"CARGO_HOME", "GOPATH", "HOME", "HTTPS_PROXY", "HTTP_PROXY", "LANG", "PATH", "TERM", "http_proxy", "https_proxy"}
	if !slices.Equal(policy.EnvNames, wantEnv) {
		t.Errorf("env_names = %v, want %v", policy.EnvNames, wantEnv)
	}
	if !slices.Equal(policy.Capabilities, caps.Names()) {
		t.Errorf("capabilities = %v, want %v", policy.Capabilities, caps.Names())
	}
	if !slices.Equal(policy.NetworkAllow, ex.NetworkAllow) {
		t.Errorf("network_allow = %v, want %v", policy.NetworkAllow, ex.NetworkAllow)
	}

	// The hash is SHA-256 of the policy object's canonical JSON, as the
	// manifest carries it.
	checkPolicyHash(t, rec, sandbox.Policy{
		Path: ex.Path, ReadOnlyPaths: ex.ReadOnlyPaths, Devices: sandbox.DeviceNodes,
		EnvNames: wantEnv, Capabilities: caps, NetworkAllow: ex.NetworkAllow,
	})

	var backend string
	if err := json.Unmarshal(member["backend"], &backend); err != nil || backend != "fake" {
		t.Errorf("backend = %s, want \"fake\"", member["backend"])
	}
	if string(member["config"]) != "{}" {
		t.Errorf("config = %s, want {}", member["config"])
	}
	if string(member["proxy"]) != "true" || string(rec["proxy"]) != "true" {
		t.Errorf("proxy = %s, preset proxy = %s, want true", member["proxy"], rec["proxy"])
	}
	if got := string(rec["network_allow"]); got != `["proxy.golang.org","*.crates.io"]` {
		t.Errorf("network_allow = %s", got)
	}
	if _, ok := member["notice"]; ok {
		t.Errorf("notice under sandbox = required: %s", member["notice"])
	}
}

func TestBuild_SandboxNoNetworkListNoProxy(t *testing.T) {
	roster := execTestRoster(&config.ExecConfig{})
	caps := sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D)
	member := buildSandboxMember(t, roster, NewSandbox(roster, sandbox.NewFake(caps)))
	rec := presetMember(t, member, "exec")
	if string(member["proxy"]) != "false" || string(rec["proxy"]) != "false" {
		t.Errorf("proxy = %s, preset proxy = %s, want false", member["proxy"], rec["proxy"])
	}
	if string(rec["network_allow"]) != "[]" {
		t.Errorf("network_allow = %s, want []", rec["network_allow"])
	}
	var policy struct {
		Path     string   `json:"path"`
		EnvNames []string `json:"env_names"`
	}
	if err := json.Unmarshal(rec["policy"], &policy); err != nil {
		t.Fatal(err)
	}
	if policy.Path != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("default path = %q", policy.Path)
	}
	if !slices.Equal(policy.EnvNames, sandbox.FixedNames) {
		t.Errorf("env_names = %v, want %v", policy.EnvNames, sandbox.FixedNames)
	}
}

func TestBuild_SandboxNoneCarriesNotice(t *testing.T) {
	if SandboxNoneNotice != "exec: sandbox none; filesystem and network confinement are unenforced" {
		t.Fatalf("notice = %q", SandboxNoneNotice)
	}
	roster := execTestRoster(&config.ExecConfig{Sandbox: config.ExecSandboxNone})
	member := buildSandboxMember(t, roster, NewSandbox(roster, sandbox.NewNone(sandbox.Supervisor{})))
	var notice, backend string
	if err := json.Unmarshal(member["notice"], &notice); err != nil || notice != SandboxNoneNotice {
		t.Errorf("notice = %s, want %q", member["notice"], SandboxNoneNotice)
	}
	if err := json.Unmarshal(member["backend"], &backend); err != nil || backend != sandbox.NoneName {
		t.Errorf("backend = %s, want %q", member["backend"], sandbox.NoneName)
	}
	if string(member["proxy"]) != "false" {
		t.Errorf("proxy = %s, want false", member["proxy"])
	}
}

func TestBuild_SandboxUnavailableBackendIsNull(t *testing.T) {
	roster := execTestRoster(&config.ExecConfig{})
	member := buildSandboxMember(t, roster, NewSandbox(roster, nil))
	if string(member["backend"]) != "null" {
		t.Errorf("backend = %s, want null", member["backend"])
	}
	if _, ok := presetMember(t, member, "exec")["policy_hash"]; !ok {
		t.Errorf("no policy_hash")
	}
}

func TestBuild_SandboxPresetNetworkListOverride(t *testing.T) {
	// The roster's list is empty; one preset sets its own, which replaces it,
	// and a second exec preset keeps the roster's.
	roster := execTestRoster(&config.ExecConfig{EnvPassthrough: []string{"GOPATH"}})
	exec := roster.Presets["exec"]
	exec.NetworkAllow = []string{"proxy.golang.org"}
	roster.Presets["exec"] = exec
	roster.Presets["offline"] = config.Preset{Mode: "review", Author: "p2", RequiredReviewers: []string{"p1"}, Tools: []string{"read", "exec"}}
	caps := sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D)
	member := buildSandboxMember(t, roster, NewSandbox(roster, sandbox.NewFake(caps)))

	ex := roster.ExecSettings()
	online := presetMember(t, member, "exec")
	checkPolicyHash(t, online, sandbox.Policy{
		Path: ex.Path, ReadOnlyPaths: ex.ReadOnlyPaths, Devices: sandbox.DeviceNodes,
		EnvNames:     append(append([]string{"GOPATH"}, sandbox.FixedNames...), sandbox.ProxyVars...),
		Capabilities: caps, NetworkAllow: []string{"proxy.golang.org"},
	})
	if string(online["proxy"]) != "true" || string(online["network_allow"]) != `["proxy.golang.org"]` {
		t.Errorf("exec: proxy = %s, network_allow = %s", online["proxy"], online["network_allow"])
	}

	offline := presetMember(t, member, "offline")
	checkPolicyHash(t, offline, sandbox.Policy{
		Path: ex.Path, ReadOnlyPaths: ex.ReadOnlyPaths, Devices: sandbox.DeviceNodes,
		EnvNames:     append([]string{"GOPATH"}, sandbox.FixedNames...),
		Capabilities: caps, NetworkAllow: []string{},
	})
	if string(offline["proxy"]) != "false" || string(offline["network_allow"]) != "[]" {
		t.Errorf("offline: proxy = %s, network_allow = %s", offline["proxy"], offline["network_allow"])
	}
	if string(online["policy_hash"]) == string(offline["policy_hash"]) {
		t.Errorf("presets with different network lists share policy_hash %s", online["policy_hash"])
	}
	if string(member["proxy"]) != "true" {
		t.Errorf("proxy = %s, want true", member["proxy"])
	}
	var presets map[string]json.RawMessage
	if err := json.Unmarshal(member["presets"], &presets); err != nil || len(presets) != 2 {
		t.Errorf("presets = %s, want exactly the two exec presets", member["presets"])
	}
}

func TestNewSandbox_NilWithoutExec(t *testing.T) {
	roster := execTestRoster(nil)
	delete(roster.Presets, "exec")
	if sb := NewSandbox(roster, sandbox.NewFake(sandbox.Caps(sandbox.D))); sb != nil {
		t.Fatalf("record without any preset granting exec: %+v", sb)
	}
}
