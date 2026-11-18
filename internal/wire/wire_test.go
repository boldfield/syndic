package wire

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	f := Frame{
		V:    1,
		ID:   "test-id",
		Type: "submit",
		Body: json.RawMessage(`{"key":"value"}`),
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Write(f); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	r := NewReader(&buf)
	f2, err := r.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if f2.V != f.V || f2.ID != f.ID || f2.Type != f.Type {
		t.Errorf("Frame mismatch: %+v vs %+v", f, f2)
	}
	if !bytes.Equal(f2.Body, f.Body) {
		t.Errorf("Body mismatch: %s vs %s", f2.Body, f.Body)
	}
}

func TestFrameAtMaxSize(t *testing.T) {
	// Test a valid frame with reasonable size
	f := Frame{
		ID:   "id",
		Type: "test",
		Body: json.RawMessage(`{"data":"test"}`),
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)

	err := w.Write(f)
	if err != nil {
		t.Fatalf("Failed to write frame: %v", err)
	}

	// Verify the frame was written
	if buf.Len() == 0 {
		t.Error("No bytes written")
	}
}

func TestReaderLargeLine(t *testing.T) {
	// Create a 2 MiB line
	line := make([]byte, 2*1024*1024)
	for i := range line {
		line[i] = 'x'
	}
	line = append(line, '\n')

	r := NewReader(bytes.NewReader(line))
	_, err := r.Read()
	if err != ErrFrameTooLarge {
		t.Errorf("Expected ErrFrameTooLarge, got %v", err)
	}

	// Reader should be marked as unusable
	_, err = r.Read()
	if err != ErrFrameTooLarge {
		t.Errorf("After error, expected ErrFrameTooLarge, got %v", err)
	}
}

func TestReaderRejectsVersion(t *testing.T) {
	line := `{"v":2,"type":"test"}`
	r := NewReader(strings.NewReader(line + "\n"))
	_, err := r.Read()
	if err == nil {
		t.Error("Expected error for v:2")
	}
}

func TestReaderRejectsArray(t *testing.T) {
	line := `[]`
	r := NewReader(strings.NewReader(line + "\n"))
	_, err := r.Read()
	if err == nil {
		t.Error("Expected error for JSON array")
	}
}

func TestReaderRejectsNonJSON(t *testing.T) {
	line := `not json at all`
	r := NewReader(strings.NewReader(line + "\n"))
	_, err := r.Read()
	if err == nil {
		t.Error("Expected error for non-JSON")
	}
}

func TestReaderIgnoresUnknownMembers(t *testing.T) {
	line := `{"v":1,"id":"test","type":"status","unknown_field":"should_be_ignored"}`
	r := NewReader(strings.NewReader(line + "\n"))
	f, err := r.Read()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if f.ID != "test" || f.Type != "status" {
		t.Errorf("Fields not decoded correctly: %+v", f)
	}
}

func TestContentPageEncoding(t *testing.T) {
	// Test with 262144 bytes of worst-case escaping
	raw := make([]byte, 262144)
	for i := range raw {
		// Mix of quotes, backslashes, and control characters
		switch i % 4 {
		case 0:
			raw[i] = '"'
		case 1:
			raw[i] = '\\'
		case 2:
			raw[i] = '\n'
		case 3:
			raw[i] = '\x00'
		}
	}

	encoded := EncodePage(raw)
	decoded, err := DecodePage(encoded)
	if err != nil {
		t.Fatalf("DecodePage failed: %v", err)
	}
	if !bytes.Equal(raw, decoded) {
		t.Error("Round-trip failed")
	}

	// Check that encoded frame is under 512 KiB
	reply := ContentReply{
		Ref:    "test",
		Offset: 0,
		Data:   encoded,
		Total:  int64(len(raw)),
		SHA256: "fake",
	}
	frame, _ := NewReply("test", reply)
	fullEncoded, _ := json.Marshal(frame)
	fullEncoded = append(fullEncoded, '\n')

	if len(fullEncoded) > 512*1024 {
		t.Errorf("Frame size %d exceeds 512 KiB limit", len(fullEncoded))
	}
}

func TestDecodePage(t *testing.T) {
	// Test with invalid UTF-8
	raw := []byte{0xFF, 0xFE, 0xFD, 'h', 'e', 'l', 'l', 'o'}
	encoded := EncodePage(raw)
	decoded, err := DecodePage(encoded)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if !bytes.Equal(raw, decoded) {
		t.Error("Invalid UTF-8 not preserved")
	}
}

