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
		name    string
		paths   []string
		wantErr bool
		errCode string
	}{
		{"empty list", []string{}, false, ""},
		{"absolute path", []string{"/etc/passwd"}, true, "attachment"},
		{"parent traversal", []string{"../etc/passwd"}, true, "attachment"},
		{"empty component", []string{"a//b.txt"}, true, "attachment"},
		{"dot component", []string{"a/./b.txt"}, true, "attachment"},
		{"trailing slash", []string{"a/b/"}, true, "attachment"},
		{"control character", []string{"a\x00b.txt"}, true, "attachment"},
		{"duplicate paths", []string{"file.txt", "file.txt"}, true, "attachment"},
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
			if tt.wantErr && errResp.Code != tt.errCode {
				t.Errorf("got error code %q, want %q", errResp.Code, tt.errCode)
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

	// Create files for testing total size
	// 4 files of 250 KiB = 1 MiB exactly (should pass)
	// 5 files of 250 KiB = 1.25 MiB (should fail)
	fileSize := 250 * 1024 // 250 KiB each
	for i := 0; i < 5; i++ {
		name := filepath.Join(tempdir, fmt.Sprintf("file%d.txt", i))
		content := make([]byte, fileSize)
		for j := range content {
			content[j] = byte('a' + i%26)
		}
		if err := os.WriteFile(name, content, 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{"at total limit (4 files)", []string{"file0.txt", "file1.txt", "file2.txt", "file3.txt"}, ""},
		{"over total limit (5 files)", []string{"file0.txt", "file1.txt", "file2.txt", "file3.txt", "file4.txt"}, "total_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attachments, errResp := engine.ReadAttachments(c, tt.paths)
			if (errResp != nil) != (tt.wantErr != "") {
				if errResp != nil {
					t.Errorf("got error %q: %s, want %q", errResp.Code, *errResp.Detail, tt.wantErr)
				} else {
					t.Errorf("got no error, want %q", tt.wantErr)
				}
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
}
