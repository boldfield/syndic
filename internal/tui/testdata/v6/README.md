# v6 TUI reference frames and style table

Illustrations supplied by the operator on 2026-11-21, showing the v6 TUI presentation. These are mock-ups, not a specification: where a frame or the style table disagrees with the ratified v6 delta design (`docs/design/v6-delta-tui-presentation.md`), the design governs.

## Reference frames

| File | SHA-256 | Shows |
|---|---|---|
| `chat-idle-100x30.txt` | `16024e9c5e51f3d2d8a245fa01a6a388a8efc0f08d2891250a28971c8fc5f261` | Chat tab at idle, 100×30 terminal, four turns with approved, incomplete, and solo outcomes |
| `chat-dumb-80x24.txt` | `906769741fea8038f4a73a5c67b702aadafb196ca38334dbcc86ff8f1908e533` | Chat tab in plain terminal mode (TERM=dumb), 80×24 terminal, running turn with reviewers dispatched |
| `chat-drawer-80x24.txt` | `d13afe9ee8bc4351d27f30c110a32b70a7230957d2665e5958236a052d69a83a` | Chat tab with reviews drawer open, 80×24 terminal, showing drawer UI with two reviewers and their verdicts |
| `chat-attach-100x30.txt` | `f534cf799dbef7edd2596178d3bb83554fec6e80ec0af763092b1d4f8e8b98b6` | Chat tab with file attachment UI, 100×30 terminal, file list and attached line showing selected file |
| `chat-outcomes-100x30.txt` | `4bf82e8f6b5e8ee3ea6eb1d6320f4e3e2eb5c67020fc8af613e1a4c32301c24f` | Chat tab with various outcome types, 100×30 terminal, showing solo, human override, cancelled, and unreviewed draft turns |
| `participants-100x30.txt` | `832ef3c4386b87a50d35d34fc088e6f65dd5bbf67835629d5154cae0cf9e2cce` | Participants tab, 100×30 terminal, showing participant details, state, and identity warning banner |
| `transcript-100x30.txt` | `41110be9edc9ad228521fcd18c0656a9e1fe355da2c28b0cbc4a9f1e08288269` | Transcript tab, 100×30 terminal, showing detailed turn history with rounds, attempts, and verdicts |
| `changes-apply-100x30.txt` | `936ac66274edff5557727cc4198a1cd4461c77fe286b2edffd3b65856420d7d3` | Changes tab with apply prompt, 100×30 terminal, showing changeset appliability and confirmation prompt |
| `style.md` | `88120450cbc1dc1a7dfda20451b033381d1eee844b19d01883c054e2ccf70faf` | Style table specifying colours (dark/light pairs), marks, columns, heights, meter/pips/spinner format |

## Disagreements with the ratified design

Ten disagreements found between the frames or the style table and `docs/design/v6-delta-tui-presentation.md`. In each, the design governs. Section numbers are the design's.

1. **`chat-idle-100x30.txt`, line 5.** Line 5 is `  approved by codex, muse against 5d20e9b1 · not in context`. Section 1 requires an outcome line to begin in column 0 with a one-character mark and a space (`✓` for approved), and section 2 requires every outcome line to end with `· <n> reviews`. The line has neither. It should read `✓ approved by codex, muse against 5d20e9b1 · not in context · <n> reviews`.

2. **`chat-dumb-80x24.txt`, line 23.** The input prompt is `>`. Section 1 specifies the input prompt `┃ `, and section 6 (plain terminal) does not override it.

3. **`style.md`, line 39.** The table prescribes `> ` as the input prompt in a plain terminal, for the same reason as item 2.

4. **`chat-outcomes-100x30.txt`, line 30.** The status line, with the chat tab active and the drawer closed, keeps the budget meter and omits `ctrl+o: reviews`. Section 1, "Status line", says: "At the right edge, `ctrl+o: reviews` when the chat tab is active, then the v5 hint `tab: switch  /help` unchanged." It also contradicts the operator ruling below, which hides the meter before the reviews hint. For comparison, `chat-idle-100x30.txt` line 30 and `chat-attach-100x30.txt` line 30 show the hint, and the two 80-column frames already hide the meter.

5. **`chat-attach-100x30.txt`, line 28.** The attached line is drawn below the input (line 27). Section 3 says: "The attached line above the input lists exactly the set that the next submit will send."

6. **`chat-dumb-80x24.txt`, line 2.** The conversation line (`conversation 7c2e41 · 5 turns`) shows neither the number of earlier turns above the view nor the scroll percentage. Section 1, "Scroll position", requires both, and section 6 does not exempt the plain terminal.

7. **`changes-apply-100x30.txt`, line 4.** The outcome line `✓ approved by codex, muse after 1 revision against d07a5b62` does not end with `· <n> reviews`. Section 2 says "Every outcome line ends with `· <n> reviews` from `review_count`. Solo turns have none." The design does not limit this to the chat tab, so the line disagrees.

8. **`transcript-100x30.txt`, line 27.** The outcome line `✓ approved by codex, muse after 2 revisions against 81b04d7a` does not end with `· <n> reviews`. Section 2 applies the rule to every outcome line, so the transcript tab is not exempt.

9. **`chat-outcomes-100x30.txt`, line 23.** `UNREVIEWED DRAFT` is presented without the one-character outcome mark section 1 requires; the style table (line 36) gives `· ` for solo and unreviewed turns.

10. **`chat-outcomes-100x30.txt`, line 30.** The status line shows `round 0/2` while the first candidate is still being reviewed (line 26 also shows round 0). Section 1, "Status line", shows round text only during a revision.

Frame artifact, not a design disagreement: `transcript-100x30.txt` lines 13-15 show an overlapping second pane that cuts the tokens column to `2.  │`.

## Operator ruling: status line overflow

When the status line does not fit the terminal width, elements are hidden in this order: budget meter (keeping `tokens/limit`), then the reviews hint, then the round text, then the state is shortened with an ellipsis, while `tab: switch  /help` is always kept. This ruling governs implementation regardless of what the frames show.
