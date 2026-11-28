package engine

import (
	"github.com/boldfield/syndic/internal/journal"
)

// attachmentRequestBytes calculates the byte cost of rendering attachments in a
// participant request, including fenced framing, path labels, and content.
// Each attachment is fenced separately with the format:
//
//	===== DELIMITER BEGIN ATTACHMENT path =====
//	[content]
//	===== DELIMITER END ATTACHMENT path =====
//
// The delimiter is assumed to be standInToken (32 bytes). Path and content are
// rendered as-is. Attachments are rendered in the order provided.
func attachmentRequestBytes(attachments []journal.AttachmentContent, delimiterLen int) int {
	if len(attachments) == 0 {
		return 0
	}
	var total int
	for _, att := range attachments {
		// ===== DELIMITER BEGIN ATTACHMENT path =====\n
		total += 6                // "===== "
		total += delimiterLen     // delimiter
		total += 18               // " BEGIN ATTACHMENT "
		total += len(att.Path)    // path
		total += 7                // " =====\n"
		total += len(att.Content) // content
		// ===== DELIMITER END ATTACHMENT path =====\n\n
		total += 6             // "===== "
		total += delimiterLen  // delimiter
		total += 15            // " END ATTACHMENT "
		total += len(att.Path) // path
		total += 8             // " =====\n\n"
	}
	return total
}

// attachmentRequestBytesStandIn calculates the worst-case byte cost of
// attachments when their content is not known. It assumes the maximum
// attachment file size for each attachment (the actual content length is
// unknown during projection). This is exported for testing only.
func attachmentRequestBytesStandIn(numAttachments int, paths []string, delimiterLen int) int {
	if numAttachments == 0 || len(paths) == 0 {
		return 0
	}
	var total int
	for i := 0; i < numAttachments && i < len(paths); i++ {
		path := paths[i]
		// ===== DELIMITER BEGIN ATTACHMENT path =====\n
		total += 6            // "===== "
		total += delimiterLen // delimiter
		total += 18           // " BEGIN ATTACHMENT "
		total += len(path)    // path
		total += 7            // " =====\n"
		// Worst case: maximum attachment file size
		total += MaxAttachmentFileSize // content
		// ===== DELIMITER END ATTACHMENT path =====\n\n
		total += 6            // "===== "
		total += delimiterLen // delimiter
		total += 15           // " END ATTACHMENT "
		total += len(path)    // path
		total += 8            // " =====\n\n"
	}
	return total
}
