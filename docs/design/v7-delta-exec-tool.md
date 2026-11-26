# v7 delta: command execution tool (extends v5 section 6)

Revision 4, DRAFT, dated November 22, 2026. Author: Fable; revision 1 in the Claude Code session that registered milestones D, E and v6, later revisions in the claude.ai session that couriers the peer rounds. Revision 1 (16,296 bytes, SHA-256 `38ff0eebea59fce995180f2f1f8eb35f142ea6a52fb3fe07c1d08a69d22d5299`) was blocked by both reviewers. Revision 2 (43,668 bytes, SHA-256 `c5a1fbdda3fb729a877ba3aacd26fd371df5dcc82ac2f319660c6bef2383f81d`) was approved by Muse and blocked by Codex on four points. Revision 3 (49,366 bytes, SHA-256 `f7127eb09de54510a40c325ab583732730b155f1c281c3e98ac740b09132d6b1`) was approved by Muse and blocked by Codex on two. Section 11 records the rounds. Not approved, not hash-bound. This delta extends the v5 delta as ratified (101,329 bytes, SHA-256 `6cb570b01ee5c40744e3f97c986d33095b9c056d205efd332b024b713b005577`) and does not change v6. It overrides only the v5 clauses named in section 0; every other v5 obligation remains.

## 0. Inputs

Operator direction, November 22, 2026: "We need bash tool too, with the same referee setup."

v5 section 6 excludes it: "No shell, no command execution, no MCP: those need per-call authorization or an OS sandbox (v2 section 8), and each is a later delta." This is that delta, for command execution only. MCP stays excluded.

Settled by the operator and not open:

- S1. A shell tool, with the referee arrangement v5 uses for web.
- S2. The command referee may read workspace files before it answers.
- S3. Commands may reach the network through a host allowlist.

Open: the author's recommendations, which both reviewers support (section 11) and the operator may overrule:

- O1. An OS sandbox is required by default, with an opt-out (section 3).
- O2. Reviewers may run commands as well as the author (section 2).
- O3. A command runs in a scratch copy of the workspace, and what it writes there is discarded (section 5).

v5 clauses this delta overrides, each only as stated:

- V1. Section 6, scope: "no command execution". Lifted for the `bash` tool of section 2.
- V2. Section 6, referee, model: "need not support tool calls". The command referee needs them (section 6).
- V3. Section 6, referee, output: a reply carrying tool calls is a deny, and the output bound is 64 tokens. For the command referee a read step is accepted while reads remain, and its bound is its own (section 6). The web referee is unchanged.
- V4. Section 6, author tool loop: a result larger than `tools.max_result_bytes` is an error, not truncated. A `bash` result is truncated instead (section 2).
- V5. Section 6, enabling: tools in a solo preset. `exec` in a solo preset is allowed only under the required sandbox (section 3).

## 1. Why the web referee is not enough by itself

The web referee judges one fact it can see whole: a URL. A command is not like that.

- The referee sees the command string, not what it runs. `make test`, `./run.sh` and `go test ./...` execute code from the workspace, including code the author staged in this turn. A referee that allows `make test` has allowed whatever the Makefile says.
- A command can do everything v5 confines: read protected paths, read outside the workspace, write the live tree past staging, read the live tree past the observation record, and open network connections past the pre-send checks.

So the referee answers a narrower question, "is this command a plausible step toward what the user asked", and the sandbox enforces what the command can touch. Under the default, `sandbox = "required"`, both are in force and neither replaces the other. Under the opt-out the sandbox is absent, and section 3 states what that costs.

## 2. The tool

A fourth tool group, `exec`, with one tool: `bash{command, timeout_s?}`.

**Invocation.** `/bin/bash --noprofile --norc -c <command>`, not a login shell, with `BASH_ENV` and `ENV` unset. Where `/bin/bash` is absent, `/bin/sh -c <command>` with `ENV` unset; the result names the shell used. Where neither exists, `exec` is unoffered for the run and the participants reply says why. The working directory is the root of the caller's scratch directory (section 5). Standard input is `/dev/null`. No terminal is allocated. Standard output and standard error are one pipe, so their bytes are captured in the order they were written. There is no background use: when the shell exits, everything it started is terminated (section 3, containment).

**Enabling.** `tools` may name `exec` for the author; `reviewer_tools` may name `exec` for required reviewers. Both remain default empty. `exec` requires `read` in the same set, because command output is workspace content. `exec` in a solo preset is allowed under `sandbox = "required"`, where nothing a command does reaches the workspace, and is invalid at roster load under `sandbox = "none"`, where a command could write the workspace with no review.

**Probe.** No new step. A participant needs `tool_calls: yes`, as for any tool.

**Bounds.** Design constants, overridable in the roster under `[tools.exec]`:

- `timeout_s`: default 120, maximum 600. It measures the shell from start to exit and does not include the referee, the scratch build or the reset. A call asking for more than the maximum runs with the maximum, and the result and the journal record the effective value. At the bound the command is terminated and the result says `timed_out`.
- `max_output_bytes`: default 61,440. It must be at most `tools.max_result_bytes` less 1,024; a roster that breaks this is refused at load.
- `max_calls`: default 20 for the author per turn and 10 for a reviewer per round. A `bash` call also counts against v5's `tools.max_calls_per_turn` or `tools.max_reviewer_calls`, whichever governs the caller. A call past `max_calls` is answered with a tool error and the attempt continues; v5's own bounds end tool use as v5 says.
- `max_command_bytes`: 4,096. A longer command is refused before anything else.

