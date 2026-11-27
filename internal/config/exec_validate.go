package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// execReservedEnv lists the environment names tools.exec.env_passthrough may
// not carry (v7 section 3, "Environment").
var execReservedEnv = []string{
	"PATH", "HOME", "LANG", "TERM", "BASH_ENV", "ENV",
	"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy",
}

// execResultFramingBytes is what the rendering of an exec result adds to its
// output, at most (v7 section 2, "Output").
const defaultExecMaxOutputBytes = 61440

const execResultFramingBytes = 1024

// validateExec applies every rule v7 checks at roster load for the exec tool
// group. Each error names the preset, endpoint or setting at fault.
func (r *Roster) validateExec() []error {
	var errs []error
	ex := r.ExecSettings()
	var raw ExecConfig
	if r.Tools != nil && r.Tools.Exec != nil {
		raw = *r.Tools.Exec
	}

	if raw.Sandbox != "" && raw.Sandbox != ExecSandboxRequired && raw.Sandbox != ExecSandboxNone {
		errs = append(errs, fmt.Errorf("tools.exec.sandbox must be %q or %q, got %q", ExecSandboxRequired, ExecSandboxNone, raw.Sandbox))
	}

	for _, b := range []struct {
		name string
		v    int64
	}{
		{"timeout_s", int64(ex.TimeoutS)},
		{"max_timeout_s", int64(ex.MaxTimeoutS)},
		{"max_output_bytes", int64(ex.MaxOutputBytes)},
		{"max_calls", int64(ex.MaxCalls)},
		{"max_reviewer_calls", int64(ex.MaxReviewerCalls)},
		{"max_workspace_bytes", ex.MaxWorkspaceBytes},
		{"max_workspace_files", int64(ex.MaxWorkspaceFiles)},
		{"kill_grace_s", int64(ex.KillGraceS)},
		{"cleanup_deadline_s", int64(ex.CleanupDeadlineS)},
	} {
		if b.v < 1 {
			errs = append(errs, fmt.Errorf("tools.exec.%s must be at least 1", b.name))
		}
	}
	if ex.TimeoutS > ex.MaxTimeoutS {
		errs = append(errs, fmt.Errorf("tools.exec.timeout_s (%d) must not exceed tools.exec.max_timeout_s (%d)", ex.TimeoutS, ex.MaxTimeoutS))
	}
	maxResult := 65536
	if r.Tools != nil && r.Tools.MaxResultBytes != nil {
		maxResult = *r.Tools.MaxResultBytes
	}
	// The default output bound is checked only where exec is in use or the
	// bound is changed, so a roster with no exec anywhere loads as it always did.
	execInUse := ex.MaxOutputBytes != defaultExecMaxOutputBytes
	for _, p := range r.Presets {
		if slices.Contains(p.Tools, "exec") || slices.Contains(p.ReviewerTools, "exec") {
			execInUse = true
		}
	}
	if execInUse && ex.MaxOutputBytes > maxResult-execResultFramingBytes {
		errs = append(errs, fmt.Errorf("tools.exec.max_output_bytes (%d) must be at most tools.max_result_bytes less %d (%d)", ex.MaxOutputBytes, execResultFramingBytes, maxResult-execResultFramingBytes))
	}

	var protected []*regexp.Regexp
	protectedPatterns := r.protectedPatternsOrDefault()
	for _, pat := range protectedPatterns {
		if re, err := compileExecProtectedPattern(pat); err == nil {
			protected = append(protected, re)
		}
	}
	areas := execForbiddenAreas()
	var pathDirs []string
	for _, d := range filepath.SplitList(ex.Path) {
		if d != "" {
			pathDirs = append(pathDirs, d)
		}
	}
	for _, d := range pathDirs {
		if why := execPathProblem(d, areas, protected); why != "" {
			errs = append(errs, fmt.Errorf("tools.exec.path: directory %q %s", d, why))
		}
	}
	for _, p := range ex.ReadOnlyPaths {
		if why := execPathProblem(p, areas, protected); why != "" {
			errs = append(errs, fmt.Errorf("tools.exec.read_only_paths: %q %s", p, why))
		}
	}

	for _, name := range ex.EnvPassthrough {
		if slices.Contains(execReservedEnv, name) {
			errs = append(errs, fmt.Errorf("tools.exec.env_passthrough: %q is fixed or reserved and may not be passed through", name))
		}
	}

	sandboxNone := ex.Sandbox == ExecSandboxNone
	if sandboxNone && len(ex.NetworkAllow) > 0 {
		errs = append(errs, fmt.Errorf("tools.exec.network_allow must be empty under tools.exec.sandbox = %q", ExecSandboxNone))
	}
	for _, pattern := range ex.NetworkAllow {
		if err := validateNetworkAllowPattern(pattern); err != nil {
			errs = append(errs, fmt.Errorf("tools.exec.network_allow: %w", err))
		}
	}

	endpoints := make(map[string]EndpointProfile, len(r.Endpoints))
	for _, ep := range r.Endpoints {
		endpoints[ep.Name] = ep
	}
	for _, name := range slices.Sorted(maps.Keys(r.Presets)) {
		preset := r.Presets[name]
		if sandboxNone && len(preset.NetworkAllow) > 0 {
			errs = append(errs, fmt.Errorf("preset %q: network_allow must be empty under tools.exec.sandbox = %q", name, ExecSandboxNone))
		}
		for _, pattern := range preset.NetworkAllow {
			if err := validateNetworkAllowPattern(pattern); err != nil {
				errs = append(errs, fmt.Errorf("preset %q: network_allow: %w", name, err))
			}
		}

		authorExec := slices.Contains(preset.Tools, "exec")
		reviewerExec := slices.Contains(preset.ReviewerTools, "exec")
		if !authorExec && !reviewerExec {
			continue
		}
		if authorExec && !slices.Contains(preset.Tools, "read") {
			errs = append(errs, fmt.Errorf("preset %q: author tools include exec but not read", name))
		}
		if reviewerExec && !slices.Contains(preset.ReviewerTools, "read") {
			errs = append(errs, fmt.Errorf("preset %q: reviewer_tools include exec but not read", name))
		}
		if sandboxNone {
			if preset.Mode == "solo" {
				errs = append(errs, fmt.Errorf("preset %q: solo mode cannot have exec tools under tools.exec.sandbox = %q", name, ExecSandboxNone))
			}
		}
		if !r.AllowWebWithFile(preset) && len(r.EffectiveNetworkAllow(preset)) > 0 {
			errs = append(errs, fmt.Errorf("preset %q: cannot grant exec with a non-empty network_allow when tools.allow_web_with_read=false", name))
		}

		for _, id := range preset.ExecParticipants() {
			for _, p := range r.Participants {
				if p.ID != id {
					continue
				}
				switch p.Route {
				case "cli":
					errs = append(errs, fmt.Errorf("preset %q: CLI participant %q cannot have exec tools", name, id))
				case "api":
					if p.Endpoint == "" {
						errs = append(errs, fmt.Errorf("preset %q: api participant %q missing endpoint but has exec tools", name, id))
					} else if endpoints[p.Endpoint].RefereeModel == "" {
						errs = append(errs, fmt.Errorf("preset %q: endpoint %q used by %q has no referee_model but exec tools are required", name, p.Endpoint, id))
					}
				}
				break
			}
		}
	}
	return errs
}

