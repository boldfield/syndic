# v8 delta: deliberate mode

Revision 3, DRAFT, dated November 26, 2026. Author: Fable, in the claude.ai session that couriers the peer rounds. Not approved, not hash-bound. Revision 2 (30,404 bytes, SHA-256 `90d73e5e09b89680f5e59860a2a8acdddac9009df76de8084ad65e50e1f00dab`) was approved by Muse and blocked by Codex on November 26, 2026; revision 1 was blocked by both; section 10 records the rounds. This delta builds the deliberate protocol that candidate v2 section 2 and the v3 addendum section 2 define and that the v3 delta section 4 deferred. It extends the v5 delta as ratified (SHA-256 `6cb570b01ee5c40744e3f97c986d33095b9c056d205efd332b024b713b005577`) and the v6 delta as ratified (SHA-256 `1cd2b40adfe726b58d91e837215599c7245d6d09976b5d1fbe7aa3ca56cb1dd7`), and it states how the v7 delta (SHA-256 `ad31943f7005d2a3dbc0ca9245f3f5f16f2f0180626b60cf8a1c3fd8926be428`) and the v9 delta (SHA-256 `b3f8d8e2356822093a8a69946e3157c1e0e1efce3497bba3a1b2ede2eae184f8`) apply in its phases. It changes one earlier clause, named in section 0. Peer review of this delta does not by itself authorize implementation.

## 0. Inputs

Operator direction, November 23, 2026: a "discussion" preset, beside review and solo, "where every question is discussed by each model before a consensus answer is given".

That is the deliberate protocol. v2 section 2: "independent proposals from all participants; every participant receives all complete peer proposals; one full peer-critique round; a designated integrator creates one candidate and objection disposition list; required reviewers inspect that exact candidate. The integrator cannot approve its own candidate." The v3 addendum section 2 adds that every declared proposer must complete its proposal and its critique, that a missing one is INCOMPLETE, that no majority, quorum or reduced roster is ever inferred, and that candidate identity includes the relevant discussion and evidence.

**Name.** One name throughout, the ratified one: the mode is `"deliberate"`, and the example preset this delta ships is named `deliberate` too, as the `review` and `solo` presets are named for their modes. "Discussion" is the operator's word for it and is not used as an identifier.

Operator decisions, November 23, 2026:

- D1. Tools are as in review: the integrator has the author's tools, every other use of a participant has the reviewer's tools (section 3).
- D2. The integrator is fixed in the preset (section 1).
- D3. The chat shows the consensus answer; the proposals and critiques are in the drawer and the transcript (section 6).
- D4. The mode and its preset carry the same name.

The default preset remains `review`, as v3 delta section 4 requires. Deliberate is chosen with `/mode` or a quick profile, like any preset.

Clause changed:

- V1. The candidate's canonical form gains one line (section 2), so its version becomes `syndic-candidate/v5`. `v3` is v6's and `v4` is v9's.

## 1. Roster

A preset with `mode = "deliberate"` has:

- `proposers`: two or more participant ids. Each proposes and each critiques.
- `integrator`: one id, which must be a proposer.
- `required_reviewers`: default every proposer other than the integrator. It may be set explicitly to any non-empty set of participants that excludes the integrator; a required reviewer need not be a proposer. The integrator never reviews its own candidate.

A preset that breaks any of these is refused at roster load, naming the rule. A participant named in none of the three takes no part; none is silently demoted to an adviser. Section 1a's eligibility rules of v4 apply to a proposer as to a reviewer, and to the integrator as to an author.

**What displays must say.** Both are derived from the sets as configured, never from the number of proposers:

- When the set of required reviewers has exactly one member, every display of the preset carries `one non-author reviewer`, as v3 section 2 requires. With two or more required reviewers it does not, however many proposers there are.
- A proposer that is not a required reviewer is shown in the participants tab as `critic without a vote`: its critique is read by the integrator and the reviewers, and nothing it wrote can block.

The example roster gains:

