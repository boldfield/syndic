package mcp

// This file is the pin file that sits beside the roster: its format, reading
// and writing it, and the hash a turn and the run manifest record (v9 delta,
// section 2, "Pins").
//
// A Snapshot is read once and never changes; no method of it reads the file
// again.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// PinFileName is the pin file's name, beside the roster.
const PinFileName = "mcp-pins.json"

// ErrPinFile is wrapped by every refusal to load a pin file.
var ErrPinFile = errors.New("mcp: invalid pin file")

func pinFileErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPinFile, fmt.Sprintf(format, args...))
}

// pinEntry is one tool's pin as written in the file.
type pinEntry struct {
	Definition string `json:"definition"`
	SHA256     string `json:"sha256"`
}

type pinFile struct {
	Servers map[string]map[string]pinEntry `json:"servers"`
}

// Snapshot is the pins of a pin file as read, immutable.
type Snapshot struct {
	pins map[string]map[string]pinEntry
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Load reads the pin file at path once, returning the snapshot and the
// SHA-256 (lower-case hex) of the file's bytes as read. A missing file is an
// empty snapshot with the hash of zero bytes. It refuses a duplicate member
// name anywhere, a pin that is not the SHA-256 of its definition's bytes, and
// a definition that would not be offered. Definitions are held to
// DefaultMaxDefinitionBytes.
func Load(path string) (*Snapshot, string, error) {
	return LoadMax(path, DefaultMaxDefinitionBytes)
}

// LoadMax is Load with mcp.max_definition_bytes given.
func LoadMax(path string, maxBytes int) (*Snapshot, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Snapshot{pins: map[string]map[string]pinEntry{}}, sha256Hex(nil), nil
		}
		return nil, "", fmt.Errorf("mcp: read pin file: %w", err)
	}
	hash := sha256Hex(data)
	if _, err := jcs.Canonicalize(data); err != nil {
		return nil, "", pinFileErr("%v", err)
	}
	var f pinFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, "", pinFileErr("%v", err)
	}
	snap := &Snapshot{pins: map[string]map[string]pinEntry{}}
	for server, tools := range f.Servers {
		if !serverNamePattern.MatchString(server) {
			return nil, "", pinFileErr("server name %q is not valid", server)
		}
		snap.pins[server] = map[string]pinEntry{}
		for tool, e := range tools {
			if !toolNamePattern.MatchString(tool) {
				return nil, "", pinFileErr("%s: tool name %q is not valid", server, tool)
			}
			if sha256Hex([]byte(e.Definition)) != e.SHA256 {
				return nil, "", pinFileErr("%s/%s: pin is not the SHA-256 of the definition", server, tool)
			}
			def, err := BuildDefinition([]byte(e.Definition), maxBytes)
			if err != nil {
				return nil, "", pinFileErr("%s/%s: %v", server, tool, err)
			}
			if string(def.Bytes) != e.Definition {
				return nil, "", pinFileErr("%s/%s: definition is not in canonical form", server, tool)
			}
			if def.Name != tool {
				return nil, "", pinFileErr("%s/%s: definition names tool %q", server, tool, def.Name)
			}
			snap.pins[server][tool] = e
		}
	}
	return snap, hash, nil
}

// Lookup returns the pinned definition bytes and the pin of a server's tool.
func (s *Snapshot) Lookup(server, tool string) (definition []byte, pin string, ok bool) {
	e, ok := s.pins[server][tool]
	if !ok {
		return nil, "", false
	}
	return []byte(e.Definition), e.SHA256, true
}

// Servers returns the pinned server names, sorted bytewise.
func (s *Snapshot) Servers() []string {
	names := make([]string, 0, len(s.pins))
	for n := range s.pins {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Tools returns a server's pinned tool names, sorted bytewise.
func (s *Snapshot) Tools(server string) []string {
	names := make([]string, 0, len(s.pins[server]))
	for n := range s.pins[server] {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Set returns a new snapshot with the definition's pin added to server, or
// replacing the tool's earlier pin. The receiver is not changed.
func (s *Snapshot) Set(server string, def *Definition) (*Snapshot, error) {
	if !serverNamePattern.MatchString(server) {
		return nil, fmt.Errorf("mcp: server name %q is not valid", server)
	}
	if !toolNamePattern.MatchString(def.Name) {
		return nil, fmt.Errorf("mcp: tool name %q is not valid", def.Name)
	}
	pins := make(map[string]map[string]pinEntry, len(s.pins)+1)
	for srv, tools := range s.pins {
		c := make(map[string]pinEntry, len(tools)+1)
		for t, e := range tools {
			c[t] = e
		}
		pins[srv] = c
	}
	if pins[server] == nil {
		pins[server] = map[string]pinEntry{}
	}
	pins[server][def.Name] = pinEntry{Definition: string(def.Bytes), SHA256: sha256Hex(def.Bytes)}
	return &Snapshot{pins: pins}, nil
}

// Bytes returns the pin file's deterministic form: servers and tools sorted
// bytewise, two-space indentation, a trailing newline.
func (s *Snapshot) Bytes() []byte {
	var b bytes.Buffer
	b.WriteString("{\n  \"servers\": {")
	for i, server := range s.Servers() {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    " + jsonString(server) + ": {")
		for j, tool := range s.Tools(server) {
			if j > 0 {
				b.WriteString(",")
			}
			e := s.pins[server][tool]
			b.WriteString("\n      " + jsonString(tool) + ": {\n")
			b.WriteString("        \"definition\": " + jsonString(e.Definition) + ",\n")
			b.WriteString("        \"sha256\": " + jsonString(e.SHA256) + "\n      }")
		}
		if len(s.pins[server]) > 0 {
			b.WriteString("\n    ")
		}
		b.WriteString("}")
	}
	if len(s.pins) > 0 {
		b.WriteString("\n  ")
	}
	b.WriteString("}\n}\n")
	return b.Bytes()
}

// jsonString encodes s as a JSON string without HTML escaping.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// Hash is the SHA-256 (lower-case hex) of Bytes.
func (s *Snapshot) Hash() string {
	return sha256Hex(s.Bytes())
}

// Save writes the snapshot to path atomically, through a temporary file in
// the same directory and a rename, with mode 0600.
func (s *Snapshot) Save(path string) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-pins-*.tmp")
	if err != nil {
		return fmt.Errorf("mcp: write pin file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("mcp: write pin file: %w", err)
	}
	if _, err = tmp.Write(s.Bytes()); err != nil {
		return fmt.Errorf("mcp: write pin file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("mcp: write pin file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("mcp: write pin file: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("mcp: write pin file: %w", err)
	}
	return nil
}

// Diff returns a line-oriented difference of two definitions' canonical
// bytes, each pretty-printed with two-space indentation. A line only in old
// begins "- ", a line only in new begins "+ ", and a line in both begins two
// spaces. Identical definitions give no "-" or "+" lines.
func Diff(oldDef, newDef []byte) string {
	a, b := prettyLines(oldDef), prettyLines(newDef)
	// Longest common subsequence table.
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			out.WriteString("  " + a[i] + "\n")
			i++
			j++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + b[j] + "\n")
			j++
		default:
			out.WriteString("- " + a[i] + "\n")
			i++
		}
	}
	return out.String()
}

func prettyLines(def []byte) []string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, def, "", "  "); err != nil {
		buf.Reset()
		buf.Write(def)
	}
	return strings.Split(buf.String(), "\n")
}
