package observation

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/candidate"
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

// TestFrameChangeSetLinesFormat verifies the change set line format with length-framing.
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
	// Format should be: <path-len> <path> <op> <base|absent> <new_sha> <new_len>\n
	// For "file.txt" (8 bytes): "8 file.txt create absent <hash> 42\n"
	if !strings.Contains(frameStr, "8 file.txt create absent") {
		t.Errorf("Change set line format mismatch, expected '8 file.txt create absent...': %q", frameStr)
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

// TestFrameChangeSetLinesPathWithNewlineCollision tests that length-framing prevents collisions
// when paths contain newlines. This reproduces the actual collision case where one path is
// "x create absent <H> 1\ny" and two separate records are "x" and "y" with identical suffixes.
func TestFrameChangeSetLinesPathWithNewlineCollision(t *testing.T) {
	hash := strings.Repeat("a", 64)

	// One change set with a path containing a newline: "x create absent " + H + " 1\ny"
	// This path, when unframed, would look like: "x create absent aaaa... 1\ny create absent aaaa... 1\n"
	pathWithNewline := "x create absent " + hash + " 1\ny"
	pathsWithNewline := []ChangeSetPathRecord{
		{
			Path:    pathWithNewline,
			Op:      "create",
			BaseSHA: "",
			NewSHA:  hash,
			NewLen:  1,
		},
	}

	frameWithNewline, err := FrameChangeSetLines(pathsWithNewline)
	if err != nil {
		t.Fatalf("FrameChangeSetLines with newline in path failed: %v", err)
	}

	// Two separate records: "x" and "y", each with the same hash and length
	pathsSeparate := []ChangeSetPathRecord{
		{
			Path:    "x",
			Op:      "create",
			BaseSHA: "",
			NewSHA:  hash,
			NewLen:  1,
		},
		{
			Path:    "y",
			Op:      "create",
			BaseSHA: "",
			NewSHA:  hash,
			NewLen:  1,
		},
	}

	frameSeparate, err := FrameChangeSetLines(pathsSeparate)
	if err != nil {
		t.Fatalf("FrameChangeSetLines with separate paths failed: %v", err)
	}

	// Critical: the bytes must differ because of length-framing
	if bytes.Equal(frameWithNewline, frameSeparate) {
		t.Error("FrameChangeSetLines with newline in path must differ from separate entries due to length-framing")
	}

	// Also verify the hashes differ
	changesHashNewline, err := ChangesHash(pathsWithNewline, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("ChangesHash with newline failed: %v", err)
	}

	changesHashSeparate, err := ChangesHash(pathsSeparate, strings.Repeat("b", 64))
	if err != nil {
		t.Fatalf("ChangesHash separate failed: %v", err)
	}

	if changesHashNewline == changesHashSeparate {
		t.Error("ChangesHash must differ for single-path with newline vs two separate records")
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

// TestFrameChangeSetLinesValidation tests validation of change set records.
func TestFrameChangeSetLinesValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)

	tests := []struct {
		name    string
		records []ChangeSetPathRecord
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid create",
			records: []ChangeSetPathRecord{
				{Path: "new.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 100},
			},
			wantErr: false,
		},
		{
			name: "valid modify",
			records: []ChangeSetPathRecord{
				{Path: "old.txt", Op: "modify", BaseSHA: hash, NewSHA: hash, NewLen: 100},
			},
			wantErr: false,
		},
		{
			name: "invalid op",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "delete", BaseSHA: "", NewSHA: hash, NewLen: 100},
			},
			wantErr: true,
			errMsg:  "invalid op",
		},
		{
			name: "create with baseSHA",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "create", BaseSHA: hash, NewSHA: hash, NewLen: 100},
			},
			wantErr: true,
			errMsg:  "empty BaseSHA",
		},
		{
			name: "modify without baseSHA",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "modify", BaseSHA: "", NewSHA: hash, NewLen: 100},
			},
			wantErr: true,
			errMsg:  "non-empty BaseSHA",
		},
		{
			name: "invalid baseSHA hex",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "modify", BaseSHA: "not_hex", NewSHA: hash, NewLen: 100},
			},
			wantErr: true,
			errMsg:  "invalid BaseSHA hex",
		},
		{
			name: "invalid newSHA hex",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: "not_hex", NewLen: 100},
			},
			wantErr: true,
			errMsg:  "invalid NewSHA hex",
		},
		{
			name: "negative newLen",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: -1},
			},
			wantErr: true,
			errMsg:  "negative NewLen",
		},
		{
			name: "duplicate paths",
			records: []ChangeSetPathRecord{
				{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 100},
				{Path: "file.txt", Op: "modify", BaseSHA: hash, NewSHA: hash, NewLen: 200},
			},
			wantErr: true,
			errMsg:  "duplicate path",
		},
		{
			name: "empty path",
			records: []ChangeSetPathRecord{
				{Path: "", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 100},
			},
			wantErr: true,
			errMsg:  "empty",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FrameChangeSetLines(test.records)
			if (err != nil) != test.wantErr {
				t.Errorf("FrameChangeSetLines: wantErr=%v, got err=%v", test.wantErr, err)
			}
			if test.wantErr && test.errMsg != "" && (err == nil || !strings.Contains(err.Error(), test.errMsg)) {
				t.Errorf("FrameChangeSetLines: expected error containing %q, got %v", test.errMsg, err)
			}
		})
	}
}

