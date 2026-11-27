# v10 delta: endpoint failures, retry, cooldown, suspension and recovery (extends v4 section 4)

Revision 4, DRAFT, dated November 27, 2026. Author: Fable. Revision 1 was written in the Claude Code session that registered milestones F, G and H; revision 2 on is written in the claude.ai session that couriers the peer rounds. Not approved, not hash-bound. Revision 1 (24,377 bytes, SHA-256 `df29ed545a02c7893d8f0b1304db99be958a3b6796f110f937824138054bc3e0`) was blocked by both reviewers. Revision 2 (51,580 bytes, SHA-256 `f402268b4c0cf7e9dcd26eddbdff83b5ef40c14859a5f2e26f4bf7a188f52518`) was approved by Muse and blocked by Codex. Revision 3 (73,399 bytes, SHA-256 `250b80ad9e991c1fa990c3b9e35fb1afb8622c8761c346598ef0ad36b4f8b1a4`) was approved by Muse and blocked by Codex on November 27, 2026; section 13 records the three rounds.

## 0. Inputs

Operator report, November 26, 2026, from a test against real endpoints: one model was rate limited; the engine ignored the rate-limit headers, sent further requests at once, never marked the model unhealthy and never took it out of the review. Operator direction: a rate-limited reviewer should leave the review set and be watched in the background until it is healthy again, and the delta "should cover all expected failure modes (e.g. 500s, etc)".

What the ratified texts say today, and the code does (checked at commit `874e1e9`):

- v4 section 4 maps `TransportStatus` to an outcome. `unreachable` gets one retry after a short delay. `reset`, `timeout` and a Go error that is not `ErrNotSent` open a capacity hold and are never retried. A Go error wrapping `ErrNotSent` ends the attempt with no hold and fails the turn. `unauthorized` and `model_unavailable` end the turn. Every other `http_<code>`, which includes 429 and every 5xx, ends the attempt with status `error` and the turn `incomplete`, with no retry and no hold.
- v4 section 4: "Nothing is cached: the next `submit` dispatches afresh." The three failure states are cleared at the participant's next dispatch.
- v4 section 3: `incomplete` on "any reviewer failed". The v3 addendum section 2: "All required reviewers must approve the exact candidate. No majority vote, timeout quorum, or reduced roster is inferred from N. Future threshold policies require a separate explicit protocol design." v8 section 2 ends a deliberate turn on any failed proposal, critique or integration.
- The adapter's `Complete` path keeps the status code and body of a response and reads no response header. Its probe paths read and record headers. A refused connection and a DNS failure come back as `TransportStatus` `unreachable` with no Go error; a reset and a timeout likewise as `reset` and `timeout`. The adapter follows a redirect to the endpoint's own host and no other. A body larger than 16 MiB is `body_too_large`, whatever the status.
- The engine's `callOnce` discards the adapter's result when the adapter also returns a Go error.
- The code journals a retry only when the retry is dispatched, after its wait, so that a journaled retry is always a dispatched one. Terminal delivery calls the same completion function and is not retried, because the retry guard refuses a closed turn.
- v4 section 4a charges a quarantined result `max(reservation, reported)` and never refunds it; an unknown attempt keeps its full reservation. v5 section 1: no token budget applies to a probe.
- v4 section 3 says the terminal reason is shown in the status strip; the strip as built shows the turn's state. The history package accepts three labels and no other.

So a 429 costs the whole turn, the next turn sends to the same endpoint immediately, and nothing remembers.

Settled by the operator and not open:

- S1. A rate-limit response is honoured: the engine waits what the server asks when that fits, and does not send to a limited endpoint in the meantime.
- S2. A participant that keeps failing is marked unhealthy, persistently, and is not dispatched to.
- S3. An unhealthy reviewer is left out of the review; the engine checks in the background and restores it.
- S4. The delta covers every failure of a model endpoint the engine can expect, not rate limits alone.

Open: the author's recommendations, which the operator may overrule (section 14).

Clauses this delta overrides, each only as stated:

- V1. v4 section 4, the mapping bullets for `unreachable`, for "any other `http_<code>`" and for `body_too_large`. Replaced by the classes of section 1 and the retry rule of section 2. For 502, 504, 408 and a 2xx body over the limit this is stricter than v4: they now open a hold.
- V2. v4 section 4, "Nothing is cached: the next `submit` dispatches afresh", and the clearing of a failure state at the next dispatch. Replaced by the cooldown of section 3 and the health record of section 4.
- V3. v4 section 4, the list of participant states. Section 9 adds states and makes `unreachable`, `unauthorized` and `model_unavailable` persistent.
- V4. v4 section 3, "any reviewer failed" as a cause of `incomplete`, and "every required reviewer has a valid approve". Replaced by section 5 for a preset that sets `min_reviewers`; unchanged for one that does not.
- V5. v4 section 3, terminal reasons. Section 5 adds reasons; none is removed.
- V6. v8 section 2, the failure clauses for proposal, critique and integration attempts. Section 2's retry runs before they apply. The clauses themselves stand (section 6).
- V7. The v3 addendum section 2, "All required reviewers must approve the exact candidate" and "No … reduced roster is inferred". This delta is the separate explicit protocol design that sentence calls for, and it makes one exception, bounded four ways: only a preset that sets `min_reviewers` below its full set permits it; a reviewer is left out only for the operational causes section 5 enumerates, never for anything about the candidate; every reviewer not left out must approve the exact candidate; and no block or revision request is ever withdrawn, overridden or passed over by leaving its author out.
- V8. v4 section 3's terminal path and v4 section 6's start, which hold capacity for every pending attempt. Replaced, for an attempt that is waiting to retry and has no request out, by section 2. Unchanged for an attempt with a request that may be at the server.
- V9. v4 section 2, `protocol: 1`. It becomes 2 (section 9).
- V10. v4 section 3, terminal delivery to every participant that took part. Section 6 leaves out a participant that is suspended or cooling at delivery time.
- V11. v9 section 5, `syndic-history/v2`. It becomes `syndic-history/v3`, which is v2 with one more permitted `label` value (section 5).

Not changed: the capacity hold and every rule about it; the rule that a request which may have been sent and whose outcome is unknown is never retried; the gate, acceptance and the verdict parser; candidate identity; probe eligibility and the manifest; the referee decisions of v5 and v7; v4 section 4a's reconciliation for a quarantined result and an unknown attempt; MCP server failures, which v9 specifies and which are not model endpoints.

## 1. Failure classes

The adapter classifies; the engine acts on the class. Every result of a send to a model endpoint, a completion, a probe step or a health check, that is not transport-ok has exactly one class or, by rule 1 below, none.

| Class | What was observed | Did the server do work | Examples |
|---|---|---|---|
| `local` | The engine could not build or authorize the request; nothing was sent and no endpoint is at fault | No | An invalid message, a secret that would not resolve, a refused `BeforeSend` hook |
| `unreachable` | No request byte reached the server | No | DNS failure, connection refused, TLS failure before any request byte |
| `throttled` | The server answered that it will not take the request now | No | 429 without a quota marker; 503; 529 |
| `quota` | The server answered that the account may not spend | No | 402; a response carrying a quota marker (section 7) |
| `server_fault` | The server answered with its own error | Unknown, and that request has ended | 500; any 5xx not listed elsewhere |
| `uncertain` | No answer, or an answer that does not show the work stopped | Unknown, and it may be continuing | reset; timeout; any other Go error; 502, 504, 408; a 2xx whose body was not read to its end |
| `rejected_config` | The server refused the caller, the model or the route | No | 401; 403; 404; a 3xx the adapter did not follow; 501; 505 |
| `rejected_request` | The server refused this request's content or shape | No | 400, 413, 422, any other 4xx |
| `bad_response` | A 2xx read to its end whose body the adapter could not use | Yes | `malformed_body` |

**Precedence.** The first rule that applies gives the class. "A response was received" means its status line and headers were read.

1. No response was received and the engine itself cancelled the request: no class, and no effect on cooldown or health.
2. No response was received and nothing was sent: `unreachable` when the adapter reports `TransportStatus` `unreachable`, with or without a Go error; `local` for any other Go error wrapping `ErrNotSent`.
3. No response was received otherwise, which covers `reset`, `timeout`, a cancellation the engine did not make, any other Go error, and any failure after the adapter followed a redirect: `uncertain`.
4. A response was received and its body carries a quota marker (section 7): `quota`, whatever its status.
5. A response was received with a status that is not 2xx: classed by the table from the status alone. If its body cannot be read, or is over the limit, the status and the headers of section 7 are kept and used; the body is not needed.
6. A response was received with a 2xx: `uncertain` when the body read ends in a reset or a timeout or passes the body limit before its end, because the adapter stopped reading and has not seen the server finish; `bad_response` when the body was read to its end and cannot be parsed.

