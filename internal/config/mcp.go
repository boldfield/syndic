package config

import (
	"bytes"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// MCP transports, tool classes and protocol revisions (v9 sections 2 and 3).
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"

	MCPClassRead   = "read"
	MCPClassEffect = "effect"

	MCPProtocolAuto = "auto"

	MCPInstanceTurn = "turn"
	MCPInstanceRun  = "run"

	MCPStateFresh      = "fresh"
	MCPStatePersistent = "persistent"

	MCPWorkspaceNone    = "none"
	MCPWorkspaceScratch = "scratch"

	DefaultMCPTimeoutS = 60
	MaxMCPTimeoutS     = 300
)

// MCPProtocolRevisions lists the explicit protocol revisions a server may pin.
var MCPProtocolRevisions = []string{"2026-07-28", "2025-11-25"}

var (
	mcpServerNameRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	mcpToolNameRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

// MCPServer is one [mcp.<server>] table: the settings every transport shares.
// The local-only and remote-only settings follow; a setting of one transport
// on the other is refused at load (v9 section 6 and section 7).
type MCPServer struct {
	Transport  string            `toml:"transport"`   // "stdio" or "http"
	Tools      map[string]string `toml:"tools"`       // tool name -> "read" or "effect"
	FileEgress bool              `toml:"file_egress"` // default false
	TimeoutS   *int              `toml:"timeout_s"`   // default 60, maximum 300
	Protocol   string            `toml:"protocol"`    // default "auto"

	// Local (stdio) settings.
	Command      string               `toml:"command"`
	Args         []string             `toml:"args"`
	Env          map[string]SecretRef `toml:"env"`           // names whose values are secret references
	Instance     string               `toml:"instance"`      // "turn" (default) or "run"
	State        string               `toml:"state"`         // "fresh" (default) or "persistent"
	Workspace    string               `toml:"workspace"`     // "none" (default) or "scratch"
	NetworkAllow []string             `toml:"network_allow"` // hosts the instance's proxy may reach

	// Remote (http) settings.
	URL          string     `toml:"url"`
	Auth         *SecretRef `toml:"auth"`
	AddressAllow []string   `toml:"address_allow"` // private ranges the server's host may resolve into
	AuthOrigins  []string   `toml:"auth_origins"`  // origins the login may use
	Scopes       []string   `toml:"scopes"`
	ClientID     string     `toml:"client_id"`
}

// MCPConfig is the [mcp] table: the mcp.max_* limits and one sub-table per
// server. Absent limits take their design default in ApplyDefaults.
type MCPConfig struct {
	MaxDefinitionBytes    *int `toml:"max_definition_bytes"`     // default 16384
	MaxTools              *int `toml:"max_tools"`                // default 256
	MaxMessageBytes       *int `toml:"max_message_bytes"`        // default 4194304
	MaxStderrBytes        *int `toml:"max_stderr_bytes"`         // default 1048576
	MaxArgumentBytes      *int `toml:"max_argument_bytes"`       // default 16384
	MaxCalls              *int `toml:"max_calls"`                // default 20, author per turn
	MaxReviewerCalls      *int `toml:"max_reviewer_calls"`       // default 10, reviewer per round
	MaxEffects            *int `toml:"max_effects"`              // default 8
	MaxStringBytes        *int `toml:"max_string_bytes"`         // default 2048
	MaxHistoryResultBytes *int `toml:"max_history_result_bytes"` // default 4096

	Servers map[string]MCPServer `toml:"-"`
}

// UnmarshalTOML decodes [mcp], whose keys are both limits and server names.
// Unknown keys are refused, as at the top level.
func (m *MCPConfig) UnmarshalTOML(data any) error {
	tbl, ok := data.(map[string]any)
	if !ok {
		return fmt.Errorf("mcp must be a table")
	}
	limits := map[string]any{}
	servers := map[string]any{}
	for k, v := range tbl {
		if _, isTable := v.(map[string]any); isTable {
			servers[k] = v
		} else {
			limits[k] = v
		}
	}
	// The alias drops UnmarshalTOML, which would otherwise recurse.
	type plain MCPConfig
	if err := decodeStrict(limits, (*plain)(m), "mcp"); err != nil {
		return err
	}
	m.Servers = nil
	for name, v := range servers {
		var s MCPServer
		if err := decodeStrict(v, &s, "mcp."+name); err != nil {
			return err
		}
		if m.Servers == nil {
			m.Servers = map[string]MCPServer{}
		}
		m.Servers[name] = s
	}
	return nil
}

func decodeStrict(v any, out any, where string) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(v); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	md, err := toml.Decode(buf.String(), out)
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		keys := make([]string, len(u))
		for i, k := range u {
			keys[i] = where + "." + k.String()
		}
		return fmt.Errorf("unknown keys in TOML: %v", keys)
	}
	return nil
}

