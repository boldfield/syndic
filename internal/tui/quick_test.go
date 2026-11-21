package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindQuickToml(t *testing.T) {
	// Test: a file two directories above is found
	tmpdir := t.TempDir()
	subdir := filepath.Join(tmpdir, "a", "b")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create .syndic/quick.toml at tmpdir level
	quickdir := filepath.Join(tmpdir, ".syndic")
	if err := os.Mkdir(quickdir, 0755); err != nil {
		t.Fatal(err)
	}
	quickfile := filepath.Join(quickdir, "quick.toml")
	if err := os.WriteFile(quickfile, []byte(`profile = "solo"`), 0644); err != nil {
		t.Fatal(err)
	}

	path, data, err := findQuickToml(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if path != quickfile {
		t.Errorf("expected %q, got %q", quickfile, path)
	}
	if string(data) != `profile = "solo"` {
		t.Errorf("unexpected data: %s", data)
	}
}

func TestFindQuickTomlStopsAtHome(t *testing.T) {
	// Test: search stops at home directory (doesn't go into parents of home)
	tmpdir := t.TempDir()
	homedir := filepath.Join(tmpdir, "home")
	subdir := filepath.Join(homedir, "subdir")
	abovedir := filepath.Join(tmpdir, "above")

	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(abovedir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create .syndic/quick.toml above the home directory
	quickdir := filepath.Join(tmpdir, ".syndic")
	if err := os.Mkdir(quickdir, 0755); err != nil {
		t.Fatal(err)
	}
	quickfile := filepath.Join(quickdir, "quick.toml")
	if err := os.WriteFile(quickfile, []byte(`profile = "solo"`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set home directory to homedir; search should stop there and not reach tmpdir
	t.Setenv("HOME", homedir)

	path, data, err := findQuickToml(subdir)
	if err != nil {
		t.Fatal(err)
	}

	// Should not find the file above home
	if path != "" {
		t.Errorf("expected no file, got %q", path)
	}
	if data != nil {
		t.Errorf("expected no data, got %v", data)
	}
}

func TestParseQuickToml(t *testing.T) {
	tests := []struct {
		name          string
		data          string
		expectedName  string
		expectedError bool
	}{
		{"valid profile", `profile = "solo"`, "solo", false},
		{"valid profile with spaces", `profile = "my profile"`, "my profile", false},
		{"missing profile", "", "", true},
		{"empty profile", `profile = ""`, "", true},
		{"extra key", "profile = \"solo\"\nextra = \"key\"", "", true},
		{"invalid toml", `{invalid`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, errStr := parseQuickToml([]byte(tt.data))
			if tt.expectedError {
				if errStr == "" {
					t.Errorf("expected error, got none")
				}
			} else {
				if errStr != "" {
					t.Errorf("expected no error, got %q", errStr)
				}
				if name != tt.expectedName {
					t.Errorf("expected %q, got %q", tt.expectedName, name)
				}
			}
		})
	}
}

func TestApprovalRecordRoundTrip(t *testing.T) {
	tmpdir := t.TempDir()

	oldStateHome := os.Getenv("XDG_STATE_HOME")
	defer func() {
		if oldStateHome == "" {
			_ = os.Unsetenv("XDG_STATE_HOME")
		} else {
			_ = os.Setenv("XDG_STATE_HOME", oldStateHome)
		}
	}()
	_ = os.Setenv("XDG_STATE_HOME", tmpdir)

	// Write an approval
	filePath := "/home/user/project/.syndic/quick.toml"
	profileName := "fast"
	fileSHA := "abc123"
	defSHA := "def456"

	if err := writeApproval(filePath, profileName, fileSHA, defSHA); err != nil {
		t.Fatal(err)
	}

	// Read it back
	approvals, err := readApprovals()
	if err != nil {
		t.Fatal(err)
	}

	key := filePath + ":" + profileName
	ap, ok := approvals[key]
	if !ok {
		t.Fatal("approval not found")
	}

	if ap.FilePath != filePath || ap.ProfileName != profileName || ap.FileSHA256 != fileSHA || ap.ProfileDefinitionSHA256 != defSHA {
		t.Errorf("approval mismatch: %+v", ap)
	}
}

func TestCheckApproval(t *testing.T) {
	tmpdir := t.TempDir()

	oldStateHome := os.Getenv("XDG_STATE_HOME")
	defer func() {
		if oldStateHome == "" {
			_ = os.Unsetenv("XDG_STATE_HOME")
		} else {
			_ = os.Setenv("XDG_STATE_HOME", oldStateHome)
		}
	}()
	_ = os.Setenv("XDG_STATE_HOME", tmpdir)

	filePath := "/home/user/project/.syndic/quick.toml"
	profileName := "fast"
	fileSHA := "abc123"
	defSHA := "def456"

	if err := writeApproval(filePath, profileName, fileSHA, defSHA); err != nil {
		t.Fatal(err)
	}

	qs, err := NewQuickSelection(tmpdir)
	if err != nil {
		t.Fatal(err)
	}

	// Should be approved with matching SHAs
	if !qs.CheckApproval(filePath, fileSHA, profileName, defSHA) {
		t.Error("approval check failed for matching SHAs")
	}

	// Should not be approved with different file SHA
	if qs.CheckApproval(filePath, "different", profileName, defSHA) {
		t.Error("approval check should fail for different file SHA")
	}

	// Should not be approved with different definition SHA
	if qs.CheckApproval(filePath, fileSHA, profileName, "different") {
		t.Error("approval check should fail for different definition SHA")
	}
}

func TestSelectedProfile(t *testing.T) {
	tests := []struct {
		name               string
		projectProfile     *string
		sessionOverride    *string
		isApproved         bool
		expectedSelected   *string
		expectedUnapproved *string
	}{
		{
			name:               "no profiles",
			expectedSelected:   nil,
			expectedUnapproved: nil,
		},
		{
			name:             "project profile approved",
			projectProfile:   strPtr("solo"),
			isApproved:       true,
			expectedSelected: strPtr("solo"),
		},
		{
			name:               "project profile unapproved",
			projectProfile:     strPtr("solo"),
			isApproved:         false,
			expectedUnapproved: strPtr("solo"),
		},
		{
			name:             "session override",
			sessionOverride:  strPtr("fast"),
			expectedSelected: strPtr("fast"),
		},
		{
			name:             "session override off",
			sessionOverride:  strPtr("off"),
			expectedSelected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := "/path/.syndic/quick.toml"
			qs := &QuickSelection{
				ProjectProfile:   tt.projectProfile,
				ProjectFilePath:  filePath,
				SessionOverride:  tt.sessionOverride,
				ApprovedProfiles: make(map[string]ApprovedProfile),
			}

			if tt.isApproved && tt.projectProfile != nil {
				key := filePath + ":" + *tt.projectProfile
				qs.ApprovedProfiles[key] = ApprovedProfile{
					FilePath:                filePath,
					ProfileName:             *tt.projectProfile,
					FileSHA256:              "sha1",
					ProfileDefinitionSHA256: "sha2",
				}
			}

			selected := qs.SelectedProfile()
			if (selected == nil) != (tt.expectedSelected == nil) {
				t.Errorf("SelectedProfile mismatch: expected %v, got %v", tt.expectedSelected, selected)
			}
			if selected != nil && tt.expectedSelected != nil && *selected != *tt.expectedSelected {
				t.Errorf("SelectedProfile mismatch: expected %q, got %q", *tt.expectedSelected, *selected)
			}

			gotUnapproved := qs.IsUnapproved()
			if (gotUnapproved == nil) != (tt.expectedUnapproved == nil) {
				t.Errorf("IsUnapproved mismatch: expected %v, got %v", tt.expectedUnapproved, gotUnapproved)
			}
			if gotUnapproved != nil && tt.expectedUnapproved != nil && *gotUnapproved != *tt.expectedUnapproved {
				t.Errorf("IsUnapproved mismatch: expected %q, got %q", *tt.expectedUnapproved, *gotUnapproved)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
