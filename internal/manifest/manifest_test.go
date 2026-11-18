package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
)

func TestBuild_CalibrationSafety_Default(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	safety := 0.75
	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{CalibrationSafety: &safety},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 4.0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	p := built.Participants["p1"]
	if !p.Usable {
		t.Error("participant should be usable")
	}

	if p.BytesPerToken == nil {
		t.Fatal("BytesPerToken should not be nil")
	}

	expected := 3.0
	if *p.BytesPerToken != expected {
		t.Errorf("expected %v, got %v", expected, *p.BytesPerToken)
	}
	if p.BytesPerTokenSource != "probe" {
		t.Errorf("expected source probe, got %q", p.BytesPerTokenSource)
	}
}

func TestBuild_CalibrationSafety_NonDefault(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	safety := 0.5
	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{CalibrationSafety: &safety},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 4.0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	p := built.Participants["p1"]
	if !p.Usable {
		t.Error("participant should be usable")
	}

	if p.BytesPerToken == nil {
		t.Fatal("BytesPerToken should not be nil")
	}

	expected := 2.0
	if *p.BytesPerToken != expected {
		t.Errorf("expected %v, got %v", expected, *p.BytesPerToken)
	}
	if p.BytesPerTokenSource != "probe" {
		t.Errorf("expected source probe, got %q", p.BytesPerTokenSource)
	}
}

func TestBuild_ContextBoundProbeWinsRoster(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	contextTokens := 2000
	roster := &config.Roster{
		Participants: []config.Participant{{
			ID:            "p1",
			Endpoint:      "ep1",
			Model:         "m1",
			ContextTokens: &contextTokens,
		}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRunWithContext(t, store, "ep1", "m1", 4000)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	p := built.Participants["p1"]
	if p.ContextBound == nil || *p.ContextBound != 4000 {
		t.Errorf("expected context bound 4000, got %v", p.ContextBound)
	}
	if p.ContextSource != "probe" {
		t.Errorf("expected source probe, got %q", p.ContextSource)
	}
}

func TestBuild_SharedEndpointModel(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants: []config.Participant{
			{ID: "p1", Endpoint: "ep1", Model: "m1"},
			{ID: "p2", Endpoint: "ep1", Model: "m1"},
		},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !built.Participants["p1"].Usable {
		t.Error("p1 should be usable")
	}
	if !built.Participants["p2"].Usable {
		t.Error("p2 should be usable")
	}

	if built.Participants["p1"].Expected.ReportedModel != built.Participants["p2"].Expected.ReportedModel {
		t.Error("shared participants should have same expected model")
	}
}

func TestBuild_ManifestJSON_TopLevelKeys(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	var manifest_out map[string]interface{}
	err = json.Unmarshal(built.JSON, &manifest_out)
	if err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	expectedKeys := map[string]bool{
		"kind":               true,
		"engine_version":     true,
		"started_at":         true,
		"roster":             true,
		"roster_path":        true,
		"roster_file_sha256": true,
		"probe_records":      true,
		"expected":           true,
	}

	for key := range manifest_out {
		if !expectedKeys[key] {
			t.Errorf("unexpected key in manifest: %s", key)
		}
		delete(expectedKeys, key)
	}

	if len(expectedKeys) > 0 {
		for key := range expectedKeys {
			t.Errorf("missing key in manifest: %s", key)
		}
	}

	if manifest_out["kind"] != "engine" {
		t.Errorf("expected kind=engine, got %v", manifest_out["kind"])
	}
}

func TestBuild_UnusableParticipantsOmitted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants: []config.Participant{
			{ID: "p1", Endpoint: "ep1", Model: "m1"},
			{ID: "p2", Endpoint: "ep2", Model: "m2"},
		},
		Endpoints: []config.EndpointProfile{
			{Name: "ep1", BaseURL: "http://test"},
			{Name: "ep2", BaseURL: "http://test2"},
		},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !built.Participants["p1"].Usable {
		t.Fatalf("p1 should be usable, detail: %s", built.Participants["p1"].Detail)
	}

	if built.Participants["p2"].Usable {
		t.Fatal("p2 should not be usable")
	}
	if built.Participants["p2"].Detail != "no_record" {
		t.Errorf("p2 detail should be no_record, got %q", built.Participants["p2"].Detail)
	}

	var manifest_out map[string]interface{}
	err = json.Unmarshal(built.JSON, &manifest_out)
	if err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	probeRecords := manifest_out["probe_records"].(map[string]interface{})
	if _, ok := probeRecords["p1"]; !ok {
		t.Error("usable p1 should be in probe_records")
	}
	if _, ok := probeRecords["p2"]; ok {
		t.Error("unusable p2 should not be in probe_records")
	}

	expectedMap := manifest_out["expected"].(map[string]interface{})
	if _, ok := expectedMap["p1"]; !ok {
		t.Error("usable p1 should be in expected")
	}
	if _, ok := expectedMap["p2"]; ok {
		t.Error("unusable p2 should not be in expected")
	}
}

func TestBuild_DeterministicManifest(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	now := time.Now().Truncate(time.Second)

	in1 := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      now,
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built1, err := Build(in1)
	if err != nil {
		t.Fatalf("first Build failed: %v", err)
	}

	in2 := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      now,
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built2, err := Build(in2)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}

	if string(built1.JSON) != string(built2.JSON) {
		t.Error("identical builds should produce identical JSON")
	}
	if built1.RosterHash != built2.RosterHash {
		t.Error("identical rosters should produce identical hash")
	}
}

