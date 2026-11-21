package journal_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

func TestCreateEngineRun(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	manifest := json.RawMessage(`{"key":"value"}`)

	runID, manifestHash, err := store.CreateEngineRun(manifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	if runID == "" {
		t.Error("runID is empty")
	}

	if manifestHash == "" {
		t.Error("manifestHash is empty")
	}

	// Verify the run is stored with kind='engine'
	conn := db.Conn()
	var kind, stored string
	err = conn.QueryRow("SELECT kind, manifest FROM runs WHERE id = ?", runID).Scan(&kind, &stored)
	if err != nil {
		t.Fatalf("failed to query run: %v", err)
	}
	if kind != "engine" {
		t.Errorf("expected kind='engine', got %q", kind)
	}
	if stored != string(manifest) {
		t.Errorf("manifest mismatch: got %q, want %q", stored, manifest)
	}
}

func TestCreateRunStillUsesProbeKind(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	manifest := json.RawMessage(`{"key":"value"}`)

	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Verify the run is stored with kind='probe'
	conn := db.Conn()
	var kind string
	err = conn.QueryRow("SELECT kind FROM runs WHERE id = ?", runID).Scan(&kind)
	if err != nil {
		t.Fatalf("failed to query run: %v", err)
	}
	if kind != "probe" {
		t.Errorf("expected kind='probe', got %q", kind)
	}
}

func TestLatestProbeRecordBareFormat(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a probe run with bare format
	manifest := json.RawMessage(`{"endpoint_name":"a","model":"m","data":"test"}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	rec, found, err := store.LatestProbeRecord("a", "m")
	if err != nil {
		t.Fatalf("LatestProbeRecord failed: %v", err)
	}
	if !found {
		t.Error("expected to find probe record")
	}
	if rec.RunID != runID {
		t.Errorf("expected runID %q, got %q", runID, rec.RunID)
	}
	if string(rec.Manifest) != string(manifest) {
		t.Errorf("manifest mismatch")
	}
}

func TestLatestProbeRecordWrappedFormat(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a probe run with wrapped format
	manifest := json.RawMessage(`{
		"export":{"endpoint_name":"a","model":"m","data":"test"},
		"config_fingerprint":"xyz"
	}`)
	runID, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	rec, found, err := store.LatestProbeRecord("a", "m")
	if err != nil {
		t.Fatalf("LatestProbeRecord failed: %v", err)
	}
	if !found {
		t.Error("expected to find probe record")
	}
	if rec.RunID != runID {
		t.Errorf("expected runID %q, got %q", runID, rec.RunID)
	}
}

func TestLatestProbeRecordPreferWrapped(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create bare format probe first
	bareManifest := json.RawMessage(`{"endpoint_name":"a","model":"m","type":"bare"}`)
	_, _, err := store.CreateRun(bareManifest)
	if err != nil {
		t.Fatalf("CreateRun bare failed: %v", err)
	}

	// Wait a tiny bit to ensure different timestamps or use same second
	// Create wrapped format probe in same second
	wrappedManifest := json.RawMessage(`{
		"export":{"endpoint_name":"a","model":"m","type":"wrapped"},
		"config_fingerprint":"xyz"
	}`)
	wrappedRunID, _, err := store.CreateRun(wrappedManifest)
	if err != nil {
		t.Fatalf("CreateRun wrapped failed: %v", err)
	}

	rec, found, err := store.LatestProbeRecord("a", "m")
	if err != nil {
		t.Fatalf("LatestProbeRecord failed: %v", err)
	}
	if !found {
		t.Error("expected to find probe record")
	}
	// Should return the wrapped one because it was created later (insertion order)
	if rec.RunID != wrappedRunID {
		t.Errorf("expected wrappedRunID %q, got %q", wrappedRunID, rec.RunID)
	}
}

func TestLatestProbeRecordMultipleEndpoints(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create three probe runs
	manifest1 := json.RawMessage(`{"endpoint_name":"a","model":"m"}`)
	_, _, err := store.CreateRun(manifest1)
	if err != nil {
		t.Fatalf("CreateRun 1 failed: %v", err)
	}

	manifest2 := json.RawMessage(`{"endpoint_name":"a","model":"m","type":"wrapped"}`)
	runID2, _, err := store.CreateRun(manifest2)
	if err != nil {
		t.Fatalf("CreateRun 2 failed: %v", err)
	}

	manifest3 := json.RawMessage(`{"endpoint_name":"b","model":"m"}`)
	runID3, _, err := store.CreateRun(manifest3)
	if err != nil {
		t.Fatalf("CreateRun 3 failed: %v", err)
	}

	// Test endpoint a
	rec, found, err := store.LatestProbeRecord("a", "m")
	if err != nil {
		t.Fatalf("LatestProbeRecord a failed: %v", err)
	}
	if !found {
		t.Error("expected to find probe record for a")
	}
	if rec.RunID != runID2 {
		t.Errorf("expected runID2 %q, got %q", runID2, rec.RunID)
	}

	// Test endpoint b
	rec, found, err = store.LatestProbeRecord("b", "m")
	if err != nil {
		t.Fatalf("LatestProbeRecord b failed: %v", err)
	}
	if !found {
		t.Error("expected to find probe record for b")
	}
	if rec.RunID != runID3 {
		t.Errorf("expected runID3 %q, got %q", runID3, rec.RunID)
	}

	// Test non-existent combination
	rec, found, err = store.LatestProbeRecord("a", "other")
	if err != nil {
		t.Fatalf("LatestProbeRecord other failed: %v", err)
	}
	if found {
		t.Error("expected not to find probe record for other")
	}
}

func TestLatestProbeRecordIgnoresEngineRuns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run with endpoint_name and model
	engineManifest := json.RawMessage(`{"endpoint_name":"a","model":"m"}`)
	_, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Try to find it as a probe record
	_, found, err := store.LatestProbeRecord("a", "m")
	if err != nil {
		t.Fatalf("LatestProbeRecord failed: %v", err)
	}
	if found {
		t.Error("expected not to find engine run as probe record")
	}
}

func TestLatestProbeRecordEmptyFieldsDoNotMatch(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create a probe run with wrapped format but missing export key
	manifest := json.RawMessage(`{"config_fingerprint":"xyz"}`)
	_, _, err := store.CreateRun(manifest)
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Try to find with empty fields
	rec, found, err := store.LatestProbeRecord("", "")
	if err != nil {
		t.Fatalf("LatestProbeRecord failed: %v", err)
	}
	if found {
		t.Error("expected not to find probe record with empty fields")
	}
	_ = rec
}

func TestCreateEngineTurnRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run first
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Create an engine turn
	deadline := time.Now().Add(1 * time.Hour)
	budget := json.RawMessage(`{"tokens":1000}`)
	params := journal.TurnParams{
		RunID:             runID,
		Mode:              "solo",
		Preset:            "default",
		QuickProfile:      "fast",
		SubmissionID:      "sub-123",
		SubmissionHash:    "hash-456",
		AttemptDeadlineMS: 5000,
		TurnDeadlineMS:    10000,
		TurnDeadline:      deadline,
		BudgetJSON:        budget,
	}

	var turnID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateEngineTurn(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateEngineTurn failed: %v", err)
	}

	// Get the turn back
	row, err := store.GetTurn(turnID)
	if err != nil {
		t.Fatalf("GetTurn failed: %v", err)
	}

	// Verify all fields round-trip
	if row.RunID != runID {
		t.Errorf("RunID mismatch: got %q, want %q", row.RunID, runID)
	}
	if row.Mode != "solo" {
		t.Errorf("Mode mismatch: got %q, want solo", row.Mode)
	}
	if row.Preset != "default" {
		t.Errorf("Preset mismatch: got %q, want default", row.Preset)
	}
	if row.QuickProfile != "fast" {
		t.Errorf("QuickProfile mismatch: got %q, want fast", row.QuickProfile)
	}
	if row.SubmissionID != "sub-123" {
		t.Errorf("SubmissionID mismatch: got %q, want sub-123", row.SubmissionID)
	}
	if row.SubmissionHash != "hash-456" {
		t.Errorf("SubmissionHash mismatch: got %q, want hash-456", row.SubmissionHash)
	}
	if row.AttemptDeadlineMS != 5000 {
		t.Errorf("AttemptDeadlineMS mismatch: got %d, want 5000", row.AttemptDeadlineMS)
	}
	if row.TurnDeadlineMS != 10000 {
		t.Errorf("TurnDeadlineMS mismatch: got %d, want 10000", row.TurnDeadlineMS)
	}
	if row.TurnDeadline == nil {
		t.Error("TurnDeadline is nil")
	} else if row.TurnDeadline.Unix() != deadline.Unix() {
		t.Errorf("TurnDeadline mismatch: got %v, want %v", row.TurnDeadline, deadline)
	}
	if string(row.BudgetJSON) != string(budget) {
		t.Errorf("BudgetJSON mismatch: got %s, want %s", row.BudgetJSON, budget)
	}
	if row.Epoch != 0 {
		t.Errorf("Epoch should be 0, got %d", row.Epoch)
	}
	if row.TerminalState != "" {
		t.Errorf("TerminalState should be empty, got %q", row.TerminalState)
	}
	if row.TerminalReason != "" {
		t.Errorf("TerminalReason should be empty, got %q", row.TerminalReason)
	}
}

func TestCreateEngineTurnNanosecondPrecision(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run first
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Create an engine turn with nanosecond-precision deadline
	deadline := time.Now().Add(1 * time.Hour).Add(123 * time.Nanosecond)
	params := journal.TurnParams{
		RunID:        runID,
		Mode:         "solo",
		SubmissionID: "sub-nano",
		TurnDeadline: deadline,
	}

	var turnID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateEngineTurn(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateEngineTurn failed: %v", err)
	}

	// Get the turn back
	row, err := store.GetTurn(turnID)
	if err != nil {
		t.Fatalf("GetTurn failed: %v", err)
	}

	// Verify nanosecond precision is preserved
	if row.TurnDeadline == nil {
		t.Error("TurnDeadline is nil")
	} else if row.TurnDeadline.Nanosecond() != deadline.Nanosecond() {
		t.Errorf("Nanosecond precision lost: got %d, want %d", row.TurnDeadline.Nanosecond(), deadline.Nanosecond())
	}
}

func TestCreateEngineTurnDuplicateSubmissionID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Create first engine turn
	params := journal.TurnParams{
		RunID:        runID,
		Mode:         "solo",
		SubmissionID: "sub-dup",
	}

	var turnID1 string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID1, txErr = tx.CreateEngineTurn(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("first CreateEngineTurn failed: %v", err)
	}

	// Try to create second engine turn with same SubmissionID
	err = store.Tx(func(tx *journal.Tx) error {
		_, txErr := tx.CreateEngineTurn(params)
		return txErr
	})
	if err == nil {
		t.Error("expected error for duplicate SubmissionID")
	}
	if !errors.Is(err, journal.ErrDuplicateSubmission) {
		t.Errorf("expected ErrDuplicateSubmission, got %T: %v", err, err)
	}

	// Verify the first turn still exists
	row, err := store.GetTurn(turnID1)
	if err != nil {
		t.Fatalf("GetTurn failed: %v", err)
	}
	if row.SubmissionID != "sub-dup" {
		t.Errorf("SubmissionID mismatch: got %q", row.SubmissionID)
	}
}

func TestFindSubmission(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Create an engine turn
	params := journal.TurnParams{
		RunID:        runID,
		Mode:         "solo",
		SubmissionID: "sub-find",
	}

	var turnID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateEngineTurn(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateEngineTurn failed: %v", err)
	}

	// Find by submission ID
	row, found, err := store.FindSubmission("sub-find")
	if err != nil {
		t.Fatalf("FindSubmission failed: %v", err)
	}
	if !found {
		t.Error("expected to find submission")
	}
	if row.ID != turnID {
		t.Errorf("expected turnID %q, got %q", turnID, row.ID)
	}

	// Try to find non-existent submission
	row, found, err = store.FindSubmission("sub-notfound")
	if err != nil {
		t.Fatalf("FindSubmission notfound failed: %v", err)
	}
	if found {
		t.Error("expected not to find submission")
	}
	if row != nil {
		t.Error("expected nil row for not found")
	}
}

func TestOpenTurns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Create two engine turns
	params1 := journal.TurnParams{
		RunID:        runID,
		Mode:         "solo",
		SubmissionID: "sub-1",
	}
	var turnID1 string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID1, txErr = tx.CreateEngineTurn(params1)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateEngineTurn 1 failed: %v", err)
	}

	params2 := journal.TurnParams{
		RunID:        runID,
		Mode:         "solo",
		SubmissionID: "sub-2",
	}
	var turnID2 string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID2, txErr = tx.CreateEngineTurn(params2)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateEngineTurn 2 failed: %v", err)
	}

	// Get open turns
	turnIDs, err := store.OpenTurns()
	if err != nil {
		t.Fatalf("OpenTurns failed: %v", err)
	}
	if len(turnIDs) != 2 {
		t.Errorf("expected 2 open turns, got %d", len(turnIDs))
	}
	if len(turnIDs) > 0 && turnIDs[0] != turnID1 {
		t.Errorf("expected first turn to be %q, got %q", turnID1, turnIDs[0])
	}

	// End the first turn
	err = store.Tx(func(tx *journal.Tx) error {
		return tx.EndTurn(turnID1, "approved", "done")
	})
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}

	// Get open turns again
	turnIDs, err = store.OpenTurns()
	if err != nil {
		t.Fatalf("OpenTurns after end failed: %v", err)
	}
	if len(turnIDs) != 1 {
		t.Errorf("expected 1 open turn, got %d", len(turnIDs))
	}
	if len(turnIDs) > 0 && turnIDs[0] != turnID2 {
		t.Errorf("expected remaining turn to be %q, got %q", turnID2, turnIDs[0])
	}
}

func TestSetTurnBudget(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Create an engine turn without budget
	params := journal.TurnParams{
		RunID:        runID,
		Mode:         "solo",
		SubmissionID: "sub-budget",
	}

	var turnID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateEngineTurn(params)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateEngineTurn failed: %v", err)
	}

	// Set a valid budget
	validBudget := json.RawMessage(`{"tokens":5000,"cost":10.5}`)
	err = store.SetTurnBudget(turnID, validBudget)
	if err != nil {
		t.Fatalf("SetTurnBudget with valid JSON failed: %v", err)
	}

	// Verify budget is stored
	row, err := store.GetTurn(turnID)
	if err != nil {
		t.Fatalf("GetTurn failed: %v", err)
	}
	if string(row.BudgetJSON) != string(validBudget) {
		t.Errorf("budget mismatch: got %s, want %s", row.BudgetJSON, validBudget)
	}

	// Try to set an invalid budget
	invalidBudget := json.RawMessage(`{invalid json}`)
	err = store.SetTurnBudget(turnID, invalidBudget)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Verify budget wasn't changed
	row, err = store.GetTurn(turnID)
	if err != nil {
		t.Fatalf("GetTurn after invalid set failed: %v", err)
	}
	if string(row.BudgetJSON) != string(validBudget) {
		t.Errorf("budget should not have changed: got %s, want %s", row.BudgetJSON, validBudget)
	}
}

func TestGetTurnNullableColumns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	// Create a legacy turn directly in DB (simulating old data)
	probeRunID := "legacy-probe-run"
	_, err := conn.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash, kind)
		VALUES (?, '{}', 'hash', 'probe')
	`, probeRunID)
	if err != nil {
		t.Fatalf("failed to create legacy probe run: %v", err)
	}

	legacyConvID := "legacy-conv-1"
	_, err = conn.Exec(`
		INSERT INTO conversations (id, workspace)
		VALUES (?, NULL)
	`, legacyConvID)
	if err != nil {
		t.Fatalf("failed to create legacy conversation: %v", err)
	}

	legacyTurnID := "legacy-turn-1"
	_, err = conn.Exec(`
		INSERT INTO turns (id, run_id, mode, conversation_id)
		VALUES (?, ?, 'solo', ?)
	`, legacyTurnID, probeRunID, legacyConvID)
	if err != nil {
		t.Fatalf("failed to create legacy turn: %v", err)
	}

	// Get the turn - should handle all nullable columns
	row, err := store.GetTurn(legacyTurnID)
	if err != nil {
		t.Fatalf("GetTurn for legacy turn failed: %v", err)
	}

	// Verify nullable fields are zero values
	if row.SubmissionID != "" {
		t.Errorf("expected empty SubmissionID, got %q", row.SubmissionID)
	}
	if row.SubmissionHash != "" {
		t.Errorf("expected empty SubmissionHash, got %q", row.SubmissionHash)
	}
	if row.PhaseAtCancel != "" {
		t.Errorf("expected empty PhaseAtCancel, got %q", row.PhaseAtCancel)
	}
	if row.TurnDeadline != nil {
		t.Errorf("expected nil TurnDeadline, got %v", row.TurnDeadline)
	}
	if row.AttemptDeadlineMS != 0 {
		t.Errorf("expected 0 AttemptDeadlineMS, got %d", row.AttemptDeadlineMS)
	}
	if row.TurnDeadlineMS != 0 {
		t.Errorf("expected 0 TurnDeadlineMS, got %d", row.TurnDeadlineMS)
	}
	if row.TerminalState != "" {
		t.Errorf("expected empty TerminalState, got %q", row.TerminalState)
	}
	if row.TerminalReason != "" {
		t.Errorf("expected empty TerminalReason, got %q", row.TerminalReason)
	}
	if len(row.BudgetJSON) != 0 {
		t.Errorf("expected empty BudgetJSON, got %s", row.BudgetJSON)
	}
}

func TestCreateEngineTurnRequiresSubmissionID(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Create an engine run
	engineManifest := json.RawMessage(`{"test":"data"}`)
	runID, _, err := store.CreateEngineRun(engineManifest)
	if err != nil {
		t.Fatalf("CreateEngineRun failed: %v", err)
	}

	// Try to create engine turn without SubmissionID
	params := journal.TurnParams{
		RunID: runID,
		Mode:  "solo",
	}

	err = store.Tx(func(tx *journal.Tx) error {
		_, txErr := tx.CreateEngineTurn(params)
		return txErr
	})
	if err == nil {
		t.Error("expected error when SubmissionID is empty")
	}
}

func TestAllocateEngineRun(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatalf("AllocateEngineRun failed: %v", err)
	}

	if runID == "" {
		t.Error("runID is empty")
	}

	// Verify the run is stored with state='probing' and empty manifest
	conn := db.Conn()
	var kind, state, manifest, manifestHash string
	err = conn.QueryRow("SELECT kind, state, manifest, manifest_hash FROM runs WHERE id = ?", runID).Scan(&kind, &state, &manifest, &manifestHash)
	if err != nil {
		t.Fatalf("failed to query run: %v", err)
	}
	if kind != "engine" {
		t.Errorf("expected kind='engine', got %q", kind)
	}
	if state != "probing" {
		t.Errorf("expected state='probing', got %q", state)
	}
	if manifest != "" {
		t.Errorf("expected empty manifest, got %q", manifest)
	}
	if manifestHash != "" {
		t.Errorf("expected empty manifest_hash, got %q", manifestHash)
	}
}

