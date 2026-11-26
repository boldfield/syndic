# v9 delta: MCP tools (extends v5 section 6 and v7)

Revision 8, DRAFT, dated November 26, 2026. Author: Fable, in the claude.ai session that couriers the peer rounds. Not approved, not hash-bound. Revision 7 (80,733 bytes, SHA-256 `33cdf3298cc98fa57d787480219796af471634616d5157513490574e09405578`) was approved by Muse and blocked by Codex on November 26, 2026; revision 6 was approved by Muse and blocked by Codex; revision 5 was blocked by both; revision 4 was approved by Muse and blocked by Codex; revision 3 was blocked by both; section 13 records the rounds. This delta extends the v5 delta as ratified (SHA-256 `6cb570b01ee5c40744e3f97c986d33095b9c056d205efd332b024b713b005577`) and the v6 delta as ratified (SHA-256 `1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7`). It also builds on the v7 delta, command execution, as ratified (SHA-256 `ad31943f7005d2a3dbc0ca9245f3f5f16f2f0180626b60cf8a1c3fd8926be428`). It overrides only the clauses named in section 0. Peer review of this delta does not by itself authorize implementation.

## 0. Inputs

Operator direction, November 25, 2026: "we need to design the MCP functionality of Syndic."

v5 section 6 excludes it: "No shell, no command execution, no MCP: those need per-call authorization or an OS sandbox (v2 section 8), and each is a later delta." v7 is the delta for command execution. This is the delta for MCP.

Operator decisions, November 25, 2026:

- D1. Syndic is an MCP client: participants call tools that MCP servers provide. Syndic as an MCP server is not in this delta.
- D2. A tool with side effects does not run during the turn. The call is staged, reviewed with the candidate, and runs only when the human applies it, as file writes do in v5 (section 5).
- D3. Both local servers, which the engine starts, and remote servers, reached over HTTPS, are supported (sections 6 and 7).
- D4. The author and the reviewers may call MCP tools, and each call that runs during a turn passes a referee (section 4).
- D5. The client speaks both the current protocol revision and the earlier one that requires a session handshake (section 3).
- D6. Remote servers are supported with the full authorization flow, not static credentials alone (section 7).
- D7. `/apply` asks once for a turn's file changes and its staged effects together, and files are applied first (section 5).
- D8. The outcome of an applied effect is carried into later turns, so the models know it (section 5).
- D9. A local server runs as one process per participant per turn by default; sharing one process is a per-server setting (section 6).

Clauses this delta overrides, each only as stated:

- V1. v5 section 6, scope: "no MCP". Lifted for tools of configured servers, as below. MCP resources, prompts and server-initiated requests stay excluded.
- V2. The candidate's canonical form gains one line (section 5), so its version becomes `syndic-candidate/v4`. `v3` is v6's.
- V3. v5's `apply` and `discard` are accepted for a turn that has an effect set, with or without a change set, and gain the effect phase (section 5).
- V4. The conversation history gains effect outcomes (section 5), so its version becomes `syndic-history/v2`.
- V5. v7 section 3's descriptor rule and v7 section 4's per-call proxy are adapted for a server process (section 6). They are unchanged for commands.

## 1. What an MCP server is, to syndic

An MCP server is someone else's code or someone else's service. Each property below is why a rule here exists.

- **Its output is untrusted content.** A result may contain text written to steer a model. It is treated as every fetched page is treated in v5.
- **Its tool descriptions are untrusted content too,** and they are shown to models as instructions for use. A server can change them at any time.
- **Its metadata is not evidence.** The protocol's own annotations (`readOnlyHint`, `destructiveHint` and the others) are hints the server asserts about itself. Syndic never classifies a tool by them.
- **A call sends data out.** Arguments reach the server, so a call is an egress route for whatever the model holds, file content included.
- **A call may act on the world,** and what it does cannot be discarded or rolled back by syndic.
- **A local server is a process** with whatever its user may do, unless it is confined.
- **Everything it sends has a size.** A message, a tool list, a schema, a notification stream and an error log are each unbounded unless the client bounds them.

## 2. Configuration

**Servers.** The roster gains `[mcp.<server>]`, default none. A server name matches `[a-z0-9_-]{1,32}`.

- `transport`: `"stdio"` for a local server, with `command`, `args` and the settings of section 6; `"http"` for a remote server, with `url` and the settings of section 7.
- `tools`: a table naming each tool the operator allows and its class, `"read"` or `"effect"`. A tool not named is not offered to any participant and is never called. There is no wildcard. A tool name must match `[A-Za-z0-9_.-]{1,128}`; a server's tool with any other name cannot be allowed.
- `file_egress`: default `false`. Whether file-derived content may be sent to this server in arguments (section 8).
- `timeout_s`: default 60, maximum 300, for one call.
- `protocol`: default `"auto"`, or one supported revision date (section 3).

**Classes.** The class is the operator's statement about what a tool does. `read` means calling it changes nothing outside syndic that matters to the operator; such a call may run during a turn. `effect` means it does; such a call is staged. Where a server's own annotations disagree with the class the operator gave, roster load warns and the operator's class stands. An operator who marks an acting tool `read` has allowed it to act during a turn, and the roster reference says so.

**Canonical JSON.** Wherever this delta hashes or compares JSON, the bytes are the JSON Canonicalization Scheme's (RFC 8785) for that value. A JSON text in which any object has a duplicate member name has no canonical form here: in a tool definition the tool is not offered, and in a call's arguments the call is a tool error.

**Definitions.** A tool's definition is the canonical JSON of the object `{"name", "description", "inputSchema"}` with exactly those three members as the server reported them, an absent description being the empty string. No other member a server reports (a title, annotations, an output schema, icons, `_meta`) is part of the definition or shown to a model. A definition over `mcp.max_definition_bytes` (16,384) is not offered. The input schema must be JSON Schema 2020-12, declared or undeclared; a schema naming another dialect is not offered. A `$ref` may point only inside the same schema; the engine never fetches a schema reference, and a tool with a reference elsewhere is not offered. A tool whose schema has an `x-mcp-header` annotation anywhere in it, at any depth, is not offered: the feature is valid MCP that this client does not support, and the engine sends no header derived from a participant's arguments.

**Pins.** `syndic mcp inspect <server>` lists a server's tools with their annotations and definitions. `syndic mcp pin <server>` records, for each tool named in the roster, the definition's bytes and their SHA-256, the pin, in a pin file beside the roster. Where a tool already has a different pin, `pin` shows the difference between the two definitions and asks before replacing it.

- A pin binds the metadata a model is shown and a reviewer approves. It proves nothing about what the server does when called, which can change with no change of definition.
- The pin file is read once at submit and is fixed for the turn; its hash is recorded on the turn. The run manifest, which is never rewritten, records the hash of the pin file as read at engine start.
- At engine start, and before each turn that could use the server, the engine lists the server's tools (section 3) and compares. A tool whose listed definition does not hash to its pin, or that has no pin, or that the listing does not contain, is not offered for that turn, and the participants reply names it and says why. Nothing is re-pinned automatically. A change the server makes after that listing is not seen until the next one; the engine subscribes to no change notification.
- The definition a model is shown is the pinned bytes, fenced and labelled as content from the server.

**Server identity.** `server_identity` is the SHA-256 of the canonical JSON of the server's configuration as loaded: every member of its `[mcp.<server>]` table, with each secret given as its reference name and never its value; for every secret the table names, static credentials and `env` values alike, that secret's generation; for a local server, the hash of the sandbox policy it runs under (section 6); for a remote server using a login, the issuer, resource and client identifier recorded at login and the login's generation (section 7). It identifies where a call goes and under whose authority. It is recorded on every MCP call and bound into every staged effect.

**Provider names.** A model is offered each MCP tool under the name `mcp_<k>_<s>`: `k` is the tool's index, in decimal, in the list of tools offered to that participant for the turn, sorted bytewise by server name and then tool name; `s` is the first 40 bytes of the tool's name with every byte outside `[A-Za-z0-9_]` replaced by `_`. The index makes the name unique. The description a model sees begins with one engine-written line naming the server and the tool exactly. The mapping is recorded with the turn.

**Presets.** `tools` and `reviewer_tools` may name `mcp:<server>` for every allowed tool of a server, or `mcp:<server>/<tool>` for one. Rules, checked at roster load:

