package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/transport"
)

// TestGuardSentinel verifies that sentinel strings in Raw
// do not appear in the redacted export (even when in both headers and body).
func TestGuardSentinel(t *testing.T) {
	sentinelInRaw := "SENSITIVE_HEADER_VALUE_12345"

	sentinelVersion := "SERVER_VERSION_SENTINEL_67890"

	rawJSON := fmt.Sprintf(`{"headers":{"X-Foo":["%s"]},"body":"%s"}`, sentinelInRaw, sentinelInRaw)

	record := transport.CapabilityRecord{
		Endpoint:       "test-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
		ServerVersion:  sentinelVersion,
		Raw:            json.RawMessage(rawJSON),
	}

	profile := config.EndpointProfile{
		Name:             "test-endpoint",
		BaseURL:          "http://localhost:8000",
		DeclaredLocation: "local",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	// Marshal export to JSON to test serialization
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	exportStr := string(data)

	// Verify sentinel is not in the export (was in both header and body in Raw)
	if found := contains(exportStr, sentinelInRaw); found {
		t.Errorf("Raw sentinel %q found in export", sentinelInRaw)
	}
	if found := contains(exportStr, sentinelVersion); found {
		t.Errorf("ServerVersion sentinel %q found in export", sentinelVersion)
	}
}

// TestCorporateHostExcluded verifies that corporate endpoint hosts are not exported.
func TestCorporateHostExcluded(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "corp-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "corp-endpoint",
		BaseURL:          "https://api.internal.corp.com:8443/v1",
		DeclaredLocation: "corporate",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.Host != "" {
		t.Errorf("Expected empty Host for corporate endpoint, got %q", export.Host)
	}
	if export.Port != "" {
		t.Errorf("Expected empty Port for corporate endpoint, got %q", export.Port)
	}

	// Serialize and check no host components appear in JSON
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	exportStr := string(data)
	forbiddenParts := []string{"api.internal.corp.com", "internal.corp", "8443"}
	for _, part := range forbiddenParts {
		if contains(exportStr, part) {
			t.Errorf("Serialized export contains forbidden host component %q", part)
		}
	}
}

// TestLocalHostIncluded verifies that local endpoint hosts are exported.
func TestLocalHostIncluded(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "local-endpoint",
		Model:          "test-model",
		AuthMode:       "none",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "local-endpoint",
		BaseURL:          "http://localhost:8000/v1",
		DeclaredLocation: "local",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.Host != "localhost" {
		t.Errorf("Expected Host 'localhost', got %q", export.Host)
	}
	if export.Port != "8000" {
		t.Errorf("Expected Port '8000', got %q", export.Port)
	}

	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	for _, part := range []string{"8000/", "://", "8000/v1", "localhost:8000/"} {
		if contains(string(data), part) {
			t.Errorf("Serialized export contains %q, which follows the host", part)
		}
	}
}

// TestLANHostIncluded verifies that LAN endpoint hosts are exported.
func TestLANHostIncluded(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "lan-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "lan-endpoint",
		BaseURL:          "http://192.168.1.100:5000",
		DeclaredLocation: "lan",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.Host != "192.168.1.100" {
		t.Errorf("Expected Host '192.168.1.100', got %q", export.Host)
	}
	if export.Port != "5000" {
		t.Errorf("Expected Port '5000', got %q", export.Port)
	}
}

// TestIPv6LocalHostIncluded verifies that IPv6 local endpoint hosts are correctly extracted.
func TestIPv6LocalHostIncluded(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "ipv6-local-endpoint",
		Model:          "test-model",
		AuthMode:       "none",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "ipv6-local-endpoint",
		BaseURL:          "http://[::1]:11434/v1",
		DeclaredLocation: "local",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.Host != "::1" {
		t.Errorf("Expected Host '::1', got %q", export.Host)
	}
	if export.Port != "11434" {
		t.Errorf("Expected Port '11434', got %q", export.Port)
	}
}

// TestCloudHostExcluded verifies that cloud endpoint hosts are not exported.
func TestCloudHostExcluded(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "cloud-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "cloud-endpoint",
		BaseURL:          "https://api.example.com/v1",
		DeclaredLocation: "cloud",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.Host != "" {
		t.Errorf("Expected empty Host for cloud endpoint, got %q", export.Host)
	}

	// Serialize and check no host components appear in JSON
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	exportStr := string(data)
	if contains(exportStr, "api.example.com") {
		t.Errorf("Serialized export contains forbidden host component api.example.com")
	}
}

// TestSerializedExportContainsRedactionMarkers verifies the export contains
// the redaction markers in JSON.
func TestSerializedExportContainsRedactionMarkers(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "test-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "test-endpoint",
		BaseURL:          "http://localhost:8000",
		DeclaredLocation: "local",
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	exportStr := string(data)

	if !contains(exportStr, `"redacted":true`) && !contains(exportStr, `"redacted": true`) {
		t.Errorf("Export JSON does not contain redacted marker")
	}
	if !contains(exportStr, `"evidence/v1"`) {
		t.Errorf("Export JSON does not contain redaction rule version")
	}
}

// TestExportTypeNoRawMessage verifies that Export has no json.RawMessage field.
func TestExportTypeNoRawMessage(t *testing.T) {
	exportType := reflect.TypeOf(Export{})

	for i := 0; i < exportType.NumField(); i++ {
		field := exportType.Field(i)
		if field.Type == reflect.TypeOf(json.RawMessage{}) {
			t.Errorf("Export field %q has type json.RawMessage", field.Name)
		}
	}
}

// TestExportTypeNoHeaderMap verifies that Export has no map[string][]string field.
func TestExportTypeNoHeaderMap(t *testing.T) {
	exportType := reflect.TypeOf(Export{})
	headerMapType := reflect.TypeOf(map[string][]string{})

	for i := 0; i < exportType.NumField(); i++ {
		field := exportType.Field(i)
		if field.Type == headerMapType {
			t.Errorf("Export field %q has type map[string][]string", field.Name)
		}
	}
}

// TestWriteRawFilePermissions verifies that WriteRaw creates files with mode 0600.
func TestWriteRawFilePermissions(t *testing.T) {
	tempDir := t.TempDir()

	record := transport.CapabilityRecord{
		Endpoint:       "test-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	path, err := WriteRaw(record, tempDir)
	if err != nil {
		t.Fatalf("WriteRaw failed: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	// Check file mode is 0600 (owner read/write only)
	mode := fi.Mode().Perm()
	if mode != 0600 {
		t.Errorf("Expected file mode 0600, got %#o", mode)
	}
}

// freezeClock fixes the file-name clock so repeated writes collide.
func freezeClock(t *testing.T) {
	t.Helper()
	orig := now
	now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { now = orig })
}

// TestWriteRawNoOverwrite verifies that WriteRaw does not overwrite existing files.
func TestWriteRawNoOverwrite(t *testing.T) {
	freezeClock(t)
	tempDir := t.TempDir()

	record := transport.CapabilityRecord{
		Endpoint:       "test-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	path1, err := WriteRaw(record, tempDir)
	if err != nil {
		t.Fatalf("First WriteRaw failed: %v", err)
	}
	before, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read first file: %v", err)
	}

	record.AdapterVersion = "2.0.0"
	if _, err = WriteRaw(record, tempDir); err == nil {
		t.Fatalf("Expected WriteRaw to fail on second write, but succeeded")
	}

	after, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("First file was deleted or inaccessible: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("First file was modified by the failed second write")
	}
}

// TestWriteExportNoOverwrite verifies that WriteExport does not overwrite existing files.
func TestWriteExportNoOverwrite(t *testing.T) {
	freezeClock(t)
	tempDir := t.TempDir()

	export := Export{Redacted: true, RedactionRule: "evidence/v1", EndpointName: "test-endpoint", Model: "test-model"}

	path1, err := WriteExport(export, tempDir)
	if err != nil {
		t.Fatalf("First WriteExport failed: %v", err)
	}
	before, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read first file: %v", err)
	}

	export.AuthMode = "changed"
	if _, err = WriteExport(export, tempDir); err == nil {
		t.Fatalf("Expected WriteExport to fail on second write, but succeeded")
	}

	after, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("First file was deleted or inaccessible: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("First file was modified by the failed second write")
	}
}

// TestRULEMDExists verifies that RULE.md exists in the evidence package.
func TestRULEMDExists(t *testing.T) {
	// Find RULE.md in the same directory as this test file
	ruleFile := filepath.Join(filepath.Dir(findTestFile(t)), "RULE.md")
	_, err := os.Stat(ruleFile)
	if err != nil {
		t.Fatalf("RULE.md not found at %s: %v", ruleFile, err)
	}
}

// TestRULEMDContent verifies that RULE.md documents the redaction rule.
func TestRULEMDContent(t *testing.T) {
	ruleFile := filepath.Join(filepath.Dir(findTestFile(t)), "RULE.md")
	data, err := os.ReadFile(ruleFile)
	if err != nil {
		t.Fatalf("Failed to read RULE.md: %v", err)
	}

	content := string(data)

	// Check for key sections
	requiredSections := []string{
		"Evidence Redaction Rule v1",
		"allowlist",
		"evidence/v1",
		"host",
		"port",
		"local",
		"cloud",
		"corporate",
		"Raw",
		"ServerVersion",
	}

	for _, section := range requiredSections {
		if !contains(content, section) {
			t.Errorf("RULE.md missing expected section: %q", section)
		}
	}
}

// TestEndpointMismatch verifies that Redact returns an error when
// record endpoint does not match profile name.
func TestEndpointMismatch(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "endpoint-a",
		Model:          "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name: "endpoint-b",
	}

	_, err := Redact(record, profile)
	if err == nil {
		t.Fatalf("Expected Redact to fail on endpoint mismatch, but it succeeded")
	}
}

// TestAuthSecretReference verifies that auth secret is included as a reference.
func TestAuthSecretReference(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "test-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "test-endpoint",
		BaseURL:          "http://localhost:8000",
		DeclaredLocation: "local",
		Auth: &config.SecretRef{
			Source: "env",
			Name:   "API_KEY",
		},
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.AuthSecret != "env:API_KEY" {
		t.Errorf("Expected auth secret 'env:API_KEY', got %q", export.AuthSecret)
	}
}

// TestExtraHeadersReferences verifies that extra headers are included as references.
func TestExtraHeadersReferences(t *testing.T) {
	record := transport.CapabilityRecord{
		Endpoint:       "test-endpoint",
		Model:          "test-model",
		AuthMode:       "api-key",
		Identity:       transport.NewUnknownIdentity(),
		ReportedModel:  "test-model",
		AdapterVersion: "1.0.0",
		ObservedAt:     time.Now(),
	}

	profile := config.EndpointProfile{
		Name:             "test-endpoint",
		BaseURL:          "http://localhost:8000",
		DeclaredLocation: "local",
		ExtraHeaders: map[string]config.SecretRef{
			"X-Custom-Header": {Source: "env", Name: "CUSTOM_HEADER"},
			"X-Token":         {Source: "file", Name: "token"},
		},
	}

	export, err := Redact(record, profile)
	if err != nil {
		t.Fatalf("Redact failed: %v", err)
	}

	if export.ExtraHeaders["X-Custom-Header"] != "env:CUSTOM_HEADER" {
		t.Errorf("Expected header ref 'env:CUSTOM_HEADER', got %q", export.ExtraHeaders["X-Custom-Header"])
	}
	if export.ExtraHeaders["X-Token"] != "file:token" {
		t.Errorf("Expected header ref 'file:token', got %q", export.ExtraHeaders["X-Token"])
	}
}

// Helper: findTestFile returns the path to this test file.
func findTestFile(t *testing.T) string {
	// Get the caller's file path
	_, file, _, _ := getCallerInfo()
	return file
}

// Helper: getCallerInfo is a simple wrapper for runtime info.
func getCallerInfo() (string, string, int, bool) {
	// Using a simple implementation: read this file
	wd, err := os.Getwd()
	if err != nil {
		return "", "", 0, false
	}
	return "", filepath.Join(wd, "evidence_test.go"), 0, true
}

// Helper: contains checks if a string contains a substring.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestFileNameSanitized verifies that disallowed characters in the endpoint and
// model never reach the file name.
func TestFileNameSanitized(t *testing.T) {
	freezeClock(t)
	tempDir := t.TempDir()

	record := transport.CapabilityRecord{
		Endpoint:       "my endpoint",
		Model:          "org/llama3:8b-q4",
		AdapterVersion: "1.0.0",
	}
	rawPath, err := WriteRaw(record, tempDir)
	if err != nil {
		t.Fatalf("WriteRaw failed: %v", err)
	}
	if got, want := filepath.Base(rawPath), "my-endpoint-org-llama3-8b-q4-20260102T030405Z.raw.json"; got != want {
		t.Errorf("raw file name = %q, want %q", got, want)
	}
	if filepath.Dir(rawPath) != tempDir {
		t.Errorf("raw file written outside dir: %s", rawPath)
	}

	export := Export{Redacted: true, RedactionRule: "evidence/v1", EndpointName: record.Endpoint, Model: record.Model}
	exportPath, err := WriteExport(export, tempDir)
	if err != nil {
		t.Fatalf("WriteExport failed: %v", err)
	}
	if got, want := filepath.Base(exportPath), "my-endpoint-org-llama3-8b-q4-20260102T030405Z.json"; got != want {
		t.Errorf("export file name = %q, want %q", got, want)
	}
	if filepath.Dir(exportPath) != tempDir {
		t.Errorf("export file written outside dir: %s", exportPath)
	}
}

// TestConfigFingerprintConsistent verifies that the same input produces the same hash.
func TestConfigFingerprintConsistent(t *testing.T) {
	profile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
		MaxTokensField:    "max_tokens",
		Models:            []string{"model-a", "model-b"},
	}

	fp1, err := ConfigFingerprint(profile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("First ConfigFingerprint failed: %v", err)
	}

	fp2, err := ConfigFingerprint(profile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("Second ConfigFingerprint failed: %v", err)
	}

	if fp1 != fp2 {
		t.Errorf("Fingerprints differ for identical input: %q vs %q", fp1, fp2)
	}

	// Check format: should be lowercase hex SHA-256 (64 chars)
	if len(fp1) != 64 {
		t.Errorf("Expected 64-char hex fingerprint, got length %d", len(fp1))
	}
	for _, c := range fp1 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("Fingerprint contains non-hex character: %c", c)
		}
	}
}

