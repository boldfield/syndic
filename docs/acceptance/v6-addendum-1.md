# V6 addendum 1 acceptance ledger

This ledger records which test proves each acceptance line of v6 addendum 1
(`docs/design/v6-addendum-1-status-line.md`, ratified at merge commit
`adc78486d06112fe4c04dcf504cc24c2e563d90c`, SHA-256
`e7354769505b2ed7cd2d5c1f2281f63f25512a8e9951867c6c222e1f6f8e5250`): the 11 lines of
section 2 and the 20 lines of section 4. The tests are those of V6A-01 to V6A-06. Each
row quotes a line and names, as `package.TestName`, the tests that prove it.
`internal/tui.TestV6Addendum1Ledger` fails if the table does not have 31 rows or if a
named test does not exist. Whether a test proves its line was decided by reading it.

The shared case table in `internal/tui/followtest` is run against the chat, the
transcript and the drawer by `TestChatFollowCases`, `TestTranscriptFollowCases`,
`TestDrawerFollowCases` and `TestEveryScrollingViewPassesTheSharedCases`; its cases are
named after the lines of section 4.

No test cited here contacts a real endpoint or needs a credential.

## Section 2: the status line

| Line | Tests |
| --- | --- |
| At 100 columns, idle, on the chat tab, the line is `review · idle · [████░░░░░░] 48213/200000`, then spaces, then `ctrl+o: reviews  tab: switch  /help`, nothing removed | `internal/tui.TestFitStatusLineFixtures` |
| At 100 columns, in the fixture's revision state on the chat tab, the meter and `ctrl+o: reviews` are removed | `internal/tui.TestFitStatusLineFixtures` |
| At 80 columns in the same state, `round 1/2` is also removed and the turn state is whole | `internal/tui.TestFitStatusLineFixtures` |
| With the quick profile, at 80 columns in the same state, the state is shortened and the elapsed time removed | `internal/tui.TestFitStatusLineFixtures` |
| At every width from 80 to 200, in every turn state, the line is one line, ends with `tab: switch  /help`, has two spaces between its parts and contains the preset, `quick:<name>` and `tokens/limit` whole | `internal/tui.TestFitStatusLineEveryWidthAndState`, `internal/tui.TestStatusLineKeepsTokensAndHint` |
| With a preset name of 30 cells at 80 columns, and at 40 columns with the fixture, the line is no wider than the terminal, steps 6 to 8 apply in order, and it begins with as much of the preset as fits | `internal/tui.TestFitStatusLineBelowThat` |
| A preset name containing a wide character and one containing a combining character are each measured in cells, and the last cell is the terminal's last column | `internal/tui.TestFitStatusLineWideAndCombining`, `internal/tui.TestStatusLineShortensWideStatesByCell` |
| A `tokens/limit` of 15 cells with names of 12 cells at 80 columns fits by steps 1 to 5 | `internal/tui.TestFitStatusLineFullCounterAndNames` |
| On the transcript tab the line never contains `ctrl+o: reviews` and step 2 changes nothing | `internal/tui.TestFitStatusLineTranscriptTab`, `internal/tui.TestStatusLineTranscriptTabNeverOffersReviews` |
| Widening from 80 to 120 columns in one process restores the removed items | `internal/tui.TestStatusLineWideningRestoresItems` |
| `Ctrl+O` opens the drawer while `ctrl+o: reviews` is not shown | `internal/tui.TestCtrlOOpensDrawerWithoutTheHint` |

## Section 4: following