- `reviewer_tools` may hold only `read` tools. A reviewer stages nothing, as in v5.
- An `effect` tool requires a review or deliberate preset, as `write` does. It is invalid in a solo preset.
- A preset granting any MCP tool to a participant whose endpoint names no `referee_model` is refused, as for `web`.
- In a deliberate preset (v8, if ratified) `tools` is the integrator's set and `reviewer_tools` is everyone else's, as for every other tool group.
- The egress rules of section 8.

**Probe.** A participant needs `tool_calls: yes`, as for any tool.

**Not supported.** Resources, prompts, sampling, elicitation, roots and long-running tasks. The engine fetches nothing a server names: no resource link, no icon, no URL in a result or a definition.

## 3. The protocol client

**Revisions.** MCP is specified by dated revisions. Revision 2026-07-28 made every request self-contained and removed the session handshake. The client speaks that revision, called current below, and revision 2025-11-25, the last that requires the handshake, called legacy below. The revision in use with each server is in the run manifest and on each call's record.

**Current.** Every request carries, in `_meta`, the protocol version, the client's capabilities, which are empty, and the client's name and version. Over HTTP each request is one POST carrying the request headers the revision requires, `Mcp-Method` and `Mcp-Name` among them. There is no session.

**Legacy.** The client sends `initialize` naming 2025-11-25 with empty capabilities. It checks the result before anything else is sent: the result must be well formed and within the bounds below, and must name 2025-11-25. A result naming any other version ends the attempt and leaves the server unoffered, since the client negotiates only the revisions it supports. Only then does it send `notifications/initialized`. Over HTTP a session identifier is optional: if the server's reply to `initialize` carries `Mcp-Session-Id`, the client returns it on every later request and deletes the session when it ends it; if the reply carries none, the client sends none. A session belongs to one owner, as section 6 defines owners for local instances; for a remote server a session belongs to one participant for one turn, or to one apply operation, or to one listing, and is never shared or resumed. A request the server sends to the client is answered with an error.

**Both.** Over stdio, messages are newline-delimited JSON on the server's standard input and output. Over HTTP, a response is a JSON body or an event stream; the engine reads the stream until the response to its own request and applies the bounds below to everything before it. The older HTTP-with-SSE transport is not supported. The engine cancels a call by the means the revision and transport define and, whatever the server then does, stops reading and treats the call as ended.

**Choosing the revision.** With `protocol` naming a revision, the engine speaks that revision and detects nothing. With `protocol = "auto"` it decides which era the server is, by the means the current revision gives each transport, sending `server/discover` as a current-revision request and nothing else.

- An answer that is a discovery result, or a JSON-RPC error the current revision defines (the unsupported-version error among them), shows a current-era server, whatever HTTP status carries it. If the answer lists supported versions and they include 2026-07-28, the current revision is used. If they do not but include 2025-11-25, the server has said it speaks both eras, and the engine sends the legacy `initialize`. If the answer lists neither, or is a current-revision error that lists no versions, the server is not offered, and the participants reply says there is no common revision.
- Over stdio, any other error, or no answer within the server's timeout, shows a legacy server, and the engine sends the legacy `initialize`.
- Over HTTP, a `400`, `404` or `405` whose body is not an error the current revision defines, an empty body included, shows a legacy server by the status alone, and the engine sends the legacy `initialize` to the same URL.
- Over HTTP, a `401` or `403` is an authentication failure; a `5xx`, a malformed or oversized success, and a transport failure are reported as what they are. None is evidence of era, none leads to `initialize`, and the server is not offered for the turn.

The decision is kept for the life of the server process over stdio and for the run over HTTP. If a later listing fails in a way that contradicts it, the server is not offered for that turn and is detected again before the next. Detection and listing use `server/discover`, `initialize` and `tools/list` and nothing else; a tool is never called to find out what a server speaks, and a call that fails is never re-sent under the other revision. A server that speaks only revisions older than 2025-11-25, or only the older HTTP-with-SSE transport, is not supported. Where detection fails against a server the operator knows to be legacy, setting `protocol = "2025-11-25"` is the remedy, and the participants reply says so.

**Listing.** The engine follows the listing's cursors to the end, for at most 32 pages and `mcp.max_tools` (256) tools. A listing that passes either bound, or names one tool twice, leaves the whole server unoffered for the turn.

**One send.** The engine sends each `tools/call` once. Nothing sends it again: not the client library, whose retries are disabled; not a reconnect; not a token refresh; not a fallback to the other revision; not recovery. Where the current revision says a client re-issues a request lost with its stream, this client does not, for a call.

**Bounds.** One protocol message, a line on stdio or a body or event over HTTP, is at most `mcp.max_message_bytes` (4,194,304), with JSON nesting at most 64 deep. At most 256 notifications are read while a call is awaited, and they are discarded. A local server's standard error is kept to `mcp.max_stderr_bytes` (1,048,576) for one instance and dropped beyond it, with a count. Passing a message, depth or notification bound closes the connection, and the call in flight has the outcome section 4 or section 5 gives a malformed reply.

**Rendering a result.** Deterministic, from the reply alone:

- If the reply has `structuredContent`, the rendered result is its canonical JSON, followed by one line giving the number of content blocks not shown.
- Otherwise it is the text blocks in order, joined by line feeds, with every block of another type (an image, audio, a resource link, an embedded resource) replaced by one line giving its type and size.
- A reply the server marks as an error is rendered the same way under one line saying the server reported an error.

Before anything is logged, journaled or given to a model, v4's scrub runs over server output with every credential value the engine holds for any server in its list (section 7).

## 4. A `read` call

A `read` tool runs when a participant calls it, in this order. The first refusal ends the call with a tool error naming the rule, and the attempt continues.

1. **Deterministic checks.** The tool is offered to this participant. The arguments have a canonical form, are valid against the pinned input schema, and are at most `mcp.max_argument_bytes` (16,384) in that form. Validation is bounded in depth and steps, and arguments that exhaust the bound are refused. The call is within `mcp.max_calls` (default 20 for the author per turn and 10 for a reviewer per round), and within v5's own per-role bounds, against which it also counts.
2. **Pre-send checks** on the arguments (section 8).
3. **Referee.** v5's referee, with its endpoint rule, model, output form, fail-closed rule and accounting unchanged. Its complete payload is the user's input for the turn; the server name and tool name; the arguments as canonical JSON; and the caller's own earlier MCP calls this turn, as server, tool and arguments. It contains no result, no tool description, no candidate text and no file content beyond what the arguments themselves carry. A third fixed engine template; its hash is in the manifest.
4. **Re-check and call.** After v5's re-check that the turn is open and the epoch current, and for a local server the gate of section 6, the engine sends the call. The call is journaled before it is sent, with its referee decision, the `server_identity`, the pin and the revision.

**Result.** Rendered as section 3 has it and bounded by `tools.max_result_bytes` under v5's rule: a larger one is an error, not truncated. A reply that asks the client for input, a reply that defers its result to a task, and a reply that is malformed or passes a bound are each a tool error; the engine supplies no input and polls nothing.

**As evidence.** An author's MCP results go to every reviewer verbatim with the other author tool results and are bound by the candidate hash. A reviewer's go to that reviewer only, unless a block cites one, as v5 has it for lookups.

**Repeatability.** A `read` call is not re-run by the engine for any reason. A reviewer who wants to check an author's result makes its own call and may get a different answer; the transcript shows both.

## 5. An `effect` call

**Staging.** When the author calls an `effect` tool, nothing is sent to the server. The engine runs section 4's deterministic checks and section 8's pre-send checks, then records a staged effect in the turn's effect list, in call order, and returns a result saying the call is staged under its id and will run only if the candidate is approved and the human applies it. `unstage_effect{effect_id}` removes one. At most `mcp.max_effects` (default 8) may be staged.

A staged effect is `{effect_id, server, server_identity, tool, pin, arguments, arguments_sha256}`. `pin` is the tool's pin from the turn's pin file. `arguments_sha256` is the SHA-256 of the arguments' canonical JSON, and `arguments` is stored as those bytes.

- Arguments are literal. A staged effect cannot refer to the result of another effect; work that needs one call's output in the next call's arguments is two turns.
- No referee runs at staging: nothing leaves the engine, and every reviewer reads the effect.
- A tool of a local server that is given the workspace (section 6) cannot be an `effect` tool; the roster is refused at load.

