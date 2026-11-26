# v6 addendum 1: fitting the status line, and following new content

Revision 3, DRAFT, dated November 26, 2026. Author: Fable. Not approved, not hash-bound. Revision 2 (13,203 bytes, SHA-256 `2451382d88d698f8c946ef92101a60a88cb4f945094b8de082d93afc22cb86a9`) was approved by Muse and blocked by Codex on November 26, 2026; revision 1 was blocked by both; section 5 records the rounds. This addendum extends the v6 delta as ratified (35,593 bytes, SHA-256 `1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7`). It governs only the two rules below and changes two earlier clauses at extreme widths, named in section 1; every other earlier requirement stands. Peer review of this addendum does not by itself authorize implementation.

## 0. Why

v6 section 1 lists what the status line carries and fixes its right edge, but not what gives way when the line is too long. At 80 columns the items of a turn in revision on the chat tab come to about 100 cells. The gap was found while writing reference frames for the first TUI task. The operator accepted the order below on November 21, 2026.

Neither v5 nor v6 says how a scrolling view behaves as content arrives. The chat tab as built pins to the first line of the newest turn when that turn is taller than the window, and stops following once the user has scrolled to the true bottom, so new lines land out of sight. The operator reported this on November 23, 2026 and chose the rule of section 3.

## 1. The rule

The status line is one line and is never wider than the terminal. Its right part is `tab: switch  /help`, preceded on the chat tab by `ctrl+o: reviews` and two spaces. Its left part is the items of v6 section 1, in that section's order, joined by ` · `. The left part starts in column 0, the right part ends in the last column, and at least two spaces separate them.

**Measure.** Every width here is counted in terminal cells, of each item as it is drawn, that is, after any character the TUI will not write to the terminal has been replaced by what it shows instead: a wide character is two cells, a combining character none, and a replaced character is as wide as its replacement.

**Giving way.** When the two parts do not fit the width, the TUI removes or shortens items in this order, stopping as soon as they fit:

1. The budget meter is removed. `tokens/limit` stays.
2. On the chat tab, `ctrl+o: reviews` and its two spaces are removed. The key still works, and `/help` and the short help line still name it. On other tabs there is nothing to remove and this step is skipped.
3. `round n/m` is removed. The progress line still shows it.
4. A turn state longer than five cells is shortened to its first four cells followed by `…`. A shorter one is left whole.
5. `elapsed/turn_deadline` is removed. The progress line still shows the elapsed time.

Steps 1 to 5 never remove or shorten the preset, `quick:<name>` when one governs (v3 requires it to be visible on every turn it governs), `tokens/limit`, or `tab: switch  /help`.

**When that is enough.** At a width of 80 or more, with a preset name of at most 12 cells, a quick-profile name of at most 12 cells and a `tokens/limit` of at most 15 cells, steps 1 to 5 always suffice: the widest such line is 12, 18 for `quick:` and the name, 5 for the shortened state and 15, joined by three separators of 3, then 2 spaces and the 18 of the right part, which is 79.

**Below that.** Outside those conditions, a narrower terminal or a longer name or counter, the four kept items may not fit by themselves, and no rule can show them all. Then, after step 5 and stopping as soon as the line fits:

6. The turn state is removed.
7. The right part is removed whole. `Tab` and `/help` still work.
8. What remains is the preset, `quick:<name>` when one governs, and `tokens/limit`, joined by ` · `. It is cut at the terminal's width, from the right, and its last cell is `…`; a wide character that would straddle the cut is dropped whole. The preset and the quick-profile name are therefore the last to lose cells, and whatever of them fits is shown.

Two earlier clauses are changed by steps 6 to 8, and only where those steps apply. v6 section 8's acceptance line "The status line is the last line at every size and ends with `tab: switch  /help`" becomes: the status line is the last line at every size, and ends with `tab: switch  /help` whenever steps 1 to 5 suffice. v4 section 4b's requirement that a governing quick profile be shown in full becomes: shown in full whenever steps 1 to 5 suffice, and otherwise as much of it as step 8 leaves. v6 section 6's plain-terminal rule is unchanged, and steps 6 to 8 apply in a plain terminal as elsewhere.

The rule is applied to the whole line on every render, from the full set of items, so widening the terminal brings items back in reverse order.

## 2. Acceptance for the status line

The fixture for the lines below: preset `review`; quick profile, where one is named, `fastcheck123`; turn state `reviewers_dispatched`; `elapsed/turn_deadline` shown as `1:12/10:00`; the meter drawn as `[████░░░░░░]`, 12 cells, followed by one space and `tokens/limit` shown as `48213/200000`; round shown as `round 1/2`. A test builds the line from these items; where an implementation draws the meter or the times differently, the test's expected line is computed from the rule and the items as drawn, and the exact strings below hold for the fixture as given.