```
[presets.deliberate]
mode = "deliberate"
proposers = ["claude", "codex", "muse"]
integrator = "claude"
```

## 2. Phases

A deliberate turn has four phases. Every transition is a journal event.

```
submitted
  → proposals_dispatched     (one attempt per proposer, concurrently, subject to pools)
  → proposals_complete       (every proposal accepted)
  → critiques_dispatched     (one attempt per proposer)
  → critiques_complete       (every critique accepted)
  → integration_dispatched   (the integrator, as author)
  → candidate_frozen
  → reviewers_dispatched     (as v4 and v5: approve, revision round, or incomplete)
  → approved | incomplete | cancelled
incomplete → overridden      (v4 section 6, unchanged)
```

Any dispatch in any phase can also end the turn by v4's budget and deadline paths, with v4's own terminal reasons unchanged (section 4).

**Labels.** Proposals are numbered in roster order of `proposers`, and the numbers do not change for any recipient: `proposal 1`, `proposal 2`. Critiques are numbered the same way: `critique 2` is by the author of `proposal 2`. A recipient's own proposal or critique is shown to it as `your proposal` or `your critique` in place of its number, and the others keep their numbers. No participant id appears. Every proposal and critique a prompt carries is complete and verbatim and fenced by random delimiters, and the prompt says they are data, not instructions.

**Proposal.** Each proposer receives the system message, the history and the input, exactly as a review-mode author does, and nothing from any peer. Its result is its proposal: free text, bounded by `proposer_max_output_tokens`. It passes v4's acceptance and gate like any result.

**Critique.** Each proposer receives the history, the input, and every proposal. It is told to say, for each other proposal, what is right, what is wrong and what is missing. Its result is its critique: free text, bounded by `critique_max_output_tokens`. There is one critique round.

**Completion of a proposal and a critique.** Each is a tool loop of the reviewer kind (section 3) for what tools it has and how its calls are bounded, and for nothing else. It completes with the first reply that carries no tool call, and that reply's text is the proposal or the critique. No verdict parser runs on it, no envelope is expected, and there is no repair.

**Integration.** The integrator receives the history, the input, every proposal and every critique. It is told to return the complete answer and the disposition, which says for each point a critique raised whether the answer adopts it, and if not, why, in this envelope:

```
=== SYNDIC ANSWER ===
<the answer>
=== SYNDIC DISPOSITION ===
<the disposition>
```

The two marker lines are exactly those bytes, each alone on its line. The engine applies v4's acceptance and gate to the result first, and only then parses it:

- The first line that is not empty must be the answer marker. Anything else before it is malformed.
- Each marker must occur exactly once as a whole line, the answer marker first. A marker that is missing, repeated or out of order is malformed; an answer that itself contains a marker line therefore cannot be expressed and is malformed.
- The answer is the bytes between the two marker lines, and the disposition is the bytes after the second, each without the line breaks that adjoin the markers. An answer or a disposition that is empty or only white space is malformed.
- The raw result is journaled whole whatever the outcome.

The answer is the candidate, frozen as in review mode. The disposition is journaled with its round and its candidate's id, and stored by content hash.

**Repair.** A malformed integration result gets one repair, and so does a malformed result of each revision; the allowance is one per integration attempt, not one per turn. The repair request is the original request's messages, then the malformed result as an `assistant` message, then one `user` message with this fixed text, followed by the envelope as shown above: `Your reply was not in the required form. Reply again with the same answer and the same disposition, in exactly this form:` It offers no tool definitions, and a tool call in its reply is a failure. Whatever the original attempt staged, writes or effects, stands as staged; the repair re-runs no tool. The repair attempt records the attempt it repairs. Its output bound is the integration bound. A second malformed result ends the turn `incomplete` with reason `integration_malformed:<id>`.