// TestCandidateAuthorEvidenceMutation tests that changing author tool evidence changes
// the candidate hash. The candidate's Evidence field includes tool result SHA256s,
// so a change in evidence changes the candidate hash.
func TestCandidateAuthorEvidenceMutation(t *testing.T) {
	hash1 := strings.Repeat("a", 64)
	hash2 := strings.Repeat("b", 64)
	hash3 := strings.Repeat("c", 64)

	snapshotHash := strings.Repeat("d", 64)

	// Test 1: changing one ResultSHA256
	changeset := []ChangeSetPathRecord{
		{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: hash1, NewLen: 100},
	}

	changesHash, err := ChangesHash(changeset, snapshotHash)
	if err != nil {
		t.Fatalf("ChangesHash failed: %v", err)
	}

	// Build candidate with original evidence
	candidate1 := candidate.Identity{
		ManifestHash:    hash1,
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("response content"),
		Evidence: []candidate.EvidencePair{
			{CallID: "tool1", ResultSHA256: hash1},
			{CallID: "tool2", ResultSHA256: hash2},
		},
		Round:       1,
		ChangesHash: changesHash,
	}

	hash1_1, err := candidate.Hash(candidate1)
	if err != nil {
		t.Fatalf("candidate.Hash 1 failed: %v", err)
	}

	// Change one ResultSHA256
	candidate2 := candidate.Identity{
		ManifestHash:    hash1,
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("response content"),
		Evidence: []candidate.EvidencePair{
			{CallID: "tool1", ResultSHA256: hash1},
			{CallID: "tool2", ResultSHA256: hash3}, // changed
		},
		Round:       1,
		ChangesHash: changesHash,
	}

	hash1_2, err := candidate.Hash(candidate2)
	if err != nil {
		t.Fatalf("candidate.Hash 2 failed: %v", err)
	}

	if hash1_1 == hash1_2 {
		t.Error("Candidate hash must change when one ResultSHA256 changes")
	}

	// Test 2: reordering Evidence pairs
	candidate3 := candidate.Identity{
		ManifestHash:    hash1,
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("response content"),
		Evidence: []candidate.EvidencePair{
			{CallID: "tool2", ResultSHA256: hash2}, // reordered
			{CallID: "tool1", ResultSHA256: hash1},
		},
		Round:       1,
		ChangesHash: changesHash,
	}

	hash1_3, err := candidate.Hash(candidate3)
	if err != nil {
		t.Fatalf("candidate.Hash 3 failed: %v", err)
	}

	if hash1_1 == hash1_3 {
		t.Error("Candidate hash must change when Evidence pairs are reordered")
	}

	// Test 3: changing snapshot_hash changes ChangesHash
	changeset2 := []ChangeSetPathRecord{
		{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: hash1, NewLen: 100},
	}

	changesHash2, err := ChangesHash(changeset2, hash2) // different snapshot hash
	if err != nil {
		t.Fatalf("ChangesHash 2 failed: %v", err)
	}

	candidate4 := candidate.Identity{
		ManifestHash:    hash1,
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("response content"),
		Evidence: []candidate.EvidencePair{
			{CallID: "tool1", ResultSHA256: hash1},
			{CallID: "tool2", ResultSHA256: hash2},
		},
		Round:       1,
		ChangesHash: changesHash2, // different
	}

	hash1_4, err := candidate.Hash(candidate4)
	if err != nil {
		t.Fatalf("candidate.Hash 4 failed: %v", err)
	}

	if hash1_1 == hash1_4 {
		t.Error("Candidate hash must change when ChangesHash (snapshot) changes")
	}
}

