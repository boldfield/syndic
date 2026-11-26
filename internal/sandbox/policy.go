package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Policy is the object the run manifest records (section 3, "Manifest").
type Policy struct {
	// Path is the PATH value, exactly tools.exec.path.
	Path string
	// ReadOnlyPaths are tools.exec.read_only_paths.
	ReadOnlyPaths []string
	// DeviceNodes are the device nodes available: /dev/null, /dev/zero,
	// /dev/random, /dev/urandom.
	DeviceNodes []string
	// EnvNames are the environment variable names the command sees, every
	// env_passthrough name included.
	EnvNames []string
	// Capabilities is the capability set the backend enforces.
	Capabilities CapSet
	// NetworkList is tools.exec.network_allow.
	NetworkList []string
}

// CanonicalJSON encodes the policy as one JSON object: keys sorted, no
// insignificant whitespace, UTF-8, HTML escaping off. Lists keep the order
// given, except "capabilities" and "env_names", which are sorted. Absent
// lists encode as []. Keys: capabilities, device_nodes, env_names,
// network_list, path, read_only_paths.
func (p Policy) CanonicalJSON() []byte {
	caps := make([]string, 0, len(p.Capabilities))
	for _, c := range p.Capabilities.Sorted() {
		caps = append(caps, string(c))
	}
	names := append([]string{}, p.EnvNames...)
	sort.Strings(names)
	obj := map[string]any{
		"path":            p.Path,
		"read_only_paths": nonNil(p.ReadOnlyPaths),
		"device_nodes":    nonNil(p.DeviceNodes),
		"env_names":       names,
		"capabilities":    caps,
		"network_list":    nonNil(p.NetworkList),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// A map of strings and string lists cannot fail to encode.
	_ = enc.Encode(obj)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// Hash is the lowercase hex SHA-256 of CanonicalJSON.
func (p Policy) Hash() string {
	sum := sha256.Sum256(p.CanonicalJSON())
	return hex.EncodeToString(sum[:])
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
