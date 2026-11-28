package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/boldfield/syndic/internal/workspace"
)

func attachFixture(t *testing.T, patterns ...string) (string, *workspace.Confined) {
	t.Helper()
	dir := t.TempDir()
	c, err := workspace.OpenConfined(dir, patterns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return dir, c
}

func attachPut(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantAttachment(t *testing.T, werr *wire.Error, path, reason string) {
	t.Helper()
	if werr == nil {
		t.Fatalf("no refusal, want %s %s", path, reason)
	}
	if werr.Code != wire.ErrorCodeAttachment || werr.Path == nil || werr.Reason == nil || *werr.Path != path || *werr.Reason != reason {
		t.Fatalf("refusal = %+v (path %v reason %v), want attachment %q %q", werr, werr.Path, werr.Reason, path, reason)
	}
}

func TestReadAttachmentsPathForm(t *testing.T) {
	_, c := attachFixture(t)
	for _, p := range []string{"", "/abs", "../x", "a/../b", "a//b", "./a", "a/./b", "a/", "a\x00b", "a\nb", "a\u0085b", "a\x7fb", "a\xffb"} {
		got, werr := ReadAttachments(c, []string{p})
		if werr == nil || werr.Code != wire.ErrorCodeAttachment || *werr.Reason != attachForm || got != nil {
			t.Errorf("%q: got %v %+v", p, got, werr)
		}
	}
}

func TestReadAttachmentsDuplicateAndCount(t *testing.T) {
	dir, c := attachFixture(t)
	attachPut(t, dir, "a.txt", []byte("a"))
	got, werr := ReadAttachments(c, []string{"a.txt", "a.txt"})
	if werr == nil || *werr.Reason != attachDuplicate || *werr.Path != "a.txt" || got != nil {
		t.Fatalf("duplicate: %v %+v", got, werr)
	}
	var paths []string
	for i := 0; i < MaxAttachments; i++ {
		n := fmt.Sprintf("f%d", i)
		attachPut(t, dir, n, []byte("x"))
		paths = append(paths, n)
	}
	if got, werr := ReadAttachments(c, paths); werr != nil || len(got) != MaxAttachments {
		t.Fatalf("eight: %d %+v", len(got), werr)
	}
	attachPut(t, dir, "f8", []byte("x"))
	if _, werr := ReadAttachments(c, append(paths, "f8")); werr == nil || werr.Code != wire.ErrorCodeBadRequest {
		t.Fatalf("nine: %+v", werr)
	}
}

func TestReadAttachmentsLoadsInListedOrderWithExec(t *testing.T) {
	dir, c := attachFixture(t)
	attachPut(t, dir, "b.txt", []byte("bee"))
	attachPut(t, dir, "sub/a.sh", []byte("#!/bin/sh\n"))
	if err := os.Chmod(filepath.Join(dir, "sub/a.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, werr := ReadAttachments(c, []string{"b.txt", "sub/a.sh"})
	if werr != nil || len(got) != 2 {
		t.Fatalf("%v %+v", got, werr)
	}
	if got[0].Path != "b.txt" || string(got[0].Content) != "bee" || got[0].OwnerExec {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Path != "sub/a.sh" || !got[1].OwnerExec {
		t.Errorf("second = %+v", got[1])
	}
}

func TestReadAttachmentsRefusals(t *testing.T) {
	dir, c := attachFixture(t, ".env*")
	attachPut(t, dir, "real.txt", []byte("x"))
	attachPut(t, dir, ".env", []byte("SECRET"))
	attachPut(t, dir, "bad.bin", []byte{0xff, 0xfe})
	attachPut(t, dir, "plain", []byte("x"))
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, reason string }{
		{"missing.txt", attachNotFound},
		{"nodir/x.txt", attachNotFound},
		{"plain/child", attachNotFound},
		{"link", attachOutside},
		{"out", attachOutside},
		{"dirlink/real.txt", attachOutside},
		{".env", attachProtected},
		{"d", attachNotRegular},
		{"fifo", attachNotRegular},
		{"bad.bin", attachNotText},
	} {
		got, werr := ReadAttachments(c, []string{"real.txt", tc.path})
		if werr == nil || werr.Code != wire.ErrorCodeAttachment || *werr.Path != tc.path || *werr.Reason != tc.reason || got != nil {
			t.Errorf("%s: got %v %+v", tc.path, got, werr)
		}
	}
}

func TestReadAttachmentsFileSizeBoundary(t *testing.T) {
	dir, c := attachFixture(t)
	attachPut(t, dir, "at", bytes.Repeat([]byte("a"), MaxAttachmentFileSize))
	attachPut(t, dir, "over", bytes.Repeat([]byte("a"), MaxAttachmentFileSize+1))
	if got, werr := ReadAttachments(c, []string{"at"}); werr != nil || len(got[0].Content) != MaxAttachmentFileSize {
		t.Fatalf("at limit: %+v", werr)
	}
	_, werr := ReadAttachments(c, []string{"over"})
	wantAttachment(t, werr, "over", attachTooLarge)
}

func TestReadAttachmentsGrowingFile(t *testing.T) {
	dir, c := attachFixture(t)
	attachPut(t, dir, "g", []byte("small"))
	c.SetAfterComponent(func(int, string) {
		f, err := os.OpenFile(filepath.Join(dir, "g"), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(bytes.Repeat([]byte("z"), MaxAttachmentFileSize)); err != nil {
			t.Error(err)
		}
	})
	_, werr := ReadAttachments(c, []string{"g"})
	wantAttachment(t, werr, "g", attachTooLarge)
}

func TestReadAttachmentsTotalBoundary(t *testing.T) {
	dir, c := attachFixture(t)
	for i := 0; i < 4; i++ {
		attachPut(t, dir, fmt.Sprintf("f%d", i), bytes.Repeat([]byte("a"), MaxAttachmentFileSize))
	}
	paths := []string{"f0", "f1", "f2", "f3"}
	got, werr := ReadAttachments(c, paths)
	if werr != nil || len(got) != 4 {
		t.Fatalf("exactly 1 MiB: %+v", werr)
	}
	attachPut(t, dir, "one", []byte("x"))
	_, werr = ReadAttachments(c, append(paths, "one"))
	wantAttachment(t, werr, "one", attachTotalTooLarge)
}

func TestReadAttachmentsUnreadableIsInternal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-0 files")
	}
	dir, c := attachFixture(t)
	attachPut(t, dir, "locked", []byte("x"))
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	_, werr := ReadAttachments(c, []string{"locked"})
	if werr == nil || werr.Code != wire.ErrorCodeInternal || werr.Reason != nil {
		t.Fatalf("got %+v", werr)
	}
}

func TestAttachmentRefusalJSONShape(t *testing.T) {
	b, err := json.Marshal(attachmentRefusal("a/b", attachTooLarge))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"code":"attachment","path":"a/b","reason":"too_large"}`; !strings.Contains(string(b), want) && string(b) != want {
		t.Fatalf("json = %s, want %s", b, want)
	}
}
