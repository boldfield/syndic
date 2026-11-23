package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/transport"
)

// Fixed inputs for the golden tests.
const (
	goldenHash          = "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	goldenAttempt       = "attempt-7"
	goldenRound         = 2
	goldenRepairAttempt = "attempt-7-repair-1"
	goldenDelimiter     = "0123456789abcdef0123456789abcdef"
)

// assertGolden compares got with testdata/name byte for byte.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func goldenHistory(t *testing.T) *history.Document {
	return buildHistoryDoc(t, history.Turn{
		ID:     "turn-1",
		State:  "approved",
		Input:  []byte("What is 2+2?"),
		Answer: []byte("4"),
		Label:  "reviewed",
	})
}

func goldenReviewInput(doc *history.Document) ReviewInput {
	return ReviewInput{
		Request:       "Summarise the attached memo.",
		Candidate:     "The memo proposes a two-week delay.",
		CandidateHash: goldenHash,
		AttemptID:     goldenAttempt,
		Delimiter:     goldenDelimiter,
		History:       doc,
	}
}

func goldenReviewerRevisionInput(doc *history.Document) ReviewerRevisionInput {
	return ReviewerRevisionInput{
		HistoryDoc:    doc,
		Request:       "Summarise the attached memo.",
		Candidate:     "The memo proposes a two-week delay to the launch.",
		CandidateHash: goldenHash,
		OpenBlocks:    []OpenBlockForRevision{{BlockID: "b1", Severity: "medium", Evidence: "\"two-week delay\"", Remedy: "Say what is delayed."}},
		AttemptID:     goldenAttempt,
		Delimiter:     goldenDelimiter,
		Round:         goldenRound,
	}
}

// renderMessages serialises messages, roles included, for comparison.
func renderMessages(msgs []transport.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString("--- " + m.Role + " ---\n")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func TestGoldenAuthorSystem(t *testing.T) {
	assertGolden(t, "author_system.txt", Author("x")[0].Content)
	assertGolden(t, "author_system.txt", AuthorWithHistory(goldenHistory(t), "x")[0].Content)
	msgs, err := AuthorRevision(goldenAuthorRevisionInput(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, sys := range []string{Author("x")[0].Content, msgs[0].Content} {
		if strings.Contains(sys, "VERDICT") || strings.Contains(sys, "END VERDICT") {
			t.Errorf("author system message contains a verdict envelope: %q", sys)
		}
	}
}

func TestGoldenReviewerSystem(t *testing.T) {
	msgs, err := Reviewer(goldenReviewInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "reviewer_system.txt", msgs[0].Content)

	msgs, err = Reviewer(goldenReviewInput(goldenHistory(t)))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "reviewer_system_history.txt", msgs[0].Content)
}

func TestGoldenAuthorRevisionSystem(t *testing.T) {
	msgs, err := AuthorRevision(goldenAuthorRevisionInput(t))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "author_revision_system.txt", msgs[0].Content)
}

func TestGoldenReviewerRevisionSystem(t *testing.T) {
	msgs, err := ReviewerRevision(goldenReviewerRevisionInput(goldenHistory(t)))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "reviewer_revision_system.txt", msgs[0].Content)
}

func TestGoldenRepairInstruction(t *testing.T) {
	in := goldenReviewInput(nil)
	msgs, err := Repair(in, "no envelope", goldenRepairAttempt)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "repair_instruction.txt", msgs[3].Content)
	in.AttemptID = goldenRepairAttempt
	want, err := Reviewer(in)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Content != want[0].Content {
		t.Error("repair system message is not the reviewer system message for the repair attempt id")
	}
}

func TestGoldenRepairRevisionInstruction(t *testing.T) {
	in := goldenReviewerRevisionInput(goldenHistory(t))
	msgs, err := RepairRevision(in, "no envelope", goldenRepairAttempt)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "repair_revision_instruction.txt", msgs[3].Content)
	in.AttemptID = goldenRepairAttempt
	want, err := ReviewerRevision(in)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Content != want[0].Content {
		t.Error("repair system message is not the revision reviewer system message for the repair attempt id")
	}
}

func goldenAuthorRevisionInput(t *testing.T) AuthorRevisionInput {
	v1 := "VERDICT v1\ncandidate: " + goldenHash + "\nattempt: " + goldenAttempt +
		"\nverdict: block\nseverity: medium\nblock_id: b1\nevidence: see call_1\nremedy: Say what is delayed.\nreason: vague\nEND VERDICT"
	return AuthorRevisionInput{
		Input:             "Summarise the attached memo.",
		PreviousCandidate: "The memo proposes a two-week delay.",
		ReviewerFeedback: []RevisionFeedback{
			{Output: v1, Results: map[string]string{"call_1": "memo text", "call_2": "uncited"}},
			{Output: "approve"},
		},
		Delimiter:  goldenDelimiter,
		HistoryDoc: goldenHistory(t),
	}
}

// The user-message goldens were rendered from main before the wording change;
// they show that only the system messages and repair instructions changed.
func TestGoldenUserMessagesUnchanged(t *testing.T) {
	msgs, err := Reviewer(goldenReviewInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "reviewer_user.txt", msgs[1].Content)

	msgs, err = Reviewer(goldenReviewInput(goldenHistory(t)))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "reviewer_user_history.txt", msgs[1].Content)

	msgs, err = ReviewerRevision(goldenReviewerRevisionInput(goldenHistory(t)))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "reviewer_revision_user.txt", msgs[1].Content)

	msgs, err = AuthorRevision(goldenAuthorRevisionInput(t))
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "author_revision_messages.txt", renderMessages(msgs[1:]))
}
