package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const designDocsPara = "Process"

func init() {
	cover(designDocsPara, "a pull request modifying a design document other than open-questions.md fails the design immutability check", "TestDesignDocs_ProtectedDocsFail")
	cover(designDocsPara, "a pull request modifying only open-questions.md passes the design immutability check", "TestDesignDocs_OpenQuestionsPasses")
	cover(designDocsPara, "a pull request with no design document changes passes the design immutability check", "TestDesignDocs_NoChangesPass")
	cover(designDocsPara, "the design immutability check fails when the base ref cannot be resolved", "TestDesignDocs_UnresolvableBaseRefFails")
	cover(designDocsPara, "a pull request modifying both open-questions.md and a protected doc fails the design immutability check", "TestDesignDocs_MixedChangeFails")
	cover(designDocsPara, "files similar to open-questions.md are caught as protected documents", "TestDesignDocs_SimilarFilesFail")
}

func TestDesignDocs_ProtectedDocsFail(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	// Set up a bare repo to act as origin
	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	// Copy design files using small fixtures instead of live tree
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v4-delta-engine-tui.md"), []byte("# Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write design file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	// Switch to a feature branch
	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	// Modify a protected design doc
	docFile := filepath.Join(testRepo, "docs/design/v4-delta-engine-tui.md")
	content, err := os.ReadFile(docFile)
	if err != nil {
		t.Fatalf("Failed to read design doc: %v", err)
	}
	if err := os.WriteFile(docFile, append(content, []byte("\nTest modification\n")...), 0644); err != nil {
		t.Fatalf("Failed to write design doc: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "docs/design/v4-delta-engine-tui.md")
	runCmd(t, testRepo, "git", "commit", "-m", "Modify design")

	// Run the check - should fail
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail but it succeeded")
	}
}

func TestDesignDocs_OpenQuestionsPasses(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	// Set up a bare repo to act as origin
	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	// Create design files with fixtures
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v4-delta-engine-tui.md"), []byte("# Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write design file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	// Switch to a feature branch
	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	// Modify only open-questions.md
	docFile := filepath.Join(testRepo, "docs/design/open-questions.md")
	content, err := os.ReadFile(docFile)
	if err != nil {
		t.Fatalf("Failed to read open questions: %v", err)
	}
	if err := os.WriteFile(docFile, append(content, []byte("\n# New question\n")...), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "docs/design/open-questions.md")
	runCmd(t, testRepo, "git", "commit", "-m", "Update open questions")

	// Run the check - should pass
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err != nil {
		t.Fatalf("Expected script to succeed but got error: %v", err)
	}
}

func TestDesignDocs_NoChangesPass(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	// Set up a bare repo to act as origin
	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	// Create a minimal repo
	if err := os.MkdirAll(filepath.Join(testRepo, "docs/design"), 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testRepo, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}
	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	// Switch to a feature branch
	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	// Run the check - should pass (no changes)
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err != nil {
		t.Fatalf("Expected script to succeed but got error: %v", err)
	}
}

func TestDesignDocs_UnresolvableBaseRefFails(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	initGitRepo(t, testRepo)

	// Create design files
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v4-delta.md"), []byte("# Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write design file: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")

	// Run the check with unresolvable base ref - should fail
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=definitely-missing-ref")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail with unresolvable base ref but it succeeded")
	}
}

func TestDesignDocs_MixedChangeFails(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	// Set up a bare repo to act as origin
	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	// Create design files
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v4-delta.md"), []byte("# Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write design file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	// Switch to a feature branch and modify both files
	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "v4-delta.md"), []byte("# Design\nModified\n"), 0644); err != nil {
		t.Fatalf("Failed to write design file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\nNew Q\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Update both files")

	// Run the check - should fail because protected doc was modified
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail on mixed changes but it succeeded")
	}
}

func TestDesignDocs_SimilarFilesFail(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	// Set up a bare repo to act as origin
	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	// Create design files including similar ones
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md.backup"), []byte("# Backup\n"), 0644); err != nil {
		t.Fatalf("Failed to write backup: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	// Switch to a feature branch and modify the backup file
	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md.backup"), []byte("# Backup\nModified\n"), 0644); err != nil {
		t.Fatalf("Failed to write backup: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Modify backup")

	// Run the check - should fail because the similar file was modified
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail on similar file modification but it succeeded")
	}
}

func initGitRepo(t *testing.T, dir string) {
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for wd != "/" {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		wd = filepath.Dir(wd)
	}
	return "."
}

func runCmd(t *testing.T, dir string, args ...string) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("Command failed: %v: %v", args, err)
	}
}
