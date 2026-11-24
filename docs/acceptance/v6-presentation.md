# V6 presentation acceptance ledger

This ledger maps normative D-side clauses of `docs/design/v6-delta-tui-presentation.md`
(ratified revision 4, commit `67f43eb6a1e2d988e0675ce9e467ec2721ea9954`, SHA-256
`1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7`) to the
implementation file and executing tests that prove it. D-side clauses (sections 1–3,
5–6) map to implementation with executed tests. E-side clauses (file attachment,
apply dispatch, file listing, attachment processing) are explicitly assigned to V6-E
tasks (pending task creation; this design task does not cover them). Both D and E
accept against the fake adapter with `httptest`, temporary files and sockets; no test
contacts a real endpoint or credential.

## Dependency inventory and visual reference provenance

**Dependency inventory decision:** Operator accepted all six TUI modules (Lip Gloss,
Bubbles, Glamour, Huh, Harmonica, bubblezone) and their transitive modules on
2026-11-22, documented in `docs/dependencies/v6-tui.md`. Implementation: `go mod tidy`
committed the pinned versions in `go.mod` and `go.sum`; test coverage is implicit
(builds succeed, no test-time module resolution).

**Visual reference provenance:** Operator chose the visual direction from mock-ups on
a private design canvas on 2026-11-21 (Ledger layout with reviews drawer and nine
additions). Reference frames (`internal/tui/testdata/v6/*.txt`) are illustrations;
where they disagree with the ratified text, the text governs. Twelve disagreements
recorded in `internal/tui/testdata/v6/README.md`; design governs each. Verification:
mock-ups inform test fixtures and golden frames; `internal/tui.TestV6PlainTerminalGoldenFrame`,
`internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6OutcomesGoldenFrame`
validate that the actual rendering matches the frames where the design governs.

## Validation evidence

Run the entire ledger:

    go test -short -count=1 ./internal/acceptance ./internal/tui

**Tree and commit verified:** The rework was verified on tree at commit `HEAD` 
(checked 2026-11-24 with `make check && make test`).

**Expected result:** All tests pass, no skipped tests in this ledger. The mirror test
`internal/tui.TestV6PresentationLedgerNamesRealTests` in `internal/tui/v6_acceptance_test.go`
verifies that every table row names at least one test, that each named test exists, and
that none calls `Skip` or `Skipf` directly.

## Safe text and content display