// TestConfigFingerprintChangesOnBaseURLChange verifies that fingerprint changes
// when base URL changes.
func TestConfigFingerprintChangesOnBaseURLChange(t *testing.T) {
	baseProfile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
	}

	fp1, err := ConfigFingerprint(baseProfile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("First ConfigFingerprint failed: %v", err)
	}

	modifiedProfile := baseProfile
	modifiedProfile.BaseURL = "http://localhost:9000/v1"

	fp2, err := ConfigFingerprint(modifiedProfile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("Second ConfigFingerprint failed: %v", err)
	}

	if fp1 == fp2 {
		t.Errorf("Fingerprint should change when base URL changes, but got same value: %q", fp1)
	}
}

// TestConfigFingerprintChangesOnModelChange verifies that fingerprint changes
// when model changes.
func TestConfigFingerprintChangesOnModelChange(t *testing.T) {
	profile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
	}

	fp1, err := ConfigFingerprint(profile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("First ConfigFingerprint failed: %v", err)
	}

	fp2, err := ConfigFingerprint(profile, "model-b", "1.0.0")
	if err != nil {
		t.Fatalf("Second ConfigFingerprint failed: %v", err)
	}

	if fp1 == fp2 {
		t.Errorf("Fingerprint should change when model changes, but got same value: %q", fp1)
	}
}

