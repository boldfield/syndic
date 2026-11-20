# Milestone B Acceptance Report

This document summarizes the acceptance testing for milestone B of the delta engine and control socket. All tests are deterministic with `fake.Adapter`; there is no real endpoint in CI.

## Coverage Summary

The acceptance test suite covers section 8 of `docs/design/v4-delta-engine-tui.md` clause by clause. The table below shows the coverage per design paragraph.

| Paragraph | Items | Tests Passing | Gaps | Milestone C |
|-----------|-------|----------------|------|-------------|
| Protocol | 10 | 10 | 0 | 0 |
| Dispatch failures | 9 | 9 | 0 | 0 |
| Runs and manifest | 13 | 13 | 0 | 0 |
| Roster and stop | 10 | 10 | 0 | 0 |
| N and roles | 5 | 4 | 1 | 0 |
| Budgets | 20 | 20 | 0 | 0 |
| Quick profile | 11 | 4 | 0 | 7 |
| Deadlines and pools | 7 | 7 | 0 | 0 |
| Cancellation and epochs | 4 | 4 | 0 | 0 |
| Recovery | 3 | 3 | 0 | 0 |
| Socket | 18 | 17 | 0 | 1 |
| Journal and races | 6 | 6 | 0 | 0 |
| Capacity | 9 | 9 | 0 | 0 |
| Repair and delivery | 7 | 7 | 0 | 0 |
| TUI | 10 | 0 | 0 | 10 |
| **Total** | **142** | **123** | **1** | **18** |

## Gaps

The following gaps are observed in the milestone B acceptance test suite. Each gap quotes the design clause and states what the engine does instead, taken from the `gap()` message in the test.

### N and roles: Author as reviewer

**Test:** `TestNAndRoles_AuthorAsReviewerRefused`

**Design clause:** A preset naming the author as reviewer is refused at submit.

**Gap:** The config validator rejects a preset whose required_reviewers contains its author when the roster is loaded (config/validator.go: "author is also in required reviewers"), so the engine refuses to start on such a roster and no submit can ever name the preset. The design specifies the engine should start normally and refuse the submit with the preset. The engine's behaviour is kept and the question is recorded as OQ-4 in `docs/design/open-questions.md`.

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
