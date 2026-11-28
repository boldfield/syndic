package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/boldfield/syndic/internal/workspace"
)

const (
	MaxAttachmentFileSize  = 256 * 1024  // 256 KiB
	MaxAttachmentTotalSize = 1024 * 1024 // 1 MiB
	MaxAttachmentCount     = 8
)

// ReadAttachments validates path forms and reads bounded attachments through the confined walk.
// It returns an in-memory validated admission bundle with attachment records and content.
// On error, it returns a *wire.Error with code "attachment".
func ReadAttachments(fs *workspace.Confined, paths []string) ([]journal.AttachmentContent, *wire.Error) {
	if len(paths) == 0 {
		return nil, nil
	}

	if len(paths) > MaxAttachmentCount {
		path := paths[MaxAttachmentCount]
		reason := "form"
		return nil, &wire.Error{
			Code:   "attachment",
			Path:   &path,
			Reason: &reason,
		}
	}

	// Validate path forms first (step 2)
	if err := validateAttachmentPaths(paths); err != nil {
		return nil, err
	}

	// Read attachments (step 5)
	var attachments []journal.AttachmentContent
	var totalSize int64

	for _, path := range paths {
		// Check for protected paths before opening
		if fs.IsProtected(path) {
			reason := "protected"
			return nil, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}

		// Read the file through confined walk
		content, ownerExec, err := readBoundedFile(fs, path)
		if err != nil {
			return nil, err
		}

		// Validate UTF-8
		if !utf8.Valid(content) {
			reason := "not_text"
			return nil, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}

		// Check total size
		totalSize += int64(len(content))
		if totalSize > MaxAttachmentTotalSize {
			reason := "total_too_large"
			return nil, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}

		attachments = append(attachments, journal.AttachmentContent{
			Path:      path,
			Content:   content,
			OwnerExec: ownerExec,
		})
	}

	return attachments, nil
}

// validateAttachmentPaths checks path form requirements from v6 step 2.
func validateAttachmentPaths(paths []string) *wire.Error {
	seen := make(map[string]bool)

	for _, path := range paths {
		// Relative path with no empty, ".", or ".." component, no trailing separator, no control character
		if err := validateAttachmentPath(path); err != nil {
			return err
		}

		// No duplicates
		if seen[path] {
			reason := "duplicate"
			return &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
		seen[path] = true
	}

	return nil
}

// validateAttachmentPath checks that a single path meets form requirements.
func validateAttachmentPath(path string) *wire.Error {
	if path == "" {
		reason := "form"
		return &wire.Error{
			Code:   "attachment",
			Path:   &path,
			Reason: &reason,
		}
	}

	// No absolute paths
	if strings.HasPrefix(path, "/") {
		reason := "form"
		return &wire.Error{
			Code:   "attachment",
			Path:   &path,
			Reason: &reason,
		}
	}

	// No trailing separator
	if strings.HasSuffix(path, "/") {
		reason := "form"
		return &wire.Error{
			Code:   "attachment",
			Path:   &path,
			Reason: &reason,
		}
	}

	// No control characters (includes C0, C1, and DEL)
	for _, r := range path {
		if unicode.IsControl(r) {
			reason := "form"
			return &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
	}

	// Check components
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			reason := "form"
			return &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
	}

	return nil
}

// readBoundedFile reads a file with confinement checks and size bounds.
// Returns the content, the owner-execute bit, and an error.
func readBoundedFile(c *workspace.Confined, path string) ([]byte, bool, *wire.Error) {
	f, err := c.OpenFile(path, 0, 0)
	if err != nil {
		// Classify the error
		if errors.Is(err, workspace.ErrSymlink) {
			reason := "outside"
			return nil, false, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
		if errors.Is(err, workspace.ErrSymlinkOnPath) {
			reason := "outside"
			return nil, false, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
		if errors.Is(err, workspace.ErrNotRegular) {
			reason := "not_regular"
			return nil, false, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
		if errors.Is(err, fs.ErrNotExist) {
			reason := "not_found"
			return nil, false, &wire.Error{
				Code:   "attachment",
				Path:   &path,
				Reason: &reason,
			}
		}
		// Other errors (permissions, I/O, etc.) are internal failures, not attachment refusals
		detail := fmt.Sprintf("I/O error reading %q: %v", path, err)
		return nil, false, &wire.Error{
			Code:   "internal",
			Detail: &detail,
		}
	}
	defer func() { _ = f.Close() }()

	// Get file stats to extract OwnerExec bit
	stat, err := f.Stat()
	if err != nil {
		detail := fmt.Sprintf("stat error on %q: %v", path, err)
		return nil, false, &wire.Error{
			Code:   "internal",
			Detail: &detail,
		}
	}

	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		detail := fmt.Sprintf("unexpected stat representation for %q", path)
		return nil, false, &wire.Error{
			Code:   "internal",
			Detail: &detail,
		}
	}
	ownerExec := (sys.Mode & unix.S_IXUSR) != 0

	// Read with bounded limit: 256 KiB + 1 (to detect oversized files)
	// Size is what was read, not what prior stat reported
	maxReadLimit := int64(MaxAttachmentFileSize + 1)
	limitedReader := io.LimitReader(f, maxReadLimit)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		detail := fmt.Sprintf("read error on %q: %v", path, err)
		return nil, false, &wire.Error{
			Code:   "internal",
			Detail: &detail,
		}
	}

	// If we read more than 256 KiB, the file is too large
	if len(content) > MaxAttachmentFileSize {
		reason := "too_large"
		return nil, false, &wire.Error{
			Code:   "attachment",
			Path:   &path,
			Reason: &reason,
		}
	}

	return content, ownerExec, nil
}