**Binding.** The candidate's canonical form, `syndic-candidate/v4`, adds after `attachments` the line `effects:<effects_hash or "none">`. `effects_hash` is the SHA-256 of the effects document: the line `syndic-effects/v1`, then for each staged effect in order the fields `effect` (the id), `server`, `identity`, `tool`, `pin` and `arguments` (the value of `arguments_sha256`), each written as v5 writes a history field, a line `<name> <byte-length>` followed by exactly that many bytes and a newline. Changing, adding, removing or reordering an effect changes the candidate hash, so an approval binds the exact calls, the definition approved and the place they go. Candidates journaled under earlier versions remain valid as recorded.

**Review.** Every reviewer's request carries the effect list in full: for each effect its id, server, tool, the pinned definition, and the arguments. A reviewer blocks an effect as it blocks anything else. A revision may change the list.

**After approval.** An approved or overridden turn with staged effects has an effect set, beside any change set. Nothing has been sent. Override applies to a turn with staged effects as it does to one with a change set, and the banner says the effects were not approved. When the turn becomes terminal, approved or overridden, the engine stores, with the effect set, the turn's canary set as it then stands (section 8) and keeps it until the set is final.

**States of one effect.**

- `pending`: staged, nothing sent.
- `skipped`: a check failed immediately before dispatch; nothing was sent. The failed rule is recorded. It is checked again by a later apply until the human acknowledges it.
- `dispatching`: the engine has journaled its intent to send. This is the only state from which a send happens, and it is entered once.
- `done`: the server answered and did not mark the reply an error. The result is stored.
- `failed`: the server answered with a reply it marked an error, with a protocol error, or with a request for input. The reply is stored. A failure the server reports does not show that nothing happened, and every display of this state says so.
- `uncertain`: the engine cannot tell whether the server acted: a timeout, a dropped connection, a reply that is malformed or passes a bound, a result deferred to a task, an engine stop.

`done`, `failed` and `uncertain` are final and are never rewritten.

**Acknowledgement and eligibility.** An effect that is `skipped`, `failed` or `uncertain` may be acknowledged by the human (below). The acknowledgement is part of the effect's journaled record. An effect is eligible to be dispatched when it is `pending`, or `skipped` and not acknowledged, and every effect before it in order is `done` or acknowledged. At most one effect is eligible at a time, the engine never passes an effect that is neither `done` nor acknowledged, and nothing else is ever eligible.

**States of the set.**

The set's state is not stored separately. It is derived, by the first rule below that applies, from the journaled record of each effect and of a discard, so it is the same during a run, between runs and after a crash.

1. `in flight`: some effect is `dispatching`. This comes first, so no set with a call in flight is in any other state, a final one least of all. It ends when the outcome is journaled, or at recovery.
2. `discarded`: the human discarded the set. No per-effect state changes.
3. `applied`: every effect is `done`. No other condition gives this state.
4. `stopped`: some effect is `skipped`, `failed` or `uncertain` and not acknowledged. That effect is the stopping effect, and there is at most one, because the engine dispatches nothing after it and marks nothing after it.
5. `closed`: no effect is `pending`.
6. `appliable`: every effect is `pending` and none is acknowledged.
7. `resumable`: otherwise. Some effect is `done` or acknowledged, an effect is `pending`, and nothing is stopping. A set is in this state between one effect's outcome and the next dispatch, and after an acknowledgement that leaves an effect `pending`.

`applied`, `closed` and `discarded` are final, and a set with a `dispatching` effect is never final. `apply` on a final set returns the state and dispatches nothing. The canary set stored with the effect set is kept until the set is final, so it is kept while a call is in flight.

**Apply.** `apply{turn_id, continue_after?}` is accepted for a turn that is `approved` or `overridden` and has a change set, an effect set, or both. `/apply` shows the change set, if any, and then each effect: the server with its URL or command, the tool, and the arguments in full, as safe text. The form's choices are Apply, Not now and Discard, as in v6. One confirmation covers both.

The whole operation, files and effects, has v5's exclusivity from its start to its end: `submit` and another `apply` are refused `busy` until the run below has finished or stopped. No turn is ever open while an effect is in flight.

1. The change set, if any, is applied by v5's rules. The effect phase begins only if there is no change set or its state is now `applied`. Otherwise no effect runs and every effect keeps its state.
2. The engine takes the first eligible effect in order, and checks: that the server's present `server_identity` equals the effect's; that the pin file's present pin for the tool equals the effect's `pin`; that a listing made now contains the tool with a definition that hashes to that pin; section 8's pre-send checks on the arguments, against the canary set stored with the effect set; for a local server, the gate of section 6; for a remote server using a login, that a usable token exists (section 7). A failed check makes the effect `skipped`, the set `stopped`, and ends the run. A re-pin after approval therefore never updates an approved effect: the new pin differs from the bound one.
3. The engine journals `effect_dispatch{effect_id}` durably, which moves the effect to `dispatching`, and only then sends the call, once. The transition is conditional on the effect being eligible and the set not being final, so it cannot happen twice, cannot happen in a final set, and cannot pass an effect that is neither `done` nor acknowledged.
4. The outcome is journaled: `done`, `failed` or `uncertain`. Anything but `done` makes the set `stopped` and ends the run. After `done` the engine returns to step 2. When no eligible effect remains, the set is `applied` if every effect is `done` and `closed` otherwise.

**A stopped set.** `apply` without `continue_after` on a `stopped` set re-runs step 2 if the stopping effect is `skipped`, which sends nothing that was sent before; if the stopping effect is `failed` or `uncertain`, no effect is eligible, and it returns the state and sends nothing. `continue_after` must name the stopping effect. It is the human's recorded acknowledgement that they have seen that effect's state and dealt with it: the engine journals `effect_acknowledged{effect_id, state}`, after which that effect is never eligible and the set is `resumable` or `closed`, and goes on from step 2. An acknowledged `skipped` effect is never sent. `/apply` on a stopped set shows the stopping effect and offers Continue with the next, Discard the rest and Not now.

**Never twice.** Nothing is ever re-sent by the engine. An `uncertain` effect is the human's to reconcile, as v2 section 7 requires for any uncertain external effect.

**Recovery.** After a crash, an effect found `dispatching` with no outcome becomes `uncertain`, whether or not the call had left, and the set, which was `in flight`, is `stopped`. Every other effect keeps its journaled state and acknowledgement, and the set's state follows from them: a crash after an outcome or an acknowledgement was journaled and before the next dispatch leaves the set `resumable`, or `stopped` or `closed` as the rules give. Recovery sends nothing. The human's next `apply` goes on from the one eligible effect, and an effect that is `done` or acknowledged is never sent again.

**Discard.** `discard{turn_id}` is refused `busy` while an apply is running, so never on a set `in flight`. It moves an `appliable`, `resumable` or `stopped` effect set to `discarded`, and a change set as v5 has it. It changes no effect's state and deletes nothing.

**Who confirms.** Only the human. No participant's output, no reviewer's approval and no quick profile runs an effect.

**Results.** An applied effect's reply is stored whole, up to the message bound, whatever its size; the outcome never depends on the size. The human sees it in the changes tab.

**In later turns: `syndic-history/v2`.** Every history document rendered from this delta on begins with the line `syndic-history/v2`. It is v1's document, with the same fields in the same order and the same framing, and for each included turn that has an effect set these fields after that turn's last v1 field:

- one `effects` field, whose value is the set's state;
- then, for each effect in order, one `effect` field. Its value is a sequence of sub-fields in the same framing: `id`; `server`; `tool`; `arguments`, the canonical JSON bytes; `state`, one of `done`, `failed`, `uncertain` and `not run`; and, for `done` and `failed` only, either `result`, the rendered reply, or, when the rendered reply is over `mcp.max_history_result_bytes` (4,096), `result-ref`, the reply's size in decimal, a space, and its SHA-256 in hex.

Rules:

- `not run` is written for `pending` and `skipped` and for nothing else. An effect that was sent keeps its own state in history whatever later happened to the set, a discard included.
- `uncertain` carries no result.
- `context_hash` is the SHA-256 of the v2 document, as v5 has it for v1, so these fields are bound into every later candidate.
- The fields belong to their turn. `context_from_turn` includes or leaves out a turn's effect fields with the turn. Leaving them out changes what models see and nothing else: the journal keeps every effect, its state and its whole reply for the life of the conversation's record.
- The fields are metered as real bytes with the rest of history. Nothing is shortened to fit; the size rule above is fixed and does not depend on any participant's limit.
- A `result` is untrusted content from the server, fenced and labelled when rendered to a model as section 8 has it.
- Because an apply excludes a submit, a history document never contains a `dispatching` effect.