// TestConfigFingerprintChangesOnAdapterVersionChange verifies that fingerprint
// changes when adapter version changes.
func TestConfigFingerprintChangesOnAdapterVersionChange(t *testing.T) {
	profile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
	}

	fp1, err := ConfigFingerprint(profile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("First ConfigFingerprint failed: %v", err)
	}

	fp2, err := ConfigFingerprint(profile, "model-a", "2.0.0")
	if err != nil {
		t.Fatalf("Second ConfigFingerprint failed: %v", err)
	}

	if fp1 == fp2 {
		t.Errorf("Fingerprint should change when adapter version changes, but got same value: %q", fp1)
	}
}

// TestConfigFingerprintChangesOnAuthChange verifies that fingerprint changes
// when auth reference changes.
func TestConfigFingerprintChangesOnAuthChange(t *testing.T) {
	profile1 := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
		Auth: &config.SecretRef{
			Source: "env",
			Name:   "KEY1",
		},
	}

	fp1, err := ConfigFingerprint(profile1, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("First ConfigFingerprint failed: %v", err)
	}

	profile2 := profile1
	profile2.Auth = &config.SecretRef{
		Source: "env",
		Name:   "KEY2",
	}

	fp2, err := ConfigFingerprint(profile2, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("Second ConfigFingerprint failed: %v", err)
	}

	if fp1 == fp2 {
		t.Errorf("Fingerprint should change when auth reference changes")
	}
}

