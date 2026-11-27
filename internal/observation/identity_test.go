package observation

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/candidate"
)

const (
	hexA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hexB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hexC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// goldenRecords mixes all three kinds with a newline path ("a\nb", 3 bytes) and
// a multibyte path ("файл.txt", 12 bytes in UTF-8).
func goldenRecords() []Record {
	return []Record{
		{Kind: KindFile, Path: "файл.txt", Value: hexA},
		{Kind: KindAbsent, Path: "a\nb"},
		{Kind: KindDir, Path: "dir", Value: hexC},
	}
}

const (
	goldenSnapshotFrame = "a 3\na\nb\n\n" +
		"d 3\ndir\n" + hexC + "\n" +
		"f 12\nфайл.txt\n" + hexA + "\n"
	goldenSnapshotHash = "e58252842eecdc93ac4e96bd16cfe4d1567c970d5b30e5f4662ace4492485ae1"
)

func TestGoldenSnapshot(t *testing.T) {
	frame, err := FrameSnapshotRecords(goldenRecords())
	if err != nil {
		t.Fatal(err)
	}
	if string(frame) != goldenSnapshotFrame {
		t.Errorf("snapshot frame:\nwant %q\n got %q", goldenSnapshotFrame, frame)
	}
	got, err := SnapshotHash(goldenRecords())
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenSnapshotHash {
		t.Errorf("snapshot_hash = %s, want %s", got, goldenSnapshotHash)
	}
}

func TestGoldenEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		frame   string
		hash    string
	}{
		{"newline name", []Entry{{"f", "a\nb"}}, "f 3\na\nb\n",
			"afbda6162f3986a7b1d489b10b6936d08af1a07f1977f5aa5cf4938dfa2889de"},
		{"separate names", []Entry{{"f", "b"}, {"f", "a"}}, "f 1\na\nf 1\nb\n",
			"9db1fb0b0767c711add8e665fa789908247feb96f99409c16bb288da80fed950"},
		{"multibyte names", []Entry{{"f", "日本"}, {"d", "файл.txt"}}, "d 12\nфайл.txt\nf 6\n日本\n",
			"e9c441d3ed0675e8dd6fc0d846b175989b98fe5d2de1c6099a96635fbec6f600"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := FrameEntries(tc.entries)
			if err != nil {
				t.Fatal(err)
			}
			if string(frame) != tc.frame {
				t.Errorf("frame: want %q, got %q", tc.frame, frame)
			}
			got, err := EntriesHash(tc.entries)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.hash {
				t.Errorf("entries_sha256 = %s, want %s", got, tc.hash)
			}
		})
	}
}

