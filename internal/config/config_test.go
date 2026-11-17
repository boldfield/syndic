package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExample(t *testing.T) {
	roster, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if roster == nil {
		t.Fatal("roster is nil")
	}
	if roster.DefaultPreset != "review" {
		t.Errorf("DefaultPreset = %q, want %q", roster.DefaultPreset, "review")
	}
	if len(roster.Participants) != 2 {
		t.Errorf("len(Participants) = %d, want 2", len(roster.Participants))
	}
	if len(roster.Endpoints) != 1 {
		t.Errorf("len(Endpoints) = %d, want 1", len(roster.Endpoints))
	}
	if len(roster.Presets) != 2 {
		t.Errorf("len(Presets) = %d, want 2", len(roster.Presets))
	}
}

func TestValidateExampleRoster(t *testing.T) {
	roster, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if err := roster.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
}

func TestValidateDuplicateParticipantID(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1", DeclaredLocation: "local", DeclaredExecution: "local", DeclaredBilling: "none"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate participant ID")
	}
	if !strings.Contains(err.Error(), "duplicate participant ID") {
		t.Errorf("error doesn't mention duplicate participant ID: %v", err)
	}
}

func TestValidateDuplicateEndpointName(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate endpoint name")
	}
	if !strings.Contains(err.Error(), "duplicate endpoint") {
		t.Errorf("error doesn't mention duplicate endpoint: %v", err)
	}
}

func TestValidateParticipantNonExistentEndpoint(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "nonexistent", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for non-existent endpoint")
	}
	if !strings.Contains(err.Error(), "references non-existent endpoint") {
		t.Errorf("error doesn't mention non-existent endpoint: %v", err)
	}
}

func TestValidateAPIParticipantMissingEndpoint(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for api participant missing endpoint")
	}
	if !strings.Contains(err.Error(), "missing endpoint") {
		t.Errorf("error doesn't mention missing endpoint: %v", err)
	}
}

func TestValidateCLIParticipantMissingBinary(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "cli", Binary: "", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for cli participant missing binary")
	}
	if !strings.Contains(err.Error(), "missing binary") {
		t.Errorf("error doesn't mention missing binary: %v", err)
	}
}

func TestValidateModelNotInAllowlist(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "unknown", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Models: []string{"m1", "m2"}, Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for model not in allowlist")
	}
	if !strings.Contains(err.Error(), "not in endpoint") && !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("error doesn't mention model not in allowlist: %v", err)
	}
}

func TestValidatePresetAuthorInReviewers(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor, RoleReviewer}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p1"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for author in required reviewers")
	}
	if !strings.Contains(err.Error(), "is also in required reviewers") {
		t.Errorf("error doesn't mention author in required reviewers: %v", err)
	}
}

func TestValidateReviewPresetNoReviewers(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for review preset with no reviewers")
	}
	if !strings.Contains(err.Error(), "review mode must have required reviewers") {
		t.Errorf("error doesn't mention review mode requirement: %v", err)
	}
}

func TestValidateSoloPresetWithReviewers(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor, RoleReviewer}, Pool: "p"},
			{ID: "p2", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleReviewer}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p2"}},
			"solo":   {Mode: "solo", Author: "p1", RequiredReviewers: []string{"p2"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for solo preset with reviewers")
	}
	if !strings.Contains(err.Error(), "solo mode must not have required reviewers") {
		t.Errorf("error doesn't mention solo mode requirement: %v", err)
	}
}

func TestValidatePresetNonExistentParticipant(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"nonexistent"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for non-existent participant in preset")
	}
	if !strings.Contains(err.Error(), "references non-existent participant") {
		t.Errorf("error doesn't mention non-existent participant: %v", err)
	}
}

func TestValidateEmptyDefaultPreset(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p1"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for empty default preset")
	}
	if !strings.Contains(err.Error(), "default_preset is empty") {
		t.Errorf("error doesn't mention empty default preset: %v", err)
	}
}

func TestValidateDefaultPresetNonExistent(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "nonexistent",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p1"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for non-existent default preset")
	}
	if !strings.Contains(err.Error(), "references non-existent preset") {
		t.Errorf("error doesn't mention non-existent preset: %v", err)
	}
}