// TestConfigFingerprintOrderIndependent verifies that map insertion order
// doesn't affect the fingerprint.
func TestConfigFingerprintOrderIndependent(t *testing.T) {
	profile1 := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
		ExtraHeaders: map[string]config.SecretRef{
			"X-Header-A": {Source: "env", Name: "HDR_A"},
			"X-Header-B": {Source: "env", Name: "HDR_B"},
		},
	}

	fp1, err := ConfigFingerprint(profile1, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("First ConfigFingerprint failed: %v", err)
	}

	// Create profile2 with same headers but inserted in different order.
	profile2 := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
		ExtraHeaders: map[string]config.SecretRef{
			"X-Header-B": {Source: "env", Name: "HDR_B"},
			"X-Header-A": {Source: "env", Name: "HDR_A"},
		},
	}

	fp2, err := ConfigFingerprint(profile2, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("Second ConfigFingerprint failed: %v", err)
	}

	if fp1 != fp2 {
		t.Errorf("Fingerprints should be equal regardless of map insertion order")
	}
}

// TestCalibrationRecordAvailableMarshaling verifies that an available
// calibration marshals to an object.
func TestCalibrationRecordAvailableMarshaling(t *testing.T) {
	cal := CalibrationRecord{
		Available:            true,
		BytesPerToken:        0.5,
		ObservedPromptTokens: 100,
		BaselinePromptTokens: 80,
	}

	data, err := json.Marshal(cal)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if bytes, ok := obj["bytes_per_token"]; !ok || bytes != 0.5 {
		t.Errorf("Expected bytes_per_token 0.5 in object")
	}
	if tokens, ok := obj["observed_prompt_tokens"]; !ok || tokens != float64(100) {
		t.Errorf("Expected observed_prompt_tokens 100 in object")
	}
	if baseline, ok := obj["baseline_prompt_tokens"]; !ok || baseline != float64(80) {
		t.Errorf("Expected baseline_prompt_tokens 80 in object")
	}
}

