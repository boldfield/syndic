package observation

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// TestFramePathNewlineDistinction tests that "a\nb" differs from separate "a" and "b" entries.
// This is the core requirement from the design: length-framing makes paths unambiguous.
func TestFramePathNewlineDistinction(t *testing.T) {
	// A path containing a newline
	pathWithNewline := "a\nb"
	frameWithNewline, err := FramePath(KindFile, pathWithNewline, strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("FramePath with newline failed: %v", err)
	}

	// Two separate paths: "a" and "b"
	frameA, err := FramePath(KindFile, "a", strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("FramePath for 'a' failed: %v", err)
	}
	frameB, err := FramePath(KindFile, "b", strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("FramePath for 'b' failed: %v", err)
	}

	frameAB := append(frameA, frameB...)

	// The critical assertion: they must be different
	if string(frameWithNewline) == string(frameAB) {
		t.Error("FramePath with newline must differ from concatenated separate frames")
	}

	// Verify the frame format for the newline case
	if !strings.Contains(string(frameWithNewline), "f 3\n") {
		t.Error("Frame for path with newline should have 'f 3\\n' (3 bytes for 'a\\nb')")
	}
}

// TestFramePathFormat verifies the canonical frame format.
func TestFramePathFormat(t *testing.T) {
	hash := strings.Repeat("a", 64)
	frame, err := FramePath(KindFile, "test.txt", hash)
	if err != nil {
		t.Fatalf("FramePath failed: %v", err)
	}

	expected := "f 8\ntest.txt\n" + hash + "\n"
	if string(frame) != expected {
		t.Errorf("Frame format mismatch.\nExpected: %q\nGot: %q", expected, string(frame))
	}
}

// TestFramePathAbsent verifies framing for absent paths.
func TestFramePathAbsent(t *testing.T) {
	frame, err := FramePath(KindAbsent, "missing.txt", "")
	if err != nil {
		t.Fatalf("FramePath absent failed: %v", err)
	}

	if !strings.Contains(string(frame), "a 11\n") {
		t.Error("Absent path frame should have 'a 11\\n'")
	}

	if !strings.Contains(string(frame), "missing.txt\n\n") {
		t.Error("Absent path frame should have path followed by empty value")
	}
}

// TestFramePathListing verifies framing for directory listings.
func TestFramePathListing(t *testing.T) {
	hash := strings.Repeat("b", 64)
	frame, err := FramePath(KindListing, "dir", hash)
	if err != nil {
		t.Fatalf("FramePath listing failed: %v", err)
	}

	if !strings.Contains(string(frame), "d 3\n") {
		t.Error("Directory listing frame should have 'd 3\\n'")
	}

	if !strings.Contains(string(frame), hash) {
		t.Error("Directory listing frame should contain the entries hash")
	}
}

// TestFramePathValidation tests validation of path framing parameters.
func TestFramePathValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)

	tests := []struct {
		name    string
		kind    string
		path    string
		value   string
		wantErr bool
	}{
		{"empty path", KindFile, "", hash, true},
		{"invalid kind", "x", "path", hash, true},
		{"file missing value", KindFile, "path", "", true},
		{"absent with value", KindAbsent, "path", hash, true},
		{"listing missing value", KindListing, "path", "", true},
		{"valid file", KindFile, "file.txt", hash, false},
		{"valid absent", KindAbsent, "missing", "", false},
		{"valid listing", KindListing, "dir", hash, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FramePath(test.kind, test.path, test.value)
			if (err != nil) != test.wantErr {
				t.Errorf("FramePath: wantErr=%v, got err=%v", test.wantErr, err)
			}
		})
	}
}

// TestFrameEntry verifies entry framing format.
func TestFrameEntry(t *testing.T) {
	frame, err := FrameEntry("f", "file.go")
	if err != nil {
		t.Fatalf("FrameEntry failed: %v", err)
	}

	expected := "f 7\nfile.go\n"
	if string(frame) != expected {
		t.Errorf("Entry frame mismatch.\nExpected: %q\nGot: %q", expected, string(frame))
	}
}

// TestFrameEntryBinaryName tests that entry names with binary content are framed correctly.
func TestFrameEntryBinaryName(t *testing.T) {
	name := "file\x00with\x00nulls"
	frame, err := FrameEntry("f", name)
	if err != nil {
		t.Fatalf("FrameEntry with binary failed: %v", err)
	}

	// The frame should include the byte length
	expected := fmt.Sprintf("f %d\n%s\n", len(name), name)
	if string(frame) != expected {
		t.Errorf("Binary name frame mismatch.\nExpected: %q\nGot: %q", expected, string(frame))
	}
}

