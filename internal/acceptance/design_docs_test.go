package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	cover(designDocsPara, "a pull request adding one new design document passes the design immutability check", "TestDesignDocs_RatifyOneAddedPasses")
	cover(designDocsPara, "a pull request adding two new design documents passes the design immutability check", "TestDesignDocs_RatifyTwoAddedPasses")
	cover(designDocsPara, "a pull request adding a design document and changing open-questions.md passes the design immutability check", "TestDesignDocs_RatifyWithOpenQuestionsPasses")
	cover(designDocsPara, "a pull request adding a design document and a file outside docs/design fails and says so", "TestDesignDocs_RatifyWithOutsideFileFails")
	cover(designDocsPara, "a pull request modifying an existing design document fails, alone or with an added one", "TestDesignDocs_ModifyExistingFails")
	cover(designDocsPara, "a pull request deleting an existing design document fails", "TestDesignDocs_DeleteExistingFails")
	cover(designDocsPara, "a pull request renaming an existing design document fails", "TestDesignDocs_RenameExistingFails")
	cover(designDocsPara, "a pull request adding a design document in a subdirectory passes", "TestDesignDocs_RatifySubdirPasses")
	cover(designDocsPara, "a pull request changing only files outside docs/design passes, including under a nested docs/design path", "TestDesignDocs_OutsideOnlyPasses")
	cover(designDocsPara, "design documents with special characters in their names are still protected", "TestDesignDocs_SpecialNamesFail")
	cover(designDocsPara, "open-questions.md of any status is exempt next to outside changes", "TestDesignDocs_OpenQuestionsAnyStatusExempt")
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

// designCheck builds a repository whose origin/main holds base (path ->
// content), applies change on a feature branch (a nil content deletes the
// path), commits, and runs the real script. It returns the combined output
// and whether the script exited zero.
func designCheck(t *testing.T, base map[string]string, change func(repo string)) (string, bool) {
	t.Helper()
	repo := t.TempDir()
	bare := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(bare, 0755); err != nil {
		t.Fatalf("Failed to create bare repo dir: %v", err)
	}
	runCmd(t, bare, "git", "init", "--bare")
	initGitRepo(t, repo)
	runCmd(t, repo, "git", "remote", "add", "origin", bare)
	for path, content := range base {
		writeDesignFile(t, repo, path, content)
	}
	writeDesignFile(t, repo, "README", "readme\n")
	runCmd(t, repo, "git", "add", ".")
	runCmd(t, repo, "git", "commit", "-m", "Initial commit")
	runCmd(t, repo, "git", "push", "origin", "HEAD:main")
	runCmd(t, repo, "git", "checkout", "-b", "feature")
	change(repo)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "Change")

	cmd := exec.Command("bash", filepath.Join(repoRoot(), "scripts/check-design-immutability.sh"))
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func writeDesignFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write %s: %v", path, err)
	}
}

func expectDesignPass(t *testing.T, out string, ok bool) {
	t.Helper()
	if !ok {
		t.Fatalf("Expected script to pass but it failed:\n%s", out)
	}
}

func expectDesignFail(t *testing.T, out string, ok bool, want ...string) {
	t.Helper()
	if ok {
		t.Fatalf("Expected script to fail but it passed:\n%s", out)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("Expected output to contain %q, got:\n%s", w, out)
		}
	}
}

const (
	existingDoc = "docs/design/existing.md"
	oqDoc       = "docs/design/open-questions.md"
)

var existingBase = map[string]string{existingDoc: "# Existing\n", oqDoc: "# Questions\n"}

func TestDesignDocs_RatifyOneAddedPasses(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
	})
	expectDesignPass(t, out, ok)
}

func TestDesignDocs_RatifyTwoAddedPasses(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
		writeDesignFile(t, r, "docs/design/v7.md", "# v7\n")
	})
	expectDesignPass(t, out, ok)
}

func TestDesignDocs_RatifyWithOpenQuestionsPasses(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
		writeDesignFile(t, r, oqDoc, "# Questions\nmore\n")
	})
	expectDesignPass(t, out, ok)
}

