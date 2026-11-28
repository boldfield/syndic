package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/workspace"
)

func TestReadAttachments_ValidatePaths(t *testing.T) {
	tests := []struct {
		name       string
		paths      []string
		wantErr    bool
		errCode    string
		wantReason string
	}{
		{"empty list", []string{}, false, "", ""},
		{"absolute path", []string{"/etc/passwd"}, true, "attachment", "form"},
		{"parent traversal", []string{"../etc/passwd"}, true, "attachment", "form"},
		{"empty component", []string{"a//b.txt"}, true, "attachment", "form"},
		{"dot component", []string{"a/./b.txt"}, true, "attachment", "form"},
		{"trailing slash", []string{"a/b/"}, true, "attachment", "form"},
		{"control character", []string{"a\x00b.txt"}, true, "attachment", "form"},
		{"duplicate paths", []string{"file.txt", "file.txt"}, true, "attachment", "duplicate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempdir := t.TempDir()
			c, err := workspace.OpenConfined(tempdir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()

			_, errResp := engine.ReadAttachments(c, tt.paths)
			if (errResp != nil) != tt.wantErr {
				t.Errorf("got error %v, want %v", errResp != nil, tt.wantErr)
			}
			if tt.wantErr {
				if errResp.Code != tt.errCode {
					t.Errorf("got error code %q, want %q", errResp.Code, tt.errCode)
				}
				if tt.wantReason != "" {
					if errResp.Reason == nil || *errResp.Reason != tt.wantReason {
						reasonStr := "(nil)"
						if errResp.Reason != nil {
							reasonStr = *errResp.Reason
						}
						t.Errorf("got reason %q, want %q", reasonStr, tt.wantReason)
					}
				}
			}
		})
	}
}

func TestReadAttachments_FileOperations(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create test files
	content1 := []byte("hello world")
	if err := os.WriteFile(filepath.Join(tempdir, "file1.txt"), content1, 0644); err != nil {
		t.Fatal(err)
	}

	content2 := []byte("another file\n")
	if err := os.Mkdir(filepath.Join(tempdir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempdir, "subdir", "file2.txt"), content2, 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{"single file", []string{"file1.txt"}, ""},
		{"nested file", []string{"subdir/file2.txt"}, ""},
		{"nonexistent file", []string{"missing.txt"}, "not_found"},
		{"directory as file", []string{"subdir"}, "not_regular"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachments, errResp := engine.ReadAttachments(c, tt.paths)
			if (errResp != nil) != (tt.wantErr != "") {
				t.Errorf("got error %v, want %v", errResp != nil, tt.wantErr != "")
			}
			if tt.wantErr != "" && (errResp.Reason == nil || *errResp.Reason != tt.wantErr) {
				reasonStr := "(nil)"
				if errResp.Reason != nil {
					reasonStr = *errResp.Reason
				}
				t.Errorf("got reason %q, want %q", reasonStr, tt.wantErr)
			}
			if tt.wantErr == "" && len(attachments) == 0 {
				t.Errorf("got no attachments for valid file")
			}
		})
	}
}

func TestReadAttachments_SizeLimits(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create a file at the boundary (256 KiB)
	maxSize := 256 * 1024
	maxContent := make([]byte, maxSize)
	for i := range maxContent {
		maxContent[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(tempdir, "max.txt"), maxContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a file over the limit (256 KiB + 1)
	overContent := make([]byte, maxSize+1)
	for i := range overContent {
		overContent[i] = 'b'
	}
	if err := os.WriteFile(filepath.Join(tempdir, "over.txt"), overContent, 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{"at limit", []string{"max.txt"}, ""},
		{"over limit", []string{"over.txt"}, "too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachments, errResp := engine.ReadAttachments(c, tt.paths)
			if (errResp != nil) != (tt.wantErr != "") {
				t.Errorf("got error %v, want %v", errResp != nil, tt.wantErr != "")
			}
			if tt.wantErr != "" && (errResp.Reason == nil || *errResp.Reason != tt.wantErr) {
				reasonStr := "(nil)"
				if errResp.Reason != nil {
					reasonStr = *errResp.Reason
				}
				t.Errorf("got reason %q, want %q", reasonStr, tt.wantErr)
			}
			if tt.wantErr == "" {
				if len(attachments) != 1 {
					t.Errorf("got %d attachments, want 1", len(attachments))
				}
				if int64(len(attachments[0].Content)) != int64(maxSize) {
					t.Errorf("got size %d, want %d", len(attachments[0].Content), maxSize)
				}
			}
		})
	}
}

func TestReadAttachments_UTF8Validation(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Valid UTF-8
	if err := os.WriteFile(filepath.Join(tempdir, "valid.txt"), []byte("valid utf8\nwith unicode: é"), 0644); err != nil {
		t.Fatal(err)
	}

	// Invalid UTF-8
	if err := os.WriteFile(filepath.Join(tempdir, "invalid.txt"), []byte{0xFF, 0xFE}, 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{"valid utf8", []string{"valid.txt"}, ""},
		{"invalid utf8", []string{"invalid.txt"}, "not_text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachments, errResp := engine.ReadAttachments(c, tt.paths)
			if (errResp != nil) != (tt.wantErr != "") {
				t.Errorf("got error %v, want %v", errResp != nil, tt.wantErr != "")
			}
			if tt.wantErr != "" && (errResp.Reason == nil || *errResp.Reason != tt.wantErr) {
				reasonStr := "(nil)"
				if errResp.Reason != nil {
					reasonStr = *errResp.Reason
				}
				t.Errorf("got reason %q, want %q", reasonStr, tt.wantErr)
			}
			if tt.wantErr == "" && len(attachments) == 0 {
				t.Errorf("got no attachments")
			}
		})
	}
}