// TestCalibrationRecordUnavailableMarshaling verifies that an unavailable
// calibration marshals to "unavailable" string.
func TestCalibrationRecordUnavailableMarshaling(t *testing.T) {
	cal := CalibrationRecord{
		Available: false,
	}

	data, err := json.Marshal(cal)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		t.Fatalf("Unmarshal to string failed: %v", err)
	}

	if str != "unavailable" {
		t.Errorf("Expected 'unavailable', got %q", str)
	}
}

// TestCalibrationRecordRoundTrip verifies that a CalibrationRecord
// round-trips through marshaling.
func TestCalibrationRecordRoundTrip(t *testing.T) {
	original := CalibrationRecord{
		Available:            true,
		BytesPerToken:        0.75,
		ObservedPromptTokens: 150,
		BaselinePromptTokens: 120,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var unmarshaled CalibrationRecord
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if unmarshaled != original {
		t.Errorf("Round-trip failed: original %+v, unmarshaled %+v", original, unmarshaled)
	}
}

// TestNewProbeManifest verifies that NewProbeManifest creates a valid manifest.
func TestNewProbeManifest(t *testing.T) {
	profile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
	}

	export := Export{
		Redacted:      true,
		RedactionRule: "evidence/v1",
		EndpointName:  "test-endpoint",
		Model:         "test-model",
	}

	cal := transport.Calibration{
		Available:            true,
		BytesPerToken:        0.5,
		ObservedPromptTokens: 100,
		BaselinePromptTokens: 80,
	}

	manifest, err := NewProbeManifest(export, profile, "test-model", "1.0.0", cal)
	if err != nil {
		t.Fatalf("NewProbeManifest failed: %v", err)
	}

	if manifest.Export.EndpointName != export.EndpointName {
		t.Errorf("Export.EndpointName mismatch")
	}
	if manifest.FingerprintRule != FingerprintRule {
		t.Errorf("FingerprintRule expected %q, got %q", FingerprintRule, manifest.FingerprintRule)
	}
	if manifest.ConfigFingerprint == "" {
		t.Errorf("ConfigFingerprint is empty")
	}
	if manifest.Calibration.Available != true {
		t.Errorf("Calibration.Available expected true, got false")
	}
}