Rules 4 to 6 apply to a response that was received even when the engine cancelled the request or ended the turn while it arrived. For the turn such a result is late and is quarantined as v4 has it; its class, its server-stated wait and its effect on cooldown and health (sections 3 and 4) are kept.

The adapter returns the class, the server-stated wait, the no-retry mark and the status in its result in every case, a Go error included, and the engine keeps them; `callOnce` no longer discards a result that comes with an error. Redirects are unchanged: the adapter follows one to the endpoint's own host, and the result is the final response's; a 3xx it does not follow is `rejected_config`.

`TransportStatus` values are unchanged. The class is a second member of the result, `FailureClass`, journaled beside `transport_status` on the dispatch row.

A result that is transport-ok and fails the gate, or completes with a status other than `complete` (truncated, refused), is not a failure of the endpoint and is outside this delta. So is a verdict the parser rejects. Transport-ok means `TransportStatus` is `ok`: a 2xx whose body the adapter read and parsed.

**Intermediaries.** A 504 shows that something in front of the model server gave up waiting; a 408 that something timed the request out; a 502 that something received a bad answer from upstream. None shows that inference stopped. They are `uncertain`: the attempt ends `interrupted`, a capacity hold opens as for a reset, and nothing is retried. A status is not treated as evidence that work stopped unless the server that did the work is the one reporting on itself. `[endpoints.<name>] bad_gateway = "server_fault"` is the operator's own declaration, for one endpoint, that its 502 means the upstream refused or failed before working; the status code is not that evidence, the operator's knowledge of the endpoint is. The default is `"uncertain"`. 504 and 408 have no such setting. This is O3.

## 2. Retry within an attempt

**Which classes.** `unreachable`, `throttled` and `server_fault` are retried. `local`, `quota`, `rejected_config`, `rejected_request`, `uncertain` and `bad_response` are never retried: the first four cannot change within a turn, `uncertain` could duplicate inference (v4, unchanged), and a `bad_response` was paid for. A response carrying `x-should-retry: false` is not retried whatever its class.

**How many.** At most `retry.max` retries per attempt, default 3, counted over all classes together. Of those, at most `retry.max_after_possible_work`, default 1, may follow a `server_fault` result, because each such result may have been charged.

**How long.** The delay before retry *n* (from 1) is the server-stated wait when the result carried one (section 7), and otherwise `retry.base × 2^(n-1)`, default base 1 s, capped at `retry.max_delay`, default 60 s. Jitter, uniform in [0, 25%] of that delay from the engine's injected random source, is then added; jitter only lengthens, and a cap is applied before it. A server-stated wait longer than `retry.max_server_wait`, default 120 s, or one that is unbounded (section 7), is not waited: the attempt ends at once as below. `max_server_wait` limits what an attempt will wait; it never shortens the cooldown of section 3, which is recorded in full.

**States of an attempt.** An attempt that is not finished is in one of two states, and the journal says which:

- `in_flight`: a request may be at the server. This is v4's pending attempt, with v4's rules.
- `waiting`: the last send has a known result of a retried class, no request is out, and the attempt holds no pool slot.

**When a retried result arrives.** One `Tx` records the result on that send's dispatch row with its class, settles that send's reservation (below), records the cooldown if section 3 requires one, updates health (section 4), moves the attempt to `waiting` with the time before which it will not be sent, and emits `attempt_wait{attempt_id, n, class, transport_status, delay_ms, server_wait_ms?}`, `n` being the number of the retry that would follow, from 1. The pool slot is released when it commits. A wait is not a dispatch: it adds no row to `attempt_dispatches` and does not change `retry_count`. If no retry is allowed, by the count, by `NoRetry`, or because the wait is too long or would end at or after the attempt's or the turn's deadline, the attempt does not wait and ends as "the retry cannot fit" below.

**When the wait ends.** The attempt queues for a pool slot like a new dispatch. When it has one, one `Tx`, immediately before the send, checks in this order, and the first that fails decides:

| Check | If it fails |
|---|---|
| The turn is open, the attempt's epoch is current, and nothing has cancelled it | The attempt ends as v4 ends a cancelled or superseded attempt; no hold, since it is `waiting` |
| The attempt's deadline and the turn's have not passed | v4's deadline outcome, `deadline:attempt:<attempt_id>` or `deadline:turn`. This is never an excusal |
| Its member and its endpoint are not suspended (section 4) | The attempt ends with status `error` and its last `transport_status`: a final failure of its own last class |
| The scope's cooldown has passed | If the cooldown now ends before both deadlines, the attempt stays `waiting` until then, emits `attempt_wait` again with the new time, and uses no retry. Otherwise the retry cannot fit |
| A trial it needs is free or is its own (section 4) | The attempt stays queued behind the trial. When the trial ends, the checks run again from the top |
| The pool admits it, holds included | It stays queued, as any dispatch does |
| The send's reservation is granted | v4's budget path, `budget:turn` or `budget:context:<participant>`. Never an excusal |

An attempt that goes back to `waiting`, or stays queued behind a trial, gives up the pool slot it had just taken: only an attempt that is sent holds one.

If every check passes, the same `Tx` makes the reservation, writes the dispatch row as v4 does for a retry with the generations of section 4, increments `retry_count`, and moves the attempt to `in_flight`. So v4's rule stands: a journaled retry is a dispatched one.

**The retry cannot fit.** An attempt whose retries are spent, or whose next send could not happen before a deadline that has not yet passed, ends with status `error` and its last `transport_status`. This is a final failure of the attempt's last class. It is distinct from a deadline that has passed, which is v4's deadline outcome.

**Ending while waiting.** A `waiting` attempt that is cancelled, or whose turn ends, ends with its last `transport_status` and opens no hold, because nothing is at the server. When its member or endpoint becomes suspended it ends at once, as the third check has it, without waiting for its time. At engine start, an attempt found `waiting` is ended the same way, with no hold and no `unknown` mark; its turn ends `engine_restart` as v4 has it. An attempt found `in_flight` is v4's pending attempt, unchanged.

**The queue.** A queued dispatch whose scope is cooling is passed over, and the first queued dispatch that may be sent takes the free slot. A cooling member never blocks another member of its pool.

**Reservations.** Every send of an attempt in a turn, first or retry, has its own reservation of the request's estimate plus `MaxOutputTokens`, made in the `Tx` that writes its dispatch row, as v4 section 4a has it for an attempt. A probe request and a health check belong to no turn and reserve nothing (v5 section 1). Each send's dispatch row carries `reserved_tokens`, `usage_prompt`, `usage_completion`, `settlement` and `settled_at`, and is settled once, in the `Tx` that records its result:

| The send's result | Settlement |
|---|---|
| Accepted, class `local`, `unreachable`, `throttled`, `quota`, `rejected_config` or `rejected_request`, no usage reported | Credited back in full |
| Accepted, any of those classes, usage reported | Charged the reported usage |
| Accepted, `server_fault` or `bad_response` | Charged `max(reservation, reported)`; the reservation when no usage was reported |
| `uncertain`, with usage reported in the result itself | Charged `max(reservation, reported)` |
| `uncertain` with no usage reported, or the attempt is unknown | The reservation stands, as v4 keeps it for an unknown attempt |
| Quarantined, any class | v4 section 4a unchanged: `max(reservation, reported)`, never a refund |

A later result for a send already settled, which is v4's late result, may raise that send's charge to `max(charge, reported)` and never lowers it; applying the same result twice changes nothing. A retry's usage never changes an earlier send's charge. Every charge is made in the `Tx` that records the result, and v4's exhaustion handling applies in that same `Tx`.

A reservation that is refused runs v4's path with `budget:turn` or `budget:context:<participant>`. It is never an excusable failure (section 5), and for a referee it is whatever v5 and v7 make of a refused referee reservation, not the deny of section 6.

## 3. Cooldown

A cooldown is the time before which nothing is sent to a rate-limit scope. It is separate from health: it is about when, not whether.

**Scope.** Providers limit by account, project and model family, not by one model name. Each endpoint has a scope key and a derived identity:

- The derived identity is `origin:<scheme>://<host>[:<port>]|cred:<credential reference name>`, with the host in lower case and a default port left out. It names what the endpoint reaches and with which credential, not the credential's value.
- The scope key is `name:<rate_limit_scope>` when `[endpoints.<name>] rate_limit_scope` is set, and the derived identity otherwise. So two roster endpoints with one origin and one credential share a scope without being told to, and renaming an endpoint does not lose it.
- `rate_limit_per_model = true` makes a cooldown observed on that endpoint apply to the one model that was limited. Endpoints that share a scope key, named or derived, must agree on it; the validator refuses a roster where they do not.

Referee models are on endpoints and are covered like any model.

