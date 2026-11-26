package observation

import (
	"sort"
	"strings"
)

// StagingOverlay applies a staging map to observations, returning a merged
// view of recorded and staged content.
type StagingOverlay struct {
	staging map[string]string
}

// NewStagingOverlay creates an overlay from a map of path to content hash.
func NewStagingOverlay(staging map[string]string) *StagingOverlay {
	return &StagingOverlay{staging: staging}
}

// IsStagedFile returns true if path is in the staging map as a file.
func (so *StagingOverlay) IsStagedFile(path string) bool {
	_, ok := so.staging[path]
	return ok
}

// StagedHash returns the content hash of path if it is staged, or empty string.
func (so *StagingOverlay) StagedHash(path string) string {
	return so.staging[path]
}

// StagedChildren returns the entries the staging map adds directly under dir:
// "f" for a staged file, "d" for a name with a staged path beneath it.
func (so *StagingOverlay) StagedChildren(dir string) map[string]string {
	prefix := ""
	if dir != Root {
		prefix = dir + "/"
	}
	out := map[string]string{}
	for p := range so.staging {
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

// MergeListings combines recorded entries with staged children, maintaining
// sort order. When a name appears in both, the recorded type is kept.
func (so *StagingOverlay) MergeListings(recorded []Entry) []Entry {
	recordedMap := make(map[string]string)
	for _, e := range recorded {
		recordedMap[e.Name] = e.Type
	}
	extra := so.StagedChildren(Root)
	for n, typ := range extra {
		if recordedMap[n] == "" {
			recordedMap[n] = typ
		}
	}
	merged := make([]Entry, 0, len(recordedMap))
	for n, typ := range recordedMap {
		merged = append(merged, Entry{Type: typ, Name: n})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}

// MergeListingsUnder combines recorded entries under dir with staged children.
func (so *StagingOverlay) MergeListingsUnder(dir string, recorded []Entry) []Entry {
	recordedMap := make(map[string]string)
	for _, e := range recorded {
		recordedMap[e.Name] = e.Type
	}
	prefix := ""
	if dir != Root {
		prefix = dir + "/"
	}
	for p := range so.staging {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" {
			continue
		}
		if name, _, deeper := strings.Cut(rest, "/"); deeper {
			if recordedMap[name] == "" {
				recordedMap[name] = "d"
			}
		} else if recordedMap[name] == "" {
			recordedMap[name] = "f"
		}
	}
	merged := make([]Entry, 0, len(recordedMap))
	for n, typ := range recordedMap {
		merged = append(merged, Entry{Type: typ, Name: n})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged
}

// PathIsStaged returns true if path or any of its descendants are staged.
func (so *StagingOverlay) PathIsStaged(path string) bool {
	prefix := ""
	if path != Root {
		prefix = path + "/"
	}
	for p := range so.staging {
		if p == path || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// StagedPathsUnder returns entry names (files and directory names) directly
// under dir. For "dir/sub/file.txt", it returns "sub".
func (so *StagingOverlay) StagedPathsUnder(dir string) []string {
	prefix := ""
	if dir != Root {
		prefix = dir + "/"
	}
	seen := make(map[string]bool)
	for p := range so.staging {
		if name, ok := strings.CutPrefix(p, prefix); ok && name != "" {
			if sep := strings.IndexByte(name, '/'); sep >= 0 {
				seen[name[:sep]] = true
			} else {
				seen[name] = true
			}
		}
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// StagedPathsRecursive returns all staged paths under dir recursively.
func (so *StagingOverlay) StagedPathsRecursive(dir string) []string {
	prefix := ""
	if dir != Root {
		prefix = dir + "/"
	}
	var out []string
	for p := range so.staging {
		if _, ok := strings.CutPrefix(p, prefix); ok && p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