**Output.** The engine reads the pipe until it closes. It keeps the longest prefix of the bytes whose rendering, below, is at most `max_output_bytes`, and goes on reading and counting, discarding the rest, so that a command never blocks on a full pipe. After the command has been terminated or has exited, the engine reads for at most one second more and then closes the pipe.

- `output` is the kept bytes decoded as UTF-8 with every byte that is not part of a valid sequence replaced by U+FFFD. The cut is made on raw bytes; a sequence the cut splits is replaced like any other invalid byte.
- `output_sha256` is the SHA-256 of the kept raw bytes, which are stored in the content store under it. `output_bytes_kept` is their count. `output_bytes_total` is the number of bytes the engine read from the pipe before closing it, and claims nothing about bytes it did not read. `truncated` is true when the two differ: output was dropped for size.
- `capture_complete` is true only when the engine read the pipe to end of file. It is false whenever the engine closed the pipe itself, at the one-second cutoff, so that output may be missing even when `truncated` is false. Reviewers see it with the rest of the result.
- The result is rendered to the model as `output` unescaped, together with everything else the rendering adds: the fields below, the fence, the random delimiters, the untrusted-content label and the closing framing. All of that together is at most 1,024 bytes; the engine refuses to start if its template could exceed it. The rendered result therefore never exceeds `tools.max_result_bytes`, and V4 applies: an over-long output is truncated and reported, not refused.

**Result.** `{exit_code, signal?, timed_out, truncated, capture_complete, output, output_sha256, output_bytes_kept, output_bytes_total, duration_ms, timeout_s_effective, shell, view}`. When the shell ends by a signal, `signal` is its number and `exit_code` is 128 plus that number. `view` is `{snapshot_version, overlay_sha256, modes_sha256}`, the workspace state the command ran against (section 5). A non-zero exit is a result, not a tool error. A refusal by a check, the referee or the sandbox is a tool error naming the rule, and the calling attempt continues.

## 3. Sandbox

Under `tools.exec.sandbox = "required"`, the default, every command runs under an OS sandbox applied by the engine, and `exec` is offered only when the sandbox enforces every capability below. v2 section 8 requires that such controls pass contract tests before they are enabled; the self-test below is that requirement at run time, and section 9 is it in CI.

**Capability contract.** A backend is a mechanism on a platform at a stated configuration. A backend is supported only if it enforces all of F, P and D, and N1; N2 is needed only for allowlisted network (section 4). The engine never runs a command under `required` with a weaker set, and never falls back from one configuration to a weaker one without leaving `exec` unoffered. Where a mechanism's abilities depend on a kernel or library version, the backend states the minimum it needs and is unsupported below it.

- F, filesystem. Read, write and execute inside the caller's scratch directory and the per-call temporary directory. Read and execute, no write, in the system locations of the closed list below. No access to any other path: not the live workspace, not another participant's scratch directory, not the home directory, not the syndic configuration, state, journal or temporary areas, not any path matching `tools.protected_paths`.
- N1, no network. No outbound or inbound connection of any family, no name resolution, the host's loopback included.
- N2, proxy only. As N1, except connections to the one proxy listener made for this call (section 4).
- P, process. The command and every descendant, including one that starts a new session or process group or detaches, are contained: the engine can terminate all of them, and none survives the call. A contained process cannot signal, trace or inspect any process outside its containment, the engine included. No new privileges. If the engine dies, a supervisor that holds the containment terminates everything in it.
- D, descriptors. The command starts with standard input on `/dev/null`, standard output and error on the one pipe, and no other descriptor of the engine.

**System locations, a closed list.** The directories in `tools.exec.path`, and the paths in `tools.exec.read_only_paths`. Nothing else outside the scratch and temporary directories is readable.

- `tools.exec.path`: default `/usr/local/bin:/usr/bin:/bin`. `PATH` in the command's environment is exactly this value.
- `tools.exec.read_only_paths`: default on Linux `/usr`, `/lib`, `/lib64`, `/etc/ssl`, `/etc/pki`, `/etc/alternatives`; default on macOS `/usr`, `/System/Library`, `/Library/Developer/CommandLineTools`, `/private/etc/ssl`. The shared libraries a toolchain loads and the certificate bundle it trusts are inside these. The defaults are initial values that the milestone's contract tests confirm per platform; the operator replaces the list for other toolchains.
- Device nodes: `/dev/null`, `/dev/zero`, `/dev/random`, `/dev/urandom`.
- A path in either setting is refused at roster load if it is, contains, or is contained in the workspace root, the home directory, a syndic configuration, state, journal or temporary area, or if it matches a protected pattern.

**Environment.** A fixed set: `PATH` as above, `HOME` pointing at the per-call temporary directory, `LANG=C.UTF-8`, `TERM=dumb`, the proxy variables of section 4 when they apply, and the names listed in `tools.exec.env_passthrough` with the engine's values. No other variable of the engine's environment passes, so no credential does by default. A passthrough name that is one of the fixed names, a proxy variable, `BASH_ENV` or `ENV` is refused at roster load.

**Manifest.** The run manifest records the backend and its configuration, the policy as one object (the path value, the read-only paths, the device nodes, the environment names including every `env_passthrough` name, the capability set, the network list), and the policy's hash: SHA-256 of that object's canonical JSON encoding, keys sorted, no insignificant whitespace, UTF-8.

