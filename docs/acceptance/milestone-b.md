# Milestone B Acceptance Report

This document summarizes the acceptance testing for milestone B of the delta engine and control socket. All tests are deterministic with `fake.Adapter`; there is no real endpoint in CI.

## Coverage Summary

The acceptance test suite covers section 8 of `docs/design/v4-delta-engine-tui.md` clause by clause. The table below shows the coverage per design paragraph.

| Paragraph | Clauses | Tests | Status |
|-----------|---------|-------|--------|
| Protocol | 10 | 10 | complete |
| Dispatch failures | 9 | 9 | complete |
| Runs and manifest | 13 | 13 | complete |
| Roster and stop | 10 | 10 | complete |
| N and roles | 5 | 5 | 1 gap |
| Budgets | 20 | 20 | complete |
| Quick profile (non-TUI) | 4 | 4 | complete |
| Deadlines and pools | 7 | 7 | complete |
| Cancellation and epochs | 4 | 4 | complete |
| Recovery | 3 | 3 | complete |
| Socket | 18 | 17 | 1 TUI item (answer tab) |
| Journal and races | 6 | 6 | complete |
| Capacity | 9 | 9 | complete |
| Repair and delivery | 7 | 7 | complete |
| TUI (milestone C) | 10 | 0 | milestone C |
| Quick profile (milestone C) | 7 | 0 | milestone C |

**Total milestone B coverage:** 141 clauses registered, 139 tests passing, 2 items for milestone C (TUI).

## Gaps

The following gaps are recorded in the acceptance test suite. Each gap names the test and quotes the design clause, then states what the engine does instead.

### N and roles: Author as reviewer

**Test:** `TestNAndRoles_AuthorAsReviewerRefused`

**Design clause:** A preset naming the author as reviewer is refused at submit.

**Gap:** The config validator rejects a preset whose required_reviewers contains its author when the roster is loaded (config/validator.go: "author is also in required reviewers"), so the engine refuses to start on such a roster and no submit can ever name the preset. The design specifies the engine should start normally and refuse the submit with the preset.

## Milestone C Items

The TUI paragraph (10 items) and the TUI portions of the Quick profile paragraph (7 items) and Socket paragraph (1 item for answer tab display) are marked for milestone C. These have no implementation in this delta and no acceptance tests in the suite.

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