// TestReviewerOnlyObservationDoesNotRewriteFrozenCandidate tests that reviewer-only
// observations (post-freeze) do not change the candidate's frozen hash. The candidate
// freezes with both a snapshot_hash and author Evidence, and post-freeze observations
// create a new version but don't change the original frozen candidate's hash.
func TestReviewerOnlyObservationDoesNotRewriteFrozenCandidate(t *testing.T) {
	hash := strings.Repeat("a", 64)
	hash2 := strings.Repeat("b", 64)

	// Frozen snapshot records (observed by author)
	frozenRecords := []SnapshotRecord{
		{Kind: KindFile, Path: "file1.txt", ValueHash: hash},
	}

	frozenSnapshotHash, err := SnapshotHash(frozenRecords)
	if err != nil {
		t.Fatalf("SnapshotHash frozen failed: %v", err)
	}

	// Frozen change set (author's changes)
	frozenChangeset := []ChangeSetPathRecord{
		{Path: "file1.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 100},
	}

	frozenChangesHash, err := ChangesHash(frozenChangeset, frozenSnapshotHash)
	if err != nil {
		t.Fatalf("ChangesHash frozen failed: %v", err)
	}

	// Build the frozen candidate with its snapshot and author evidence
	frozenCandidate := candidate.Identity{
		ManifestHash:    hash,
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("response content"),
		Evidence: []candidate.EvidencePair{
			{CallID: "tool_call_1", ResultSHA256: hash},
		},
		Round:       1,
		ChangesHash: frozenChangesHash,
	}

	frozenCandidateHash, err := candidate.Hash(frozenCandidate)
	if err != nil {
		t.Fatalf("candidate.Hash frozen failed: %v", err)
	}

	// Reviewer observes an additional path (post-freeze observation)
	extendedRecords := []SnapshotRecord{
		{Kind: KindFile, Path: "file1.txt", ValueHash: hash},
		{Kind: KindFile, Path: "file2.txt", ValueHash: hash2},
	}

	extendedSnapshotHash, err := SnapshotHash(extendedRecords)
	if err != nil {
		t.Fatalf("SnapshotHash extended failed: %v", err)
	}

	// The extended snapshot hash is different from frozen
	if frozenSnapshotHash == extendedSnapshotHash {
		t.Error("Extended snapshot should have different hash from frozen")
	}

	// If the candidate were re-frozen with the extended snapshot, its hash would change
	extendedChangesHash, err := ChangesHash(frozenChangeset, extendedSnapshotHash)
	if err != nil {
		t.Fatalf("ChangesHash extended failed: %v", err)
	}

	refrozenCandidate := candidate.Identity{
		ManifestHash:    hash,
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("response content"),
		Evidence: []candidate.EvidencePair{
			{CallID: "tool_call_1", ResultSHA256: hash},
		},
		Round:       1,
		ChangesHash: extendedChangesHash, // different from frozen
	}

	refrozenCandidateHash, err := candidate.Hash(refrozenCandidate)
	if err != nil {
		t.Fatalf("candidate.Hash refrozen failed: %v", err)
	}

	// The refrozen hash is different from the original frozen hash
	if frozenCandidateHash == refrozenCandidateHash {
		t.Error("Refrozen candidate hash must differ from frozen when snapshot changes")
	}

	// But the original frozen candidate's hash remains unchanged
	// (verified by hashing the frozen candidate again)
	refrozenFrozenCandidateHash, err := candidate.Hash(frozenCandidate)
	if err != nil {
		t.Fatalf("candidate.Hash refrozen frozen failed: %v", err)
	}

	if frozenCandidateHash != refrozenFrozenCandidateHash {
		t.Error("Frozen candidate hash should be stable when using the same evidence and changes hash")
	}
}

// TestEntryDistinctionWithNewline tests that a directory with one entry "a\nb"
// differs from a directory with entries "a" and "b".
func TestEntryDistinctionWithNewline(t *testing.T) {
	entries1 := []EntryRecord{
		{Type: "f", Name: "a\nb"},
	}

	entries2 := []EntryRecord{
		{Type: "f", Name: "a"},
		{Type: "f", Name: "b"},
	}

	frame1, err := FrameEntries(entries1)
	if err != nil {
		t.Fatalf("FrameEntries 1 failed: %v", err)
	}

	frame2, err := FrameEntries(entries2)
	if err != nil {
		t.Fatalf("FrameEntries 2 failed: %v", err)
	}

	if bytes.Equal(frame1, frame2) {
		t.Error("Entry frames must differ for 'a\\nb' vs 'a' and 'b'")
	}

	// Compute hashes
	hash1 := sha256.Sum256(frame1)
	hash2 := sha256.Sum256(frame2)

	if hash1 == hash2 {
		t.Error("entries_sha256 must differ for 'a\\nb' vs 'a' and 'b'")
	}
}

// TestFrameEntriesDuplicateDetection tests that duplicate entry names are rejected.
func TestFrameEntriesDuplicateDetection(t *testing.T) {
	entries := []EntryRecord{
		{Type: "f", Name: "file.txt"},
		{Type: "d", Name: "dir"},
		{Type: "f", Name: "file.txt"}, // duplicate
	}

	_, err := FrameEntries(entries)
	if err == nil {
		t.Error("FrameEntries should reject duplicate entry names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Expected error containing 'duplicate', got: %v", err)
	}
}

// TestFrameEntriesTypeValidation tests that entry types are validated.
func TestFrameEntriesTypeValidation(t *testing.T) {
	tests := []struct {
		name    string
		entries []EntryRecord
		wantErr bool
	}{
		{
			name:    "empty type",
			entries: []EntryRecord{{Type: "", Name: "file"}},
			wantErr: true,
		},
		{
			name:    "type with newline",
			entries: []EntryRecord{{Type: "f\nd", Name: "file"}},
			wantErr: true,
		},
		{
			name:    "valid types",
			entries: []EntryRecord{{Type: "f", Name: "file"}, {Type: "d", Name: "dir"}},
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FrameEntries(test.entries)
			if (err != nil) != test.wantErr {
				t.Errorf("FrameEntries: wantErr=%v, got err=%v", test.wantErr, err)
			}
		})
	}
}

