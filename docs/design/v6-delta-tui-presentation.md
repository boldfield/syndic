# v6 delta: TUI presentation and attachments (amends v5 section 5 and parts of v5 sections 3 and 6)

Revision 4, DRAFT, dated November 21, 2026. Author: Fable, in the claude.ai session that couriered the v5 peer rounds. Revision 1 (16,754 bytes, SHA-256 `af032c4e46ed58b65e258737b5a7849139c38e98571eab3360acb1d24b5c9f67`) was blocked by both reviewers. Revision 2 (29,880 bytes, SHA-256 `4b7456c1aee7fb5e1bec7f73aadfd3c08d7d68139198cfa92792dce894e1ec13`) was approved by Muse and blocked by Codex on four gaps. Revision 3 (32,924 bytes, SHA-256 `97e39538f48433b992a2eecc9a10a63e8aa091400d56cef99b6e813fb9570d26`) was approved by Muse and blocked by Codex on one. Section 9 records the rounds. Not approved, not hash-bound. This delta extends the v5 delta as ratified (101,329 bytes, SHA-256 `6cb570b01ee5c40744e3f97c986d33095b9c056d205efd332b024b713b005577`) and everything that delta extends. It governs only the changes below; every earlier requirement stands. Peer review of this delta does not by itself authorize implementation.

## 0. Inputs

The operator chose a visual direction for the TUI on November 21, 2026, from mock-ups on a private design canvas: the "Ledger" layout (no boxes, a two-character gutter, coloured outcome lines) with a reviews drawer, and nine additions built on the Bubble Tea ecosystem. The mock-ups are illustrations. Where a mock-up and this text differ, this text governs.

Operator decisions:

- D1. The base layout is Ledger with the reviews drawer (section 2).
- D2. All nine additions of section 5 are wanted.
- D3. `@path` attaches the file to the turn: its content is sent with the input (section 4). The operator first chose path-only and reversed it the same day.
- D4. The terminal bell is off by default.

Not yet ruled on by the operator, and settled here the way both reviewers advised: mouse capture is off by default (section 5); the identity warning is a banner (section 1); attachments are allowed under a preset that grants no `read` tool (section 4). The operator may overrule any of the three.

Sections 1, 3, 5 and 6 change only the TUI. Section 2 adds one read-only request. Section 4 changes the engine: `submit`, the candidate format, the snapshot, taint and egress, and what participants receive. Nothing here changes the verdict envelope, the revision loop, or apply.

## 1. Layout and style

v5 section 5's layout stands: tab bar, active tab, banners, input, status line last.

**Safe text.** Every byte sequence the TUI did not write itself is *content*: model output, reviewer text, the user's own input when echoed, attachment bytes, file names, paths, and detail strings in errors and events. Content is displayed only as safe text: every C0 control except line feed and tab, DEL, every C1 control, and every Unicode bidirectional override or isolate character is replaced by a visible escape such as `\x1b`, and a byte that is not valid UTF-8 by `\xNN`. Tab is shown as spaces. No content byte reaches the terminal as a control. This holds in every tab, the drawer, every list and prompt, rendered or not. The journal and the content store keep the original bytes, and the `transcript` content references return them unchanged.

**Content lines.** After rendering and wrapping, the TUI prefixes every displayed line of content with two spaces. The prefix is applied last, to each terminal line, so no content can place a character in column 0 or 1 of the chat tab.

**Chat lines.** A user input begins `› ` in column 0, and its continuation lines are content lines. An outcome line begins in column 0 with a one-character mark and a space: `✓` approved, `!` incomplete or overridden, `×` cancelled, `·` solo. The banner strings of v4 and v5 follow the mark word for word.

**One correction to a v5 string.** `approved by <ids> after <n> revisions against <hash prefix>` reads `after 1 revision` when n is 1. For n of 2 or more it is unchanged.

