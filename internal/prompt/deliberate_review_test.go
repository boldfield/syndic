package prompt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

const (
	testDisposition    = "adopted 1; rejected 2 because wrong"
	testEarlierDisp    = "EARLIER-DISPOSITION-TEXT"
	testPreviousAnswer = "previous answer"
)

func checkCarries(t *testing.T, content string, own int) {
	t.Helper()
	for i, s := range append(append([]string{}, testProposals...), testCritiques...) {
		if !strings.Contains(content, "\n"+s+"\n=====") {
			t.Errorf("text %d not verbatim", i)
		}
	}
	if !strings.Contains(content, " BEGIN DISPOSITION =====\n"+testDisposition+"\n=====") {
		t.Error("disposition missing or mislabelled")
	}
	for i := range testProposals {
		for _, kind := range []string{"PROPOSAL", "CRITIQUE"} {
			own1 := fmt.Sprintf("BEGIN %s %d =====", kind, i+1)
			ownL := "BEGIN YOUR " + kind + " ====="
			if i == own {
				if !strings.Contains(content, ownL) || strings.Contains(content, own1) {
					t.Errorf("own %s mislabelled", kind)
				}
			} else if !strings.Contains(content, own1) {
				t.Errorf("%s %d label missing", kind, i+1)
			}
		}
	}
}

func TestDeliberateReviewCarriesEverything(t *testing.T) {
	for _, own := range []int{-1, 0, 1} {
		in := goldenReviewInput(goldenHistory(t))
		msgs, err := DeliberateReview(in, testProposals, testCritiques, testDisposition, own, &seqRand{})
		if err != nil {
			t.Fatal(err)
		}
		base, _ := Reviewer(in)
		if !sameMsg(msgs[0], base[0]) || !strings.HasPrefix(msgs[1].Content, base[1].Content) {
			t.Error("not review mode's request")
		}
		if !strings.Contains(msgs[1].Content, in.Candidate) {
			t.Error("candidate missing")
		}
		checkCarries(t, msgs[1].Content, own)
	}
}

func TestDeliberateBuildersRefuseWithoutDeliberation(t *testing.T) {
	in := goldenReviewInput(nil)
	if _, err := DeliberateReview(in, nil, nil, testDisposition, -1, &seqRand{}); err == nil {
		t.Error("review: no proposals accepted")
	}
	if _, err := DeliberateReview(in, testProposals, testCritiques[:1], testDisposition, -1, &seqRand{}); err == nil {
		t.Error("review: critique mismatch accepted")
	}
	if _, err := DeliberateReview(in, testProposals, testCritiques, "", -1, &seqRand{}); err == nil {
		t.Error("review: empty disposition accepted")
	}
	rin := goldenReviewerRevisionInput(goldenHistory(t))
	if _, err := DeliberateReReview(rin, nil, nil, testDisposition, -1, &seqRand{}); err == nil {
		t.Error("re-review: no proposals accepted")
	}
	if _, err := IntegratorRevision(goldenRevisionInput(t), nil, nil, testDisposition, 0, &seqRand{}); err == nil {
		t.Error("revision: no proposals accepted")
	}
}

func goldenRevisionInput(t *testing.T) AuthorRevisionInput {
	return AuthorRevisionInput{
		Input:             "q",
		PreviousCandidate: testPreviousAnswer,
		ReviewerFeedback:  []RevisionFeedback{{Output: "reviewer one says no"}, {Output: "reviewer two says maybe"}},
		Delimiter:         goldenDelimiter,
		HistoryDoc:        goldenHistory(t),
	}
}

