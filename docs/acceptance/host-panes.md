# Host Panes Manual Acceptance Checklist

This document describes the manual verification steps for running the syndic TUI in different host multiplexer environments. The scripted tmux test (`internal/acceptance/tmux_test.go`) runs in CI. These steps document verification in bare terminals, Herdr panes, and cmux panes, which cannot run in CI and must be checked by hand.

## Prerequisites

- Build `syndicd` and `syndic` binaries with `go build -o syndicd ./cmd/syndicd` and `go build -o syndic ./cmd/syndic` from the module root
- Set `XDG_RUNTIME_DIR` to a writable directory (e.g., `export XDG_RUNTIME_DIR=/tmp/syndic-runtime`)
- Create directories: `mkdir -p /tmp/syndic-evidence/raw /tmp/syndic-evidence/export /tmp/syndic-secrets`
- Run a stub endpoint or use the test endpoint in `internal/acceptance/tmux_test.go`
- Use the roster file provided in the "Stub Roster" section below, with the endpoint URL for your environment
- Run `./syndic probe --roster roster.toml --endpoint stub-endpoint --model test-model --raw-dir ./raw --export-dir ./export --journal journal.db --secrets-dir ./secrets` for both author and reviewer roles
- Start `./syndicd -roster roster.toml -journal journal.db -secrets-dir ./secrets` in a background window
- The engine listens on `$XDG_RUNTIME_DIR/syndic/engine.sock`

## Stub Roster

Save this as `roster.toml` and replace `http://localhost:8080` with your stub endpoint URL:

```toml
default_preset = "test-preset"

[[presets]]
name = "test-preset"
mode = "governed"
author = "author"
required_reviewers = ["reviewer"]

[[participants]]
id = "author"
model = "test-model"
route = "api"
endpoint = "stub-endpoint"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "test-model"
route = "api"
endpoint = "stub-endpoint"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "stub-endpoint"
base_url = "http://localhost:8080"
dialect = "chat_completions_v1"
models = ["test-model"]
declared_location = "local"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
max_tokens_field = "max_tokens"
tokens_per_byte_max = 1.0

[[pools]]
name = "default"
concurrency = 10

[[deadlines]]
phase = "attempt"
duration_ms = 30000

[[deadlines]]
phase = "turn"
duration_ms = 60000
```

## Stub Endpoint Implementation

Your stub endpoint must handle these requests:

1. **`GET /models`**: Return a JSON list with at least one model
   ```json
   {
     "object": "list",
     "data": [{"id": "test-model", "object": "model"}]
   }
   ```

2. **Author request (no envelope)**: Return a simple chat completion
   ```json
   {
     "id": "chatcmpl-123",
     "object": "chat.completion",
     "model": "test-model",
     "choices": [{
       "message": {"role": "assistant", "content": "This is a test answer to your question."},
       "finish_reason": "stop"
     }],
     "usage": {"prompt_tokens": 10, "completion_tokens": 5}
   }
   ```

3. **Reviewer request (has envelope)**: Extract `candidate_hash` and `attempt_id` from the request's `envelope` field and return a well-formed approval:
   ```json
   {
     "id": "chatcmpl-456",
     "object": "chat.completion",
     "model": "test-model",
     "choices": [{
       "message": {"role": "assistant", "content": "<verdict approve=\"<candidate_hash>\" attempt=\"<attempt_id>\"/>"},
       "finish_reason": "stop"
     }],
     "usage": {"prompt_tokens": 20, "completion_tokens": 10}
   }
   ```

For blocking requests, use a verdict with `block_severity` and return an appropriate block message.

## Scenario: Terminal Session (80×24 cells, all phases)

Each step lists what to type, what to observe, and what to verify.

### Step 1: Start

- **Type:** `./syndic` (or `go run ./cmd/syndic` from module root)
- **Observe:** The TUI appears with the layout divided into panes for different content
- **Verify:** No errors; the status bar shows the roster file is loaded and the engine state is ready

### Step 2: Submit a question

- **Type:** `What is the capital of France?` (or any question)
- **Press:** `Enter` to submit
- **Observe:** 
  - The input area clears
  - A turn ID is recorded
  - The status shows "awaiting answer"
  - The answer area remains empty until the response arrives
- **Verify:** The question is processed without errors; the turn progresses through dispatch

### Step 3: Wait for approved answer

- **Observe:**
  - After a short delay, the answer area populates with the model's response
  - A line appears showing the approval with the reviewer name and timestamp
  - The status bar shows the turn is complete