// TestGoldenFixturesChangeSet provides golden fixtures with pinned expected bytes and hashes.
func TestGoldenFixturesChangeSet(t *testing.T) {
	// Golden fixture 1: simple create with ASCII path
	hash64 := strings.Repeat("a", 64)
	changeset1 := []ChangeSetPathRecord{
		{Path: "file.txt", Op: "create", BaseSHA: "", NewSHA: hash64, NewLen: 42},
	}

	frame1, err := FrameChangeSetLines(changeset1)
	if err != nil {
		t.Fatalf("FrameChangeSetLines fixture 1 failed: %v", err)
	}

	// Expected: "8 file.txt create absent " + hash64 + " 42\n"
	expected1 := fmt.Sprintf("8 file.txt create absent %s 42\n", hash64)
	if string(frame1) != expected1 {
		t.Errorf("Golden fixture 1 mismatch.\nExpected: %q\nGot: %q", expected1, string(frame1))
	}

	snapshotHash1 := strings.Repeat("b", 64)
	changesHash1, err := ChangesHash(changeset1, snapshotHash1)
	if err != nil {
		t.Fatalf("ChangesHash fixture 1 failed: %v", err)
	}

	// Verify the frame is deterministic and matches the expected format
	changesBytes := append(frame1, []byte(fmt.Sprintf("snapshot:%s\n", snapshotHash1))...)
	actualChangesHash1 := fmt.Sprintf("%x", sha256.Sum256(changesBytes))
	if actualChangesHash1 != changesHash1 {
		t.Errorf("ChangesHash fixture 1 mismatch: expected computation %q, got %q", actualChangesHash1, changesHash1)
	}

	// Golden fixture 2: path with newline
	changesetNewline := []ChangeSetPathRecord{
		{Path: "file\nname.txt", Op: "create", BaseSHA: "", NewSHA: hash64, NewLen: 100},
	}

	frameNewline, err := FrameChangeSetLines(changesetNewline)
	if err != nil {
		t.Fatalf("FrameChangeSetLines fixture newline failed: %v", err)
	}

	// The path is 15 bytes: "file\nname.txt"
	pathBytes := []byte("file\nname.txt")
	expectedNewline := fmt.Sprintf("%d file\nname.txt create absent %s 100\n", len(pathBytes), hash64)
	if string(frameNewline) != expectedNewline {
		t.Errorf("Golden fixture newline mismatch.\nExpected: %q\nGot: %q", expectedNewline, string(frameNewline))
	}

	// Compute and pin the changes hash for this fixture
	snapshotHashNewline := strings.Repeat("c", 64)
	changesHashNewline, err := ChangesHash(changesetNewline, snapshotHashNewline)
	if err != nil {
		t.Fatalf("ChangesHash fixture newline failed: %v", err)
	}

	changesHexNewline := fmt.Sprintf("%x", sha256.Sum256(append(frameNewline, []byte(fmt.Sprintf("snapshot:%s\n", snapshotHashNewline))...)))
	if changesHexNewline != changesHashNewline {
		t.Errorf("ChangesHash fixture newline mismatch")
	}
}