- At 100 columns, idle, on the chat tab, the line is `review · idle · [████░░░░░░] 48213/200000`, then spaces, then `ctrl+o: reviews  tab: switch  /help`: 78 cells of content, nothing removed.
- At 100 columns, in the fixture's revision state on the chat tab, the full line would be 119 cells; the meter and `ctrl+o: reviews` are removed, and the line is `review · reviewers_dispatched · 1:12/10:00 · 48213/200000 · round 1/2`, spaces, `tab: switch  /help`.
- At 80 columns in the same state, `round 1/2` is also removed and the turn state is whole: `review · reviewers_dispatched · 1:12/10:00 · 48213/200000`, spaces, `tab: switch  /help`.
- With the quick profile, at 80 columns in the same state, the line is `review · quick:fastcheck123 · revi… · 48213/200000`, twelve spaces, `tab: switch  /help`: the state is shortened and the elapsed time removed.
- At every width from 80 to 200, in every turn state, with names of at most 12 cells and a counter of at most 15: the line is one line, ends with `tab: switch  /help`, has at least two spaces between its parts, and contains the preset, `quick:<name>` when one governs, and `tokens/limit`, each whole.
- With a preset name of 30 cells at 80 columns, and at 40 columns with the fixture, the line is one line no wider than the terminal, steps 6 to 8 apply in order, and the line begins with as much of the preset as fits.
- A preset name containing a wide character and one containing a combining character are each measured in cells, and the line's last cell is the terminal's last column.
- A `tokens/limit` of 15 cells with names of 12 cells at 80 columns fits by steps 1 to 5.
- On the transcript tab the line never contains `ctrl+o: reviews` and step 2 changes nothing.
- Widening from 80 to 120 columns in one process restores the removed items.
- `Ctrl+O` opens the drawer while `ctrl+o: reviews` is not shown.

## 3. Following new content

This applies to every view that scrolls and grows: the chat tab, the transcript tab, and the reviews drawer. A row is one line of the view as drawn at the current width.

**At the bottom.** A view is at the bottom when its last row is shown. That is decided from what is shown after the scroll position has been clamped to the content, and from nothing else. When all of the content fits in the view, every row is shown, so the view is at the bottom whatever key was last pressed.

- A view that is at the bottom stays at the bottom as rows arrive, whatever arrives: a progress line, a tool-call line, a block, an outcome, an answer of any height, the next turn's input. There is no exception for a tall answer.
- Scrolling up, by any key or by the mouse, or pressing `Home`, takes the view away from the bottom only if, afterwards, its last row is not shown.
- Scrolling back until the last row is shown, or pressing `End`, puts the view at the bottom again and resumes following.
- A view starts at the bottom: when the TUI starts, when a conversation is opened or switched to, when a tab is first shown, and when the drawer is opened.
- Opening the drawer does not change whether the chat is at the bottom.