**Rendered answers.** Answer text in the chat tab is rendered as Markdown (inline code, lists, emphasis, fenced code blocks with a language label) from its safe text. `/raw on|off` turns Markdown rendering off and on for the chat tab; raw means unformatted safe text, not terminal-raw bytes. The transcript tab is always unformatted safe text. Links are shown as text with the URL visible; no hyperlink escape sequence is emitted.

**Status line.** Left to right: preset; `quick:<name>` when one governs; turn state, or `idle`; `elapsed/turn_deadline` while a turn runs; a budget meter and `tokens/limit`; `round n/m` during a revision. At the right edge, `ctrl+o: reviews` when the chat tab is active, then the v5 hint `tab: switch  /help` unchanged. This is a cut from v5, which said the status line carries all of v4's strip. Per-participant states are on the chat's progress line, where v5 already puts them, and in the participants tab.

**Identity warning.** `identity: unknown(name-matched)` is a banner, shown whenever the condition applies, naming each participant it applies to. It sorts first among banners and is never folded into a count (below). It remains in the participants tab.

**Progress line.** One line, as in v5: a spinner, the phase, `round n/m` with one pip per allowed round (filled for rounds started), each participant with `✓` when done, a spinner when working and `○` when queued, and the elapsed time. Milestone E's tool-call lines follow it as v5 section 6 says.

**Colour.** Colours are declared as light and dark pairs and chosen from the terminal's background. No meaning is carried by colour alone: every outcome has its mark and its words.

**Input.** A multi-line text area with the prompt `┃ `. v5's keys for it stand.

**Scroll position.** The conversation line shows, at its right, how many earlier turns are above the view and the scroll position as a percentage. The transcript and changes tabs show the percentage.

**Heights.** The TUI does not promise that everything fits at once; it gives each region a bounded viewport. At any size of at least 80×24:

- The tab bar and the status line are one line each and always shown.
- The input shows 1 to 6 lines and scrolls inside itself beyond that.
- Banners take at most 2 lines. When more apply than fit, the last shown line ends `+<n> more` and the participants tab lists them all. The identity warning is always among those shown.
- The attached line (section 3) is one line: paths in order until the width runs out, then `+<n> more`.
- A command or file list takes at most 5 lines and closes when the drawer opens.
- The short help line is one line and is hidden while the drawer is open.
- The drawer takes 7 lines, or 9 above 30 rows.
- The active tab's content gets what remains and never fewer than 5 lines. When opening the drawer would leave fewer, the input viewport shrinks to 1 line and banners to 1 line first.

Below 80×24 the drawer and the lists do not open, and v4's plain-terminal rule governs the rest.

## 2. Reviews drawer

v5 shows reviewer output only in the transcript tab. This adds a second, lighter view in the chat tab. It shows nothing the transcript does not.

**Wire contract.** New read-only request `reviews{turn_id, cursor?, limit?}` → `{items: [{round, candidate_hash, participant, attempt_id, kind, blocks: [{block_id, state, severity?, evidence?, remedy?}], commentary}], next_cursor?}`.

- One item per accepted verdict, ordered by round and then by roster order. A verdict is accepted when it passed acceptance and the gate. A verdict accepted on repair is listed once, with the repair attempt's id; the malformed original and any unsuccessful repair are not listed and appear only in the transcript.
- `kind` is the verdict's kind as the engine classified it (`approve`, `block`, `approve-with-changes`). `state` is the block's state in that verdict: `opened`, `maintained` or `withdrawn`.
- `commentary`, `evidence` and `remedy` are content references `{ref, sha256, byte_len}`, never inline text, and are fetched as transcript content is, in as many frames as that retrieval needs.
- An item can still be large, because a reviewer chooses its block ids and may maintain many blocks, and nothing bounds either. An item whose encoding exceeds 64 KiB is therefore returned in short form: `round`, `candidate_hash`, `participant`, `attempt_id` and `kind`, which the engine bounds, then `oversized: true` and `record: {ref, sha256, byte_len}` in place of `blocks` and `commentary`. `record` refers to the verdict's stored structured record, the full item in the same shape as canonical JSON, and is fetched as transcript content is. The engine writes that record from its acceptance records, at acceptance for a verdict accepted under this delta and on first request for one accepted earlier. It is never produced by parsing model output at request time.
- `limit` is at most 50. While items remain, the engine returns at least one and at most `limit`, as many as fit in one encoded frame, and a `next_cursor` while more remain; a short-form item always fits. A turn with no accepted verdict returns an empty list and no cursor. The client pages until no cursor is returned.
- Every field is taken from the records the engine wrote when it accepted the verdict. The engine does not reparse stored text to answer, and the TUI never parses verdict text.