Conversations and context hashes journaled under `syndic-history/v1` remain valid as recorded.

## 6. Local servers

A local server is a program the engine starts and talks to over its standard input and output.

**Policy.** It runs under v7's sandbox and v7's capability contract, with these differences from a command's policy, which together are the server policy. Its hash is in the manifest and is part of `server_identity`.

- **Filesystem (F).** Read and write in the server's state directory and in a temporary directory made for the instance. Read and execute in v7's system locations. With `mcp.<server>.workspace = "scratch"`, read and no write in the instance's copy of the workspace (below). Nothing else.
- **Network.** N1, or N2 with `mcp.<server>.network_allow` and the instance's proxy listener (below).
- **Process (P).** As v7: the engine can terminate the server and everything it started, and nothing survives the instance.
- **Descriptors.** Standard input and standard output are two pipes to the engine and carry the protocol and nothing else. Standard error is a third pipe, read by the engine into its log under section 3's bound and given to no model. The server holds no other descriptor of the engine.
- **Environment.** v7's fixed set, with `HOME` pointing at the state directory and `TMPDIR` at the instance's temporary directory, plus the names in `mcp.<server>.env`, whose values are secret references under v3's rules.

**Instances.** An instance is one server process with one owner. It starts when its owner first needs it and ends when its owner does. The engine journals `mcp_instance_started` before the process starts and `mcp_instance_ended` only when the backend has confirmed that nothing of the instance remains. An instance with the first record and not the second is unresolved. An engine that stopped uncleanly leaves its instances unresolved; the next engine start resolves each one by v7's termination sequence and cleanup deadline, to `mcp_instance_ended` or to containment unverified (below). Until then an unresolved instance's directories are kept, as a retained instance's are.

- A **turn instance** belongs to one participant in one turn. It is what `mcp.<server>.instance = "turn"`, the default, gives every `read` call. One participant cannot leave state in a process that another then reads.
- An **apply instance** belongs to one apply operation and runs that operation's effects for the server. It has no workspace.
- A **listing instance** belongs to one listing: the engine's before a turn, or `syndic mcp inspect` or `pin`. It is sent discovery and listing requests and nothing else, has no workspace and a fresh state directory, and ends when the listing does.
- The **run instance** exists only under `instance = "run"`: one process for the life of the engine, shared by every participant, every turn, every listing and every apply. The participants reply says the server is shared and that one participant's calls can change what another's return. `instance = "run"` is refused at roster load together with `workspace = "scratch"`.

`instance` is a setting of local servers only. A remote server has no instance; its legacy sessions follow section 3.

**State directories.** `mcp.<server>.state`, default `"fresh"`, gives every instance an empty state directory, removed when it ends. `"persistent"` keeps a directory between instances under a key:

- For a turn instance: the server, the participant, and the hash of the conversation's workspace root. One participant's stored state is never another's, and two workspaces never share a directory, whether or not the server is given the workspace.
- For an apply instance: the server, the author whose turn staged the effects, and the hash of that conversation's workspace root.
- For the run instance: the server alone. It is one process for every conversation of the run, so its directory is shared across participants and workspaces. That is the sharing the participants reply already states for `instance = "run"`, and the reply says it reaches across workspaces.

A directory carries its own provenance (section 8), which no change to the server's command, arguments or settings removes. A directory under another key is kept and never reused under this one. An instance holds an exclusive lock on its directory for its life, and an instance that cannot take the lock is not started. A listing instance never uses a persistent directory.

**The workspace copy.** With `workspace = "scratch"`, a turn instance gets a read-only copy of the workspace, built as v7 section 5 builds a command's scratch directory, from the record and the caller's overlay, before the process starts. v7's three `view` values are taken when the build completes and recorded on every call the instance serves. The copy is not rebuilt under a running process. Before each call the engine compares the caller's present `overlay_sha256` with the instance's; if they differ, as after the author stages a change or in a new round, the instance is ended and a new one started on a fresh copy. A result is therefore always from a process started on the view its record names. An instance with `state = "fresh"` loses its state at that restart.

Giving a server the workspace is its own authorization, shown as its own line in the participants reply: the server process reads workspace content directly, whatever `file_egress` says. `file_egress` governs what models may put in arguments. Section 8 has what follows for taint and for the network.

**The proxy.** With a non-empty `network_allow`, each instance has one proxy listener and credential, as v7 section 4 has for each command, with matching, address rule and journal unchanged, and these differences:

- The proxy opens an upstream connection only while the instance is alive and its owner is: a turn that is open, an apply that is running, a listing in progress, or the run.
- The listener is closed, its tunnels closed and its credential dead at the moment the instance's termination begins.
- Each connection is journaled before it opens with the server, the instance, the call in flight if there is one, the host and the port.
- A server can use the network at any time while its instance lives, from a background thread as well as while handling a call. With `workspace = "scratch"` and a non-empty `network_allow`, a server can send workspace content to an allowed host; the participants reply flags that server and names the hosts.

**Cancelling a call.** A call that times out or is abandoned is cancelled as section 3 has it. A turn or apply instance is then ended, since its state is unknown, and a later call starts a new one. The run instance is left running.

**Termination.** v7's one sequence: SIGTERM, the grace period, SIGKILL, then the cleanup deadline for confirmation.

**Containment unverified.** If a server and everything it started are not confirmed gone within the cleanup deadline, at the end of an instance, at engine stop or at recovery, v7 section 3's outcome applies. The engine journals the failure with the instance. The instance's state directory, temporary directory and workspace copy stay in place, exempt from ordinary cleanup until the containment is confirmed empty. v7's one execution gate for the run covers local servers as it covers commands: once it is closed, by a command or by a server, no command and no server process starts, the engine checks it immediately before starting any server process and before sending any call to a local server, local MCP tools are no longer offered to anyone, and the participants reply says why. A caller is told as v7 has it: a tool error while the turn is open and the epoch current, and no continuation after a terminal transition or at recovery. During an apply, an effect in flight on the failed instance is `uncertain`, the run ends there, and every later effect stays `pending`. After the human acknowledges it, each later effect meets step 2's checks in its turn: one for a local server is then `skipped` by the gate, and one for a remote server is sent. Remote servers are unaffected.

**Self-test.** At engine start, and before the first local instance of each turn, the engine runs v7's six tests under the server policy, driving its helper over the protocol pipes, and two more: a write into a workspace copy fails; a read of another key's state directory fails. A failure leaves local servers unoffered, for the run or for the turn as in v7.

**Under `tools.exec.sandbox = "none"`.** A local server runs unconfined, as a command does in v7, with the same consequences and the same displayed line, naming the server. `network_allow` must be empty, as in v7.

A server that fails to start, or does not answer discovery and listing within its timeout, is not offered for the turn.

## 7. Remote servers

**Connection.** The engine, not a participant, connects to `url`. It must be `https`, except that a URL whose host is the literal `127.0.0.1`, `::1` or `localhost` may be `http` and is connected on loopback. The engine resolves the name, applies the address rule, connects to the address that passed and no other, and follows no redirect.

**Addresses.** v5's address rule applies to every destination this section names, with exactly these exceptions, each of which the operator wrote into the roster:

- A configured `url` whose host is a loopback literal is connected on loopback, as above.
- `mcp.<server>.address_allow` is a list of address ranges, each of which must lie inside the private ranges of RFC 1918 or the unique-local range. It permits the server's own host to resolve into those ranges.
- `mcp.<server>.auth_origins` is a list of origins the login may use (below). A listed origin may resolve inside `address_allow`, and one whose host is a loopback literal is connected on loopback.

No setting permits link-local, multicast or unspecified addresses, and no setting permits loopback for a host that is not a loopback literal. A destination a server or a metadata document names is never excepted by being named. Before every connection of this section, discovery included, the engine validates the destination, resolves, applies the rule and connects to the address that passed. The participants reply shows both settings.

**No ambient authority.** The engine sends only what the roster configures for that server. It forwards no cookie, no other server's credential and no header from a participant.

**Credentials.** `mcp.<server>.auth` is a secret reference: a static token or header set, or a login written by `syndic mcp login <server>`. No credential, token, authorization code, client secret or authorization header is ever journaled, shown, or given to a model.

**Generations.** Every secret a server's configuration names has a generation: a random identifier, not derived from the value, kept in the secret store beside it. It says which assignment of authority is in force, and it is the only thing about a secret that reaches the journal or `server_identity`.

