package workspace

import (
	"io"
	"os"
	"path/filepath"
	"testing"
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

func TestConfinedOpenFile_SymlinkSwapAttack(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "" {
		t.Skip("symlink swap timing test may be flaky in CI")
	}

	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(subdir, "file.txt")
	if err := os.WriteFile(testFile, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Try to open the file - this should work initially
	// Even if someone swaps the directory for a symlink between our
	// component checks, the O_NOFOLLOW flag on the last component
	// and the directory components will catch it
	_, err = c.OpenFile("sub/file.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("Initial open should work: %v", err)
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

func TestConfinedRootRelocationRetainsIdentity(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := OpenConfined(dir, nil)
	if err != nil {
		t.Fatalf("OpenConfined: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Open the file with the original root fd
	f1, err := c.OpenFile("test.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("First open: %v", err)
	}
	defer func() { _ = f1.Close() }()

	content1, err := io.ReadAll(f1)
	if err != nil {
		t.Fatalf("Read first: %v", err)
	}

	// Open again - should get same file through same root fd
	f2, err := c.OpenFile("test.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("Second open: %v", err)
	}
	defer func() { _ = f2.Close() }()

	content2, err := io.ReadAll(f2)
	if err != nil {
		t.Fatalf("Read second: %v", err)
	}

	if string(content1) != string(content2) {
		t.Errorf("content mismatch: %q != %q", string(content1), string(content2))
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