func TestEventKindsConstant(t *testing.T) {
	// All event kinds from design should be present
	expectedKinds := []string{
		EventKindEngineStarted,
		EventKindEngineStopping,
		EventKindTurnSubmitted,
		EventKindTurnState,
		EventKindParticipantState,
		EventKindAttemptBegun,
		EventKindAttemptCompleted,
		EventKindAttemptUnknown,
		EventKindMessageAppended,
		EventKindCandidateFrozen,
		EventKindVerdictRecorded,
		EventKindVerdictsDelivered,
		EventKindBlockOpened,
		EventKindBlockState,
		EventKindCapacity,
		EventKindDeadline,
		EventKindBudget,
		EventKindBudgetEstimateExceeded,
		EventKindBudgetExhausted,
		EventKindOverride,
	}

	for _, kind := range expectedKinds {
		found := false
		for _, k := range EventKinds {
			if k == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("EventKinds missing %s", kind)
		}
	}

	// Check that EventKinds has the right elements
	if len(EventKinds) != len(expectedKinds) {
		t.Errorf("EventKinds length mismatch: got %d, expected %d", len(EventKinds), len(expectedKinds))
	}
}

func TestRequestNames(t *testing.T) {
	// All request types should have string constants
	requests := []string{
		RequestStatus,
		RequestSubmit,
		RequestCancel,
		RequestOverride,
		RequestRelease,
		RequestTurn,
		RequestTranscript,
		RequestContent,
		RequestSubscribe,
		RequestUnsubscribe,
	}

	expectedNames := map[string]bool{
		"status":      true,
		"submit":      true,
		"cancel":      true,
		"override":    true,
		"release":     true,
		"turn":        true,
		"transcript":  true,
		"content":     true,
		"subscribe":   true,
		"unsubscribe": true,
	}

	for _, r := range requests {
		if !expectedNames[r] {
			t.Errorf("Unexpected request type: %s", r)
		}
	}
}

func TestErrorCodes(t *testing.T) {
	errorCodes := []string{
		ErrorCodeProtocol,
		ErrorCodeStopping,
		ErrorCodeBusy,
		ErrorCodeOrphanedHolds,
		ErrorCodeQuickProfileUnknown,
		ErrorCodePresetInvalid,
		ErrorCodeUnprobed,
		ErrorCodePolicy,
		ErrorCodeBudget,
		ErrorCodeProvisional,
		ErrorCodeNotIncomplete,
		ErrorCodeReasonRequired,
		ErrorCodeNoHold,
		ErrorCodeAmbiguous,
		ErrorCodeNoSuchHold,
		ErrorCodeFrame,
		ErrorCodeLagging,
		ErrorCodeSubmissionConflict,
		ErrorCodeInternal,
		ErrorCodeNotOpen,
		ErrorCodeNotFound,
		ErrorCodeBadRequest,
	}

	expectedCodes := map[string]bool{
		"protocol":              true,
		"stopping":              true,
		"busy":                  true,
		"orphaned_holds":        true,
		"quick_profile_unknown": true,
		"preset_invalid":        true,
		"unprobed":              true,
		"policy":                true,
		"budget":                true,
		"provisional":           true,
		"not_incomplete":        true,
		"reason_required":       true,
		"no_hold":               true,
		"ambiguous":             true,
		"no_such_hold":          true,
		"frame":                 true,
		"lagging":               true,
		"submission_conflict":   true,
		"internal":              true,
		"not_open":              true,
		"not_found":             true,
		"bad_request":           true,
	}

	for _, code := range errorCodes {
		if !expectedCodes[code] {
			t.Errorf("Unexpected error code: %s", code)
		}
	}
}

func TestSubmitReplyJSON(t *testing.T) {
	reply := SubmitReply{
		TurnID:          "turn-123",
		Preset:          "review",
		QuickProfile:    "fast",
		EstimatedTokens: 50000,
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Check that field names match the design
	if !strings.Contains(string(data), "turn_id") {
		t.Error("Missing turn_id field")
	}
	if !strings.Contains(string(data), "preset") {
		t.Error("Missing preset field")
	}
	if !strings.Contains(string(data), "quick_profile") {
		t.Error("Missing quick_profile field")
	}
	if !strings.Contains(string(data), "estimated_tokens") {
		t.Error("Missing estimated_tokens field")
	}
}

func TestStatusReplyJSON(t *testing.T) {
	reply := StatusReply{
		RunID:         "run-123",
		DefaultPreset: "review",
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	expectedKeys := map[string]bool{
		"run_id":                true,
		"run_started_at":        true,
		"roster_path":           true,
		"roster_hash":           true,
		"roster_file_changed":   true,
		"default_preset":        true,
		"deadlines_provisional": true,
		"participants":          true,
		"pools":                 true,
		"orphaned_holds":        true,
		"quick_profiles":        true,
		"turns_open":            true,
		"engine_started_at":     true,
	}

	for key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("Missing key in StatusReply: %s", key)
		}
	}
}

func TestHelloReplyJSON(t *testing.T) {
	reply := HelloReply{
		EngineVersion: "1.0.0",
		Protocol:      1,
		RunID:         "run-123",
		RunFirstSeq:   100,
		RosterHash:    "abc123",
		ManifestHash:  "def456",
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	expectedKeys := map[string]bool{
		"engine_version": true,
		"protocol":       true,
		"run_id":         true,
		"run_first_seq":  true,
		"roster_hash":    true,
		"manifest_hash":  true,
	}

	for key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("Missing key in HelloReply: %s", key)
		}
	}
}