- A login's generation is made new by every completed `syndic mcp login`, the same account or another. Refreshing an access token and rotating a refresh token keep it.
- For a static credential or an `env` value, the secret store also keeps a keyed digest of the value under a key that never leaves the store. When the engine loads a value whose digest differs from the stored one, it makes a new generation and stores the new digest. Neither the value nor the digest is journaled.
- A new generation changes `server_identity`. An effect approved under the old one is `skipped` at apply and needs a new turn and a new approval.

**Login.** `syndic mcp login <server>` runs in the operator's terminal, never inside a turn or an apply.

1. It requests the server's protected-resource metadata from the server's own origin, under the server's own address rules, sending no credential, and requires that the resource it names is the server's URL. It takes the authorization server's issuer from it.
2. The issuer's origin, and the origin of every endpoint the issuer's metadata names (authorization, token, registration), is a destination of its own. Each must be `https` and pass v5's address rule, or be listed in `mcp.<server>.auth_origins`. A listing authorizes every request of login and refresh to that origin, the metadata request included. An origin that is neither is refused before any connection to it. A private server does not by itself authorize a metadata document to send the engine to a loopback, link-local or private address.
3. It requests the issuer's metadata, sending no credential, and requires that the issuer the metadata names is the one it asked. It requires that the metadata offers the S256 challenge method.
4. It identifies the client (below), then runs the authorization-code flow with PKCE S256 and the `resource` parameter naming the server's URL, in the authorization request and in every token request. It prints the authorization URL for the operator to open and listens on a loopback address for that one login.
5. The `state` value is random, belongs to this login, and is accepted once; the listener closes after one request. If the response carries `iss`, it must equal the recorded issuer. If it carries none and the metadata says the server sends one, the response is refused. Otherwise its absence is accepted.
6. It stores the tokens in the secret store under the issuer, the resource and the client identifier together, with a new generation (above). Two resources on one host, or two issuers for one resource, never share a token.

**Client identity.** By a client ID metadata document where the authorization server accepts one; by dynamic registration where it accepts that, naming the application type the revision requires; otherwise by `mcp.<server>.client_id`, which the operator registered by hand. A registration, and any client secret it returned, is stored in the secret store under the issuer that made it and is used with no other issuer. Where the metadata document is hosted is open point 1.

**Use and refresh.**

- The access token is sent to the configured server and nowhere else. The refresh token and any client secret are sent only to the recorded issuer's token endpoint.
- The engine refreshes an expired access token before a call, under one lock for that credential, so two refreshes never race. When the reply carries a new refresh token the engine writes it to the secret store before it uses the new access token. A refresh the issuer rejects marks the login unusable.
- If the server's protected-resource metadata now names another issuer than the one recorded, the stored tokens and registration are not used, and the operator must log in again.
- A reply that the token lacks a scope is a tool error naming the scope; the engine requests no further scope by itself. `mcp.<server>.scopes` sets what login asks for; absent that, login asks for what the server's challenge or metadata names.
- An unusable login, a rejected token or a missing scope is a tool error during a turn and a `skipped` or `failed` effect during an apply, by section 5's rules. The engine never opens a login by itself, and never sends a call a second time after obtaining a token.

**Scrub.** Every credential value the engine holds, static or issued, and every value of a local server's `env`, is in v4's scrub list. The scrub runs over local servers' standard error and over everything a server returns (results, definitions, error text) before any of it reaches a log, the journal or a model.

A remote server is a destination outside the machine (section 8).

## 8. Egress, taint and the pre-send checks

**Destinations.** Every configured server is listed in the participants reply and the manifest with its transport, its URL or command, its classes, its `file_egress` setting, whether it is file-holding and why, its `server_identity` and, for a local server, its instance, state, workspace and `network_allow` settings.

**File-holding servers.** A server is file-holding when it can have received workspace content: its configuration gives it the workspace (`workspace = "scratch"`) or lets models send it file content (`file_egress = true`), or it is file-holding by the mark below. Every result from a file-holding server is file-derived content under v5 section 6.

**The mark.** What a server was given it may keep: in a persistent directory, in a shared process, or, for a remote server, on its own side. So the engine journals two marks, durably, before each exposure they record: before a server process is started with a workspace copy, and before the first `tools/call` is sent to a server whose configuration lets file content into arguments. Both marks are written at each of those two moments, the directory's whenever the instance holds a persistent directory. Discovery and listing mark nothing. If the engine stops between the mark and the exposure, the mark stands and nothing was exposed; no exposure happens without its mark already in the journal.

- `mcp_file_holding{destination}` marks the destination: the URL of a remote server, or the command and arguments of a local one, whatever name the roster gives it.
- `mcp_state_file_holding{directory}` marks the persistent state directory the instance holds, by its key. Every instance that later opens a marked directory is file-holding, whatever its command, arguments or settings then are.

Both are read from the journal at every engine start, and before any preset is checked. A server is file-holding by the mark when its destination is marked or when any persistent directory it could open under the preset being checked is marked. No change of configuration removes a mark: a server whose workspace grant or `file_egress` is taken away is still file-holding. 

**Clearing.** `syndic mcp clear-provenance <server>` acts on that server's destination, under every roster name that has it, and on every marked persistent directory of that server. It records the operator's statement that the server holds none of that content; for a remote server that is a statement about the server's side.

- It runs only while no engine is running on that state. An engine holds its exclusive lock for its whole life; the command takes that lock and is refused if an engine holds it, so no instance can start or be called while it runs.
- Holding the lock does not show that an earlier engine's servers are gone. The command is therefore also refused while any instance of that server is unresolved (section 6), that is, has `mcp_instance_started` and no `mcp_instance_ended`, whether or not a containment failure has been journaled for it, and while any containment of that server is unverified. It does not resolve instances itself: an engine start does. Directories of an unresolved or retained instance are not deleted by it, and no mark is cleared.
- It journals `mcp_provenance_clear_started{destination}`, deletes each marked directory, a directory already absent counting as deleted, and only when every deletion has succeeded journals `mcp_provenance_cleared{destination}`. The marks are in force until that second record. A deletion that fails, or a crash between the two records, leaves every mark in force, and running the command again finishes the work.

It follows that:

- A preset that grants any participant a tool of a file-holding server is treated as granting `read`, for every rule of v5 that turns on it. Every participant must then be on an endpoint that v5's egress rule accepts, checked at roster load and at submit, before any workspace content reaches the server or a model. A turn under such a preset is file-tainted.
- A result of a file-holding server enters the turn's canary set when it is recorded, as both the reply's text bytes and the rendered bytes, before any later pre-send check of any role, a later call in the same tool step included, without waiting for delivery to a model.

A result from a server that is not file-holding is not file-derived and is not added to the canary set. It is a lookup, and v5's rule for the egress of lookups applies to it.

**The canary set, for MCP arguments.** v5's set for the turn (snapshot contents read, staged writes, base contents of staged paths, the diffs given as evidence, and the `answer` and `change` fields of the history document), v6's attachments, v7's command output, the results of file-holding servers above, and the `effect` fields of the history document.

**Pre-send checks.** Deterministic, no model, on the arguments of every `read` call before its referee, of every `effect` at staging, and of every effect again at apply against the set stored with its effect set.

1. Canary. With `file_egress = false`, the call is refused if any member name or string value of the arguments, taken after JSON unescaping and after v5's bounded decoding (percent, base64, base64url and hex, each alone and any one nested once inside any one), or the concatenation of all string values in document order, contains a run of at least 16 bytes that occurs in the set.
2. Shape. With `file_egress = false`, no string value is longer than `mcp.max_string_bytes` (2,048).

With `file_egress = true` both checks are skipped for that server, the operator having accepted it as a destination for file content, and the participants reply names the participants exposed.

**Web with file content.** For `tools.allow_web_with_read`, a remote MCP tool counts as `web`, and so does a tool of a local server with a non-empty `network_allow`. With the setting `false`, a preset in which any participant has one of those and any participant has `read`, `write` or a tool of a file-holding server is refused at roster load, and a local server with both `workspace = "scratch"` and a non-empty `network_allow` is refused whatever the presets grant.

**Injection.** A result and a tool definition are fenced by random delimiters and labelled untrusted, as every tool result is. Nothing in a result changes which tools are offered, which servers exist, or what the engine does.

## 9. Lifecycle and accounting

