package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func execRoster(mutate func(*Roster)) *Roster {
	r := makeBaseRoster()
	r.Endpoints[0].RefereeModel = "referee"
	r.Presets["x"] = Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}, Tools: []string{"read", "exec"}}
	if mutate != nil {
		mutate(r)
	}
	r.ApplyDefaults()
	return r
}

func ensureExec(r *Roster) *ExecConfig {
	if r.Tools == nil {
		r.Tools = &ToolsConfig{}
	}
	if r.Tools.Exec == nil {
		r.Tools.Exec = &ExecConfig{}
	}
	return r.Tools.Exec
}

// pinExecEnv points HOME, TMPDIR and the XDG directories at a fresh layout so
// the location checks do not depend on the host. It returns the layout's root.
func pinExecEnv(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("TMPDIR", filepath.Join(base, "tmp"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, "run"))
	return base
}

func TestExecRules(t *testing.T) {
	pinExecEnv(t)
	solo := func(tools ...string) func(*Roster) {
		return func(r *Roster) {
			r.Presets["x"] = Preset{Mode: "solo", Author: "author", Tools: tools}
		}
	}
	tests := []struct {
		name    string
		mutate  func(*Roster)
		wantErr string // empty: accepted
	}{
		{"exec with read accepted", nil, ""},
		{"exec without read", func(r *Roster) {
			r.Presets["x"] = Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}, Tools: []string{"exec"}}
		}, "include exec but not read"},
		{"reviewer exec with read accepted", func(r *Roster) {
			r.Presets["x"] = Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}, ReviewerTools: []string{"read", "exec"}}
		}, ""},
		{"reviewer exec without read", func(r *Roster) {
			r.Presets["x"] = Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}, ReviewerTools: []string{"exec"}}
		}, "reviewer_tools include exec but not read"},
		{"unknown group refused", func(r *Roster) {
			r.Presets["x"] = Preset{Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}, Tools: []string{"shell"}}
		}, `invalid author tool "shell"`},
		{"sandbox required accepted", func(r *Roster) { ensureExec(r).Sandbox = "required" }, ""},
		{"sandbox none accepted", func(r *Roster) { ensureExec(r).Sandbox = "none" }, ""},
		{"sandbox invalid", func(r *Roster) { ensureExec(r).Sandbox = "maybe" }, "tools.exec.sandbox"},
		{"endpoint without referee_model", func(r *Roster) { r.Endpoints[0].RefereeModel = "" }, `endpoint "ep1"`},
		{"referee_endpoint does not satisfy the participant endpoint", func(r *Roster) {
			r.Endpoints[0].RefereeModel = ""
			r.Endpoints[0].RefereeEndpoint = "ep2"
			r.Endpoints = append(r.Endpoints, EndpointProfile{Name: "ep2", Dialect: "chat_completions_v1", DeclaredLocation: "local", DeclaredExecution: "local", DeclaredBilling: "none", NetworkDependency: "none", RefereeModel: "m"})
		}, `endpoint "ep1"`},
		{"max_output_bytes at the limit", func(r *Roster) {
			r.Tools = &ToolsConfig{MaxResultBytes: ptrInt(10000)}
			ensureExec(r).MaxOutputBytes = ptrInt(10000 - 1024)
		}, ""},
		{"max_output_bytes over the limit", func(r *Roster) {
			r.Tools = &ToolsConfig{MaxResultBytes: ptrInt(10000)}
			ensureExec(r).MaxOutputBytes = ptrInt(10000 - 1023)
		}, "max_output_bytes"},
		{"timeout_s equal to max accepted", func(r *Roster) { ensureExec(r).TimeoutS = ptrInt(600) }, ""},
		{"timeout_s over max_timeout_s", func(r *Roster) { ensureExec(r).TimeoutS = ptrInt(601) }, "timeout_s"},
		{"bound below one", func(r *Roster) { ensureExec(r).MaxCalls = ptrInt(0) }, "tools.exec.max_calls must be at least 1"},
		{"kill_grace_s below one", func(r *Roster) { ensureExec(r).KillGraceS = ptrInt(0) }, "kill_grace_s"},
		{"bound of one accepted", func(r *Roster) { ensureExec(r).MaxCalls = ptrInt(1) }, ""},
		{"path in the home directory", func(r *Roster) { ensureExec(r).Path = filepath.Join(homeForTest(), "bin") }, "tools.exec.path"},
		{"path containing the home directory", func(r *Roster) { ensureExec(r).Path = "/" }, "tools.exec.path"},
		{"path matching a protected pattern", func(r *Roster) { ensureExec(r).Path = "/opt/.git/bin" }, "protected"},
		{"extra system path accepted", func(r *Roster) { ensureExec(r).Path = "/usr/bin:/opt/tools/bin" }, ""},
		{"read_only_paths in the home directory", func(r *Roster) { ensureExec(r).ReadOnlyPaths = []string{homeForTest()} }, "read_only_paths"},
		{"read_only_paths inside syndic state", func(r *Roster) {
			ensureExec(r).ReadOnlyPaths = []string{filepath.Join(homeForTest(), ".local", "state", "syndic", "journal")}
		}, "read_only_paths"},
		{"read_only_paths inside the temporary area", func(r *Roster) {
			ensureExec(r).ReadOnlyPaths = []string{filepath.Join(os.TempDir(), "syndic-sbx-x")}
		}, "read_only_paths"},
		{"read_only_paths containing the temporary area", func(r *Roster) { ensureExec(r).ReadOnlyPaths = []string{filepath.Dir(os.TempDir())} }, "read_only_paths"},
		{"read_only_paths matching a protected pattern", func(r *Roster) {
			r.Tools = &ToolsConfig{ProtectedPaths: []string{"**/secrets/"}}
			ensureExec(r).ReadOnlyPaths = []string{"/opt/secrets"}
		}, "protected"},
		{"read_only_paths replaced with a safe list", func(r *Roster) { ensureExec(r).ReadOnlyPaths = []string{"/usr", "/opt/sdk"} }, ""},
		{"env_passthrough PATH", func(r *Roster) { ensureExec(r).EnvPassthrough = []string{"PATH"} }, "env_passthrough"},
		{"env_passthrough BASH_ENV", func(r *Roster) { ensureExec(r).EnvPassthrough = []string{"BASH_ENV"} }, "env_passthrough"},
		{"env_passthrough lower-case proxy", func(r *Roster) { ensureExec(r).EnvPassthrough = []string{"https_proxy"} }, "env_passthrough"},
		{"env_passthrough other name accepted", func(r *Roster) { ensureExec(r).EnvPassthrough = []string{"GOFLAGS", "CI"} }, ""},
		{"none with roster network_allow", func(r *Roster) {
			e := ensureExec(r)
			e.Sandbox = "none"
			e.NetworkAllow = []string{"example.com"}
		}, "network_allow must be empty"},
		{"none with preset network_allow", func(r *Roster) {
			ensureExec(r).Sandbox = "none"
			p := r.Presets["x"]
			p.NetworkAllow = []string{"example.com"}
			r.Presets["x"] = p
		}, "network_allow must be empty"},
		{"none with empty lists accepted", func(r *Roster) { ensureExec(r).Sandbox = "none" }, ""},
		{"none with exec in solo", func(r *Roster) { ensureExec(r).Sandbox = "none"; solo("read", "exec")(r) }, "solo mode cannot have exec"},
		{"required with exec in solo accepted", func(r *Roster) { solo("read", "exec")(r) }, ""},
		{"none without exec in solo accepted", func(r *Roster) { ensureExec(r).Sandbox = "none"; solo("read")(r) }, ""},
		{"allow_web_with_read false with network list", func(r *Roster) {
			r.Tools = &ToolsConfig{AllowWebWithRead: ptrBool(false)}
			ensureExec(r).NetworkAllow = []string{"example.com"}
		}, "allow_web_with_read=false"},
		{"allow_web_with_read false with preset network list", func(r *Roster) {
			f := false
			p := r.Presets["x"]
			p.AllowWebWithRead = &f
			p.NetworkAllow = []string{"example.com"}
			r.Presets["x"] = p
		}, "allow_web_with_read=false"},
		{"allow_web_with_read false with preset clearing the list", func(r *Roster) {
			f := false
			ensureExec(r).NetworkAllow = []string{"example.com"}
			p := r.Presets["x"]
			p.AllowWebWithRead = &f
			p.NetworkAllow = []string{}
			r.Presets["x"] = p
		}, ""},
		{"allow_web_with_read false with empty list accepted", func(r *Roster) { r.Tools = &ToolsConfig{AllowWebWithRead: ptrBool(false)} }, ""},
		{"network_allow pattern accepted", func(r *Roster) {
			ensureExec(r).NetworkAllow = []string{"example.com", "*.example.org", "**.example.net"}
		}, ""},
		{"network_allow IP literal", func(r *Roster) { ensureExec(r).NetworkAllow = []string{"192.0.2.1"} }, "network_allow"},
		{"network_allow path", func(r *Roster) { ensureExec(r).NetworkAllow = []string{"example.com/x"} }, "network_allow"},
		{"preset network_allow IP literal", func(r *Roster) {
			p := r.Presets["x"]
			p.NetworkAllow = []string{"2001:db8::1"}
			r.Presets["x"] = p
		}, `preset "x": network_allow`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := execRoster(tt.mutate).Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func ptrInt(i int) *int { return &i }

func homeForTest() string {
	h, _ := os.UserHomeDir()
	return h
}

func TestExecDefaults(t *testing.T) {
	r := makeBaseRoster()
	r.ApplyDefaults()
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	ex := r.ExecSettings()
	if ex.Sandbox != "required" || ex.TimeoutS != 120 || ex.MaxTimeoutS != 600 || ex.MaxOutputBytes != 61440 ||
		ex.MaxCalls != 20 || ex.MaxReviewerCalls != 10 || ex.Path != "/usr/local/bin:/usr/bin:/bin" ||
		ex.MaxWorkspaceBytes != 1073741824 || ex.MaxWorkspaceFiles != 100000 || ex.KillGraceS != 5 || ex.CleanupDeadlineS != 10 {
		t.Errorf("defaults = %+v", ex)
	}
	if len(ex.Exclude) != 1 || ex.Exclude[0] != ".git/" || len(ex.EnvPassthrough) != 0 || len(ex.NetworkAllow) != 0 {
		t.Errorf("list defaults = %+v", ex)
	}
	if want := DefaultExecReadOnlyPaths(); strings.Join(ex.ReadOnlyPaths, ",") != strings.Join(want, ",") || len(want) == 0 {
		t.Errorf("read_only_paths = %v, want %v", ex.ReadOnlyPaths, want)
	}
	if *r.Tools.Referee.MaxReads != 6 || *r.Tools.Referee.ExecMaxOutputTokens != 256 {
		t.Error("referee defaults changed")
	}
}

func TestExecLoadAndResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roster.toml")
	src := `
default_preset = "p"
[[participants]]
id = "a"
model = "m"
route = "api"
endpoint = "e"
eligible_roles = ["author"]
[[endpoints]]
name = "e"
dialect = "chat_completions_v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
referee_model = "r"
[presets.p]
mode = "solo"
author = "a"
tools = ["read", "exec"]
network_allow = ["example.com"]
[presets.q]
mode = "solo"
author = "a"
tools = ["read"]
[tools.exec]
timeout_s = 30
network_allow = ["roster.example.com"]
env_passthrough = ["GOFLAGS"]
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	eff, err := r.Resolve("p", "")
	if err != nil {
		t.Fatal(err)
	}
	if eff.Exec.TimeoutS != 30 || eff.Exec.MaxTimeoutS != 600 || len(eff.NetworkAllow) != 1 || eff.NetworkAllow[0] != "example.com" {
		t.Errorf("effective = %+v %v", eff.Exec, eff.NetworkAllow)
	}
	eff, err = r.Resolve("q", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.NetworkAllow) != 1 || eff.NetworkAllow[0] != "roster.example.com" || eff.Exec.EnvPassthrough[0] != "GOFLAGS" {
		t.Errorf("effective = %+v %v", eff.Exec, eff.NetworkAllow)
	}
}

func TestCheckExecWorkspaceRoot(t *testing.T) {
	base := pinExecEnv(t)
	r := execRoster(nil)
	for _, root := range []string{"/usr", "/usr/src/project", "/"} {
		if err := r.CheckExecWorkspaceRoot(root); err == nil {
			t.Errorf("root %q accepted, want refusal", root)
		}
	}
	for _, root := range []string{"/home/someone/project", filepath.Join(base, "work")} {
		if err := r.CheckExecWorkspaceRoot(root); err != nil {
			t.Errorf("root %q refused: %v", root, err)
		}
	}
}

func TestExecSyndicAreasOutsideHome(t *testing.T) {
	base := pinExecEnv(t)
	for _, area := range []string{"config", "state", "data", "run"} {
		refused := filepath.Join(base, area, "syndic", "x")
		neighbour := filepath.Join(base, area, "other")
		err := execRoster(func(r *Roster) { ensureExec(r).ReadOnlyPaths = []string{refused} }).Validate()
		if err == nil || !strings.Contains(err.Error(), "read_only_paths") {
			t.Errorf("%s area: %q accepted, want refusal (%v)", area, refused, err)
		}
		if err := execRoster(func(r *Roster) { ensureExec(r).ReadOnlyPaths = []string{neighbour} }).Validate(); err != nil {
			t.Errorf("%s area: neighbour %q refused: %v", area, neighbour, err)
		}
	}
}

func TestExecLocationChecksNeedExec(t *testing.T) {
	t.Setenv("HOME", "/")
	r := makeBaseRoster()
	r.ApplyDefaults()
	if err := r.Validate(); err != nil {
		t.Fatalf("roster without exec refused under HOME=/: %v", err)
	}
	if err := execRoster(nil).Validate(); err == nil {
		t.Fatal("roster granting exec accepted under HOME=/, want refusal")
	}
}
