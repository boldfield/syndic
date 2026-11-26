package sandbox

import "sort"

// Proxy variable names set when a command has a proxy listener
// (v7 delta, section 4, "Per-call listener").
var ProxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

// FixedNames are the names the fixed environment always sets.
var FixedNames = []string{"HOME", "LANG", "PATH", "TERM"}

// EnvConfig is what the command's environment is built from.
type EnvConfig struct {
	// Path is the PATH value, exactly tools.exec.path.
	Path string
	// TempDir is the per-call temporary directory, which HOME names.
	TempDir string
	// ProxyURL is the proxy listener's URL with its credential, set in the
	// four proxy variables; empty when the call has no listener.
	ProxyURL string
	// Passthrough lists tools.exec.env_passthrough.
	Passthrough []string
	// Source is the engine's environment the passthrough values come from.
	Source map[string]string
}

// Env builds the command's environment (v7 delta, section 3, "Environment"):
// PATH, HOME as the temporary directory, LANG=C.UTF-8, TERM=dumb, the proxy
// variables when a listener is given, and each passthrough name present in
// Source with its value. Nothing else: a passthrough name absent from Source
// is omitted, and one that is a fixed name, a proxy variable, BASH_ENV or ENV
// is dropped (the roster refuses those at load). The result is sorted by
// name. Env never reads the process environment.
func Env(c EnvConfig) []string {
	vars := map[string]string{
		"PATH": c.Path,
		"HOME": c.TempDir,
		"LANG": "C.UTF-8",
		"TERM": "dumb",
	}
	if c.ProxyURL != "" {
		for _, name := range ProxyVars {
			vars[name] = c.ProxyURL
		}
	}
	for _, name := range c.Passthrough {
		if ReservedName(name) {
			continue
		}
		if v, ok := c.Source[name]; ok {
			vars[name] = v
		}
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+vars[name])
	}
	return env
}

// ReservedName reports whether name may not be passed through: a fixed name,
// a proxy variable, BASH_ENV or ENV (v7 delta, section 3, "Environment").
func ReservedName(name string) bool {
	if name == "BASH_ENV" || name == "ENV" {
		return true
	}
	for _, n := range FixedNames {
		if n == name {
			return true
		}
	}
	for _, n := range ProxyVars {
		if n == name {
			return true
		}
	}
	return false
}