func TestBuild_DifferentStartedAt_SameRosterHash(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	now := time.Now().Truncate(time.Second)

	in1 := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      now,
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built1, err := Build(in1)
	if err != nil {
		t.Fatalf("first Build failed: %v", err)
	}

	in2 := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      now.Add(time.Hour),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built2, err := Build(in2)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}

	if string(built1.JSON) == string(built2.JSON) {
		t.Error("different StartedAt should produce different JSON")
	}
	if built1.RosterHash != built2.RosterHash {
		t.Error("same roster should produce same hash regardless of StartedAt")
	}
}

func TestRosterFileChanged(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "roster.toml")
	content := []byte("original content")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	hash := sha256hash(content)

	changed, err := RosterFileChanged(path, hash)
	if err != nil {
		t.Fatalf("RosterFileChanged failed: %v", err)
	}
	if changed {
		t.Error("same file should not be reported as changed")
	}

	modifiedContent := []byte("original content\n# comment")
	if err := os.WriteFile(path, modifiedContent, 0600); err != nil {
		t.Fatalf("failed to write modified file: %v", err)
	}

	changed, err = RosterFileChanged(path, hash)
	if err != nil {
		t.Fatalf("RosterFileChanged failed: %v", err)
	}
	if !changed {
		t.Error("modified file should be reported as changed")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove file: %v", err)
	}

	changed, err = RosterFileChanged(path, hash)
	if err != nil {
		t.Fatalf("RosterFileChanged failed: %v", err)
	}
	if !changed {
		t.Error("removed file should be reported as changed")
	}

	// Restore the file and verify it's not reported as changed
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("failed to restore file: %v", err)
	}

	changed, err = RosterFileChanged(path, hash)
	if err != nil {
		t.Fatalf("RosterFileChanged failed: %v", err)
	}
	if changed {
		t.Error("restored file should not be reported as changed")
	}
}

func TestBuild_ExpectedValues(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	p := built.Participants["p1"]
	if !p.Usable {
		t.Error("participant should be usable")
	}

	if p.Expected.ReportedModel != "test-model" {
		t.Errorf("expected reported model test-model, got %q", p.Expected.ReportedModel)
	}

	var manifest_out map[string]interface{}
	err = json.Unmarshal(built.JSON, &manifest_out)
	if err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	expectedMap := manifest_out["expected"].(map[string]interface{})
	if expectedMap["p1"] == nil {
		t.Fatal("p1 should be in expected")
	}

	expectedObj := expectedMap["p1"].(map[string]interface{})
	if expectedObj["reported_model"] != "test-model" {
		t.Errorf("expected reported_model test-model, got %v", expectedObj["reported_model"])
	}
}

func TestBuild_NoFingerprint(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants: []config.Participant{
			{ID: "p1", Endpoint: "ep1", Model: "m1"},
			{ID: "p2", Endpoint: "ep2", Model: "m2"},
		},
		Endpoints: []config.EndpointProfile{
			{Name: "ep1", BaseURL: "http://test"},
			{Name: "ep2", BaseURL: "http://test2"},
		},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)
	createTestProbeRunWithoutFingerprint(t, store, "ep2", "m2")

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if built.Participants["p2"].Usable {
		t.Error("p2 without fingerprint should not be usable")
	}
	if built.Participants["p2"].Detail != "no_fingerprint" {
		t.Errorf("p2 detail should be no_fingerprint, got %q", built.Participants["p2"].Detail)
	}
}

