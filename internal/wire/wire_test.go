package wire

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ptrString(s string) *string {
	return &s
}

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

func TestFrameRoundTripWithLargeBody(t *testing.T) {
	// Test round-trip with a large non-trivial body
	// Create a large encoded binary content (like a content page)
	largeData := make([]byte, 100000)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}
	encodedData := EncodePage(largeData)

	reply := ContentReply{
		Ref:    "test-ref",
		Offset: 0,
		Data:   encodedData,
		Total:  int64(len(largeData)),
		SHA256: "abc123",
	}

	f := Frame{
		V:    1,
		ID:   "large-test",
		Type: "content",
	}
	var err error
	f.Body, err = json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal reply: %v", err)
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

	if f2.V != 1 || f2.ID != "large-test" || f2.Type != "content" {
		t.Errorf("Frame mismatch: %+v vs %+v", f, f2)
	}
	if !bytes.Equal(f2.Body, f.Body) {
		t.Errorf("Body mismatch in length: %d vs %d", len(f2.Body), len(f.Body))
	}
}

func TestFrameAtMaxSize(t *testing.T) {
	// Test that a frame exactly at MaxFrameBytes is written successfully
	// Frame structure: {"v":1,"id":"...","type":"...","body":"..."}
	// We need to construct a body such that the entire frame is exactly MaxFrameBytes long

	exactFrame, _ := buildExactFrame(t)

	// Write the exact-sized frame - should succeed
	var buf1 bytes.Buffer
	w1 := NewWriter(&buf1)
	if err := w1.Write(exactFrame); err != nil {
		t.Fatalf("Failed to write frame at exactly MaxFrameBytes: %v", err)
	}
	if buf1.Len() != MaxFrameBytes {
		t.Errorf("Exact frame size mismatch: got %d bytes, expected %d", buf1.Len(), MaxFrameBytes)
	}

	// Now test one byte over - should fail and write nothing
	overFrame := exactFrame
	overFrame.Body = json.RawMessage(`"` + strings.Repeat("a", len(exactFrame.Body)-1) + `"`)

	var buf2 bytes.Buffer
	w2 := NewWriter(&buf2)
	if err := w2.Write(overFrame); err != ErrFrameTooLarge {
		t.Errorf("Expected ErrFrameTooLarge for frame over MaxFrameBytes, got %v", err)
	}
	if buf2.Len() != 0 {
		t.Error("Bytes should not be written for oversized frame")
	}

	// Test frame significantly over MaxFrameBytes - should fail
	oversizePadding := make([]byte, MaxFrameBytes)
	for i := range oversizePadding {
		oversizePadding[i] = 'b'
	}

	f3 := Frame{
		ID:   "id",
		Type: "content",
		Body: json.RawMessage(fmt.Sprintf(`{"data":"%s"}`, string(oversizePadding))),
	}
	var buf3 bytes.Buffer
	w3 := NewWriter(&buf3)

	if err := w3.Write(f3); err != ErrFrameTooLarge {
		t.Errorf("Expected ErrFrameTooLarge for massively oversized frame, got %v", err)
	}
	if buf3.Len() != 0 {
		t.Error("Bytes should not be written for massively oversized frame")
	}
}

type boundedCountingReader struct {
	r     io.Reader
	count int
	limit int
}

func (bcr *boundedCountingReader) Read(p []byte) (int, error) {
	if bcr.count >= bcr.limit {
		return 0, io.EOF
	}
	maxRead := bcr.limit - bcr.count
	if len(p) > maxRead {
		p = p[:maxRead]
	}
	n, err := bcr.r.Read(p)
	bcr.count += n
	return n, err
}

func TestReaderExactMaxSize(t *testing.T) {
	// Create a valid JSON frame that is exactly MaxFrameBytes long (including newline)
	// This should be read successfully

	_, encoded := buildExactFrame(t)

	r := NewReader(bytes.NewReader(encoded))
	f, err := r.Read()
	if err != nil {
		t.Fatalf("Failed to read frame at exactly MaxFrameBytes: %v", err)
	}
	if f.Type != "content" {
		t.Errorf("Frame type mismatch: got %s, expected content", f.Type)
	}
}