**Record.** `cooldowns{scope_key, identity, model, not_before, unbounded, cause, observed_at}`: the scope key and derived identity of the endpoint that observed it, at that time; `model` is `*` unless that endpoint is per-model. `not_before` is absolute wall-clock time; `unbounded` marks a cooldown with no end. There is one record for each distinct `(scope_key, identity, model)`: two endpoints with different derived identities that share a named scope and are both limited leave two records, so neither identity is lost.

**Matching.** A send to a model on an endpoint is stopped by every unexpired record whose `model` is `*` or that model and whose `scope_key` equals the endpoint's present scope key or whose `identity` equals the endpoint's present derived identity. The cooldown in force is the latest `not_before` among them, and an unbounded one outlasts all. So a whole-scope record and a model record combine as their maximum, and adding, removing or renaming `rate_limit_scope` never frees an endpoint from a cooldown it earned: the identity still matches. A record that matches no endpoint in the roster has no effect, is kept until it expires and is listed in `engine_started.unmatched_cooldowns`.

**When one is recorded.** In the `Tx` that records any `throttled` result, and any other result that carries a server-stated wait, from a completion, a probe step or a health check, whether or not a retry will follow and whether or not the result is quarantined. `not_before` is the time the result was observed plus the wait section 2 computes for it, jitter included, and is never capped: a wait of two days is recorded as two days. A wait section 7 reads as unbounded records an unbounded cooldown with cause `unrepresentable_wait`. A zero retry allowance, a wait too long to wait, a deadline that leaves no room and a cancelled turn each end the attempt; none of them shortens or drops the provider's cooldown.

**Combining.** A new observation replaces the record with the same scope key, the same identity and the same model, and only if its `not_before` is later; an observation that differs in any of the three is a record of its own. Results that arrive out of order therefore cannot shorten a cooldown, and no observation overwrites another endpoint's identity. Records are combined only when they are read, by the matching rule above.

**What it stops.** Before the cooldown in force has passed, the engine sends nothing to the model on that endpoint: no attempt, retry, repair, tool continuation, referee request, terminal delivery, startup-probe request or health check. The check is in the `Tx` immediately before each send.

**What ends it.** Time, for a bounded one. A transport-ok result from a request sent earlier, a passing health check and an ordinary `resume` do not. Only the operator ends one early or ends an unbounded one:

- `resume{participant, clear_cooldown: true}` deletes every record that stops that participant now, by the matching rule.
- `resume{scope: "<rate_limit_scope name>", clear_cooldown: true}` deletes every record whose scope key is that name.

Each deleted record is journaled as `cooldown_cleared{scope_key, identity, model, by: "operator"}`. It is the operator's statement that the limit is gone.

**Restart.** Records survive and are honoured after a restart. An expired record is deleted when it is next read.

## 4. Health

**Rows.** Health is recorded in `member_health{endpoint_name, model, state, cause, streak, since, check_interval, next_check, generation, trial_owner, last_result}`. There are two kinds of row:

- A member row, for the pair `{endpoint_name, model}`.
- An endpoint row, with `model` `*`, for what concerns the endpoint whatever the model.

A send to a member is governed by both its member row and its endpoint row. It is refused if either is `suspended`. Referee models are members. A row is created when first needed and is never deleted, so its generation is never reused.

**Which row a result touches.**

| Result | Endpoint row | Member row |
|---|---|---|
| `unreachable` | failure | untouched |
| 401 | suspends at once, cause `unauthorized` | untouched |
| `quota` | suspends at once, cause `quota` | untouched |
| `throttled`, `server_fault`, `uncertain`, `bad_response` | success if a response was received, else untouched | failure |
| 403 | success | suspends at once, cause `unauthorized` |
| Any other `rejected_config` | success | suspends at once, cause `model_unavailable` |
| `rejected_request` | success | success |
| transport-ok | success | success |
| `local`, the engine's cancellation, a deadline | untouched | untouched |

For the endpoint row a success is evidence that the endpoint answered; for the member row, that the model served or would have.

**States.** `healthy`; `suspended` with a cause; `trial`.

| From | On | To |
|---|---|---|
| `healthy` | a failure, counted once for an attempt that ended in it after its retries | streak + 1; `suspended` when the streak reaches `suspend.after` (default 3) |
| `healthy` | a success | streak 0 |
| `healthy` | a result that suspends at once | `suspended` |
| `suspended` | a passing check; or, for cause `throttled`, the cooldown in force passing; or, where there is no usable check, `next_check` arriving | `trial` |
| `suspended` | `resume` | `trial` |
| `trial` | the trial attempt's final result is a success for this row | `healthy`, streak 0 |
| `trial` | the trial attempt's final result is a failure for this row, or suspends it at once | `suspended`, `check_interval` doubled up to its maximum |
| `trial` | the trial attempt's final result leaves this row untouched | `trial` still, with no owner; the next dispatch is the trial |
| any, `suspended` included | `suspend` by the operator | `suspended`, cause `operator`; a new generation even if the row was already suspended for another cause |

- The streak counts consecutive attempts that ended, after their retries, in a failure for that row. One exhausted attempt does not suspend; `suspend.after` of them in a row do. An attempt that was ended early, by a suspension or by removal from the queue, without a result of its own, counts for nothing, and so does a deadline outcome. An attempt whose final result arrives late and is quarantined for its turn (section 1) counts once, by that result, like any other, under the generation rule below.
- The endpoint row's streak counts such attempts on any model of the endpoint.
- A suspension with cause `operator` is never checked and ends only by `resume`.
- A 403 is the member's, so a 403 that in fact covers the whole account suspends that endpoint's members one at a time, as each is used, and not the endpoint at once. That converges, and it keeps one refused model from stopping its neighbours.

**Cause and what the client sees.**

| What suspended | Cause | Participant state on the wire |
|---|---|---|
| `unreachable` | `unreachable` | `unreachable` |
| `throttled` | `throttled` | `rate_limited` |
| `server_fault` | `server_error` | `server_error` |
| `uncertain` | `uncertain` | `unknown` |
| `bad_response` | `bad_response` | `bad_response` |
| `quota` | `quota` | `quota` |
| 401 or 403 | `unauthorized` | `unauthorized` |
| any other `rejected_config` | `model_unavailable` | `model_unavailable` |
| operator | `operator` | `suspended` |

A participant shows the state of whichever of its two rows is not healthy, the endpoint row first when both are. One whose rows admit only a trial shows `trial`; a row in `trial` keeps the cause of the suspension it is recovering from. One that is healthy and under a cooldown shows `rate_limited`, with the cooldown's end in `cooling.until` (section 9).

**Generations.** `generation` comes from one counter for the whole journal that only rises. A row takes a new value at every change of its state. Every dispatch row records the generation of its member row and of its endpoint row at the send. A result changes a row's state or streak only if it was sent under that row's present generation. Any other result is written to the row's `last_result{class, transport_status, at}` for the operator to read, and changes nothing else. So a slow success cannot undo a later suspension, a slow failure cannot re-suspend a row that has since been tried and restored, and since an operator's suspension is a change of state, nothing sent before it can replace or end it. Cooldowns are outside this rule: section 3 records them from every result.

**Trials.** A row in `trial` admits one attempt at a time, its owner, across every model and every pool the row covers: an endpoint row's trial is one attempt for the whole endpoint. Ownership is taken in the `Tx` immediately before the send, by writing the attempt's id to `trial_owner`. A dispatch that needs both its rows and finds both in `trial` takes both in that `Tx` or neither. A dispatch that cannot take what it needs stays queued. The owner keeps the trial until its attempt ends, retries included, and each row it owns then judges the final result by the tables above. `trial_owner` is cleared whenever a row leaves `trial`, to `healthy` or to `suspended`, an operator's `suspend` included, and when a trial ends with the row untouched.

**Effect.** No attempt is dispatched to a member while either of its rows is suspended. A dispatch already queued when that happens, and an attempt `waiting`, are ended at once (sections 2 and 5). A request already in flight is not cancelled, and its result is handled by the generation rule. A suspension is not in the manifest and changes no manifest hash; it never makes an `unprobed` participant usable and never stands in for a probe record. A participant whose startup probe failed is `unprobed` as v5 has it, and v5's remedies are the only ones.

**Recovery.** The engine checks a suspended row in the background, turn or no turn, and never through a pool slot:

