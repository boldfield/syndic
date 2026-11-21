# Host Panes Manual Acceptance Checklist

This document describes the manual verification steps for running the syndic TUI in different host multiplexer environments. The scripted tmux test (`internal/acceptance/tmux_test.go`) runs in CI. These steps document verification in bare terminals, Herdr panes, and cmux panes, which cannot run in CI and must be checked by hand.

## Prerequisites

- Build `syndicd` and `syndic` binaries or run them with `go run ./cmd/syndicd` and `go run ./cmd/syndic`
- Create a roster TOML file with one author and one reviewer participant pointing to a stub endpoint or your own endpoints
- The stub endpoint must answer `/models` with a list and `/chat/completions` with deterministic responses
- For the author request, return a simple text answer
- For the reviewer request, return a well-formed approval envelope with the candidate hash and attempt id
- Run `syndic probe --endpoint <endpoint> --model <model>` for each participant before starting the engine
- Start `syndicd -roster <path> -journal <path> -secrets-dir <dir>`

## Scenario: Terminal Session (80×24 cells, all phases)

Each step lists what to type, what to observe, and what to verify.

### Step 1: Start

- **Type:** `syndic` (or `go run ./cmd/syndic`)
- **Observe:** The TUI appears with three panes: input (bottom), transcript (middle), answer (top)
- **Verify:** No errors; the status strip shows the roster, default preset, and engine state

### Step 2: Submit a question

- **Type:** `What is the capital of France?` (or any question)
- **Press:** `Enter` to submit
- **Observe:** 
  - The input area clears
  - The turn ID appears in the transcript
  - The status strip shows "awaiting answer"
  - The answer tab remains empty until the answer arrives
- **Verify:** The question is recorded in the transcript with the correct turn ID

### Step 3: Wait for approved answer

- **Observe:**
  - After a short delay, the answer tab populates with the model's response
  - Below the answer, a line appears: `approved by <reviewer_name> at <timestamp>`
  - The status strip shows the turn is complete
- **Verify:**
  - The answer text is fully visible and matches the stub's response
  - No line in the pane exceeds 80 cells
  - The approval line is present and readable

### Step 4: Submit a turn that blocks

- **Type:** `This request should block` (use a question that your stub is configured to block)
- **Press:** `Enter` to submit
- **Observe:**
  - The input area clears
  - After the reviewer response arrives, the answer tab shows the block with severity and remedy
  - The status strip shows the turn is blocked
- **Verify:** The block is displayed with its severity level and remedy text

### Step 5: Use `/why` on a budget failure

- **Type:** `/why` (if the turn has a budget hold or block)
- **Press:** `Enter`
- **Observe:** A help text or detail appears explaining the hold or block reason
- **Verify:** The `/why` output is relevant to the turn's current state

### Step 6: Detach and reattach during a turn

- **Submit** a new question (as in Step 2)
- **While the turn is open** (before the answer arrives), press `Ctrl+C` or close the terminal
- **Observe:** The TUI exits; the engine continues processing
- **Type:** `syndic` again (reconnect)
- **Observe:**
  - The TUI reconnects
  - The same turn ID is shown
  - If the answer has arrived, it appears in the answer tab with approval
  - If the answer is still pending, the transcript shows the submission and the status strip says "awaiting answer"
- **Verify:** No turn is lost; reattach shows the exact state the turn reached

### Step 7: Test with `TERM=dumb`

- **Type:** `TERM=dumb syndic`
- **Observe:** The TUI runs without color and uses plain ASCII for all UI elements
- **Verify:** All text is readable; no special characters break the layout

### Step 8: Resize to 80×24

- **Resize your terminal** to exactly 80 cells wide and 24 cells tall (or use `stty rows 24 cols 80`)
- **Type:** `syndic`
- **Observe:** The TUI renders entirely on-screen with the three-pane layout
- **Verify:**
  - The input pane is at the bottom (rows 20–24)
  - The transcript is in the middle (rows 11–19)
  - The answer tab is at the top (rows 1–10)
  - No line exceeds 80 cells
  - No wrapping or clipping; all text is visible

## Verification Record

Document your verification in the table below. Record the date, host environment, software version, and pass/fail result for each environment.

| Date | Host Environment | syndic Version | Result | Notes |
|------|-----------------|-----------------|--------|-------|
|      | Bare terminal   |                 | Pass / Fail |       |
|      | tmux pane       |                 | Pass / Fail |       |
|      | Herdr pane      |                 | Pass / Fail |       |
|      | cmux pane       |                 | Pass / Fail |       |

## Notes

- **Bare terminal:** A shell prompt in a local terminal emulator (xterm, GNOME Terminal, iTerm2, etc.)
- **tmux pane:** A terminal pane inside a tmux session, created with `tmux new-session` or `tmux new-window`
- **Herdr pane:** A pane in the Herdr terminal multiplexer (Herdr is not automated in CI)
- **cmux pane:** A pane in the cmux terminal multiplexer (cmux is not automated in CI)

## Known Limitations

- Herdr and cmux verification is performed by hand because these multiplexers are not available in CI
- The scripted tmux test (`internal/acceptance/tmux_test.go`) covers the tmux scenario and runs in CI with `SYNDIC_TMUX_TEST=1`
- Real model endpoints are out of scope; use a stub endpoint as described in the Prerequisites

## Defects Found

If any step fails or behaves unexpectedly, record it here and report the issue:

- Defect #1: [description]
- Defect #2: [description]