func TestNewlineNameDiffersFromSeparateEntries(t *testing.T) {
	joined, err := EntriesHash([]Entry{{"f", "a\nb"}})
	if err != nil {
		t.Fatal(err)
	}
	separate, err := EntriesHash([]Entry{{"f", "a"}, {"f", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if joined == separate {
		t.Fatal("entries_sha256 of one entry a\\nb equals that of entries a and b")
	}

	// The same holds for snapshot records, with equal framed lengths avoided
	// by shaping the path so the unframed bytes would coincide.
	one, err := SnapshotHash([]Record{{Kind: KindAbsent, Path: "a\na 1\nb"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := SnapshotHash([]Record{{Kind: KindAbsent, Path: "a"}, {Kind: KindAbsent, Path: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("snapshot_hash collides for newline path vs separate records")
	}
}

func goldenChanges() []Change {
	return []Change{
		{Path: "файл.txt", Op: OpModify, BaseSHA: hexA, NewSHA: hexB, NewLen: 150},
		{Path: "x\ny", Op: OpCreate, BaseSHA: Absent, NewSHA: hexB, NewLen: 5},
	}
}

const (
	goldenChangeLines = "3 x\ny create absent " + hexB + " 5\n" +
		"12 файл.txt modify " + hexA + " " + hexB + " 150\n"
	goldenChangesHash = "9f489907c4c09af6e110fbbff9916710fa3fb76cc33ad40d3576e20968c6bd03"
)

func TestGoldenChangeSet(t *testing.T) {
	lines, err := FrameChangeSetLines(goldenChanges())
	if err != nil {
		t.Fatal(err)
	}
	if string(lines) != goldenChangeLines {
		t.Errorf("lines: want %q, got %q", goldenChangeLines, lines)
	}
	full, err := FrameChangeSet(goldenChanges(), goldenSnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	if want := goldenChangeLines + "snapshot:" + goldenSnapshotHash + "\n"; string(full) != want {
		t.Errorf("change set: want %q, got %q", want, full)
	}
	got, err := ChangesHash(goldenChanges(), goldenSnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenChangesHash {
		t.Errorf("changes_hash = %s, want %s", got, goldenChangesHash)
	}
}

func TestChangeSetPathCollision(t *testing.T) {
	forged := []Change{{Path: "x create absent " + hexB + " 1\ny", Op: OpCreate, BaseSHA: Absent, NewSHA: hexB, NewLen: 1}}
	pair := []Change{
		{Path: "x", Op: OpCreate, BaseSHA: Absent, NewSHA: hexB, NewLen: 1},
		{Path: "y", Op: OpCreate, BaseSHA: Absent, NewSHA: hexB, NewLen: 1},
	}
	fb, _ := FrameChangeSet(forged, goldenSnapshotHash)
	pb, _ := FrameChangeSet(pair, goldenSnapshotHash)
	if string(fb) == string(pb) {
		t.Fatal("canonical bytes collide")
	}
	fh, _ := ChangesHash(forged, goldenSnapshotHash)
	ph, _ := ChangesHash(pair, goldenSnapshotHash)
	if fh == ph {
		t.Fatal("changes_hash collides")
	}
}

func TestOrderIndependence(t *testing.T) {
	rev := goldenRecords()
	rev[0], rev[2] = rev[2], rev[0]
	if h, _ := SnapshotHash(rev); h != goldenSnapshotHash {
		t.Errorf("snapshot hash depends on input order: %s", h)
	}
	ch := goldenChanges()
	ch[0], ch[1] = ch[1], ch[0]
	if h, _ := ChangesHash(ch, goldenSnapshotHash); h != goldenChangesHash {
		t.Errorf("changes hash depends on input order: %s", h)
	}
}

func TestValidation(t *testing.T) {
	if _, err := FrameSnapshotRecords([]Record{{KindAbsent, "p", ""}, {KindAbsent, "p", ""}}); err == nil {
		t.Error("duplicate snapshot path accepted")
	}
	if _, err := FrameSnapshotRecords([]Record{{KindFile, "p", "nothex"}}); err == nil {
		t.Error("bad file hash accepted")
	}
	if _, err := FrameSnapshotRecords([]Record{{KindAbsent, "p", hexA}}); err == nil {
		t.Error("absence with value accepted")
	}
	if _, err := FrameSnapshotRecords([]Record{{'x', "p", ""}}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := FrameEntries([]Entry{{"f", "a"}, {"d", "a"}}); err == nil {
		t.Error("duplicate entry name accepted")
	}
	if _, err := FrameEntries([]Entry{{"f\nx", "a"}}); err == nil {
		t.Error("newline in entry type accepted")
	}
	if _, err := FrameEntries([]Entry{{"", "a"}}); err == nil {
		t.Error("empty entry type accepted")
	}
	bad := []Change{
		{Path: "p", Op: "delete", BaseSHA: hexA, NewSHA: hexB},
		{Path: "p", Op: OpCreate, BaseSHA: hexA, NewSHA: hexB},
		{Path: "p", Op: OpModify, BaseSHA: Absent, NewSHA: hexB},
		{Path: "p", Op: OpModify, BaseSHA: hexA, NewSHA: "zz"},
		{Path: "p", Op: OpModify, BaseSHA: hexA, NewSHA: hexB, NewLen: -1},
		{Path: "", Op: OpModify, BaseSHA: hexA, NewSHA: hexB},
	}
	for _, c := range bad {
		if _, err := FrameChangeSetLines([]Change{c}); err == nil {
			t.Errorf("invalid change accepted: %+v", c)
		}
	}
	dup := Change{Path: "p", Op: OpModify, BaseSHA: hexA, NewSHA: hexB}
	if _, err := FrameChangeSetLines([]Change{dup, dup}); err == nil {
		t.Error("duplicate change path accepted")
	}
	if _, err := FrameChangeSet(nil, "short"); err == nil {
		t.Error("bad snapshot hash accepted")
	}
}

// frozenIdentity builds the candidate over the golden snapshot with one change.
func frozenIdentity(t *testing.T, records []Record, evidence []candidate.EvidencePair) candidate.Identity {
	t.Helper()
	snap, err := SnapshotHash(records)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := ChangesHash([]Change{{Path: "x", Op: OpCreate, BaseSHA: Absent, NewSHA: hexB, NewLen: 1}}, snap)
	if err != nil {
		t.Fatal(err)
	}
	return candidate.Identity{
		ManifestHash:    "m1",
		TurnID:          "t1",
		AuthorAttemptID: "att1",
		Content:         []byte("answer"),
		Evidence:        evidence,
		ChangesHash:     changes,
	}
}

func authorEvidence() []candidate.EvidencePair {
	return []candidate.EvidencePair{{CallID: "call1", ResultSHA256: hexA}, {CallID: "call2", ResultSHA256: hexB}}
}

const (
	goldenFrozenChangesHash     = "d3dacc2fb800436c14e4423c017ef7f83b80e7a396a2e5115820b277dd730e17"
	goldenFrozenCandidateHashV2 = "3298d7839d16dee5793cbd03371c17f731cbb0bb08097ae62f6a53558310236f"
)

func TestCandidateAuthorEvidenceMutationChangesHash(t *testing.T) {
	base := frozenIdentity(t, goldenRecords(), authorEvidence())
	if base.ChangesHash != goldenFrozenChangesHash {
		t.Fatalf("changes_hash = %s, want %s", base.ChangesHash, goldenFrozenChangesHash)
	}
	baseHashV2, err := candidate.HashV2(base)
	if err != nil {
		t.Fatal(err)
	}
	if baseHashV2 != goldenFrozenCandidateHashV2 {
		t.Fatalf("candidate v2 hash = %s, want %s", baseHashV2, goldenFrozenCandidateHashV2)
	}

	mutated := authorEvidence()
	mutated[1].ResultSHA256 = hexC
	reordered := authorEvidence()
	reordered[0], reordered[1] = reordered[1], reordered[0]
	changedRecords := append(goldenRecords(), Record{Kind: KindAbsent, Path: "extra"})

	for name, id := range map[string]candidate.Identity{
		"result_sha256 changed": frozenIdentity(t, goldenRecords(), mutated),
		"evidence reordered":    frozenIdentity(t, goldenRecords(), reordered),
		"snapshot changed":      frozenIdentity(t, changedRecords, authorEvidence()),
	} {
		h, err := candidate.HashV2(id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if h == baseHashV2 {
			t.Errorf("%s: candidate v2 hash unchanged", name)
		}
	}
}

func TestReviewerOnlyEvidenceDoesNotRewriteFrozenCandidate(t *testing.T) {
	frozen := frozenIdentity(t, goldenRecords(), authorEvidence())
	frozenSnap, _ := SnapshotHash(goldenRecords())
	canonBefore, err := candidate.Canonical(frozen)
	if err != nil {
		t.Fatal(err)
	}

	// A reviewer's post-freeze first touch extends the record to a new version,
	// and the reviewer receives its own tool result.
	extended := append(goldenRecords(), Record{Kind: KindFile, Path: "generated/dep.go", Value: hexB})
	extSnap, err := SnapshotHash(extended)
	if err != nil {
		t.Fatal(err)
	}
	if extSnap == frozenSnap {
		t.Fatal("extended record should have a new snapshot hash")
	}
	reviewerResult := candidate.EvidencePair{CallID: "rev1", ResultSHA256: hexC}

	// The frozen binding is recomputed from the frozen record set and still
	// matches the stored identity; the reviewer result is not author evidence.
	refrozen, _ := ChangesHash([]Change{{Path: "x", Op: OpCreate, BaseSHA: Absent, NewSHA: hexB, NewLen: 1}}, frozenSnap)
	if refrozen != frozen.ChangesHash {
		t.Error("frozen changes_hash no longer derivable from the frozen records")
	}
	for _, p := range frozen.Evidence {
		if p == reviewerResult {
			t.Error("reviewer result entered the author evidence")
		}
	}
	canonAfter, err := candidate.Canonical(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonBefore) != string(canonAfter) || strings.Contains(string(canonAfter), extSnap) {
		t.Error("frozen candidate canonical bytes depend on the extended record")
	}
	hV2, _ := candidate.HashV2(frozen)
	if hV2 != goldenFrozenCandidateHashV2 {
		t.Errorf("frozen candidate v2 hash = %s, want %s", hV2, goldenFrozenCandidateHashV2)
	}
}