- The check is one `GET /models` on the endpoint with its credential, with a 10 s timeout and at most 1 MiB of body read. It is not inference; v5 section 1's rule that a metered endpoint is not probed without `--probe-missing` is about inference probes and is not relaxed. It reserves nothing. It obeys the cooldown in force like any send.
- Checks run one at a time for the whole engine. No check starts after `engine_stopping`; a check in flight then is cancelled, and by section 1 its response counts only if it had been received.
- A check's response is read by section 7 like any other: a 429 or a stated wait records a cooldown (section 3) and the check fails. A check never changes a streak.
- The check passes on a 2xx. For cause `model_unavailable` it passes only if the body parses as a model list, within that bound and without a further page, and names the member's model.
- An endpoint whose `/models` answers 404, 405 or 501, or whose list cannot be used to decide a `model_unavailable` check, has no usable check. Its rows move to `trial` when `next_check` arrives, so an endpoint without that route is never suspended for ever.
- `throttled` needs no check: the row moves to `trial` when the cooldown in force has passed. Under an unbounded cooldown that is when the operator clears it.
- `quota` is checked at the maximum interval only, since a passing `/models` says little about spend; its trial is the evidence.
- Checks run at `suspend.check_interval`, default 30 s, doubling after each failure to `suspend.check_max_interval`, default 10 min.

A passing check does not show that inference works, which is why recovery ends in a trial and not in `healthy`.

**Operator.** On the control socket and as `/resume` and `/suspend` in the TUI:

- `resume{participant}` and `resume{member}` move every suspended row that stops that member, its member row and its endpoint row, to `trial` at once, an operator's suspension included. `resume{endpoint}` moves the endpoint row only. The reply lists the rows changed. None clears a cooldown unless asked (section 3).
- `suspend{participant, reason}` and `suspend{member, reason}` suspend the member row; `suspend{endpoint, reason}` the endpoint row.

**Restart.** Rows survive, streaks, generations and trial states included; a `trial_owner` found at start is cleared, since its attempt did not survive. On start the engine resumes checking; `engine_started` lists suspended rows and cooldowns.

**Events.** `member_health{endpoint_name, model, state, cause?, since, streak, next_check?, generation}` on every change; `health_check{endpoint_name, model, ok, status}` per check; `cooldown{scope_key, model, not_before?, unbounded?, cause}` when one is recorded or extended.

## 5. A review with a reviewer missing

A preset gains `min_reviewers`, an integer from 1 to the number of `required_reviewers`. Absent, it equals that number, and this section changes nothing for the preset: any reviewer that cannot review ends the turn, as v4 says. Present and smaller, reviewers may be excused as follows, and only as follows (V7). This section applies to the reviewers of a review-mode turn and to the final reviewers of a deliberate turn.

"Suspended" here means that the reviewer's member row or endpoint row is suspended; "cooling past the deadline" that the cooldown in force for it is unbounded or ends at or after the turn's deadline.

A reviewer's review of a candidate in a round is one logical unit made of several transport attempts: its first attempt, one further attempt after each tool step (v5 section 6), which this section calls a continuation, and at most one repair. Each attempt is retried and ends by section 2 on its own. The review has returned its result when one of its attempts returns a transport-ok result that is not a tool step, which is the result the gate and the verdict parser see.

**Open items.** A reviewer has an open item when either holds:

- One of its blocks is open, by v4's and v5's rules for a block: opened by its verdict, and open until it withdraws it or the human overrides.
- Its most recent qualifying verdict in this turn is a revision request (`approve-with-changes`). A qualifying verdict is one the parser classed `valid` or `revision_request`; a `malformed` or `rejected` verdict row is not one and neither begins nor ends an item. The item begins when the revision request is recorded. It ends when the same reviewer's next qualifying verdict on a later candidate is recorded: an `approve` ends it, a block replaces it with the block, and another revision request replaces it with the new one. Nothing else ends it. It is not stored separately: it is read from the turn's verdict rows, so it survives the clearing of a round's results when a revision is frozen, and a restart.

A reviewer with an open item is never excused. If it cannot review the current candidate, because it is suspended or cooling past the deadline when the round is dispatched, or because its attempt ends in a way that would otherwise excuse it, the turn ends `incomplete` with v4's reason for that item, `block:<block_id>` or `revision_request:<participant>`, taking the first in roster order, and not with a deadline or availability reason. Human override remains what v4 makes it. So excusal never withdraws a block, never overrides one, and never lets a candidate be approved past one.

**Outcomes for a reviewer with no open item.** This table is closed: every way a reviewer can fail to return a verdict on the current candidate is in one row, and the first row that applies decides.

| When | What happened | Outcome |
|---|---|---|
| Before the round is dispatched | It is suspended, whatever the cause, `operator` and `bad_response` included | Excused, cause `suspended:<cause>`; no attempt is created |
| Before the round is dispatched | It is cooling past the deadline | Excused, cause `cooldown`; no attempt is created |
| In the round | Its attempt's own result is `local`, `rejected_request` or `bad_response`; or fails the gate; or is truncated or refused; or its verdict is malformed after repair; or a reservation is refused; or a deadline has passed | v4's outcome for that result. Never excused, whatever suspension the result also causes |
| In the round, after its review has returned its result for this candidate | Its repair fails, or cannot be sent because it is suspended or cooling | v4's outcome for a failed repair, `reviewer_failed:<participant>:<status>`, the status being `suspended` or `rate_limited` when nothing was sent. Never excused |
| In the round, before any request of its review was sent | Its queued dispatch is removed because it became suspended, whatever the cause | Excused, cause `suspended:<cause>`, with no `attempt_id` |
| In the round, after a tool step and before its review has returned its result | Its continuation cannot be sent: it became suspended while the tools ran or while the continuation was queued, whatever the cause; or it is now cooling past the deadline | Excused, cause `suspended:<cause>` or `cooldown`, with the `attempt_id` of the tool step it would have continued. No continuation attempt is created |
| In the round | Its attempt finally fails in `unreachable`, `throttled`, `server_fault`, `uncertain`, `quota` or `rejected_config`; or its retry cannot fit (section 2); or it was `waiting` and ended because it became suspended | Excused, cause `failed:<class>:<transport_status>`, with `attempt_id` |

A member in `trial` is not excused before dispatch: its attempt is the trial. A reviewer whose cooldown ends before the turn's deadline is not excused: its attempt waits in the queue. A continuation whose cooldown ends before the turn's deadline waits in the queue like any attempt, and one that is sent and then fails is judged by the third row or the last. A `bad_response` is never excused in the round because the turn has paid for an answer it cannot read, and v4 ends the turn on it; repeated ones make the member unhealthy for later rounds and turns, which is the first row.

**The record.** Each excusal journals `reviewer_excused{turn_id, round, participant, cause, attempt_id?}` and a row in `turn_excusals`.

**The floor.** It is checked before the round is dispatched and again after every excusal. If the reviewers not excused number fewer than `min_reviewers`, the turn ends at once by the terminal path, `incomplete` with reason `reviewers_unavailable:<n>/<min>`. Before dispatch, that means no reviewer is dispatched. During a round, no further reviewer is dispatched and reviewers in flight are ended as the terminal path ends any attempt.

**Approval.** The turn is `approved` when every reviewer not excused in the current round has a valid approve on the candidate's hash, those reviewers number at least `min_reviewers`, and no open item stands. The terminal reason is `all_approved` when none was excused in that round and `approved_reduced:<ids>` when any was, the ids being those excused in the approving round, in roster order, comma-joined. A block from any reviewer that did review has its v4 and v5 effect. An excused reviewer never counts as an approve.

**Rounds.** In a v5 revision, each re-review decides excusal afresh, so a reviewer that recovers rejoins and reviews the current candidate. The author's revision request carries the output of every reviewer that reviewed, as v5 has it, and one engine-written line for each reviewer excused in that round saying that it did not review this candidate and why. It carries no text of an excused reviewer from that round, because there is none.

**Not hidden.** A reduced approval is never shown as a plain one:

- The `turn` reply and `turn_state` carry `excused: [{participant, cause, round}]` for every round, in round and then roster order, from `turn_excusals`, so a replay shows what the live turn showed.
- The chat tab keeps v5's `approved by <ids> against <hash prefix>` line and adds, on the next line, `REDUCED REVIEW: <n> of <m> required reviewers; excused <ids>`. The `/apply` form shows the same line above the changes.
- In a later turn's history the answer's label is `reviewed by <n> of <m>`, with 1 ≤ n < m, where v5 has `reviewed`. The history document becomes `syndic-history/v3` (V11): v2 with that one further label, accepted by its validator and parser. Documents and context hashes journaled under v1 and v2 remain valid as recorded.
- Terminal delivery goes to every participant that took part and is neither suspended nor cooling at delivery time (V10), and tells every recipient who was excused and why.
- The protocol version steps (section 9), so a client that does not know a reduced approval cannot attach and show it as a plain one.

**Solo.** `min_reviewers` has no meaning in a solo preset and is refused there by the validator.

## 6. Everyone who is not a reviewer

Section 2's retry applies to every attempt of every role, tool steps, repairs and referee requests included. The gates of sections 3 and 4 apply to every send. What each role does when the gate is closed:

