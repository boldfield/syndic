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
	cover(designDocsPara, "the design immutability check fails when git diff fails due to unrelated histories", "TestDesignDocs_DiffFailsUnrelatedHistories")
	cover(designDocsPara, "adding one new file under docs/design/ and nothing else passes", "TestDesignDocs_AddingSingleRatificationPasses")
	cover(designDocsPara, "adding two new files under docs/design/ and nothing else passes", "TestDesignDocs_AddingMultipleRatificationPasses")
	cover(designDocsPara, "adding a new file under docs/design/ together with a change to open-questions.md passes", "TestDesignDocs_AddingRatificationWithOpenQuestionsPasses")
	cover(designDocsPara, "adding a new file under docs/design/ together with any file outside docs/design/ fails", "TestDesignDocs_AddingRatificationWithExternalChangesFails")
	cover(designDocsPara, "modifying an existing design document fails", "TestDesignDocs_ModifyingDesignDocFails")
	cover(designDocsPara, "deleting an existing design document fails", "TestDesignDocs_DeletingDesignDocFails")
	cover(designDocsPara, "renaming an existing design document fails", "TestDesignDocs_RenamingDesignDocFails")
	cover(designDocsPara, "adding a new file under docs/design/ in a subdirectory passes when nothing else changes", "TestDesignDocs_AddingSubdirectoryRatificationPasses")
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

func TestDesignDocs_DiffFailsUnrelatedHistories(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	// Set up a bare repo to act as origin with main branch
	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	// Create initial main branch
	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v4-delta.md"), []byte("# Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write design file: %v", err)
	}
	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial main commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	// Create an orphan branch (unrelated history)
	runCmd(t, testRepo, "git", "checkout", "--orphan", "feature")
	runCmd(t, testRepo, "git", "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(testRepo, "README.md"), []byte("# Feature\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}
	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Feature orphan commit")

	// Run the check - should fail because there is no merge base
	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail with unrelated histories but it succeeded")
	}
}

func TestDesignDocs_AddingSingleRatificationPasses(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "v6-new.md"), []byte("# New Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write new design: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "docs/design/v6-new.md")
	runCmd(t, testRepo, "git", "commit", "-m", "Add new design")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err != nil {
		t.Fatalf("Expected script to succeed for ratification but got error: %v", err)
	}
}

func TestDesignDocs_AddingMultipleRatificationPasses(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "v6-new.md"), []byte("# New Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write new design: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v6-another.md"), []byte("# Another Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write another design: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "docs/design/v6-new.md")
	runCmd(t, testRepo, "git", "add", "docs/design/v6-another.md")
	runCmd(t, testRepo, "git", "commit", "-m", "Add multiple new designs")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err != nil {
		t.Fatalf("Expected script to succeed for multiple ratification but got error: %v", err)
	}
}

func TestDesignDocs_AddingRatificationWithOpenQuestionsPasses(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "v6-new.md"), []byte("# New Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write new design: %v", err)
	}
	oqFile := filepath.Join(designDir, "open-questions.md")
	content, err := os.ReadFile(oqFile)
	if err != nil {
		t.Fatalf("Failed to read open questions: %v", err)
	}
	if err := os.WriteFile(oqFile, append(content, []byte("\n# New question\n")...), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}
	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Add design and update questions")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err != nil {
		t.Fatalf("Expected script to succeed for ratification with open-questions change but got error: %v", err)
	}
}

func TestDesignDocs_AddingRatificationWithExternalChangesFails(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testRepo, "README.md"), []byte("# Readme\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "v6-new.md"), []byte("# New Design\n"), 0644); err != nil {
		t.Fatalf("Failed to write new design: %v", err)
	}
	readmeFile := filepath.Join(testRepo, "README.md")
	content, err := os.ReadFile(readmeFile)
	if err != nil {
		t.Fatalf("Failed to read README: %v", err)
	}
	if err := os.WriteFile(readmeFile, append(content, []byte("\nUpdated\n")...), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}
	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Add design and update readme")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail for ratification with external changes but it succeeded")
	}
}

func TestDesignDocs_ModifyingDesignDocFails(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v6-existing.md"), []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("Failed to write existing design: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.WriteFile(filepath.Join(designDir, "v6-existing.md"), []byte("# Existing\nModified\n"), 0644); err != nil {
		t.Fatalf("Failed to modify design: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "docs/design/v6-existing.md")
	runCmd(t, testRepo, "git", "commit", "-m", "Modify design")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail for modifying design doc but it succeeded")
	}
}

func TestDesignDocs_DeletingDesignDocFails(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v6-existing.md"), []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("Failed to write existing design: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.Remove(filepath.Join(designDir, "v6-existing.md")); err != nil {
		t.Fatalf("Failed to delete design: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "-A")
	runCmd(t, testRepo, "git", "commit", "-m", "Delete design")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail for deleting design doc but it succeeded")
	}
}

func TestDesignDocs_RenamingDesignDocFails(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "v6-old.md"), []byte("# Old\n"), 0644); err != nil {
		t.Fatalf("Failed to write old design: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	if err := os.Rename(filepath.Join(designDir, "v6-old.md"), filepath.Join(designDir, "v6-new.md")); err != nil {
		t.Fatalf("Failed to rename design: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "-A")
	runCmd(t, testRepo, "git", "commit", "-m", "Rename design")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err == nil {
		t.Fatal("Expected script to fail for renaming design doc but it succeeded")
	}
}

func TestDesignDocs_AddingSubdirectoryRatificationPasses(t *testing.T) {
	t.Parallel()
	testRepo := t.TempDir()

	bareRepo := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bareRepo, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bareRepo, "git", "init", "--bare")

	initGitRepo(t, testRepo)
	runCmd(t, testRepo, "git", "remote", "add", "origin", bareRepo)

	designDir := filepath.Join(testRepo, "docs/design")
	if err := os.MkdirAll(designDir, 0755); err != nil {
		t.Fatalf("Failed to create design dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "open-questions.md"), []byte("# Questions\n"), 0644); err != nil {
		t.Fatalf("Failed to write open questions: %v", err)
	}

	runCmd(t, testRepo, "git", "add", ".")
	runCmd(t, testRepo, "git", "commit", "-m", "Initial commit")
	runCmd(t, testRepo, "git", "push", "origin", "HEAD:main")

	runCmd(t, testRepo, "git", "checkout", "-b", "feature")

	subDir := filepath.Join(designDir, "v6")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "new.md"), []byte("# New Design in subdir\n"), 0644); err != nil {
		t.Fatalf("Failed to write new design: %v", err)
	}
	runCmd(t, testRepo, "git", "add", "docs/design/v6/new.md")
	runCmd(t, testRepo, "git", "commit", "-m", "Add design in subdirectory")

	scriptPath := filepath.Join(repoRoot(), "scripts/check-design-immutability.sh")
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = testRepo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")

	if err := cmd.Run(); err != nil {
		t.Fatalf("Expected script to succeed for ratification in subdirectory but got error: %v", err)
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
