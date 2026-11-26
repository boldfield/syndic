package sandbox

import "sort"

// BuildEnv builds the command's complete environment (section 3,
// "Environment"): PATH exactly path, HOME the per-call temporary directory,
// LANG=C.UTF-8, TERM=dumb, the four proxy variables when proxy is non-nil,
// and each passthrough name with its value from caller. Nothing else. A
// passthrough name absent from caller is omitted, not set empty. A
// passthrough name that is a fixed name, a proxy variable, BASH_ENV or ENV is
// ignored (the roster refuses those at load; this keeps the guarantee here).
// It does not read the process environment. The result is KEY=VALUE, sorted.
func BuildEnv(path, home string, proxy *ProxyListener, passthrough []string, caller map[string]string) []string {
	env := map[string]string{
		"PATH": path,
		"HOME": home,
		"LANG": "C.UTF-8",
		"TERM": "dumb",
	}
	if proxy != nil {
		for _, k := range ProxyVars {
			env[k] = proxy.URL
		}
	}
	for _, name := range passthrough {
		if reservedEnvName(name) {
			continue
		}
		if v, ok := caller[name]; ok {
			env[name] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// ProxyVars are the four proxy variables of section 4.
var ProxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}

func reservedEnvName(name string) bool {
	switch name {
	case "PATH", "HOME", "LANG", "TERM", "BASH_ENV", "ENV":
		return true
	}
	for _, v := range ProxyVars {
		if name == v {
			return true
		}
	}
	return false
}
