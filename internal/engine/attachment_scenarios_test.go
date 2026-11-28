package engine

import (
	"testing"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
)

// TestAttachmentEstimateConsistency verifies that estimate functions handle
// attachments consistently across different scenarios.
func TestAttachmentEstimateConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping acceptance scenario in -short mode")
	}

	delimiter := standInToken
	testAttachments := []journal.AttachmentContent{
		{Path: "file1.txt", Content: []byte("content1")},
		{Path: "file2.py", Content: []byte("import sys")},
	}

	attachmentBytes := attachmentRequestBytes(testAttachments, len(delimiter))
	if attachmentBytes == 0 {
		t.Fatal("expected non-zero attachment bytes")
	}

	msgs := []transport.Message{
		{Role: "user", Content: "test request"},
	}

	overheads := budget.Overheads{
		MessageTokens: 10,
		RequestTokens: 20,
	}

	bytesPerToken := 1.0

	// Calculate estimate using budget package directly
	baseEst := budget.RequestEstimateTokens(msgs, nil, bytesPerToken, overheads)

	// Create partBudget and test
	pb := partBudget{
		id:            "test-participant",
		bytesPerToken: bytesPerToken,
		overheads:     overheads,
	}

	// Estimate without attachments should match budget package
	noAttachEst := pb.estimate(msgs, nil)
	if noAttachEst != baseEst {
		t.Errorf("estimate without attachments %d should equal budget.RequestEstimateTokens %d", noAttachEst, baseEst)
	}

	// Estimate with attachments should be greater
	withAttachEst := pb.estimateWithAttachments(msgs, nil, attachmentBytes)
	if withAttachEst <= noAttachEst {
		t.Errorf("estimate with attachments %d should be > estimate without %d", withAttachEst, noAttachEst)
	}
}

// TestAttachmentAdmissionWithAndWithoutAttachments verifies that admission
// calculation properly accounts for attachments.
func TestAttachmentAdmissionWithAndWithoutAttachments(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping acceptance scenario in -short mode")
	}

	delimiter := standInToken
	testAttachments := []journal.AttachmentContent{
		{Path: "proposal.txt", Content: []byte("proposal content here")},
	}

	attachmentBytes := attachmentRequestBytes(testAttachments, len(delimiter))
	if attachmentBytes == 0 {
		t.Fatal("expected non-zero attachment bytes")
	}

	msgs := []transport.Message{
		{Role: "user", Content: "request"},
	}

	overheads := budget.Overheads{
		MessageTokens: 10,
		RequestTokens: 20,
	}

	pb := partBudget{
		id:            "test-participant",
		bytesPerToken: 1.0,
		overheads:     overheads,
	}

	// Admission without attachments
	admNoAttach := pb.admission(msgs, nil)

	// Admission with attachments
	admWithAttach := pb.admissionWithAttachments(msgs, nil, attachmentBytes)

	if admWithAttach <= admNoAttach {
		t.Errorf("admission with attachments %d should be > without %d", admWithAttach, admNoAttach)
	}

	// The difference should approximately equal the token cost of attachment bytes
	diff := admWithAttach - admNoAttach
	minExpectedDiff := attachmentBytes / 2 // Conservative estimate
	if diff < minExpectedDiff {
		t.Errorf("admission difference %d seems too small for %d attachment bytes", diff, attachmentBytes)
	}
}

// TestAttachmentNullBytesHandling verifies that attachment functions handle
// zero attachment bytes correctly.
func TestAttachmentNullBytesHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping acceptance scenario in -short mode")
	}

	pb := partBudget{
		id:            "test",
		bytesPerToken: 1.0,
		overheads: budget.Overheads{
			MessageTokens: 10,
			RequestTokens: 20,
		},
	}

	msgs := []transport.Message{
		{Role: "user", Content: "test"},
	}

	// With zero attachment bytes, should equal non-attachment versions
	estWithZero := pb.estimateWithAttachments(msgs, nil, 0)
	estWithout := pb.estimate(msgs, nil)
	if estWithZero != estWithout {
		t.Errorf("estimate with 0 bytes %d should equal estimate without %d", estWithZero, estWithout)
	}

	admWithZero := pb.admissionWithAttachments(msgs, nil, 0)
	admWithout := pb.admission(msgs, nil)
	if admWithZero != admWithout {
		t.Errorf("admission with 0 bytes %d should equal admission without %d", admWithZero, admWithout)
	}
}

// TestAttachmentStandInWillExceedBudget verifies that stand-in attachments
// correctly project the worst-case byte cost.
func TestAttachmentStandInWillExceedBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping acceptance scenario in -short mode")
	}

	delimiter := standInToken

	// Single small attachment
	smallPaths := []string{"small.txt"}
	smallBytes := attachmentRequestBytesStandIn(1, smallPaths, len(delimiter))
	if smallBytes < MaxAttachmentFileSize {
		t.Errorf("standIn for single attachment %d should be >= MaxAttachmentFileSize %d", smallBytes, MaxAttachmentFileSize)
	}

	// Multiple attachments should sum up
	manyPaths := []string{"a.txt", "b.txt", "c.txt"}
	manyBytes := attachmentRequestBytesStandIn(3, manyPaths, len(delimiter))
	if manyBytes < 3*MaxAttachmentFileSize {
		t.Errorf("standIn for 3 attachments %d should be >= 3*MaxAttachmentFileSize", manyBytes)
	}

	// Three attachments should cost more than one
	if manyBytes <= smallBytes {
		t.Errorf("standIn for 3 attachments %d should be > standIn for 1 %d", manyBytes, smallBytes)
	}
}

// TestAttachmentFramingDataMarking verifies that the framing includes the
// "data, not instructions" marking concept.
func TestAttachmentFramingDataMarking(t *testing.T) {
	delimiter := "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1"
	testPath := "test.txt"
	testContent := []byte("test content")

	rendered := RenderAttachmentFencing(delimiter, testPath, testContent)

	// Verify the rendering has the structure described in the design
	if !contains(rendered, "BEGIN ATTACHMENT") {
		t.Error("rendered attachment should contain BEGIN ATTACHMENT marker")
	}
	if !contains(rendered, "END ATTACHMENT") {
		t.Error("rendered attachment should contain END ATTACHMENT marker")
	}
	if !contains(rendered, testPath) {
		t.Error("rendered attachment should contain the path")
	}
	if !contains(rendered, string(testContent)) {
		t.Error("rendered attachment should contain the content")
	}

	// Verify the note constant exists and is non-empty
	if AttachmentFencingNote == "" {
		t.Error("AttachmentFencingNote should not be empty")
	}
	if !contains(AttachmentFencingNote, "data") {
		t.Error("AttachmentFencingNote should mention 'data'")
	}
	if !contains(AttachmentFencingNote, "instructions") {
		t.Error("AttachmentFencingNote should mention 'instructions'")
	}
}