func TestReaderLargeLine(t *testing.T) {
	// Create a 2 MiB line and track consumption
	twoMiB := 2 * 1024 * 1024
	line := make([]byte, twoMiB)
	for i := range line {
		line[i] = 'x'
	}
	line = append(line, '\n')

	// Wrapped reader that tracks bytes read and limits them to ~1.1 MiB
	cr := &boundedCountingReader{
		r:     bytes.NewReader(line),
		limit: MaxFrameBytes + 1024*100,
	}

	r := NewReader(cr)
	_, err := r.Read()
	if err != ErrFrameTooLarge {
		t.Errorf("Expected ErrFrameTooLarge, got %v", err)
	}

	if cr.count > MaxFrameBytes+1024*100 {
		t.Errorf("Reader consumed too much: %d bytes (should be <= ~1.1 MiB)", cr.count)
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
	frame, err := NewReply("test", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
	}
	fullEncoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	fullEncoded = append(fullEncoded, '\n')

	if len(fullEncoded) > 512*1024 {
		t.Errorf("Frame size %d exceeds 512 KiB limit", len(fullEncoded))
	}

	// Test encoding through Writer
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Write(frame); err != nil {
		t.Fatalf("Writer.Write failed: %v", err)
	}
	if buf.Len() > 512*1024 {
		t.Errorf("Written frame size %d exceeds 512 KiB limit", buf.Len())
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

func TestDecodePageRandomBytes(t *testing.T) {
	// Test with random bytes including invalid UTF-8
	raw := make([]byte, 10000)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("Failed to generate random bytes: %v", err)
	}

	encoded := EncodePage(raw)
	decoded, err := DecodePage(encoded)
	if err != nil {
		t.Fatalf("Failed to decode random bytes: %v", err)
	}
	if !bytes.Equal(raw, decoded) {
		t.Error("Random bytes round-trip failed")
	}
}

func TestEventKindsConstant(t *testing.T) {
	// All event kinds from design should be present - literal design strings
	expectedKinds := []string{
		"run_opened",
		"probe_started",
		"probe_finished",
		"engine_started",
		"engine_stopping",
		"turn_submitted",
		"turn_state",
		"participant_state",
		"attempt_begun",
		"attempt_completed",
		"attempt_unknown",
		"message_appended",
		"candidate_frozen",
		"verdict_recorded",
		"verdicts_delivered",
		"block_opened",
		"block_state",
		"capacity",
		"deadline",
		"budget",
		"budget_estimate_exceeded",
		"budget_exhausted",
		"override",
		"conversation_created",
	}

	// Check exact set match
	if len(EventKinds) != len(expectedKinds) {
		t.Errorf("EventKinds length mismatch: got %d, expected %d", len(EventKinds), len(expectedKinds))
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
			t.Errorf("EventKinds missing design string: %s", kind)
		}
	}

	for _, k := range EventKinds {
		found := false
		for _, kind := range expectedKinds {
			if k == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("EventKinds contains unexpected value: %s", k)
		}
	}
}

func TestRequestNames(t *testing.T) {
	// All request types should have string constants
	requests := []string{
		RequestHello,
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
		RequestConversations,
		RequestConversation,
	}

	expectedNames := map[string]bool{
		"hello":         true,
		"status":        true,
		"submit":        true,
		"cancel":        true,
		"override":      true,
		"release":       true,
		"turn":          true,
		"transcript":    true,
		"content":       true,
		"subscribe":     true,
		"unsubscribe":   true,
		"conversations": true,
		"conversation":  true,
	}

	for _, r := range requests {
		if !expectedNames[r] {
			t.Errorf("Unexpected request type: %s", r)
		}
	}

	// Check that all expected names are present
	if len(requests) != len(expectedNames) {
		t.Errorf("Request names length mismatch: got %d, expected %d", len(requests), len(expectedNames))
	}
}

func TestErrorCodes(t *testing.T) {
	// All error codes from design - literal design strings
	expectedCodes := []string{
		"protocol",
		"stopping",
		"busy",
		"orphaned_holds",
		"quick_profile_unknown",
		"preset_invalid",
		"unprobed",
		"policy",
		"budget",
		"provisional",
		"not_incomplete",
		"reason_required",
		"no_hold",
		"ambiguous",
		"no_such_hold",
		"frame",
		"lagging",
		"submission_conflict",
		"internal",
		"not_open",
		"not_found",
		"bad_request",
		"probing",
		"workspace_mismatch",
		"max_rounds",
		"egress",
	}

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
		ErrorCodeProbing,
		ErrorCodeWorkspaceMismatch,
		ErrorCodeMaxRounds,
		ErrorCodeEgress,
	}

	// Check lengths match
	if len(errorCodes) != len(expectedCodes) {
		t.Errorf("Error codes count mismatch: got %d, expected %d", len(errorCodes), len(expectedCodes))
	}

	// Check all expected codes are present
	for _, code := range expectedCodes {
		found := false
		for _, c := range errorCodes {
			if c == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Error code missing from constants: %s", code)
		}
	}

	// Check no unexpected codes are present
	for _, c := range errorCodes {
		found := false
		for _, code := range expectedCodes {
			if c == code {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Unexpected error code constant: %s", c)
		}
	}
}

func TestSubmitReplyJSON(t *testing.T) {
	qp := "fast"
	reply := SubmitReply{
		TurnID:          "turn-123",
		Preset:          "review",
		QuickProfile:    &qp,
		EstimatedTokens: 50000,
		ConversationID:  "conv-456",
		ContextHash:     "ctx-abc123",
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"turn_id":          true,
		"preset":           true,
		"quick_profile":    true,
		"estimated_tokens": true,
		"conversation_id":  true,
		"context_hash":     true,
	}

	// Check exact key set
	if len(m) != len(expectedKeys) {
		t.Errorf("SubmitReply has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in SubmitReply: %s", key)
		}
	}
}

