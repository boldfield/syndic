package prompt

import (
	"errors"
	"fmt"
	"io"

	"github.com/boldfield/syndic/internal/transport"
)

// Instruction strings of the review of a deliberate candidate. Each has a
// golden file.
const (
	DeliberateReviewInstruction = "The candidate was written by integrating the proposals and critiques below, and the disposition says which points of the critiques it adopted and why not the others. Judge the candidate as you would any candidate, by the standard in the system message; the proposals, the critiques and the disposition are context for that judgment, not a part of the answer the user receives. Reply with your verdict in the format the system message gives."

	IntegratorRevisionInstruction = "Reviewers have objected to your previous answer; their complete output is above. Write the revised answer and the revised disposition, the disposition covering the critiques as before and now the reviewers' objections as well. Reply in exactly this form, with each marker alone on its line:"

	DeliberateReReviewInstruction = "The revised candidate was written by integrating the proposals and critiques below, and its disposition says which points of the critiques and of the reviewers' objections it adopted and why not the others. Review it as the system message directs; the proposals, the critiques and the disposition are context, not a part of the answer the user receives."
)

// deliberateContext returns the paragraphs every deliberate review request
// adds to review mode's: the instruction, the data note, every proposal, every
// critique and the disposition, each fenced and labelled for the recipient at
// index own among the proposers (-1 for a recipient that proposed nothing).
func deliberateContext(instruction string, proposals, critiques []string, disposition string, own int, rand io.Reader) ([]string, error) {
	if len(proposals) == 0 {
		return nil, errors.New("no proposals")
	}
	if len(critiques) != len(proposals) {
		return nil, fmt.Errorf("%d critiques for %d proposals", len(critiques), len(proposals))
	}
	if own < -1 || own >= len(proposals) {
		return nil, fmt.Errorf("own index %d out of range for %d proposals", own, len(proposals))
	}
	if disposition == "" {
		return nil, errors.New("empty disposition")
	}
	ps, err := fenceAll(rand, "proposal", proposals, own)
	if err != nil {
		return nil, err
	}
	cs, err := fenceAll(rand, "critique", critiques, own)
	if err != nil {
		return nil, err
	}
	d, err := fence(rand, "disposition", disposition)
	if err != nil {
		return nil, err
	}
	parts := []string{instruction, deliberateDataNote}
	parts = append(parts, ps...)
	parts = append(parts, cs...)
	return append(parts, d), nil
}

// DeliberateReview returns what Reviewer returns for in, with every proposal,
// every critique and the candidate's disposition added to the user message.
// own is the reviewer's index among the proposers, or -1 if it proposed nothing.
func DeliberateReview(in ReviewInput, proposals, critiques []string, disposition string, own int, rand io.Reader) ([]transport.Message, error) {
	extra, err := deliberateContext(DeliberateReviewInstruction, proposals, critiques, disposition, own, rand)
	if err != nil {
		return nil, err
	}
	msgs, err := Reviewer(in)
	if err != nil {
		return nil, err
	}
	msgs[1].Content = paragraphs(append([]string{msgs[1].Content}, extra...)...)
	return msgs, nil
}

// IntegratorRevision returns the integrator's revision request: the messages
// Integration returns, the previous candidate as an assistant message in the
// integration envelope, then the reviewers' feedback as AuthorRevision builds
// it, with the instruction for the revised answer and disposition added. The
// integrator is the proposer at index own. fb.PreviousCandidate is the previous
// answer; fb.Input and fb.HistoryDoc must be the request's and the history's.
func IntegratorRevision(fb AuthorRevisionInput, proposals, critiques []string, previousDisposition string, own int, rand io.Reader) ([]transport.Message, error) {
	if len(proposals) == 0 {
		return nil, errors.New("no proposals")
	}
	if previousDisposition == "" {
		return nil, errors.New("empty disposition")
	}
	feedback, err := AuthorRevision(fb)
	if err != nil {
		return nil, err
	}
	msgs, err := Integration(fb.HistoryDoc, fb.Input, proposals, critiques, own, rand)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs,
		transport.Message{Role: "assistant", Content: AnswerMarker + "\n" + fb.PreviousCandidate + "\n" + DispositionMarker + "\n" + previousDisposition},
		transport.Message{Role: "user", Content: paragraphs(feedback[len(feedback)-1].Content, IntegratorRevisionInstruction, IntegrationEnvelope)},
	)
	return msgs, nil
}

// DeliberateReReview returns what ReviewerRevision returns for in, with every
// proposal, every critique and the new candidate's disposition added to the
// user message. own is as in DeliberateReview.
func DeliberateReReview(in ReviewerRevisionInput, proposals, critiques []string, disposition string, own int, rand io.Reader) ([]transport.Message, error) {
	extra, err := deliberateContext(DeliberateReReviewInstruction, proposals, critiques, disposition, own, rand)
	if err != nil {
		return nil, err
	}
	msgs, err := ReviewerRevision(in)
	if err != nil {
		return nil, err
	}
	msgs[1].Content = paragraphs(append([]string{msgs[1].Content}, extra...)...)
	return msgs, nil
}