**Away from the bottom.** A view that is away from the bottom holds its place by content, not by a row number. Its anchor is the item whose row is first shown (a turn's input, an answer, a block, a transcript entry) and the character of that item at which that row begins.

- While rows arrive below, while an item above changes height because its body loaded or changed, and when the width changes and the content is wrapped again, the view shows, as its first row, the row that now contains the anchor.
- If the anchor's item is still in the content but is now shorter than the anchor's character, the anchor moves to the start of that item's last row.
- If the anchor's item is no longer in the view's content, the anchor moves to the start of the next item that is, or failing that the previous one; if there is none, the view goes to the bottom.
- After any of these, the scroll position is clamped to the content and the test for being at the bottom is applied, in that order.
- A change that makes all of the content fit puts the view at the bottom, by the definition above.

**The count.** When a view leaves the bottom it records the seen mark: the item and character at which its last shown row ended. Scrolling down moves the mark forward to the end of the last row shown, and never back. While the view is away from the bottom and there are rows after the mark, it shows `↓ <n> new lines`, where `n` is the number of rows after the mark at the current width. Wrapping the content again recomputes `n`; a line that is replaced in place, as a progress line is, counts for the rows it has now and not once for each replacement. If the mark's item is still in the content but now ends before the mark, the mark moves to that item's end. If the mark's item is gone, the mark moves to the end of the nearest earlier item that remains, or, if none remains, to before the first row, so that everything shown counts as new. Reaching the bottom clears the mark and the count.

**Where the count is shown.** On the chat tab, on the conversation line, which is the line v6 section 1 gives the scroll position ("Scroll position"), at its right, before the turns-above count and the percentage. On the transcript tab and in the drawer, at the right of that view's own header line, before its percentage.

**Resizing.** A view that was at the bottom is at the bottom after a resize. A view that was away keeps its anchor, as above.

## 4. Acceptance for following

- With the chat at the bottom, an answer taller than the window arrives and the view shows the answer's last rows and the view's last row.
- With the chat at the bottom, each of a progress line, a block and a new turn's input arrives and the last row stays shown.
- After `PgUp`, rows arrive and the first shown row does not change; the conversation line shows `↓ <n> new lines` with the number of rows after the seen mark.
- After scrolling back down until the last row is shown, the count is gone and the next row that arrives is shown.
- `End` from any position shows the last row and resumes following; `Home` on content taller than the view shows the first row and following stops.
- With content shorter than the view, `Home`, `PgUp` and a scroll up each leave the view at the bottom, and the next row that arrives is shown.
- A view that the user scrolled to its last row by hand follows from then on.
- While away from the bottom, the terminal is made narrower so that rows above the view wrap into more rows: the first shown row is the row that contains the anchor's character.
- While away from the bottom on the transcript tab, an entry above the view loads its body and grows: the first shown row does not change.
- While away from the bottom, the anchor's item is removed: the view's first row is the start of the next item.
- While away from the bottom, the anchor's item is replaced by a shorter one that ends before the anchor's character: the view's first row is that item's last row.
- While away from the bottom, the seen mark's item is replaced by a shorter one: the count is the rows after that item's end. The seen mark's item is removed: the count is the rows after the end of the nearest earlier item, and with no earlier item it is every row.
- After each of those, a view whose last row is now shown is at the bottom and its count is cleared.
- While away from the bottom, a progress line below is replaced ten times: the count rises by that line's rows once.
- While away from the bottom, a resize changes the wrapping below the mark: the count is the number of rows after the mark at the new width.
- While away from the bottom, a resize that makes all of the content fit puts the view at the bottom and clears the count.
- The drawer opens showing its last row.
- The transcript tab and the drawer each pass the same cases, with the count on their own header lines.
- Starting the TUI on a conversation longer than the window shows its last row.
- Resizing while at the bottom keeps the last row shown.

## 5. Disposition of review

**Revision 1, November 26, 2026.** 5,962 bytes, SHA-256 `471b4f7c…081cf8d`. Blocked by both. Muse required three changes (Q1 to Q3) and Codex three (L1 to L3). Neither objected to S1, the order of giving way, or S2, a view at the bottom stays there.

| Point | Required | Where answered |
|---|---|---|
| Q1, L1 | "Never removed" against the final cut; what happens when the kept items alone do not fit | Section 1: when that is enough; below that, steps 6 to 8; the changed clauses |
| L2 | Reproducible width fixtures; cells as drawn | Section 1: measure; section 2 |
| Q2 | Define the conversation line; one term; where the count is shown | Section 3: where the count is shown; "row" throughout |
| Q3 | The drawer starts at the bottom | Section 3: at the bottom |
| L3 | Stable anchors; what counts as new; following from the clamped view | Section 3: at the bottom, away from the bottom, the count; section 4 |

Optional notes taken from Muse: `quick:<name>` in the every-width check; resizing while away; step 2 on the chat tab only; a resize that makes everything fit.

**Revision 2, November 26, 2026.** 13,203 bytes, SHA-256 `2451382d…2cb86a9`. Approved by Muse: Q1 to Q3 satisfied. Blocked by Codex: L1 and L2 satisfied; one required change (T1).

| Point | Required | Where answered |
|---|---|---|
| T1 | Anchor and seen-mark recovery when an item shrinks or disappears; the resize test against the rule | Section 3: away from the bottom, the count; section 4 |

Optional notes taken: step 4 only for a state longer than five cells; a wide character at the cut (Muse). The v6 references corrected, acceptance is section 8 and the plain terminal section 6; v4 section 4b named among the changed clauses (Codex).

**Revision 3 terminal votes.** Codex: APPROVE (T1 satisfied; "Required changes: none. Optional notes: none."). Muse: APPROVE (T1 satisfied; "My revision 2 Q1 to Q3 remain satisfied"). Neither objected to S1 or S2, and there are no open points. Operator, November 26, 2026: "Ratify the addendum". Voted on the text of revision 3 at 15,356 bytes, SHA-256 `a6041ca5cdd57f6210ba12d149fde5a979bf49a5f67d627eebdb087d6dae0c1d`; this paragraph is the only difference between that text and this one. The record is `review/2026-11-26-v6-addendum-1-rev3-review.md`.