**Binding the deliberation.** The deliberation document for a candidate is the line `syndic-deliberation/v1` and its newline, then one `proposal` field for each proposer in roster order, then one `critique` field for each proposer in roster order, then one `disposition` field holding that candidate's disposition, each field written as v5 writes a history field: a line `<name> <byte-length>` followed by exactly that many bytes and a newline. `deliberation_hash` is its SHA-256. The candidate's canonical form, `syndic-candidate/v5`, is v4's with one line added after `effects`: `deliberation:<deliberation_hash or "none">`. It is `none` outside deliberate mode. An engine that implements this delta before v9's milestone writes `effects:none`. An approval therefore binds the answer, the proposals, the critiques and that round's disposition together: changing a byte of any of them changes the candidate hash. Candidates journaled under earlier versions remain valid as recorded.

**Final review.** Each required reviewer receives what a review-mode reviewer receives (the request, the history, the candidate) and also every proposal, every critique and the candidate's disposition. It answers with v5's verdict envelope. From here the turn is a review-mode turn with the integrator as author: approval, blocks, v5 section 4's revision loop with its limit and ceiling, exhaustion, override and terminal delivery all apply, with the differences the next three paragraphs state.

**A revision.** No proposal or critique is dispatched again. The integrator's revision request is: the system message; the history; the input; one `user` message carrying every proposal and every critique, as in integration; the previous candidate as an `assistant` message, in the envelope, with its answer and its disposition; then v5's `user` message carrying every reviewer's complete output with the results its blocks cite. The integrator returns a revised answer and a revised disposition in the envelope, parsed and repaired as above. A repair of a revision carries the same messages.

**An unchanged revision.** v5's comparison stands and compares what it compared: the answer bytes and the change set lines, and under v9 the effects. A revision that changes only the disposition is unchanged and ends the turn `incomplete` with reason `revision_unchanged`.

**Re-review.** Each required reviewer's prompt contains what v5's re-review contains, the request, the history, the new candidate and that reviewer's own open blocks, and also every proposal, every critique and the new candidate's disposition. It does not contain other reviewers' blocks or an earlier disposition.

**Failure.** Every declared proposer must complete its proposal and its critique. A proposer whose attempt fails acceptance or the gate, does not complete, or passes its deadline ends the turn `incomplete` with reason `proposal_failed:<id>` or `critique_failed:<id>`. An integration attempt, first or revised, that fails acceptance or the gate, does not complete, or passes its deadline ends the turn `incomplete` with reason `integration_failed:<id>`; the one repair is for a malformed envelope only and never for these. Nothing proceeds with a reduced roster. A turn that ends before `candidate_frozen` has no candidate and cannot be overridden.

**What is never done.** No proposal or critique is summarised, truncated or dropped for any recipient (v2 section 3). When a prompt that must carry them does not fit, the turn is refused or ends, by section 4.

## 3. Tools, files and egress

D1 maps v5's two tool sets onto the phases. The preset's `tools` is the integrator's set in the integration phase and in every revision. The preset's `reviewer_tools` is every participant's set in the proposal, critique and final review phases. So one participant, the integrator, stages writes, as v2 section 8 requires, and everyone else may read and look things up.

