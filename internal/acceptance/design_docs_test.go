package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const designDocsPara = "Design documents"

func init() {
	cover(designDocsPara, "a pull request modifying a design document other than open-questions.md fails the design immutability check", "TestDesignDocs_ProtectedDocsFail")
	cover(designDocsPara, "a pull request modifying only open-questions.md passes the design immutability check", "TestDesignDocs_OpenQuestionsPasses")
	cover(designDocsPara, "a pull request with no design document changes passes the design immutability check", "TestDesignDocs_NoChangesPass")
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

	// Copy design files
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	sourceDir := filepath.Join(repoRoot(), "docs/design")
	runCmd(t, testRepo, "bash", "-c", "cp -r "+sourceDir+"/* "+designDir+"/")

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

	// Copy design files
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	sourceDir := filepath.Join(repoRoot(), "docs/design")
	runCmd(t, testRepo, "bash", "-c", "cp -r "+sourceDir+"/* "+designDir+"/")

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

func initGitRepo(t *testing.T, dir string) {
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")
}

func repoRoot() string {
	wd, _ := os.Getwd()
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