**Backends.** Linux: user, mount, PID and network namespaces. Linux without usable namespaces: Landlock with seccomp, only at a version and filter that together enforce F, N1, P and D, and N2 where needed; otherwise unsupported. macOS: a `sandbox-exec` profile generated per call, with a supervisor for P. These names are candidates, not guarantees: a backend is offered on the strength of the self-test and the contract tests, not of this paragraph, and a platform on which none passes has no `exec`.

**Containment and termination.** Termination is one sequence for every cause (shell exit, timeout, cancellation, any terminal turn transition, engine stop): the engine signals everything contained with SIGTERM, waits `tools.exec.kill_grace_s` (default 5), signals what remains with SIGKILL, and then waits at most `tools.exec.cleanup_deadline_s` (default 10) for the backend to confirm that nothing contained remains. On normal shell exit the first wait is skipped. A result is delivered only after that confirmation.

**Containment unverified.** If the confirmation does not come within the cleanup deadline, the engine does not wait further and does not pretend. The call's proxy listener and credential were already revoked when termination began. Whatever caused the termination, during a turn or at recovery:

- The engine journals `exec_containment_unverified` with the call, and delivers no result for it.
- The call's scratch and temporary directories stay in place, neither removed nor reused. They are exempt from the ordinary cleanup at the end of the call and of the turn until the containment is confirmed empty.
- No command starts in that run from then on. The engine holds one execution gate for the run. It closes the gate at this moment, and it checks the gate immediately before starting any command's process, with the check and the closing mutually exclusive, so that a command either had started before the gate closed or does not start. This covers a call already accepted, wherever it stands in section 6's order, including one waiting on its referee. `exec` is no longer offered to any participant, and the participants reply says why.
- The supervisor goes on trying to finish the cleanup.

What a caller is told depends on the turn. While the turn is open and the caller's epoch is current, the caller of the unverified call, and the caller of any call the gate refused, receives a tool error naming the outcome, in its next attempt, under the ordinary admission and reservation checks. After a cancellation, after any transition of the turn to a terminal state, and at recovery, the call is journaled `abandoned` beside the containment failure, and no continuation is dispatched to any caller.

**Self-test.** At engine start, and again before the first `exec` call of each turn, the engine runs these under the same backend and policy a command would get. Each uses only the shell and a helper the engine places in the per-call temporary directory.

1. `true` exits 0.
2. A read of a file the engine created in its state directory fails.
3. A write of a new file in a directory the engine created outside the scratch, temporary and system locations fails, and the file does not exist afterwards.
4. A TCP connection to a loopback listener the engine opened for the test fails, and the listener sees no connection.
5. A helper that starts a new session, detaches and sleeps is started; after the call ends no such process exists.
6. With a non-empty network list: a request through this call's proxy listener for the reserved name `selftest.syndic.invalid`, which the proxy answers itself, succeeds; a connection to a second listener made for another call fails.

If any behaves wrongly, `exec` is not offered, for the run when it fails at start and for the turn when it fails later, and the participants reply says which test failed.

**The opt-out.** `tools.exec.sandbox = "none"` on the roster. This is an explicit departure from v2 section 8, which holds that filesystem and network capabilities require OS enforcement. Under it:

- A command runs as the engine's user, in the scratch directory, with the fixed environment, the bounds of section 2, the referee, and termination of the shell's process group. Nothing else is enforced. Filesystem and network confinement are unenforced.
- A command can therefore read and write the live workspace and any other file the engine's user can, including protected paths and syndic's own state; can use the network without restriction; and can leave processes running after the call. Capabilities F, N1, N2 and P do not hold, and the guarantees of sections 4 and 5 that rest on them (the workspace unchanged, writes discarded, the proxy as the only route) do not hold either.
- `tools.exec.network_allow` must be empty; a non-empty list is refused at roster load, because without a sandbox the list restricts nothing.
- `exec` in a solo preset is invalid at roster load (section 2).
- The roster reference states these consequences in these words. The participants reply, the engine's start log and the run manifest each carry the line `exec: sandbox none; filesystem and network confinement are unenforced`, and the participants tab shows it beside every participant that holds `exec`.

The default is `"required"`.

## 4. Allowlisted network

Under the required sandbox a command has no network (N1). `tools.exec.network_allow`, a list of host patterns on the roster or per preset, default empty, makes one exception, and only on a backend that enforces N2. On a backend that does not, a preset under a non-empty list is refused at roster load, naming the backend.

- **Per-call listener.** For each command the engine opens one proxy listener that belongs to that call, with a random credential. The sandbox permits the command to connect to that listener and to nothing else: on Linux the listener is a Unix socket placed in the command's namespace, with a forwarder on the namespace's own loopback, which is not the host's; on macOS the profile permits outbound connections to the one loopback port of that listener. `HTTP_PROXY`, `HTTPS_PROXY` and their lower-case forms carry the listener's address and the credential. The variables are a convenience for tools; the enforcement is the sandbox, and a tool that ignores the variables has no network because of N2, not because of the variables.
- **Identity.** A request without the credential is refused 407. The proxy knows the call from the listener, and before it opens any upstream connection it checks that the call is executing, that the turn is open and that the attempt's epoch is current; otherwise it refuses.
- **Revocation.** When the command ends or is abandoned, at the moment termination begins and before any wait, the listener is closed, every tunnel it carried is closed, and the credential is dead. No upstream connection is opened for a call after that moment.
- **Matching.** The proxy accepts `CONNECT host:port` and plain HTTP requests. The host must match a pattern, by v5 section 6's allowlist matching without resolution (`*.example.com` does not match the apex, `**.example.com` does; an IP literal never matches). Ports are 443 and 80 unless the pattern names another. Anything else is refused 403 and journaled.
- **Address.** For an allowed host the proxy resolves the name, applies v5's address rule (no loopback, private, link-local, multicast, unspecified or IPv4-mapped form of those), and connects to the address that passed.
- **Journal.** Each connection is journaled before it is opened, with the call, the host, the port and, at close, the bytes each way.
- **What this does not do.** The proxy inspects no request body, in a tunnel or in plain HTTP, and does not open TLS. The referee is asked about the command, not about each connection. v5's canary and shape checks do not apply to proxy traffic. A listed host that accepts uploads is therefore a route by which a command can send workspace content out, and the operator who lists a host accepts that for that host. The roster reference says so, and the participants reply names every participant with `exec` under a non-empty list as exposed, with the list.
- `tools.allow_web_with_read = false` refuses, at roster load, a preset that grants `exec` under a non-empty list, since `exec` always comes with `read`.
- The list and the proxy's use are in the run manifest.