- A `read` call in flight when the turn is cancelled, reaches its deadline or becomes terminal is abandoned: the call is cancelled (section 3), journaled `abandoned`, no result delivered, nothing re-sent.
- Turn instances and remote sessions of a turn end when the turn becomes terminal.
- Admission meters the rendered tool request and the rendered result under v5's rules. Tool definitions offered to a model are part of its rendered request and are metered there.
- A late or quarantined tool step calls nothing and stages nothing.
- An apply is not charged to any turn's budget. Each effect is bounded by the server's `timeout_s`, after which it is `uncertain`.

## 10. Protocol and TUI

- `apply{turn_id, continue_after?}` and `discard{turn_id}` are as section 5 has them. The `apply` and `changes` replies gain the effect set: its state and, for each effect, its state, the failed rule for a `skipped` one, and any acknowledgement.
- In every reply and event, an effect's arguments, a definition or a result whose bytes exceed 4,096 is carried as a v6 content reference and read in pages, never inline.
- New command-line verbs: `syndic mcp inspect`, `syndic mcp pin`, `syndic mcp login`, `syndic mcp clear-provenance`. None runs inside a turn, and `clear-provenance` runs only with the engine stopped.
- The TUI shows an MCP call under the progress line as it shows a fetch: server and tool, the referee decision, and the outcome. A staged effect shows as `staged e1 <server>.<tool>`.
- The changes tab lists the effect set under the change set: each effect with its server, URL or command, arguments, state and result, and for `failed` the line that a reported failure does not show that nothing happened.
- The participants tab lists each server with what section 8 says the participants reply holds: transport, pin state per tool, `file_egress`, whether it is file-holding and why, `server_identity`, `address_allow`, `auth_origins`, instance, state, workspace and `network_allow`, the workspace line, the shared line, the exposure flag, and the unconfined line where each applies.

## 11. Acceptance

Against the fake adapter, a fake MCP server of each revision and a fake authorization server, all in the test binary. No test contacts a real endpoint or a real MCP server.

Configuration: a tool not named in the roster is never offered and a call to it is a tool error; an `effect` tool in `reviewer_tools` or in a solo preset is refused at load; an MCP grant on an endpoint with no `referee_model` is refused at load; an annotation that disagrees with the operator's class warns and the class stands; a server or tool name outside the stated patterns is refused; two definitions that differ only in member order or whitespace have one pin, and two that differ in a description byte have two; a definition with a duplicate member name, another schema dialect, a reference outside the schema, an `x-mcp-header` annotation, or more bytes than the bound is not offered and no reference is fetched; a tool with no pin, absent from the listing, or whose listed definition does not hash to its pin is not offered and is named in the participants reply; the definition a model receives equals the pinned bytes; `pin` over an existing different pin shows the difference and changes nothing without confirmation; a pin file changed during a turn does not change that turn; two tools whose names differ only in bytes outside `[A-Za-z0-9_]` receive different provider names, and the recorded mapping returns each call to the right server and tool; changing a server's URL, command, argument, secret reference name or sandbox policy changes its `server_identity`; so does replacing the value of a static credential or an `env` secret, through its new generation; refreshing an access token and rotating its refresh token leave `server_identity` unchanged.

Protocol: against a fake server of each revision, tools are listed and called; current-revision requests carry the version, capabilities and client identity and, over HTTP, the required headers; with `protocol = "auto"`: a server answering discovery with a result naming 2026-07-28 is used under the current revision; one answering with the unsupported-version error listing 2025-11-25 is used under the legacy revision, and one listing neither is unoffered with no `initialize` sent; over stdio a server answering discovery with an unknown-method error, and one that does not answer, are each sent `initialize`; over HTTP a `400`, a `404` and a `405` whose body is neither a discovery result nor a current-revision error, an empty body included, each lead to `initialize`; a `404` or `405` carrying a current-revision error is treated as the `400` cases that follow; a `400` carrying the unsupported-version error that lists 2025-11-25 leads to `initialize` because of that list, one that lists neither supported revision leads to none, and a `400` carrying a current-revision error with no version list leads to none; a `401`, a `500`, a malformed success and a refused connection each leave the server unoffered and cause no legacy attempt; with `protocol` naming a revision no discovery is sent; an `initialize` result naming another version is not followed by `initialized`; a legacy HTTP server that issues no session identifier is used with none sent, and one that issues one receives it on every request and a delete at the end; the manifest names the revision in use; no `tools/call` is sent during detection or listing; a listing past the page or tool bound, or naming one tool twice, leaves the server unoffered; under the legacy revision two participants never share a remote session, and share a local one only under `instance = "run"`; a request from the server to the client is answered with an error; a message over the size bound, nesting over the depth bound and a 257th notification each close the connection; standard error past its bound is dropped and counted; a reply with structured content renders as its canonical JSON with the count line, and one without renders its text blocks in order with one placeholder line per other block; a call whose stream drops, whose token was refreshed, or whose connection was re-made is sent once.

Read calls: arguments with a duplicate member name, invalid against the pinned schema, over the size bound, exhausting the validation bound, or past the call bound are refused before the referee; the referee request matches the template byte for byte and contains no result and no tool description; a referee deny, timeout, adapter error and malformed reply each yield a tool error and send nothing; a late referee allow sends nothing; the call is journaled before it is sent, with its `server_identity`, pin and revision; a result over `tools.max_result_bytes` is an error; a reply asking for input, a reply deferring to a task and a malformed reply are each a tool error and nothing further is sent; an author's result reaches every reviewer verbatim and changing it changes the candidate hash; a timed-out call is not sent again.

Effects, staging and binding: an `effect` call sends nothing to the server and returns a staged id; a ninth is refused; `unstage_effect` removes one; an `effect` tool on a workspace server is refused at load; changing an argument, the order, a tool's pin or a server's identity changes the candidate hash; an effect whose server or tool name contains a line feed cannot exist, and the effects document for any list parses back to that list; each reviewer's request carries every effect with its arguments and pinned definition.

Effects, apply: nothing is sent before `/apply`; Not now and `Esc` send nothing; one confirmation covers the change set and the effects, and no effect is sent unless the change set is `applied`; a turn with effects and no change set is applied and discarded by the same requests; `submit` and a second `apply` during the effect phase are refused `busy`; effects run in order; a server error marks `failed`, a request for input marks `failed`, and a timeout, a dropped stream, a malformed reply and a deferred result each mark `uncertain`, and in every such case the set is `stopped`, later effects stay `pending` and nothing is re-sent; `apply` on a set stopped at a `failed` or `uncertain` effect sends nothing; `continue_after` naming the stopping effect journals the acknowledgement and runs the next, and naming any other effect is refused; a set whose acknowledged effects leave nothing pending is `closed`; a server whose URL changed after approval, a tool re-pinned after approval even though the new pin matches the new definition, a listing that no longer contains the tool, and arguments carrying 16 bytes of a file the turn read, with `file_egress = false`, each mark the effect `skipped` and send nothing; a later `apply` re-checks a `skipped` stopping effect and sends it when the check now passes; an engine stopped after `effect_dispatch` is journaled and before the call is sent, and one stopped after it is sent, each recover the effect as `uncertain` and send nothing; two concurrent `apply` requests dispatch each effect at most once; discarding a set after one effect is `done` leaves that effect `done` in the changes reply and in history; after a `skipped` effect is acknowledged and the next effect succeeds, the acknowledged effect is not selected again, is never sent, and the set is `closed`; after a `failed` effect is acknowledged and every later effect succeeds, the set is `closed` and not `applied`; an acknowledgement journaled before a crash is in force after recovery; with the first effect `done` and the second `pending` the set is `resumable` and the second is dispatched in the same run; an engine stopped after the first effect's outcome is journaled and before the second is dispatched, and one stopped after an acknowledgement is journaled and before the next dispatch, each recover a `resumable` set, send nothing in recovery, and on the human's next `apply` send the next effect once and no completed or acknowledged effect again; `apply` on a set stopped at an unacknowledged `failed` effect does not send the effects after it; a set of one effect, and a set whose last effect is being sent, are `in flight` and not final from `effect_dispatch` until the outcome is journaled, the stored canary set exists throughout, a `discard` in that window is refused `busy`, and an engine stopped in that window recovers the effect as `uncertain` and the set as `stopped`; the outcome then recorded gives `applied` or `stopped` and no state was final before it; when containment of a local server fails during an apply, the effect in flight is `uncertain`, every later effect is still `pending`, there is one stopping effect, and after its acknowledgement a later local effect is `skipped` by the gate and a later remote effect is sent; `apply` and `continue_after` on an `applied`, a `closed` and a `discarded` set each return the state and dispatch nothing; with two accounts and an otherwise identical configuration, an effect approved under a login to the first is `skipped` after a login to the second, and is `skipped` after a second login to the first; a token refresh and a refresh-token rotation leave `server_identity` unchanged and the effect is sent; replacing a static credential's value changes its generation and `server_identity`, and neither the value nor its digest appears in any journal row; an override of a turn with staged effects shows that they were not approved; a reply larger than the result bound is stored whole with outcome `done`.