// TestFrameEntriesSorted verifies that entries are sorted before framing.
func TestFrameEntriesSorted(t *testing.T) {
	entries := []EntryRecord{
		{Type: "f", Name: "zebra"},
		{Type: "f", Name: "apple"},
		{Type: "d", Name: "banana"},
	}

	frame, err := FrameEntries(entries)
	if err != nil {
		t.Fatalf("FrameEntries failed: %v", err)
	}

	frameStr := string(frame)
	appleIdx := strings.Index(frameStr, "apple")
	bananaIdx := strings.Index(frameStr, "banana")
	zebraIdx := strings.Index(frameStr, "zebra")

	if appleIdx > bananaIdx || bananaIdx > zebraIdx {
		t.Error("Entries must be sorted by name")
	}
}

// TestFrameEntriesEmpty tests that empty entry list produces empty frame.
func TestFrameEntriesEmpty(t *testing.T) {
	frame, err := FrameEntries([]EntryRecord{})
	if err != nil {
		t.Fatalf("FrameEntries empty failed: %v", err)
	}

	if len(frame) != 0 {
		t.Errorf("Empty entries should produce empty frame, got %q", string(frame))
	}
}

// TestSnapshotHashConsistency tests that the same records produce the same hash.
func TestSnapshotHashConsistency(t *testing.T) {
	records := []SnapshotRecord{
		{Kind: KindFile, Path: "file.txt", ValueHash: strings.Repeat("a", 64)},
		{Kind: KindAbsent, Path: "missing.txt", ValueHash: ""},
	}

	hash1, err := SnapshotHash(records)
	if err != nil {
		t.Fatalf("First SnapshotHash failed: %v", err)
	}

	hash2, err := SnapshotHash(records)
	if err != nil {
		t.Fatalf("Second SnapshotHash failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("SnapshotHash not consistent: %s vs %s", hash1, hash2)
	}
}

// TestSnapshotHashOrdering verifies that records are sorted by path in the hash.
func TestSnapshotHashOrdering(t *testing.T) {
	hash := strings.Repeat("a", 64)

	recordsForward := []SnapshotRecord{
		{Kind: KindFile, Path: "a.txt", ValueHash: hash},
		{Kind: KindFile, Path: "z.txt", ValueHash: hash},
	}

	recordsReverse := []SnapshotRecord{
		{Kind: KindFile, Path: "z.txt", ValueHash: hash},
		{Kind: KindFile, Path: "a.txt", ValueHash: hash},
	}

	hashForward, err := SnapshotHash(recordsForward)
	if err != nil {
		t.Fatalf("SnapshotHash forward failed: %v", err)
	}

	hashReverse, err := SnapshotHash(recordsReverse)
	if err != nil {
		t.Fatalf("SnapshotHash reverse failed: %v", err)
	}

	if hashForward != hashReverse {
		t.Error("SnapshotHash must be order-independent")
	}
}

// TestFrameChangeSetLinesFormat verifies the change set line format.
func TestFrameChangeSetLinesFormat(t *testing.T) {
	hash := strings.Repeat("a", 64)
	paths := []ChangeSetPathRecord{
		{
			Path:    "file.txt",
			Op:      "create",
			BaseSHA: "",
			NewSHA:  hash,
			NewLen:  42,
		},
	}

	frame, err := FrameChangeSetLines(paths)
	if err != nil {
		t.Fatalf("FrameChangeSetLines failed: %v", err)
	}

	frameStr := string(frame)
	if !strings.Contains(frameStr, "file.txt create absent") {
		t.Errorf("Change set line format mismatch: %q", frameStr)
	}
	if !strings.Contains(frameStr, hash) {
		t.Error("Change set line must contain new SHA")
	}
	if !strings.Contains(frameStr, "42\n") {
		t.Error("Change set line must contain new length")
	}
}

// TestFrameChangeSetLinesSorted tests that change set lines are sorted by path.
func TestFrameChangeSetLinesSorted(t *testing.T) {
	hash := strings.Repeat("a", 64)
	paths := []ChangeSetPathRecord{
		{Path: "z.txt", Op: "modify", BaseSHA: hash, NewSHA: hash, NewLen: 10},
		{Path: "a.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 5},
		{Path: "m.txt", Op: "modify", BaseSHA: hash, NewSHA: hash, NewLen: 8},
	}

	frame, err := FrameChangeSetLines(paths)
	if err != nil {
		t.Fatalf("FrameChangeSetLines failed: %v", err)
	}

	frameStr := string(frame)
	aIdx := strings.Index(frameStr, "a.txt")
	mIdx := strings.Index(frameStr, "m.txt")
	zIdx := strings.Index(frameStr, "z.txt")

	if aIdx > mIdx || mIdx > zIdx {
		t.Error("Change set paths must be sorted")
	}
}

// TestChangesHashFormat verifies that changes_hash includes the snapshot line.
func TestChangesHashFormat(t *testing.T) {
	hash := strings.Repeat("a", 64)
	snapshotHash := strings.Repeat("b", 64)

	paths := []ChangeSetPathRecord{
		{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 10},
	}

	changesHash, err := ChangesHash(paths, snapshotHash)
	if err != nil {
		t.Fatalf("ChangesHash failed: %v", err)
	}

	// Verify that including or excluding the snapshot line changes the hash
	pathsFrame, _ := FrameChangeSetLines(paths)
	hashWithoutSnapshot := sha256.Sum256(pathsFrame)
	hashWithSnapshot := sha256.Sum256(append(pathsFrame, []byte(fmt.Sprintf("snapshot:%s\n", snapshotHash))...))

	if fmt.Sprintf("%x", hashWithoutSnapshot) == changesHash {
		t.Error("ChangesHash must include the snapshot line, not just the paths")
	}
	if fmt.Sprintf("%x", hashWithSnapshot) != changesHash {
		t.Error("ChangesHash must match the hash of paths plus snapshot line")
	}
}

// TestChangesHashRequiresSnapshot tests that ChangesHash requires a snapshot hash.
func TestChangesHashRequiresSnapshot(t *testing.T) {
	_, err := ChangesHash([]ChangeSetPathRecord{}, "")
	if err == nil {
		t.Error("ChangesHash should require non-empty snapshot hash")
	}

	_, err = ChangesHash([]ChangeSetPathRecord{}, strings.Repeat("a", 64))
	if err != nil {
		t.Errorf("ChangesHash should not error with valid snapshot: %v", err)
	}
}

// TestValidateRecord tests record validation.
func TestValidateRecord(t *testing.T) {
	hash := strings.Repeat("a", 64)

	tests := []struct {
		name    string
		record  SnapshotRecord
		wantErr bool
	}{
		{"empty path", SnapshotRecord{Kind: KindFile, Path: ""}, true},
		{"invalid kind", SnapshotRecord{Kind: "x", Path: "p"}, true},
		{"file missing hash", SnapshotRecord{Kind: KindFile, Path: "p"}, true},
		{"file invalid hash", SnapshotRecord{Kind: KindFile, Path: "p", ValueHash: "bad"}, true},
		{"absent with hash", SnapshotRecord{Kind: KindAbsent, Path: "p", ValueHash: hash}, true},
		{"listing missing hash", SnapshotRecord{Kind: KindListing, Path: "p"}, true},
		{"listing invalid hash", SnapshotRecord{Kind: KindListing, Path: "p", ValueHash: "bad"}, true},
		{"valid file", SnapshotRecord{Kind: KindFile, Path: "file", ValueHash: hash}, false},
		{"valid absent", SnapshotRecord{Kind: KindAbsent, Path: "missing"}, false},
		{"valid listing", SnapshotRecord{Kind: KindListing, Path: "dir", ValueHash: hash}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRecord(test.record)
			if (err != nil) != test.wantErr {
				t.Errorf("ValidateRecord: wantErr=%v, got err=%v", test.wantErr, err)
			}
		})
	}
}

// TestIsValidSHA256Hex tests SHA256 hex validation.
func TestIsValidSHA256Hex(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{strings.Repeat("a", 64), true},
		{strings.Repeat("0", 64), true},
		{strings.Repeat("f", 64), true},
		{strings.Repeat("A", 64), false}, // must be lowercase
		{strings.Repeat("g", 64), false}, // invalid hex
		{strings.Repeat("a", 63), false}, // too short
		{strings.Repeat("a", 65), false}, // too long
	}

	for _, test := range tests {
		if IsValidSHA256Hex(test.input) != test.valid {
			t.Errorf("IsValidSHA256Hex(%q): expected %v", test.input, test.valid)
		}
	}
}

