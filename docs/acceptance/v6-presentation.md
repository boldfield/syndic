# V6 presentation acceptance ledger

This ledger maps every normative clause of `docs/design/v6-delta-tui-presentation.md`
(ratified revision 4, commit `67f43eb6a1e2d988e0675ce9e467ec2721ea9954`, SHA-256
`1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7`) to the
executing tests that prove it. D-side clauses (sections 1–3, 5–6) map to implementation
with executed tests. E-side clauses (file list, attachments, file-related prompts; sections
3–4, 5.9) are explicitly assigned to V6-E tasks (not covered here). Both D and E accept
against the fake adapter with `httptest`, temporary files and sockets; no test contacts a
real endpoint or credential.

## Dependency inventory and visual reference provenance

The dependency inventory decision: Operator accepted all six TUI modules (Lip Gloss, Bubbles,
Glamour, Huh, Harmonica, bubblezone) and their transitive modules on 2026-11-22, documented
in `docs/dependencies/v6-tui.md`. Implementation: `go mod tidy` committed the pinned versions;
test coverage is implicit (builds succeed, no test-time module resolution).

Visual reference provenance: Operator chose the visual direction from mock-ups on a private
design canvas on 2026-11-21 (Ledger layout with reviews drawer and nine additions). Reference
frames (`internal/tui/testdata/v6/*.txt`) are illustrations; where they disagree with the
ratified text, the text governs. Twelve disagreements recorded in
`internal/tui/testdata/v6/README.md`; design governs each. Verification: mock-ups inform test
fixtures and golden frames; `TestV6PlainTerminalGoldenFrame`, `TestV6Crowded80x24GoldenFrame`,
`TestV6OutcomesGoldenFrame` validate that the actual rendering matches the frames where the
design governs.

## Validation evidence

Run the entire ledger:

    go test -short -count=1 ./internal/acceptance ./internal/tui

Expected result: all tests pass, no skipped tests in this ledger. The mirror test
`TestV6PresentationLedgerNamesRealTests` in `internal/tui/v6_acceptance_test.go` verifies
that every table row names at least one test, that each named test exists, and that none
calls `Skip` or `Skipf`.

## Operator decision D1: Ledger layout with reviews drawer