func (m *MCPConfig) applyDefaults() {
	set := func(p **int, v int) {
		if *p == nil {
			*p = &v
		}
	}
	set(&m.MaxDefinitionBytes, 16384)
	set(&m.MaxTools, 256)
	set(&m.MaxMessageBytes, 4194304)
	set(&m.MaxStderrBytes, 1048576)
	set(&m.MaxArgumentBytes, 16384)
	set(&m.MaxCalls, 20)
	set(&m.MaxReviewerCalls, 10)
	set(&m.MaxEffects, 8)
	set(&m.MaxStringBytes, 2048)
	set(&m.MaxHistoryResultBytes, 4096)
	for name, s := range m.Servers {
		if s.TimeoutS == nil {
			t := DefaultMCPTimeoutS
			s.TimeoutS = &t
		}
		if s.Protocol == "" {
			s.Protocol = MCPProtocolAuto
		}
		if s.Transport == MCPTransportStdio {
			if s.Instance == "" {
				s.Instance = MCPInstanceTurn
			}
			if s.State == "" {
				s.State = MCPStateFresh
			}
			if s.Workspace == "" {
				s.Workspace = MCPWorkspaceNone
			}
		}
		m.Servers[name] = s
	}
}

// validateMCP applies the roster-load rules for [mcp] (v9 section 2).
func (r *Roster) validateMCP() []error {
	m := r.MCP
	if m == nil {
		return nil
	}
	var errs []error
	for _, l := range []struct {
		name string
		v    *int
	}{
		{"max_definition_bytes", m.MaxDefinitionBytes},
		{"max_tools", m.MaxTools},
		{"max_message_bytes", m.MaxMessageBytes},
		{"max_stderr_bytes", m.MaxStderrBytes},
		{"max_argument_bytes", m.MaxArgumentBytes},
		{"max_calls", m.MaxCalls},
		{"max_reviewer_calls", m.MaxReviewerCalls},
		{"max_effects", m.MaxEffects},
		{"max_string_bytes", m.MaxStringBytes},
		{"max_history_result_bytes", m.MaxHistoryResultBytes},
	} {
		if l.v != nil && *l.v < 1 {
			errs = append(errs, fmt.Errorf("mcp.%s must be at least 1, got %d", l.name, *l.v))
		}
	}

	names := make([]string, 0, len(m.Servers))
	for n := range m.Servers {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		s := m.Servers[name]
		if !mcpServerNameRE.MatchString(name) {
			errs = append(errs, fmt.Errorf("mcp server name %q must match [a-z0-9_-]{1,32}", name))
		}
		where := fmt.Sprintf("mcp.%s", name)
		if s.Transport != MCPTransportStdio && s.Transport != MCPTransportHTTP {
			errs = append(errs, fmt.Errorf("%s.transport must be %q or %q, got %q", where, MCPTransportStdio, MCPTransportHTTP, s.Transport))
		}
		if s.TimeoutS != nil && (*s.TimeoutS < 1 || *s.TimeoutS > MaxMCPTimeoutS) {
			errs = append(errs, fmt.Errorf("%s.timeout_s must be between 1 and %d, got %d", where, MaxMCPTimeoutS, *s.TimeoutS))
		}
		if s.Protocol != "" && s.Protocol != MCPProtocolAuto && !slices.Contains(MCPProtocolRevisions, s.Protocol) {
			errs = append(errs, fmt.Errorf("%s.protocol must be %q or one of %s, got %q", where, MCPProtocolAuto, strings.Join(MCPProtocolRevisions, ", "), s.Protocol))
		}
		tools := make([]string, 0, len(s.Tools))
		for t := range s.Tools {
			tools = append(tools, t)
		}
		slices.Sort(tools)
		for _, t := range tools {
			if !mcpToolNameRE.MatchString(t) {
				errs = append(errs, fmt.Errorf("%s.tools: tool name %q must match [A-Za-z0-9_.-]{1,128}", where, t))
			}
			if c := s.Tools[t]; c != MCPClassRead && c != MCPClassEffect {
				errs = append(errs, fmt.Errorf("%s.tools.%q: class must be %q or %q, got %q", where, t, MCPClassRead, MCPClassEffect, c))
			}
		}
		switch s.Transport {
		case MCPTransportStdio:
			errs = append(errs, r.validateMCPLocal(where, s)...)
		case MCPTransportHTTP:
			errs = append(errs, validateMCPRemote(where, s)...)
		}
	}
	return errs
}