func TestIntegratorRevisionOrderAndContent(t *testing.T) {
	fb := goldenRevisionInput(t)
	msgs, err := IntegratorRevision(fb, testProposals, testCritiques, testDisposition, 1, &seqRand{})
	if err != nil {
		t.Fatal(err)
	}
	integ, _ := Integration(fb.HistoryDoc, fb.Input, testProposals, testCritiques, 1, &seqRand{})
	n := len(integ)
	if len(msgs) != n+2 {
		t.Fatalf("len %d want %d", len(msgs), n+2)
	}
	for i := range integ {
		if !sameMsg(msgs[i], integ[i]) {
			t.Errorf("message %d differs from integration's", i)
		}
	}
	if msgs[0].Role != "system" {
		t.Error("first message not system")
	}
	if integ[n-1].Role != "user" || msgs[n].Role != "assistant" || msgs[n+1].Role != "user" {
		t.Errorf("roles wrong: %s %s", msgs[n].Role, msgs[n+1].Role)
	}
	wantPrev := AnswerMarker + "\n" + testPreviousAnswer + "\n" + DispositionMarker + "\n" + testDisposition
	if msgs[n].Content != wantPrev {
		t.Errorf("previous candidate: %q", msgs[n].Content)
	}
	checkCarriesNoDisp := msgs[n-1].Content
	for i, s := range append(append([]string{}, testProposals...), testCritiques...) {
		if !strings.Contains(checkCarriesNoDisp, "\n"+s+"\n=====") {
			t.Errorf("text %d not verbatim", i)
		}
	}
	rev, _ := AuthorRevision(fb)
	last := msgs[n+1].Content
	if !strings.HasPrefix(last, rev[len(rev)-1].Content) {
		t.Error("reviewers' feedback is not AuthorRevision's")
	}
	if !strings.HasSuffix(last, IntegratorRevisionInstruction+"\n\n"+IntegrationEnvelope) {
		t.Error("instruction/envelope missing")
	}
}

func TestIntegratorRevisionRepairCarriesSameMessages(t *testing.T) {
	msgs, err := IntegratorRevision(goldenRevisionInput(t), testProposals, testCritiques, testDisposition, 0, &seqRand{})
	if err != nil {
		t.Fatal(err)
	}
	got := IntegrationRepair(msgs, "malformed")
	if len(got) != len(msgs)+2 {
		t.Fatalf("len %d", len(got))
	}
	for i := range msgs {
		if !sameMsg(got[i], msgs[i]) {
			t.Errorf("message %d changed", i)
		}
	}
	assertGolden(t, "integration_repair_instruction.txt", got[len(got)-1].Content)
}

func TestDeliberateReReview(t *testing.T) {
	for _, own := range []int{-1, 2} {
		in := goldenReviewerRevisionInput(goldenHistory(t))
		in.OpenBlocks = []OpenBlockForRevision{{BlockID: "mine", Severity: "low", Evidence: "e", Remedy: "r"}}
		msgs, err := DeliberateReReview(in, testProposals, testCritiques, testDisposition, own, &seqRand{})
		if err != nil {
			t.Fatal(err)
		}
		base, _ := ReviewerRevision(in)
		if !sameMsg(msgs[0], base[0]) || !strings.HasPrefix(msgs[1].Content, base[1].Content) {
			t.Error("not review mode's re-review")
		}
		all := joined(msgs)
		for _, s := range []string{in.Candidate, "BEGIN BLOCK mine", testDisposition} {
			if !strings.Contains(all, s) {
				t.Errorf("missing %q", s)
			}
		}
		for _, s := range []string{"BLOCK other", "other-reviewer-evidence", testEarlierDisp} {
			if strings.Contains(all, s) {
				t.Errorf("contains %q", s)
			}
		}
		checkCarries(t, msgs[1].Content, own)
	}
}

func TestGoldenDeliberateReview(t *testing.T) {
	assertGolden(t, "deliberate_review_instruction.txt", DeliberateReviewInstruction)
	assertGolden(t, "integrator_revision_instruction.txt", IntegratorRevisionInstruction)
	assertGolden(t, "deliberate_re_review_instruction.txt", DeliberateReReviewInstruction)
	ps, cs := []string{"first", "second"}, []string{"c1", "c2"}
	r, err := DeliberateReview(goldenReviewInput(goldenHistory(t)), ps, cs, "disp", 1, &seqRand{})
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "deliberate_review_user.txt", r[1].Content)
	rr, err := DeliberateReReview(goldenReviewerRevisionInput(goldenHistory(t)), ps, cs, "disp", -1, &seqRand{})
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "deliberate_re_review_user.txt", rr[1].Content)
	fb := goldenRevisionInput(t)
	iv, err := IntegratorRevision(fb, ps, cs, "disp", 0, &seqRand{})
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "integrator_revision_messages.txt", renderMessages(iv))
}

func sameMsg(a, b transport.Message) bool { return a.Role == b.Role && a.Content == b.Content }
