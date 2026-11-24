package tui

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

func TestFindQuickToml(t *testing.T) {
	// Test: a file two directories above is found
	tmpdir := t.TempDir()
	subdir := filepath.Join(tmpdir, "a", "b")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create .syndic/quick.toml at tmpdir level
	quickdir := filepath.Join(tmpdir, ".syndic")
	if err := os.Mkdir(quickdir, 0755); err != nil {
		t.Fatal(err)
	}
	quickfile := filepath.Join(quickdir, "quick.toml")
	if err := os.WriteFile(quickfile, []byte(`profile = "solo"`), 0644); err != nil {
		t.Fatal(err)
	}

	path, data, err := findQuickToml(subdir)
	if err != nil {
		t.Fatal(err)
	}
	if path != quickfile {
		t.Errorf("expected %q, got %q", quickfile, path)
	}
	if string(data) != `profile = "solo"` {
		t.Errorf("unexpected data: %s", data)
	}
}

func TestFindQuickTomlStopsAtHome(t *testing.T) {
	// Test: search stops at home directory (doesn't go into parents of home)
	tmpdir := t.TempDir()
	homedir := filepath.Join(tmpdir, "home")
	subdir := filepath.Join(homedir, "subdir")
	abovedir := filepath.Join(tmpdir, "above")

	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(abovedir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create .syndic/quick.toml above the home directory
	quickdir := filepath.Join(tmpdir, ".syndic")
	if err := os.Mkdir(quickdir, 0755); err != nil {
		t.Fatal(err)
	}
	quickfile := filepath.Join(quickdir, "quick.toml")
	if err := os.WriteFile(quickfile, []byte(`profile = "solo"`), 0644); err != nil {
		t.Fatal(err)
	}

	// Set home directory to homedir; search should stop there and not reach tmpdir
	t.Setenv("HOME", homedir)

	path, data, err := findQuickToml(subdir)
	if err != nil {
		t.Fatal(err)
	}

	// Should not find the file above home
	if path != "" {
		t.Errorf("expected no file, got %q", path)
	}
	if data != nil {
		t.Errorf("expected no data, got %v", data)
	}
}

func TestParseQuickToml(t *testing.T) {
	tests := []struct {
		name          string
		data          string
		expectedName  string
		expectedError bool
	}{
		{"valid profile", `profile = "solo"`, "solo", false},
		{"valid profile with spaces", `profile = "my profile"`, "my profile", false},
		{"missing profile", "", "", true},
		{"empty profile", `profile = ""`, "", true},
		{"extra key", "profile = \"solo\"\nextra = \"key\"", "", true},
		{"invalid toml", `{invalid`, "", true},
	}

	filePath := "/home/user/project/.syndic/quick.toml"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, errStr := parseQuickToml([]byte(tt.data), filePath)
			if tt.expectedError {
				if errStr == "" {
					t.Errorf("expected error, got none")
				}
			} else {
				if errStr != "" {
					t.Errorf("expected no error, got %q", errStr)
				}
				if name != tt.expectedName {
					t.Errorf("expected %q, got %q", tt.expectedName, name)
				}
			}
		})
	}
}