// validateMCPLocal applies the load rules for a stdio server (v9 section 6).
func (r *Roster) validateMCPLocal(where string, s MCPServer) []error {
	var errs []error
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"url", s.URL != ""},
		{"auth", s.Auth != nil},
		{"address_allow", len(s.AddressAllow) > 0},
		{"auth_origins", len(s.AuthOrigins) > 0},
		{"scopes", len(s.Scopes) > 0},
		{"client_id", s.ClientID != ""},
	} {
		if f.set {
			errs = append(errs, fmt.Errorf("%s.%s is a setting of remote servers; transport is %q", where, f.name, s.Transport))
		}
	}
	if s.Command == "" {
		errs = append(errs, fmt.Errorf("%s.command is required for a %q server", where, s.Transport))
	}
	envNames := make([]string, 0, len(s.Env))
	for n := range s.Env {
		envNames = append(envNames, n)
	}
	slices.Sort(envNames)
	for _, n := range envNames {
		if n == "" || strings.Contains(n, "=") {
			errs = append(errs, fmt.Errorf("%s.env: name %q is not a valid environment variable name", where, n))
		}
		if err := validateMCPSecretRef(s.Env[n]); err != nil {
			errs = append(errs, fmt.Errorf("%s.env.%s: %w", where, n, err))
		}
	}
	if s.Instance != "" && s.Instance != MCPInstanceTurn && s.Instance != MCPInstanceRun {
		errs = append(errs, fmt.Errorf("%s.instance must be %q or %q, got %q", where, MCPInstanceTurn, MCPInstanceRun, s.Instance))
	}
	if s.State != "" && s.State != MCPStateFresh && s.State != MCPStatePersistent {
		errs = append(errs, fmt.Errorf("%s.state must be %q or %q, got %q", where, MCPStateFresh, MCPStatePersistent, s.State))
	}
	if s.Workspace != "" && s.Workspace != MCPWorkspaceNone && s.Workspace != MCPWorkspaceScratch {
		errs = append(errs, fmt.Errorf("%s.workspace must be %q or %q, got %q", where, MCPWorkspaceNone, MCPWorkspaceScratch, s.Workspace))
	}
	scratch := s.Workspace == MCPWorkspaceScratch
	if s.Instance == MCPInstanceRun && scratch {
		errs = append(errs, fmt.Errorf("%s: instance = %q cannot be combined with workspace = %q", where, MCPInstanceRun, MCPWorkspaceScratch))
	}
	if scratch {
		tools := make([]string, 0, len(s.Tools))
		for t, c := range s.Tools {
			if c == MCPClassEffect {
				tools = append(tools, t)
			}
		}
		slices.Sort(tools)
		for _, t := range tools {
			errs = append(errs, fmt.Errorf("%s.tools.%q: an effect tool cannot be on a server with workspace = %q", where, t, MCPWorkspaceScratch))
		}
	}
	for _, pattern := range s.NetworkAllow {
		if err := validateNetworkAllowPattern(pattern); err != nil {
			errs = append(errs, fmt.Errorf("%s.network_allow: %w", where, err))
		}
	}
	if len(s.NetworkAllow) > 0 && r.ExecSettings().Sandbox == ExecSandboxNone {
		errs = append(errs, fmt.Errorf("%s.network_allow must be empty under tools.exec.sandbox = %q", where, ExecSandboxNone))
	}
	return errs
}