// CheckExecWorkspaceRoot applies to a workspace root the test the roster
// applies to tools.exec.path and tools.exec.read_only_paths at load: the root
// must not be, contain or lie inside any of those locations. The engine calls
// it when a workspace is opened, since the root is not known at roster load.
func (r *Roster) CheckExecWorkspaceRoot(root string) error {
	ex := r.ExecSettings()
	for _, d := range filepath.SplitList(ex.Path) {
		if d != "" && execPathsOverlap(d, root) {
			return fmt.Errorf("workspace root %q overlaps tools.exec.path directory %q", root, d)
		}
	}
	for _, p := range ex.ReadOnlyPaths {
		if execPathsOverlap(p, root) {
			return fmt.Errorf("workspace root %q overlaps tools.exec.read_only_paths entry %q", root, p)
		}
	}
	return nil
}

func (r *Roster) protectedPatternsOrDefault() []string {
	if r.Tools != nil && len(r.Tools.ProtectedPaths) > 0 {
		return r.Tools.ProtectedPaths
	}
	c := Roster{}
	c.ApplyDefaults()
	return c.Tools.ProtectedPaths
}

// validateNetworkAllowPattern applies the allowlist pattern rules of
// tools.web.allowed_hosts. An IP literal is refused by those rules.
func validateNetworkAllowPattern(pattern string) error {
	return validateAllowedHostPattern(pattern)
}

