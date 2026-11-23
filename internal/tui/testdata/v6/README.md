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

One disagreement found between the frames and `docs/design/v6-delta-tui-presentation.md` sections 0–7:

**File: `chat-outcomes-100x30.txt`, line 23**

The ratified design section 1 specifies: "An outcome line begins in column 0 with a one-character mark and a space: `✓` approved, `!` incomplete or overridden, `×` cancelled, `·` solo."

Frame shows:
```
22	› explain why detach never cancels a turn
23	  UNREVIEWED DRAFT
```

The solo outcome on line 23 is missing the solo mark `·`. According to the design, this line should read:
```
23	· UNREVIEWED DRAFT
```

This is inconsistent with line 5 of the same frame, which correctly shows `· UNREVIEWED (solo)` with the mark.

## Operator ruling: status line overflow

When the status line does not fit the terminal width, elements are hidden in this order: budget meter (keeping `tokens/limit`), then the reviews hint, then the round text, then the state is shortened with an ellipsis, while `tab: switch  /help` is always kept. This ruling governs implementation regardless of what the frames show.