// mcpPrivateRanges are the ranges address_allow may lie inside: RFC 1918 and
// the unique-local range.
var mcpPrivateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// validateMCPRemote applies the load rules for an http server (v9 section 7).
func validateMCPRemote(where string, s MCPServer) []error {
	var errs []error
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"command", s.Command != ""},
		{"args", len(s.Args) > 0},
		{"env", len(s.Env) > 0},
		{"instance", s.Instance != ""},
		{"state", s.State != ""},
		{"workspace", s.Workspace != ""},
		{"network_allow", len(s.NetworkAllow) > 0},
	} {
		if f.set {
			errs = append(errs, fmt.Errorf("%s.%s is a setting of local servers; transport is %q", where, f.name, s.Transport))
		}
	}
	if s.URL == "" {
		errs = append(errs, fmt.Errorf("%s.url is required for an %q server", where, s.Transport))
	} else if err := validateMCPURL(s.URL); err != nil {
		errs = append(errs, fmt.Errorf("%s.url: %w", where, err))
	}
	if s.Auth != nil {
		if err := validateMCPSecretRef(*s.Auth); err != nil {
			errs = append(errs, fmt.Errorf("%s.auth: %w", where, err))
		}
	}
	for _, a := range s.AddressAllow {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			addr, aerr := netip.ParseAddr(a)
			if aerr != nil {
				errs = append(errs, fmt.Errorf("%s.address_allow: %q is not an address range", where, a))
				continue
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		inside := false
		for _, r := range mcpPrivateRanges {
			if p.Addr().BitLen() == r.Addr().BitLen() && p.Bits() >= r.Bits() && r.Contains(p.Addr()) {
				inside = true
			}
		}
		if !inside {
			errs = append(errs, fmt.Errorf("%s.address_allow: %q is not inside RFC 1918 or the unique-local range", where, a))
		}
	}
	for _, o := range s.AuthOrigins {
		if err := validateMCPOrigin(o); err != nil {
			errs = append(errs, fmt.Errorf("%s.auth_origins: %q: %w", where, o, err))
		}
	}
	return errs
}

func validateMCPSecretRef(ref SecretRef) error {
	if ref.Source != "env" && ref.Source != "file" {
		return fmt.Errorf("secret source must be %q or %q, got %q", "env", "file", ref.Source)
	}
	if ref.Name == "" {
		return fmt.Errorf("secret reference needs a name")
	}
	return nil
}

func mcpLoopbackLiteral(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

// validateMCPURL requires https, except that a loopback literal host may use
// plain http.
func validateMCPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("not a valid URL")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("needs a host")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !mcpLoopbackLiteral(host) {
			return fmt.Errorf("plain http is allowed only for the host 127.0.0.1, ::1 or localhost, got %q", host)
		}
	default:
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	return nil
}

// validateMCPOrigin requires scheme://host[:port] with no path, and the same
// https rule as a url.
func validateMCPOrigin(raw string) error {
	if err := validateMCPURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("must be an origin: scheme, host and optional port only")
	}
	return nil
}