func TestFindQuickTomlNearestWins(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	sub := filepath.Join(root, "a", "b")
	for _, dir := range []string{root, filepath.Join(root, "a")} {
		if err := os.MkdirAll(filepath.Join(dir, ".syndic"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".syndic", "quick.toml"), []byte(`profile = "`+filepath.Base(dir)+`"`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path, data, err := findQuickToml(sub)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "a", ".syndic", "quick.toml"); path != want || string(data) != `profile = "a"` {
		t.Fatalf("found %q %q, want the nearer file %q", path, data, want)
	}
}

func TestFindQuickTomlHomeItselfIsSearched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".syndic"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".syndic", "quick.toml")
	if err := os.WriteFile(want, []byte(`profile = "fast"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, _, err := findQuickToml(home); err != nil || path != want {
		t.Fatalf("found %q, %v", path, err)
	}
}

func TestApprovalsFileModeAndReplacement(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "syndic")
	// A directory and a file that exist with loose modes are tightened.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quick-approvals"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	first := ApprovedProfile{FilePath: "/p/.syndic/quick.toml", FileSHA256: "f1", ProfileName: "fast", ProfileDefinitionSHA256: "d1"}
	other := ApprovedProfile{FilePath: "/q/.syndic/quick.toml", FileSHA256: "f2", ProfileName: "fast", ProfileDefinitionSHA256: "d1"}
	for _, ap := range []ApprovedProfile{first, other} {
		if err := writeApproval(ap); err != nil {
			t.Fatal(err)
		}
	}
	// A new profile for the same path replaces its older entry.
	second := first
	second.ProfileName, second.FileSHA256 = "slow", "f3"
	if err := writeApproval(second); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]fs.FileMode{dir: 0o700, filepath.Join(dir, "quick-approvals"): 0o600} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", path, st.Mode().Perm(), want)
		}
	}
	got, err := readApprovals()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[first.FilePath] != second || got[other.FilePath] != other {
		t.Fatalf("approvals = %+v", got)
	}
}

func TestApprovedForNeedsAllFourValues(t *testing.T) {
	qs := &QuickSelection{ProjectProfile: "fast", ProjectFilePath: "/p/q.toml", ProjectFileSHA: "f1", approved: map[string]ApprovedProfile{
		"/p/q.toml": {FilePath: "/p/q.toml", FileSHA256: "f1", ProfileName: "fast", ProfileDefinitionSHA256: "d1"},
	}}
	if !qs.approvedFor("d1") {
		t.Fatal("matching entry not approved")
	}
	if qs.approvedFor("d2") {
		t.Error("changed definition still approved")
	}
	qs.ProjectFileSHA = "f2"
	if qs.approvedFor("d1") {
		t.Error("changed file bytes still approved")
	}
	qs.ProjectFileSHA, qs.ProjectProfile = "f1", "slow"
	if qs.approvedFor("d1") {
		t.Error("changed profile name still approved")
	}
}

// quickEnv isolates the home and state directories and returns a project directory inside the home.
func quickEnv(t *testing.T) (home, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	project = filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(project, ".syndic"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, project
}

func writeQuickFile(t *testing.T, project, body string) string {
	t.Helper()
	path := filepath.Join(project, ".syndic", "quick.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func quickHarness(t *testing.T, roster string) *enginetest.Harness {
	t.Helper()
	return enginetest.New(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText)},
		"reviewer": {approve(), approve(), approve()},
	})
}

// startQuickRig is a connected TUI whose quick profile search starts in project.
func startQuickRig(t *testing.T, roster, project string) *rig {
	t.Helper()
	h := quickHarness(t, roster)
	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, paths.Socket, WithWorkDir(project))
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.waitConnected(t)
	return r
}

func pressKey(r *rig, k string) {
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
}

// submitAndWait types a line and returns the new turn once it has finished, with the profile it carried.
func submitAndWait(t *testing.T, r *rig, line, state string) (turn string, quick *string) {
	t.Helper()
	r.typeLine(line)
	turn = turnIn(t, r, state)
	r.until(t, "the turn's record", func(a *App) (string, bool) {
		quick = a.state.Turns[turn].QuickProfile
		return "", true
	})
	return turn, quick
}

// stripOf is the strip as it showed while the turn was open, rendered from the state the App holds for it.
func stripOf(t *testing.T, r *rig, turn string) string {
	t.Helper()
	return r.until(t, "the strip", func(a *App) (string, bool) {
		s := a.state
		s.CurrentTurn = turn
		return renderStrip(s, 120, time.Now(), false), true
	})
}

func quickIs(q *string, want string) bool { return q != nil && *q == want }

const approvalPromptText = "Approve this selection? (y/n)"

// approveFirstRun answers yes to the prompt of a first run.
func approveFirstRun(t *testing.T, r *rig) {
	t.Helper()
	untilView(t, r, approvalPromptText)
	pressKey(r, "y")
	untilView(t, r, "quick profile fast approved")
}

func TestQuickFirstRunPromptsAndYesApplies(t *testing.T) {
	_, project := quickEnv(t)
	file := writeQuickFile(t, project, `profile = "fast"`)
	r := startQuickRig(t, enginetest.RosterTOML, project)

	v := untilView(t, r, approvalPromptText)
	for _, want := range []string{file, `"fast"`, "preset:  solo", "drafts:  false", "author_max_output_tokens", "quick: unapproved fast"} {
		if !strings.Contains(strings.ReplaceAll(v, "\n", ""), want) {
			t.Errorf("approval view lacks %q:\n%s", want, v)
		}
	}
	pressKey(r, "y")
	untilView(t, r, "quick profile fast approved")

	var def string
	r.until(t, "the roster profile", func(a *App) (string, bool) {
		def = a.state.Status.QuickProfiles[0].DefinitionSHA256
		return "", true
	})
	data, _ := os.ReadFile(file)
	approvals, err := readApprovals()
	if err != nil {
		t.Fatal(err)
	}
	want := ApprovedProfile{FilePath: file, FileSHA256: fmt.Sprintf("%x", sha256.Sum256(data)), ProfileName: "fast", ProfileDefinitionSHA256: def}
	if approvals[file] != want || def == "" {
		t.Fatalf("approvals = %+v, want %+v", approvals, want)
	}
	path, _ := approvalsPath()
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("approvals file: %v %v", st, err)
	}

	turn, quick := submitAndWait(t, r, "hello", "solo_complete")
	if !quickIs(quick, "fast") || presetOf(t, r, turn) != "solo" {
		t.Fatalf("turn carried quick=%v preset=%q", quick, presetOf(t, r, turn))
	}
	if strip := stripOf(t, r, turn); !strings.Contains(strip, "[quick:fast]") {
		t.Errorf("strip = %q", strip)
	}
	if v := view(t, r); strings.Contains(v, "quick: unapproved") {
		t.Errorf("banner left after approval:\n%s", v)
	}

	// The TUI wrote nothing into the project.
	var found []string
	_ = filepath.WalkDir(project, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 || found[0] != file {
		t.Fatalf("project files = %v", found)
	}
}

func TestQuickSecondProcessDoesNotAsk(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	approveFirstRun(t, startQuickRig(t, enginetest.RosterTOML, project))

	r := startQuickRig(t, enginetest.RosterTOML, project)
	_, quick := submitAndWait(t, r, "hello", "solo_complete")
	if !quickIs(quick, "fast") {
		t.Fatalf("quick = %v", quick)
	}
	if v := view(t, r); strings.Contains(v, approvalPromptText) || strings.Contains(v, "quick: unapproved") {
		t.Fatalf("second process asked:\n%s", v)
	}
}

func TestQuickRewrittenFileAsksAgain(t *testing.T) {
	_, project := quickEnv(t)
	file := writeQuickFile(t, project, `profile = "fast"`)
	approveFirstRun(t, startQuickRig(t, enginetest.RosterTOML, project))

	writeQuickFile(t, project, "profile = \"fast\" # reworded\n")
	r := startQuickRig(t, enginetest.RosterTOML, project)
	untilView(t, r, approvalPromptText, file)
	pressKey(r, "n")
	untilView(t, r, "will not be sent")
}

func TestQuickChangedDefinitionAsksAgain(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	approveFirstRun(t, startQuickRig(t, enginetest.RosterTOML, project))

	changed := strings.Replace(enginetest.RosterTOML, "[quick.fast]\npreset = \"solo\"", "[quick.fast]\npreset = \"review\"", 1)
	if changed == enginetest.RosterTOML {
		t.Fatal("roster not changed")
	}
	r := startQuickRig(t, changed, project)
	untilView(t, r, approvalPromptText, "preset:  review")
}

func TestQuickUnknownProfileIsUnapprovedAndNotSent(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "nope"`)
	r := startQuickRig(t, enginetest.RosterTOML, project)
	untilView(t, r, "quick: unapproved nope (engine has no such profile)")
	turn, quick := submitAndWait(t, r, "hello", "approved")
	if quick != nil || presetOf(t, r, turn) != "review" {
		t.Fatalf("quick = %v preset = %q", quick, presetOf(t, r, turn))
	}
	if v := view(t, r); strings.Contains(v, approvalPromptText) {
		t.Fatalf("asked to approve a profile the engine lacks:\n%s", v)
	}
}

func TestQuickNoWritesNothingAndSendsNothing(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	r := startQuickRig(t, enginetest.RosterTOML, project)
	untilView(t, r, approvalPromptText)
	pressKey(r, "n")
	untilView(t, r, "quick: unapproved fast", "will not be sent")
	if v := view(t, r); strings.Contains(v, approvalPromptText) {
		t.Fatalf("still asking:\n%s", v)
	}
	path, _ := approvalsPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("approvals file written after no: %v", err)
	}
	turn, quick := submitAndWait(t, r, "hello", "approved")
	if quick != nil || presetOf(t, r, turn) != "review" {
		t.Fatalf("quick = %v preset = %q", quick, presetOf(t, r, turn))
	}
	untilView(t, r, "quick: unapproved fast")
}