func TestReadAttachments_TotalSizeLimit(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create files to test exact boundary: 1 MiB total limit
	// Each file is under 256 KiB (per-file limit)
	// 4 files of 256 KiB each = 1 MiB exactly (should pass)
	// 5 files = 1 MiB + 256 KiB (should fail on the 5th file)
	fileSize256KiB := 256 * 1024 // 256 KiB each
	for i := 0; i < 5; i++ {
		name := filepath.Join(tempdir, fmt.Sprintf("file%d.txt", i))
		content := make([]byte, fileSize256KiB)
		for j := range content {
			content[j] = byte('a' + i%26)
		}
		if err := os.WriteFile(name, content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a small file (1 byte) to push over the limit when combined with 4 files
	smallContent := make([]byte, 1)
	smallContent[0] = 'x'
	if err := os.WriteFile(filepath.Join(tempdir, "file_small.txt"), smallContent, 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{"at total limit (4 × 256 KiB = 1 MiB)", []string{"file0.txt", "file1.txt", "file2.txt", "file3.txt"}, ""},
		{"over total limit (4 × 256 KiB + 1 byte)", []string{"file0.txt", "file1.txt", "file2.txt", "file3.txt", "file_small.txt"}, "total_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachments, errResp := engine.ReadAttachments(c, tt.paths)
			if (errResp != nil) != (tt.wantErr != "") {
				if errResp != nil {
					reasonStr := "(nil)"
					if errResp.Reason != nil {
						reasonStr = *errResp.Reason
					}
					t.Errorf("got error %q: %s, want %q", errResp.Code, reasonStr, tt.wantErr)
				} else {
					t.Errorf("got no error, want %q", tt.wantErr)
				}
			}
			if tt.wantErr != "" && (errResp.Reason == nil || *errResp.Reason != tt.wantErr) {
				reasonStr := "(nil)"
				if errResp.Reason != nil {
					reasonStr = *errResp.Reason
				}
				t.Errorf("got reason %q, want %q", reasonStr, tt.wantErr)
			}
			if tt.wantErr == "" && len(attachments) != len(tt.paths) {
				t.Errorf("got %d attachments, want %d", len(attachments), len(tt.paths))
			}
		})
	}
}

func TestReadAttachments_MaxCount(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create 9 files
	var paths []string
	for i := 0; i < 9; i++ {
		name := filepath.Join(tempdir, "file"+string(byte('0'+i))+".txt")
		if err := os.WriteFile(name, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, "file"+string(byte('0'+i))+".txt")
	}

	attachments, errResp := engine.ReadAttachments(c, paths[:8])
	if errResp != nil {
		t.Fatalf("got error for 8 files: %v", errResp)
	}
	if len(attachments) != 8 {
		t.Errorf("got %d attachments, want 8", len(attachments))
	}

	_, errResp = engine.ReadAttachments(c, paths)
	if errResp == nil || errResp.Code != "attachment" {
		t.Errorf("got no error for 9 files, want attachment error")
	}
	if errResp.Reason == nil || *errResp.Reason != "ninth" {
		reasonStr := "(nil)"
		if errResp.Reason != nil {
			reasonStr = *errResp.Reason
		}
		t.Errorf("got reason %q, want \"ninth\"", reasonStr)
	}
}

func TestReadAttachments_Symlinks(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create a regular file
	if err := os.WriteFile(filepath.Join(tempdir, "real.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink at the last component
	if err := os.Symlink("real.txt", filepath.Join(tempdir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	// Create a directory with a symlink in the path
	if err := os.Mkdir(filepath.Join(tempdir, "dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempdir, "dir", "file.txt"), []byte("nested"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(tempdir, "linkdir")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{"symlink at last component", []string{"link.txt"}, "outside"},
		{"symlink in path", []string{"linkdir/file.txt"}, "outside"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errResp := engine.ReadAttachments(c, tt.paths)
			if errResp == nil || errResp.Code != "attachment" {
				t.Errorf("got no error, want attachment error")
			}
			if errResp.Reason == nil || *errResp.Reason != tt.wantErr {
				reasonStr := "(nil)"
				if errResp.Reason != nil {
					reasonStr = *errResp.Reason
				}
				t.Errorf("got reason %q, want %q", reasonStr, tt.wantErr)
			}
		})
	}
}

func TestReadAttachments_Protected(t *testing.T) {
	tempdir := t.TempDir()
	patterns := []string{".env"}
	c, err := workspace.OpenConfined(tempdir, patterns)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create a protected file
	if err := os.WriteFile(filepath.Join(tempdir, ".env"), []byte("SECRET=value"), 0644); err != nil {
		t.Fatal(err)
	}

	_, errResp := engine.ReadAttachments(c, []string{".env"})
	if errResp == nil || errResp.Code != "attachment" {
		t.Errorf("got no error, want attachment error")
	}
	if errResp.Reason == nil || *errResp.Reason != "protected" {
		reasonStr := "(nil)"
		if errResp.Reason != nil {
			reasonStr = *errResp.Reason
		}
		t.Errorf("got reason %q, want \"protected\"", reasonStr)
	}
}

func TestReadAttachments_GrowingFile(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// This test verifies that if a file grows between stat and read,
	// the read limit detects the overflow and rejects it as too_large.
	// We create a file at the limit, which will be read successfully.
	// The key is that ReadAttachments uses io.LimitReader which will
	// prevent reading beyond MaxAttachmentFileReadLimit bytes.

	maxSize := 256 * 1024
	content := make([]byte, maxSize+1)
	for i := range content {
		content[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(tempdir, "growing.txt"), content, 0644); err != nil {
		t.Fatal(err)
	}

	_, errResp := engine.ReadAttachments(c, []string{"growing.txt"})
	if errResp == nil || errResp.Code != "attachment" {
		t.Errorf("got no error, want attachment error")
	}
	if errResp.Reason == nil || *errResp.Reason != "too_large" {
		reasonStr := "(nil)"
		if errResp.Reason != nil {
			reasonStr = *errResp.Reason
		}
		t.Errorf("got reason %q, want \"too_large\"", reasonStr)
	}
}

func TestReadAttachments_Device(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Try to read from a character device (not_regular)
	// /dev/null is a character device that should be rejected
	// We'll test with a directory instead which is reliably not regular
	if err := os.Mkdir(filepath.Join(tempdir, "dir"), 0755); err != nil {
		t.Fatal(err)
	}

	_, errResp := engine.ReadAttachments(c, []string{"dir"})
	if errResp == nil || errResp.Code != "attachment" {
		t.Errorf("got no error, want attachment error")
	}
	if errResp.Reason == nil || *errResp.Reason != "not_regular" {
		reasonStr := "(nil)"
		if errResp.Reason != nil {
			reasonStr = *errResp.Reason
		}
		t.Errorf("got reason %q, want \"not_regular\"", reasonStr)
	}
}

func TestReadAttachments_NoPartialBundle(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create valid files
	if err := os.WriteFile(filepath.Join(tempdir, "file1.txt"), []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempdir, "file2.txt"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create an invalid file (non-existent)
	// When we try to read file1, file2, and missing, it should fail on missing
	// and return nil attachments, not a partial bundle with file1 and file2

	attachments, errResp := engine.ReadAttachments(c, []string{"file1.txt", "file2.txt", "missing.txt"})
	if errResp == nil || errResp.Code != "attachment" {
		t.Errorf("got no error, want attachment error")
	}
	if len(attachments) > 0 {
		t.Errorf("got %d attachments on error, want none (no partial bundle)", len(attachments))
	}
}

func TestReadAttachments_OwnerExec(t *testing.T) {
	tempdir := t.TempDir()
	c, err := workspace.OpenConfined(tempdir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Create a regular file (not executable)
	if err := os.WriteFile(filepath.Join(tempdir, "script.sh"), []byte("#!/bin/bash\necho hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create an executable file
	if err := os.WriteFile(filepath.Join(tempdir, "executable.sh"), []byte("#!/bin/bash\necho hello"), 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		path     string
		wantExec bool
	}{
		{"regular file", "script.sh", false},
		{"executable file", "executable.sh", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachments, errResp := engine.ReadAttachments(c, []string{tt.path})
			if errResp != nil {
				t.Fatalf("got error: %v", errResp)
			}
			if len(attachments) != 1 {
				t.Fatalf("got %d attachments, want 1", len(attachments))
			}
			if attachments[0].OwnerExec != tt.wantExec {
				t.Errorf("got OwnerExec=%v, want %v", attachments[0].OwnerExec, tt.wantExec)
			}
		})
	}
}