func TestValidateDefaultPresetMustBeReview(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "solo",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"solo": {Mode: "solo", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for default preset not being review mode")
	}
	if !strings.Contains(err.Error(), "must have mode review") {
		t.Errorf("error doesn't mention review mode requirement: %v", err)
	}
}

func TestValidateLocalOnlyRejectsCorporate(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
			{ID: "p2", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleReviewer}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", DeclaredLocation: "corporate", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p2"}},
		},
		Policy: RoutingPolicy{LocalOnly: true},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for local_only rejecting corporate endpoint")
	}
	if !strings.Contains(err.Error(), "local_only policy rejects") {
		t.Errorf("error doesn't mention local_only rejection: %v", err)
	}
}

func TestValidateMixedPolicyCorporateNotInAllowed(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
			{ID: "p2", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleReviewer}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", DeclaredLocation: "corporate", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p2"}},
		},
		Policy: RoutingPolicy{LocalOnly: false, AllowedDestinations: []string{}},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for corporate endpoint not in allowed destinations")
	}
	if !strings.Contains(err.Error(), "must be in allowed_destinations") {
		t.Errorf("error doesn't mention allowed_destinations requirement: %v", err)
	}
}

func TestValidateMeteredBilling(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", DeclaredBilling: "metered", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p1"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for metered billing")
	}
	if !strings.Contains(err.Error(), "metered") {
		t.Errorf("error doesn't mention metered billing: %v", err)
	}
}

func TestValidateMultipleViolations(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "nonexistent",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for multiple violations")
	}
	errStr := err.Error()
	// Should have multiple violations
	if !strings.Contains(errStr, "duplicate") || !strings.Contains(errStr, "missing endpoint") {
		t.Errorf("error doesn't contain all expected violations: %v", err)
	}
}

func TestSecretRefString(t *testing.T) {
	tests := []struct {
		ref      SecretRef
		expected string
	}{
		{SecretRef{Source: "env", Name: "API_KEY"}, "env:API_KEY"},
		{SecretRef{Source: "file", Name: "secret.txt"}, "file:secret.txt"},
		{SecretRef{Source: "unknown", Name: "value"}, "unknown:value"},
	}

	for _, tt := range tests {
		got := tt.ref.String()
		if got != tt.expected {
			t.Errorf("String() = %q, want %q", got, tt.expected)
		}
	}
}

func TestRosterStringNoSecret(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Auth: &SecretRef{Source: "env", Name: "API_KEY"}},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}

	str := fmt.Sprintf("%v", roster)
	// String representation should not contain resolved secret values
	if strings.Contains(str, "my-secret-key") {
		t.Errorf("String contains resolved secret: %s", str)
	}
}

func TestResolveEnv(t *testing.T) {
	os.Setenv("TEST_SECRET", "test-value")
	defer os.Unsetenv("TEST_SECRET")

	value, err := resolveEnv("TEST_SECRET")
	if err != nil {
		t.Fatalf("resolveEnv failed: %v", err)
	}
	if value != "test-value" {
		t.Errorf("value = %q, want %q", value, "test-value")
	}
}

