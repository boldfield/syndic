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

	var policy struct {
		Path          string   `json:"path"`
		ReadOnlyPaths []string `json:"read_only_paths"`
		Devices       []string `json:"devices"`
		EnvNames      []string `json:"env_names"`
		Capabilities  []string `json:"capabilities"`
		NetworkAllow  []string `json:"network_allow"`
	}
	if err := json.Unmarshal(member["policy"], &policy); err != nil {
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
	var hash string
	if err := json.Unmarshal(member["policy_hash"], &hash); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(member["policy"])
	if hash != hex.EncodeToString(sum[:]) {
		t.Errorf("policy_hash = %s, want SHA-256 of %s", hash, member["policy"])
	}
	want := sandbox.Policy{
		Path: ex.Path, ReadOnlyPaths: ex.ReadOnlyPaths, Devices: sandbox.DeviceNodes,
		EnvNames: wantEnv, Capabilities: caps, NetworkAllow: ex.NetworkAllow,
	}
	canonical, err := want.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(member["policy"]) != string(canonical) {
		t.Errorf("policy = %s, want %s", member["policy"], canonical)
	}

	var backend string
	if err := json.Unmarshal(member["backend"], &backend); err != nil || backend != "fake" {
		t.Errorf("backend = %s, want \"fake\"", member["backend"])
	}
	if string(member["config"]) != "{}" {
		t.Errorf("config = %s, want {}", member["config"])
	}
	if string(member["proxy"]) != "true" {
		t.Errorf("proxy = %s, want true", member["proxy"])
	}
	if got := string(member["network_allow"]); got != `["proxy.golang.org","*.crates.io"]` {
		t.Errorf("network_allow = %s", got)
	}
	if got := string(member["preset_network_allow"]); got != `{"exec":["proxy.golang.org","*.crates.io"]}` {
		t.Errorf("preset_network_allow = %s", got)
	}
	if _, ok := member["notice"]; ok {
		t.Errorf("notice under sandbox = required: %s", member["notice"])
	}
}

func TestBuild_SandboxNoNetworkListNoProxy(t *testing.T) {
	roster := execTestRoster(&config.ExecConfig{})
	caps := sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D)
	member := buildSandboxMember(t, roster, NewSandbox(roster, sandbox.NewFake(caps)))
	if string(member["proxy"]) != "false" {
		t.Errorf("proxy = %s, want false", member["proxy"])
	}
	if string(member["network_allow"]) != "[]" {
		t.Errorf("network_allow = %s, want []", member["network_allow"])
	}
	var policy struct {
		Path     string   `json:"path"`
		EnvNames []string `json:"env_names"`
	}
	if err := json.Unmarshal(member["policy"], &policy); err != nil {
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
	if _, ok := member["policy_hash"]; !ok {
		t.Errorf("no policy_hash")
	}
}

func TestNewSandbox_NilWithoutExec(t *testing.T) {
	roster := execTestRoster(nil)
	delete(roster.Presets, "exec")
	if sb := NewSandbox(roster, sandbox.NewFake(sandbox.Caps(sandbox.D))); sb != nil {
		t.Fatalf("record without any preset granting exec: %+v", sb)
	}
}