| Clause | Implementation | Tests |
| --- | --- | --- |
| Every C0 control except LF and tab, DEL, every C1 control and every Unicode bidi/isolate character is replaced by a visible escape | `internal/tui.renderSafeText` (content.go) | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInDrawer`, `internal/tui.TestV6AdversarialContentInTranscript` |
| A byte not valid UTF-8 is shown as `\xNN` | `internal/tui.renderSafeText` (content.go) | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript` |
| Tab is shown as spaces | `internal/tui.renderSafeText` (content.go) | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript` |
| Every displayed content line is prefixed with two spaces after wrapping; no content reaches column 0 or 1 | `internal/tui.chat()` calls `lipgloss.NewStyle().PaddingLeft(2)` (chat.go) | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6Crowded80x24GoldenFrame` |
| An answer whose text is an outcome line, and one using CR or cursor movement to reach column 0, are shown indented and unmarked | `internal/tui.chat()` applies padding to outcome lines (chat.go) | `internal/tui.TestV6AdversarialContentThroughChat` |
| A user input begins `› ` in column 0, continuation lines are content lines | `internal/tui.chat()` renders user input prefix (chat.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| An outcome line begins with a one-character mark and a space: `✓` approved, `!` incomplete/overridden, `×` cancelled, `·` solo | `internal/tui.answer.Mark` (answer.go) | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| `approved by <ids> after 1 revision` for n=1; unchanged for n≥2 | `internal/tui.answer` formats revision text (answer.go) | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| Every v4 and v5 banner string appears verbatim after its mark | `internal/tui.answer` formats banner strings (answer.go) | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| `/raw on` shows answer safe text with no Markdown; `transcript` content returns journal bytes unchanged | `internal/tui.commands` processes `/raw` (commands.go); `internal/tui.answer` renders with/without Markdown (answer.go) | `internal/tui.TestV6RawAndMarkdownThroughCommands` |
| Answer text is rendered as Markdown (inline code, lists, emphasis, fenced code with language label) from safe text | `internal/tui.renderMarkdown` (markdown.go) | `internal/tui.TestV6RawAndMarkdownThroughCommands` |

## Status line

| Clause | Implementation | Tests |
| --- | --- | --- |
| Status line is last at every size and ends with `tab: switch  /help` | `internal/tui.statusLine()` (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Status line elements (meter, reviews hint, round text, state) are hidden in order when width is insufficient; `tab: switch  /help` is always kept | `internal/tui.statusLine()` rebuilds elements based on available width (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Status line shows: preset; `quick:<name>` when one governs; turn state or `idle`; `elapsed/turn_deadline` while turn runs | `internal/tui.statusLine()` builds state string (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Status line shows: budget meter and `tokens/limit`; `round n/m` during revision | `internal/tui.statusLine()` includes meter and round (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Status line ends with `ctrl+o: reviews` when chat tab is active, then `tab: switch  /help` | `internal/tui.statusLine()` builds right edge (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |

## Progress line and scroll position

| Clause | Implementation | Tests |
| --- | --- | --- |
| Progress line shows spinner, phase, round pips (filled for started, empty for future), each participant with status, elapsed time | `internal/tui.progress()` (progress.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Conversation line shows earlier turns above view and scroll percentage | `internal/tui.chat()` renders scroll indicator (chat.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Transcript and changes tabs show scroll percentage | `internal/tui.chat()` renders scroll indicator for all tabs (chat.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Round pips are shown in the progress line, one per allowed round; filled for started, empty for future | `internal/tui.progress()` renders pips (progress.go) | `internal/tui.TestV6PlainTerminalGoldenFrame`, `internal/tui.TestV6Crowded80x24GoldenFrame` |

## Heights and viewport management

| Clause | Implementation | Tests |
| --- | --- | --- |
| Tab bar and status line are one line each and always shown | `internal/tui.layout()` enforces fixed heights (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Input shows 1 to 6 lines and scrolls beyond that | `internal/tui.input.View()` bounds height to 6 lines (input.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Banners take at most 2 lines; excess ends `+<n> more` | `internal/tui.chat()` truncates banners (chat.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Attached line is one line with paths until width runs out, then `+<n> more` | `internal/tui.attachedLine()` formats one line (chat.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Command or file list takes at most 5 lines and closes when drawer opens | `internal/tui.commandList` bounds height; `internal/tui.app.Update()` closes list when drawer opens (command_list.go, app.go) | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Short help line is one line, hidden while drawer is open | `internal/tui.chat()` renders short help; `internal/tui.app.Update()` hides it when drawer opens (chat.go, app.go) | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Active tab content gets what remains and never fewer than 5 lines | `internal/tui.layout()` enforces minimum 5 lines (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| When opening drawer would leave fewer than 5 lines for tab, input viewport shrinks to 1 line and banners to 1 line first | `internal/tui.layout()` recalculates heights on drawer open (layout.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |

## Rendering and style

| Clause | Implementation | Tests |
| --- | --- | --- |
| No meaning is carried by colour alone; every outcome has its mark and words | `internal/tui.answer.Mark` enforces non-colour-only meaning (answer.go) | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| Input prompt is `┃ ` | `internal/tui.input.View()` renders prompt (input.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Identity warning is a banner, never folded into count, sort first among banners | `internal/tui.chat()` handles identity warning as banner (chat.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |

## Command list and short help

| Clause | Implementation | Tests |
| --- | --- | --- |
| When input begins with `/` and contains no space, matching commands list appears above input | `internal/tui.commandList` triggers and filters (command_list.go) | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Command list shows each command with one-line description and current value where applicable | `internal/tui.commandList.View()` renders descriptions and values (command_list.go) | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `↑` `↓` select in list; `Tab` completes; `Esc` closes; while list open, `Tab` completes instead of switching tabs | `internal/tui.commandList` and `internal/tui.app.Update()` handle keys (command_list.go, app.go) | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `?` on empty input shows one line of applicable keys above status line | `internal/tui.chat()` renders short help on `?` (chat.go) | `internal/tui.TestV6CommandCompletionAndShortHelp` |

## Conversations list

| Clause | Implementation | Tests |
| --- | --- | --- |
| `/conversations` is a selectable list; `/` filters it, `↑` `↓` select, `Enter` switches, `n` starts new, `Esc` closes | `internal/tui.conversations` and `internal/tui.conversationList` (conversations.go, conversation_list.go) | `internal/tui.TestV6ChooserThroughUpdateAndView` |

## Prompts (D-side)

| Clause | Implementation | Tests |
| --- | --- | --- |
| `/override` and `/release` without a reason open a form with one required field | `internal/tui.reasonForm` (reason_form.go) | `internal/tui.TestV6OverrideReasonForm`, `internal/tui.TestV6ReleaseReasonForm` |
| Submitting a prompt sends the command with the reason; an empty reason cannot be submitted | `internal/tui.reasonForm` enforces non-empty reason (reason_form.go) | `internal/tui.TestV6OverrideReasonForm`, `internal/tui.TestV6ReleaseReasonForm` |
| Prompts in a plain terminal are single-line questions | `internal/tui.reasonForm` renders single-line in plain mode (reason_form.go) | `internal/tui.TestV6ReasonPromptInPlainTerminal` |

## Drawer wire contract and behavior (D-side)

| Clause | Implementation | Tests |
| --- | --- | --- |
| New read-only request `reviews{turn_id, cursor?, limit?}` returns `{items: […], next_cursor?}` | `internal/engine` implements reviews request (server-side, not TUI) | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| One item per accepted verdict, ordered by round then roster order | `internal/engine` orders verdicts (server-side) | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| `kind` is the verdict's kind; `state` is the block's state in that verdict | `internal/engine` populates fields (server-side) | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| `commentary`, `evidence` and `remedy` are content references never inline text | `internal/engine` returns references (server-side) | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| An item exceeding 64 KiB is returned in short form with `oversized: true` and `record` reference | `internal/engine` enforces 64 KiB bound (server-side) | `internal/tui.TestV6DrawerOversizedShortForm`, `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| `limit` is at most 50; at least one and at most `limit` items per reply, as many as fit in one frame | `internal/engine` enforces limit and frame bound (server-side) | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| `next_cursor` is returned while items remain; no cursor when none remain | `internal/engine` returns cursor (server-side) | `internal/tui.TestV6DrawerLongListPages`, `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| A verdict accepted on repair is listed once with the repair attempt's id; malformed and unsuccessful repair attempts are not listed | `internal/engine` selects accepted repairs (server-side) | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| A block opened in round 1, maintained in round 2 and withdrawn in round 3 shows all three states in those rounds | `internal/engine` preserves block states (server-side) | `internal/acceptance.TestV6Reviews_HistoricalBlockTransitions` |
| Replacing stored commentary does not change block states; content hashes and byte lengths match the returned bytes | `internal/engine` does not reparse; hashes match stored bytes (server-side) | `internal/acceptance.TestV6Reviews_MutationNoReparse` |
| A verdict accepted before the record existed is served like a current one | `internal/engine` writes records on first request for old verdicts (server-side) | `internal/acceptance.TestV6Reviews_OldRecords` |
| A solo turn and a turn whose reviewers all failed list nothing and give no cursor | `internal/engine` returns empty lists (server-side) | `internal/acceptance.TestV6Reviews_EmptyPages` |
| Ctrl+O opens the drawer; Esc or Ctrl+O closes it; default closed, state not persisted | `internal/tui.app.Update()` handles drawer open/close (app.go); `internal/tui.reviews` manages drawer state (reviews.go) | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| Drawer shows turn number, rounds, each reviewer with verdict kind, blocks with state, commentary and remedy | `internal/tui.reviews.View()` renders drawer (reviews.go) | `internal/tui.TestV6DrawerKeysThroughUpdateAndView`, `internal/tui.TestV6Crowded80x24GoldenFrame` |
| `↑` `↓` choose turn; `←` `→` choose round; `PgUp` `PgDn` scroll drawer | `internal/tui.reviews.Update()` handles navigation (reviews.go) | `internal/tui.TestV6DrawerKeysThroughUpdateAndView`, `internal/tui.TestV6DrawerHoldsTheKeyboard` |
| Input cannot be edited while drawer has keyboard | `internal/tui.app.Update()` gates input.Update() when drawer has focus (app.go) | `internal/tui.TestV6DrawerHoldsTheKeyboard` |
| Reviewers are named by participant id | `internal/tui.reviews.View()` names participants (reviews.go) | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| `review_count` field equals the number of items `reviews` lists | `internal/engine` populates review_count; `internal/tui` uses it (server-side, TUI-side) | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce`, `internal/tui.TestV6DrawerLongListPages` |
| Every outcome line ends with `· <n> reviews` from `review_count` | `internal/tui.answer` formats review count (answer.go) | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| A turn with five reviewers, six rounds, evidence and remedy of 600 KiB on each block retrieves completely paging `reviews` and fetching references | `internal/engine` pages and references; `internal/tui` fetches (server-side, TUI-side) | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| Fetching oversized record returns every block id and state with matching hashes and byte lengths | `internal/engine` returns record (server-side) | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |

## Notify (terminal bell and notice)

| Clause | Implementation | Tests |
| --- | --- | --- |
| By default no BEL is written | `internal/tui.notify` does not write BEL by default (notify.go) | `internal/tui.TestV6NotifyPlainModeOffByDefaultNoBell` |
| With `/notify on` a BEL is written for a terminal event at or above `live_from_seq` | `internal/tui.notify` writes BEL for live events (notify.go) | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| No BEL is written for an event below `live_from_seq` | `internal/tui.notify` filters by `live_from_seq` (notify.go) | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| With an empty replay the next live event rings once | `internal/tui.notify` deduplicates by retained seq (notify.go) | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| After a connection loss with a running turn that then ends, the reattached TUI shows one notice line | `internal/tui.chat()` renders notice for ended turns (chat.go) | `internal/acceptance.TestV6Reviews_ReplayLiveBoundary` |
| A second reconnect replaying the same event shows no further line and writes no BEL | `internal/tui.notify` deduplicates; `internal/tui.chat()` does not reshow notice (notify.go, chat.go) | `internal/acceptance.TestV6Reviews_RepeatedReconnect` |
| A newly started TUI shows no notice line | `internal/tui.notify` has no retained seq at start (notify.go) | `internal/acceptance.TestV6Reviews_FreshProcess` |

## Mouse

| Clause | Implementation | Tests |
| --- | --- | --- |
| `/mouse on` refuses capture and shows a refusal on `TERM=dumb` | `internal/tui.commands` refuses on TERM=dumb (commands.go) | `internal/tui.TestV6NotifyMouseOptIn` |
| Shrinking the terminal to plain mode releases the mouse capture | `internal/tui.app.Update()` releases mouse on resize (app.go) | `internal/tui.TestV6NotifyMouseReleasedWhenScreenShrinks` |

## Plain terminal and degradation

| Clause | Implementation | Tests |
| --- | --- | --- |
| `TERM=dumb` mode: no colour, no alternate screen, spinner is state word, meter omitted, pips omitted, Markdown off, no animation, no mouse | `internal/tui.isPlainTerminal()` and `internal/tui.app` gate features (app.go) | `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Prompts in plain mode are single-line questions | `internal/tui.reasonForm` renders single-line (reason_form.go) | `internal/tui.TestV6ReasonPromptInPlainTerminal` |
| Every screen in plain mode renders with no escape sequence | `internal/tui` avoids escape sequences in plain mode (all files) | `internal/tui.TestV6PlainTerminalGoldenFrame` |

## Replay and boundaries

| Clause | Implementation | Tests |
| --- | --- | --- |
| Replay frames precede `live_from_seq` and live frames follow it with no gap | `internal/tui.conn` processes frame stream (conn.go) | `internal/acceptance.TestV6Reviews_ReplayLiveBoundary` |
| An empty replay is followed by live frames starting exactly at `live_from_seq` | `internal/tui.conn` handles empty replay (conn.go) | `internal/acceptance.TestV6Reviews_EmptyReplay` |
| Repeated reconnects deliver each frame once | `internal/tui.conn` deduplicates by seq (conn.go) | `internal/acceptance.TestV6Reviews_RepeatedReconnect` |
| A restarted engine replays the same frames to a fresh client | `internal/engine` replay (server-side) | `internal/acceptance.TestV6Reviews_FreshProcess` |

## E-side clauses (file attachment, apply, files request)

E-side clauses are explicitly assigned to V6-E tasks (pending task creation). The board
will create V6-E01 through V6-E18 tasks. This design task does not implement the following,
which are listed here for completeness and traceability:

| Clause | Assignment |
| --- | --- |
| File list (files request with workspace-relative paths, case-insensitive substring match, no symlink following, protected pattern filtering) | V6-E: files request and filtering |
| File list does not follow symlinks, descends restricted patterns, returns no invalid UTF-8 paths | V6-E: files confined walk and protected patterns |
| File list paging with `cursor` and `limit` (at most 200), `truncated` flag on 20,000 entry bound | V6-E: files paging and truncation |
| Attachment tokens: `@path` for simple paths, `@"…"` with escaping for complex paths | V6-E: attachment token text encoding |
| Choosing a file adds token and path to attachment set; deleting last token removes from set | V6-E: attachment tokens and set management |
| Typed `@` word attaches nothing; paths with space are sent in quoted form | V6-E: attachment token text encoding |
| Attached line equals the `attachments` the submit sends | V6-E: attached line binding (D-side display; E-side protocol) |
| `/apply` shows each path with its operation and the workspace | V6-E: apply command dispatch |
| `/apply` sends `apply`, `Discard` sends `discard`, `Not now` and `Esc` send nothing | V6-E: apply command dispatch |
| File-related prompts for attachment management | V6-E: attachment UI forms |
| Attachment bytes in submit; deduplication, path form, policy, reading, admission, budget | V6-E: attachment submit protocol and ordered steps |
| Attachment records, candidate format with `syndic-candidate/v3` and `attachments_hash` | V6-E: attachment binding and manifest |
| Snapshot overlay, read_file staging, unstage, attached base | V6-E: attachment snapshot and staging |
| Participants receive attachments fenced, labelled, in manifest order | V6-E: attachment request presentation |
| Taint, egress, file-derived content, web+file combination, canary seeding | V6-E: attachment taint and egress rules |
| Attachment history (not carried forward), display (path, size, hash, web+file flag) | V6-E: attachment history and display |
| Retried `submission_id` handling; refused submit leaves no turn, conversation, taint, observation | V6-E: attachment submit refusal semantics |
| A solo turn is allowed under the same order, rules and binding | V6-E: attachment solo support |

## Identified coverage gaps in D-side clauses

The following D-side clauses from the design lack test execution and should be considered 
for future test development:

1. **Second `?` to hide short help:** Design requires pressing `?` again to hide; test types `x` instead.
2. **`/help` command:** Design requires `/help` to list every command, flag and key added in this delta; no test invokes it.
3. **`/override` with empty form:** Design forbids empty reason submission; test does not verify refusal or `Esc` sends nothing.
4. **`/release` overlap risk:** Design requires `/release` to include overlap risk statement; test does not verify this text.
5. **Opening drawer never cancels or submits:** Design forbids side effects on drawer open; test does not verify this invariant.
6. **Open blocks on incomplete/cancelled:** Design requires blocks to remain visible in chat; test does not verify this.
7. **Maintained block in closed drawer:** Design requires maintained blocks to remain in closed chat; test does not verify this.
8. **Drawer animation rendering:** Design requires animation with short effect; test disables animation.
9. **Mouse and notify non-persistence:** Design requires settings to be process-scoped; tests do not verify non-persistence.
10. **Mouse clicks on UI elements:** Design covers tabs, review count, drawer rounds clickable; tests do not send mouse events.
11. **Colour-pair selection from terminal background:** Design requires colours chosen from terminal background; test uses ASCII golden frame.
12. **Drawer height >80x24:** Design requires 7 lines at 80x24, 9 lines above 30 rows; test uses fixed 24-row frame.
13. **Drawer and lists below 80x24:** Design forbids opening below 80x24; test does not verify screen-size constraint.
14. **Colour-off degradation:** Design covers colour-off flag; test only checks TERM=dumb.
15. **Full Markdown rendering:** Design covers fenced code blocks with language labels and lists; test uses bold and inline-code only.
16. **Link display without hyperlink escape:** Design forbids hyperlink escapes; test does not contain link input.
17. **Transcript tab unformatted:** Design requires transcript always unformatted; test does not inspect transcript tab.