func TestBuild_FingerprintMismatchBaseURL(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants: []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints: []config.EndpointProfile{{
			Name:    "ep1",
			BaseURL: "http://changed-url",
		}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if built.Participants["p1"].Usable {
		t.Error("p1 with mismatched base URL should not be usable")
	}
	if built.Participants["p1"].Detail != "fingerprint_mismatch" {
		t.Errorf("p1 detail should be fingerprint_mismatch, got %q", built.Participants["p1"].Detail)
	}
}

func TestBuild_FingerprintMismatchAdapterVersion(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "2.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if built.Participants["p1"].Usable {
		t.Error("p1 with mismatched adapter version should not be usable")
	}
	if built.Participants["p1"].Detail != "fingerprint_mismatch" {
		t.Errorf("p1 detail should be fingerprint_mismatch, got %q", built.Participants["p1"].Detail)
	}
}

func TestBuild_ContextBoundRosterFallback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	contextTokens := 2000
	roster := &config.Roster{
		Participants: []config.Participant{{
			ID:            "p1",
			Endpoint:      "ep1",
			Model:         "m1",
			ContextTokens: &contextTokens,
		}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRunWithoutContext(t, store, "ep1", "m1")

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	p := built.Participants["p1"]
	if p.ContextBound == nil || *p.ContextBound != 2000 {
		t.Errorf("expected context bound 2000, got %v", p.ContextBound)
	}
	if p.ContextSource != "roster" {
		t.Errorf("expected source roster, got %q", p.ContextSource)
	}
}

func TestBuild_BytesPerTokenRosterFallback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	bytesPerToken := 2.5
	roster := &config.Roster{
		Participants: []config.Participant{{
			ID:            "p1",
			Endpoint:      "ep1",
			Model:         "m1",
			BytesPerToken: &bytesPerToken,
		}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRunWithoutCalibration(t, store, "ep1", "m1")

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	p := built.Participants["p1"]
	if p.BytesPerToken == nil || *p.BytesPerToken != 2.5 {
		t.Errorf("expected bytes per token 2.5, got %v", p.BytesPerToken)
	}
	if p.BytesPerTokenSource != "roster" {
		t.Errorf("expected source roster, got %q", p.BytesPerTokenSource)
	}
}

func TestBuild_ModelChangesRosterHash(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster1 := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	roster2 := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m2"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://test"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)

	now := time.Now().Truncate(time.Second)

	in1 := Input{
		Store:          store,
		Roster:         roster1,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      now,
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built1, err := Build(in1)
	if err != nil {
		t.Fatalf("first Build failed: %v", err)
	}

	in2 := Input{
		Store:          store,
		Roster:         roster2,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      now,
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built2, err := Build(in2)
	if err != nil {
		t.Fatalf("second Build failed: %v", err)
	}

	if built1.RosterHash == built2.RosterHash {
		t.Error("different model should produce different roster hash")
	}
}

func TestBuild_NewerMismatchingRecordOverridesOlder(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	roster := &config.Roster{
		Participants:  []config.Participant{{ID: "p1", Endpoint: "ep1", Model: "m1"}},
		Endpoints:     []config.EndpointProfile{{Name: "ep1", BaseURL: "http://changed-url"}},
		DefaultPreset: "default",
		Presets:       map[string]config.Preset{"default": {Mode: "solo", Author: "p1"}},
		Budgets:       config.Budgets{},
		Pools:         map[string]config.Pool{},
	}

	createTestProbeRun(t, store, "ep1", "m1", 0)
	createTestProbeRun(t, store, "ep1", "m1", 0)

	in := Input{
		Store:          store,
		Roster:         roster,
		RosterPath:     "/path/to/roster.toml",
		RosterFile:     []byte("content"),
		EngineVersion:  "1.0",
		StartedAt:      time.Now(),
		AdapterVersion: func(p config.Participant) string { return "1.0" },
	}

	built, err := Build(in)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if built.Participants["p1"].Usable {
		t.Error("p1 should not be usable when newer record has fingerprint mismatch")
	}
	if built.Participants["p1"].Detail != "fingerprint_mismatch" {
		t.Errorf("p1 detail should be fingerprint_mismatch, got %q", built.Participants["p1"].Detail)
	}
}

func openTestDB(t *testing.T) (*journal.DB, func()) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	db, err := journal.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open journal: %v", err)
	}

	return db, func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close db: %v", err)
		}
	}
}