| Role and moment | Cooling | Suspended |
|---|---|---|
| Author, at `submit` | Accepted and queued if the cooldown ends before the turn's deadline; otherwise refused `error{code: "author_unavailable", cause: "rate_limited", until?}` and no turn is created | Refused `error{code: "author_unavailable", cause, until?}`; no turn is created |
| Author, during the turn (revision, tool continuation, repair) | Waits in the queue; the deadlines decide | The turn ends `incomplete` with `author_failed:<status>`, as today |
| Deliberate proposer, critic, integrator, at `submit` | As the author, with `participant_unavailable` | As the author, with `participant_unavailable` |
| Deliberate proposer, critic, integrator, at a phase dispatch | Waits in the queue; the deadlines decide | The turn ends as v8 section 2 ends it |
| Deliberate final reviewer | Section 5 | Section 5 |
| Referee, at a decision | Waits if the cooldown ends before the calling attempt's deadline; otherwise deny, reason `referee_unavailable` | Deny without a dispatch, reason `referee_unavailable` |
| Terminal delivery | Not sent; recorded as v4 records an unsent delivery, `transport_status: "not_sent:rate_limited"` | Not sent; `transport_status: "not_sent:suspended"` |
| Startup-probe request (v4 section 1a, v5 section 1) | Not sent; the probe step fails and the participant is `unprobed` as v5 has it | Not sent; the same |

- There is no substitute author (O4), and no proposer, critic or integrator is excused (O5).
- A referee attempt that finally fails is a deny, as v5 and v7 already say for a referee that does not answer.
- Terminal delivery is never retried (v4, unchanged) and never waits.
- A probe step's result is classed by section 1 from its status and error kind, and feeds cooldown and health as a completion's does. The probe paths report the class and the server-stated wait themselves; they do not pass through `CompleteResult`. A probe of several requests records each response's cooldown before its next request, and that next request passes the gate of section 3 like any send, so a probe that is throttled on its first request sends no second. A probe never retries beyond what v5 specifies and, as v5 has it, reserves nothing.

## 7. What the adapter reads

A completion result, a probe step's result and a health check's result each gain `FailureClass`; `ServerWait`, which is absent, a duration, or unbounded, an explicit zero being a duration; and `NoRetry`. Nothing else of a response's headers leaves the adapter, and no header value is journaled raw. The headers are read from every response that was received, one whose body could not be read included.

**`ServerWait`, for the OpenAI-compatible adapter.**

1. `retry-after-ms`, an integer of milliseconds. Azure OpenAI documents it; OpenAI's SDKs read it before `Retry-After`.
2. `Retry-After`, as delay seconds or an HTTP date (RFC 9110 section 10.2.3). OpenAI documents it on a 429 and on a 503; Claude's API documents `retry-after` in seconds.
3. When both parse, the larger.
4. On a 429 with neither: the largest of `x-ratelimit-reset-requests`, `x-ratelimit-reset-tokens` and `x-ratelimit-reset-project-tokens` among those whose matching `x-ratelimit-remaining-*` is `0`. Only a duration with unit suffixes (`1s`, `6m0s`), which is what OpenAI documents, is read. A bare number is ignored, because Azure OpenAI sends bare numbers there and documents no unit. With none that qualifies, there is no wait.

A value that is not a number, a duration or a date of those forms, is negative, or is a date in the past is treated as absent. A wait is kept exactly whenever it fits a signed 64-bit count of milliseconds: two days is two days. A value of the right form that does not fit, a run of digits too long to hold, is `unbounded`; it is never replaced by a shorter wait.

**`NoRetry`.** True when the response carries `x-should-retry: false`, which Claude's API documents and OpenAI's SDKs honour.

**Quota markers.** A response is `quota` when its JSON body matches a row, read at exactly these paths. The table is in the adapter with one fixture for each row, and is expected to grow.

| Status | Path | Value | Source |
|---|---|---|---|
| any | `error.code` | `insufficient_quota`, `credit_balance_exhausted`, `organization_spend_limit_exceeded`, `project_spend_limit_exceeded`, `organization_usage_limit_exceeded` | OpenAI error codes |
| any | `error.type` | `insufficient_quota` | OpenAI error codes |
| any | `error.details.error_code` | `enforced_spend_limit_reached` | Claude spend cap |
| 400 | `error.type` and `error.message` | `invalid_request_error` and a message beginning `You have reached your specified` | Claude spend limit set by the organization |
| 402 | none needed | | |

A 429 whose `error.details.error_code` is `ramp_limit_exceeded`, or whose `error.code` is `slow_down`, is `throttled` like any 429 without a marker.

**Status table.** 429, 503, 529: `throttled`. 500 and any 5xx not listed: `server_fault`. 502: `uncertain`, or `server_fault` where the endpoint says so. 504, 408: `uncertain`. 401, 403, 404, a 3xx not followed, 501, 505: `rejected_config`. Any other 4xx: `rejected_request`. Any 404 is `rejected_config`, as the code has it today, with or without a model-not-found body.

**Sources.** The published pages these rules were written from: OpenAI, `https://developers.openai.com/api/docs/guides/rate-limits` and `https://developers.openai.com/api/docs/guides/error-codes`; Azure OpenAI, `https://learn.microsoft.com/azure/foundry/openai/how-to/quota`; Claude, `https://platform.claude.com/docs/en/api/rate-limits` and `https://platform.claude.com/docs/en/api/errors`.

**Fixtures.** The adapter's test carries the documented response shape for each row above and for each route the example roster uses. A new adapter reads its own provider's headers and bodies by the same rules and brings its own table.

## 8. Configuration

```toml
[retry]
max                      = 3
max_after_possible_work  = 1
base                     = "1s"
max_delay                = "60s"
max_server_wait          = "120s"

[suspend]
after              = 3
check_interval     = "30s"
check_max_interval = "10m"

[endpoints.example]
rate_limit_scope     = "example-org"   # absent: derived from origin and credential
rate_limit_per_model = false
bad_gateway          = "uncertain"     # or "server_fault"

[presets.review]
min_reviewers = 1        # absent: every required reviewer
```

Validated: durations positive; `max_delay ≥ base`; `check_max_interval ≥ check_interval`; `retry.max` at least 0; `max_after_possible_work` from 0 to `retry.max`; `suspend.after` at least 1; `min_reviewers` from 1 to the number of `required_reviewers`; `bad_gateway` one of its two values; `rate_limit_scope` a non-empty name; endpoints sharing a scope key, named or derived, agree on `rate_limit_per_model`. `retry.max = 0` disables retry entirely; unlike v4, `unreachable` then also gets none, and the validator warns. Cooldown and suspension work whatever `retry.max` is. Every setting here is frozen into the run's manifest with the other limits.

## 9. Protocol and journal

**Version.** `protocol` becomes 2. Every addition below is one a v1 client would ignore, as v4 section 2 allows; the version steps all the same, because a client that ignored `excused` and the new reason would show a reduced approval as an approval. The engine answers a `hello` with `protocol: 1` with `error{code: "protocol"}`, as v4 has it. An older client is refused on purpose.

**Participant.** `state` gains `rate_limited`, `quota`, `server_error`, `bad_response`, `suspended` and `trial`. A participant with a row that is not healthy also has `health: {row: "member" | "endpoint", cause, since, next_check?, streak, last_result?}`, for the endpoint row when both are not healthy; `status.health` lists every row. A participant under a cooldown, healthy or not, has `cooling: {until?, unbounded?, cause}`.

**Turn.** `excused` on `turn` and `turn_state` (section 5). Terminal reasons `approved_reduced:<ids>` and `reviewers_unavailable:<n>/<min>`.

**Requests.**

- `resume{participant | member: {endpoint, model} | endpoint | scope, clear_cooldown?}` → `{rows: [{endpoint, model, state}], cooldowns_cleared: n}`. `scope` is accepted only with `clear_cooldown`.
- `suspend{participant | member | endpoint, reason}` → `{rows: [{endpoint, model, state}]}`.
- `error{code: "no_such_participant"}`, `error{code: "no_such_member"}`, `error{code: "no_such_endpoint"}`, `error{code: "no_such_scope"}`.
- `status` gains `cooldowns: [{scope_key, identity, model, not_before?, unbounded?, cause}]` and `health: [{endpoint, model, state, cause?, streak, next_check?, generation}]`.
- `submit` may answer `error{code: "author_unavailable" | "participant_unavailable", participant, cause, until?}`.

**Events.** `attempt_wait`, `member_health`, `health_check`, `cooldown`, `cooldown_cleared`, `reviewer_excused`. `engine_started` gains `suspended_rows`, `cooldowns` and `unmatched_cooldowns`.

**Journal.** `attempt_dispatches` gains `failure_class`, `server_wait_ms`, `health_generation_member`, `health_generation_endpoint`, `reserved_tokens`, `usage_prompt`, `usage_completion`, `settlement` and `settled_at`; `attempts` gains the `waiting` state and its `not_before`; tables `cooldowns`, `member_health` and `turn_excusals`; one journal-wide generation counter.