The turn entries of `conversation{id}` gain `review_count`, the number of items `reviews` would list in all.

**Behaviour.**

- Every outcome line ends with `· <n> reviews` from `review_count`. Solo turns have none.
- `Ctrl+O` on the chat tab opens the drawer and gives it the keyboard. It is closed by default and its state is not persisted.
- The drawer sits between the chat and the input at the height section 1 gives it. The chat keeps scrolling behind it and is not reflowed.
- The drawer shows one turn and one round: a header with the turn number and the rounds, then each reviewer in roster order with its verdict kind, each block with its state, and its commentary and remedy as content lines. Longer text scrolls inside the drawer.
- Keys while the drawer has the keyboard: `↑` `↓` choose the turn (its input line is highlighted in the chat), `←` `→` choose the round, `PgUp` `PgDn` scroll the drawer, `Esc` or `Ctrl+O` closes it and returns the keyboard to the input. The input cannot be edited while the drawer has the keyboard.
- Open blocks on an `incomplete` or `cancelled` turn stay in the chat whether or not the drawer is open, as v5 requires.
- Reviewers are named by participant id, as the v4 answer tab names them in the approval line. This is the human's view; nothing here is sent to a participant.

## 3. Lists and prompts

**Command list.** When the input begins with `/` and contains no space, a list of matching commands appears above the input, each with a one-line description and its current value where it has one. `↑` `↓` select, `Tab` completes, `Esc` closes. While this list or the file list is open, `Tab` completes; otherwise it switches tabs as v5 says.

**File list (milestone E).** Typing `@` followed by characters with no space opens a list of workspace files matching them. It is filled by a new read-only request, `files{workspace, query, cursor?, limit?}` → `{entries: [{path, size}], next_cursor?, truncated}`:

- `path` is workspace-relative with `/` separators. Only regular files are returned. `query` matches as a case-insensitive substring of the path.
- The engine walks from the workspace root with the confined walk of v5 section 6. It does not follow or return symbolic links, does not descend into a directory matching a protected pattern, and returns no path matching one.
- A file whose name contains a control character, or is not valid UTF-8, is not returned and cannot be attached.
- `limit` is at most 200 and the reply fits one frame. A request examines at most 20,000 directory entries; when that bound stops it, `truncated` is true and the list says the results are partial.
- It returns names and sizes only, reads no file content, and records no snapshot observation.

The TUI reads no workspace file or directory itself.

**Attachment tokens.** Choosing an entry inserts a token into the input and adds the path to the pending attachment set. The set, not the input text, decides what is attached.