func TestQuickInvalidFile(t *testing.T) {
	_, project := quickEnv(t)
	file := writeQuickFile(t, project, "profile = \"fast\"\nextra = 1\n")
	r := startQuickRig(t, enginetest.RosterTOML, project)
	untilView(t, r, "quick: invalid "+file)
	_, quick := submitAndWait(t, r, "hello", "approved")
	if quick != nil {
		t.Fatalf("an invalid file selected %v", *quick)
	}
}

func TestQuickOffStopsAnApprovedSelection(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	approveFirstRun(t, startQuickRig(t, enginetest.RosterTOML, project))

	r := startQuickRig(t, enginetest.RosterTOML, project)
	r.typeLine("/quick off")
	untilView(t, r, "quick profile off")
	turn, quick := submitAndWait(t, r, "hello", "approved")
	if quick != nil || presetOf(t, r, turn) != "review" {
		t.Fatalf("quick = %v preset = %q", quick, presetOf(t, r, turn))
	}
	if v := view(t, r); strings.Contains(v, approvalPromptText) {
		t.Fatalf("/quick off opened the project approval:\n%s", v)
	}
}

func TestQuickNamedNeedsNoApprovalAndUnknownIsRefused(t *testing.T) {
	_, project := quickEnv(t) // no project file
	r := startQuickRig(t, enginetest.RosterTOML, project)
	r.typeLine("/quick nope")
	untilView(t, r, `unknown quick profile "nope"`, "profiles: fast")
	r.typeLine("/quick fast")
	untilView(t, r, "quick profile fast selected")
	turn, quick := submitAndWait(t, r, "hello", "solo_complete")
	if !quickIs(quick, "fast") || presetOf(t, r, turn) != "solo" {
		t.Fatalf("quick = %v preset = %q", quick, presetOf(t, r, turn))
	}
	if v := view(t, r); strings.Contains(v, approvalPromptText) {
		t.Fatalf("/quick asked for approval:\n%s", v)
	}
}

