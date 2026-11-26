package prompt

import (
	"errors"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/history"
)

func TestReviewerPacketFenced(t *testing.T) {
	in := ReviewInput{Request: "req", Candidate: "cand", CandidateHash: "h", AttemptID: "a", Delimiter: "D1",
		Diffs: "--- a/x\n+++ b/x\n", AuthorEvidence: "call 1: read (id c)\n"}
	msgs, err := Reviewer(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "===== D1 END CANDIDATE =====\n\n===== D1 BEGIN CHANGE SET =====\n--- a/x\n+++ b/x\n===== D1 END CHANGE SET =====\n\n" +
		"===== D1 BEGIN AUTHOR EVIDENCE =====\ncall 1: read (id c)\n===== D1 END AUTHOR EVIDENCE ====="
	if !strings.HasSuffix(msgs[1].Content, want) {
		t.Fatalf("user message:\n%q", msgs[1].Content)
	}
	if !strings.Contains(msgs[0].Content, reviewerPacketNote) {
		t.Fatal("system message lacks the packet note")
	}
	for _, bad := range []ReviewInput{{Diffs: "x D1 y"}, {AuthorEvidence: "D1"}} {
		in.Diffs, in.AuthorEvidence = bad.Diffs, bad.AuthorEvidence
		if _, err := Reviewer(in); !errors.Is(err, ErrDelimiterCollision) {
			t.Fatalf("err = %v", err)
		}
	}
	empty := ReviewInput{Request: "req", Candidate: "cand", CandidateHash: "h", AttemptID: "a", Delimiter: "D1"}
	msgs, err = Reviewer(empty)
	if err != nil || strings.Contains(msgs[1].Content, "CHANGE SET") || strings.Contains(msgs[0].Content, reviewerPacketNote) {
		t.Fatalf("empty packet added a block: %v", err)
	}
}

func TestReviewerRevisionPacketFenced(t *testing.T) {
	in := ReviewerRevisionInput{Request: "req", Candidate: "cand", CandidateHash: "h", AttemptID: "a", Delimiter: "D1",
		Round: 1, HistoryDoc: history.New(), Diffs: "d\n", AuthorEvidence: "e\n"}
	msgs, err := ReviewerRevision(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"===== D1 BEGIN CHANGE SET =====\nd\n===== D1 END CHANGE SET =====", "===== D1 BEGIN AUTHOR EVIDENCE =====\ne\n===== D1 END AUTHOR EVIDENCE ====="} {
		if !strings.Contains(msgs[1].Content, want) {
			t.Fatalf("missing %q in %q", want, msgs[1].Content)
		}
	}
	in.Diffs = "D1"
	if _, err := ReviewerRevision(in); !errors.Is(err, ErrDelimiterCollision) {
		t.Fatalf("err = %v", err)
	}
	rep, err := RepairRevision(ReviewerRevisionInput{Request: "req", Candidate: "cand", CandidateHash: "h", AttemptID: "a", Delimiter: "D1",
		Round: 1, HistoryDoc: history.New(), Diffs: "d\n"}, "out", "r")
	if err != nil || !strings.Contains(rep[0].Content, reviewerPacketNote) {
		t.Fatalf("repair: %v", err)
	}
}
