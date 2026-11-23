package tui

import (
	"strings"
	"testing"
	"time"
)

// progressState is a review turn with author w and reviewers r1, r2, and the
// attempts given, all of turn t1.
func progressState(state string, round int, attempts ...AttemptInfo) State {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		ID: "t1", State: state, Preset: "review", SubmittedAt: stripT0, Round: round, MaxRounds: 3,
		Author: "w", AuthorState: "idle", Reviewers: []string{"r1", "r2"},
		ReviewerStates: map[string]string{"r1": "idle", "r2": "idle"},
	}
	for _, ai := range attempts {
		ai.TurnID = "t1"
		s.Attempts[ai.ID] = ai
	}
	return s
}

func setStates(s State, author string, reviewers ...string) {
	ti := s.Turns["t1"]
	ti.AuthorState = author
	ti.ReviewerStates = map[string]string{"r1": reviewers[0], "r2": reviewers[1]}
	s.Turns["t1"] = ti
}

func TestProgressLineMarks(t *testing.T) {
	now := stripT0.Add(42 * time.Second)
	spin := spinnerAt(now)
	authored := AttemptInfo{ID: "a1", Participant: "w", Phase: "author", Epoch: 1, Status: "complete"}
	cases := []struct {
		name string
		s    State
		want string
	}{
		{
			name: "author working, reviewers not begun",
			s: func() State {
				s := progressState("author_dispatched", 0, AttemptInfo{ID: "a1", Participant: "w", Phase: "author", Epoch: 1, Status: "in_progress"})
				setStates(s, "running", "idle", "idle")
				return s
			}(),
			want: spin + " turn in progress: author_dispatched, round 0/3 ○○○, " + spin + " w, ○ r1, ○ r2, 42s",
		},
		{
			name: "author done and idle, one reviewer queued, one working",
			s: func() State {
				s := progressState("reviewers_dispatched", 0, authored, AttemptInfo{ID: "v2", Participant: "r2", Phase: "review", Epoch: 2, Status: "in_progress"})
				setStates(s, "idle", "queued", "running")
				return s
			}(),
			want: spin + " turn in progress: reviewers_dispatched, round 0/3 ○○○, ✓ w, ○ r1, " + spin + " r2, 42s",
		},
		{
			name: "a reviewer done and idle again",
			s: progressState("reviewers_dispatched", 0, authored,
				AttemptInfo{ID: "v1", Participant: "r1", Phase: "review", Epoch: 2, Status: "complete"},
				AttemptInfo{ID: "v2", Participant: "r2", Phase: "review", Epoch: 2, Status: "in_progress"}),
			want: spin + " turn in progress: reviewers_dispatched, round 0/3 ○○○, ✓ w, ✓ r1, ○ r2, 42s",
		},
		{
			name: "revision dispatched: the earlier reviews are not of this candidate",
			s: progressState("revision_dispatched", 1, authored,
				AttemptInfo{ID: "v1", Participant: "r1", Phase: "review", Epoch: 2, Status: "complete"},
				AttemptInfo{ID: "v2", Participant: "r2", Phase: "review", Epoch: 2, Status: "complete"},
				AttemptInfo{ID: "a2", Participant: "w", Phase: "author", Epoch: 2, Status: "in_progress"}),
			want: spin + " turn in progress: revision_dispatched, round 1/3 ●○○, ○ w, ○ r1, ○ r2, 42s",
		},
		{
			name: "second round's reviews",
			s: progressState("reviewers_dispatched", 1, authored,
				AttemptInfo{ID: "v1", Participant: "r1", Phase: "review", Epoch: 2, Status: "complete"},
				AttemptInfo{ID: "a2", Participant: "w", Phase: "author", Epoch: 2, Status: "complete"},
				AttemptInfo{ID: "v3", Participant: "r1", Phase: "review", Epoch: 3, Status: "complete"}),
			want: spin + " turn in progress: reviewers_dispatched, round 1/3 ●○○, ✓ w, ✓ r1, ○ r2, 42s",
		},
		{
			name: "cancelling and unknown still work",
			s: func() State {
				s := progressState("reviewers_dispatched", 2, authored)
				setStates(s, "idle", "cancelling", "unknown")
				return s
			}(),
			want: spin + " turn in progress: reviewers_dispatched, round 2/3 ●●○, ✓ w, " + spin + " r1, " + spin + " r2, 42s",
		},
	}
	for _, c := range cases {
		got := progressLine(c.s, "t1", now)
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if strings.ContainsRune(got, '\x1b') {
			t.Errorf("%s: progress line carries escapes: %q", c.name, got)
		}
	}
}

// TestProgressLineIsTheRenderedTurns: a running turn that is not the current
// one shows its own progress.
func TestProgressLineIsTheRenderedTurns(t *testing.T) {
	s := progressState("reviewers_dispatched", 0)
	other := s.Turns["t1"]
	other.ID, other.State, other.Author = "t0", "author_dispatched", "x"
	s.Turns["t0"] = other
	if got := progressLine(s, "t0", stripT0); !strings.Contains(got, "author_dispatched") || !strings.Contains(got, " x, ") {
		t.Errorf("progress of t0 = %q", got)
	}
}

// TestProgressSpinnerTurns: the spinner moves with the clock, and every
// frame differs from the done and queued marks.
func TestProgressSpinnerTurns(t *testing.T) {
	seen := map[string]bool{}
	for i := range spinnerFrames {
		f := spinnerAt(stripT0.Add(time.Duration(i) * time.Second))
		if f == markDone || f == markQueued {
			t.Fatalf("spinner frame %q is a participant mark", f)
		}
		seen[f] = true
	}
	if len(seen) != len(spinnerFrames) {
		t.Errorf("spinner shows %d frames over %d seconds", len(seen), len(spinnerFrames))
	}
}

// TestChatViewShowsProgressMarks renders the App and finds the progress line
// with its marks and pips in the chat.
func TestChatViewShowsProgressMarks(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "reviewers_dispatched", "running question", "")
	ti := a.state.Turns["t1"]
	ti.SubmittedAt = chatNow.Add(-5 * time.Second)
	ti.Round, ti.MaxRounds = 1, 2
	ti.Author, ti.AuthorState = "w", "idle"
	ti.Reviewers = []string{"r1", "r2"}
	ti.ReviewerStates = map[string]string{"r1": "idle", "r2": "running"}
	a.state.Turns["t1"] = ti
	a.state.CurrentTurn = "t1"
	a.state.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Participant: "w", Phase: "author", Epoch: 2, Status: "complete"}
	a.state.Attempts["v1"] = AttemptInfo{ID: "v1", TurnID: "t1", Participant: "r1", Phase: "review", Epoch: 3, Status: "complete"}

	spin := spinnerAt(chatNow)
	lineOf(t, chatView(a), spin+" turn in progress: reviewers_dispatched, round 1/2 ●○, ✓ w, ✓ r1, "+spin+" r2, 5s")
	// At 80 columns round n/m outranks the reviews hint.
	fields := "[review] [reviewers_dispatched] [5s] [round 1/2]"
	if last, want := chatView(a)[23], fields+strings.Repeat(" ", 80-len(fields)-len(keyHint))+keyHint; last != want {
		t.Errorf("status line = %q, want %q", last, want)
	}
	a.width = 90
	if last, want := chatView(a)[23], fields+strings.Repeat(" ", 90-len(fields)-len(keyHint)-len(reviewsHint)-2)+reviewsHint+"  "+keyHint; last != want {
		t.Errorf("status line at 90 = %q, want %q", last, want)
	}
}