- **Verify:**
  - The answer text matches the stub's configured response
  - No line in the pane exceeds 80 cells (use `echo -n "text" | wc -c` to verify)
  - The approval line is present and indicates who approved it

### Step 4: Detach and reattach during a turn

- **Submit** a new question (as in Step 2)
- **While the turn is open** (before the answer arrives), press `Ctrl+D` to detach or close the terminal window
- **Observe:** The TUI exits; the engine continues processing in the background
- **Type:** `./syndic` again (reconnect to the engine)
- **Observe:**
  - The TUI reconnects to the engine and loads the previous turn's state
  - The same turn ID is shown
  - If the answer has arrived, it appears with approval
  - If still pending, the status shows "awaiting answer"
- **Verify:** No turn is lost; reattach shows the exact final state including any approved answer; the turn was not cancelled by detach

### Step 5: Test with `TERM=dumb`

- **Type:** `TERM=dumb ./syndic`
- **Observe:** The TUI runs without colors or special terminal features
- **Verify:** All text is readable in a plain terminal; no special characters or sequences cause rendering issues

### Step 6: Resize to 80×24

- **Resize your terminal** to exactly 80 columns and 24 rows (use `stty rows 24 cols 80` or terminal settings)
- **Type:** `./syndic`
- **Observe:** The TUI renders completely on-screen without scrolling or clipping
- **Verify:**
  - All panes fit within 80 columns and 24 rows
  - No line exceeds 80 display columns (use `echo -n "text" | wc -c` for byte count or use a display-width tool)
  - No wrapping or horizontal scrolling; all content is visible

### Step 7: Submit a turn that is blocked (optional, requires block-configured stub)

If your stub is configured to block certain requests:

- **Type:** `A request that triggers a block` (or use a stub-configured block query)
- **Press:** `Enter` to submit
- **Observe:**
  - The input area clears
  - After the reviewer response arrives, the answer area shows the block message
  - The status bar indicates the turn is blocked
- **Verify:** The block message is fully visible and indicates severity and reason

### Step 8: Check the `/why` command (optional)

If a turn has entered a hold or incomplete state:

- **Type:** `/why`
- **Press:** `Enter`
- **Observe:** Information appears explaining the hold or failure reason
- **Verify:** The output is relevant to the turn's current state

## Verification Record

Document your verification in the table below. Record the date, host environment, software version, and pass/fail result for each environment. The scripted tmux test (`internal/acceptance/tmux_test.go`) runs in CI automatically; this table is for manual verification only on the named hosts.

| Date | Host Environment | syndic Commit | Steps Completed | Result | Notes |
|------|-----------------|--------|---|--------|-------|
|      | Bare terminal   |        | 1–8 | Pass / Fail |       |
|      | tmux pane       |        | 1–8 | Pass / Fail |       |
|      | Herdr pane      |        | 1–8 | Pass / Fail |       |
|      | cmux pane       |        | 1–8 | Pass / Fail |       |

## Environment Notes

- **Bare terminal:** A shell prompt in a local terminal emulator (xterm, GNOME Terminal, iTerm2, etc.)
- **tmux pane:** A terminal pane inside a tmux session, created with `tmux new-session` or `tmux new-window`
- **Herdr pane:** A pane in the Herdr terminal multiplexer (requires Herdr installed; not available in CI)
- **cmux pane:** A pane in the cmux terminal multiplexer (requires cmux installed; not available in CI)

## Automation Status

- The scripted tmux test (`internal/acceptance/tmux_test.go`) runs in CI for the tmux case with `SYNDIC_TMUX_TEST=1` set
- Bare terminal, Herdr, and cmux verification are manual because those environments are not available in CI
- When completing this checklist, ensure the CI job also passes (check `.github/workflows/ci.yml` for the tmux test step)

## Checklist Completion

Before submitting the PR:

1. ✓ Run `gofmt -l .` and ensure it prints nothing
2. ✓ Run `make check` and verify all checks pass
3. ✓ Run `make test` and verify all tests pass (including the tmux test with `SYNDIC_TMUX_TEST=1`)
4. ✓ For each host in the table above, complete steps 1–8 and record the result
5. ✓ If any defects are found, list them below and check that they are either fixed or documented as out of scope

## Defects Found During Testing

If any step fails or behaves unexpectedly during manual verification, record it here and report the issue:

- [Date] [Host] [Defect]: [description]
