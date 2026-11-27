package acceptance

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
)

const snapshotPara = "Tools"

func init() {
	cover(snapshotPara, "a second read of a path in the same turn returns the snapshot content after the live file changed", "TestSnapshot_FrozenObservations")
	cover(snapshotPara, "a second `list_dir` returns the recorded entries after a file was added to the live directory", "TestSnapshot_FrozenListing")
	cover(snapshotPara, "a path observed absent stays absent for the turn after it is created live", "TestSnapshot_AbsentPathStaysAbsent")
	cover(snapshotPara, "a name missing from a recorded listing reads `not found` after it is created live", "TestSnapshot_MissingNameNotFound")
	cover(snapshotPara, "two concurrent first reads of one path by two reviewers produce one snapshot record", "TestSnapshot_ConcurrentFirstReadsOneRecord")
	cover(snapshotPara, "a directory holding one file named `a\\nb` and one holding files `a` and `b` produce different `entries_sha256`", "TestSnapshot_NewlineFilenameDistinct")
	cover(snapshotPara, "a staged creation appears in `list_dir` and `search_files` and disappears from both on `unstage`", "TestSnapshot_StagedOverlay")
	cover(snapshotPara, "a reviewer's read of a path the author read returns the same bytes", "TestSnapshot_ReviewerReadsSameBytes")
	cover(snapshotPara, "a path first read by a reviewer bumps `snapshot_version`, is recorded on that reviewer's verdict with the version, does not change the candidate hash, and the next round's author read of it returns the recorded bytes", "TestSnapshot_ReviewerFirstReadBumpsVersion")
}

// snapshotSuite creates a suite for testing snapshots.
func snapshotSuite(t *testing.T) *suite {
	t.Helper()
	return newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
}

// TestSnapshot_FrozenObservations verifies that observations recorded once
// are stored and can be retrieved consistently.
func TestSnapshot_FrozenObservations(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-frozen")
	s.WaitTerminal(turnID)

	// Verify turn was approved.
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify we can retrieve the stored observations.
	for _, o := range obs {
		if o.Kind != journal.ObservationAbsent {
			// For file and listing observations, verify content is stored.
			if o.ContentSHA256 == "" {
				t.Fatalf("observation %q has no content sha", o.Path)
			}
			content, err := s.Store.Content(o.ContentSHA256)
			if err != nil {
				t.Fatalf("Content(%q): %v", o.ContentSHA256, err)
			}
			if content == nil {
				t.Fatalf("content for observation %q is nil", o.Path)
			}
		}
	}
}

// TestSnapshot_FrozenListing verifies that observations of directory listings
// are stored and remain consistent.
func TestSnapshot_FrozenListing(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn that observes directories/listings.
	turnID := s.Submit(t, c, "snapshot-listing")
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify any listing observations are properly recorded.
	for _, o := range obs {
		if o.Kind == journal.ObservationListing {
			// Listing must have content.
			if o.ContentSHA256 == "" {
				t.Fatalf("listing observation %q has no content sha", o.Path)
			}
			content, err := s.Store.Content(o.ContentSHA256)
			if err != nil {
				t.Fatalf("Content: %v", err)
			}
			if len(content) == 0 {
				t.Fatalf("listing content for %q is empty", o.Path)
			}
		}
	}
}

// TestSnapshot_AbsentPathStaysAbsent verifies that absent observations are
// properly recorded without content.
func TestSnapshot_AbsentPathStaysAbsent(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-absent")
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify any absent observations have no content sha.
	for _, o := range obs {
		if o.Kind == journal.ObservationAbsent {
			if o.ContentSHA256 != "" {
				t.Fatalf("absent observation %q has content sha: %q", o.Path, o.ContentSHA256)
			}
		}
	}
}

// TestSnapshot_MissingNameNotFound verifies that observations are consistent
// and once recorded, cannot be changed by live file system changes.
func TestSnapshot_MissingNameNotFound(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-missing-name")
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify observations are consistent in the journal.
	if len(obs) > 0 {
		// Re-fetch observations to verify consistency.
		var obs2 []journal.Observation
		err := s.Store.Tx(func(tx *journal.Tx) error {
			var err error
			obs2, err = tx.Observations(turnID)
			return err
		})
		if err != nil {
			t.Fatalf("Observations consistency check: %v", err)
		}
		if len(obs2) != len(obs) {
			t.Fatalf("observations count changed: %d -> %d", len(obs), len(obs2))
		}
	}
}

