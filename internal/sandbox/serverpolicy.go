package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ServerConfig is what a local MCP server's policy is built from (v9 delta,
// section 6, "Policy").
type ServerConfig struct {
	// System is the command policy's system locations.
	System SystemLocations
	// StateDir is the server's state directory.
	StateDir string
	// TempDir is the instance's temporary directory.
	TempDir string
	// WorkspaceDir is the instance's read-only workspace copy, or empty for
	// none.
	WorkspaceDir string
	// NetworkAllow is mcp.<server>.network_allow.
	NetworkAllow []string
	// EnvNames are the names in mcp.<server>.env.
	EnvNames []string
}

// ServerPolicy is the sandbox policy a local MCP server runs under.
type ServerPolicy struct {
	// System is the closed list of read and execute locations.
	System SystemLocations
	// ReadWrite lists the directories the server may read and write: the
	// state directory and the instance's temporary directory.
	ReadWrite []string
	// ReadOnly lists the instance's workspace copy when there is one.
	ReadOnly []string
	// Capabilities is the capability set in force.
	Capabilities CapSet
	// NetworkAllow is mcp.<server>.network_allow, in configured order.
	NetworkAllow []string
	// EnvNames are the names of the server's environment: v7's fixed names
	// and the listed ones, sorted.
	EnvNames []string

	stateDir, tmpDir, workspaceDir string
}

// NewServerPolicy builds the server policy. A listed environment name that
// ReservedName reserves is refused.
func NewServerPolicy(c ServerConfig) (ServerPolicy, error) {
	names := append([]string{}, FixedNames...)
	for _, n := range c.EnvNames {
		if ReservedName(n) {
			return ServerPolicy{}, fmt.Errorf("sandbox: environment name %q is reserved", n)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	caps := Caps(F, P, D, N1)
	if len(c.NetworkAllow) > 0 {
		caps = caps.Without(N1) | CapSet(N2)
	}
	p := ServerPolicy{
		System:       c.System,
		ReadWrite:    []string{c.StateDir, c.TempDir},
		Capabilities: caps,
		NetworkAllow: append([]string{}, c.NetworkAllow...),
		EnvNames:     names,
		stateDir:     c.StateDir,
		tmpDir:       c.TempDir,
		workspaceDir: c.WorkspaceDir,
	}
	if c.WorkspaceDir != "" {
		p.ReadOnly = []string{c.WorkspaceDir}
	}
	return p, nil
}

// Env builds the server's environment: v7's fixed set with HOME as the state
// directory and TMPDIR as the temporary directory, plus each listed name
// present in source. The result is sorted by name.
func (p ServerPolicy) Env(path, proxyURL string, source map[string]string) []string {
	vars := map[string]string{}
	for _, kv := range Env(EnvConfig{Path: path, TempDir: p.stateDir, ProxyURL: proxyURL}) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	vars["TMPDIR"] = p.tmpDir
	for _, n := range p.EnvNames {
		if v, ok := source[n]; ok && !ReservedName(n) {
			vars[n] = v
		}
	}
	names := make([]string, 0, len(vars))
	for n := range vars {
		names = append(names, n)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, n := range names {
		env = append(env, n+"="+vars[n])
	}
	return env
}

// canonicalServerPolicy fixes the encoding, fields in bytewise key order. The
// per-instance directories appear by role only, never by path.
type canonicalServerPolicy struct {
	Capabilities  []string `json:"capabilities"`
	Devices       []string `json:"devices"`
	EnvNames      []string `json:"env_names"`
	NetworkAllow  []string `json:"network_allow"`
	Path          []string `json:"path"`
	ReadOnlyPaths []string `json:"read_only_paths"`
	Roles         []string `json:"roles"`
}

// CanonicalJSON returns the canonical encoding of what is fixed for a server
// across its instances, in the form Policy.CanonicalJSON uses. The state,
// temporary and workspace directories are named by role (state, tmp,
// workspace; the last only with a workspace copy).
func (p ServerPolicy) CanonicalJSON() ([]byte, error) {
	roles := []string{"state", "tmp"}
	if p.workspaceDir != "" {
		roles = append(roles, "workspace")
	}
	sort.Strings(roles)
	c := canonicalServerPolicy{
		Capabilities:  p.Capabilities.Names(),
		Devices:       orEmpty(p.System.Devices),
		EnvNames:      orEmpty(p.EnvNames),
		NetworkAllow:  orEmpty(p.NetworkAllow),
		Path:          orEmpty(p.System.Path),
		ReadOnlyPaths: orEmpty(p.System.ReadOnlyPaths),
		Roles:         roles,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Hash returns the lowercase hex SHA-256 of the canonical encoding.
func (p ServerPolicy) Hash() (string, error) {
	b, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