| Clause | Tests |
| --- | --- |
| The base layout is Ledger with the reviews drawer | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6OutcomesGoldenFrame` |

## Operator decision D2: Nine additions

### 1. Command list

| Clause | Tests |
| --- | --- |
| `/` enters command mode; `↑` `↓` select, `Tab` completes, `Esc` closes | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `Tab` completes with a list open and switches tabs otherwise | `internal/tui.TestV6CommandCompletionAndShortHelp` |

### 2. Round pips

| Clause | Tests |
| --- | --- |
| Round pips are shown in the progress line, one per allowed round; filled for started, empty for future | `internal/tui.TestV6PlainTerminalGoldenFrame`, `internal/tui.TestV6Crowded80x24GoldenFrame` |

### 3. Mouse

| Clause | Tests |
| --- | --- |
| Mouse is off by default on a capable terminal | `internal/tui.TestV6NotifyMouseOptIn` |
| `/mouse on` enables capture on a capable terminal | `internal/tui.TestV6NotifyMouseOptIn` |
| `/mouse on` refuses capture and shows a refusal on `TERM=dumb` | `internal/tui.TestV6NotifyMouseOptIn` |
| Tabs can be clicked with the mouse enabled | `internal/tui.TestV6NotifyMouseOptIn` |
| A `reviews` count opens the drawer when clicked | `internal/tui.TestV6NotifyMouseOptIn` |
| A round in the drawer header selects it when clicked | `internal/tui.TestV6NotifyMouseOptIn` |
| Shrinking the terminal to plain mode releases the mouse capture | `internal/tui.TestV6NotifyMouseReleasedWhenScreenShrinks` |

### 4. Prompts (D-side)

| Clause | Tests |
| --- | --- |
| `/override` and `/release` without a reason open a form with one required field | `internal/tui.TestV6OverrideReasonForm`, `internal/tui.TestV6ReleaseReasonForm` |
| Submitting a prompt sends the command with the reason; an empty reason cannot be submitted | `internal/tui.TestV6OverrideReasonForm`, `internal/tui.TestV6ReleaseReasonForm` |
| `/override` with an empty form field sends nothing | `internal/tui.TestV6OverrideReasonForm` |
| Prompts in a plain terminal are single-line questions | `internal/tui.TestV6ReasonPromptInPlainTerminal` |

### 4. Prompts (E-side)

| Behavior | Assignment |
| --- | --- |
| `/apply` shows each path with its operation and the workspace | V6-E: file attachment and apply logic |
| `/apply` sends `apply`, `Discard` sends `discard`, `Not now` and `Esc` send nothing | V6-E: apply command dispatch |

### 5. Filter in `/conversations`

| Clause | Tests |
| --- | --- |
| `/conversations` is a selectable list; `/` filters it, `↑` `↓` select, `Enter` switches, `n` starts new, `Esc` closes | `internal/tui.TestV6ChooserThroughUpdateAndView` |

### 6. Short help

| Clause | Tests |
| --- | --- |
| `?` on an empty input shows one line of keys above the status line | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `?` again hides it | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `/help` lists every command and key | `internal/tui.TestV6CommandCompletionAndShortHelp` |

### 7. Drawer animation

| Clause | Tests |
| --- | --- |
| The drawer opens and closes with a short animation | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |

### 8. Turn-end bell and notice

| Clause | Tests |
| --- | --- |
| By default no BEL is written | `internal/tui.TestV6NotifyPlainModeOffByDefaultNoBell` |
| With `/notify on` a BEL is written for a terminal event at or above `live_from_seq` | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| No BEL is written for an event below `live_from_seq` | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| With an empty replay the next live event rings once | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| After a connection loss with a running turn that then ends, the reattached TUI shows one notice line | `internal/acceptance.TestV6Reviews_ReplayLiveBoundary` |
| A second reconnect replaying the same event shows no further line and writes no BEL | `internal/acceptance.TestV6Reviews_RepeatedReconnect` |
| A newly started TUI shows no notice line | `internal/acceptance.TestV6Reviews_FreshProcess` |

### 9. File list and attachments (D-side visibility)

| Clause | Tests |
| --- | --- |
| File list and attachments do not appear in the chat tab | `internal/tui.TestV6NoEOnlyControls` |

### 9. File list and attachments (E-side tasks)

| Behavior | Assignment |
| --- | --- |
| Attachment tokens and chosen files in `/conversations` and `/apply` | V6-E: file list, attachment tokens, files request |
| Attachment bytes in submit; deduplication, path form, policy, reading, admission, budget | V6-E: attachment submit protocol and ordered steps |
| Attachment records, candidate format with `syndic-candidate/v3` and `attachments_hash` | V6-E: attachment binding and manifest |
| Snapshot overlay, read_file staging, unstage, attached base | V6-E: attachment snapshot and staging |
| Participants receive attachments fenced, labelled, in manifest order | V6-E: attachment request presentation |
| Taint, egress, file-derived content, web+file combination, canary seeding | V6-E: attachment taint and egress rules |
| Attachment history (not carried forward), display (path, size, hash, web+file flag) | V6-E: attachment history and display |
| Retried `submission_id` handling; refused submit leaves no turn, conversation, taint, observation | V6-E: attachment submit refusal semantics |
| A solo turn is allowed under the same order, rules and binding | V6-E: attachment solo support |

## Display: safe text, content lines and outcome rendering

| Clause | Tests |
| --- | --- |
| Every C0 control except LF and tab, DEL, every C1 control and every Unicode bidi/isolate character is replaced by a visible escape | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInDrawer`, `internal/tui.TestV6AdversarialContentInTranscript` |
| A byte not valid UTF-8 is shown as `\xNN` | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript` |
| Tab is shown as spaces | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript` |
| Every displayed content line is prefixed with two spaces after wrapping; no content reaches column 0 or 1 | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6Crowded80x24GoldenFrame` |
| An answer whose text is an outcome line, and one using CR or cursor movement to reach column 0, are shown indented and unmarked | `internal/tui.TestV6AdversarialContentThroughChat` |
| A user input begins `› ` in column 0, continuation lines are content lines | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| An outcome line begins with a one-character mark and a space: `✓` approved, `!` incomplete/overridden, `×` cancelled, `·` solo | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| `approved by <ids> after 1 revision` for n=1; unchanged for n≥2 | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| Every v4 and v5 banner string appears verbatim after its mark | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| `/raw on` shows answer safe text with no Markdown; `transcript` content returns journal bytes unchanged | `internal/tui.TestV6RawAndMarkdownThroughCommands` |
| Answer and reviewer commentary containing escape sequences, carriage returns and bidi overrides are shown with visible escapes in chat, transcript and drawer with `/raw` on and off | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript`, `internal/tui.TestV6AdversarialContentInDrawer` |
| Links are shown as text with the URL visible; no hyperlink escape sequence is emitted | `internal/tui.TestV6RawAndMarkdownThroughCommands` |
| Status line is last at every size and ends with `tab: switch  /help` | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Status line elements (meter, reviews hint, round text, state) are hidden in order when width is insufficient; `tab: switch  /help` is always kept | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |

## Drawer wire contract and behavior

| Clause | Tests |
| --- | --- |
| New read-only request `reviews{turn_id, cursor?, limit?}` returns `{items: […], next_cursor?}` | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| One item per accepted verdict, ordered by round then roster order | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| `kind` is the verdict's kind; `state` is the block's state in that verdict | `internal/acceptance.TestV6Reviews_HistoricalBlockTransitions` |
| `commentary`, `evidence` and `remedy` are content references never inline text | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| An item exceeding 64 KiB is returned in short form with `oversized: true` and `record` reference | `internal/tui.TestV6DrawerOversizedShortForm`, `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| `limit` is at most 50; at least one and at most `limit` items per reply, as many as fit in one frame | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| `next_cursor` is returned while items remain; no cursor when none remain | `internal/tui.TestV6DrawerLongListPages`, `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| A verdict accepted on repair is listed once with the repair attempt's id; malformed and unsuccessful repair attempts are not listed | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce` |
| A block opened in round 1, maintained in round 2 and withdrawn in round 3 shows all three states in those rounds | `internal/acceptance.TestV6Reviews_HistoricalBlockTransitions` |
| Replacing stored commentary does not change block states; content hashes and byte lengths match the returned bytes | `internal/acceptance.TestV6Reviews_MutationNoReparse` |
| A verdict accepted before the record existed is served like a current one | `internal/acceptance.TestV6Reviews_OldRecords` |
| A solo turn and a turn whose reviewers all failed list nothing and give no cursor | `internal/acceptance.TestV6Reviews_EmptyPages` |
| Ctrl+O opens the drawer; Esc or Ctrl+O closes it; default closed, state not persisted | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| Opening the drawer never cancels or submits anything | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| Drawer shows turn number, rounds, each reviewer with verdict kind, blocks with state, commentary and remedy | `internal/tui.TestV6DrawerKeysThroughUpdateAndView`, `internal/tui.TestV6Crowded80x24GoldenFrame` |
| `↑` `↓` choose turn; `←` `→` choose round; `PgUp` `PgDn` scroll drawer | `internal/tui.TestV6DrawerKeysThroughUpdateAndView`, `internal/tui.TestV6DrawerHoldsTheKeyboard` |
| Input cannot be edited while drawer has keyboard | `internal/tui.TestV6DrawerHoldsTheKeyboard` |
| Open blocks on `incomplete` or `cancelled` turns stay in chat whether drawer is open | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| A turn with a maintained block shows it in the chat with the drawer closed | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| Reviewers are named by participant id | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| `review_count` field equals the number of items `reviews` lists | `internal/acceptance.TestV6Reviews_AcceptedRepairOnce`, `internal/tui.TestV6DrawerLongListPages` |
| Every outcome line ends with `· <n> reviews` from `review_count` | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| A turn with five reviewers, six rounds, evidence and remedy of 600 KiB on each block retrieves completely paging `reviews` and fetching references | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| Fetching oversized record returns every block id and state with matching hashes and byte lengths | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |

## Lists and prompts (D-side)

| Clause | Tests |
| --- | --- |
| Command list shows matching commands with descriptions and current values | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Tab switches tabs with no list open and completes with one open | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `/conversations` is closed by default and re-opens at each invocation | `internal/tui.TestV6ChooserThroughUpdateAndView` |
| A conversation from another workspace is listed dimmed and cannot be chosen | `internal/tui.TestV6ChooserThroughUpdateAndView` |

## Lists and prompts (E-side tasks)

| Behavior | Assignment |
| --- | --- |
| File list (`files` request, workspace-relative paths, case-insensitive substring match) | V6-E: files request and filtering |
| File list does not follow symlinks, descends restricted patterns, returns no invalid UTF-8 paths | V6-E: files confined walk and protected patterns |
| File list paging with `cursor` and `limit` (at most 200), `truncated` flag on 20,000 entry bound | V6-E: files paging and truncation |
| Chosen file adds token and path to attachment set; deleting last token removes from set | V6-E: attachment tokens and set management |
| Typed `@` word attaches nothing; paths with space are sent in quoted form | V6-E: attachment token text encoding |
| Attached line equals the `attachments` the submit sends | V6-E: attached line binding |

## Heights and viewport management

| Clause | Tests |
| --- | --- |
| At 80x24 with drawer open, eight attachments, three banners, six-line input: tab bar, at least five chat lines, drawer, identity warning, attached line with `+<n> more`, at least one input line and status line fit | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Tab bar and status line are one line each and always shown | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Input shows 1 to 6 lines and scrolls beyond that | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Banners take at most 2 lines; excess ends `+<n> more` | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Attached line is one line with paths until width runs out, then `+<n> more` | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Command or file list takes at most 5 lines and closes when drawer opens | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Drawer takes 7 lines or 9 above 30 rows | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Active tab content gets what remains and never fewer than 5 lines | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Below 80x24 drawer and lists do not open; plain-terminal rule governs | `internal/tui.TestV6CommandCompletionAndShortHelp` |

## Plain terminal and degradation

| Clause | Tests |
| --- | --- |
| `TERM=dumb` mode: no colour, no alternate screen, spinner is state word, meter omitted, pips omitted, Markdown off, no animation, no mouse | `internal/tui.TestV6PlainTerminalGoldenFrame` |
| When colour is off: spinner is the word for the state, meter omitted, pips omitted, Markdown off, no animation, no mouse | `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Prompts in plain mode are single-line questions | `internal/tui.TestV6ReasonPromptInPlainTerminal` |
| Every screen in plain mode renders with no escape sequence | `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Mouse never captured under TERM=dumb with `--mouse` or with colour off | `internal/tui.TestV6NotifyMouseOptIn` |

## Colour and rendering

| Clause | Tests |
| --- | --- |
| Colours are declared as light/dark pairs and chosen from terminal background | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| No meaning is carried by colour alone; every outcome has its mark and words | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| Answer text is rendered as Markdown (inline code, lists, emphasis, fenced code with language label) from safe text | `internal/tui.TestV6RawAndMarkdownThroughCommands` |
| `/raw on|off` toggles Markdown rendering; raw means unformatted safe text, not terminal-raw bytes | `internal/tui.TestV6RawAndMarkdownThroughCommands` |
| Transcript tab is always unformatted safe text | `internal/tui.TestV6RawAndMarkdownThroughCommands` |

## Status line and UI elements