History: a document rendered after this delta begins `syndic-history/v2` and parses back to its fields; a `done` and a `failed` effect carry the result; a result over the history bound is carried as its size and hash; an `uncertain` effect carries none; a `pending`, `skipped` or never-applied effect is `not run`, and a sent effect of a discarded set is never `not run`; changing an effect's state changes `context_hash`; `context_from_turn` past a turn removes its effect fields from the document and from no journal row; a history with effect fields that does not fit a participant is refused as v5 refuses any history and nothing is shortened; a `v1` conversation still loads and its stored hashes still verify.

Local servers, under each supported backend with a fixture server in the test binary: the protocol round-trips over standard input and output, and bytes on standard error reach the log and no model; a server process cannot read the workspace with `workspace = "none"`, cannot write the workspace copy, cannot open a connection with no network list, sees only the fixed and listed environment with `HOME` at its state directory, and does not survive its instance; with `instance = "turn"` a value an author's call stored in the server is absent from a reviewer's instance, with `state = "fresh"` and with `state = "persistent"`; conversations in two workspaces use two persistent directories for a turn instance, with and without the workspace grant; the run instance uses one and the participants reply says it is shared across workspaces; a second instance on a locked persistent directory is not started; a listing instance receives no call and leaves no state; an apply instance is created for an apply under `instance = "turn"`, has no workspace, and ends with the apply; `instance = "run"` with `workspace = "scratch"` is refused at load; after the author stages a change, the next call to a workspace server is served by a new process whose recorded `overlay_sha256` is the new one; a cancelled call ends a turn instance and leaves the run instance; a proxied connection is journaled before it opens with server, instance and call, the listener is closed when termination begins, and no connection opens after the instance ends or outside its owner's life; every instance has `mcp_instance_started` in the journal before its process starts and `mcp_instance_ended` only after confirmation; an engine start after an unclean stop resolves each unresolved instance to one or the other and keeps its directories until then; a backend that never confirms a server's termination yields the journaled failure, retained directories, a closed gate that stops later commands and later server starts, no local MCP tool offered, and no continuation after a terminal transition or at recovery; a gate closed by a command stops server starts; each self-test failure leaves local servers unoffered and is named.

Remote servers: a plain-HTTP URL whose host is not a loopback literal is refused at load; a redirect is not followed; a configured loopback-literal URL is connected on loopback; a configured hostname that resolves to loopback is refused whatever `address_allow` holds; a private address is refused without a matching `address_allow` range; an `address_allow` range outside the private ranges is refused at load; link-local is refused whatever the settings hold; the credential is sent to the configured server and to no other, and appears in no journal row, event, transcript item or model request.

Authorization, against the fake authorization server: login completes with PKCE S256 and sends `resource` in the authorization and token requests; resource metadata naming another resource, issuer metadata naming another issuer, metadata without S256, a response with a wrong `iss`, a response without `iss` when the metadata promised one, a wrong `state` and a replayed `state` are each refused and store nothing; a response without `iss` when none was promised is accepted; an issuer on a private address is refused before any connection unless its origin is in `auth_origins`, and with it listed its metadata is fetched and the login completes; a token endpoint on a private address named by a public issuer's metadata is refused unless its origin is listed; a metadata document naming a loopback endpoint whose origin is not in `auth_origins` is refused, and one naming a loopback-literal endpoint whose origin is listed is used; every discovery connection goes to the address that was validated; no credential is sent in a discovery request; each of the three client-identity routes is used when the fake server accepts only that one; tokens for two resources on one host are stored and used separately; a registration is not used with a second issuer; an expired token is refreshed before the call, two concurrent calls cause one refresh, and a rotated refresh token is in the store before the new access token is used; an issuer change in the resource's metadata stops use of the stored tokens; a missing-scope reply is a tool error naming the scope and requests nothing; a failed refresh yields a tool error, or a `skipped` effect at apply, and opens no login; a token value placed in a server's result, in its error text and in a local server's standard error is scrubbed before the log, the journal and the model.

Egress: with `file_egress = false`, arguments carrying 16 bytes read from a file, from an attachment, from command output, from a workspace server's result earlier in the same tool step, or from an `effect` field of history are refused before the referee, in a string value, in a member name, base64-encoded, and split across two string values; a string over the shape limit is refused; with `file_egress = true` both pass and the participants reply names the exposed participants; a result of a server with `file_egress = true` enters the canary, and a preset granting its tool is treated as granting `read`; after a call under `workspace = "scratch"` or `file_egress = true`, removing that setting leaves the server file-holding, under a new roster name as well, across an engine restart, and in a conversation with another workspace, and its results still enter the canary and its presets still need eligible endpoints; a local server with `file_egress = true` and persistent state, then given another command and `file_egress = false`, opens the same directory and is file-holding by the directory's mark, and a preset granting its tool still needs eligible endpoints; an engine stopped after a mark is journaled and before the server process starts or the call is sent recovers with the mark and nothing sent, and in no run is a workspace copy opened to a server or a file-bearing call sent before its mark is in the journal; a listing marks nothing; a server process started with a workspace copy and a persistent directory has both marks in the journal before it starts, and a later server on that directory without the workspace is file-holding; `syndic mcp clear-provenance` removes the destination's mark under every roster name, deletes the server's marked persistent directories, and journals both records with the destination; it is refused while an engine is running, a shared instance alive included, and while a directory of that server is retained after a containment failure; after an engine is killed while a server's descendant process survives, the command, run before any engine start, obtains the lock and is refused, deletes nothing and clears no mark, with no containment failure yet journaled; after an engine start has resolved that instance as ended the command succeeds, and after it has resolved it as containment unverified the command is still refused; when one directory cannot be deleted, and when the command is stopped after the first record and before the second, every mark is still in force at the next engine start, and a second run completes; a preset granting a workspace server's tool with a participant on an endpoint v5's egress rule rejects is refused at load, and a turn under such a preset is file-tainted; with `tools.allow_web_with_read = false`, a preset granting `read` and a remote MCP tool, a preset granting `read` and a tool of a local server with a network list, and a local server with both the workspace and a network list are each refused at load.

## 12. Milestone

One milestone, H, after E and after v7's milestone F: configuration, names, classes, canonical definitions and pins; server identity; the protocol client for both revisions with its bounds; `read` calls with checks and referee; staging, binding and review of effects; the effect state machine, apply, continuation and recovery; `syndic-history/v2`; the server policy, instances, state directories, workspace copies and the instance proxy; containment failure and the shared gate; remote servers and the address rules; login, client identity, storage and refresh; egress and taint wiring; the command-line verbs; protocol and TUI display; acceptance, including the per-backend contract tests. Roughly forty-five to sixty tasks.

## 13. Disposition of review

**Revision 3, November 25, 2026.** 28,207 bytes, SHA-256 `769c0530…ac978e7`. Blocked by both. Muse required six changes (M1 to M6) and Codex twelve (C1 to C12). Both supported the author's position on every open point. Revisions 1 and 2 were author drafts and were not reviewed. Sections were renumbered in revision 4: revision 3's sections 3 to 11 are now 4 to 12 and its section 12 is now 14, with the protocol client new as section 3 and this section new as 13.

