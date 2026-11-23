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

Four disagreements found between the frames and `docs/design/v6-delta-tui-presentation.md` sections 0–7:

**File: `chat-idle-100x30.txt`, line 5**

The ratified design section 1 specifies: "An outcome line begins in column 0 with a one-character mark and a space: `✓` approved, `!` incomplete or overridden, `×` cancelled, `·` solo." Section 2 (line 76) further specifies: "Every outcome line ends with `· <n> reviews`."

Frame shows:
```
4	› what does the gate check before a reviewer result counts?
5	  approved by codex, muse against 5d20e9b1 · not in context
```

Line 5 shows an approved outcome (marked as "approved by...") but lacks the required `✓` mark and the reviews count. According to the design, this line should start with the mark and end with the count:
```
5	✓ approved by codex, muse against 5d20e9b1 · not in context · <n> reviews
```

**File: `chat-dumb-80x24.txt`, line 23**

The ratified design section 1 specifies: "A multi-line text area with the prompt `┃ `." Section 6 says that in plain terminal mode (TERM=dumb) with no colour, "prompts are single-line questions as in v4" but does not override the input prompt character.

Frame shows:
```
23	>
```

The input prompt uses `>` but the design specifies `┃ `. The design does not provide an exception for TERM=dumb, so this is a disagreement.

**File: `style.md`, line 39**

The style table specifies:
```
39	| `┃ ` | 0 | the input prompt (`> ` in a plain terminal) |
```

The table documents `┃` as the input prompt with `> ` as a fallback for plain terminals. However, the design section 1 specifies `┃` as the input prompt and does not override it for plain terminals (section 6 does not mention the input prompt). This is inconsistent with the design's lack of exception for TERM=dumb.

**File: `chat-outcomes-100x30.txt`, line 30**

The ratified design section 1 specifies: "The status line shows (at 100×30 width, all elements visible) `review · reviews_dispatched · <elapsed>/<budget> · <meter> · round <r>/<n> · ctrl+o: reviews   tab: switch  /help`." The status line element order is defined in section 1 and applies when the chat tab is active.

Frame shows:
```
30	review · reviewers_dispatched · 0:18/10:00 · █░░░░░░░░░ 12k/120k · round 0/2      tab: switch  /help
```

Line 30 is the status line with the chat tab active and the drawer closed. It retains the budget meter (`█░░░░░░░░░ 12k/120k`) but omits `ctrl+o: reviews`. According to the operator's overflow ruling (recorded below), the budget meter is hidden before the reviews hint; this frame contradicts both the design's requirement that the reviews hint appear on the active chat tab and the ruling's ordering. For comparison: `chat-idle-100x30.txt` line 26 and `chat-attach-100x30.txt` show the reviews hint; `chat-dumb-80x24.txt` and `chat-drawer-80x24.txt` at 80 columns are consistent with the ruling (meter already hidden, then the hint is hidden or hidden).

The frame should show:
```
30	review · reviewers_dispatched · 0:18/10:00 · round 0/2 · ctrl+o: reviews      tab: switch  /help
```
(with the meter hidden by the overflow rule and the reviews hint present).

## Operator ruling: status line overflow

When the status line does not fit the terminal width, elements are hidden in this order: budget meter (keeping `tokens/limit`), then the reviews hint, then the round text, then the state is shortened with an ellipsis, while `tab: switch  /help` is always kept. This ruling governs implementation regardless of what the frames show.