| Line | Tests |
| --- | --- |
| With the chat at the bottom, an answer taller than the window arrives and the view shows the answer's last rows and the view's last row | `internal/tui.TestChatFollowsATallAnswerAtTheBottom`, `internal/tui.TestChatFollowCases` |
| With the chat at the bottom, each of a progress line, a block and a new turn's input arrives and the last row stays shown | `internal/tui.TestChatFollowsProgressBlockAndInput`, `internal/tui.TestChatFollowCases` |
| After `PgUp`, rows arrive and the first shown row does not change; the conversation line shows `↓ <n> new lines` with the number of rows after the seen mark | `internal/tui.TestChatAwayCountsNewLines`, `internal/tui.TestChatFollowCases` |
| After scrolling back down until the last row is shown, the count is gone and the next row that arrives is shown | `internal/tui.TestChatCountClearsAtTheBottomAndFollows`, `internal/tui.TestChatFollowCases` |
| `End` from any position shows the last row and resumes following; `Home` on content taller than the view shows the first row and following stops | `internal/tui.TestChatEndAndHome`, `internal/tui.TestChatFollowCases` |
| With content shorter than the view, `Home`, `PgUp` and a scroll up each leave the view at the bottom, and the next row that arrives is shown | `internal/tui.TestChatShorterThanTheViewStaysAtTheBottom`, `internal/tui.TestChatFollowCases` |
| A view that the user scrolled to its last row by hand follows from then on | `internal/tui.TestChatScrolledToTheLastRowByHandFollows`, `internal/tui.TestChatFollowCases` |
| While away from the bottom, the terminal is made narrower so that rows above the view wrap into more rows: the first shown row is the row that contains the anchor's character | `internal/tui.TestChatNarrowerHoldsTheAnchor`, `internal/tui.TestChatNarrowerHoldsADeepAnchor`, `internal/tui.TestChatNarrowerHoldsADeepAnswerAnchor` |
| While away from the bottom on the transcript tab, an entry above the view loads its body and grows: the first shown row does not change | `internal/tui.TestTranscriptHoldsItsPlaceWhileAnEntryAboveLoads`, `internal/tui.TestDrawerHoldsItsPlaceWhenABodyAboveLoads` |
| While away from the bottom, the anchor's item is removed: the view's first row is the start of the next item | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowAnchorShrinkMovesItAndRemovalUsesPreviousOrder` |
| While away from the bottom, the anchor's item is replaced by a shorter one that ends before the anchor's character: the view's first row is that item's last row | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowAnchorShrinkMovesItAndRemovalUsesPreviousOrder` |
| While away from the bottom, the seen mark's item is replaced by a shorter one, or removed: the count is the rows after the item's end, or after the nearest earlier item's end, or every row | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowMarkBeforeFirstRowCountsEverything` |
| After each of those, a view whose last row is now shown is at the bottom and its count is cleared | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowCases` |
| While away from the bottom, a progress line below is replaced ten times: the count rises by that line's rows once | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowCases` |
| While away from the bottom, a resize changes the wrapping below the mark: the count is the number of rows after the mark at the new width | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowCases` |
| While away from the bottom, a resize that makes all of the content fit puts the view at the bottom and clears the count | `internal/tui.TestChatFollowCases`, `internal/tui.TestFollowCases` |
| The drawer opens showing its last row | `internal/tui.TestDrawerOpensShowingItsLastRow` |
| The transcript tab and the drawer each pass the same cases, with the count on their own header lines | `internal/tui.TestEveryScrollingViewPassesTheSharedCases`, `internal/tui.TestTranscriptFollowCases`, `internal/tui.TestDrawerFollowCases`, `internal/tui.TestTranscriptCountIsOnTheHeaderLine`, `internal/tui.TestDrawerCountIsOnItsHeaderLine` |
| Starting the TUI on a conversation longer than the window shows its last row | `internal/tui.TestChatStartsAtTheBottom` |
| Resizing while at the bottom keeps the last row shown | `internal/tui.TestChatResizeAtTheBottom` |

## Clauses changed in earlier designs

Addendum section 1 changes two earlier clauses, only where its steps 6 to 8 apply.

**v6 section 8, status-line acceptance line.** Was: "The status line is the last line at
every size and ends with `tab: switch  /help`." Now: the status line is the last line at
every size, and ends with `tab: switch  /help` whenever steps 1 to 5 suffice.
Held by `internal/tui.TestChatStatusLineIsLastAt80x24`,
`internal/tui.TestV6StatusLineLastAtEverySizeAndTab`,
`internal/tui.TestV5StatusLineIsLastAtEverySize` (every size, all tabs) and
`internal/tui.TestFitStatusLineEveryWidthAndState` (the hint at every width from 80 to 200).

**v4 section 4b, display of a governing quick profile.** Was: the strip shows
`quick:<name>` on every turn the profile governs. Now: shown in full whenever steps 1 to 5
suffice, and otherwise as much of it as step 8 leaves. Held by
`internal/tui.TestFitStatusLineEveryWidthAndState` (in full, at every width from 80 to 200),
`internal/tui.TestFitStatusLineFixtures` (with the state shortened) and
`internal/tui.TestFitStatusLineQuickProfileCutByStep8` (cut by step 8 while the profile governs).

The v6 section 8 line is also what `docs/acceptance/v6-presentation.md` records, in its
amended form; v5's `docs/acceptance/milestone-d.md` cites the older form, which these
tests also hold.
