package engine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/boldfield/syndic/internal/workspace"
)

// Attachment limits are design constants (v6 section 4, step 5).
const (
	MaxAttachments         = 8
	MaxAttachmentFileSize  = 256 << 10
	MaxAttachmentTotalSize = 1 << 20
)

// Reasons of an attachment refusal (v6 section 4, steps 2 and 5).
const (
	attachForm          = "form"
	attachDuplicate     = "duplicate"
	attachNotFound      = "not_found"
	attachNotRegular    = "not_regular"
	attachProtected     = "protected"
	attachOutside       = "outside"
	attachTooLarge      = "too_large"
	attachTotalTooLarge = "total_too_large"
	attachNotText       = "not_text"
)

func attachmentRefusal(path, reason string) *wire.Error {
	r := refusal(wire.ErrorCodeAttachment)
	r.Path = strPtr(path)
	r.Reason = strPtr(reason)
	return r
}

// attachmentPathForm reports whether path is a relative workspace path with no
// empty, "." or ".." component, no trailing separator and no control character.
func attachmentPathForm(path string) bool {
	if path == "" || !utf8.ValidString(path) {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, comp := range strings.Split(path, "/") {
		if comp == "" || comp == "." || comp == ".." {
			return false
		}
	}
	return true
}

// ValidateAttachmentPaths checks the list shape, then each path's form and
// that no two are equal (v6 section 4, step 2). It touches no file. A list
// over the maximum is a malformed request, not an attachment refusal: v6 gives
// attachment reasons only for single paths.
func ValidateAttachmentPaths(paths []string) *wire.Error {
	if len(paths) > MaxAttachments {
		r := refusal(wire.ErrorCodeBadRequest)
		r.Detail = strPtr(fmt.Sprintf("at most %d attachments", MaxAttachments))
		return r
	}
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !attachmentPathForm(p) {
			return attachmentRefusal(p, attachForm)
		}
		if seen[p] {
			return attachmentRefusal(p, attachDuplicate)
		}
		seen[p] = true
	}
	return nil
}

// ReadAttachments validates paths and loads each through the confined walk,
// returning the attachments in the order listed. It commits nothing; on any
// refusal it returns no content. A failure that is not a defined attachment
// reason (permission, I/O) is an internal error.
func ReadAttachments(c *workspace.Confined, paths []string) ([]journal.AttachmentContent, *wire.Error) {
	if werr := ValidateAttachmentPaths(paths); werr != nil {
		return nil, werr
	}
	out := make([]journal.AttachmentContent, 0, len(paths))
	var total int64
	for _, p := range paths {
		att, werr := readAttachment(c, p)
		if werr != nil {
			return nil, werr
		}
		total += int64(len(att.Content))
		if total > MaxAttachmentTotalSize {
			return nil, attachmentRefusal(p, attachTotalTooLarge)
		}
		out = append(out, att)
	}
	return out, nil
}

func readAttachment(c *workspace.Confined, p string) (journal.AttachmentContent, *wire.Error) {
	f, err := c.OpenFile(p, unix.O_RDONLY, 0)
	if err != nil {
		return journal.AttachmentContent{}, classifyAttachmentOpen(p, err)
	}
	defer func() { _ = f.Close() }()

	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return journal.AttachmentContent{}, attachmentIOError(p, err)
	}
	// The limit is the bytes read, not the size a stat reported.
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachmentFileSize+1))
	if err != nil {
		return journal.AttachmentContent{}, attachmentIOError(p, err)
	}
	if len(data) > MaxAttachmentFileSize {
		return journal.AttachmentContent{}, attachmentRefusal(p, attachTooLarge)
	}
	if !utf8.Valid(data) {
		return journal.AttachmentContent{}, attachmentRefusal(p, attachNotText)
	}
	return journal.AttachmentContent{Path: p, Content: data, OwnerExec: st.Mode&unix.S_IXUSR != 0}, nil
}

func classifyAttachmentOpen(p string, err error) *wire.Error {
	switch {
	case errors.Is(err, workspace.ErrProtected):
		return attachmentRefusal(p, attachProtected)
	case errors.Is(err, workspace.ErrSymlink), errors.Is(err, workspace.ErrSymlinkOnPath):
		return attachmentRefusal(p, attachOutside)
	case errors.Is(err, workspace.ErrNotRegular):
		return attachmentRefusal(p, attachNotRegular)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, unix.ENOTDIR):
		// ENOTDIR: a component before the last is a regular file, so the
		// path names nothing.
		return attachmentRefusal(p, attachNotFound)
	case errors.Is(err, unix.ENXIO), errors.Is(err, unix.EISDIR):
		return attachmentRefusal(p, attachNotRegular)
	}
	return attachmentIOError(p, err)
}

func attachmentIOError(p string, err error) *wire.Error {
	r := refusal(wire.ErrorCodeInternal)
	r.Detail = strPtr(fmt.Sprintf("read attachment %q: %v", p, err))
	return r
}