func TestQuickWithModeShowsTheOverride(t *testing.T) {
	_, project := quickEnv(t)
	r := startQuickRig(t, enginetest.RosterTOML, project)
	r.typeLine("/quick fast")
	untilView(t, r, "quick profile fast selected")
	r.typeLine("/mode review")
	untilView(t, r, "[next: review]")
	turn, quick := submitAndWait(t, r, "hello", "approved")
	if !quickIs(quick, "fast") || presetOf(t, r, turn) != "review" {
		t.Fatalf("quick = %v preset = %q", quick, presetOf(t, r, turn))
	}
	if strip := stripOf(t, r, turn); !strings.Contains(strip, "[quick:fast (mode set by /mode)]") {
		t.Errorf("strip = %q", strip)
	}
}

func TestQuickProfileVanishingCancelsOpenApproval(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	a := NewApp(context.Background(), "unused.sock", WithWorkDir(project))
	a.state.Status = &wire.StatusReply{QuickProfiles: []wire.QuickProfile{{Name: "fast", Preset: "solo", DefinitionSHA256: "d1"}}}
	a.updateQuickProfileState()
	if a.pendingApproval() == nil || !strings.Contains(strings.Join(a.panel, "\n"), approvalPromptText) {
		t.Fatalf("no approval prompt: %+v %v", a.pendingApproval(), a.panel)
	}

	// A refresh arrives whose roster no longer has the profile.
	a.state.Status = &wire.StatusReply{}
	a.updateQuickProfileState()
	if a.pendingApproval() != nil || strings.Contains(a.View(), approvalPromptText) {
		t.Fatalf("stale approval prompt remains:\n%s", a.View())
	}
	if v := a.View(); !strings.Contains(v, "quick: unapproved fast (engine has no such profile)") {
		t.Errorf("banner missing:\n%s", v)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if approvals, err := readApprovals(); err != nil || len(approvals) != 0 {
		t.Fatalf("approval written for an absent profile: %v %v", approvals, err)
	}
}

func TestFindQuickTomlHomeWithTrailingSeparatorOrSymlink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".syndic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".syndic", "quick.toml"), []byte(`profile = "solo"`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "homelink")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]string{"trailing separator": home + string(filepath.Separator), "symlinked home": link} {
		t.Setenv("HOME", h)
		if path, _, err := findQuickToml(project); err != nil || path != "" {
			t.Errorf("%s: found %q, %v above home", name, path, err)
		}
	}
}

func TestFindQuickTomlSymlinkedWorkingDirectoryStopsAtHome(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".syndic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".syndic", "quick.toml"), []byte(`profile = "solo"`), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(home, "work"), alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if path, _, err := findQuickToml(filepath.Join(alias, "project")); err != nil || path != "" {
		t.Fatalf("found %q, %v above home through a symlinked working directory", path, err)
	}

	// A file inside home is still found through the alias, at its physical path.
	if err := os.MkdirAll(filepath.Join(home, "work", ".syndic"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "work", ".syndic", "quick.toml")
	if err := os.WriteFile(want, []byte(`profile = "fast"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, _, err := findQuickToml(filepath.Join(alias, "project")); err != nil || path != want {
		t.Fatalf("found %q, %v, want %q", path, err, want)
	}
}

func TestGetDevID(t *testing.T) {
	tmpdir := t.TempDir()
	file1 := filepath.Join(tmpdir, "file1")
	file2 := filepath.Join(tmpdir, "file2")
	if err := os.WriteFile(file1, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	fi1, err := os.Stat(file1)
	if err != nil {
		t.Fatal(err)
	}
	fi2, err := os.Stat(file2)
	if err != nil {
		t.Fatal(err)
	}

	dev1 := getDevID(fi1)
	dev2 := getDevID(fi2)

	if dev1 == 0 {
		t.Error("getDevID returned 0 for file1")
	}
	if dev1 != dev2 {
		t.Errorf("getDevID returned different values: %d vs %d for files in the same directory", dev1, dev2)
	}
}
