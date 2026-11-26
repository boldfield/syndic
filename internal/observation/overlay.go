package observation

import "strings"

// StagedChildren returns the entries a staging map (staged path to content
// hash) adds directly under dir: "f" for a staged file, "d" for a name with a
// staged path beneath it.
func StagedChildren(staging map[string]string, dir string) map[string]string {
	prefix := ""
	if dir != Root {
		prefix = dir + "/"
	}
	out := map[string]string{}
	for p := range staging {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" {
			continue
		}
		if name, _, deeper := strings.Cut(rest, "/"); deeper {
			out[name] = "d"
		} else if out[name] == "" {
			out[name] = "f"
		}
	}
	return out
}

// HasStagedDescendant reports whether the staging map holds a path beneath
// path, which makes path a directory in the overlaid view.
func HasStagedDescendant(staging map[string]string, path string) bool {
	prefix := path + "/"
	for p := range staging {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// Ancestors returns the proper ancestor directories of path, outermost first,
// without the workspace root.
func Ancestors(path string) []string {
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}
