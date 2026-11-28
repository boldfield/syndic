package engine

import (
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestRenderAttachmentFencing(t *testing.T) {
	tests := []struct {
		name      string
		delimiter string
		path      string
		content   []byte
	}{
		{
			name:      "simple attachment",
			delimiter: "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1",
			path:      "test.txt",
			content:   []byte("hello world"),
		},
		{
			name:      "attachment with special chars",
			delimiter: "0123456789abcdef0123456789abcdef",
			path:      "dir/file-name_123.py",
			content:   []byte("import sys\nprint('test')"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderAttachmentFencing(tt.delimiter, tt.path, tt.content)
			// Verify format
			expectedStart := "===== " + tt.delimiter + " BEGIN ATTACHMENT " + tt.path + " ====="
			expectedEnd := "===== " + tt.delimiter + " END ATTACHMENT ====="
			if !contains(rendered, expectedStart) {
				t.Errorf("rendered does not contain start: %q", expectedStart)
			}
			if !contains(rendered, expectedEnd) {
				t.Errorf("rendered does not contain end: %q", expectedEnd)
			}
			if !contains(rendered, string(tt.content)) {
				t.Errorf("rendered does not contain content")
			}
		})
	}
}

func TestAttachmentRequestBytesExactMatch(t *testing.T) {
	delimiter := standInToken // 32-byte placeholder
	tests := []struct {
		name        string
		attachments []journal.AttachmentContent
	}{
		{
			name:        "no attachments",
			attachments: nil,
		},
		{
			name: "single attachment",
			attachments: []journal.AttachmentContent{
				{Path: "file.txt", Content: []byte("hello")},
			},
		},
		{
			name: "multiple attachments",
			attachments: []journal.AttachmentContent{
				{Path: "a.txt", Content: []byte("content1")},
				{Path: "b.txt", Content: []byte("content2")},
			},
		},
		{
			name: "attachment with newlines",
			attachments: []journal.AttachmentContent{
				{Path: "multiline.txt", Content: []byte("line1\nline2\nline3")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachmentRequestBytes(tt.attachments, len(delimiter))

			// Verify exact match by rendering and comparing
			var totalRendered int
			for _, att := range tt.attachments {
				rendered := RenderAttachmentFencing(delimiter, att.Path, att.Content)
				totalRendered += len(rendered)
			}

			if got != totalRendered {
				t.Errorf("attachmentRequestBytes() = %d, but rendered = %d, they must match exactly", got, totalRendered)
			}
		})
	}
}

func TestAttachmentRequestBytesStandIn(t *testing.T) {
	delimiter := standInToken // 32-byte placeholder
	paths := []string{"file.txt", "dir/file.py"}

	tests := []struct {
		name           string
		numAttachments int
		paths          []string
	}{
		{
			name:           "no attachments",
			numAttachments: 0,
			paths:          nil,
		},
		{
			name:           "single attachment",
			numAttachments: 1,
			paths:          []string{"file.txt"},
		},
		{
			name:           "multiple attachments",
			numAttachments: 2,
			paths:          paths,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachmentRequestBytesStandIn(tt.numAttachments, tt.paths, len(delimiter))

			// Verify that result makes sense: should be greater than zero for positive input
			if tt.numAttachments > 0 && got == 0 {
				t.Errorf("attachmentRequestBytesStandIn() = 0, want > 0 for %d attachments", tt.numAttachments)
			}

			// Verify that it accounts for MaxAttachmentFileSize
			if tt.numAttachments > 0 {
				minExpected := tt.numAttachments * (MaxAttachmentFileSize + 100)
				if got < minExpected {
					t.Errorf("attachmentRequestBytesStandIn() = %d, want >= %d (accounting for max file size)", got, minExpected)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
