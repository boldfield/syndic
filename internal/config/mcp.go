package config

import (
	"bytes"
	"fmt"
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
// Transport-specific settings are not part of this type yet.
type MCPServer struct {
	Transport  string            `toml:"transport"`   // "stdio" or "http"
	Tools      map[string]string `toml:"tools"`       // tool name -> "read" or "effect"
	FileEgress bool              `toml:"file_egress"` // default false
	TimeoutS   *int              `toml:"timeout_s"`   // default 60, maximum 300
	Protocol   string            `toml:"protocol"`    // default "auto"
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
	}
	return errs
}
