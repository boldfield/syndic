# Milestone B Acceptance Report

This document summarizes the acceptance testing for milestone B of the delta engine and control socket. All tests are deterministic with `fake.Adapter`; there is no real endpoint in CI.

## Coverage Summary

The acceptance test suite covers section 8 of `docs/design/v4-delta-engine-tui.md` clause by clause. The table below shows the coverage per design paragraph.

| Paragraph | Clauses | Tests passing | Status |
|-----------|---------|----------------|--------|
| Protocol | 10 | 10 | complete |
| Dispatch failures | 9 | 9 | complete |
| Runs and manifest | 13 | 13 | complete |
| Roster and stop | 10 | 10 | complete |
| N and roles | 5 | 4 | 1 gap |
| Budgets | 20 | 20 | complete |
| Quick profile | 11 | 4 | 7 milestone C |
| Deadlines and pools | 7 | 7 | complete |
| Cancellation and epochs | 4 | 4 | complete |
| Recovery | 3 | 3 | complete |
| Socket | 18 | 17 | 1 milestone C |
| Journal and races | 6 | 6 | complete |
| Capacity | 9 | 9 | complete |
| Repair and delivery | 7 | 7 | complete |
| TUI | 10 | 0 | 10 milestone C |

**Total:** 142 clauses registered, 123 milestone B tests passing, 1 milestone B gap, 18 milestone C items.

## Gaps

The following gaps are observed in the milestone B acceptance test suite. Each gap quotes the design clause and states what the engine does instead, taken from the `gap()` message in the test.

### N and roles: Author as reviewer

**Test:** `TestNAndRoles_AuthorAsReviewerRefused`

**Design clause:** A preset naming the author as reviewer is refused at submit.

**Gap:** The config validator rejects a preset whose required_reviewers contains its author when the roster is loaded (config/validator.go: "author is also in required reviewers"), so the engine refuses to start on such a roster and no submit can ever name the preset. The design specifies the engine should start normally and refuse the submit with the preset.

## Milestone C Items

The following 18 items are registered as milestone C (TUI implementation). These are not implemented in this delta.

- TUI: answer tab is empty before terminal state with drafts off
- TUI: drafts on shows the banner
- TUI: a quick profile with `drafts = true` turns it on for governed turns
- TUI: incomplete shows blocks with remedy
- TUI: `/why` on a budget failure shows estimated and limit
- TUI: override requires a reason and shows the banner
- TUI: the provisional prompt appears once per process and the resend carries `allow_provisional`
- TUI: the roster-changed banner follows `status`
- TUI: `TERM=dumb` runs
- TUI: the three-pane layout renders at 80×24
- Quick profile: (TUI) a project file is found from a subdirectory, shown, asked about once, recorded by path, file hash, profile name and definition hash, and sent on the next submit
- Quick profile: (TUI) a rewritten file asks again
- Quick profile: (TUI) an unchanged file whose profile's `preset` changed in the roster (a different `definition_sha256` in `status.quick_profiles`) asks again
- Quick profile: (TUI) a file naming a profile the manifest no longer has asks again and is not sent
- Quick profile: (TUI) an unapproved file is not sent and the strip shows `quick: unapproved`
- Quick profile: (TUI) `/quick off` stops sending it for that process
- Quick profile: (TUI) the strip shows `quick:x`
- Socket: (TUI) detach with a turn open, reattach, turn finished meanwhile, answer tab shows the result

## Running the Suite

To run the full acceptance test suite:

```bash
go test -race ./internal/acceptance/...
```

To run only coverage checks:

```bash
go test ./internal/acceptance -run TestCoverage
```

To list all registered items and gaps:

```bash
go test ./internal/acceptance -run TestCoverage_Gaps -v
```

The suite runs in under two minutes on a standard machine with no skipped tests except those reporting gaps.