| Clause | Tests |
| --- | --- |
| Status line shows: preset; `quick:<name>` when one governs; turn state or `idle`; `elapsed/turn_deadline` while turn runs | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Status line shows: budget meter and `tokens/limit`; `round n/m` during revision | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Status line ends with `ctrl+o: reviews` when chat tab is active, then `tab: switch  /help` | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| When status line does not fit, elements hidden in order: meter, reviews hint, round text, then state shortened; `tab: switch  /help` always kept | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Identity warning is a banner, never folded into count, sort first among banners | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Progress line shows spinner, phase, round pips (filled for started, empty for future), each participant with status, elapsed time | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Conversation line shows earlier turns above view and scroll percentage | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Transcript and changes tabs show scroll percentage | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Input prompt is `┃ `; multi-line text area scrolls beyond 6 lines | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Banners take at most 2 lines; excess ends `+<n> more` with identity warning always shown | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Attached line is one line: paths until width runs out, then `+<n> more` | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Command or file list takes at most 5 lines and closes when drawer opens | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Short help line is one line, hidden while drawer is open | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Drawer takes 7 lines, or 9 above 30 rows | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Active tab content gets what remains and never fewer than 5 lines | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| When opening drawer would leave fewer than 5 lines for tab, input shrinks to 1 line and banners to 1 line first | `internal/tui.TestV6Crowded80x24GoldenFrame` |

## Command list and short help

| Clause | Tests |
| --- | --- |
| When input begins with `/` and contains no space, matching commands list appears above input | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| Command list shows each command with one-line description and current value where applicable | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `↑` `↓` select in list; `Tab` completes; `Esc` closes; while list open, `Tab` completes instead of switching tabs | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `?` on empty input shows one line of applicable keys above status line; `?` again hides it | `internal/tui.TestV6CommandCompletionAndShortHelp` |

## Drawer animation

| Clause | Tests |
| --- | --- |
| Drawer opens and closes with a short animation | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |

## Notify and mouse behaviour

| Clause | Tests |
| --- | --- |
| Mouse is off by default on a capable terminal; `/mouse on` enables capture; `--mouse` enables at start | `internal/tui.TestV6NotifyMouseOptIn` |
| Mouse capture is never stored; the setting lasts for the TUI process only | `internal/tui.TestV6NotifyMouseOptIn` |
| Tabs can be clicked with mouse enabled | `internal/tui.TestV6NotifyMouseOptIn` |
| A `reviews` count opens the drawer when clicked | `internal/tui.TestV6NotifyMouseOptIn` |
| A round in the drawer header selects it when clicked | `internal/tui.TestV6NotifyMouseOptIn` |
| Shrinking terminal to plain mode releases mouse capture | `internal/tui.TestV6NotifyMouseReleasedWhenScreenShrinks` |
| BEL (terminal bell) is off by default; `/notify on` enables it; `--notify` enables at start | `internal/tui.TestV6NotifyPlainModeOffByDefaultNoBell` |
| Notify setting is never stored; the setting lasts for the TUI process only | `internal/tui.TestV6NotifyPlainModeOffByDefaultNoBell` |
| With `/notify on`, BEL is written for a terminal event at or above `live_from_seq` | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| No BEL for an event below `live_from_seq`; with empty replay, next live event rings once | `internal/tui.TestV6NotifyPlainModeBellAndNoEscape` |
| After connection loss with running turn that ends, reattached TUI shows one notice line | `internal/acceptance.TestV6Reviews_ReplayLiveBoundary` |
| Second reconnect replaying same event shows no further line and writes no BEL | `internal/acceptance.TestV6Reviews_RepeatedReconnect` |
| Newly started TUI shows no notice line | `internal/acceptance.TestV6Reviews_FreshProcess` |
| `/help` lists every command, flag and key added in this delta | `internal/tui.TestV6CommandCompletionAndShortHelp` |

## Replay and boundaries

| Clause | Tests |
| --- | --- |
| Replay frames precede `live_from_seq` and live frames follow it with no gap | `internal/acceptance.TestV6Reviews_ReplayLiveBoundary` |
| An empty replay is followed by live frames starting exactly at `live_from_seq` | `internal/acceptance.TestV6Reviews_EmptyReplay` |
| Repeated reconnects deliver each frame once | `internal/acceptance.TestV6Reviews_RepeatedReconnect` |
| A restarted engine replays the same frames to a fresh client | `internal/acceptance.TestV6Reviews_FreshProcess` |