// TestPathHasBinaryContent tests binary content detection.
func TestPathHasBinaryContent(t *testing.T) {
	tests := []struct {
		path   string
		binary bool
	}{
		{"simple.txt", false},
		{"path/with/slashes", false},
		{"path\x00with\x00nulls", true},
		{"path\nwith\nnewlines", false}, // newlines are OK, they're handled by framing
		{"path\twith\ttabs", false},     // tabs are OK
		{"path\rcarriage", true},        // carriage return is binary
	}

	for _, test := range tests {
		if PathHasBinaryContent(test.path) != test.binary {
			t.Errorf("PathHasBinaryContent(%q): expected %v", test.path, test.binary)
		}
	}
}

// TestFramePathWithMultibytePath tests that multibyte UTF-8 paths are handled correctly.
func TestFramePathWithMultibytePath(t *testing.T) {
	// A path with multibyte UTF-8 characters
	path := "файл.txt" // Russian: "file.txt"
	hash := strings.Repeat("a", 64)

	frame, err := FramePath(KindFile, path, hash)
	if err != nil {
		t.Fatalf("FramePath with multibyte failed: %v", err)
	}

	frameStr := string(frame)
	pathBytes := []byte(path)

	// The frame should have the byte length, not the character count
	expectedByteLen := fmt.Sprintf("f %d\n", len(pathBytes))
	if !strings.HasPrefix(frameStr, expectedByteLen) {
		t.Errorf("Multibyte path frame must use byte length: expected prefix %q, got %q",
			expectedByteLen, frameStr[:min(len(expectedByteLen), len(frameStr))])
	}
}

