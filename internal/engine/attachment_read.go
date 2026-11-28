package engine

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/boldfield/syndic/internal/workspace"
)

const (
	MaxAttachmentFileSize      = 256 * 1024   // 256 KiB
	MaxAttachmentFileReadLimit = 256*1024 + 1 // 256 KiB + 1 byte (for overflow detection)
	MaxAttachmentTotalSize     = 1024 * 1024  // 1 MiB
	MaxAttachmentCount         = 8
)

// attachmentError represents a failure to read and validate an attachment.
type attachmentError struct {
	path   string
	reason string
}

func (e *attachmentError) Error() string { return fmt.Sprintf("%s: %s", e.path, e.reason) }

// ReadAttachments validates path forms and reads bounded attachments through the confined walk.
// It returns an in-memory validated admission bundle with attachment records and content.
// On error, it returns a *wire.Error with code "attachment".
func ReadAttachments(fs *workspace.Confined, paths []string) ([]journal.AttachmentContent, *wire.Error) {
	if len(paths) == 0 {
		return nil, nil
	}

	if len(paths) > MaxAttachmentCount {
		path := paths[MaxAttachmentCount]
		return nil, &wire.Error{
			Code:   "attachment",
			Detail: strPtr(fmt.Sprintf("%q: cannot attach more than %d files", path, MaxAttachmentCount)),
		}
	}

	// Validate path forms first (step 2)
	if err := validateAttachmentPaths(paths); err != nil {
		var ae *attachmentError
		if errors.As(err, &ae) {
			return nil, &wire.Error{
				Code:   "attachment",
				Detail: strPtr(fmt.Sprintf("%q: %s", ae.path, ae.reason)),
			}
		}
		return nil, &wire.Error{Code: "attachment", Detail: strPtr(err.Error())}
	}

	// Read attachments (step 5)
	var attachments []journal.AttachmentContent
	var totalSize int64

	for _, path := range paths {
		// Check for protected paths
		if fs.IsProtected(path) {
			return nil, &wire.Error{
				Code:   "attachment",
				Detail: strPtr(fmt.Sprintf("%q: protected", path)),
			}
		}

		// Read the file through confined walk
		content, err := readBoundedFile(fs, path)
		if err != nil {
			var ae *attachmentError
			if errors.As(err, &ae) {
				return nil, &wire.Error{
					Code:   "attachment",
					Detail: strPtr(fmt.Sprintf("%q: %s", ae.path, ae.reason)),
				}
			}
			return nil, &wire.Error{
				Code:   "attachment",
				Detail: strPtr(fmt.Sprintf("%q: %v", path, err)),
			}
		}

		// Validate UTF-8
		if !utf8.Valid(content) {
			return nil, &wire.Error{
				Code:   "attachment",
				Detail: strPtr(fmt.Sprintf("%q: not_text", path)),
			}
		}

		// Check total size
		totalSize += int64(len(content))
		if totalSize > MaxAttachmentTotalSize {
			return nil, &wire.Error{
				Code:   "attachment",
				Detail: strPtr(fmt.Sprintf("%q: total_too_large", path)),
			}
		}

		attachments = append(attachments, journal.AttachmentContent{
			Path:    path,
			Content: content,
		})
	}

	return attachments, nil
}

// validateAttachmentPaths checks path form requirements from v6 step 2.
func validateAttachmentPaths(paths []string) error {
	seen := make(map[string]bool)

	for _, path := range paths {
		// Relative path with no empty, ".", or ".." component, no trailing separator, no control character
		if err := validateAttachmentPath(path); err != nil {
			return err
		}

		// No duplicates
		if seen[path] {
			return &attachmentError{path: path, reason: "duplicate"}
		}
		seen[path] = true
	}

	return nil
}

// validateAttachmentPath checks that a single path meets form requirements.
func validateAttachmentPath(path string) error {
	if path == "" {
		return &attachmentError{path: path, reason: "form"}
	}

	// No absolute paths
	if strings.HasPrefix(path, "/") {
		return &attachmentError{path: path, reason: "form"}
	}

	// No trailing separator
	if strings.HasSuffix(path, "/") {
		return &attachmentError{path: path, reason: "form"}
	}

	// No control characters
	for _, r := range path {
		if r < 0x20 || r == 0x7F {
			return &attachmentError{path: path, reason: "form"}
		}
	}

	// Check components
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return &attachmentError{path: path, reason: "form"}
		}
	}

	return nil
}

// readBoundedFile reads a file with confinement checks and size bounds.
// Returns error reasons: not_found, not_regular, outside, too_large, based on v6 design.
func readBoundedFile(c *workspace.Confined, path string) ([]byte, error) {
	f, err := c.OpenFile(path, 0, 0)
	if err != nil {
		// Classify the error
		if errors.Is(err, workspace.ErrSymlink) {
			// A symlink at the last component is a confinement violation
			return nil, &attachmentError{path: path, reason: "outside"}
		}
		if errors.Is(err, workspace.ErrSymlinkOnPath) {
			// A symlink on the path before the last component
			return nil, &attachmentError{path: path, reason: "outside"}
		}
		if strings.Contains(err.Error(), "not a regular file") {
			return nil, &attachmentError{path: path, reason: "not_regular"}
		}
		// Default to not_found for missing paths
		return nil, &attachmentError{path: path, reason: "not_found"}
	}
	defer func() { _ = f.Close() }()

	// Read with bounded limit: 256 KiB + 1 (to detect oversized files)
	limitedReader := io.LimitReader(f, MaxAttachmentFileReadLimit)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, &attachmentError{path: path, reason: "not_found"}
	}

	// If we read more than 256 KiB, the file is too large
	if len(content) > MaxAttachmentFileSize {
		return nil, &attachmentError{path: path, reason: "too_large"}
	}

	return content, nil
}