## 10. TUI

The participants tab shows each participant that is not healthy: state, cause, which row, since, next check, the last result, and the `/resume` line for it; and each cooldown with its end, or `until cleared` for an unbounded one. A waiting retry shows on the attempt as `retry n/max in <seconds>`. The status strip shows the turn's terminal reason beside its state, and so `approved_reduced:<ids>`; v4 section 3 already requires the reason there and the strip as built shows the state only, so this is work for this milestone and not a description of what exists. `/resume` and `/suspend` are added; `/suspend`, and `/resume` with `clear_cooldown`, ask for confirmation.

## 11. Acceptance

Every line is tested with the fake adapter or an `httptest` server. No test contacts a real endpoint.

Classes and reading:
- A1. Each status and error kind of section 1 yields its class; an unlisted 4xx is `rejected_request`, an unlisted 5xx `server_fault`, a 3xx to another host `rejected_config`.
- A2. An invalid message, an unresolved secret and a refused `BeforeSend` hook are each `local`; a refused connection reported as `TransportStatus` `unreachable` with no Go error is `unreachable`; a `reset` and a `timeout` reported with no Go error are `uncertain`.
- A3. 504 and 408 are `uncertain`; 502 is `uncertain` by default and `server_fault` on an endpoint with `bad_gateway = "server_fault"`.
- A4. A non-2xx whose body read fails or is over the limit keeps its status class and its `ServerWait`; a 2xx whose body read is reset is `uncertain`; a 2xx over the body limit is `uncertain` and opens a hold; a 2xx read whole and malformed is `bad_response`.
- A5. A result returned together with a Go error keeps its class, `ServerWait` and `NoRetry` through the engine.
- A6. A redirect to the endpoint's own host is followed and the final response classed; a failure after it is `uncertain`.
- A7. `retry-after-ms`, `Retry-After` in seconds and as a date each yield the stated wait; with both, the larger; a malformed, negative or past value is absent; an explicit `0` is a wait of zero and is not absent.
- A8. `Retry-After: 172800` yields a wait of two days exactly; a run of digits too long for 64 bits yields `unbounded`.
- A9. The reset headers yield a wait only on a 429 with no retry header, only for a limit whose remaining is `0`, and only from a duration with units; a bare number yields none.
- A10. Each row of the quota table yields `quota` at its status and path, and the same value at another path does not; `ramp_limit_exceeded` and `slow_down` yield `throttled`.
- A11. `x-should-retry: false` sets `NoRetry`.
- A12. A probe step's result and a health check's result carry the same class and wait as a completion with the same response.
- A13. A request the engine cancels before any response is received has no class and records nothing. A 429 with `Retry-After` whose headers are received as the engine cancels the turn is quarantined for the turn and its cooldown is recorded.

Retry:
- A14. A 429 with `Retry-After: 2` is retried once after at least 2 s on the injected clock, with one `attempt_wait` event at the result, `n` 1, and one new dispatch row at the send, `retry_count` 1, and the turn completes when the retry succeeds.
- A15. `retry.max` bounds retries over all classes; `max_after_possible_work` bounds those that follow a `server_fault`.
- A16. `local`, `quota`, `rejected_config`, `rejected_request`, `uncertain` and `bad_response` are never retried; nor is a result with `NoRetry`. `uncertain` opens a hold, 502 and 504 included, and the others do not.
- A17. A retry that would be sent at or after the attempt's or the turn's deadline is not made and the attempt ends as a final failure of its class, not as a deadline; a server wait above `max_server_wait` fails the attempt without waiting.
- A18. Jitter never shortens a delay, and a capped delay plus jitter may exceed the cap by at most a quarter.
- A19. A `waiting` attempt holds no pool slot: with pool concurrency 1, an attempt to another member of the pool is dispatched while the first waits. A queued dispatch to a cooling member does not stop the dispatch queued behind it.
- A20. Immediately before a retry is sent, a turn that closed and a cancellation each end the attempt with no dispatch row and no hold; a deadline that has passed ends it with v4's deadline reason.
- A21. A cooldown extended during the wait to a time before both deadlines keeps the attempt `waiting`, emits `attempt_wait` again, uses no retry, and the retry is then sent; one extended past a deadline ends the attempt as a final failure of its class.
- A22. A retry whose row is in `trial` owned by another attempt stays queued; when that trial restores the row the retry is sent, and when it suspends the row the retry ends as a final failure.
- A23. A member that becomes suspended while its attempt is `waiting` ends that attempt at once.
- A24. Cancelling a turn while an attempt is `waiting` opens no hold for it and opens one for an attempt `in_flight`.
- A25. An engine stopped while an attempt is `waiting` restarts with that attempt ended, no hold and no `unknown` mark; one stopped while an attempt is `in_flight` restarts as v4 has it.

Reservations:
- A26. A 429's reservation is credited back and its retry reserves again; a 429 that reports usage is charged that usage.
- A27. A 500's reservation stands and its retry adds a second; a 500 that reports less than its reservation is charged the reservation, and one that reports more is charged what it reported.
- A28. An `uncertain` result that itself reports more than its reservation is charged what it reported, in the `Tx` that records it, and exhausts the turn's budget there if it passes the limit; one that reports less, or nothing, keeps its full reservation. A late result for a settled send reporting more raises the charge once, one reporting less changes nothing, and the same result applied twice changes nothing.
- A29. Usage reported by a retry does not change the first send's charge; each dispatch row shows its reservation, usage and settlement.
- A30. A retry whose reservation is refused ends the turn `budget:turn`, and for a reviewer that is not an excusal. A probe request and a health check reserve nothing.

Cooldown:
- A31. A `throttled` result records a cooldown when a retry follows and also with `retry.max = 0`, with a wait above `max_server_wait`, with no room before the deadline, and when the turn is cancelled before the wait ends.
- A32. A two-day `Retry-After` records a cooldown that ends two days on; an unbounded wait records an unbounded cooldown that only `clear_cooldown` ends.
- A33. Until the cooldown in force has passed, nothing is sent: no attempt, retry, repair, tool continuation, referee request, terminal delivery, startup-probe request or health check.
- A34. Two endpoints with one origin and one credential share a cooldown; two with different credentials do not; `rate_limit_scope` joins endpoints; `rate_limit_per_model` confines a cooldown to its model; a whole-scope record and a model record together give the later time.
- A35. A roster whose endpoints share a scope key, named or derived, and disagree on `rate_limit_per_model` is refused.
- A36. A cooldown earned under a derived scope still stops the endpoint after a restart that gives it a `rate_limit_scope` name, and one earned under a name still stops it after the name is removed or changed; a record that matches no endpoint is listed in `engine_started.unmatched_cooldowns`.
- A37. Of two cooldowns recorded for one scope key, identity and model, the later `not_before` stands whichever arrives first.
- A37a. Endpoints A and B, with different derived identities, share named scope X and are both limited, in either order: two records exist; after a restart in which X is renamed, A is still stopped until its own time and B until its own.
- A38. A transport-ok result from an earlier send, a passing check and a plain `resume` do not end a cooldown; `resume{participant, clear_cooldown}` deletes exactly the records that stop that participant, `resume{scope, clear_cooldown}` exactly those of that name, and each journals `cooldown_cleared`.
- A39. A cooldown survives a restart.
- A40. A health check answered 429 with `Retry-After` records a cooldown and fails; a probe throttled on its first request records a cooldown and sends no second request.

Health:
- A41. A 401 suspends the endpoint row at once, for every model on it; a 403 suspends the member row only, and another model on the endpoint is still dispatched.
- A42. Three consecutive exhausted 500s suspend the member and two do not; a success or a 400 between them resets the streak; a `local` failure between them does not; an attempt ended early by a suspension counts for nothing.
- A43. Three consecutive `unreachable` attempts on three different models of one endpoint suspend the endpoint row.
- A44. Three consecutive `bad_response` results suspend the member with cause `bad_response`.
- A45. Each cause yields the wire state of section 4's cause table; a participant whose endpoint row is suspended shows that row's state.
- A46. No attempt is dispatched to a member whose member row or endpoint row is suspended; a dispatch queued then is removed; a request in flight is not cancelled.
- A47. A suspended row is checked at the interval, which doubles on failure to the maximum; a passing check gives `trial`; checks run one at a time; none starts after `engine_stopping`.
- A48. An endpoint row in `trial` admits one attempt across all its models and pools, and a second, to another model, waits; a dispatch needing both rows in `trial` takes both or waits.
- A49. A transport-ok trial restores every row it owns; a trial ending in a failure for a row suspends that row with the interval doubled; a trial ending `local`, cancelled or at a deadline leaves the row in `trial` with no owner; a trial answered 500 restores the endpoint row and suspends the member row.
- A50. A check sends `GET /models` only, takes no pool slot, reads at most the bound, reserves nothing, and waits for the cooldown in force.
- A51. An endpoint whose `/models` answers 404 moves its rows to `trial` at `next_check`; a `model_unavailable` member passes only when the list names its model.
- A52. A `throttled` suspension moves to `trial` when the cooldown passes, with no check sent.
- A53. A success sent under an older generation does not clear a later suspension, and a failure sent under an older generation changes neither state nor streak; both appear in `last_result`.
- A54. A row restored and suspended again never repeats a generation, across a restart included.
- A55. A failure arriving for a request sent before an operator's suspension leaves the cause `operator`; an operator's `suspend` of a row already suspended for another cause gives it a new generation and clears `trial_owner`; an operator suspension is never checked and ends only by `resume`.
- A56. `resume{participant}` moves both a suspended member row and a suspended endpoint row to `trial` and lists them; `resume{endpoint}` moves the endpoint row only; `suspend{endpoint}` stops every model on it.
- A57. Suspension, streak and generation survive restart; a `trial_owner` does not; `engine_started` lists suspended rows and cooldowns.
- A58. A suspension changes no manifest hash, and an `unprobed` participant on a healthy member is still refused as v5 refuses it.

