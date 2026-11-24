# V6 presentation acceptance ledger

This ledger maps each clause of section 8 ("Acceptance") of
`docs/design/v6-delta-tui-presentation.md` (ratified revision 4) to the
executing tests that prove it. The design is ratified at commit
`67f43eb6a1e2d988e0675ce9e467ec2721ea9954`, SHA-256
`1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7`.

Operator decisions D1–D4 and all named acceptance scenarios are covered below
by tests that run under `go test -short` with the fake adapter, `httptest`,
temporary files and sockets. None contacts a real endpoint or requires a
credential. The mirror test `TestV6PresentationLedgerNamesRealTests` in
`internal/tui/v6_acceptance_test.go` reads this file, requires every table
row to name at least one test, finds each named test in its package directory
and fails on a missing test or a `Skip` call.

Run the whole ledger:

    go test -short -count=1 ./internal/acceptance ./internal/tui

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

### 4. Prompts

| Clause | Tests |
| --- | --- |
| `/apply` shows each path with its operation and the workspace | `internal/tui.TestV6OverrideReasonForm` |
| `/apply` sends `apply`, `Discard` sends `discard`, `Not now` and `Esc` send nothing | `internal/tui.TestV6OverrideReasonForm` |
| `/override` and `/release` without a reason open a form with one required field | `internal/tui.TestV6OverrideReasonForm`, `internal/tui.TestV6ReleaseReasonForm` |
| Submitting a prompt sends the command with the reason; an empty reason cannot be submitted | `internal/tui.TestV6OverrideReasonForm`, `internal/tui.TestV6ReleaseReasonForm` |
| Prompts in a plain terminal are single-line questions | `internal/tui.TestV6ReasonPromptInPlainTerminal` |

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

### 9. File list and attachments

| Clause | Tests |
| --- | --- |
| File list and attachments are E-side only; no attachment or file controls appear | `internal/tui.TestV6NoEOnlyControls` |

## Display: safe text, content lines and outcome rendering

| Clause | Tests |
| --- | --- |
| Every C0 control except LF and tab, DEL, every C1 control and every Unicode bidi/isolate character is replaced by a visible escape | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInDrawer`, `internal/tui.TestV6AdversarialContentInTranscript` |
| A byte not valid UTF-8 is shown as `\xNN` | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript` |
| Tab is shown as spaces | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript` |
| Every displayed content line is prefixed with two spaces after wrapping; no content reaches column 0 or 1 | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6Crowded80x24GoldenFrame` |
| A user input begins `› ` in column 0, continuation lines are content lines | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| An outcome line begins with a one-character mark and a space: `✓` approved, `!` incomplete/overridden, `×` cancelled, `·` solo | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| `approved by <ids> after 1 revision` for n=1; unchanged for n≥2 | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| Every v4 and v5 banner string appears verbatim after its mark | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| `/raw on` shows answer safe text with no Markdown; `transcript` content returns journal bytes unchanged | `internal/tui.TestV6RawAndMarkdownThroughCommands` |
| Answer and reviewer commentary containing escape sequences, carriage returns and bidi overrides are shown with visible escapes in chat, transcript and drawer with `/raw` on and off | `internal/tui.TestV6AdversarialContentThroughChat`, `internal/tui.TestV6AdversarialContentInTranscript`, `internal/tui.TestV6AdversarialContentInDrawer` |
| Status line is last at every size and ends with `tab: switch  /help` | `internal/tui.TestV6Crowded80x24GoldenFrame`, `internal/tui.TestV6PlainTerminalGoldenFrame` |

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
| `review_count` equals the number of verdicts `reviews` lists | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Ctrl+O opens the drawer; Esc or Ctrl+O closes it; default closed, state not persisted | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| Drawer shows turn number, rounds, each reviewer with verdict kind, blocks with state, commentary and remedy | `internal/tui.TestV6DrawerKeysThroughUpdateAndView`, `internal/tui.TestV6Crowded80x24GoldenFrame` |
| `↑` `↓` choose turn; `←` `→` choose round; `PgUp` `PgDn` scroll drawer | `internal/tui.TestV6DrawerKeysThroughUpdateAndView`, `internal/tui.TestV6DrawerHoldsTheKeyboard` |
| Input cannot be edited while drawer has keyboard | `internal/tui.TestV6DrawerHoldsTheKeyboard` |
| Open blocks on `incomplete` or `cancelled` turns stay in chat whether drawer is open | `internal/tui.TestV6DrawerKeysThroughUpdateAndView` |
| Reviewers are named by participant id | `internal/tui.TestV6Crowded80x24GoldenFrame` |
| Every outcome line ends with `· <n> reviews` from `review_count` | `internal/tui.TestV6OutcomeWordsMarksAndReviewCount` |
| A turn with five reviewers, six rounds, evidence and remedy of 600 KiB on each block retrieves completely paging `reviews` and fetching references | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |
| Fetching oversized record returns every block id and state with matching hashes and byte lengths | `internal/acceptance.TestV6Reviews_OversizedFieldsAndMetadata` |

## Lists and prompts

| Clause | Tests |
| --- | --- |
| File list (milestone E) and attachments are not shown or functional | `internal/tui.TestV6NoEOnlyControls` |
| Tab switches tabs with no list open and completes with one open | `internal/tui.TestV6CommandCompletionAndShortHelp` |
| `/conversations` is closed by default and re-opens at each invocation | `internal/tui.TestV6ChooserThroughUpdateAndView` |
| A conversation from another workspace is listed dimmed and cannot be chosen | `internal/tui.TestV6ChooserThroughUpdateAndView` |

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

## Plain terminal

| Clause | Tests |
| --- | --- |
| `TERM=dumb` mode: no colour, no alternate screen, spinner is state word, meter omitted, pips omitted, Markdown off, no animation, no mouse | `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Prompts in plain mode are single-line questions | `internal/tui.TestV6ReasonPromptInPlainTerminal` |
| Every screen in plain mode renders with no escape sequence | `internal/tui.TestV6PlainTerminalGoldenFrame` |
| Mouse never captured under TERM=dumb with `--mouse` | `internal/tui.TestV6NotifyMouseOptIn` |

## Replay and notifications

| Clause | Tests |
| --- | --- |
| Replay frames precede `live_from_seq` and live frames follow it with no gap | `internal/acceptance.TestV6Reviews_ReplayLiveBoundary` |
| An empty replay is followed by live frames starting exactly at `live_from_seq` | `internal/acceptance.TestV6Reviews_EmptyReplay` |
| Repeated reconnects deliver each frame once | `internal/acceptance.TestV6Reviews_RepeatedReconnect` |
| A restarted engine replays the same frames to a fresh client | `internal/acceptance.TestV6Reviews_FreshProcess` |