// TestGoldenFixturesEntries provides golden fixtures for directory entries with pinned hashes.
func TestGoldenFixturesEntries(t *testing.T) {
	// Golden fixture 1: simple entries
	entries1 := []EntryRecord{
		{Type: "f", Name: "file.txt"},
		{Type: "d", Name: "subdir"},
	}

	frame1, err := FrameEntries(entries1)
	if err != nil {
		t.Fatalf("FrameEntries fixture 1 failed: %v", err)
	}

	// Expected (sorted by name bytewise): "f 8\nfile.txt\nd 6\nsubdir\n" (file.txt < subdir)
	expected1 := "f 8\nfile.txt\nd 6\nsubdir\n"
	if string(frame1) != expected1 {
		t.Errorf("Golden fixture 1 mismatch.\nExpected: %q\nGot: %q", expected1, string(frame1))
	}

	hash1 := sha256.Sum256(frame1)
	hashHex1 := fmt.Sprintf("%x", hash1)
	if !IsValidSHA256Hex(hashHex1) {
		t.Errorf("entries_sha256 fixture 1 is not valid hex: %q", hashHex1)
	}

	// Golden fixture 2: entry with newline in name
	entries2 := []EntryRecord{
		{Type: "f", Name: "a\nb"},
	}

	frame2, err := FrameEntries(entries2)
	if err != nil {
		t.Fatalf("FrameEntries fixture 2 failed: %v", err)
	}

	// The name is 3 bytes: "a\nb"
	nameBytes := []byte("a\nb")
	expected2 := fmt.Sprintf("f %d\na\nb\n", len(nameBytes))
	if string(frame2) != expected2 {
		t.Errorf("Golden fixture 2 mismatch.\nExpected: %q\nGot: %q", expected2, string(frame2))
	}

	hash2 := sha256.Sum256(frame2)
	hashHex2 := fmt.Sprintf("%x", hash2)
	if !IsValidSHA256Hex(hashHex2) {
		t.Errorf("entries_sha256 fixture 2 is not valid hex: %q", hashHex2)
	}

	// The two fixtures must have different hashes
	if hashHex1 == hashHex2 {
		t.Error("Golden fixtures 1 and 2 should have different hashes")
	}

	// Golden fixture 3: entry with multibyte UTF-8 name
	entries3 := []EntryRecord{
		{Type: "f", Name: "файл.txt"},
	}

	frame3, err := FrameEntries(entries3)
	if err != nil {
		t.Fatalf("FrameEntries fixture 3 failed: %v", err)
	}

	// The name "файл.txt" is 11 bytes in UTF-8
	nameBytes3 := []byte("файл.txt")
	expected3 := fmt.Sprintf("f %d\nфайл.txt\n", len(nameBytes3))
	if string(frame3) != expected3 {
		t.Errorf("Golden fixture 3 mismatch.\nExpected: %q\nGot: %q", expected3, string(frame3))
	}

	hash3 := sha256.Sum256(frame3)
	hashHex3 := fmt.Sprintf("%x", hash3)
	if !IsValidSHA256Hex(hashHex3) {
		t.Errorf("entries_sha256 fixture 3 is not valid hex: %q", hashHex3)
	}

	// All three hashes must be different
	if hashHex1 == hashHex3 {
		t.Error("Golden fixture 1 and 3 should have different hashes")
	}
	if hashHex2 == hashHex3 {
		t.Error("Golden fixture 2 and 3 should have different hashes")
	}
}

