// Package mcp holds the engine's handling of MCP tools. This file derives the
// provider names under which a model is offered each tool, and the mapping
// that returns a call to its server and tool (v9 delta, section 2).
//
// It offers no tool, journals nothing and dispatches no call.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
)

// maxSuffixBytes bounds the part of a tool's name kept in its provider name.
const maxSuffixBytes = 40

var (
	serverNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	toolNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	providerPattern   = regexp.MustCompile(`^mcp_[0-9]+_[A-Za-z0-9_]{1,40}$`)
)

// Tool names one tool of one server.
type Tool struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

// Entry is one tool with the name a model is offered it under.
type Entry struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

// Mapping is the entries for one participant's turn, in index order.
type Mapping struct {
	Entries []Entry
}

// BuildMapping sorts tools bytewise by server and then tool name and names
// each by its index. A name outside the configuration patterns, or a repeated
// server and tool, is refused.
func BuildMapping(tools []Tool) (*Mapping, error) {
	sorted := make([]Tool, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Server != sorted[j].Server {
			return sorted[i].Server < sorted[j].Server
		}
		return sorted[i].Tool < sorted[j].Tool
	})
	m := &Mapping{Entries: make([]Entry, 0, len(sorted))}
	for i, t := range sorted {
		if err := validateTool(t); err != nil {
			return nil, err
		}
		if i > 0 && sorted[i-1] == t {
			return nil, fmt.Errorf("mcp: tool %q of server %q is listed twice", t.Tool, t.Server)
		}
		m.Entries = append(m.Entries, Entry{Name: providerName(i, t.Tool), Server: t.Server, Tool: t.Tool})
	}
	return m, nil
}

func validateTool(t Tool) error {
	if !serverNamePattern.MatchString(t.Server) {
		return fmt.Errorf("mcp: server name %q is outside [a-z0-9_-]{1,32}", t.Server)
	}
	if !toolNamePattern.MatchString(t.Tool) {
		return fmt.Errorf("mcp: tool name %q is outside [A-Za-z0-9_.-]{1,128}", t.Tool)
	}
	return nil
}

func providerName(index int, tool string) string {
	s := []byte(tool)
	if len(s) > maxSuffixBytes {
		s = s[:maxSuffixBytes]
	}
	for i, b := range s {
		if !isSafeByte(b) {
			s[i] = '_'
		}
	}
	return "mcp_" + strconv.Itoa(index) + "_" + string(s)
}

// Lookup returns the server and tool a provider name stands for.
func (m *Mapping) Lookup(name string) (Tool, bool) {
	for _, e := range m.Entries {
		if e.Name == name {
			return Tool{Server: e.Server, Tool: e.Tool}, true
		}
	}
	return Tool{}, false
}

// Serialize returns the canonical record of the mapping: a compact JSON array
// of entries in index order, each with members name, server and tool.
func (m *Mapping) Serialize() ([]byte, error) {
	entries := m.Entries
	if entries == nil {
		entries = []Entry{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entries); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ParseMapping reads a serialised mapping and checks it is well formed: names
// match the provider pattern and carry their own index, and no tool repeats.
func ParseMapping(data []byte) (*Mapping, error) {
	var entries []Entry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("mcp: parse mapping: %w", err)
	}
	if entries == nil {
		return nil, fmt.Errorf("mcp: parse mapping: not an array")
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("mcp: parse mapping: trailing data")
	}
	tools := make([]Tool, len(entries))
	for i, e := range entries {
		tools[i] = Tool{Server: e.Server, Tool: e.Tool}
	}
	m, err := BuildMapping(tools)
	if err != nil {
		return nil, err
	}
	for i, e := range entries {
		if m.Entries[i] != e || !providerPattern.MatchString(e.Name) {
			return nil, fmt.Errorf("mcp: parse mapping: entry %d does not match the derived mapping", i)
		}
	}
	return m, nil
}

// DescriptionLine is the engine-written first line of a tool's description.
// It refuses a name configuration would not allow.
func DescriptionLine(t Tool) (string, error) {
	if err := validateTool(t); err != nil {
		return "", err
	}
	return "MCP tool " + t.Tool + " of server " + t.Server + ".", nil
}

func isSafeByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_'
}