Under the required sandbox there is no unrestricted network for commands.

## 5. What a command sees

A command must see the workspace state the other tools of the turn see, or its evidence means little: a reviewer who runs the tests must run them against the candidate.

**Scratch directory.** Each participant that calls `bash` has its own scratch directory for the turn, under the engine's temporary area, reachable by no other participant's commands. It is built before the participant's first command and rebuilt before each later one, so that nothing an earlier command wrote carries over.

**What is in it.** Every regular file and directory under the workspace root that is not protected and not excluded by `tools.exec.exclude` (default `.git/`), taken through v5 section 6's observation mechanism: each directory listed and each file read is an observation, recorded once, under the turn's one lock, and served from the record afterwards. The staging overlay is laid over it as v5 defines the overlay: a staged creation or edit appears with its staged content, and after `unstage` the recorded base shows again. v5 stages no deletion, so the overlay only adds or replaces files. Rules:

- Symbolic links, FIFOs, sockets and device files are not materialised. A directory entry of such a type is simply missing from the scratch directory.
- A path matching a protected pattern is not materialised, whether it comes from the record or from the overlay.
- Modes. A directory is `0755`. A file is `0755` if it was executable for its owner when its content was first observed, else `0644`; setuid, setgid and sticky bits are never carried. That one bit is recorded with the observation in a mode record kept beside the snapshot, taken at the same moment as the content and never read again from the live tree. A file the turn created by `write_file` is `0644`; a staged edit keeps the recorded bit. A staged creation has an explicit entry with the bit clear.
- Every file's modification time is the turn's submit time.
- Nothing else about a file is carried: no owner, no extended attribute, no hard-link identity.

**Bound.** `tools.exec.max_workspace_bytes` (default 1 GiB) and `max_workspace_files` (default 100,000) are checked while the directory is built, on the first call and on every rebuild. When a bound is passed the build stops, the partial directory is removed without anything in it being executed, and the call is refused with a tool error. Observations made before the bound was passed stay in the record: they are true, and v5's record never forgets.

**Temporary directory.** Each call gets a fresh, empty directory under the engine's temporary area. The sandbox allows that directory and not its parent. It is removed when the call ends.

**Writes are discarded (O3).** Under the required sandbox nothing a command writes reaches the staging map, the change set or the workspace. A formatter's or generator's output is seen in the command's output or not at all; work that needs a generated file and its test must do both in one command. Harvesting written files into staging is a later delta.

**Snapshot and freeze.** The observations a build makes extend the record and bump `snapshot_version`, as any observation does.

- An author's first `bash` call puts the copied tree into the record, and the candidate's `snapshot_hash`, computed at freeze, binds it.
- A candidate's `snapshot_hash` never changes after freeze. A reviewer's scratch directory is built from the record and the frozen candidate's overlay. Where that build observes a path the record did not yet hold, v5's rule for a post-freeze observation applies as written: the record gains a version, the observation is bound to that reviewer's verdict, and it changes nothing about the frozen candidate.
- v5 keeps one record for the turn, not one per participant, and this delta does not change that. So another reviewer's later build is served the same recorded content, and a later round's author sees the extended record and freezes the version its new candidate rested on, exactly as for a reviewer's `read_file` today.
- What does not cross between participants: scratch directories, anything a command wrote, and results (section 7).
- An author's `bash` call in a revision round belongs to the new candidate of that round.

**What `view` records.** Three values, taken when the build completes and held unchanged until the command has ended:

- `snapshot_version`: the record's version at that moment. It identifies the observations; the overlay hash does not.
- `overlay_sha256`: SHA-256 over the staging map's change entries, `{path, op, base_sha256, new_sha256, new_len}`, written as the canonical lines v5 section 6 uses for the change set and in its order, and without the `snapshot:` line that v5 appends for `changes_hash`. With nothing staged it is the SHA-256 of the empty string. For a reviewer it is computed over the frozen candidate's change set, so it stays the same while that reviewer's build extends the record.
- `modes_sha256`: SHA-256 over one entry for every regular file in the built directory, staged creations included, sorted bytewise by path. Each entry is the bit as one byte, `x` when set and `-` when clear, one space, the path's length in bytes in decimal, a line feed, the path's bytes, and a line feed.

**What the engine fixes and what it does not.** For one execution the engine fixes the files, their one mode bit, their times, the environment and the command. It does not fix the toolchain's version, the clock, randomness, the machine's load, or what an allowed host returns. A result is evidence of one run, not a proof that the run can be repeated.

## 6. Referee

