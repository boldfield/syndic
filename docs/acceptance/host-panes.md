# Host-pane acceptance checklist

This is the manual form of the scripted run in `internal/acceptance/tmux_test.go`. It runs one
scenario in four hosts: a bare terminal, tmux, a Herdr pane and a cmux pane. The design text is
`docs/design/v4-delta-engine-tui.md`, section 7 ("Plain terminal") and section 2 ("Invariants":
detaching never cancels a turn, and reattaching shows the exact state).

The scripted test covers tmux in CI. Herdr and cmux are checked by hand because they cannot run
in CI. Run the whole checklist once per host and record the result in the table at the end.

Nothing here contacts a real model endpoint. The checklist uses a stub that serves the roster
below on `127.0.0.1`, or you can use your own endpoints (see step 0).

## Conventions

- "Host pane" is the pane, window or terminal under test. The stub and the engine can run in any
  other terminal; they are not under test.
- Every command is run from the repository root unless it says otherwise.
- "Type" means type the text and press Enter.
- Every step has an **Expect** line. If what you see differs, the step fails: write down what you
  saw in the notes column of the results table.

## 0. Set up once

0.1. In a terminal that is not the host under test, build the binaries and create a scratch
directory:

```
export SYNDIC_ACC=$(mktemp -d)
mkdir -p "$SYNDIC_ACC"/{bin,run,data,cfg,stub}
go build -o "$SYNDIC_ACC/bin/" ./cmd/...
```

**Expect:** `$SYNDIC_ACC/bin` holds `syndic` and `syndicd`.

0.2. Write the environment file that every pane will source, so the engine, the probe and the TUI
agree on the socket and the journal:

```
cat > "$SYNDIC_ACC/env.sh" <<EOF
export XDG_RUNTIME_DIR="$SYNDIC_ACC/run"
export XDG_DATA_HOME="$SYNDIC_ACC/data"
export XDG_CONFIG_HOME="$SYNDIC_ACC/cfg"
export XDG_STATE_HOME="$SYNDIC_ACC/state"
export PATH="$SYNDIC_ACC/bin:\$PATH"
EOF
```

**Expect:** the file exists and has five `export` lines.

0.3. Start the stub. It listens on `127.0.0.1` on a free port, writes the roster for it, prints
both, and runs until you press Ctrl+C:

```
SYNDIC_STUB_SERVE="$SYNDIC_ACC/stub" go test -count=1 -timeout 1h -run TestTMUX_ServeStub -v ./internal/acceptance/
```

**Expect:** the output contains `stub serving at http://127.0.0.1:<port>` and
`roster written to <dir>/stub/syndic.toml`. Leave it running.

The stub gives a fixed answer of the form `Stub answer to: <your question>`, and its reviewer
approves, with two exceptions that are driven by the question: a question containing `[block]`
makes the reviewer block, and a question containing `[slow]` makes the reviewer wait 30 seconds
before it approves.

The roster it writes has one author (`stub-author`) and one reviewer (`stub-reviewer`), each on
its own endpoint of the stub, the `review` preset, and non-provisional deadlines (attempt 60s,
turn 120s, delivery 10s, `provisional = false`).

Using your own endpoints instead: write a roster with one author and one reviewer on endpoints
you control, set `provisional = false` under `[deadlines]`, and use it in place of
`$SYNDIC_ACC/stub/syndic.toml` below. The `[block]` and `[slow]` triggers then do not apply; for
the block step you need a question your reviewer blocks, and for the detach step a question that
takes long enough to detach during.

0.4. In a second terminal that is not the host under test, probe both participants and start the
engine:

```
. "$SYNDIC_ACC/env.sh"
for p in "author-ep stub-author" "reviewer-ep stub-reviewer"; do
  set -- $p
  syndic probe --roster "$SYNDIC_ACC/stub/syndic.toml" --endpoint "$1" --model "$2" \
    --raw-dir "$SYNDIC_ACC/raw" --export-dir "$SYNDIC_ACC/export" --context-tokens 4096
done
syndicd --roster "$SYNDIC_ACC/stub/syndic.toml"
```

**Expect:** each probe prints a summary with a `Raw record:` and an `Export:` path and exits 0.
`syndicd` then prints one line, the socket path, a run id and the roster path, and keeps running.

## 1. Enter the host

Do the part for the host you are testing. Every host then continues at step 2.

- **Bare terminal.** Open a terminal window that is not inside tmux, Herdr or cmux. Check that
  `echo "$TMUX"` prints nothing.
- **tmux.** Run `tmux new-session -s acc` (or `tmux new-session -d -s acc` and then
  `tmux attach -t acc`). Check that `echo "$TMUX"` prints a path.
- **Herdr.** Open Herdr and create a new pane with its normal new-pane action. Run the steps in
  that pane.
- **cmux.** Open cmux and create a new pane with its normal new-pane action. Run the steps in
  that pane.

**Expect:** you have a shell prompt in the host pane, and the pane is at least 80 columns by 24
rows (check with `stty size`; it prints rows then columns).

## 2. Start

2.1. In the host pane:

```
. "$SYNDIC_ACC/env.sh"
syndic
```

**Expect:** the screen clears and shows `[review] [idle]` on the first line, and an input line
that starts with `>` on the last line. The stub's roster name for the preset is `review`.