type execArea struct {
	path string
	// outsideOnly areas are refused only when the path is, or contains, them.
	outsideOnly bool
}

// execForbiddenAreas lists the home directory and the syndic configuration,
// state, journal and temporary areas.
func execForbiddenAreas() []execArea {
	var areas []execArea
	home, _ := os.UserHomeDir()
	add := func(p string, outsideOnly bool) {
		if p != "" && filepath.IsAbs(p) {
			areas = append(areas, execArea{p, outsideOnly})
		}
	}
	add(home, false)
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		add(filepath.Join(v, "syndic"), false)
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		add(filepath.Join(v, "syndic"), false)
	}
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		add(filepath.Join(v, "syndic"), false)
	}
	if home != "" {
		add(filepath.Join(home, ".config", "syndic"), false)
		add(filepath.Join(home, ".local", "state", "syndic"), false)
	}
	add(os.TempDir(), true)
	return areas
}

func execPathProblem(p string, areas []execArea, protected []*regexp.Regexp) string {
	if p == "" {
		return ""
	}
	for _, a := range areas {
		if execPathsOverlapArea(p, a) {
			return fmt.Sprintf("is, contains or lies inside %q, which a command may not read", a.path)
		}
	}
	rel := strings.TrimPrefix(filepath.Clean(p), "/")
	for _, re := range protected {
		if re.MatchString(rel) {
			return "matches a tools.protected_paths pattern"
		}
	}
	return ""
}

func execPathsOverlapArea(p string, a execArea) bool {
	if a.outsideOnly {
		return pathWithin(p, resolveExecPath(a.path))
	}
	return execPathsOverlap(p, a.path)
}

// execPathsOverlap reports whether a is, contains, or is contained in b.
func execPathsOverlap(a, b string) bool {
	a, b = resolveExecPath(a), resolveExecPath(b)
	return pathWithin(a, b) || pathWithin(b, a)
}

// pathWithin reports whether inner is outer or lies below it.
func pathWithin(outer, inner string) bool {
	outer, inner = filepath.Clean(outer), filepath.Clean(inner)
	if outer == inner || outer == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(inner, outer+string(filepath.Separator))
}

// resolveExecPath cleans p and resolves symbolic links where the path exists,
// so a link cannot hide an overlap.
func resolveExecPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// compileExecProtectedPattern converts a tools.protected_paths glob to a
// regular expression with the semantics the workspace applies.
func compileExecProtectedPattern(pattern string) (*regexp.Regexp, error) {
	escaped := regexp.QuoteMeta(pattern)
	escaped = strings.ReplaceAll(escaped, `\*\*/`, `(?:.*/)?`)
	escaped = strings.ReplaceAll(escaped, `/\*\*`, `/.*`)
	escaped = strings.ReplaceAll(escaped, `\*\*`, `.*`)
	escaped = strings.ReplaceAll(escaped, `\*`, `[^/]*`)
	escaped = strings.ReplaceAll(escaped, `\?`, `[^/]`)
	if strings.HasPrefix(pattern, "/") {
		escaped = `^` + strings.TrimPrefix(escaped, `/`)
	} else {
		escaped = `(^|/)` + escaped
	}
	if strings.HasSuffix(pattern, "/") {
		escaped = strings.TrimSuffix(escaped, "/") + `($|/.*)`
	} else {
		escaped += `($|/.*)`
	}
	return regexp.Compile(escaped)
}