Review:
- A59. With `min_reviewers` absent, a suspended reviewer ends the turn as v4 does.
- A60. With `min_reviewers = 1` and two reviewers, one suspended: the other is dispatched, its approve gives `approved` with reason `approved_reduced:<id>`, `excused` names the missing one with cause `suspended:<cause>`, and the chat shows the `REDUCED REVIEW` line.
- A61. A reviewer suspended before dispatch is excused whatever the cause, `operator` and `bad_response` included.
- A62. A reviewer that exhausts retries mid-round is excused with cause `failed:<class>:<status>` and the turn continues while the floor holds; so is one whose retry cannot fit before the deadline.
- A63. A reviewer whose own result is `local`, `rejected_request` or `bad_response`, fails the gate, is malformed twice, meets a passed deadline or is refused its reservation is not excused, and a third `bad_response` that also suspends the member still ends the turn as v4 does.
- A64. A reviewer whose queued first dispatch is removed by a suspension is excused with cause `suspended:<cause>` and no `attempt_id`; one whose repair cannot be sent for the same reason ends the turn `reviewer_failed:<participant>:suspended`.
- A64a. A reviewer that becomes suspended while its tools run after a tool step, and one that becomes suspended while its continuation is queued, are each excused with cause `suspended:<cause>` and the tool step's `attempt_id`, and no continuation attempt is created; a reviewer whose continuation is sent and returns a malformed verdict twice is not excused.
- A65. Fewer than `min_reviewers` available before dispatch ends the turn `reviewers_unavailable` with no reviewer dispatched; an excusal during the round that breaks the floor ends it at once with no further dispatch.
- A66. A reviewer that blocked round 0 and is suspended at round 1 is not excused: the turn ends `incomplete` with `block:<block_id>` and is not approved by the others; override still works.
- A67. A reviewer whose round 0 verdict was a revision request and that approves the revised candidate in round 1 leaves no open item, and the turn is `approved`.
- A68. The same reviewer, suspended at round 1 or cooling past the deadline, is not excused: the turn ends `incomplete` with `revision_request:<participant>`; a malformed verdict row recorded after the revision request does not end the item; the item is read from the verdict rows and is the same after a restart.
- A69. A block from the remaining reviewer is a block; an excused reviewer is never counted as approving.
- A70. A reviewer excused in round 0 that recovers is dispatched in round 1 and reviews the current candidate; the author's revision request names it as not having reviewed round 0.
- A71. `excused` lists every round's excusals in round and roster order and is the same after a restart; `approved_reduced` lists only the approving round's, in roster order.
- A72. A reviewer whose cooldown ends before the turn's deadline is not excused and its attempt waits; one whose cooldown ends after it, or is unbounded, is excused with cause `cooldown`.
- A73. A later turn's history document begins `syndic-history/v3` and labels a reduced approval `reviewed by <n> of <m>`; the validator accepts that label and refuses `reviewed by 2 of 2`; the `/apply` form and terminal delivery state the excusals.
- A74. A deliberate turn's final review excuses a reviewer by the same rules.

Others:
- A75. A submit whose author is suspended, or whose cooldown ends after the turn's deadline, is refused `author_unavailable` and creates no turn; one whose cooldown ends before it is accepted and waits.
- A76. A deliberate submit with a suspended proposer is refused `participant_unavailable`; a throttled proposal is retried and the turn still ends as v8 says on its final failure.
- A77. A suspended referee member denies with `referee_unavailable` and no dispatch; a cooling one waits when the cooldown fits the calling attempt's deadline and denies when it does not.
- A78. Terminal delivery to a suspended member is recorded `not_sent:suspended`, to a cooling one `not_sent:rate_limited`, and neither is retried.
- A79. A startup-probe request is not sent under a cooldown, and a probe step that is throttled records one.

Protocol and TUI:
- A80. A `hello` with `protocol: 1` is refused; the `status`, `turn`, `resume` and `suspend` replies carry the members section 9 names.
- A81. The status strip shows `approved_reduced:<ids>` on a reduced approval; the participants tab shows an unbounded cooldown as `until cleared`.

## 12. Milestone

One milestone, I, after E, G and H: its engine tasks change the dispatch path those three also change, so it is registered once theirs have merged. Task shape, in dependency order: failure classes, header and body reading in the adapter, the probe paths and the fake, with `callOnce` keeping a result that comes with an error; configuration and manifest; attempt states and the retry lifecycle; per-send reservations and settlement; cooldown, scopes and matching; the health rows, generations, trials and the journal; the background check; operator requests; open items, excusal and the reduced approval; history v3; the non-reviewer rules; protocol version and members; TUI, the strip's reason included; the acceptance suite.

## 13. Disposition of review

**Revision 3, November 27, 2026.** 73,399 bytes, SHA-256 `250b80ad…b4f8b1a4`. Approved by Muse. Blocked by Codex with three required changes, cited here as E1 to E3; Codex found D1 to D4, D6, D7, D9 and D10 satisfied. Neither objected to S1 to S4, and both left O1 to O9 unchanged.

| Point | Required | Where answered |
|---|---|---|
| E1 | Keep every observing identity when cooldowns combine | Section 3, record and combining: one record for each `(scope_key, identity, model)`, combined only when read; A37, A37a |
| E2 | Charge usage that comes with the first uncertain result | Section 2, reservations: the `uncertain` rows, and the charge and exhaustion in one `Tx`; A28 |
| E3 | A suspension between a reviewer's tool attempts | Section 5: the paragraph defining a review and its attempts, and the continuation row of the outcomes table; A64a |

Codex's optional notes, all taken: "qualifying verdict" in section 5's open items; an operator's suspension takes a new generation over another cause (section 4); O3 no longer says a hold is released only by the operator. Muse's optional notes, all taken: endpoints sharing any scope key agree on `rate_limit_per_model` (sections 3 and 8); a late final result counts once for the streak and a deadline never does (section 4); when `trial_owner` is cleared (section 4); which row `health` shows and the cause of a row in `trial` (sections 4 and 9); a retry that goes back to waiting gives up its slot (section 2); the consequence of the 403 split (section 4).

**Revision 2, November 27, 2026.** 51,580 bytes, SHA-256 `f402268b…88f52518`. Approved by Muse, with every revision 1 point satisfied. Blocked by Codex with eleven required changes, cited here as D1 to D11; Codex found C2, C3, C8 and C12 satisfied and the rest satisfied in part. Neither objected to S1 to S4.

| Point | Required | Where answered |
|---|---|---|
| D1 | Never shorten a long cooldown; fail closed on an unrepresentable one | Section 7, `ServerWait`: kept exactly, or `unbounded`; section 3, when one is recorded, what ends it; section 2, how long. The 24 h substitution is gone |
| D2 | Keep throttling evidence across cancellation | Section 1, precedence rules 1 to 3 and the paragraph after rule 6; section 3, when one is recorded; A13 |
| D3 | Endpoint-wide health and who owns a trial; what `/resume` does | Section 4: rows, which row a result touches, trials, operator. 401 is endpoint-wide and 403 is the member's, so that one refused model does not suspend its endpoint |
| D4 | Generations that are never reused; an operator's suspension is not replaced by a late failure | Section 4, generations: one rising counter, rows never deleted, both generations on the dispatch row, and a stale result goes to `last_result` only. "A failure always takes effect" is withdrawn |
| D5 | v4's conservative reconciliation; no usage lost; durable per-send records; idempotent late results | Section 2, reservations: the settlement table and the paragraph after it; section 9, journal |
| D6 | Results with no Go error; `callOnce`; same-host redirects; a 2xx over the body limit | Section 0, the code as it is; section 1, precedence, and the paragraph on what the adapter returns. A 2xx over the limit is `uncertain`, with a hold (V1) |
| D7 | Every outcome of a refused retry admission; "cannot fit" apart from a passed deadline | Section 2: the table under "when the wait ends", "the retry cannot fit", "ending while waiting"; A20 to A23 |
| D8 | Scope matching, validation, what a clear removes, canonical identity, reconfiguration | Section 3: scope, record, matching, what ends it; section 8 |
| D9 | Health checks and probes read like any response; check concurrency and shutdown; no budget for probes; a probe's requests gated one by one | Section 4, recovery; section 6, the last bullet; section 2, reservations |
| D10 | When a revision request stands, what ends it, how it is rebuilt | Section 5, open items; A67, A68 |
| D11 | One closed mapping for excusal; no excusal by way of queue removal for a non-excusable failure; operator suspension | Section 5, the outcomes table |