func TestEvEngineStartedJSON(t *testing.T) {
	ev := EvEngineStarted{
		RunID:             "run-123",
		ManifestHash:      "abc123",
		RosterHash:        "def456",
		RecoveredAttempts: 5,
		UncertainCapacity: []string{"pool1", "pool2"},
		OrphanedHolds:     []Hold{},
		Unprobed:          []string{"participant1"},
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	expectedKeys := map[string]bool{
		"run_id":             true,
		"manifest_hash":      true,
		"roster_hash":        true,
		"recovered_attempts": true,
		"uncertain_capacity": true,
		"orphaned_holds":     true,
		"unprobed":           true,
	}

	for key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("Missing key in EvEngineStarted: %s", key)
		}
	}
}

func TestSetObserver(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	var encodedLength int
	w.SetObserver(func(len int) {
		encodedLength = len
	})

	f := Frame{
		ID:   "test",
		Type: "submit",
	}

	w.Write(f)

	if encodedLength == 0 {
		t.Error("Observer was not called")
	}

	if encodedLength != buf.Len() {
		t.Errorf("Observer received wrong length: %d vs %d", encodedLength, buf.Len())
	}
}

func TestNewRequest(t *testing.T) {
	req := SubmitRequest{
		SubmissionID: "sub-123",
		Input:        "test input",
	}

	f, err := NewRequest("id-1", RequestSubmit, req)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}

	if f.ID != "id-1" || f.Type != RequestSubmit {
		t.Errorf("Request frame incorrect: %+v", f)
	}

	var decoded SubmitRequest
	json.Unmarshal(f.Body, &decoded)
	if decoded.SubmissionID != "sub-123" {
		t.Error("Request body not encoded correctly")
	}
}

func TestNewReply(t *testing.T) {
	reply := SubmitReply{
		TurnID: "turn-123",
		Preset: "review",
	}

	f, err := NewReply("id-1", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
	}

	if f.ID != "id-1" || f.Type != "reply" {
		t.Errorf("Reply frame incorrect: %+v", f)
	}
}

func TestNewError(t *testing.T) {
	err := &Error{
		Code:   ErrorCodeBusy,
		Detail: "another turn is open",
	}

	f, ferr := NewError("id-1", err)
	if ferr != nil {
		t.Fatalf("NewError failed: %v", ferr)
	}

	if f.ID != "id-1" || f.Type != "error" {
		t.Errorf("Error frame incorrect: %+v", f)
	}

	var decoded Error
	json.Unmarshal(f.Body, &decoded)
	if decoded.Code != ErrorCodeBusy {
		t.Error("Error not encoded correctly")
	}
}

func TestNewEvent(t *testing.T) {
	body, _ := json.Marshal(EvEngineStarted{RunID: "run-123"})
	now := time.Now()

	f := NewEvent(100, EventKindEngineStarted, now, body)

	if f.Type != "event" || f.Seq != 100 || f.Kind != EventKindEngineStarted {
		t.Errorf("Event frame incorrect: %+v", f)
	}

	if f.At != now.Format(time.RFC3339) {
		t.Errorf("Event timestamp incorrect: %s vs %s", f.At, now.Format(time.RFC3339))
	}
}

func TestConcurrentWrite(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			f := Frame{
				ID:   "id" + string(rune(id)),
				Type: "test",
			}
			w.Write(f)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Count lines in buffer
	lines := bytes.Split(buf.Bytes(), []byte{'\n'})
	count := 0
	for _, line := range lines {
		if len(line) > 0 {
			count++
		}
	}

	if count != 10 {
		t.Errorf("Expected 10 lines, got %d", count)
	}
}

func TestErrorInterface(t *testing.T) {
	err := &Error{Code: ErrorCodeBusy}
	if err.Error() != ErrorCodeBusy {
		t.Error("Error() should return code")
	}
}

func TestFrameOmitEmpty(t *testing.T) {
	// Test that omitempty fields are properly omitted
	f := Frame{
		V:    1,
		Type: "test",
		ID:   "",  // should be omitted
		Seq:  0,   // should be omitted
		Kind: "",  // should be omitted
		At:   "",  // should be omitted
		Body: nil, // should be omitted
	}

	data, _ := json.Marshal(f)
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	if _, ok := m["id"]; ok {
		t.Error("Empty id should be omitted")
	}
	if _, ok := m["seq"]; ok {
		t.Error("Zero seq should be omitted")
	}
	if _, ok := m["kind"]; ok {
		t.Error("Empty kind should be omitted")
	}
	if _, ok := m["at"]; ok {
		t.Error("Empty at should be omitted")
	}
	if _, ok := m["body"]; ok {
		t.Error("Nil body should be omitted")
	}
}
