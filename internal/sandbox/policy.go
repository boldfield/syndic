package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Policy is the sandbox policy as the run manifest records it, as one object
// (v7 delta, section 3, "Manifest").
type Policy struct {
	// Path is the PATH value, tools.exec.path.
	Path string
	// ReadOnlyPaths is tools.exec.read_only_paths, in configured order.
	ReadOnlyPaths []string
	// Devices are the device nodes; see DeviceNodes.
	Devices []string
	// EnvNames are the names of the command's environment, every
	// env_passthrough name included.
	EnvNames []string
	// Capabilities is the capability set in force.
	Capabilities CapSet
	// NetworkAllow is tools.exec.network_allow, in configured order.
	NetworkAllow []string
}

// canonicalPolicy fixes the encoding. Its fields are declared in bytewise
// order of their JSON keys, so that encoding/json writes the keys sorted.
type canonicalPolicy struct {
	Capabilities  []string `json:"capabilities"`
	Devices       []string `json:"devices"`
	EnvNames      []string `json:"env_names"`
	NetworkAllow  []string `json:"network_allow"`
	Path          string   `json:"path"`
	ReadOnlyPaths []string `json:"read_only_paths"`
}

// CanonicalJSON returns the policy's canonical JSON encoding: one object with
// its keys sorted bytewise, no insignificant whitespace, UTF-8, HTML escaping
// off, and no trailing newline. Every list is an array, empty rather than
// null when it has no element. Lists keep the order given except the
// capability set and the environment names, which are sorted bytewise.
// Strings are encoded as encoding/json writes them with HTML escaping off:
// quote, backslash and control characters escaped, U+2028 and U+2029 written
// as   and  , invalid UTF-8 replaced by U+FFFD, and every other
// character as its raw UTF-8 bytes.
func (p Policy) CanonicalJSON() ([]byte, error) {
	envNames := append([]string{}, p.EnvNames...)
	sort.Strings(envNames)
	c := canonicalPolicy{
		Capabilities:  p.Capabilities.Names(),
		Devices:       orEmpty(p.Devices),
		EnvNames:      envNames,
		NetworkAllow:  orEmpty(p.NetworkAllow),
		Path:          p.Path,
		ReadOnlyPaths: orEmpty(p.ReadOnlyPaths),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Hash returns the lowercase hex SHA-256 of the policy's canonical JSON
// encoding (v7 delta, section 3, "Manifest").
func (p Policy) Hash() (string, error) {
	b, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
