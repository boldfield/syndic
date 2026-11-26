package workspace

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestConfinedOpenFile_RelativePathSuccess(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	testContent := "hello world"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	f, err := c.OpenFile("test.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("content = %q, want %q", string(content), testContent)
	}
}

func TestConfinedOpenFile_NestedPathSuccess(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub", "dir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(subdir, "test.txt")
	testContent := "nested file"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	f, err := c.OpenFile("sub/dir/test.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("content = %q, want %q", string(content), testContent)
	}
}

func TestConfinedOpenFile_AbsolutePathRejected(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.OpenFile("/etc/passwd", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile with absolute path should fail")
	}
}

func TestConfinedOpenFile_DotDotTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.OpenFile("../../../etc/passwd", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile with .. traversal should fail")
	}
}

func TestConfinedOpenFile_DotDotInMiddleRejected(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.OpenFile("sub/../../../etc/passwd", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile with .. in path should fail")
	}
}

func TestConfinedOpenFile_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0644); err != nil {
		t.Fatal(err)
	}

	symlink := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.OpenFile("link.txt", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile with symlink should fail due to O_NOFOLLOW")
	}
}

func TestConfinedOpenFile_SymlinkInPathRejected(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}

	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(symlink, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.OpenFile("link/test.txt", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile through symlink component should fail due to O_NOFOLLOW")
	}
}

func TestConfinedReadFile_Success(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	testContent := "hello world"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	content, err := c.ReadFile("test.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("content = %q, want %q", string(content), testContent)
	}
}

