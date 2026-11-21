package tui

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"

	"github.com/boldfield/syndic/internal/wire"
)

// QuickSelection is the quick profile state of one TUI process: what the
// project file names, what the operator approved, and what they typed.
type QuickSelection struct {
	ProjectProfile  string // name from .syndic/quick.toml; "" when none or invalid
	ProjectFilePath string
	ProjectFileSHA  string
	Invalid         string                     // "quick: invalid <path>" when the project file could not be used
	Override        *string                    // set by /quick: a profile name, or "off"
	approved        map[string]ApprovedProfile // by project file path
	declined        map[string]bool            // states the operator answered no to in this process
}

// ApprovedProfile is one record of the approvals file.
type ApprovedProfile struct {
	FilePath                string `json:"FilePath"`
	FileSHA256              string `json:"FileSHA256"`
	ProfileName             string `json:"ProfileName"`
	ProfileDefinitionSHA256 string `json:"ProfileDefinitionSHA256"`
}

// approvedFor reports whether the project selection matches a stored entry on
// all four values: file path, file bytes, profile name and the definition hash
// status.quick_profiles reports now.
func (qs *QuickSelection) approvedFor(definitionSHA256 string) bool {
	ap, ok := qs.approved[qs.ProjectFilePath]
	return ok && ap.FileSHA256 == qs.ProjectFileSHA && ap.ProfileName == qs.ProjectProfile &&
		ap.ProfileDefinitionSHA256 == definitionSHA256
}

// stateKey identifies the state a no was given for.
func (qs *QuickSelection) stateKey(definitionSHA256 string) string {
	return strings.Join([]string{qs.ProjectFilePath, qs.ProjectFileSHA, qs.ProjectProfile, definitionSHA256}, "\x00")
}

// selected returns the profile to send on submit, or nil. An override wins and
// needs no approval; a project selection must match a stored entry.
func (qs *QuickSelection) selected(profiles []wire.QuickProfile) *string {
	if qs == nil {
		return nil
	}
	if qs.Override != nil {
		if *qs.Override == "off" {
			return nil
		}
		return qs.Override
	}
	if qs.ProjectProfile == "" {
		return nil
	}
	for _, p := range profiles {
		if p.Name == qs.ProjectProfile && qs.approvedFor(p.DefinitionSHA256) {
			name := p.Name
			return &name
		}
	}
	return nil
}

// approve records an approval, in memory and in the approvals file, replacing
// any older entry for the same project file.
func (qs *QuickSelection) approve(definitionSHA256 string) error {
	ap := ApprovedProfile{
		FilePath:                qs.ProjectFilePath,
		FileSHA256:              qs.ProjectFileSHA,
		ProfileName:             qs.ProjectProfile,
		ProfileDefinitionSHA256: definitionSHA256,
	}
	if err := writeApproval(ap); err != nil {
		return err
	}
	qs.approved[ap.FilePath] = ap
	return nil
}

// quickTomlFile holds the parsed .syndic/quick.toml structure.
type quickTomlFile struct {
	Profile *string `toml:"profile"`
}

// findQuickToml searches for .syndic/quick.toml from the working directory
// up through parents, stopping at home or filesystem boundary.
// Returns (path, bytes, error) where bytes are the file contents, or (path, nil, error).
func findQuickToml(wd string) (string, []byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	home = filepath.Clean(home)
	current := filepath.Clean(wd)
	homeInfo, _ := os.Stat(home)
	var currentDev uint64
	if st, err := os.Stat(current); err == nil {
		currentDev = getDevID(st)
	}

	for {
		tomlPath := filepath.Join(current, ".syndic", "quick.toml")
		if data, err := os.ReadFile(tomlPath); err == nil {
			return tomlPath, data, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return tomlPath, nil, err
		}

		// Check if we've reached the home directory
		if current == home {
			break
		}
		if homeInfo != nil {
			if ci, err := os.Stat(current); err == nil && os.SameFile(ci, homeInfo) {
				break
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}

		// Check if we've crossed a filesystem boundary
		if st, err := os.Stat(parent); err == nil {
			parentDev := getDevID(st)
			if currentDev != 0 && parentDev != currentDev {
				break
			}
			currentDev = parentDev
		}

		current = parent
	}

	return "", nil, nil
}

// getDevID extracts the device ID from a FileInfo.
func getDevID(fi os.FileInfo) uint64 {
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return stat.Dev
}

// parseQuickToml parses .syndic/quick.toml. Returns the profile name, an error string if invalid, or ("", "").
// The error includes the file path.
func parseQuickToml(data []byte, path string) (string, string) {
	// First, check for extra keys by unmarshaling to a map
	var m map[string]interface{}
	if err := toml.Unmarshal(data, &m); err != nil {
		return "", fmt.Sprintf("quick: invalid %s", path)
	}

	// Check for any keys other than "profile"
	for key := range m {
		if key != "profile" {
			return "", fmt.Sprintf("quick: invalid %s", path)
		}
	}

	// Now unmarshal to the struct
	var file quickTomlFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return "", fmt.Sprintf("quick: invalid %s", path)
	}

	if file.Profile == nil || *file.Profile == "" {
		return "", fmt.Sprintf("quick: invalid %s", path)
	}
	return *file.Profile, ""
}

// approvalsPath returns the approvals file path.
func approvalsPath() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}

	dir := filepath.Join(stateHome, "syndic")
	file := filepath.Join(dir, "quick-approvals")
	return file, nil
}

// readApprovals reads the approvals file: one JSON entry per line, keyed by project file path.
func readApprovals() (map[string]ApprovedProfile, error) {
	file, err := approvalsPath()
	if err != nil {
		return nil, err
	}
	approvals := make(map[string]ApprovedProfile)
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return approvals, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		var ap ApprovedProfile
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &ap) != nil {
			continue
		}
		approvals[ap.FilePath] = ap
	}
	return approvals, nil
}

// writeApproval stores ap, replacing any older entry for the same path, and
// leaves the file mode 0600 in a 0700 directory.
func writeApproval(ap ApprovedProfile) error {
	file, err := approvalsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create approvals directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("cannot chmod approvals directory: %w", err)
	}
	approvals, err := readApprovals()
	if err != nil {
		return err
	}
	approvals[ap.FilePath] = ap
	paths := make([]string, 0, len(approvals))
	for p := range approvals {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		line, err := json.Marshal(approvals[p])
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("cannot write approvals file: %w", err)
	}
	// WriteFile keeps the mode of a file that already exists.
	if err := os.Chmod(file, 0o600); err != nil {
		return fmt.Errorf("cannot chmod approvals file: %w", err)
	}
	return nil
}

// NewQuickSelection discovers the project file from wd and loads the approvals.
// The returned selection is always usable; the error says what could not be read.
func NewQuickSelection(wd string) (*QuickSelection, error) {
	qs := &QuickSelection{declined: make(map[string]bool)}
	var errs []error
	approved, err := readApprovals()
	if err != nil {
		errs = append(errs, fmt.Errorf("quick: cannot read approvals: %w", err))
		approved = make(map[string]ApprovedProfile)
	}
	qs.approved = approved

	path, data, err := findQuickToml(wd)
	if err != nil {
		errs = append(errs, fmt.Errorf("quick: cannot read %s: %w", path, err))
	}
	if data != nil {
		if profile, bad := parseQuickToml(data, path); bad != "" {
			qs.Invalid = bad
		} else {
			qs.ProjectProfile = profile
			qs.ProjectFilePath = path
			qs.ProjectFileSHA = fmt.Sprintf("%x", sha256.Sum256(data))
		}
	}
	return qs, errors.Join(errs...)
}