| Point | Required | Where answered |
|---|---|---|
| M1 | File-derived MCP results enter the canary, raw and rendered; so do file-derived effect results in history | Section 8: file-derived MCP content; the canary set, which holds the `effect` fields of history |
| M2 | Apply-time checks use the staging turn's canary and the bound pin | Section 5: canary set stored with the effect set; step 2 compares the bound pin, the pin file and a fresh listing |
| M3 | Persistent state is per participant | Section 6: state directory keys |
| M4 | Session sharing consistent; `instance` local only | Sections 3 and 6; section 11 protocol |
| M5 | Server termination failure has v7's unverified outcome | Section 6: containment unverified, one gate for commands and servers |
| M6 | Proxy lifetime, journaling and background egress | Section 6: the proxy; section 8: web with file content |
| C1 | Bind approval to an immutable execution target | Section 2: server identity; section 5: staged effect, binding, apply step 2 |
| C2 | Durable effect state machine and continuation | Section 5: states, apply, a stopped set, recovery, discard, history rules |
| C3 | Apply keeps exclusivity; remove the contrary sentence | Section 5: apply. The sentence and its acceptance case are removed |
| C4 | MCP-specific sandbox and lifecycle contract | Section 6: policy, instances, the proxy, cancelling, containment, self-test; V5 |
| C5 | Snapshot refresh and persistent-state isolation | Section 6: the workspace copy, state directories, the run instance |
| C6 | Route-based taint and canary coverage | Section 8 throughout; section 6: the workspace line |
| C7 | Canonical pinning and effect serialization | Section 2: canonical JSON, definitions, pins, provider names; section 5: binding; section 3: listing |
| C8 | Complete the OAuth binding rules | Section 7: login, client identity, use and refresh, scrub |
| C9 | Narrow private-network authorization | Section 7: addresses; login step 3. `allow_private` is replaced by `address_allow` and `auth_origins` |
| C10 | Two protocol adapters and replay policy | Section 3 |
| C11 | Bound and specify untrusted protocol data | Section 3: bounds, rendering; section 2: definitions, not supported; section 5: results; section 10: content references |
| C12 | `syndic-history/v2` as an exact format; effect-only apply | Section 5: in later turns; V3 |

Answered by narrowing rather than by adding: an `effect` tool cannot belong to a workspace server, and `instance = "run"` cannot have the workspace (C4, C5); a tool whose schema uses `x-mcp-header` is not offered (C11).

Optional notes taken: `network_allow` in the participants tab; `arguments_sha256` defined; a registration's client secret stored and never journaled; `HOME` for a server; the URL or command shown at `/apply` and bound through `server_identity` (Muse). Pin changes shown as a difference before replacement (Codex).

Optional notes not taken: giving the referee the pinned description or an operator-written purpose (Muse). The description is the server's text, and v5 keeps untrusted content out of the referee's payload; an operator-written purpose is a possible later addition. Re-listing before every call (Muse) is not taken; section 2 states what a listing does not catch.

**Revision 4, November 25, 2026.** 60,086 bytes, SHA-256 `81f0b938…d65c3f8`. Approved by Muse: M1 to M6 satisfied, no required change, the three narrowings supported. Blocked by Codex: C3, C4, C7, C8, C11 and C12 satisfied; five required changes (E1 to E5). Both accept open point 6.

| Point | Required | Where answered |
|---|---|---|
| E1 | Acknowledgement and aggregate completion consistent; no dispatch from a final set | Section 5: acknowledgement and eligibility; states of the set; apply steps 2 to 4; a stopped set; recovery |
| E2 | Bind the authorization assignment, not only its reference name | Section 7: generations; section 2: server identity |
| E3 | Provenance across retained server state | Section 8: file-holding servers and the mark; section 6: state directory keys with the workspace scope |
| E4 | Address-policy contradictions; `auth_origins` and issuer metadata | Section 7: addresses; login steps 1 to 3 |
| E5 | Correct and complete compatibility detection; optional session identifier; validate the `initialize` result | Section 3: legacy; choosing the revision |

E3 is answered more widely than asked: a server with `file_egress = true` is file-holding as a workspace server is, because it too can return what it was sent.

Optional notes taken from revision 4's verdicts: which outcome leads to the legacy attempt; the canary set stored for an overridden turn; `x-mcp-header` at any depth (Muse). The run manifest records the pin file's hash at start and is never rewritten; `x-mcp-header` described as unsupported by this client, not as invalid (Codex). Muse's note that the run instance does the listing under `instance = "run"` is already what section 6 says of the run instance.

**Revision 5, November 25, 2026.** 70,839 bytes, SHA-256 `fda4b9c9…10bb5d`. Blocked by both. Muse: E1, E3 (with its widening), E4 and E5 satisfied; one required change (N1). Codex: three required changes (F1 to F3). Open-point positions unchanged.

| Point | Required | Where answered |
|---|---|---|
| N1, F3 | Acceptance contradicted the text: a secret's value and `server_identity`; a `400` carrying a current-revision error; metadata-named loopback endpoints | Section 11: configuration, protocol, authorization. Section 3 now says what an error without a version list means |
| F1 | A resumable aggregate state between effects; recovery after an outcome or an acknowledgement | Section 5: the set's state is derived from the effects' records, with `resumable`; eligibility requires every earlier effect `done` or acknowledged; recovery |
| F2 | Provenance bound to retained storage; workspace isolation or a stated sharing policy; the mark durable before exposure | Section 8: the two marks, written before exposure; section 6: state directory keys always carry the workspace for turn and apply instances, with the run instance's sharing stated |

Optional notes taken from Muse: the set's state during a run (answered with F1); marking on the first `tools/call` and never on listing; `clear-provenance` by destination, deleting marked directories; file-holding and `server_identity` in the participants tab.

**Revision 6, November 25, 2026.** 76,689 bytes, SHA-256 `c557cc2b…49dcc8ab`. Approved by Muse: N1 and F1 to F3 satisfied, no required change. Blocked by Codex: F3 satisfied, F2's core satisfied; three required changes (G1 to G3). Open-point positions unchanged.

| Point | Required | Where answered |
|---|---|---|
| G1 | A `dispatching` effect must prevent final aggregate states | Section 5: states of the set, rule 1, `in flight`; the canary set kept while a call is in flight; discard refused during an apply; recovery |
| G2 | Containment failure must keep one stopping effect | Section 6: containment unverified. Later effects stay `pending` |
| G3 | Safe, recoverable provenance clearing | Section 8: clearing. Engine stopped, refused while containment is unverified, two journal records with the marks in force until the second |

Optional notes taken from Muse: `404` and `405` qualified as `400` is; both marks before each exposure, with the workspace case in acceptance; `resumable` after an acknowledgement only when an effect is still `pending`.

**Revision 7, November 26, 2026.** 80,733 bytes, SHA-256 `33cdf329…09405578`. Approved by Muse: G1 to G3 satisfied, no required change. Blocked by Codex: G1 and G2 satisfied; one required change (H1). Open-point positions unchanged.

| Point | Required | Where answered |
|---|---|---|
| H1 | Clearing must have containment confirmed after an unclean engine exit; the lock alone shows nothing | Section 6: instances, with `mcp_instance_started` and `mcp_instance_ended` and the unresolved instance; section 8: clearing, second bullet |

Optional notes taken from Muse: the status-alone rule applies only when the body is neither a discovery result nor a current-revision error; a directory already absent counts as deleted; the engine holds its lock for its whole life.

**Revision 8 terminal votes.** Codex: APPROVE (H1 satisfied; "Required changes: none"; "Prior required changes remain satisfied"). Muse: APPROVE (H1 satisfied; "Required changes: 1. None."). Both support the author's position on open points 1 to 6. Operator, November 26, 2026, on the voted text and the points put to him with it: "v9 looks goot to me." On open point 1 the same day: "for now we'll use syndic.chat, i just bought the domain"; the client ID metadata document is therefore to be served under `https://syndic.chat/`, its path fixed when the login tasks are written, and open point 1 is settled for now on those terms. The other open points of section 14 stay as stated and block nothing. Voted on the text of revision 8 at 83,215 bytes, SHA-256 `5f10fa682e092d2078d05d4d1a8d125a6983d993d9f990a132760405db355dfb`; this paragraph is the only difference between that text and this one. The record is `review/2026-11-26-v9-delta-rev8-review.md`.

## 14. Open points

1. **The client ID metadata document.** That route needs a document at a public HTTPS address that the project controls, and syndic has none yet. Until one exists the client uses dynamic registration or a hand-registered `client_id`. Deferred by the operator; both reviewers support the deferral.
2. **Effect results as history.** D8 puts a server's text into every later turn that includes the staging turn. It is fenced and bounded, and `context_from_turn` removes it from view.
3. **Classes are the operator's word.** Nothing checks that a tool marked `read` only reads. The referee sees the call and the reviewers see the result, but a `read` tool that acts has acted by then.
4. **Resources and prompts** are excluded. Some servers expose most of their value as resources.
5. **Syndic as an MCP server** (D1) is a separate delta: other agents calling syndic for a reviewed answer.
6. **Narrowed in revision 4.** A server given the workspace offers `read` tools only, and a tool whose schema asks for headers from arguments is not offered. Each can be lifted by a later delta.
