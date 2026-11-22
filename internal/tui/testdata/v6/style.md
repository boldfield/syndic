# v6 TUI style table

Illustrative values from the design mock-ups. The ratified v6 delta governs behaviour; these are the
looks to match. Colours are light/dark pairs chosen from the terminal background (Lip Gloss adaptive
colours). No meaning is carried by colour alone.

## Palette

| Role | Dark background | Light background | Used for |
|---|---|---|---|
| background | `#0f1115` | `#fbfbfc` | terminal default; do not paint it |
| text | `#d4d7dd` | `#1f2328` | answers, inputs, values |
| dim | `#8b93a1` | `#59636e` | labels, hints, progress line, block commentary |
| dimmer | `#7d8594` | `#59636e` | turns marked `not in context` |
| rule | `#4a5160` | `#b4bac4` | horizontal rules, empty meter cells |
| approve | `#7fd88f` | `#116329` | `✓` lines, `approve`, done marks |
| warn | `#e5c07b` | `#7d4e00` | `!` INCOMPLETE, `·` solo, banners, `approve-with-changes`, working spinner |
| block | `#f07178` | `#a40e26` | `×` CANCELLED, block lines, `UNREVIEWED DRAFT`, inline code |
| accent | `#7aa2f7` | `#0550ae` | `›`, `┃`, phase, key hints that act, filled meter and pips |
| override | `#c9a4f5` | `#6639ba` | `HUMAN OVERRIDE`, overridden blocks, code keywords |
| selection | `#1e2433` | `#e3e8f0` | highlighted row or turn (background) |
| code | `#1a1d25` | `#eceef1` | inline code and code block background |
| diff add | text approve on `#14261a` | approve on `#dafbe1` | added lines |
| diff remove | text block on `#2a171b` | block on `#ffebe9` | removed lines |

Every text colour above is at least 4.5:1 against its background.

## Marks and prefixes

| Mark | Column | Meaning |
|---|---|---|
| `› ` | 0 | a user input; continuation lines are content lines |
| `✓ ` | 0 | approved |
| `! ` | 0 | incomplete, or human override |
| `× ` | 0 | cancelled |
| `· ` | 0 | solo, unreviewed |
| two spaces | 0–1 | every content line, applied after rendering and wrapping |
| spinner | 0 | the progress line of a running turn |
| `┃ ` | 0 | the input prompt (`> ` in a plain terminal) |

Active tab: reverse video, bold, one space each side. Inactive tabs: dim. Outcome banners
(`INCOMPLETE, not approved` and the like): bold. `UNREVIEWED DRAFT`: reverse video in the block colour.

## Columns

| Screen | Columns (width in cells) |
|---|---|
| Transcript | id 6, sender 10, phase 11, status 11, scrub 8, tokens 16, classification rest |
| Participants | id 9, role 10, state 11, pool 9, reported model 16, context/out 15, probed rest; detail label 20 |
| Changes | op 9, path 42, lines 12, outcome rest |
| Conversations | mark 2, id 9, last activity 15, turns 8, preview rest |
| Drawer | reviewer 7, then verdict kind and block states; commentary indented 4 |
| File list | path 50, size rest |

## Heights (from the ratified text, section 1)

| Region | Lines |
|---|---|
| tab bar | 1, always |
| status line | 1, always, last |
| input | 1–6, scrolls inside |
| banners | at most 2; `+<n> more`; identity warning always shown |
| attached line | 1; `+<n> more` |
| command or file list | at most 5; closes when the drawer opens |
| short help | 1; hidden while the drawer is open |
| drawer | 7, or 9 above 30 rows |
| active tab | the rest, never fewer than 5 |

## Meter, pips, spinner

- Meter: 10 cells at 100 columns (`███▍░░░░░░`), filled in the accent colour, empty in the rule colour,
  followed by `tokens/limit`. Omitted in a plain terminal.
- Pips: one per allowed round after `round n/m`, `●` filled for rounds started, `○` otherwise.
- Spinner: the Bubbles "dot" spinner. In a plain terminal the word for the state replaces it.