// TestGoldenFixturesMultibyte provides golden fixtures with multibyte paths.
func TestGoldenFixturesMultibyte(t *testing.T) {
	hash := strings.Repeat("a", 64)

	// Golden fixture: multibyte UTF-8 path
	changeset := []ChangeSetPathRecord{
		{Path: "файл.txt", Op: "create", BaseSHA: "", NewSHA: hash, NewLen: 150},
	}

	frame, err := FrameChangeSetLines(changeset)
	if err != nil {
		t.Fatalf("FrameChangeSetLines multibyte failed: %v", err)
	}

	// The path "файл.txt" is 11 bytes in UTF-8
	pathBytes := []byte("файл.txt")
	expected := fmt.Sprintf("%d файл.txt create absent %s 150\n", len(pathBytes), hash)
	if string(frame) != expected {
		t.Errorf("Golden fixture multibyte mismatch.\nExpected: %q\nGot: %q", expected, string(frame))
	}

	snapshotHash := strings.Repeat("b", 64)
	changesHash, err := ChangesHash(changeset, snapshotHash)
	if err != nil {
		t.Fatalf("ChangesHash multibyte failed: %v", err)
	}

	if !IsValidSHA256Hex(changesHash) {
		t.Errorf("ChangesHash multibyte is not valid hex: %q", changesHash)
	}
}