func TestSubmitRequestWithoutPreset(t *testing.T) {
	// Test that SubmitRequest without preset marshals without the preset field
	// but includes the required workspace field
	req := SubmitRequest{
		SubmissionID: "sub-123",
		Input:        "test input",
		Workspace:    "/home/user",
		// Preset is nil
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, ok := m["preset"]; ok {
		t.Error("Nil preset should be omitted from JSON")
	}

	// Also verify the present fields
	expectedKeys := map[string]bool{
		"submission_id": true,
		"input":         true,
		"workspace":     true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("SubmitRequest has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in SubmitRequest: %s", key)
		}
	}
}

func TestStatusReplyJSON(t *testing.T) {
	reply := StatusReply{
		RunID:         "run-123",
		DefaultPreset: "review",
		Revision:      Revision{Default: 2, Ceiling: 5},
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"run_id":                true,
		"run_started_at":        true,
		"roster_path":           true,
		"roster_hash":           true,
		"roster_file_changed":   true,
		"default_preset":        true,
		"presets":               true,
		"deadlines_provisional": true,
		"participants":          true,
		"pools":                 true,
		"orphaned_holds":        true,
		"quick_profiles":        true,
		"turns_open":            true,
		"engine_started_at":     true,
		"revision":              true,
	}

	// Check exact key set
	if len(m) != len(expectedKeys) {
		t.Errorf("StatusReply has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in StatusReply: %s", key)
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
		ManifestHash:  ptrString("def456"),
		State:         "started",
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"engine_version": true,
		"protocol":       true,
		"run_id":         true,
		"run_first_seq":  true,
		"roster_hash":    true,
		"manifest_hash":  true,
		"state":          true,
	}

	// Check exact key set
	if len(m) != len(expectedKeys) {
		t.Errorf("HelloReply has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in HelloReply: %s", key)
		}
	}
}