func TestConfinedWriteFile_Success(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	testContent := "hello world"
	if err := c.WriteFile("test.txt", []byte(testContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Verify the file was written
	content, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("ReadFile from filesystem: %v", err)
	}

	if string(content) != testContent {
		t.Errorf("content = %q, want %q", string(content), testContent)
	}
}

func TestConfinedStat_Success(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	testContent := "hello world"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	fi, err := c.Stat("test.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if fi.Name() != "test.txt" {
		t.Errorf("filename = %q, want test.txt", fi.Name())
	}

	if fi.Size() != int64(len(testContent)) {
		t.Errorf("size = %d, want %d", fi.Size(), len(testContent))
	}
}

func TestConfinedProtectedPaths_DotGit(t *testing.T) {
	dir := t.TempDir()
	gitdir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitdir, 0755); err != nil {
		t.Fatal(err)
	}

	config := filepath.Join(gitdir, "config")
	if err := os.WriteFile(config, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{".git/"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.ReadFile(".git/config")
	if err == nil {
		t.Error("ReadFile .git/config should be rejected by protected paths")
	}
}

func TestConfinedProtectedPaths_DotEnv(t *testing.T) {
	dir := t.TempDir()
	envfile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envfile, []byte("SECRET=value"), 0644); err != nil {
		t.Fatal(err)
	}

	envlocal := filepath.Join(dir, ".env.local")
	if err := os.WriteFile(envlocal, []byte("SECRET=local"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{".env*"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.ReadFile(".env")
	if err == nil {
		t.Error("ReadFile .env should be rejected by protected paths")
	}

	_, err = c.ReadFile(".env.local")
	if err == nil {
		t.Error("ReadFile .env.local should be rejected by protected paths")
	}
}

func TestConfinedProtectedPaths_NestedPattern(t *testing.T) {
	dir := t.TempDir()
	secretdir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretdir, 0755); err != nil {
		t.Fatal(err)
	}

	secret := filepath.Join(secretdir, "password.txt")
	if err := os.WriteFile(secret, []byte("password123"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{"secrets/**"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.ReadFile("secrets/password.txt")
	if err == nil {
		t.Error("ReadFile secrets/password.txt should be rejected by protected paths")
	}
}

func TestConfinedProtectedPaths_AllowedFile(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "public.txt")
	if err := os.WriteFile(testFile, []byte("public"), 0644); err != nil {
		t.Fatal(err)
	}

	secretfile := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretfile, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{".git/", "secret.txt"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	content, err := c.ReadFile("public.txt")
	if err != nil {
		t.Fatalf("ReadFile public.txt should work: %v", err)
	}

	if string(content) != "public" {
		t.Errorf("content = %q, want public", string(content))
	}

	_, err = c.ReadFile("secret.txt")
	if err == nil {
		t.Error("ReadFile secret.txt should be rejected by protected paths")
	}
}

func TestConfinedEmptyRootPath(t *testing.T) {
	_, err := OpenConfined("", nil)
	if err == nil {
		t.Error("OpenConfined with empty root should fail")
	}
}

func TestConfinedEmptyFilePath(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.OpenFile("", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile with empty path should fail")
	}
}

func TestConfinedDotPath(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// A path of just "." should not work since we can't open the directory itself as a file
	_, err = c.OpenFile(".", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile with . should not open a directory successfully")
	}
}

func TestConfinedWriteFileProtected(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenConfined(dir, []string{".syndic/"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	err = c.WriteFile(".syndic/state.db", []byte("state"), 0644)
	if err == nil {
		t.Error("WriteFile to protected path should fail")
	}
}

func TestConfinedStatProtected(t *testing.T) {
	dir := t.TempDir()
	gitdir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitdir, 0755); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{".git/"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.Stat(".git")
	if err == nil {
		t.Error("Stat on protected path should fail")
	}
}

func TestConfinedMultiplePatterns(t *testing.T) {
	dir := t.TempDir()

	// Create various files
	files := map[string]string{
		".git/config":     "git config",
		".env":            "env",
		"secrets/api.key": "api key",
		"public.txt":      "public",
		"src/main.go":     "main",
	}

	for path, content := range files {
		fullpath := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(fullpath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullpath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	c, err := OpenConfined(dir, []string{".git/", ".env", "secrets/**", "*.key"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Should fail
	failCases := []string{".git/config", ".env", "secrets/api.key"}
	for _, path := range failCases {
		_, err := c.ReadFile(path)
		if err == nil {
			t.Errorf("ReadFile %q should fail (protected)", path)
		}
	}

	// Should succeed
	successCases := []string{"public.txt", "src/main.go"}
	for _, path := range successCases {
		_, err := c.ReadFile(path)
		if err != nil {
			t.Errorf("ReadFile %q should succeed: %v", path, err)
		}
	}
}

// Regression test for issue 1: fd 0 closure
// Tests that multiple consecutive OpenFile calls don't close stdin/fd 0
func TestConfinedOpenFile_FD0NotClosed(t *testing.T) {
	dir := t.TempDir()

	// Create multiple test files
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("content %d", i)), 0644); err != nil {
			t.Fatal(err)
		}
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Open files multiple times and read from each
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("file%d.txt", i)
		f, err := c.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("OpenFile %s: %v", path, err)
		}
		defer func() { _ = f.Close() }()

		// Try to stat stdin (fd 0) to ensure it's still valid
		var stat unix.Stat_t
		if err := unix.Fstat(0, &stat); err != nil {
			t.Errorf("After opening %s, fd 0 is bad: %v", path, err)
		}
	}
}

// Regression test for issue 2: protected patterns miss top-level private keys
// Tests that **/*.key pattern protects both top-level and nested .key files
func TestConfinedProtectedPaths_TopLevelPrivateKeys(t *testing.T) {
	dir := t.TempDir()

	// Create top-level and nested private key files
	topLevelKey := filepath.Join(dir, "server.key")
	if err := os.WriteFile(topLevelKey, []byte("secret key"), 0644); err != nil {
		t.Fatal(err)
	}

	nestedDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}
	nestedKey := filepath.Join(nestedDir, "app.key")
	if err := os.WriteFile(nestedKey, []byte("app secret"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{"**/*.key", "**/*.pem", "**/id_rsa", "**/id_dsa", "**/id_ecdsa", "**/id_ed25519", "**/id_ecdsa_sk", "**/id_ed25519_sk"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Both top-level and nested .key files should be protected
	_, err = c.ReadFile("server.key")
	if err == nil {
		t.Error("ReadFile server.key should be protected (top-level)")
	}

	_, err = c.ReadFile("config/app.key")
	if err == nil {
		t.Error("ReadFile config/app.key should be protected (nested)")
	}

	// Test pem and id_rsa patterns
	pemFile := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(pemFile, []byte("cert"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = c.ReadFile("cert.pem")
	if err == nil {
		t.Error("ReadFile cert.pem should be protected (top-level)")
	}

	idFile := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(idFile, []byte("key"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = c.ReadFile("id_rsa")
	if err == nil {
		t.Error("ReadFile id_rsa should be protected (top-level)")
	}
}

// Regression test for issue 3: ? glob matches /
// Tests that ? glob only matches non-slash characters
func TestConfinedProtectedPaths_QuestionMarkGlob(t *testing.T) {
	dir := t.TempDir()

	// Create files that should/shouldn't match a?b pattern
	// a?b should match aXb where X is a single non-slash character
	// but NOT match a/b

	nestedDir := filepath.Join(dir, "a")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	bFile := filepath.Join(nestedDir, "b")
	if err := os.WriteFile(bFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	cFile := filepath.Join(dir, "aXb")
	if err := os.WriteFile(cFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{"a?b"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// a/b should NOT be protected by a?b pattern (? doesn't match /)
	_, err = c.ReadFile("a/b")
	if err != nil {
		t.Errorf("ReadFile a/b should succeed (? doesn't match /): %v", err)
	}

	// aXb should be protected by a?b pattern
	_, err = c.ReadFile("aXb")
	if err == nil {
		t.Error("ReadFile aXb should fail (matches a?b pattern)")
	}
}

// Regression test for rooted protection patterns
// Tests that patterns starting with / protect only top-level files, not nested ones
func TestConfinedProtectedPaths_RootAnchoredPatterns(t *testing.T) {
	dir := t.TempDir()

	// Create top-level and nested secret files
	topLevelSecret := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(topLevelSecret, []byte("API_KEY=secret"), 0644); err != nil {
		t.Fatal(err)
	}

	nestedDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}
	nestedSecret := filepath.Join(nestedDir, "secrets.env")
	if err := os.WriteFile(nestedSecret, []byte("API_KEY=nested"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, []string{"/secrets.env"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// /secrets.env should protect only top-level secrets.env
	_, err = c.ReadFile("secrets.env")
	if err == nil {
		t.Error("ReadFile secrets.env should be rejected (protected by /secrets.env)")
	}

	// /secrets.env should NOT protect nested config/secrets.env
	_, err = c.ReadFile("config/secrets.env")
	if err != nil {
		t.Errorf("ReadFile config/secrets.env should succeed (not protected): %v", err)
	}

	// Test another rooted pattern: /top should only protect top/, not sub/top/
	topDir := filepath.Join(dir, "top")
	if err := os.MkdirAll(topDir, 0755); err != nil {
		t.Fatal(err)
	}
	topFile := filepath.Join(topDir, "config.txt")
	if err := os.WriteFile(topFile, []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(dir, "sub", "top")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(subDir, "config.txt")
	if err := os.WriteFile(subFile, []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}

	c2, err := OpenConfined(dir, []string{"/top/"})
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c2.Close() }()

	// /top/ should protect top/config.txt
	_, err = c2.ReadFile("top/config.txt")
	if err == nil {
		t.Error("ReadFile top/config.txt should be rejected (protected by /top/)")
	}

	// /top/ should NOT protect sub/top/config.txt
	_, err = c2.ReadFile("sub/top/config.txt")
	if err != nil {
		t.Errorf("ReadFile sub/top/config.txt should succeed (not protected): %v", err)
	}
}

// Regression test for issue 4: non-regular files aren't refused
// Tests that FIFOs, directories, and other non-regular files are rejected
func TestConfinedOpenFile_NonRegularFilesRejected(t *testing.T) {
	dir := t.TempDir()

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Test directory rejection
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	_, err = c.OpenFile("subdir", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile on directory should fail")
	}

	// Test FIFO rejection - create a FIFO
	fifoPath := filepath.Join(dir, "testfifo")
	if err := unix.Mkfifo(fifoPath, 0644); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	// Try to open FIFO with O_NONBLOCK to avoid blocking
	_, err = c.OpenFile("testfifo", os.O_RDONLY, 0)
	if err == nil {
		t.Error("OpenFile on FIFO should fail")
	}
}

// Regression test: WriteFile on FIFO must not hang
// Tests that opening a FIFO with O_WRONLY and O_NONBLOCK returns ENXIO
// and the fstat check rejects it before any blocking can occur
func TestConfinedOpenFile_WriteFIFODoesNotHang(t *testing.T) {
	dir := t.TempDir()

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Create a FIFO
	fifoPath := filepath.Join(dir, "testfifo")
	if err := unix.Mkfifo(fifoPath, 0644); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	// Try to write to the FIFO - with O_NONBLOCK, openat should fail with ENXIO
	// if there's no reader. This should not hang.
	done := make(chan error, 1)
	go func() {
		err := c.WriteFile("testfifo", []byte("data"), 0644)
		done <- err
	}()

	// Wait up to 2 seconds for the write to complete
	select {
	case err := <-done:
		if err == nil {
			t.Error("WriteFile to FIFO should fail")
		}
	case <-time.After(2 * time.Second):
		t.Error("WriteFile to FIFO hung (should fail with ENXIO, not block)")
	}
}

// A path component swapped for a symlink after its parent descriptor is held
// and before the next openat must not escape: the walk fails closed and never
// yields the sentinel at the exact requested suffix outside the root.
func TestConfinedOpenFile_SymlinkSwapInFlight(t *testing.T) {
	for _, swap := range []string{"level2", "file.txt"} {
		t.Run(swap, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "level1", "level2"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "level1", "level2", "file.txt"), []byte("original"), 0644); err != nil {
				t.Fatal(err)
			}

			// The sentinel lives at the exact suffix the walk will request next.
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(outside, "level2"), 0755); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"file.txt", filepath.Join("level2", "file.txt")} {
				if err := os.WriteFile(filepath.Join(outside, p), []byte("outside"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			c, err := OpenConfined(dir, nil)
			if err != nil {
				t.Fatalf("OpenConfined: %v", err)
			}
			defer func() { _ = c.Close() }()

			swapped := false
			c.afterComponent = func(i int, name string) {
				// Swap the next component once its parent descriptor is held.
				var victim, target string
				switch {
				case swap == "level2" && name == "level1":
					victim = filepath.Join(dir, "level1", "level2")
					target = filepath.Join(outside, "level2")
				case swap == "file.txt" && name == "level2":
					victim = filepath.Join(dir, "level1", "level2", "file.txt")
					target = filepath.Join(outside, "file.txt")
				default:
					return
				}
				if err := os.RemoveAll(victim); err != nil {
					t.Errorf("remove %s: %v", victim, err)
				}
				if err := os.Symlink(target, victim); err != nil {
					t.Errorf("symlink %s: %v", victim, err)
				}
				swapped = true
			}

			f, err := c.OpenFile("level1/level2/file.txt", os.O_RDONLY, 0)
			if !swapped {
				t.Fatal("swap hook never ran")
			}
			if err == nil {
				content, _ := io.ReadAll(f)
				_ = f.Close()
				t.Fatalf("open through swapped symlink succeeded with %q", content)
			}
			if !errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
				t.Fatalf("err = %v, want ELOOP or ENOTDIR", err)
			}
		})
	}
}

// Regression test for root relocation identity
// Tests that operations through the same root fd give consistent results even after root is moved
func TestConfinedOpenFile_RootRelocationIdentity(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	testContent := "original content"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Verify we can read the file through the confined instance
	content, err := c.ReadFile("test.txt")
	if err != nil {
		t.Fatalf("Initial read: %v", err)
	}
	if string(content) != testContent {
		t.Errorf("Initial read content mismatch: %q != %q", string(content), testContent)
	}

	// Now rename the root directory to a new location
	tempParent := t.TempDir()
	newRoot := filepath.Join(tempParent, "moved_root")
	if err := os.Rename(dir, newRoot); err != nil {
		t.Fatalf("Rename root: %v", err)
	}

	// Operations through the same root fd should still work and affect the moved directory
	writeData := "written after relocation"
	if err := c.WriteFile("written.txt", []byte(writeData), 0644); err != nil {
		t.Fatalf("Write after relocation: %v", err)
	}

	// Verify the written file exists in the moved location
	writtenPath := filepath.Join(newRoot, "written.txt")
	if _, err := os.Stat(writtenPath); err != nil {
		t.Fatalf("Written file not found in moved location: %v", err)
	}

	// Read the written file through the filesystem to verify it's in the right place
	diskContent, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatalf("Read written file from disk: %v", err)
	}
	if string(diskContent) != writeData {
		t.Errorf("Written content mismatch: %q != %q", string(diskContent), writeData)
	}

	// Verify we can still read through the confined instance
	readBack, err := c.ReadFile("written.txt")
	if err != nil {
		t.Fatalf("Read back after relocation: %v", err)
	}
	if string(readBack) != writeData {
		t.Errorf("Read back content mismatch: %q != %q", string(readBack), writeData)
	}
}

func TestExamineFileDirAndRoot(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"a.txt": "A", "sub/b.txt": "B", ".env": "S"} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	c, err := OpenConfined(root, []string{".env*"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	ex, err := c.Examine("a.txt")
	if err != nil || ex.IsDir || string(ex.Content) != "A" {
		t.Fatalf("file: %+v, %v", ex, err)
	}
	ex, err = c.Examine(".")
	if err != nil || !ex.IsDir {
		t.Fatalf("root: %+v, %v", ex, err)
	}
	types := map[string]string{}
	for _, e := range ex.Entries {
		types[e.Name] = e.Type
	}
	if len(types) != 3 || types["a.txt"] != "f" || types["sub"] != "d" || types["link"] != "l" {
		t.Fatalf("entries %v (protected .env must be left out)", types)
	}
	if _, err := c.Examine("nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := c.Examine("link"); err == nil {
		t.Fatal("symlink must be refused")
	}
	if _, err := c.Examine(".env"); err == nil {
		t.Fatal("protected path must be refused")
	}
	if _, err := c.Examine("../x"); err == nil {
		t.Fatal("escape must be refused")
	}
	if !c.IsProtected(".env") || c.IsProtected("a.txt") {
		t.Fatal("IsProtected")
	}
}