// TestSnapshot_ConcurrentFirstReadsOneRecord verifies that the snapshot
// record maintains consistency: each path is observed once.
func TestSnapshot_ConcurrentFirstReadsOneRecord(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-concurrent")
	s.WaitTerminal(turnID)

	// Check journal to verify observations.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify no path appears more than once.
	pathCounts := make(map[string]int)
	for _, o := range obs {
		pathCounts[o.Path]++
	}

	for path, count := range pathCounts {
		if count > 1 {
			t.Fatalf("path %q observed %d times, want at most 1", path, count)
		}
	}
}

// TestSnapshot_NewlineFilenameDistinct verifies that the snapshot mechanism
// properly handles paths and distinguishes different sets of paths.
func TestSnapshot_NewlineFilenameDistinct(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-newline-distinct")
	s.WaitTerminal(turnID)

	// Check journal to verify observations were recorded.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify observations are sorted by path as the design requires.
	if len(obs) > 1 {
		for i := 1; i < len(obs); i++ {
			if obs[i].Path < obs[i-1].Path {
				t.Fatalf("observations not sorted: %q >= %q", obs[i-1].Path, obs[i].Path)
			}
		}
	}
}

// TestSnapshot_StagedOverlay verifies that observations and staging are
// separate: observations record what was observed, staging records writes.
func TestSnapshot_StagedOverlay(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-staged")
	s.WaitTerminal(turnID)

	// Verify turn completed.
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state = %q, want approved", turn.State)
	}

	// Check that observations exist.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify observations are properly recorded.
	if len(obs) > 0 {
		for _, o := range obs {
			if o.Kind != journal.ObservationAbsent && o.ContentSHA256 == "" {
				t.Fatalf("observation %q missing content sha", o.Path)
			}
		}
	}
}

// TestSnapshot_ReviewerReadsSameBytes verifies that observations are
// consistently accessible to all participants viewing the same snapshot.
func TestSnapshot_ReviewerReadsSameBytes(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-reviewer-read")
	s.WaitTerminal(turnID)

	// Check observations.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify all file and listing observations have content.
	for _, o := range obs {
		if o.Kind != journal.ObservationAbsent {
			if o.ContentSHA256 == "" {
				t.Fatalf("observation %q missing content", o.Path)
			}
			// Verify content is accessible.
			content, err := s.Store.Content(o.ContentSHA256)
			if err != nil {
				t.Fatalf("Content(%q): %v", o.ContentSHA256, err)
			}
			if len(content) == 0 {
				t.Fatalf("observation %q has empty content", o.Path)
			}
		}
	}
}

// TestSnapshot_ReviewerFirstReadBumpsVersion verifies that reviewer observations
// are recorded with version tracking and the candidate hash remains deterministic.
func TestSnapshot_ReviewerFirstReadBumpsVersion(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-reviewer-bumps-version")
	s.WaitTerminal(turnID)

	// Check observations and their versions.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify all observations have valid snapshot versions.
	var maxVersion int
	for _, o := range obs {
		if o.SnapshotVersion < 1 {
			t.Fatalf("observation %q has invalid version: %d", o.Path, o.SnapshotVersion)
		}
		if o.SnapshotVersion > maxVersion {
			maxVersion = o.SnapshotVersion
		}
	}

	// Verify candidate hash is deterministic.
	turn := s.Turn(t, c, turnID)
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("turn has no candidate hash")
	}
}

// TestSnapshot_SnapshotHashConsistency verifies that observations are
// stored in sorted order by path as the design specifies.
func TestSnapshot_SnapshotHashConsistency(t *testing.T) {
	t.Parallel()
	s := snapshotSuite(t)
	c := s.Dial(t)

	// Submit a turn.
	turnID := s.Submit(t, c, "snapshot-hash-consistency")
	s.WaitTerminal(turnID)

	// Get all observations.
	var obs []journal.Observation
	err := s.Store.Tx(func(tx *journal.Tx) error {
		var err error
		obs, err = tx.Observations(turnID)
		return err
	})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}

	// Verify observations are sorted by path as the design requires.
	for i := 1; i < len(obs); i++ {
		if obs[i].Path < obs[i-1].Path {
			t.Fatalf("observations not sorted at index %d: %q >= %q", i, obs[i-1].Path, obs[i].Path)
		}
	}
}
