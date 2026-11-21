package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/boldfield/syndic/internal/wire"
)

// QuickSelection holds the current quick profile selection state.
type QuickSelection struct {
	ProjectProfile   *string // name from .syndic/quick.toml, if found and parsed
	ProjectFilePath  string  // path to the .syndic/quick.toml file
	ProjectFileData  []byte  // bytes of the .syndic/quick.toml file
	SessionOverride  *string // set by /quick command; "off" means send nothing
	ApprovedProfiles map[string]ApprovedProfile
}

// ApprovedProfile holds one approved quick profile record.
type ApprovedProfile struct {
	FilePath                string
	FileSHA256              string
	ProfileName             string
	ProfileDefinitionSHA256 string
}

// SelectedProfile returns the profile to send on submit, or "" if none.
// It prefers SessionOverride, then ProjectProfile if approved, then "".
func (qs *QuickSelection) SelectedProfile() *string {
	if qs.SessionOverride != nil {
		if *qs.SessionOverride == "off" {
			return nil
		}
		return qs.SessionOverride
	}
	if qs.ProjectProfile != nil && qs.isProjectProfileApproved() {
		return qs.ProjectProfile
	}
	return nil
}

// IsUnapproved returns the name of an unapproved profile, or "" if none.
func (qs *QuickSelection) IsUnapproved() *string {
	if qs.SessionOverride != nil && *qs.SessionOverride != "off" {
		return nil
	}
	if qs.ProjectProfile != nil && !qs.isProjectProfileApproved() {
		return qs.ProjectProfile
	}
	return nil
}

// isProjectProfileApproved checks if the project profile is approved.
func (qs *QuickSelection) isProjectProfileApproved() bool {
	if qs.ProjectProfile == nil || qs.ProjectFilePath == "" {
		return false
	}
	key := qs.ProjectFilePath + ":" + *qs.ProjectProfile
	_, ok := qs.ApprovedProfiles[key]
	return ok
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

	current := wd
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

// parseQuickToml parses .syndic/quick.toml. Returns "" if invalid.
func parseQuickToml(data []byte) string {
	var file quickTomlFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return ""
	}
	if file.Profile == nil || *file.Profile == "" {
		return ""
	}
	return *file.Profile
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

// readApprovals reads and parses the approvals file.
func readApprovals() (map[string]ApprovedProfile, error) {
	file, err := approvalsPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return make(map[string]ApprovedProfile), nil
		}
		return nil, err
	}

	approvals := make(map[string]ApprovedProfile)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ap ApprovedProfile
		if err := json.Unmarshal([]byte(line), &ap); err != nil {
			continue
		}
		key := ap.FilePath + ":" + ap.ProfileName
		approvals[key] = ap
	}

	return approvals, nil
}

// writeApproval writes or updates an approval record.
func writeApproval(filePath, profileName, fileSHA256, definitionSHA256 string) error {
	file, err := approvalsPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("cannot create approvals directory: %w", err)
	}

	approvals, err := readApprovals()
	if err != nil {
		return err
	}

	key := filePath + ":" + profileName
	approvals[key] = ApprovedProfile{
		FilePath:                filePath,
		FileSHA256:              fileSHA256,
		ProfileName:             profileName,
		ProfileDefinitionSHA256: definitionSHA256,
	}

	var lines []string
	for _, ap := range approvals {
		if data, err := json.Marshal(ap); err == nil {
			lines = append(lines, string(data))
		}
	}

	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		return fmt.Errorf("cannot write approvals file: %w", err)
	}

	return nil
}

// profileDefinitionSHA256 computes the SHA-256 of a profile's definition.
func profileDefinitionSHA256(qp wire.QuickProfile) string {
	// Create a canonical representation of the profile definition.
	def := map[string]interface{}{
		"preset":  qp.Preset,
		"drafts":  qp.Drafts,
		"budgets": qp.Budgets,
	}
	data, _ := json.Marshal(def)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// NewQuickSelection creates a fresh QuickSelection, discovering and approving as needed.
func NewQuickSelection(wd string) (*QuickSelection, error) {
	qs := &QuickSelection{
		ApprovedProfiles: make(map[string]ApprovedProfile),
	}

	approvals, err := readApprovals()
	if err != nil {
		return qs, fmt.Errorf("cannot read approvals: %w", err)
	}

	for _, ap := range approvals {
		qs.ApprovedProfiles[ap.FilePath+":"+ap.ProfileName] = ap
	}

	path, data, err := findQuickToml(wd)
	if err != nil {
		return qs, fmt.Errorf("cannot search for quick.toml: %w", err)
	}

	if data != nil {
		if profile := parseQuickToml(data); profile != "" {
			qs.ProjectProfile = &profile
			qs.ProjectFilePath = path
			qs.ProjectFileData = data
		}
	}

	return qs, nil
}

// ApproveProfile marks a profile as approved after showing the user what it does.
func (qs *QuickSelection) ApproveProfile(filePath string, profileName string, fileSHA256 string, qp wire.QuickProfile) error {
	definitionSHA256 := profileDefinitionSHA256(qp)
	return writeApproval(filePath, profileName, fileSHA256, definitionSHA256)
}

// CheckApproval checks if a profile file and profile name are approved,
// and returns whether it's approved and what details it has.
func (qs *QuickSelection) CheckApproval(filePath, fileSHA256, profileName, definitionSHA256 string) bool {
	key := filePath + ":" + profileName
	if ap, ok := qs.ApprovedProfiles[key]; ok {
		return ap.FileSHA256 == fileSHA256 && ap.ProfileDefinitionSHA256 == definitionSHA256
	}
	return false
}