func TestFinalizeEngineRunManifest(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Allocate a run first
	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatalf("AllocateEngineRun failed: %v", err)
	}

	// Finalize with a manifest
	manifest := json.RawMessage(`{"key":"value","data":123}`)
	manifestHash, err := store.FinalizeEngineRunManifest(runID, manifest)
	if err != nil {
		t.Fatalf("FinalizeEngineRunManifest failed: %v", err)
	}

	if manifestHash == "" {
		t.Error("manifestHash is empty")
	}

	// Verify the run state changed to 'started' and manifest was written
	conn := db.Conn()
	var state, stored string
	err = conn.QueryRow("SELECT state, manifest FROM runs WHERE id = ?", runID).Scan(&state, &stored)
	if err != nil {
		t.Fatalf("failed to query run: %v", err)
	}
	if state != "started" {
		t.Errorf("expected state='started', got %q", state)
	}
	if stored != string(manifest) {
		t.Errorf("manifest mismatch: got %q, want %q", stored, manifest)
	}
}

func TestManifestImmutabilityAfterFinalize(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Allocate and finalize
	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatalf("AllocateEngineRun failed: %v", err)
	}

	manifest := json.RawMessage(`{"original":true}`)
	_, err = store.FinalizeEngineRunManifest(runID, manifest)
	if err != nil {
		t.Fatalf("FinalizeEngineRunManifest failed: %v", err)
	}

	// Try to update the manifest - should fail due to trigger
	conn := db.Conn()
	_, err = conn.Exec(`
		UPDATE runs SET manifest = ?, manifest_hash = ? WHERE id = ?
	`, `{"new":true}`, "new_hash", runID)
	if err == nil {
		t.Error("expected error when trying to update immutable manifest")
	}
}