- **Command execution (v7).** With the `exec` group in `tools`, the integrator has `bash` in integration and in every revision, as v7's author. With it in `reviewer_tools`, every participant has it in the proposal, critique and final review phases, as v7's reviewer: its own scratch copy, results private to it. v7's sandbox, capability contract, referee, execution gate, evidence and taint rules apply in every phase by that role. This applies once v7's milestone is in; until then a preset naming `exec` is refused as any preset is.
- **MCP tools (v9).** `read` tools named in `reviewer_tools` are available in the proposal, critique and final review phases, with results to that participant only. `read` tools named in `tools` are available to the integrator. `effect` tools may be named only in `tools`, as v9 requires; only the integrator stages effects, in integration and in a revision, and they run only when the human applies them. v9's referee, pre-send checks, file-holding rule, instance rule (one local server process per participant per turn, across that participant's phases) and gate apply in every phase by that role. This applies once v9's milestone is in.
- **Loops and counters.** A proposal, a critique and a final review are each a reviewer tool loop under v5 section 6. Every bound that v5, v7 or v9 sets per reviewer per round (`tools.max_reviewer_calls`, and v9's reviewer bound on MCP calls) has a separate count for each participant in the proposal phase, in the critique phase, and in each round of final review: the first review and the review of each revision each start from zero, as rounds do in review mode. Integration and its revisions are author tool loops, and every bound set per author per turn covers integration and all revisions together. Every bound set per turn stays per turn and is shared by all phases.
- **Results across phases.** A participant's tool results in the proposal phase and in the critique phase go to no other participant and are not carried into its own later requests. What it learned there reaches its later self and its peers through what it wrote. Nothing else about evidence changes. The integrator's results in integration and in a revision are an author's results: they go to every required reviewer verbatim with the candidate and are bound by it, as v5 has author tool results, v7 command evidence and v9 MCP results. A final-review block may cite, by v5's rule, results of that reviewer's own calls in that review round, and the results a block cites follow it into the integrator's revision request, as section 2 and v5 have it.
- **Snapshot.** The turn has one observation record, as v5 has it. Observations made before the candidate exists are in the record the candidate's `snapshot_hash` binds at freeze. Observations a reviewer makes in final review extend the record and are not bound by that candidate, which is already frozen, as in review mode.
- **Blind.** Proposals are blind to each other's text. They are not blind to the workspace: two proposers that read the same file are served the same recorded bytes.
- **Egress.** Every proposal, every critique and every disposition goes to every endpoint that a later phase's prompt reaches. The routing policy of v3 addendum section 4 is checked, when the preset is validated, for every pair of participants between which a proposal, a critique or a disposition travels, and for the candidate as in review mode. A preset that would violate it for any of those is refused before any dispatch; no peer text is omitted to make a preset fit. A proposal, critique or disposition written with `read` in view is file-derived content, and v5's file-egress and web-with-file rules apply to a deliberate preset as to any preset that grants `read`.

## 4. Budgets and deadlines

- **Output bounds.** v4 section 4a's budgets table gains `proposer_max_output_tokens` (default 4096), `critique_max_output_tokens` (default 2048) and `integrator_max_output_tokens` (default 6144). The last bounds the whole integration result, answer and disposition together, and is used for integration, for each revision and for the repair of either. Each may be overridden in a quick profile's `budgets` exactly as v4 section 4b lets a quick profile override v4's bounds, with the same precedence and as part of the same effective definition that v4 hashes, and each is clamped to the probe record's `output_bound` when there is one. `author_max_output_tokens` is not used in deliberate mode. Reviews use `reviewer_max_output_tokens` as today.
- **Stand-ins.** A stand-in proposal is `proposer_max_output_tokens × bytes_per_token` of its proposer; a stand-in critique likewise; the stand-in for a candidate and its disposition together is `integrator_max_output_tokens × bytes_per_token(integrator)`.
- **Projection at submit.** The engine builds every phase's prompts with the real prompt builders and the stand-ins: N proposal prompts and outputs; N critique prompts, each carrying N stand-in proposals; the integration prompt, carrying N proposals and N critiques, and its repair prompt, carrying those and a stand-in malformed result; each required reviewer's prompt, carrying those and a stand-in candidate and disposition, with its repair; and the deliveries. Each prompt must fit its recipient's usable bound, else `error{code: "budget", budget: "context", participant, phase}`; the sum must fit `turn_max_estimated_tokens`. v5's rule that the projection refuses only on the first round stands.
- **At dispatch.** Every attempt is admitted and reserved over its rendered request as today. A refused reservation ends the turn by v4's terminal path with v4's reasons, kept apart as v4 keeps them: `budget:context:<participant>` when the request does not fit that participant's bound, `budget:turn` when the turn's total would be passed.
- **Deadlines.** The attempt deadline applies to each attempt. The turn deadline covers all four phases and every revision round, and is not extended.
- **Cost.** With three proposers and the default reviewers, a turn approved without revision, repair or tool call makes nine dispatches that produce text, three proposals, three critiques, one integration and two reviews, and three terminal deliveries: twelve. Review mode with the same three participants makes three and three. The participants tab and `/budget` show the projection per phase.

## 5. History and records

- A later turn's history carries a deliberate turn's input and approved answer as it carries any turn's. Proposals, critiques and dispositions are not carried forward.
- The turn record gains the mode, the phase, and for each proposal, critique and disposition its participant, attempt and content reference. A disposition's record also has its round and the id of the candidate it belongs to. The candidate's record has its `deliberation_hash`.
- New read-only request `deliberation{turn_id, cursor?, limit?}` → `{items: [{phase, participant, attempt_id, label, round?, candidate_id?, content: {ref, sha256, byte_len}}], next_cursor?}`, ordered by phase and then roster order, with dispositions in round order, paged like v6's `reviews`. `label` is the numbered label other participants saw (`proposal 2`). Every disposition is returned, one for each candidate, with its `round` and `candidate_id`; a malformed integration result is not a disposition and is in the transcript.
- New events: `proposals_dispatched`, `proposals_complete`, `critiques_dispatched`, `critiques_complete`, `integration_dispatched`.

## 6. TUI

v6 stands. What deliberate adds:

- **Progress line.** The phase is `propose`, `critique`, `integrate` or `review`, with each participant's state as today.
- **Outcome.** The outcome line and every banner are review mode's, unchanged. The next line, a content line, reads `integrated by <id> from <n> proposals`, and adds `one non-author reviewer` exactly when section 1 requires that string.
- **Drawer.** `Ctrl+O` opens it as in v6. For a deliberate turn its header offers `proposals`, `critiques`, `disposition` and then the review rounds; `←` `→` move between them. Proposals and critiques are shown by participant id, as safe text; the disposition shown is the current candidate's, and earlier ones are in the transcript. While the turn runs, a proposal or critique appears in the drawer when it is accepted, under the line `UNREVIEWED PROPOSAL` or `UNREVIEWED CRITIQUE`.
- **Chat.** No proposal or critique appears in the chat tab. The transcript tab shows every attempt of every phase.
- **Status line.** The preset's name, as today.
- **Participants tab.** Each participant's part: proposer, integrator, required reviewer, and `critic without a vote` where section 1 requires it.

## 7. Acceptance

Against the fake adapter. No test contacts a real endpoint.

Roster: a deliberate preset with one proposer, with an integrator that is not a proposer, with the integrator among the required reviewers, or with no required reviewer is refused at load; a required reviewer that is not a proposer is accepted; with two proposers and the default reviewers the displays carry `one non-author reviewer`; with two proposers and two explicit outside reviewers they do not; with three proposers and one explicit reviewer they do, and the two proposers left out are each shown as `critic without a vote`; a preset in which a proposal, a critique or a disposition would cross a route the routing policy forbids is refused at validation and nothing is dispatched.

Phases: no proposal request contains any peer's text; each critique request contains every proposal verbatim, numbered in roster order with the recipient's own as `your proposal`, and no participant id; the integration request contains every proposal and every critique verbatim; each final review request contains every proposal, every critique, the candidate and its disposition verbatim; the integrator receives no final review request; a proposal or critique completes on the first reply without a tool call and no verdict parser or repair runs on it; a failed, incomplete or late proposal ends the turn `incomplete` with `proposal_failed:<id>` and no critique is dispatched; a failed critique ends it with `critique_failed:<id>` and no integration is dispatched; an integration attempt that fails the gate, does not complete or is late ends it with `integration_failed:<id>` and no repair is dispatched; approval requires a valid `approve` from every required reviewer on the current candidate hash; a turn that ended before a candidate was frozen cannot be overridden.

Envelope: a well-formed result yields the answer and the disposition without the adjoining line breaks; text before the answer marker, a missing marker, a repeated marker, markers out of order, an empty answer and an empty disposition are each malformed; the raw result is in the journal in every case; a result that fails the gate is not parsed; a malformed result is repaired once, by a request that carries the original messages and the malformed result, offers no tool and records the attempt it repairs; a tool call in the repair reply is a failure; writes and effects staged by the original attempt are unchanged by the repair; a second malformed result ends the turn `incomplete` with `integration_malformed:<id>`; a revision gets its own one repair.

Binding: the deliberation document for a list of proposals, critiques and a disposition parses back to them; changing one byte of a proposal, of a critique or of the disposition changes `deliberation_hash` and the candidate hash; the candidate form begins `syndic-candidate/v5` and carries the `attachments`, `effects` and `deliberation` lines; a candidate outside deliberate mode has `deliberation:none`; each disposition's record names its round and candidate; an approval of round 1's candidate does not match round 2's hash when only the disposition changed.

Revision: a block in final review starts a revision round in which no proposal or critique is dispatched again; the revision request carries every proposal, every critique, the previous answer and disposition in the envelope, and the reviewers' outputs; a revised result with the same answer and change set and a new disposition ends the turn `revision_unchanged`; a re-review request carries every proposal, every critique, the new disposition and that reviewer's own open blocks, and no other reviewer's block.

Tools: a proposer's and a critic's requests carry the definitions of `reviewer_tools` and never a write tool or an `effect` tool; only the integrator's requests carry `tools`; a tool result from the proposal phase or the critique phase appears in no other participant's request and not in that participant's own later requests; the integrator's tool results in integration and in a revision, a command's output and an MCP result among them, reach every required reviewer verbatim and changing one changes the candidate hash; a result cited by a final-review block is in the integrator's revision request after that block; a final-review block citing a result from an earlier phase is refused as v5 refuses a citation it cannot resolve; a participant that reaches `tools.max_reviewer_calls` in its proposal has the full bound again in its critique; a reviewer that reaches it in the first review has the full bound again in the review of the first revision; a per-turn bound reached in the proposal phase is still reached in the critique phase; with `exec` in `reviewer_tools`, a proposer's command runs in its own scratch copy under v7's reviewer rules, and with it only in `tools` no proposer is offered it; an MCP `effect` staged in integration is in the candidate and is sent only on apply; two proposers reading one path are served the same bytes; a deliberate preset granting `read` with a participant outside the egress destinations is refused at load.

Budgets: each of the three output bounds is taken from the table, overridden by a quick profile, and clamped to the probe's bound; the integration request and its repair carry `integrator_max_output_tokens`; a roster whose integration prompt cannot fit the integrator's bound is refused at submit with `error{code: "budget", budget: "context", participant, phase}` naming the integrator and the phase; the projection lists each phase and includes the integration repair; a critique dispatch that does not fit its recipient ends the turn `budget:context:<participant>`, and one that would pass the turn total ends it `budget:turn`; the dispatch count for three proposers approved without revision is twelve, deliveries included; no proposal is truncated for any recipient.

Records and TUI: `deliberation` returns each proposal and critique once with its numbered label, returns one disposition for each candidate with its round, and pages with `cursor`; a later turn's requests contain no proposal, critique or disposition of an earlier turn; the chat tab shows no proposal or critique; the drawer shows them under the unreviewed line while the turn runs; the outcome banner strings are review mode's verbatim, followed by `integrated by <id> from <n> proposals`.

## 8. Milestone

One milestone, G, after D: roster, validation and displays; the four phases and their state machine; the prompt builders, including revision and re-review; the integration envelope and repair; the deliberation document and `syndic-candidate/v5`; the projection and admission by phase; the `deliberation` request and records; the TUI additions; acceptance. Tools in deliberate mode follow milestone E; `exec` follows v7's milestone and MCP tools follow v9's, and the acceptance lines that name them are run when those are in. Roughly sixteen to twenty-two tasks.

## 9. Open points

1. **Anonymous labels.** Participants see `proposal 2`, not `codex`, as v5 labels reviewers for the author, so that a model weighs the text and not the name. The human sees the ids. The engine cannot stop a model naming itself in its own text.
2. **Critiques are free text.** The engine does not extract objections, so nothing mechanical checks that the disposition answers each one. The check is the critics themselves, who are the required reviewers by default and can block. A critic left out of `required_reviewers` has no vote, and the participants tab says so.
3. **One critique round.** v2 fixes it at one. More rounds are a later delta.
4. **Cost.** Deliberate mode makes three times review mode's text-producing dispatches at three participants, and grows with N squared in prompt bytes, because every critic reads every proposal. The projection refuses what does not fit; nothing here makes it cheap, and nothing here promises a better answer.
5. **Results do not cross phases.** A participant that looked something up while proposing must look it up again while critiquing, or have written it down. Carrying results forward would make the later prompts larger and their size harder to project.

## 10. Disposition of review

**Revision 1, November 26, 2026.** 15,633 bytes, SHA-256 `e257885f…71d75ac5`. Blocked by both. Muse required seven changes (P1 to P7) and Codex six (K1 to K6). On revision 1's five open points both supported the author on anonymous labels, free-text critiques, one round and cost. On the first, the disposition's place outside the candidate hash, Muse supported the author and Codex did not, citing the v3 addendum's rule that candidate identity includes the relevant discussion; the author concedes it, and revision 2 binds the whole deliberation.

| Point | Required | Where answered |
|---|---|---|
| K1 | Bind the deliberation evidence to approval | Section 2: binding the deliberation; V1; section 5; acceptance, binding |
| K2, P1 | The integration envelope, its grammar, and the repair contract | Section 2: integration, repair, an unchanged revision; acceptance, envelope |
| P2 | Integration failure other than a malformed envelope | Section 2: failure |
| K3, P3 | Complete requests across revisions and phases | Section 2: completion of a proposal and a critique, a revision, re-review; section 3: results across phases |
| K4, P7 | The phase budget contract; budget terminals | Section 4; section 2, the line after the diagram |
| K5, P4 | The v7 and v9 mapping, and counters by phase | Section 3: command execution, MCP tools, loops and counters; section 8 |
| P5 | The disposition in the routing check | Section 3: egress |
| K6, P6 | Reviewer-count display from the actual set | Section 1: what displays must say; section 6 |

Optional notes taken: numbering by roster order with `your proposal`; every disposition returned with its round; a required reviewer need not be a proposer; observations in final review; the error format verbatim (Muse). The routing citation corrected to v3 addendum section 4; an excluded critic shown as such; no promise of quality (Codex).

**Revision 2, November 26, 2026.** 30,404 bytes, SHA-256 `90d73e5e…1f00dab`. Approved by Muse: P1 to P7 satisfied, the binding of the deliberation accepted. Blocked by Codex: K1, K2, K4 and K6 satisfied; two required changes (R1, R2). Both support the author on all five open points.

| Point | Required | Where answered |
|---|---|---|
| R1 | The rule on results across phases must not cancel required evidence transfers | Section 3: results across phases; acceptance, tools |
| R2 | Final-review rounds keep separate counters | Section 3: loops and counters; acceptance, tools |

Optional notes taken: the repair text quoted; the document's header line and its newline (Muse). Quick profiles may override the three new bounds, by v4 section 4b's rule (Codex); revision 2 wrongly said a preset's `budgets`.

**Revision 3 terminal votes.** Codex: APPROVE (R1 and R2 satisfied; "Required changes: none"). Muse: APPROVE (R1 and R2 satisfied; "My revision 2 P1 to P7 remain satisfied"). Both support the author on the five open points of section 9, which stay as stated and block nothing. Codex's optional note on this revision is recorded here because it corrects the record: the remark above that revision 2 "wrongly said a preset's `budgets`" is itself inaccurate, since v4 section 2b lets a preset override budgets and v4 section 4b applies a quick profile's overrides after it; section 4's rule takes v4's precedence whole and stands as written. Operator, November 26, 2026: "Ratify v8". Voted on the text of revision 3 at 32,383 bytes, SHA-256 `6e6f8c0a6e961f2e06dee956204fc579ee3713c6572ac4c3369058d7c116b25d`; this paragraph is the only difference between that text and this one. The record is `review/2026-11-26-v8-delta-rev3-review.md`.
