package engine

import (
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestAttachmentRequestBytes(t *testing.T) {
	delimiter := 32 // standInToken length
	tests := []struct {
		name        string
		attachments []journal.AttachmentContent
		wantMin     int
	}{
		{
			name:        "no attachments",
			attachments: nil,
			wantMin:     0,
		},
		{
			name: "single small attachment",
			attachments: []journal.AttachmentContent{
				{Path: "file.txt", Content: []byte("hello")},
			},
			wantMin: 100, // framing overhead + content
		},
		{
			name: "multiple attachments",
			attachments: []journal.AttachmentContent{
				{Path: "a.txt", Content: []byte("content1")},
				{Path: "b.txt", Content: []byte("content2")},
			},
			wantMin: 200, // framing for both + content
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachmentRequestBytes(tt.attachments, delimiter)
			if got < tt.wantMin {
				t.Errorf("attachmentRequestBytes() = %d, want >= %d", got, tt.wantMin)
			}

			// Verify calculation
			if got > 0 && tt.attachments != nil {
				// Should include path lengths and content lengths
				contentSize := 0
				pathSize := 0
				for _, att := range tt.attachments {
					contentSize += len(att.Content)
					pathSize += len(att.Path)
				}
				// Rough check: bytes should be at least content + paths + substantial framing
				if got < contentSize+pathSize+50 {
					t.Errorf("attachmentRequestBytes() = %d, seems too small for %d attachments", got, len(tt.attachments))
				}
			}
		})
	}
}

func TestAttachmentRequestBytesStandIn(t *testing.T) {
	delimiter := 32 // standInToken length
	tests := []struct {
		name            string
		numAttachments  int
		paths           []string
		wantMin         int
		wantGreaterThan int
	}{
		{
			name:           "no attachments",
			numAttachments: 0,
			paths:          nil,
			wantMin:        0,
		},
		{
			name:           "single attachment, max size assumed",
			numAttachments: 1,
			paths:          []string{"file.txt"},
			wantMin:        MaxAttachmentFileSize + 100, // content + framing
		},
		{
			name:           "multiple attachments",
			numAttachments: 3,
			paths:          []string{"a.txt", "b.txt", "c.txt"},
			wantMin:        3 * (MaxAttachmentFileSize + 100), // per-attachment overhead
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachmentRequestBytesStandIn(tt.numAttachments, tt.paths, delimiter)
			if got < tt.wantMin {
				t.Errorf("attachmentRequestBytesStandIn() = %d, want >= %d", got, tt.wantMin)
			}
		})
	}
}