## 3. Submit and see an approved answer

3.1. Type `first question`.

**Expect:** the input line shows `> first question` while the turn runs, then the answer tab
shows these two lines, in this order:

```
Stub answer to: first question
approved by reviewer against <12 hex characters>
```

3.2. Check the width: no line of the screen runs past the right edge or wraps in the middle of
the `approved by` line.

**Expect:** every line fits in 80 columns.

## 4. A block, and `/why`

4.1. Type `please [block] this`.

**Expect:** the answer tab shows a block, then the incomplete line, then the candidate, in this
order:

```
block:<id>:stub-block
[high] <id>:stub-block
  evidence: The request asks the stub to block.
  remedy: Withdraw the block trigger from the request.
INCOMPLETE, not approved
Stub answer to: please [block] this
```

There is no `approved by` line for this turn.

4.2. Type `/why`.

**Expect:** a panel shows `turn <id>: incomplete`, the reason `block:<id>:stub-block`, the block
with its `[high]` severity, evidence and remedy, and the line
`reviewer reason (reviewer): The stub blocks on request.` Press Esc to close the panel.

4.3. Press Tab, then Tab again, then Tab again.

**Expect:** the first Tab shows the transcript of the turn (`transcript of turn <id>`, then the
user message and each attempt), the second shows the participants table with `author` and
`reviewer` both `idle`, and the third returns to the answer tab.

## 5. Detach and reattach during a turn

5.1. Type `a [slow] question`. The reviewer now waits 30 seconds.

**Expect:** the turn is not finished: the first line shows a running phase instead of `[idle]`
(for example `[review] [reviewers_dispatched] [4s / 2m0s] ...`), the answer tab says
`turn in progress`, and there is no `approved by` line.

5.2. Within those 30 seconds, close the host pane the hard way, without `/quit`: close the
terminal window; in tmux run `tmux kill-session -t acc` from another terminal; in Herdr and cmux
close the pane.

**Expect:** the TUI is gone. In the engine terminal (step 0.4) `syndicd` is still running and
prints nothing.

5.3. Wait 30 seconds, then enter the host again as in step 1 (new window, new tmux session, new
pane), and run:

```
. "$SYNDIC_ACC/env.sh"
syndic
```

**Expect:** the screen shows the turn finished while you were away:
`Stub answer to: a [slow] question` and an `approved by reviewer against <hash>` line. The turn
was not cancelled by the detach.

5.4. Repeat 5.1, but this time reattach before the 30 seconds are over.

**Expect:** the reattached TUI shows the turn still running (`turn in progress`), and shortly
after the 30 seconds are over it shows `Stub answer to: a [slow] question` and the `approved by`
line.

## 6. `TERM=dumb`

6.1. Quit the TUI with `/quit`, then in the host pane run:

```
TERM=dumb syndic
```

**Expect:** it starts and does not fail. The first line shows `[review] [idle]` and the input line
shows `>`.

6.2. Type `hello dumb`.

**Expect:** the answer `Stub answer to: hello dumb` and an `approved by reviewer against <hash>`
line appear, with no color and no screen-clearing: earlier shell output is still above it when
you scroll back.

6.3. Type `/quit`.

**Expect:** the TUI exits and returns you to the shell prompt, after printing the answer text of
the last turn.

## 7. Resize to 80×24

7.1. Start `syndic` again (without `TERM=dumb`) and resize the host pane to 80 columns by
24 rows: in a terminal window drag it to that size; in tmux run
`tmux resize-window -x 80 -y 24` from another terminal if the window is larger; in Herdr and cmux
resize the pane by dragging or with its resize key.

**Expect:** the layout redraws: the first line is `[review] [idle]`, the input line is the last
line, and no line is wider than 80 columns.

7.2. Type `resized question`.

**Expect:** the answer tab shows `Stub answer to: resized question` and the `approved by` line,
all within 80 columns.

7.3. Type `/quit`.

**Expect:** the TUI exits and returns you to the shell.

## 8. Tear down

8.1. Press Ctrl+C in the stub terminal, then press Ctrl+C in the engine terminal.

**Expect:** the engine exits with status 0 (`echo $?` prints 0) and does not print an error. The
engine socket `$SYNDIC_ACC/run/syndic/engine.sock` accepts no connections (`syndic` started in a pane
afterwards shows `engine: connecting` and never reaches `[idle]`; press Ctrl+C to leave it).

8.2. Remove the scratch directory: `rm -rf "$SYNDIC_ACC"`.

**Expect:** no `syndicd`, `syndic` or stub process of yours remains (`pgrep -l syndic` prints
nothing).

## Results

Run the checklist once per host and add a row. "Result" is `pass`, or `fail` with the step
numbers that failed in the notes.

| Date | Host | Host version | Syndic version | Result | Notes |
| ---- | ---- | ------------ | -------------- | ------ | ----- |
|      | Bare terminal |     |                |        |       |
|      | tmux |              |                |        |       |
|      | Herdr |             |                |        |       |
|      | cmux |              |                |        |       |

Herdr and cmux are checked by hand because they cannot run in CI. The tmux row is also covered by
the scripted run (`SYNDIC_TMUX_TEST=1 go test -run TestTMUX_EndToEnd ./internal/acceptance/`), but
the checklist is still run by hand once per release.