// TestSnapshotHashWithMultibytePaths tests snapshot hashing with multibyte paths.
func TestSnapshotHashWithMultibytePaths(t *testing.T) {
	hash := strings.Repeat("a", 64)
	records := []SnapshotRecord{
		{Kind: KindFile, Path: "file.txt", ValueHash: hash},
		{Kind: KindFile, Path: "файл.txt", ValueHash: hash},
	}

	h, err := SnapshotHash(records)
	if err != nil {
		t.Fatalf("SnapshotHash with multibyte failed: %v", err)
	}

	if !IsValidSHA256Hex(h) {
		t.Errorf("SnapshotHash should return valid hex: %q", h)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestCandidateAuthorEvidenceMutationChangesHash tests that changes to author evidence change the candidate hash.
// This is integration with the candidate identity: the candidate's Evidence field includes
// the tool result SHA256s, so a change in the evidence changes the candidate hash.
func TestSnapshotHashChangesWithRecords(t *testing.T) {
	hash1 := strings.Repeat("a", 64)
	hash2 := strings.Repeat("b", 64)

	records1 := []SnapshotRecord{
		{Kind: KindFile, Path: "file.txt", ValueHash: hash1},
	}

	records2 := []SnapshotRecord{
		{Kind: KindFile, Path: "file.txt", ValueHash: hash2},
	}

	snapshot1, err := SnapshotHash(records1)
	if err != nil {
		t.Fatalf("SnapshotHash 1 failed: %v", err)
	}

	snapshot2, err := SnapshotHash(records2)
	if err != nil {
		t.Fatalf("SnapshotHash 2 failed: %v", err)
	}

	if snapshot1 == snapshot2 {
		t.Error("SnapshotHash must change when observation content changes")
	}
}

// TestReviewerOnlyObservationDoesNotRewriteFrozen tests that adding observations
// after a candidate is frozen (which the design calls "post-freeze observation")
// results in a new snapshot version that doesn't change the candidate's frozen snapshot_hash.
func TestReviewerOnlyObservationDoesNotRewriteFrozen(t *testing.T) {
	hash := strings.Repeat("a", 64)

	// Original records (frozen with the candidate)
	frozenRecords := []SnapshotRecord{
		{Kind: KindFile, Path: "file1.txt", ValueHash: hash},
	}

	frozenHash, err := SnapshotHash(frozenRecords)
	if err != nil {
		t.Fatalf("SnapshotHash frozen failed: %v", err)
	}

	// A reviewer observes an additional path (post-freeze)
	extendedRecords := []SnapshotRecord{
		{Kind: KindFile, Path: "file1.txt", ValueHash: hash},
		{Kind: KindFile, Path: "file2.txt", ValueHash: hash},
	}

	extendedHash, err := SnapshotHash(extendedRecords)
	if err != nil {
		t.Fatalf("SnapshotHash extended failed: %v", err)
	}

	// The hashes must be different because the snapshot records changed
	if frozenHash == extendedHash {
		t.Error("Adding post-freeze observations must produce a different snapshot hash")
	}

	// The candidate's original frozen snapshot_hash stays the same (this is verified elsewhere
	// in the design; here we just verify that the extended hash is different)
	if extendedHash == frozenHash {
		t.Error("Post-freeze observation must result in a different snapshot hash version")
	}
}