func TestDesignDocs_RatifyWithOutsideFileFails(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
		writeDesignFile(t, r, "README", "changed\n")
	})
	expectDesignFail(t, out, ok, "A docs/design/v6.md", "ratification pull request must contain only design documents")

	out, ok = designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
		writeDesignFile(t, r, "newfile.txt", "x\n")
	})
	expectDesignFail(t, out, ok, "A docs/design/v6.md")
}

func TestDesignDocs_ModifyExistingFails(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, existingDoc, "# Existing\nmodified\n")
	})
	expectDesignFail(t, out, ok, "M docs/design/existing.md",
		"ERROR: Design documents cannot be modified in implementation PRs.",
		"Only docs/design/open-questions.md may be changed.")

	out, ok = designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, existingDoc, "# Existing\nmodified\n")
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
	})
	expectDesignFail(t, out, ok, "M docs/design/existing.md")
}

func TestDesignDocs_DeleteExistingFails(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		if err := os.Remove(filepath.Join(r, existingDoc)); err != nil {
			t.Fatal(err)
		}
	})
	expectDesignFail(t, out, ok, "D docs/design/existing.md")
}

func TestDesignDocs_RenameExistingFails(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		runCmd(t, r, "git", "mv", existingDoc, "docs/design/renamed.md")
	})
	expectDesignFail(t, out, ok, "D docs/design/existing.md")
}

func TestDesignDocs_RatifySubdirPasses(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "docs/design/sub/v6.md", "# v6\n")
	})
	expectDesignPass(t, out, ok)
}

func TestDesignDocs_OutsideOnlyPasses(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "README", "changed\n")
	})
	expectDesignPass(t, out, ok)

	out, ok = designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, "x/docs/design/a.md", "# a\n")
	})
	expectDesignPass(t, out, ok)
}

func TestDesignDocs_SpecialNamesFail(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"docs/design/caf\u00e9.md", "docs/design/c\"d.md", "docs/design/a\tb.md"} {
		base := map[string]string{name: "# x\n", oqDoc: "# Questions\n"}
		out, ok := designCheck(t, base, func(r string) {
			writeDesignFile(t, r, name, "# x\nmodified\n")
			writeDesignFile(t, r, "README", "changed\n")
		})
		expectDesignFail(t, out, ok, "M "+name)

		out, ok = designCheck(t, base, func(r string) {
			if err := os.Remove(filepath.Join(r, name)); err != nil {
				t.Fatal(err)
			}
		})
		expectDesignFail(t, out, ok, "D "+name)
	}
}

func TestDesignDocs_OpenQuestionsAnyStatusExempt(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, oqDoc, "# Questions\nmore\n")
		writeDesignFile(t, r, "README", "changed\n")
	})
	expectDesignPass(t, out, ok)

	out, ok = designCheck(t, existingBase, func(r string) {
		if err := os.Remove(filepath.Join(r, oqDoc)); err != nil {
			t.Fatal(err)
		}
		writeDesignFile(t, r, "README", "changed\n")
	})
	expectDesignPass(t, out, ok)

	base := map[string]string{existingDoc: "# Existing\n"}
	out, ok = designCheck(t, base, func(r string) {
		writeDesignFile(t, r, oqDoc, "# Questions\n")
		writeDesignFile(t, r, "README", "changed\n")
	})
	expectDesignPass(t, out, ok)
}

func TestDesignDocs_ModifyAddAndOutsideListsAll(t *testing.T) {
	t.Parallel()
	out, ok := designCheck(t, existingBase, func(r string) {
		writeDesignFile(t, r, existingDoc, "# Existing\nmodified\n")
		writeDesignFile(t, r, "docs/design/v6.md", "# v6\n")
		writeDesignFile(t, r, "README", "changed\n")
	})
	expectDesignFail(t, out, ok, "M docs/design/existing.md", "A docs/design/v6.md",
		"ratification pull request must contain only design documents")
}