- A token is one unit in the text area: the cursor steps over it whole, it cannot be edited inside, and deleting it deletes all of it.
- Its text is `@` and the path when the path has only letters, digits and `. _ - /`; otherwise `@"…"` with `"` and `\` escaped by a backslash. That text is what `input` carries.
- Choosing a path already in the set inserts another token for the same attachment; the set has one entry per path. An attachment leaves the set when its last token is deleted.
- An `@` word that was typed or pasted and not chosen from the list is ordinary text and attaches nothing.
- The attached line above the input lists exactly the set that the next submit will send, with sizes, and nothing else.

**Conversations.** `/conversations` is a selectable list: `/` filters it, `↑` `↓` select, `Enter` switches, `n` starts a new conversation, `Esc` closes. A conversation from another workspace is listed dimmed and cannot be chosen.

**Short help.** `?` on an empty input shows one line of the keys that apply now, above the status line; `?` again hides it. `/help` is unchanged and lists every command and key, including those added here.

**Prompts.** Each prompt is a form with defined outcomes.

- `/apply` shows each path with its operation and what will happen to it, and the workspace. Apply sends `apply{turn_id}`. Discard sends `discard{turn_id}`. Not now and `Esc` send nothing.
- `/override` and `/release` typed without a reason open a form with one required field; `/release` keeps v4's statement of the overlap risk. Submitting sends the command with the entered reason; an empty reason cannot be submitted; `Esc` sends nothing. A command typed with its reason sends as before.

## 4. Attachments (milestone E)

An attachment is a workspace file the human chose, whose content the engine reads at submit and sends with the turn. The human chooses it; no model does. Attachments are allowed whatever tools the preset grants, including none, because the file-content rules below apply by route and not by tool.

**Protocol.** `submit` gains `attachments?`, a list of at most 8 workspace-relative paths. The request hash stored beside `submission_id` covers the list as sent. The TUI sends paths, never content.

**Order at submit.** The steps run in this order, and the first refusal ends the submit.

1. Deduplication by `submission_id`, as v4 defines, before anything else. A matching request replays the stored reply and opens no file, whether or not the files have since changed or gone. A differing request is `submission_conflict`.
2. Path form. Each path must be relative, with no empty, `.` or `..` component and no trailing separator, and contain no control character. Two equal paths in one list are refused. Refusal: `error{code: "attachment", path, reason}`, reasons `form` and `duplicate`.
3. Every refusal v4 and v5 already define for `submit`, in their order, except admission and budget, which run at step 6 so that they count the attachment bytes.
4. Policy, before any file is opened: the egress and web rules below.
5. Reading. The engine opens each path with the confined walk and applies the protected-path check to the resolved relative path. Each must be a regular file. The engine reads through the descriptor at most 256 KiB plus one byte and refuses the file if the extra byte arrives; the size is what was read, not what a prior stat reported. The content must be valid UTF-8, and the attachments together at most 1 MiB. Reasons: `not_found`, `not_regular`, `protected`, `outside`, `too_large`, `total_too_large`, `not_text`. The limits are design constants.
6. Admission and budget, with the attachment bytes counted in every request that will carry them. A turn that does not fit is refused as today; nothing is dropped or truncated.
7. One transaction commits the turn, the attachment records `{path, sha256, byte_len, content ref}`, the snapshot observations of step 5 as bases, `file_tainted`, and the stored reply. Dispatch starts after it commits.

A refused submit creates no turn and no conversation, sets no taint, records no observation, and keeps no content.

**Retention.** Attachment records belong to the turn. Every dispatch of the turn that carries attachments, in every round (the author's first candidate, each revision, each review and re-review, each repair), is built from the stored content. The live file is not read again for the turn.

**Candidate format.** The candidate's canonical form becomes `syndic-candidate/v3`, which adds after `changes` the line `attachments:<attachments_hash or "none">`. `attachments_hash` is the SHA-256 of the manifest: the line `syndic-attachments/v1`, then for each attachment in ascending byte order of its path, three lines: the path's length in bytes in decimal, a colon and the path; the content's SHA-256 in lower-case hex; and the content's length in bytes in decimal. Each line ends with a line feed. The line is `none` when the turn has no attachments. Every candidate of the turn, in every round, carries the same line, with or without tools, with or without a change set. A solo turn's record carries `attachments_hash` beside its answer. Candidates journaled as v2 remain valid as recorded. This is a new binding, not a reading of v5's evidence binding, which covers author tool results and nothing else.

**Snapshot.** Each attachment is recorded at submit as the turn's observation of that path: an immutable base with its `base_sha256`. v5's overlay stands over it. `read_file` of an attached path returns the staged content when a staged write or edit exists and the attached base otherwise, and `unstage` shows the attached base again. The request's attachment block always carries the base.

**What participants receive.** The author's request carries each attachment after the input, fenced by random delimiters, labelled with its path, and marked as data, not instructions. Attachments are presented in the manifest's order, ascending byte order of path, whatever order the submit listed them in. Every reviewer's request, and every later round's requests, carry the same attachments in the same order and form, so one manifest hash describes one input.

**Taint and egress.** Attachment content is file-derived content. v5's definition gains one clause: a turn is file-tainted when it ran with `read` or `write` for any role, or was admitted with an attachment, or when any earlier turn of its conversation is file-tainted. At step 4:

- Destinations. Every participant of the preset receives the attachment, so every one must be on an endpoint with local execution or one named in `tools.file_egress_destinations`. Otherwise the submit is refused `error{code: "egress", route: "attachment", participant}`.
- Web with file content. When `tools.allow_web_with_read` is false for the roster or the preset and any participant of the preset has `web`, the submit is refused `error{code: "egress", route: "attachment", participant}`. This holds whether or not any participant has `read` or `write` and whether or not the conversation was tainted before.
- When the combination is allowed, the attachment bytes are in the turn's canary set (v5 section 6, pre-send check 1) from the moment the turn is committed, before any web call of any role. The turn record states that web and file content were combined, and the transcript shows it.
- After an attachment turn is admitted, the conversation is tainted and v5's `route: "history"` refusals apply to later submits as written, for both the destinations rule and the web rule.

**History.** Attachment content is not carried into a later turn's history by the engine, as tool results are not. The input text, with its tokens, is carried as any input is, and an answer that quotes an attachment is carried as any answer is. The human attaches the file again if a later turn needs it.

**Display.** In the chat, a turn's input is followed by one line per attachment: path, size and hash prefix, and `web + file content` when that combination was in effect. The transcript shows the attached content as safe text.

**Solo.** Allowed, under the same order, rules and binding.

## 5. Additions, by operator decision D2

1. Command list (section 3).
2. Round pips (section 1).
3. Mouse. With `/mouse on`, or `--mouse`, tabs can be clicked, a `reviews` count opens the drawer on that turn, and a round in the drawer header selects it. Off by default, because capturing the mouse stops the terminal's own text selection unless a modifier is held. The setting lasts for the TUI process and is not persisted. The mouse is never captured under `TERM=dumb` or with colour off.
4. Prompts (section 3).
5. Filter in `/conversations` (section 3).
6. Short help (section 3).
7. The drawer opens and closes with a short animation.
8. Turn-end bell and notice. With `/notify on`, or `--notify`, the TUI writes one BEL when it receives, live, the event that takes a turn to a terminal state. Off by default; process-scoped; not persisted. The boundary is the `live_from_seq` the subscription reply already carries: an event with `seq` below it is replay, and one at or above it is live. The TUI keeps, for the life of the process and across reconnects, the highest `seq` it has processed, and ignores for bells and notices any event at or below it. So a replayed event never rings, an empty replay changes nothing, and no terminal event rings or is noticed twice. Separately from the bell: when the TUI reattaches within one process, after a connection loss or `/resume`, the chat shows one line for each turn whose terminal event is replay and above the retained `seq`, naming its outcome and time. A newly started TUI has no retained `seq` and shows no such line.
9. File list and attachments (sections 3 and 4).

Commands added: `/raw on|off`, `/notify on|off`, `/mouse on|off`. Flags added: `--notify`, `--mouse`.

## 6. Plain terminal

v4's rule stands: `TERM=dumb` degrades to no colour and no alternate screen and does not fail. In that mode, and whenever colour is off: the spinner is the word for the state, the meter is omitted and `tokens/limit` stays, pips are omitted and `round n/m` stays, Markdown is off, there is no animation and no mouse, and prompts are single-line questions as in v4. Safe text applies in every mode.

## 7. Dependencies

The TUI adds Lip Gloss, Bubbles, Glamour, Huh, Harmonica and bubblezone. Each is pinned, and the first task lists the transitive modules each brings so the operator can decline any of them before code depends on it. None is used by the engine.

## 8. Acceptance

Against the fake adapter, as before. No test contacts a real endpoint.

Display:

- Every v4 and v5 banner string appears verbatim after its mark, except `after 1 revision` at n of 1.
- An answer, a reviewer commentary, an attachment, a file name and a user input each containing an escape sequence, a carriage return and a bidirectional override are shown with visible escapes in the chat, the transcript, the drawer, the file list and the attached line, with `/raw` on and off, and no such byte is written to the terminal.
- Every displayed content line begins with two spaces after wrapping; an answer whose text is an outcome line, and one that uses carriage returns or cursor movement to reach column 0, are shown indented and unmarked.
- `/raw on` shows the answer's safe text with no Markdown rendering; the `transcript` content reference for the same answer returns the journal's bytes unchanged.
- The status line is the last line at every size and ends with `tab: switch  /help`.
- At 80×24 with the drawer open, eight attachments, three banners including the identity warning, and a six-line input: the tab bar, at least five chat lines, the drawer, the identity warning, the attached line with `+<n> more`, at least one input line and the status line are all shown.

Drawer:

- The drawer is closed at start; `Ctrl+O` opens it; `Esc` closes it; the input does not change while it is open; opening it never cancels or submits anything.
- `reviews` lists each accepted verdict once under its round with its kind and each block's state; a block opened in round 1, maintained in round 2 and withdrawn in round 3 shows those three states in those rounds; a malformed attempt and an unsuccessful repair are not listed, and a verdict accepted on repair is listed once with the repair attempt's id.
- After the stored commentary text of a verdict is replaced by different text in a test fixture, `reviews` returns the same verdict kinds and block states as before; each content reference's `sha256` and `byte_len` match the bytes its retrieval returns.
- A turn with five reviewers, six rounds, and an evidence and a remedy of 600 KiB on every block is retrieved completely by paging `reviews` and fetching each reference; no reply exceeds one frame and nothing is truncated.
- A verdict that maintains 3,000 blocks whose ids are 1 KiB each is returned in short form with `oversized: true`; fetching `record` returns every block id and state, its `sha256` and `byte_len` match the bytes returned, and no reply exceeds one frame. The same holds for a verdict accepted before the record existed.
- `reviews` for a solo turn, and for a turn whose reviewers all failed, returns an empty list and no cursor.
- `review_count` equals the number of verdicts `reviews` lists.
- A turn with a maintained block shows it in the chat with the drawer closed.

Lists and prompts:

- `Tab` switches tabs with no list open and completes with one open.
- `files` returns no path outside the workspace, none through a symbolic link, none under or matching a protected pattern, none whose name has a control character, and no content; it pages with `cursor`; it reports `truncated` when its bound stops it.
- Choosing a file adds it to the attached line and to the next submit's `attachments`; choosing it twice sends it once; deleting one of two tokens keeps it and deleting the last removes it; a typed or pasted `@` word attaches nothing; a path with a space is sent in `input` in the quoted form.
- The attached line equals the `attachments` the submit sends.
- `/apply`: Apply sends `apply`, Discard sends `discard`, Not now and `Esc` send nothing. `/override` with an empty form field sends nothing.

Attachments:

- A retried `submission_id` with identical fields replays the stored reply after the attached file was deleted; with different `attachments` it is `submission_conflict`.
- A path with `..`, an absolute path, a duplicate, a path through a symbolic link, one matching a protected pattern, a directory, a file that is not UTF-8, a file of 256 KiB plus one byte, a file that grows past the limit between stat and read, a set over 1 MiB, and a ninth attachment each refuse the submit; each leaves no turn, no conversation, no taint and no observation.
- A submit refused at the policy step opens no file.
- With the turn, attempt and every other identity field held fixed by the fixture, changing only an attachment's content changes the candidate hash; so does changing only its path; this holds with no tools, with no change set, and in round 2. Two submits identical except that their `attachments` lists are in reverse order produce the same manifest hash and requests that present the attachments in the same order with the same bytes.
- The author's request, each reviewer's request, a revision request and a re-review request contain the attached bytes verbatim, fenced and labelled, after the live file changed; a preset with no tools sends them.
- `read_file` of an attached path returns the attached base after the live file changed; after a staged edit it returns the staged content; after `unstage` it returns the attached base again.
- Under a preset with a participant outside the egress destinations, a submit with an attachment is refused `egress` with route `attachment`.
- With `tools.allow_web_with_read = false` and a participant with `web` and no participant with `read` or `write`, a submit with an attachment in an untainted conversation is refused `egress` with route `attachment`.
- With the combination allowed, a `web_fetch` whose URL contains 16 bytes of an attachment is refused by the canary on the turn's first web call.
- After an attachment turn is admitted, a tool-free submit to that conversation under a preset with a participant outside the destinations is refused `egress` with route `history`.
- A later turn's requests contain no attachment block for an earlier turn's attachment.

Notify and mouse:

- No BEL is written by default. With `/notify on`, one BEL is written for a terminal event whose `seq` is at or above `live_from_seq`, including one exactly at it, and none for an event below it.
- With an empty replay, the next live terminal event rings once.
- After a connection loss during a running turn that then ends, the reattached TUI shows one notice line for it and writes no BEL; a second and a third reconnect that replay the same event show no further line and write no BEL.
- A newly started TUI attached to a conversation whose last turn is terminal shows no notice line.
- The mouse is not captured by default, nor under `TERM=dumb` with `--mouse`.
- With `TERM=dumb` every screen renders with no escape sequence.
- `/help` lists every command, flag and key of this delta.

## 9. Disposition of review

**Revision 1** was reviewed on November 21, 2026. Codex: **BLOCK**, nine required changes (C1–C9), having verified the hash and inspected the code at `df12fa9`. Muse: **BLOCK**, seven required changes (M1–M7). Both gave positions on the eight open points. The record is `review/2026-11-21-v6-delta-rev1-review.md`.

| Finding | Required | Disposition in revision 2 |
|---|---|---|
| C1, M1 | Settle attachment binding; explicit versioned form; every round, tool-free and solo; test with other identity fields fixed | Section 4, candidate format: `syndic-candidate/v3` with an `attachments` line over a defined manifest; solo carries the hash; acceptance holds identity fields fixed. The v2-reuse option is withdrawn |
| C2 | Admission, persistence and retry order; bounded read; one commit; refusal leaves nothing; retention across rounds | Section 4, order at submit (seven steps) and retention |
| C3 | Preserve the staging overlay over attached bases | Section 4, snapshot; acceptance case added |
| C4, M2 | Refuse attachment plus `web` when web-with-file is off; seed the canary before the first web call | Section 4, taint and egress |
| C5 | Two impossible acceptance claims | Section 8: carry-forward tested after an admitted turn; "no attachment block" replaces "no content" |
| C6, M6 | Drawer wire contract; no reparsing | Section 2: `reviews{turn_id}` and `review_count` from accepted-verdict records |
| C7, M4 | Raw versus terminal safety; escape every content source; re-indent after rendering | Section 1: safe text and content lines; `/raw` redefined |
| C8, M3 | File names, token identity, duplicates, edits, normalization; bounded, paged `files` | Section 3: `files` and attachment tokens; section 4, step 2 |
| C9 | Height priorities; notification watermark; "ended while detached" | Section 1, heights; section 5, item 8 |
| M5 | Mouse default and scope in normative text | Section 5, item 3 |
| M7 | Prompt outcomes | Section 3, prompts |

**Revision 2** was reviewed on November 21, 2026. Muse: **APPROVE**, finding M1–M7 satisfied and agreeing with the disposition of C1–C9. Codex: **BLOCK**, finding C2, C3, C4, C5, C7 and C8 satisfied and C1, C6 and C9 partial, with four required changes; it agreed with every point of section 10. The record is `review/2026-11-21-v6-delta-rev2-review.md`. Disposition in revision 3:

| Finding | Required | Disposition in revision 3 |
|---|---|---|
| E1 (C1) | Canonical presentation order of attachments in every request | Section 4, what participants receive: manifest order in every request; acceptance with reversed lists |
| E2 (C6) | Bound `reviews` delivery under the frame cap | Section 2: paged, one item per verdict, text only by content reference; acceptance with oversized fields |
| E3 (C6) | Accepted repair listed once; narrow the mutation test | Section 2 and section 8 |
| E4 (C9) | Use `live_from_seq`; retain last processed `seq`; deduplicate notices and bells | Section 5, item 8; acceptance for empty replay, the boundary and repeated reconnects |

Nothing else differs from revision 2.

**Revision 3** was reviewed on November 21, 2026. Muse: **APPROVE**, finding E1–E4 satisfied. Codex: **BLOCK**, finding E1, E3 and E4 satisfied and E2 partial, with one required change. The record is `review/2026-11-21-v6-delta-rev3-review.md`. Disposition in revision 4:

| Finding | Required | Disposition in revision 4 |
|---|---|---|
| F1 (E2, C6) | A single verdict item can exceed a frame through unbounded block ids and counts; give it a bounded retrieval path; test oversized metadata; allow an empty result | Section 2: an item over 64 KiB is returned in short form with a content reference to its stored structured record; "at least one" holds while items remain; an empty list for a turn with no accepted verdict. Section 8: three cases added. The sentence claiming no field can outgrow a frame is removed |

Nothing else differs from revision 3.

Positions on revision 1's open points, and where they went: 1 (drawer data) is now the contract of section 2. 2 (limits) accepted by both as initial constants, with admission as the real bound. 3 (mouse) off by default, both. 4 (identity warning) banner, both, on condition it cannot be hidden: section 1. 5 (existing tasks) both: reconcile after ratification; see open point 2 below. 6 (binding) explicit versioned form, both. 7 (no `read` tool) allow, both, subject to the egress and web rules. 8 (milestone) E, both.

**Revision 4 terminal votes.** Codex: APPROVE (F1 satisfied; E1–E4 and C1–C9 satisfied; "No outstanding blocks from me"). Muse: APPROVE (F1 satisfied; C1–C9 and M1–M7 satisfied). Voted on the text of revision 4 at 35,151 bytes, SHA-256 `0d8c52a6b970fc61ccff2f69f67734b881f8fbc33e374da73fdae9174ac4d3d0`; this paragraph is the only difference between that text and this one. The record is `review/2026-11-21-v6-delta-rev4-review.md`.

## 10. Open points

1. **Notice for a newly started TUI.** The "ended while detached" line is limited to reattachment within one TUI process, because a new process has no earlier event position and the engine does not track what a human has seen. Showing it across restarts would need the TUI to persist a position per conversation, or the engine to record a last-viewed position. The author leaves it out.
2. **Existing tasks.** Milestone D and E tasks were registered against v5 as ratified. After this delta is ratified, tasks that draw the status line, progress line or chat tab are superseded or followed by new tasks; a hash-bound spec is not edited. None is changed by this draft.
3. **`web + file content` for a preset with only `web`.** v5 shows `web + file content: allowed` in the participants tab per preset, from its tools. An attachment brings the combination to a preset that has only `web`, per turn. This draft records it on the turn and shows it on the attached line and in the transcript; it does not change the participants tab.