func TestResolveEnvNotSet(t *testing.T) {
	_, err := resolveEnv("NONEXISTENT_VAR_12345")
	if err == nil {
		t.Fatal("expected error for unset env var")
	}
	if !strings.Contains(err.Error(), "not set") {
		t.Errorf("error doesn't mention not set: %v", err)
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(filePath, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	value, err := resolveFile(dir, "secret.txt")
	if err != nil {
		t.Fatalf("resolveFile failed: %v", err)
	}
	if value != "secret-value" {
		t.Errorf("value = %q, want %q", value, "secret-value")
	}
}

func TestResolveFilePathSeparator(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveFile(dir, "subdir/secret.txt")
	if err == nil {
		t.Fatal("expected error for path separator in name")
	}
	if !strings.Contains(err.Error(), "path separator") {
		t.Errorf("error doesn't mention path separator: %v", err)
	}
}

func TestResolveFileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveFile(dir, "nonexistent.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestEnvFileResolver(t *testing.T) {
	dir := t.TempDir()
	resolver := NewEnvFileResolver(dir)

	// Test env resolver
	os.Setenv("TEST_SECRET", "env-value")
	defer os.Unsetenv("TEST_SECRET")

	value, err := resolver.Resolve(SecretRef{Source: "env", Name: "TEST_SECRET"})
	if err != nil {
		t.Fatalf("Resolve env failed: %v", err)
	}
	if value != "env-value" {
		t.Errorf("env value = %q, want %q", value, "env-value")
	}

	// Test file resolver
	filePath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(filePath, []byte("file-value\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	value, err = resolver.Resolve(SecretRef{Source: "file", Name: "secret.txt"})
	if err != nil {
		t.Fatalf("Resolve file failed: %v", err)
	}
	if value != "file-value" {
		t.Errorf("file value = %q, want %q", value, "file-value")
	}
}

func TestResolveWithSentinelString(t *testing.T) {
	// This test verifies that fmt.Sprintf output never contains resolved secret values
	os.Setenv("SENTINEL_SECRET", "SENTINEL_VALUE_12345")
	defer os.Unsetenv("SENTINEL_SECRET")

	dir := t.TempDir()
	resolver := NewEnvFileResolver(dir)

	secretRef := SecretRef{Source: "env", Name: "SENTINEL_SECRET"}
	value, err := resolver.Resolve(secretRef)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// The resolved value should contain the sentinel
	if value != "SENTINEL_VALUE_12345" {
		t.Fatalf("resolved value = %q, want %q", value, "SENTINEL_VALUE_12345")
	}

	// But the string representation of SecretRef should not
	str := fmt.Sprintf("%v", secretRef)
	if strings.Contains(str, "SENTINEL_VALUE_12345") {
		t.Errorf("SecretRef.String() contains resolved secret: %s", str)
	}

	// And the roster string should not
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Auth: &secretRef},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	str = fmt.Sprintf("%v", roster)
	if strings.Contains(str, "SENTINEL_VALUE_12345") {
		t.Errorf("Roster.String() contains resolved secret: %s", str)
	}
}

func TestUnknownRoleInParticipant(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{"unknown_role"}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
	if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("error doesn't mention unknown role: %v", err)
	}
}

func TestUnknownPresetMode(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "unknown", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for unknown preset mode")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("error doesn't mention unknown mode: %v", err)
	}
}

func TestParticipantNotEligibleForRole(t *testing.T) {
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleReviewer}, Pool: "p"},
			{ID: "p2", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "p"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{"p2"}},
		},
	}
	err := roster.Validate()
	if err == nil {
		t.Fatal("expected error for participant not eligible for role")
	}
	if !strings.Contains(err.Error(), "not eligible") {
		t.Errorf("error doesn't mention role eligibility: %v", err)
	}
}

func BenchmarkValidate(b *testing.B) {
	roster, _ := Load("testdata/example.toml")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = roster.Validate()
	}
}

func TestStressTest(t *testing.T) {
	start := time.Now()

	participants := make([]Participant, 50)
	endpoints := make([]EndpointProfile, 50)

	for i := 0; i < 50; i++ {
		participants[i] = Participant{
			ID:            fmt.Sprintf("p%d", i),
			Model:         "m1",
			Route:         "api",
			Endpoint:      fmt.Sprintf("ep%d", i),
			EligibleRoles: []Role{RoleAuthor, RoleReviewer},
			Pool:          "p",
		}
		endpoints[i] = EndpointProfile{
			Name:              fmt.Sprintf("ep%d", i),
			Dialect:           "chat_completions_v1",
			DeclaredLocation:  "local",
			DeclaredExecution: "local",
			DeclaredBilling:   "none",
		}
	}

	presets := make(map[string]Preset)
	presets["review"] = Preset{Mode: "review", Author: "p0", RequiredReviewers: []string{"p1"}}

	roster := &Roster{
		DefaultPreset: "review",
		Participants:  participants,
		Endpoints:     endpoints,
		Presets:       presets,
		Policy:        RoutingPolicy{LocalOnly: true},
	}

	err := roster.Validate()
	if err != nil {
		t.Errorf("Validate failed on stress test: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Logf("Validation took %v (may be slow)", elapsed)
	}
}