func TestHelloReplyNullManifestHashRoundTrip(t *testing.T) {
	// Test typed roundtrip of HelloReply with null manifest_hash (probing state)
	reply := HelloReply{
		EngineVersion: "1.0.0",
		Protocol:      1,
		RunID:         "run-123",
		RunFirstSeq:   100,
		RosterHash:    "abc123",
		ManifestHash:  nil, // null during probing
		State:         "probing",
	}

	f, err := NewReply("hello-id", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
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

	var reply2 HelloReply
	if err := json.Unmarshal(f2.Body, &reply2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if reply2.ManifestHash != nil {
		t.Errorf("ManifestHash should be null, got %v", reply2.ManifestHash)
	}
	if reply2.State != "probing" {
		t.Errorf("State mismatch: expected probing, got %s", reply2.State)
	}
	if reply2.RunFirstSeq != 100 {
		t.Errorf("RunFirstSeq mismatch: expected 100, got %d", reply2.RunFirstSeq)
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
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"run_id":             true,
		"manifest_hash":      true,
		"roster_hash":        true,
		"recovered_attempts": true,
		"uncertain_capacity": true,
		"orphaned_holds":     true,
		"unprobed":           true,
	}

	// Check exact key set
	if len(m) != len(expectedKeys) {
		t.Errorf("EvEngineStarted has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in EvEngineStarted: %s", key)
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

	if err := w.Write(f); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

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
	if err := json.Unmarshal(f.Body, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}
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

	// Verify body unmarshals correctly
	var decoded SubmitReply
	if err := json.Unmarshal(f.Body, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal reply body: %v", err)
	}
	if decoded.TurnID != "turn-123" || decoded.Preset != "review" {
		t.Error("Reply body not decoded correctly")
	}
}

func TestNewError(t *testing.T) {
	detail := "another turn is open"
	err := &Error{
		Code:   ErrorCodeBusy,
		Detail: &detail,
	}

	f, ferr := NewError("id-1", err)
	if ferr != nil {
		t.Fatalf("NewError failed: %v", ferr)
	}

	if f.ID != "id-1" || f.Type != "error" {
		t.Errorf("Error frame incorrect: %+v", f)
	}

	var decoded Error
	if err := json.Unmarshal(f.Body, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal error body: %v", err)
	}
	if decoded.Code != ErrorCodeBusy {
		t.Error("Error not encoded correctly")
	}
}

func TestNewEvent(t *testing.T) {
	body, err := json.Marshal(EvEngineStarted{RunID: "run-123"})
	if err != nil {
		t.Fatal(err)
	}
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
				ID:   "id-" + strconv.Itoa(id),
				Type: "test",
			}
			if err := w.Write(f); err != nil {
				t.Errorf("Write failed: %v", err)
			}
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

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

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

func TestSubmitReplyWithConversation(t *testing.T) {
	qp := "fast"
	reply := SubmitReply{
		TurnID:          "turn-123",
		Preset:          "review",
		QuickProfile:    &qp,
		EstimatedTokens: 50000,
		ConversationID:  "conv-456",
		ContextHash:     "ctx-abc123",
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"turn_id":          true,
		"preset":           true,
		"quick_profile":    true,
		"estimated_tokens": true,
		"conversation_id":  true,
		"context_hash":     true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("SubmitReply has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in SubmitReply: %s", key)
		}
	}
}

func TestSubmitRequestWithConversation(t *testing.T) {
	req := SubmitRequest{
		SubmissionID:    "sub-123",
		Input:           "test input",
		Workspace:       "/home/user",
		ConversationID:  ptrString("conv-456"),
		ContextFromTurn: ptrString("turn-789"),
		MaxRounds:       ptrInt(2),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"submission_id":     true,
		"input":             true,
		"workspace":         true,
		"conversation_id":   true,
		"context_from_turn": true,
		"max_rounds":        true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("SubmitRequest has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in SubmitRequest: %s", key)
		}
	}
}

func TestConversationsReplyJSON(t *testing.T) {
	summary := ConversationSummary{
		ID:              "conv-123",
		CreatedAt:       "2026-11-21T10:00:00Z",
		Workspace:       ptrString("/home/user"),
		LastSubmittedAt: "2026-11-21T11:00:00Z",
		TurnCount:       3,
		Running:         false,
		Preview:         "What is 2+2?",
	}

	reply := ConversationsReply{
		Items:      []ConversationSummary{summary},
		NextCursor: ptrString("cursor-next"),
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"items":       true,
		"next_cursor": true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("ConversationsReply has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in ConversationsReply: %s", key)
		}
	}
}