// TestParseProbeManifestWrapped verifies that ParseProbeManifest can parse
// the new wrapped shape.
func TestParseProbeManifestWrapped(t *testing.T) {
	profile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
	}

	export := Export{
		Redacted:      true,
		RedactionRule: "evidence/v1",
		EndpointName:  "test-endpoint",
		Model:         "test-model",
	}

	cal := transport.Calibration{
		Available:            true,
		BytesPerToken:        0.5,
		ObservedPromptTokens: 100,
		BaselinePromptTokens: 80,
	}

	original, err := NewProbeManifest(export, profile, "test-model", "1.0.0", cal)
	if err != nil {
		t.Fatalf("NewProbeManifest failed: %v", err)
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	parsed, hasFingerprint, err := ParseProbeManifest(data)
	if err != nil {
		t.Fatalf("ParseProbeManifest failed: %v", err)
	}

	if !hasFingerprint {
		t.Errorf("Expected hasFingerprint=true, got false")
	}
	if parsed.ConfigFingerprint != original.ConfigFingerprint {
		t.Errorf("ConfigFingerprint mismatch: %q vs %q", parsed.ConfigFingerprint, original.ConfigFingerprint)
	}
	if parsed.FingerprintRule != original.FingerprintRule {
		t.Errorf("FingerprintRule mismatch")
	}
	if parsed.Calibration.Available != original.Calibration.Available {
		t.Errorf("Calibration.Available mismatch")
	}
}

// TestParseProbeManifestBareExport verifies that ParseProbeManifest can parse
// a bare export (old format).
func TestParseProbeManifestBareExport(t *testing.T) {
	export := Export{
		Redacted:      true,
		RedactionRule: "evidence/v1",
		EndpointName:  "test-endpoint",
		Model:         "test-model",
	}

	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("Marshal export failed: %v", err)
	}

	parsed, hasFingerprint, err := ParseProbeManifest(data)
	if err != nil {
		t.Fatalf("ParseProbeManifest failed: %v", err)
	}

	if hasFingerprint {
		t.Errorf("Expected hasFingerprint=false for bare export, got true")
	}
	if parsed.Export.EndpointName != export.EndpointName {
		t.Errorf("Parsed export EndpointName doesn't match original")
	}
	if parsed.ConfigFingerprint != "" {
		t.Errorf("Expected empty ConfigFingerprint for bare export")
	}
}

// TestParseProbeManifestEmptyObject verifies that ParseProbeManifest rejects
// an empty object.
func TestParseProbeManifestEmptyObject(t *testing.T) {
	_, _, err := ParseProbeManifest(json.RawMessage("{}"))
	if err == nil {
		t.Errorf("Expected error for empty object, got nil")
	}
}

// TestParseProbeManifestArray verifies that ParseProbeManifest rejects an array.
func TestParseProbeManifestArray(t *testing.T) {
	_, _, err := ParseProbeManifest(json.RawMessage("[]"))
	if err == nil {
		t.Errorf("Expected error for array, got nil")
	}
}

// TestManifestSentinelInReference verifies that a sentinel secret in a reference
// is preserved in the fingerprint, but only the reference name appears.
func TestManifestSentinelInReference(t *testing.T) {
	sentinelSecret := "[SENSITIVE_SECRET_12345]"

	profile := config.EndpointProfile{
		Name:              "test-endpoint",
		BaseURL:           "http://localhost:8000/v1",
		DeclaredLocation:  "local",
		DeclaredExecution: "local",
		DeclaredBilling:   "none",
		NetworkDependency: "none",
		Dialect:           "chat_completions_v1",
		Auth: &config.SecretRef{
			Source: "env",
			Name:   "API_KEY", // This is the reference name, not the sentinel
		},
	}

	fp1, err := ConfigFingerprint(profile, "model-a", "1.0.0")
	if err != nil {
		t.Fatalf("ConfigFingerprint failed: %v", err)
	}

	// The fingerprint should not contain the sentinel
	if contains(fp1, sentinelSecret) {
		t.Errorf("Fingerprint contains sentinel secret value")
	}

	// Verify it only contains the reference name
	data, _ := json.Marshal(profile)
	if !contains(string(data), "API_KEY") {
		t.Errorf("Profile should contain reference name API_KEY")
	}
}