func TestAllocateAndFinalizeLifecycle(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	// Step 1: Allocate run in probing state
	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatalf("AllocateEngineRun failed: %v", err)
	}

	conn := db.Conn()
	var state1 string
	err = conn.QueryRow("SELECT state FROM runs WHERE id = ?", runID).Scan(&state1)
	if err != nil {
		t.Fatalf("failed to query initial state: %v", err)
	}
	if state1 != "probing" {
		t.Errorf("after allocation, expected state='probing', got %q", state1)
	}

	// Step 2: Finalize manifest
	manifest := json.RawMessage(`{"endpoint":"test","model":"gpt-4"}`)
	_, err = store.FinalizeEngineRunManifest(runID, manifest)
	if err != nil {
		t.Fatalf("FinalizeEngineRunManifest failed: %v", err)
	}

	// Step 3: Verify final state
	var state2 string
	var stored string
	err = conn.QueryRow("SELECT state, manifest FROM runs WHERE id = ?", runID).Scan(&state2, &stored)
	if err != nil {
		t.Fatalf("failed to query final state: %v", err)
	}
	if state2 != "started" {
		t.Errorf("after finalization, expected state='started', got %q", state2)
	}
	if stored != string(manifest) {
		t.Errorf("manifest mismatch: got %q, want %q", stored, manifest)
	}
}

func TestFinalizeWithInvalidJSON(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	runID, err := store.AllocateEngineRun()
	if err != nil {
		t.Fatalf("AllocateEngineRun failed: %v", err)
	}

	// Try to finalize with invalid JSON
	invalidJSON := json.RawMessage(`{invalid}`)
	_, err = store.FinalizeEngineRunManifest(runID, invalidJSON)
	if err == nil {
		t.Error("expected error when finalizing with invalid JSON")
	}
}
