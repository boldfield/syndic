package journal

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrBaseChanged reports a first apply whose classification found a path not
// at its base. Nothing is journaled when it is returned.
var ErrBaseChanged = errors.New("base changed")

// BaseChangedError names the paths that were not pending at a first apply.
type BaseChangedError struct {
	Paths []string
}

func (e *BaseChangedError) Error() string {
	return "base_changed: " + strings.Join(e.Paths, ", ")
}

// Is makes errors.Is(err, ErrBaseChanged) true.
func (e *BaseChangedError) Is(target error) bool { return target == ErrBaseChanged }

// LivePath is what the confined open found at one path of the workspace.
// A path absent from the map ClassifyPaths receives is the same as !Exists.
type LivePath struct {
	Exists bool
	SHA256 string
}

// PathClass is one path's classification against the live workspace.
type PathClass struct {
	Path    string
	Outcome string // PathPending, PathApplied (done) or PathConflict
}

// ClassifyPaths classifies each path of a change set from the live
// workspace: at its base (an absent file for a create) is pending, at its new
// content is done (applied), at neither is conflict. The result is in the
// order of paths.
func ClassifyPaths(paths []ChangeSetPath, live map[string]LivePath) []PathClass {
	out := make([]PathClass, 0, len(paths))
	for _, p := range paths {
		l := live[p.Path]
		outcome := PathConflict
		switch {
		case l.Exists && l.SHA256 == p.NewSHA256:
			outcome = PathApplied
		case p.Op == "create" && !l.Exists:
			outcome = PathPending
		case p.Op != "create" && l.Exists && l.SHA256 == p.BaseSHA256:
			outcome = PathPending
		}
		out = append(out, PathClass{Path: p.Path, Outcome: outcome})
	}
	return out
}

// ApplyPlan is what a started apply has left to do.
type ApplyPlan struct {
	State     string   // the change set's state: applied, or partial once begun
	Pending   []string // paths to write, sorted
	Conflicts []string // paths at neither hash; reported after the pending ones are written
}

// PrepareApply decides and journals the start of an apply from the live
// classification, in one transaction. An applied change set returns state
// applied and writes nothing to the journal. An appliable one must have every
// path pending, else *BaseChangedError is returned, the change set stays
// appliable and nothing is journaled. A partial or conflict one skips done
// paths (recording them applied) and leaves pending and conflict paths in the
// plan. A discarded one is refused with ErrChangeSetState.
func (tx *Tx) PrepareApply(changeSetID string, live map[string]LivePath, override bool) (ApplyPlan, error) {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return ApplyPlan{}, err
	}
	if cs.State == ChangeSetApplied {
		return ApplyPlan{State: ChangeSetApplied}, nil
	}
	classes := ClassifyPaths(cs.Paths, live)
	if cs.State == ChangeSetAppliable {
		var bad []string
		for _, c := range classes {
			if c.Outcome != PathPending {
				bad = append(bad, c.Path)
			}
		}
		if len(bad) > 0 {
			sort.Strings(bad)
			return ApplyPlan{State: cs.State}, &BaseChangedError{Paths: bad}
		}
	}
	if err := tx.BeginApply(cs.ID, override); err != nil {
		return ApplyPlan{}, err
	}
	plan := ApplyPlan{State: ChangeSetPartial}
	current := make(map[string]string, len(cs.Paths))
	for _, p := range cs.Paths {
		current[p.Path] = p.Outcome
	}
	for _, c := range classes {
		switch c.Outcome {
		case PathApplied:
			if current[c.Path] != PathApplied {
				if err := tx.RecordApplyFile(cs.ID, c.Path, PathApplied); err != nil {
					return ApplyPlan{}, err
				}
			}
		case PathPending:
			plan.Pending = append(plan.Pending, c.Path)
		default:
			plan.Conflicts = append(plan.Conflicts, c.Path)
		}
	}
	sort.Strings(plan.Pending)
	sort.Strings(plan.Conflicts)
	return plan, nil
}

// RecoverApply is recovery for an apply begun and not finished. It classifies
// every path from the live workspace, journals each path whose classification
// is done or conflict and differs from its recorded outcome as an apply_file,
// then journals apply_recovered, all in one transaction; it never writes the
// workspace. A pending classification journals nothing, since a path's
// outcome starts pending. It returns the change set as recorded.
func (tx *Tx) RecoverApply(changeSetID string, live map[string]LivePath) (ChangeSet, error) {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return ChangeSet{}, err
	}
	current := make(map[string]string, len(cs.Paths))
	for _, p := range cs.Paths {
		current[p.Path] = p.Outcome
	}
	for _, c := range ClassifyPaths(cs.Paths, live) {
		if c.Outcome == PathPending || current[c.Path] == c.Outcome {
			continue
		}
		if err := tx.RecordApplyFile(cs.ID, c.Path, c.Outcome); err != nil {
			return ChangeSet{}, err
		}
	}
	if err := tx.RecordApplyRecovered(cs.ID); err != nil {
		return ChangeSet{}, err
	}
	return tx.GetChangeSet(cs.ID)
}

// RecoverApply is Tx.RecoverApply in its own transaction.
func (s *Store) RecoverApply(changeSetID string, live map[string]LivePath) (cs ChangeSet, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		cs, txErr = tx.RecoverApply(changeSetID, live)
		return txErr
	})
	if err != nil {
		return ChangeSet{}, fmt.Errorf("recover apply: %w", err)
	}
	return cs, nil
}