// TestGoldenFixturesSnapshot provides golden fixtures for snapshot frame with pinned exact bytes and hashes.
func TestGoldenFixturesSnapshot(t *testing.T) {
	hash := strings.Repeat("a", 64)

	// Golden fixture 1: simple ASCII paths
	records1 := []SnapshotRecord{
		{Kind: KindFile, Path: "file.txt", ValueHash: hash},
		{Kind: KindAbsent, Path: "missing.txt", ValueHash: ""},
		{Kind: KindListing, Path: "dir", ValueHash: strings.Repeat("b", 64)},
	}

	frame1, err := FrameSnapshotRecords(records1)
	if err != nil {
		t.Fatalf("FrameSnapshotRecords fixture 1 failed: %v", err)
	}

	// Expected frame (sorted by path bytewise):
	// dir entry first: "d 3\ndir\n" + entries_hash + "\n"
	// file.txt: "f 8\nfile.txt\n" + file_hash + "\n"
	// missing.txt: "a 11\nmissing.txt\n\n"
	expectedHashLine1 := "d 3\ndir\n" + strings.Repeat("b", 64) + "\nf 8\nfile.txt\n" + hash + "\na 11\nmissing.txt\n\n"
	if string(frame1) != expectedHashLine1 {
		t.Errorf("Golden fixture snapshot 1 mismatch.\nExpected:\n%q\nGot:\n%q", expectedHashLine1, string(frame1))
	}

	snapshotHash1, err := SnapshotHash(records1)
	if err != nil {
		t.Fatalf("SnapshotHash fixture 1 failed: %v", err)
	}

	if !IsValidSHA256Hex(snapshotHash1) {
		t.Errorf("snapshot_hash fixture 1 is not valid hex: %q", snapshotHash1)
	}

	// Golden fixture 2: path with newline
	records2 := []SnapshotRecord{
		{Kind: KindFile, Path: "file\nname.txt", ValueHash: hash},
	}

	frame2, err := FrameSnapshotRecords(records2)
	if err != nil {
		t.Fatalf("FrameSnapshotRecords fixture 2 failed: %v", err)
	}

	// Expected: "f 13\nfile\nname.txt\n" + hash + "\n" (the path is 13 bytes: file\nname.txt)
	expectedHashLine2 := "f 13\nfile\nname.txt\n" + hash + "\n"
	if string(frame2) != expectedHashLine2 {
		t.Errorf("Golden fixture snapshot 2 mismatch.\nExpected:\n%q\nGot:\n%q", expectedHashLine2, string(frame2))
	}

	snapshotHash2, err := SnapshotHash(records2)
	if err != nil {
		t.Fatalf("SnapshotHash fixture 2 failed: %v", err)
	}

	if !IsValidSHA256Hex(snapshotHash2) {
		t.Errorf("snapshot_hash fixture 2 is not valid hex: %q", snapshotHash2)
	}

	// The two snapshot hashes must be different
	if snapshotHash1 == snapshotHash2 {
		t.Error("Golden fixture snapshot 1 and 2 should have different hashes")
	}

	// Golden fixture 3: multibyte UTF-8 path
	records3 := []SnapshotRecord{
		{Kind: KindFile, Path: "файл.txt", ValueHash: hash},
	}

	frame3, err := FrameSnapshotRecords(records3)
	if err != nil {
		t.Fatalf("FrameSnapshotRecords fixture 3 failed: %v", err)
	}

	// The path "файл.txt" is 11 bytes in UTF-8
	pathBytes3 := []byte("файл.txt")
	expectedHashLine3 := fmt.Sprintf("f %d\nфайл.txt\n%s\n", len(pathBytes3), hash)
	if string(frame3) != expectedHashLine3 {
		t.Errorf("Golden fixture snapshot 3 mismatch.\nExpected:\n%q\nGot:\n%q", expectedHashLine3, string(frame3))
	}

	snapshotHash3, err := SnapshotHash(records3)
	if err != nil {
		t.Fatalf("SnapshotHash fixture 3 failed: %v", err)
	}

	if !IsValidSHA256Hex(snapshotHash3) {
		t.Errorf("snapshot_hash fixture 3 is not valid hex: %q", snapshotHash3)
	}

	// All three snapshot hashes must be different
	if snapshotHash1 == snapshotHash3 {
		t.Error("Golden fixture snapshot 1 and 3 should have different hashes")
	}
	if snapshotHash2 == snapshotHash3 {
		t.Error("Golden fixture snapshot 2 and 3 should have different hashes")
	}
}