Codex's optional notes, all taken: the source pages are listed in section 7; section 10 says the strip's reason is new work; the history label has its validator change and, since the document is hashed, a version, `syndic-history/v3` (V11). Codex's remarks on O3, O7 and O9 are taken into section 1 and section 14.

Muse's optional notes on revision 2, all taken: the endpoint row's streak and trial (section 4); an open item under a long cooldown ends the turn with the item's reason (section 5); the cause and `attempt_id` of a queue removal (section 5); a `waiting` attempt ends at once on suspension, and an attempt ended early counts for no streak (sections 2 and 4); `n` in `attempt_wait` (section 2); `cooling.until` for a healthy participant (section 9); V10 for terminal delivery; why `bad_response` is not excused in the round (section 5); `unreachable` with and without a Go error (section 1); A49 and A63 name `local`.

Changes in revision 3 that neither reviewer asked for in these words, made to answer the points above: the 403 split in D3; `uncertain` for a 2xx over the body limit in D6; the derived identity on every cooldown record in D8.

**Revision 1, November 27, 2026.** 24,377 bytes, SHA-256 `df29ed54…054bc3e0`. Blocked by both. Muse required eight changes (R1 to R8) and Codex thirteen (C1 to C13), with five corrections to what the draft said of the code and of earlier designs. Neither objected to S1 to S4. Sections were renumbered in revision 2: cooldown is new as section 3, so revision 1's sections 3 to 11 are now 4 to 12, its section 12 is now 14, and this section is new as 13.

| Point | Required | Where answered |
|---|---|---|
| R1, C9 | One exhausted attempt must not suspend; one transition table; trial, operator and streak defined; updates ordered | Section 4: the states table, the cause table, generations |
| R2, C8 | An attempt cannot both keep its slot and let another member dispatch | Section 2: states of an attempt, the queue |
| R3, C7 | A waiting retry is not in flight; journal the dispatch apart from the wait; recheck before sending; recovery for each state | Section 2: when a retried result arrives, when the wait ends, ending while waiting; V8 |
| R4, C1 | A block survives excusal; the floor is rechecked during the round | Section 5: open items, the floor, rounds |
| C2 | Name the change to the v3 addendum's consensus rule | Section 0: V7; section 5 |
| R5, C3 | 504 and 408 are not evidence that work stopped | Section 1: intermediaries. 502 is `uncertain` by default, with a per-endpoint declaration; see O3 |
| R6 | Class, cause and wire state; transport-ok; trial outcomes | Section 4: the tables; section 1 |
| R7 | Budget and `quota`; what `max_after_possible_work` counts; jitter and caps; S1 and `quota` | Section 2: how many, how long, reservations. A `quota` response is never retried and is honoured by suspension and, where it states a wait, by a cooldown |
| R8 | Each role under a cooldown and under a suspension; excusal causes and order | Section 6: the table; section 5: the outcomes table, approval |
| C4 | Cooldown persists whatever becomes of the retry | Section 3 |
| C5 | Rate-limit scope is not one model | Section 3: scope |
| C6 | Every possibly charged send is reserved and reconciled by itself | Section 2: reservations |
| C10 | Classification total; local faults apart from endpoint faults; repeated malformed replies | Section 1: `local`, precedence; section 4: `bad_response` in the streak |
| C11 | `/models` bounded, optional and never a substitute for a probe; every dispatch path gated | Section 4: recovery, effect; section 3: what it stops; section 6 |
| C12 | Reduced approval durable and unmistakable; the wire contract | Section 5: the record, not hidden; section 9: version 2 and members |
| C13 | Exact paths, precedence, absent against zero, overflow | Section 7 |

Corrections taken in revision 2: the `Complete` path reads no header and the probe paths do; a reviewer's transport failure ends the turn, it does not start a revision round; v8's failure clauses are in its section 2; terminal delivery already does not retry; probes report class and wait themselves, not through `CompleteResult` (Codex). The author's own check found two more: revision 1 journaled a retry before its wait, against the code's rule that a journaled retry is a dispatched one, and it said v4 gives `uncertain_release_after` no default, where v4 gives it `0s`, meaning never.

Provider documentation checked for section 7: `retry-after-ms` is documented by Azure OpenAI and not by OpenAI; OpenAI's reset headers are durations with units and Azure's are bare numbers; `billing_hard_limit_reached` is in no published document found and is removed; the documented quota codes are those now in the table.

Where the reviewers differed in revision 1: on 502. Muse would class it `server_fault` with one retry and no hold; Codex would treat every intermediary's answer as `uncertain`. Revision 2 took the stricter default with a per-endpoint declaration, and in round 2 both accepted it: Muse as a compromise, Codex as an operator's declaration and not evidence from the status. It stays listed for the operator as O3.

**Revision 4 terminal votes.** Codex: APPROVE ("Required changes" "None."; E1, E2 and E3 each "Satisfied"). Muse: APPROVE ("None. My revision 1 points R1 to R8 remain satisfied in revision 4."). Neither objected to S1 to S4. Both support O1 to O9 of section 14 as qualified in their verdicts; those items stay as stated, block nothing, and remain the operator's to overrule. Muse's four optional notes on this revision are not taken into the text, which is the voted text; they are recorded here with the author's reading of each, for the task specs, and they change no rule: in section 3's combining, an unbounded observation counts as later than any bounded one for the same scope key, identity and model, and a bounded one never replaces it; in section 5's continuation row, a reviewer that is both suspended and cooling past the deadline is excused with cause `suspended:<cause>`; in section 2, a dispatch that stays queued because the pool does not admit it holds no slot, like any queued dispatch; and in section 2's settlement table, a send whose `uncertain` result itself reported usage is charged `max(reservation, reported)` whether or not its attempt is also unknown. Operator, November 27, 2026: "Ratify v10". Voted on the text of revision 4 at 78,951 bytes, SHA-256 `1f099ddfaba8377824af9618fd3eb5f973a23c54d425ef354c4d49b454cb98e1`; this paragraph is the only difference between that text and this one. The record is `review/2026-11-27-v10-delta-rev4-review.md`.

## 14. Open for the operator

- O1. **`min_reviewers` defaults to the full set**, so a preset degrades only when the operator says so. Both reviewers support this.
- O2. **A reduced approval is `approved`**, with its own reason, not a new terminal state. Both support it, given that a block survives excusal and the protocol version steps.
- O3. **Intermediary answers open a hold.** 504 and 408 are `uncertain`. 502 is `uncertain` unless an endpoint sets `bad_gateway = "server_fault"`. Both reviewers now support this; Muse first preferred `server_fault` for 502, and Codex accepts the setting only as the operator's own declaration about an endpoint. Under v4's default a hold has no timed release, so an endpoint behind a gateway that times out can hold a pool slot until one of the release paths O7 lists.
- O4. **No substitute author.** Both support this.
- O5. **No excused proposers, critics or integrator in deliberate mode.** Both support this; deliberate final reviewers are excused by section 5.
- O6. **Recovery checks use `/models` only**, and an endpoint without that route recovers by timed trial. Both support no automatic paid inference.
- O7. **Adjacent, not in this delta:** v4 defaults `uncertain_release_after` to `0s`, which means never. A hold is then released only by a cancellation the server confirms, by a late result for the held attempt, or by the operator. This delta makes holds more common by adding 502, 504, 408 and an over-long 2xx body. Whether the example roster should ship a release time, and whether tool turns should have a per-step deadline, are separate questions. Both reviewers would keep them separate; Codex would keep no timed release as the default.
- O8. **The default rate-limit scope is the whole endpoint and credential**, every model on it. That is the reading of S1's "a limited endpoint" and is the safe one. It pauses a model that may have had its own headroom, and it does not join two credential names that draw on one account: `rate_limit_per_model` narrows it and `rate_limit_scope` joins. Both reviewers support the default.
- O9. **The protocol version steps to 2.** Both support it. An older client is refused on purpose; the TUI and the engine ship as one binary, but a client built apart from the engine must be updated with it.