func TestConversationReplyJSON(t *testing.T) {
	reply := ConversationReply{
		ID: "conv-123",
		Turns: []ConversationTurnSummary{
			{ID: "turn-1", State: "approved"},
			{ID: "turn-2", State: "incomplete"},
		},
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"id":    true,
		"turns": true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("ConversationReply has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in ConversationReply: %s", key)
		}
	}
}

func TestEvConversationCreatedJSON(t *testing.T) {
	ev := EvConversationCreated{
		ConversationID: "conv-123",
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"conversation_id": true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("EvConversationCreated has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in EvConversationCreated: %s", key)
		}
	}
}

func TestEvTurnSubmittedWithConversationJSON(t *testing.T) {
	qp := "fast"
	ev := EvTurnSubmitted{
		TurnID:          "turn-123",
		SubmissionID:    "sub-456",
		Preset:          "review",
		QuickProfile:    &qp,
		EstimatedTokens: 40000,
		ConversationID:  "conv-789",
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	expectedKeys := map[string]bool{
		"turn_id":          true,
		"submission_id":    true,
		"preset":           true,
		"quick_profile":    true,
		"estimated_tokens": true,
		"conversation_id":  true,
	}

	if len(m) != len(expectedKeys) {
		t.Errorf("EvTurnSubmitted has %d keys, expected %d. Keys: %v", len(m), len(expectedKeys), m)
	}
	for key := range m {
		if !expectedKeys[key] {
			t.Errorf("Unexpected key in EvTurnSubmitted: %s", key)
		}
	}
}

func TestStatusReplyWithRevisionJSON(t *testing.T) {
	revision := Revision{Default: 2, Ceiling: 5}
	reply := StatusReply{
		RunID:                "run-123",
		DefaultPreset:        "review",
		DeadlinesProvisional: false,
		Participants:         []Participant{},
		Pools:                []Pool{},
		OrphanedHolds:        []Hold{},
		QuickProfiles:        []QuickProfile{},
		TurnsOpen:            []string{},
		Revision:             revision,
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, ok := m["revision"]; !ok {
		t.Error("Revision field should be present when set")
	}

	var revisionData map[string]interface{}
	revisionBytes, err := json.Marshal(m["revision"])
	if err != nil {
		t.Fatalf("Failed to marshal revision: %v", err)
	}
	if err := json.Unmarshal(revisionBytes, &revisionData); err != nil {
		t.Fatalf("Failed to unmarshal revision: %v", err)
	}

	expectedRevisionKeys := map[string]bool{
		"default": true,
		"ceiling": true,
	}

	if len(revisionData) != len(expectedRevisionKeys) {
		t.Errorf("Revision has %d keys, expected %d. Keys: %v", len(revisionData), len(expectedRevisionKeys), revisionData)
	}
	for key := range revisionData {
		if !expectedRevisionKeys[key] {
			t.Errorf("Unexpected key in Revision: %s", key)
		}
	}
}

func ptrInt(i int) *int {
	return &i
}

func TestSubmitReplyRoundtrip(t *testing.T) {
	// Test typed roundtrip of SubmitReply
	original := SubmitReply{
		TurnID:          "turn-123",
		Preset:          "review",
		QuickProfile:    ptrString("fast"),
		EstimatedTokens: 50000,
		ConversationID:  "conv-456",
		ContextHash:     "ctx-abc123",
	}

	// Marshal to JSON
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Unmarshal back to struct
	var roundtripped SubmitReply
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify field equality (not pointer equality)
	if roundtripped.TurnID != original.TurnID ||
		roundtripped.Preset != original.Preset ||
		(roundtripped.QuickProfile == nil || *roundtripped.QuickProfile != *original.QuickProfile) ||
		roundtripped.EstimatedTokens != original.EstimatedTokens ||
		roundtripped.ConversationID != original.ConversationID ||
		roundtripped.ContextHash != original.ContextHash {
		t.Errorf("Roundtrip failed: %+v != %+v", original, roundtripped)
	}
}

func TestSubmitRequestRoundtrip(t *testing.T) {
	// Test typed roundtrip of SubmitRequest with all fields
	original := SubmitRequest{
		SubmissionID:     "sub-123",
		Input:            "test input",
		Preset:           ptrString("review"),
		QuickProfile:     ptrString("fast"),
		AllowProvisional: ptrBool(true),
		Workspace:        "/home/user",
		ConversationID:   ptrString("conv-456"),
		ContextFromTurn:  ptrString("turn-789"),
		MaxRounds:        ptrInt(2),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var roundtripped SubmitRequest
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify field equality (not pointer equality)
	if roundtripped.SubmissionID != original.SubmissionID ||
		roundtripped.Input != original.Input ||
		(roundtripped.Preset == nil || *roundtripped.Preset != *original.Preset) ||
		(roundtripped.QuickProfile == nil || *roundtripped.QuickProfile != *original.QuickProfile) ||
		(roundtripped.AllowProvisional == nil || *roundtripped.AllowProvisional != *original.AllowProvisional) ||
		roundtripped.Workspace != original.Workspace ||
		(roundtripped.ConversationID == nil || *roundtripped.ConversationID != *original.ConversationID) ||
		(roundtripped.ContextFromTurn == nil || *roundtripped.ContextFromTurn != *original.ContextFromTurn) ||
		(roundtripped.MaxRounds == nil || *roundtripped.MaxRounds != *original.MaxRounds) {
		t.Errorf("Roundtrip failed: %+v != %+v", original, roundtripped)
	}
}

func TestConversationSummaryWithNullWorkspace(t *testing.T) {
	// Test that ConversationSummary can be created with null workspace
	// (pre-migration conversations)
	summary := ConversationSummary{
		ID:              "conv-123",
		CreatedAt:       "2026-11-21T10:00:00Z",
		Workspace:       nil, // null workspace for pre-migration conversations
		LastSubmittedAt: "2026-11-21T11:00:00Z",
		TurnCount:       1,
		Running:         false,
		Preview:         "first turn",
	}

	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal to map: %v", err)
	}

	// Verify workspace is null in JSON
	if wsVal, ok := m["workspace"]; !ok {
		t.Error("workspace key should be present (even if null)")
	} else if wsVal != nil {
		t.Errorf("workspace should be null, got %v", wsVal)
	}

	// Now roundtrip to struct
	var roundtripped ConversationSummary
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("Failed to unmarshal to struct: %v", err)
	}

	if roundtripped != summary {
		t.Errorf("Roundtrip failed: %+v != %+v", summary, roundtripped)
	}
}

func TestConversationsRequestRoundtrip(t *testing.T) {
	// Test typed roundtrip of ConversationsRequest with optional fields absent
	original := ConversationsRequest{
		Workspace: ptrString("/home/user"),
		Cursor:    ptrString("cursor-abc"),
		Limit:     ptrInt(50),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var roundtripped ConversationsRequest
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify field equality (not pointer equality)
	if (roundtripped.Workspace == nil || *roundtripped.Workspace != *original.Workspace) ||
		(roundtripped.Cursor == nil || *roundtripped.Cursor != *original.Cursor) ||
		(roundtripped.Limit == nil || *roundtripped.Limit != *original.Limit) {
		t.Errorf("Roundtrip failed: %+v != %+v", original, roundtripped)
	}
}

func TestConversationsRequestRoundtripMinimal(t *testing.T) {
	// Test roundtrip with no optional fields
	original := ConversationsRequest{}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var roundtripped ConversationsRequest
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if roundtripped != original {
		t.Errorf("Roundtrip failed: %+v != %+v", original, roundtripped)
	}
}

func TestConversationRequestRoundtrip(t *testing.T) {
	original := ConversationRequest{
		ID: "conv-123",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var roundtripped ConversationRequest
	if err := json.Unmarshal(data, &roundtripped); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if roundtripped != original {
		t.Errorf("Roundtrip failed: %+v != %+v", original, roundtripped)
	}
}

func TestErrorWithCeilingAndRoute(t *testing.T) {
	// Test Error with new Ceiling field (for max_rounds error)
	ceiling := 5
	err := Error{
		Code:    ErrorCodeMaxRounds,
		Ceiling: &ceiling,
	}

	data, json_err := json.Marshal(err)
	if json_err != nil {
		t.Fatalf("Failed to marshal: %v", json_err)
	}

	var roundtripped Error
	if json_err := json.Unmarshal(data, &roundtripped); json_err != nil {
		t.Fatalf("Failed to unmarshal: %v", json_err)
	}

	if roundtripped.Code != err.Code || (roundtripped.Ceiling == nil || *roundtripped.Ceiling != ceiling) {
		t.Errorf("Roundtrip failed: %+v != %+v", err, roundtripped)
	}
}

func TestErrorWithRoute(t *testing.T) {
	// Test Error with new Route field (for egress error)
	route := "history"
	participant := "reviewer-1"
	err := Error{
		Code:        ErrorCodeEgress,
		Route:       &route,
		Participant: &participant,
	}

	data, json_err := json.Marshal(err)
	if json_err != nil {
		t.Fatalf("Failed to marshal: %v", json_err)
	}

	var roundtripped Error
	if json_err := json.Unmarshal(data, &roundtripped); json_err != nil {
		t.Fatalf("Failed to unmarshal: %v", json_err)
	}

	if roundtripped.Code != err.Code || (roundtripped.Route == nil || *roundtripped.Route != route) {
		t.Errorf("Roundtrip failed: %+v != %+v", err, roundtripped)
	}
}

func TestConversationsReplyRoundTrip(t *testing.T) {
	// Test typed roundtrip of ConversationsReply with null workspace and present cursor
	conversations := []ConversationSummary{
		{
			ID:              "conv-1",
			CreatedAt:       "2026-11-21T10:00:00Z",
			Workspace:       ptrString("/home/user1"),
			LastSubmittedAt: "2026-11-21T11:00:00Z",
			TurnCount:       3,
			Running:         false,
			Preview:         "First conversation",
		},
		{
			ID:              "conv-2",
			CreatedAt:       "2026-11-21T09:00:00Z",
			Workspace:       nil, // null workspace for pre-migration conversations
			LastSubmittedAt: "2026-11-21T10:00:00Z",
			TurnCount:       1,
			Running:         false,
			Preview:         "Second conversation",
		},
	}

	reply := ConversationsReply{
		Items:      conversations,
		NextCursor: ptrString("cursor-123"),
	}

	f, err := NewReply("test-id", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
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

	var reply2 ConversationsReply
	if err := json.Unmarshal(f2.Body, &reply2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(reply2.Items) != 2 || reply2.Items[1].Workspace != nil {
		t.Errorf("Null workspace not preserved in roundtrip")
	}
	if reply2.NextCursor == nil || *reply2.NextCursor != "cursor-123" {
		t.Errorf("NextCursor not preserved: %v", reply2.NextCursor)
	}
}

func TestConversationsReplyWithoutCursor(t *testing.T) {
	// Test ConversationsReply with no next cursor (last page)
	reply := ConversationsReply{
		Items: []ConversationSummary{
			{
				ID:              "conv-1",
				CreatedAt:       "2026-11-21T10:00:00Z",
				Workspace:       ptrString("/home/user"),
				LastSubmittedAt: "2026-11-21T11:00:00Z",
				TurnCount:       1,
				Running:         false,
				Preview:         "Only conversation",
			},
		},
		NextCursor: nil, // absent when there are no more pages
	}

	f, err := NewReply("test-id", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
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

	var reply2 ConversationsReply
	if err := json.Unmarshal(f2.Body, &reply2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if reply2.NextCursor != nil {
		t.Errorf("NextCursor should be omitted, got %v", reply2.NextCursor)
	}
}

func TestFrameWithConversationsReplyExceedsMaxSize(t *testing.T) {
	// Test that a conversations reply exceeding MaxFrameBytes is refused with ErrFrameTooLarge
	// Build a reply large enough to exceed the frame cap
	conversations := make([]ConversationSummary, 0)

	// Each summary with a long workspace path grows the reply size
	longWorkspace := strings.Repeat("/very/long/workspace/path/", 50) // ~1500 chars per item
	for i := 0; i < 1000; i++ {
		ws := longWorkspace + fmt.Sprintf("-%d", i)
		conversations = append(conversations, ConversationSummary{
			ID:              fmt.Sprintf("conv-%d", i),
			CreatedAt:       "2026-11-21T10:00:00Z",
			Workspace:       &ws,
			LastSubmittedAt: "2026-11-21T11:00:00Z",
			TurnCount:       999,
			Running:         false,
			Preview:         strings.Repeat("x", 500), // long preview
		})
	}

	reply := ConversationsReply{
		Items:      conversations,
		NextCursor: ptrString("cursor-" + strings.Repeat("x", 1000)),
	}

	f, err := NewReply("test-id", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	err = w.Write(f)
	if err != ErrFrameTooLarge {
		t.Errorf("Expected ErrFrameTooLarge, got %v", err)
	}

	// Verify that Reader also detects oversized frames
	oversizeData := make([]byte, MaxFrameBytes+100)
	for i := range oversizeData {
		oversizeData[i] = 'x'
	}
	oversizeData[len(oversizeData)-1] = '\n'

	r := NewReader(bytes.NewReader(oversizeData))
	_, err = r.Read()
	if err != ErrFrameTooLarge {
		t.Errorf("Reader should refuse oversized frame, got %v", err)
	}
}

func TestConversationReplyRoundTrip(t *testing.T) {
	// Test typed roundtrip of ConversationReply
	reply := ConversationReply{
		ID: "conv-123",
		Turns: []ConversationTurnSummary{
			{ID: "turn-1", State: "approved"},
			{ID: "turn-2", State: "incomplete"},
			{ID: "turn-3", State: "running"},
		},
	}

	f, err := NewReply("test-id", reply)
	if err != nil {
		t.Fatalf("NewReply failed: %v", err)
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

	var reply2 ConversationReply
	if err := json.Unmarshal(f2.Body, &reply2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if reply2.ID != "conv-123" || len(reply2.Turns) != 3 {
		t.Errorf("ConversationReply roundtrip failed: %+v", reply2)
	}
	if reply2.Turns[1].State != "incomplete" {
		t.Errorf("Turn state not preserved: %s", reply2.Turns[1].State)
	}
}

func TestMaxConversationListSizeConstant(t *testing.T) {
	// Verify that MaxConversationListSize is defined and is 200
	if MaxConversationListSize != 200 {
		t.Errorf("MaxConversationListSize should be 200, got %d", MaxConversationListSize)
	}
}

func TestConversationListLimit(t *testing.T) {
	i := func(n int) *int { return &n }
	cases := []struct {
		in   *int
		want int
	}{{nil, 200}, {i(0), 200}, {i(-5), 200}, {i(1), 1}, {i(200), 200}, {i(201), 200}, {i(5000), 200}}
	for _, c := range cases {
		if got := ConversationListLimit(c.in); got != c.want {
			t.Errorf("ConversationListLimit(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func ptrBool(b bool) *bool {
	return &b
}

// buildExactFrame returns a frame whose encoded line, including the newline,
// is exactly MaxFrameBytes long, together with that encoded line.
func buildExactFrame(t *testing.T) (Frame, []byte) {
	t.Helper()
	f := Frame{V: 1, ID: "x", Type: "content", Body: json.RawMessage(`""`)}
	base, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	pad := MaxFrameBytes - (len(base) + 1)
	f.Body = json.RawMessage(`"` + strings.Repeat("a", pad) + `"`)
	enc, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	enc = append(enc, '\n')
	if len(enc) != MaxFrameBytes {
		t.Fatalf("encoded length %d, want %d", len(enc), MaxFrameBytes)
	}
	return f, enc
}