func createTestProbeRun(t *testing.T, store *journal.Store, endpoint, model string, calibration float64) {
	profile := config.EndpointProfile{
		Name:    endpoint,
		BaseURL: "http://test",
	}

	export := evidence.Export{
		Redacted:                 true,
		RedactionRule:            "redaction/v1",
		EndpointName:             endpoint,
		DeclaredLocation:         "local",
		DeclaredExecution:        "local",
		DeclaredBilling:          "none",
		NetworkDependency:        "none",
		Model:                    model,
		AuthMode:                 "api-key",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            "test-model",
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
	}

	cal := transport.Calibration{}
	if calibration > 0 {
		cal.Available = true
		cal.BytesPerToken = calibration
	}

	manifest, err := evidence.NewProbeManifest(export, profile, model, "1.0", cal)
	if err != nil {
		t.Fatalf("failed to create probe manifest: %v", err)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	_, _, err = store.CreateRun(data)
	if err != nil {
		t.Fatalf("failed to create probe run: %v", err)
	}
}

func createTestProbeRunWithContext(t *testing.T, store *journal.Store, endpoint, model string, contextBound int) {
	profile := config.EndpointProfile{
		Name:    endpoint,
		BaseURL: "http://test",
	}

	cb := contextBound
	export := evidence.Export{
		Redacted:                 true,
		RedactionRule:            "redaction/v1",
		EndpointName:             endpoint,
		DeclaredLocation:         "local",
		DeclaredExecution:        "local",
		DeclaredBilling:          "none",
		NetworkDependency:        "none",
		Model:                    model,
		AuthMode:                 "api-key",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            "test-model",
		ContextBound:             &cb,
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
	}

	cal := transport.Calibration{}
	manifest, err := evidence.NewProbeManifest(export, profile, model, "1.0", cal)
	if err != nil {
		t.Fatalf("failed to create probe manifest: %v", err)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	_, _, err = store.CreateRun(data)
	if err != nil {
		t.Fatalf("failed to create probe run: %v", err)
	}
}

func createTestProbeRunWithoutContext(t *testing.T, store *journal.Store, endpoint, model string) {
	profile := config.EndpointProfile{
		Name:    endpoint,
		BaseURL: "http://test",
	}

	export := evidence.Export{
		Redacted:                 true,
		RedactionRule:            "redaction/v1",
		EndpointName:             endpoint,
		DeclaredLocation:         "local",
		DeclaredExecution:        "local",
		DeclaredBilling:          "none",
		NetworkDependency:        "none",
		Model:                    model,
		AuthMode:                 "api-key",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            "test-model",
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
	}

	cal := transport.Calibration{}
	manifest, err := evidence.NewProbeManifest(export, profile, model, "1.0", cal)
	if err != nil {
		t.Fatalf("failed to create probe manifest: %v", err)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	_, _, err = store.CreateRun(data)
	if err != nil {
		t.Fatalf("failed to create probe run: %v", err)
	}
}

func createTestProbeRunWithoutCalibration(t *testing.T, store *journal.Store, endpoint, model string) {
	profile := config.EndpointProfile{
		Name:    endpoint,
		BaseURL: "http://test",
	}

	export := evidence.Export{
		Redacted:                 true,
		RedactionRule:            "redaction/v1",
		EndpointName:             endpoint,
		DeclaredLocation:         "local",
		DeclaredExecution:        "local",
		DeclaredBilling:          "none",
		NetworkDependency:        "none",
		Model:                    model,
		AuthMode:                 "api-key",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            "test-model",
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
	}

	cal := transport.Calibration{}
	manifest, err := evidence.NewProbeManifest(export, profile, model, "1.0", cal)
	if err != nil {
		t.Fatalf("failed to create probe manifest: %v", err)
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	_, _, err = store.CreateRun(data)
	if err != nil {
		t.Fatalf("failed to create probe run: %v", err)
	}
}

func createTestProbeRunWithoutFingerprint(t *testing.T, store *journal.Store, endpoint, model string) {
	export := evidence.Export{
		Redacted:                 true,
		RedactionRule:            "redaction/v1",
		EndpointName:             endpoint,
		DeclaredLocation:         "local",
		DeclaredExecution:        "local",
		DeclaredBilling:          "none",
		NetworkDependency:        "none",
		Model:                    model,
		AuthMode:                 "api-key",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            "test-model",
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
	}

	bareJSON, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("failed to marshal bare export: %v", err)
	}

	_, _, err = store.CreateRun(bareJSON)
	if err != nil {
		t.Fatalf("failed to create probe run: %v", err)
	}
}

func sha256hash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