The role, the endpoint rule (the caller's own endpoint, or the local endpoint that `referee_endpoint` names), the model configuration, the fail-closed rule for the call, the acceptance checks and the accounting of v5 section 6 "Referee" apply. What differs for commands:

**When required.** A roster granting `exec` to a participant whose endpoint names no `referee_model` is refused at load, naming the endpoint, as for `web`. As in v5, it is the participant's own endpoint table that names the model, whether or not `referee_endpoint` moves the referee to a local endpoint. The referee model used for commands needs a probe record with `tool_calls: yes` (V2); without it `exec` is unoffered on that endpoint, and `web` is unaffected.

**A decision is a loop.** One command decision is a sequence of referee attempts. Each attempt is a model dispatch: admitted over its complete rendered request, reserved, charged, reconciled and quarantined on lateness exactly as v4 section 4a and v5 require, and a reservation that fails because the turn budget is exhausted ends the turn `budget:turn`, as v5 has it. Reads between attempts are tool executions by the engine, not dispatches, and are charged nothing.

- The first request carries the payload below and the definitions of `read_file`, `list_dir` and `search_files`, and nothing else.
- A result that finishes with the dialect's tool-call status and carries calls is a read step (V3). It passes the identity, epoch and completion checks before any read runs. Each call in it is then checked in order: the tool is one of the three, the arguments validate, and reads remain under `tools.referee.max_reads` (default 6, counted per decision, that is, per `bash` call). A call that fails a check is answered with a tool error and the others still run. The results are appended, fenced and labelled as untrusted content, and the next attempt is sent.
- When no reads remain, the next request carries no tool definitions and the statement that the referee must now answer.
- A result that carries no calls is the answer: `allow` or `deny` with one line of reason.
- Every other outcome is a deny: a timeout, an adapter error, a provider-side refusal, a malformed reply, a reply cut off at the bound, a result carrying calls under any finish status other than the tool-call status, and a result carrying calls in reply to a request that offered no tools.
- The output bound of each attempt is `tools.referee.exec_max_output_tokens`, default 256, and the reservation uses it. Each attempt has the attempt deadline; the decision as a whole has the turn deadline.

**Payload.** The first request's complete payload is: the user's input for the turn; the tool name and the command string; the caller's own earlier `bash` calls this turn, as command strings only. The engine places nothing else in it: no command output, no candidate text, no file content, no other tool's arguments or results, no other participant's calls. Later requests of the same decision add the referee's own read calls and their results, and nothing else. File content therefore enters a decision only through the referee's own reads.

**Reads (S2).** A referee read is served from the turn's record and the caller's overlay as they stand when the decision begins, which is after the caller's scratch directory has been built. It takes no observation from the live tree, bumps no `snapshot_version` and changes no candidate hash; a path the record does not hold reads `not found`. It is under the same confinement and protected-path rules and the same size bound, `tools.max_result_bytes`, as a participant's read. The referee runs on the caller's endpoint or a local one, and the caller already receives file content, so the reads add no egress route. What the referee reads is data: the template says so, and the sandbox, not the referee, is what holds if a file's text persuades it. Each read is journaled as `referee_read{decision_id, attempt_id, name, args_sha256, sha256, byte_len}`.

**Template.** A second fixed engine template, for commands. Its hash is in the run manifest beside the web template's. The acceptance test asserts the first request byte for byte.

**Order for one `bash` call.** The deterministic checks (command length, call bounds, `exec` offered); then the scratch build with its bound; then the referee decision; then, after v5's re-check that the turn is open and the epoch current and after the run's execution gate (section 3) is found open, execution. The command is journaled with its referee decision before it starts.

**Deny.** A tool error to the caller; the attempt continues.

## 7. Egress, taint and evidence

- Command output is file-derived content. `exec` requires `read`, so a turn that ran with `exec` is file-tainted by v5's rule, and v5's rule on file-derived egress applies to it.
- An author's `bash` results go to every reviewer verbatim as evidence, with the other author tool results, and are bound by the candidate hash. A reviewer's results go to that reviewer only, except a result its block cites, which goes to the author's revision request, as v5 has it for lookups.
- A command's output enters the turn's canary set in both forms a model can come to hold: the kept raw bytes, and the bytes of the rendered `output`, which differ wherever U+FFFD replaced something. They are added when the accepted command's result is recorded, before the engine checks any later web call of any role, a later call in the same tool step included; the engine does not wait for the result to reach a model. They are matched by the same rule as bytes read: a run of at least 16 bytes. With `web` and `exec` both granted, `tools.allow_web_with_read` governs, command output being file content for that rule.
- The canary covers `web_fetch` and `web_search`. It does not cover a command's own traffic through the proxy (section 4).

## 8. Lifecycle

**What governs a running command.** A `bash` call runs after its calling attempt has completed, in v5's `executing` state, so that attempt's deadline no longer applies. What applies is: the command's own `timeout_s`; the turn deadline; and, for each referee attempt of its decision, the attempt deadline, whose expiry is a deny and not a failure of the caller.

**Termination by the turn.** Cancellation, the turn deadline and every other transition of the turn to a terminal state terminate the command by section 3's sequence. The call is journaled `abandoned` with the cause, no result is delivered, and the calling attempt ends as that transition dictates under v5's re-check rule.

**Budget.** A running command is local execution, not a dispatch, and costs no tokens. Budget exhaustion alone does not terminate it: v4 lets work already under way finish. The command finishes and its result is journaled. The result is delivered only if the caller's next attempt can be reserved; if that reservation fails the turn ends `budget:turn` as v4 requires, and the result stays in the journal undelivered.

**Engine stop and recovery.** If the engine stops, the supervisor terminates everything contained (section 3, P). At recovery a command that was running is journaled `abandoned` and never rerun. Before the engine removes or reuses a scratch directory left by an earlier run, it establishes that nothing of that run's containment is still running, terminating what it finds under section 3's sequence and cleanup deadline. If it cannot establish that, the outcome is section 3's "containment unverified": the directory stays, `exec_containment_unverified` is journaled, and `exec` is not offered for the run.

**Cleanup.** A scratch directory is removed when its turn ends. A temporary directory is removed when its call ends. Directories held under section 3's "containment unverified" are the exception and stay until the containment is confirmed empty.

**Other.** A late or quarantined tool step executes nothing, as v5 requires. Admission meters the rendered tool request and the rendered result under v5's rules for tool requests. Wall time is not charged; `duration_ms` is journaled. The TUI shows the command, the referee decision and the exit status under the call, as it does for a fetch.

## 9. Acceptance

Against the fake adapter and a fake sandbox backend where the test is about the engine's logic, and against each supported real backend, in CI on that platform, where the test is about enforcement. No test contacts a real model endpoint or a real remote host.

Configuration: a participant without `tool_calls: yes` is refused `exec`; `exec` without `read` is invalid at load; a roster granting `exec` on an endpoint with no `referee_model` is refused at load; a referee model without `tool_calls: yes` leaves `exec` unoffered on that endpoint and `web` offered; `max_output_bytes` above `tools.max_result_bytes` less 1,024 is refused at load; a `read_only_paths` entry that is, contains or lies inside the workspace, the home directory or a syndic area is refused at load; with neither shell present `exec` is unoffered.

Tool: the shell is started with no profile and no rc file and with `BASH_ENV` and `ENV` unset; a command reading standard input reads end of file; a command sees no terminal; a command over 4,096 bytes is refused before the scratch build; with v5's per-role bound raised above it, the author's twenty-first and a reviewer's eleventh `bash` call are each refused with a tool error and the attempt continues; a call asking for 900 seconds records an effective timeout of 600; a command over its timeout is terminated with its descendants and reports `timed_out`; a command killed by signal 9 reports `signal: 9` and `exit_code: 137`; a non-zero exit is a result.

Output: bytes written to standard output and standard error alternately appear in the order written; output that is not valid UTF-8 is rendered with U+FFFD and `output_sha256` matches the raw kept bytes; output past the bound is truncated with `output_bytes_kept` and `output_bytes_total` both reported; a rendered result at the maximum, with the longest header, fence, delimiters, label and closing framing, is within `tools.max_result_bytes`; a command whose descendant holds the pipe open past the one-second cutoff yields `capture_complete: false` with `truncated: false`, and a command that closes the pipe yields `capture_complete: true`; a cut that splits a multi-byte sequence yields U+FFFD; a command that writes a gigabyte does not block and is not buffered whole.

Sandbox, on each real backend: a command reading a file outside the scratch, temporary and system locations fails; a command reading a protected path finds no such file; a command writing a file outside the scratch and temporary directories fails and the file does not exist; a command cannot read another participant's scratch directory; a command cannot signal the engine; a command inherits no descriptor beyond the three; a command opening a network connection of any family, loopback included, fails when the list is empty; a command sees no variable of the engine's environment other than the fixed set and the passthrough names; a helper that starts a new session and detaches does not survive the call, on shell exit, on timeout and on cancellation; killing the engine during a command leaves no contained process; with a backend that never confirms its containment is empty, the engine stops waiting at the cleanup deadline, journals `exec_containment_unverified`, delivers no result, leaves the directories in place past the end of the call and of the turn, has already revoked the proxy listener, and offers `exec` to no participant for the rest of the run; in that case, with the turn open, the caller's next attempt carries a tool error naming the outcome, and with the turn cancelled or otherwise terminal the call is journaled `abandoned` and no attempt is dispatched; while one call's containment becomes unverified and a second participant's call is waiting on its referee, the second call does not execute when its allow arrives and its caller receives the tool error; a command whose start races the gate's closing either started before it or never starts; each of the six self-tests, made to fail in turn, leaves `exec` unoffered with that test named; a backend configured below its stated minimum is unsupported and `exec` is unoffered, with no weaker set used.

Manifest: the manifest's policy object lists the path value, the read-only paths, the device nodes, every environment name and the network list, and the policy hash equals the SHA-256 of its canonical JSON.

Opt-out: with `sandbox = "none"` the participants reply, the start log and the manifest carry the stated line; a non-empty `network_allow` is refused at load; `exec` in a solo preset is refused at load; and the tests of the "Sandbox", "Network" and "Scratch" paragraphs here are not asserted, because the guarantees they test do not hold.

Network, under the required sandbox with a non-empty list: a request through the call's listener to a listed host succeeds against a stub and is journaled before it opens; a request to an unlisted host is refused 403 with no name resolution; a request without the credential is refused 407; a direct connection past the proxy fails; a command cannot reach another call's listener; an IP literal is refused; a listed name resolving to a private address is refused; `*.example.com` does not match the apex; after the command ends its open tunnel is closed and a new request on its listener opens no upstream connection; a connection requested as the turn is cancelled opens no upstream connection once the call is abandoned; the participants reply names the exposed participants and the list; `allow_web_with_read = false` refuses the preset at load; on a backend without N2 the preset is refused at load.

Scratch, under the required sandbox: a command sees a staged edit and a staged creation, and after `unstage` sees the base again; a symbolic link, a FIFO and a protected file in the workspace are absent from the scratch directory; a file executable in the workspace is executable in the scratch directory and a setuid file is not setuid; a file made executable in the live tree after its first observation is not executable in a later build; a file a command writes is absent from the next call, from the change set and from the workspace; the workspace is byte-identical after any command; a second command sees the recorded content of a file changed live after the first; a workspace over the size bound refuses the call, removes the partial directory, runs nothing, and leaves the observations already made in the record; the temporary directory is empty at the start of each call and gone after it; the scratch directory is gone after the turn; `view` in each result equals the version, overlay hash and mode hash taken at build completion; `modes_sha256` matches the stated byte framing for a directory with one executable file, one plain file and one staged creation; a passthrough name colliding with a fixed name is refused at load.

Freeze: a reviewer's first `bash` call after freeze leaves the candidate's `snapshot_hash` unchanged; where its build extended the record, its `view` shows a higher `snapshot_version` and the same `overlay_sha256` as before the extension; it records its new observations on that reviewer's verdict, and a file the reviewer's command wrote appears in no other participant's scratch directory and in no request.

Referee: the first referee request matches the command template byte for byte and contains no output and no file content; a read step is accepted, its reads are served from the record and overlay, and the next request contains those results and nothing else new; a referee read of a path not in the record returns `not found` and creates no observation, and `snapshot_version` is unchanged by any referee read; the seventh read in one decision is answered with a tool error; the request after the bound carries no tool definitions, and calls in its reply are a deny; a referee read of a protected path is refused; each referee attempt is reserved over its rendered request and a reservation failure ends the turn `budget:turn`; a referee deny, timeout, adapter error, malformed reply, cut-off reply, and calls under a finish status other than the tool-call status each yield a tool error and run nothing; a late referee allow runs nothing; each read is journaled under its decision.

Lifecycle: a cancelled turn terminates the running command and journals it `abandoned` with no result delivered; the turn deadline does the same; the calling attempt's own deadline passing during execution does not terminate the command; a referee attempt's deadline passing yields a deny; a command running when the budget is exhausted finishes and is journaled, and the caller's next reservation failure ends the turn `budget:turn`; recovery reruns nothing; recovery that finds a surviving contained process terminates it before removing the scratch directory, and one that cannot establish this within the cleanup deadline leaves the directory, journals `exec_containment_unverified`, dispatches no continuation to any caller and does not offer `exec`.

Evidence: an author's result reaches every reviewer verbatim and changing it changes the candidate hash; a reviewer's result reaches no other participant unless cited; a turn with `exec` is file-tainted; a `web_fetch` whose URL contains 16 bytes of an earlier command's output is refused by the canary; in one tool step carrying a `bash` call and then a `web_fetch` whose URL contains 16 bytes of that command's output, the fetch is refused; for output that is not valid UTF-8, a URL carrying 16 of the raw bytes and a URL carrying 16 bytes of the rendered text with its U+FFFD are each refused; the same bytes sent by a command through the proxy are not inspected.

## 10. Milestone

One milestone, F, after E: the `exec` configuration and load rules; the sandbox backends on Linux and macOS with the capability contract, containment, supervisor and self-test; the per-call allowlist proxy; the scratch build through observations, with overlay, mode record, bound and rebuild; the runner with bounds and output capture; the command referee template, its read loop and dispatch; taint, egress and evidence wiring; lifecycle and recovery; protocol and TUI display; acceptance, including the per-platform enforcement tests. Roughly twenty to twenty-six tasks.

## 11. Disposition of review

**Revision 1** was reviewed on November 22, 2026. Codex: **BLOCK**, eight required changes (C1–C8), having verified both hashes and inspected commit `6734e55`. Muse: **BLOCK**, seven required changes (M1–M7). Neither objected to S1, S2 or S3. Both support O1, O2 and O3. The record is `review/2026-11-22-v7-delta-rev1-review.md`.

| Finding | Required | Disposition in revision 2 |
|---|---|---|
| C1, M4 | The referee's read loop: accepted read steps, each attempt admitted and reserved, reads as local executions, bound per decision, no-file-content limited to the first request, answer format | Section 6, "A decision is a loop", payload and reads; overrides V2 and V3 named in section 0 |
| M4, in part | Referee on the caller's endpoint only; delete "or a local one" | Not taken. v5 section 6 allows the referee on the caller's endpoint or on the local endpoint `referee_endpoint` names, and this delta does not narrow v5. Section 6 now states the rule in v5's words and the load rule in terms of `referee_model` |
| M4, in part | Referee reads create no observation and move no hash | Section 6, reads: served from the record after the caller's build; `not found` otherwise |
| C2 | Containment of every descendant; bounded termination; engine death; recovery before scratch reuse | Section 3, capability P and "Containment and termination"; section 8, recovery; acceptance |
| C3, M1 | Capability contract per backend; closed lists; descriptors; other scratch; prohibited read-only grants; wider self-test; no silent weakening | Section 3: capability contract, system locations, environment, manifest, backends, self-test |
| M1 | Exact `PATH`, closed toolchain list, CA bundle, canonical policy hash, passthrough names in the manifest, shell without profile or rc | Section 3, system locations and manifest; section 2, invocation |
| C4, M2 | Per-call proxy binding, validation, revocation; how each backend permits only the proxy | Section 4: per-call listener, identity, revocation; capability N2 |
| M2 | Self-test before each turn's first call, with exact commands; `none` forbids a network list and states its consequences | Section 3, self-test (at start and per turn) and "The opt-out" |
| C5, M3 | Scratch view: file types, modes, protected and staged-absent paths, temporary directory, cleanup, bound and partial build, `view` identity, no claim of repeatability | Section 5 throughout; section 2, `view` |
| M3 and C5 on modes | Muse: preserve the executable bit. Codex: take no further metadata from the live tree on rebuild | Both: the one bit is recorded at first observation in a mode record and never read again (section 5) |
| M3 | Size check among the deterministic checks, before the referee | Section 6, order: the scratch build and its bound precede the referee |
| M3, in part | A staged deletion removes the file from scratch | Not applicable: v5 stages no deletion. Section 5 says so and covers `unstage` |
| M5 | Freeze: which record a reviewer extends, whether the frozen hash moves, no leak to the author | Section 5, "Snapshot and freeze". Stated by v5's own rule. A separate record per reviewer is not taken: v5 keeps one record for the turn, and this delta does not override it. What does not cross is listed |
| C6 | The opt-out's exceptions; scope guarantees and tests to enforced mode; departure from v2 section 8 | Section 1; section 3, "The opt-out"; section 9, opt-out paragraph. Solo with `exec` is refused under `none` (V5) |
| C7 | Output bytes: encoding, hash, counts, draining, ordering, and v5's oversized-result rule | Section 2, bounds and output; override V4 |
| C8 | Which deadline governs execution; budget exhaustion against `budget:turn` | Section 8 |
| M6 | Canary on command output; none on proxy traffic | Section 7; section 4 |
| M7 | Timeout meaning, stdin, no terminal, signal exit code, effective timeout, no shell, the call-bound fixture | Section 2; section 9 |

**Revision 2** was reviewed on November 22, 2026. Muse: **APPROVE**, finding M1–M7 satisfied, accepting the three points not taken as worded (the referee's endpoint rule, the single observation record, staged deletion) and the mode record. Codex: **BLOCK**, finding C1, C3, C4, C6 and C8 satisfied and C2, C5 and C7 partial, with four required changes; it accepted the same four points. Neither changed its position on O1, O2 or O3. The record is `review/2026-11-22-v7-delta-rev2-review.md`. Disposition in revision 3:

| Finding | Required | Disposition in revision 3 |
|---|---|---|
| D1 (C2) | Bound the wait after SIGKILL; an explicit unverified-containment outcome, live and at recovery | Section 3, `cleanup_deadline_s` and "Containment unverified"; section 4, revocation when termination begins; section 8, recovery; acceptance |
| D2 (C5) | Exact `overlay_sha256` without the snapshot line; exact framing of `modes_sha256`; values taken at build completion | Section 5, "What `view` records" and the modes rule; acceptance |
| D3 (C7) | Distinguish incomplete capture from size truncation; the 1,024 bytes cover all framing | Section 2, `capture_complete` and the rendering bound; acceptance |
| D4 | Canary seeded with raw and rendered bytes when the result is recorded, before a later call in the same step | Section 7; acceptance |

Two optional notes of round 2 are also taken, each one sentence: a passthrough name that collides with a fixed name is refused at load (Codex, section 3), and the load rule says whose endpoint table names the referee model (Muse, section 6). Nothing else differs from revision 2.

**Revision 3** was reviewed on November 22, 2026. Muse: **APPROVE**, finding D1–D4 satisfied. Codex: **BLOCK**, finding D2, D3 and D4 satisfied and D1 partial, with two required changes. The record is `review/2026-11-22-v7-delta-rev3-review.md`. Disposition in revision 4:

| Finding | Required | Disposition in revision 4 |
|---|---|---|
| E1 (D1, C2) | Unverified containment must stop execution run-wide, including calls already accepted; a gate checked immediately before spawn, serialized against its closing | Section 3, "Containment unverified", third item; section 6, order; acceptance |
| E2 (D1, C2) | Deliver the tool error only to an active caller; after cancellation, a terminal transition or recovery, journal and dispatch nothing; retained directories exempt from ordinary cleanup | Section 3, "Containment unverified", second item and closing paragraph; section 8, cleanup and recovery; acceptance |

Nothing else differs from revision 3. Muse's two optional notes of round 3 are not taken, as neither asks for a change.

Optional notes of round 1. Muse's request to reword revision 1's closing sentence about `none` as a default is taken: that sentence is gone, and section 3 states the default. Codex's and Muse's notes on resource limits and on the cost of copying are not taken in this revision; they are in section 12.

**Revision 4 terminal votes.** Codex: APPROVE (E1 and E2 satisfied; C1–C8 and D1–D4 satisfied; "No required changes remain"). Muse: APPROVE (E1 and E2 satisfied; "No further required changes"). Both support O1, O2 and O3. Operator, November 25, 2026: "I accept O1 through O3"; the three are therefore settled as sections 2, 3 and 5 state them, and the two remaining items of section 12 stay open and block nothing. Voted on the text of revision 4 at 52,332 bytes, SHA-256 `4809e7ea5a5cf843af815825052f9dcf564e6c4648a2313065a19fefa8d195cd`; this paragraph is the only difference between that text and this one. The record is `review/2026-11-22-v7-delta-rev4-review.md`.

## 12. Open for the operator

- O1, O2 and O3 in section 0. Both reviewers support all three.
- Limits on what a command may consume beyond time and output (processes, memory, bytes written to scratch) are not specified. Codex raised it as optional. A command can fill the disk or exhaust process slots within its timeout.
- Building and rebuilding the scratch directory copies up to the size bound on every call. Muse raised the cost as optional. Copy-on-write or an incremental rebuild is an implementation choice this text permits, provided the result is the same directory.
