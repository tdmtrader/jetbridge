# Curation record — feedback-jb-003

## Provenance walk

Backed out of a **review round whose answer is one commit**. A design-quality
review of the agentic-platform program (spec + `00-shared-contracts.md` + 14
workstream plans) was committed as a document; the next commit on `main`
answered all twelve confirmed findings and appended its own per-finding
resolution log to that same document. Because the review is *in-tree at the
cut*, the task input is verbatim rather than reconstructed — and because the
answer is a single commit with a resolution table, the ground truth is
finding-granular.

| Role | SHA | Date | Subject |
|---|---|---|---|
| grandparent | `f8153f730ebfa53dee0cfabdd089e859b24293da` | — | (parent of the review commit) |
| **pre_state** (the review lands) | `efb0a8cdb068c5e6e6074e0ffc1d0a63e7218b4f` | 2026-07-08T20:38:17-07:00 | `docs(agentic-platform): design review (18 reviewers, adversarially verified)` |
| **terminal artifact** (the answer) | `8157023ae91960df4a3ca2208a87c2062d33c7f1` | 2026-07-09T07:51:25-07:00 | `docs(agentic-platform): apply all 12 design-review findings (F1-F12)` |
| next round (withheld) | `2fd306913c` | 2026-07-09T17:37:38-07:00 | `final Fable review (§8) - 32 confirmed findings F13-F40` |
| next round applied (withheld) | `cf5d0d2f2e` / `e04802bc46` | 2026-07-10 | `apply final-review findings F13-F39` / `§9 resolution log` |

### Verification performed

Every claim in the mining candidate was re-checked. All held; three details
were corrected or sharpened.

- **Topology.** `git rev-list --parents -n1 8157023ae9` →
  `8157023ae9 efb0a8cdb0`. The terminal artifact is the **direct, only** child
  of the pre_state; there is no rebase, squash or gap. `git merge-base
  --is-ancestor 8157023ae9 main` → true, so `ground_truth.outcome: merged` is
  literal (it landed on the mainline, not via a PR).
- **The pre_state is exactly "the review, nothing applied".** `efb0a8cdb0`
  changes one file: `REVIEW.md`, +207 lines, created. So the tree at the cut is
  the plan set as reviewed, with the findings sitting next to it.
- **The terminal artifact says what the candidate claimed.** `git log -1 --format=%B
  8157023ae9` enumerates the per-finding resolution and matches the diff:
  11 files, **+933/-122**, and every file is owned by at least one finding.
  `REVIEW.md` §7 (the resolution log, 23 added lines) is the commit's own
  per-finding answer key.
- **Every defect is really present at pre_state.** Each anchor recorded in
  `ground_truth/expected_findings.yaml` was read at `efb0a8cdb0` with
  `git show` / `git grep <rev>` (no checkout). Spot examples, all confirmed
  verbatim: `11-dispatch.md:842` is `func renderCheckpointStep(in RenderInput,
  s workflow.Step) atc.AgentStep` with the LLM prompt at `:854` and the two
  dead env vars at `:849`/`:850`; `07-agent-step.md:1884` is the unconditional
  `if step.budgetChecker != nil && rm.CostUSD > 0`, with the two `StreamFile(ctx,
  …)` calls at `:1817`/`:1835`; `05-workflow-store.md:2634` is "The approved
  spec and plan are in spec.md and plan.md" and `:731` is the lone
  `AskTimeoutSeconds < 0` check; `12-delivery-outcomes.md:2611` is the
  `continue // already tracked; …elsewhere`; `00-shared-contracts.md:1465`
  is "during dispatch, before run creation", `:1452` is the bare "gateway
  enforces", `:1472` is the one-way "created and kept in sync";
  `13-scorecards.md:441` is a `perVersion` that calls only `metricsAndCost`;
  `14-process-intel-experiments.md:3733` is "Read intel.md in your workspace",
  `:3959` is `f := intel.Filter{}` and `:3976` is `Body: string(snapshot)`;
  `02-credentials-and-budgets.md:3735` is the silent
  `platform-credential-not-vaulted` no-op.
- **Line-number correction.** The review cites the seed prompts at
  `:2636/:2643/:2650`; at the cut they are at `:2634/:2641/:2647`. Off-by-a-few
  citations like this are in the review as filed and are part of the honest
  input — `expected_findings.yaml` records the *actual* pre_state lines.
- **`bench/` does not exist at pre_state** (`git ls-tree efb0a8cdb0 bench/` is
  empty), so the schema's self-hosted-corpus caveat is satisfied: replaying
  this case cannot expose the corpus or its answers.
- **The subject code does not exist at pre_state.** Verified absent with
  `git cat-file -e`: `atc/exec/agent_step.go`, `agent/dispatch/render.go`,
  `agent/workflow/parse.go`, `agent/credentials/platform_syncer.go`,
  `agent/outcomewatcher/watcher.go`, `atc/db/scorecard_store.go`,
  `agent/api/metrics/types.go`. The program has not started executing (ROADMAP
  wave 1 is pending on this review), so the deliverable can only be plan edits.
  This is what makes the case structurally unambiguous without the task having
  to say so; `task.md` says it once anyway, as orientation.

### Why the pre-state is coherent as a "respond to feedback" moment

`efb0a8cdb0` *is* the review landing. The review's §1 says wave 1 is gated on
the wave-1 finding (F5) and that "the blockers are in wave 4, so wave 1 can
begin now while these are scheduled" — i.e. the very next action after this
commit is to answer the review. The real history did exactly that, eleven hours
later, in one commit. The trigger is not a reconstruction; it is the schedule
the review itself set.

## Leakage analysis

`withheld: []` — **nothing present at pre_state gives the answer away.** The
exposure risk here is entirely *descendants*.

### Deliberately exposed, and why: REVIEW.md is the task input

For a feedback-loop case the findings document is not a leak; it is the work
item. The boundary that matters is **§1–§6 yes, §7 no**, and it draws itself:
§7 (the resolution log) is added by the terminal commit, so a clean checkout of
`efb0a8cdb0` is already correctly scrubbed. Verified: `REVIEW.md` at the cut is
207 lines ending at §6's "Defer (optional polish)" line, blob
`7df38a45f57263f1e02ac886b74aba774c128ea3`; `task/review.md` is byte-identical
to it (same blob hash).

Note what this exposes on purpose: the review carries a **"Recommended change"**
paragraph per finding, plus a ranked §6 action list. That is a lot of guidance —
and it is exactly what the human answering the review had. The skill under test
is therefore *not* "diagnose twelve defects"; it is "read a verified review
carefully, apply the right remedy at the right scope to each, keep a
twelve-way change coherent across three co-signing owners, and do not
over-apply". This is deliberate and is recorded in `case.yaml#curation.learnings`
per the schema's execute-a-written-plan rule.

### Withheld by construction (must not be reachable)

- **`8157023ae9` and every descendant.** The diff is the answer and §7 is a
  per-finding answer key. The pre_state is its parent, so a *detached* snapshot
  cannot reach it — but a clone with refs can: `git branch -a --contains
  8157023ae9` lists ~18 tips including `main` and `jetbridge`. **Materialize
  this case as a detached checkout / `git archive` at the pinned SHA with no
  other refs and no reflog.** This is the whole leakage story.
- **`2fd306913c`, `cf5d0d2f2e`, `e04802bc46` in particular.** These continue the
  same review chain (§8 findings F13–F40 and their §9 resolution log) on the
  *corrected* set, so they leak both the structure of the answer and the later
  document layout. Same withholding rule covers them.
- **The curation machine's memory files.** `~/.claude/projects/…/memory/` on
  this machine carries several notes about the agentic-platform program
  (`project_agentic_platform_end_state.md`, `project_remainder_plans_scoped.md`,
  `project_agentic_functions_v3_cleanup.md`, and others) that summarise
  decisions downstream of this review. They are
  outside the repo and cannot leak through the snapshot, but they *will* be in a
  Claude Code session's context here. Any run of this case must be executed
  without them loaded, or the result is void. Flagged because it is invisible to
  a purely git-based exposure audit.
  **Declared as `known_leak_channels: [project-auto-memory]` in `case.yaml`**
  (curator-fixup 2026-07-25), per the README's "Operator-environment leakage"
  rule: replay harnesses must not mount project memory, session context or
  conversation history into the solver, and a local hand-run on this machine is
  invalid unless they are suppressed. The memory itself is not edited — it is a
  working artifact of the dev machine, not part of the corpus.

### Checked and found clean at pre_state

- `git grep -E '\bF(1|2|…|12)\b' efb0a8cdb0 -- docs/superpowers` excluding
  `REVIEW.md` → **no hits**. No plan references the finding IDs.
- `git grep -i "resolution log" efb0a8cdb0 -- docs` → **no hits**.
- `git grep -i "2026-07-09" efb0a8cdb0 -- docs/superpowers` → **no hits**. No
  future-dated amendment text exists at the cut.
- `git grep UpsertReturningInserted efb0a8cdb0` → **no hits**. None of the
  reference's new identifiers appears anywhere.
- The `agentic-platform` directory at the cut is 18 files / 2.15 MB
  (`ROADMAP.md`, `workstreams.json`, `00`–`14`, `REVIEW.md`); none of the six
  plans the reference leaves untouched contains a description of the fixes.

### What was scrubbed from `task.md`

`task.md` is a reframing of a trigger that, in real life, was a short spoken
instruction ("apply the review"). Two things were written and then cut:

1. **A per-finding work list.** An early draft grouped the findings by wave and
   said which were doc-only. That is the entire discriminator — it would have
   converted "read the verification outcomes" into "follow this list". Cut.
2. **"Do not apply §4."** True, and the review says it in §6, but repeating it
   in the task removes the restraint axis from the rubric. Cut; the agent has to
   read §6.

Two more were cut by the 2026-07-25 fixup, for the same reason one step further
out — they did not name an answer, but each pre-warned a rubric axis:

3. **"Its verification pass changed some findings on the way through — read what
   each finding actually concluded … and respond to *that*."** This is the
   disposition trap said out loud. The fact that the findings were adversarially
   verified is a property of the document and stays in the Context section (the
   review's own §2 preamble says it); the instruction to key off the
   verification paragraph rather than the severity label does not. Cut.
4. **"Do not widen the blast radius beyond what a finding justifies."** The
   restraint axis as advice. Cut. The scope boundary that remains
   ("keep the change scoped to the design set") is environment orientation a
   real work item would carry, and its rubric point is now annotated as
   compliance rather than judgement.

Also cut in the same pass: the trailing clause of the record-keeping bullet
("…and so the review itself shows what was done about each finding"), which
pointed the responder at `REVIEW.md` as the place to write the per-finding
record — i.e. at §7, the reference's own answer key location. The Deliverable
still asks for "a per-finding record of what was done and why" with no file,
section or format named.

What survived and is authentic: the orientation to the three document layers and
the normativity of `00-shared-contracts.md` (both stated verbatim in
`ROADMAP.md` at the cut); "the review is an input, not a work order" (the
repo's own `superpowers:receiving-code-review` discipline, and the review
demonstrably argued with itself); the cross-workstream co-sign rule (ROADMAP §
"How to use this directory" and every plan's Task 1); the dated-addendum
convention (present in every plan at the cut); and the deliverable being one
commit.

Nothing in `task.md` names a file, a finding, a symbol or an approach.

## Case shape and grading

- **Exposure manifest** = repo at `efb0a8cdb0` (detached, no other refs)
  − nothing withheld + `task/` (`task.md`, `review.md`).
- **Rubric is `judge`.** The deliverable is an edit to design documents; many
  textually different edits are correct. `ground_truth/rubric.md` scores three
  separate axes — **coverage** (did each finding get a response),
  **disposition** (was it the right *kind* of response), **restraint** (did the
  response stay inside what the findings justify) — plus cross-file coherence,
  right-remedy-where-two-existed, and record-keeping. Runs must report the axes
  separately; a single number hides the case's entire point.
- **The precision oracle is derived, not invented.** §6 explicitly defers all of
  §4, and the reference leaves six whole plan files untouched (`01`, `03`, `04`,
  `06`, `09`, `10`) plus `ROADMAP.md` and `workstreams.json`. §5 names five
  things not to touch. Both lists are artifacts, which is a stronger position
  than `review-jb-001` could reach.
- **`ground_truth/validate_ground_truth.sh` is validation, not grading.** It
  checks fourteen textual anchors that are absent at the parent and present at
  the child. It proves the SHAs and the reference diff are what the case claims.
  It is useless for scoring an agent — the anchors encode the reference author's
  identifiers (`UpsertReturningInserted`, `applyWindow`, "there is no runtime
  race") and a correct answer with different names fails all fourteen. The two
  uses are kept apart on purpose.

## Open questions

- **Is the task too large for one turn?** The exposed plan set is ~2.15 MB of
  markdown across 18 files and the correct response touches 11 of them with
  ~1000 lines of edits. This is the largest case in the corpus by exposed
  surface. If pilot runs saturate low on *coverage* while scoring fine on
  *disposition*, that is a budget failure, not a capability signal — the honest
  fix is a companion case scoped to one wave (F3 + F4 + F5 make a natural
  wave-1/2 slice and carry the same disposition trap in F5) rather than a looser
  rubric.
- **Is §7 (the resolution log) part of the required deliverable?** The reference
  wrote one, and `task.md` asks for "a per-finding record of what was done and
  why" without naming §7 or `REVIEW.md`. The rubric therefore scores
  record-keeping at 2 points out of ~36 and accepts any auditable form. If a
  strong response records dispositions only in its commit message, that should
  score the point; flag for the validation pass whether judges agree.
- **F10's acceptable alternative is a judgement call.** The review offers
  spec-delivery as "simplest"; genuinely materialising an `intel.md` input is
  defensible if prompt, seed and trigger all move onto it *and* the dispatch
  materialization contract is amended. `expected_findings.yaml` records this;
  it is the one place the rubric leans on judge discretion rather than the
  artifact.
- **The refuted-candidate list is not in the repo.** The review says one finding
  was refuted outright and three were downgraded, but only the survivors are
  written up. The pre-verification finding set exists only in the originating
  session. A future case built from a review that committed its *rejected*
  candidates would give a much stronger precision oracle — the same platform gap
  `review-jb-001` flagged, still open.
- **Pairs with the same program's later rounds.** `2fd306913c` → `cf5d0d2f2e` →
  `e04802bc46` is the identical shape one step later (F13–F40, with its own §9
  resolution log) and would make a second feedback case with a *much* larger
  finding set. Its pre_state is a descendant of this one's, so the two overlap
  heavily and must never be run in the same session or pooled as independent
  samples. Noted so it is not re-mined blind.
- **Sibling not built:** the §4 "minor polish" list is itself a candidate for a
  *negative* case ("here is a review; §6 defers all of §4 — what should you do
  about §4?" with `ground_truth.outcome: wont_fix`). Cheap to build from the
  same SHAs, and it would isolate the restraint axis. Not built here because it
  would share this case's pre_state.

## Validation

### Extractor pre-check (informational — not the formal validation pass)

At the time this paragraph was written `case.yaml` recorded
`validation.status: unvalidated`; it records `validated` now, on the strength of
the formal pass below. What follows in this subsection is the build-time sanity
check only.

Method: the nine touched plan files plus the touched spec were extracted with
`git show <full-sha>:<path>` into two throwaway trees under the scratchpad (no
checkout, no worktree — the repo was treated as read-only) and
`ground_truth/validate_ground_truth.sh` was run in each. Blob hashes were
verified against `git rev-parse <sha>:<path>` before running, after an earlier
attempt was silently invalidated by a clobbered scratchpad directory (see the
caveat below).

| Tree | Result |
|---|---|
| `efb0a8cdb068c5e6e6074e0ffc1d0a63e7218b4f` (pre_state) | **14 MISS, exit 1** |
| `8157023ae91960df4a3ca2208a87c2062d33c7f1` (terminal) | **14 ok, exit 0** |

Clean fail-at-pre / pass-at-post across all twelve findings (F7 and F12 carry
two anchors each). The ground truth is real and the before/after states are
what the case claims. No toolchain, Postgres, network or build is involved.

**Round-trip check (stronger, and it closes the loop).** `docs/superpowers` was
extracted at `efb0a8cdb0` with `git archive`, and
`ground_truth/reference.diff` was applied to it:

- `git apply --check` → **clean** (the reference diff is a valid patch against
  the pinned pre_state, so `pre_state.ref` and the diff cannot have drifted
  apart).
- after `git apply`, `validate_ground_truth.sh` → **14 ok, exit 0**.
- after `git apply`, the resulting blobs equal the terminal artifact's
  (`07-agent-step.md` → `a4092c85bfe1150f01b41d2fddb8f1fdee8b963a`, matching
  `git rev-parse 8157023ae9:…`), and `REVIEW.md` is 230 lines (207 + the 23-line
  §7 resolution log).

So pre_state + reference.diff reconstructs the terminal artifact byte-for-byte.

Caveat recorded because it nearly produced a false result: the first run of this
check reported "ok" at pre_state for 13 of 14 anchors. Cause was a corrupted
extraction, not a bad anchor — the shell loop that wrote the two trees resolved
the wrong revision and both trees ended up holding the working-tree version of
every file. It was caught only by hashing the extracted blobs against
`git rev-parse <sha>:<path>`. **Hash the materialized files before trusting any
validation run**; a validation that quietly compares a tree to itself looks
exactly like a passing case.

### Formal validation

Filled 2026-07-25; the mechanical detail is in the **Validation** section
immediately below, which is the section `case.yaml#validation.notes` points at.

- status: **validated** (mechanical legs only — see the caveat on the last line)
- corpus commit validated against: the corpus commit that seals this case
  (v0 sealing is git; the case directory as committed on
  `claude/jetbridge-test-corpus-b7a23f`)
- ground-truth validation re-run (14 MISS at pre / 14 ok at post): **yes** —
  14 MISS / exit 1 at `efb0a8cdb0`, 14 ok / exit 0 at `8157023ae9`, plus the
  round-trip (`reference.diff` applies clean to the pinned pre_state and
  reproduces the terminal blobs).
- judge-rubric sanity (does a strong response score >0 on the disposition axis,
  i.e. does anyone actually leave F2/F12 as documentation?): **not run** — no
  pilot response has been scored against `rubric.md` yet. This remains the one
  open validation question and is the reason the case is `judge`-graded rather
  than mechanical; it does not affect the ground truth, which is artifact-backed.
- leakage audit (two independent models): opus **borderline**, sonnet **pass**,
  then curator-fixup **pass** after the task-side leading text was removed
  (see "Fixup 2026-07-25" at the end of this file).
- notes: nothing in this case compiles or runs; no Postgres, build, cluster or
  network.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `efb0a8cdb068c5e6e6074e0ffc1d0a63e7218b4f`, post `8157023ae91960df4a3ca2208a87c2062d33c7f1`
- outcome: **validated** (both legs; ground-truth / drift guards only — agent grading is `ground_truth/rubric.md`)

### Anchor script — `sh <case>/ground_truth/validate_ground_truth.sh`
PRE: exit 1, **14 MISS / 0 ok**:
```
  MISS F1
  MISS F2
  MISS F3
  MISS F4
  MISS F5
  MISS F6
  ...
  MISS F11
  MISS F12a
  MISS F12b
```
POST: exit 0, **14 ok / 0 MISS**. Needs only POSIX sh + grep; runs in well under a second.

### Drift guard — reference.diff still applies to the pinned pre_state
Run without mutating anything, using a read-only `git archive` of the pre tree into scratch:
```
git archive efb0a8cdb068c5e6e6074e0ffc1d0a63e7218b4f docs/superpowers > t.tar && tar -xf t.tar -C <scratch>
cd <scratch> && git apply --check <case>/ground_truth/reference.diff   # exit 0
cd <scratch> && git apply       <case>/ground_truth/reference.diff     # exit 0
```
Post-apply verification:
- `git hash-object docs/superpowers/plans/agentic-platform/07-agent-step.md` -> `a4092c85bfe1150f01b41d2fddb8f1fdee8b963a` (matches the terminal artifact)
- `wc -l docs/superpowers/plans/agentic-platform/REVIEW.md` -> `230`
- re-running the anchor script on the patched tree: exit 0, **14 ok / 0 MISS**

For completeness, `git apply --check reference.diff` on the POST tree fails (`patch does not apply` on 00-shared-contracts.md / 02-credentials-and-budgets.md) — expected, the diff is already applied there.

- corrected_cmd: the `git init -q .` in the case.yaml drift-guard command is unnecessary — `git apply` works outside a repository, so the guard can stay entirely read-only:
  `git archive <pre_sha> docs/superpowers > $TMP/t.tar && tar -xf $TMP/t.tar -C $TMP && cd $TMP && git apply --check <abs>/ground_truth/reference.diff`
  (also: pipe `git archive` to a FILE then untar; `git archive | tar -x` has produced corrupt trees on this machine.)
- notes: no Postgres, no build, no network.

## Fixup 2026-07-25

Curator-fixup pass over the merged `leakage_audit` (opus: borderline, sonnet:
pass). Every audit item is resolved below into one of the four buckets.

### Real defects — fixed

1. **`task/task.md`, bullet 1 — leading text (opus item 1 of 3).** Removed
   *"Its verification pass changed some findings on the way through — read what
   each finding actually concluded, including its recommended action, and
   respond to that"*. That sentence is the disposition trap (F2/F11/F12) said
   out loud, and disposition is the axis this case exists to measure. The bullet
   now reads: *"The review is an input, not a work order. This repo expects
   review feedback to be received with technical rigor rather than transcribed:
   where you disagree with the review on the merits, say so and act on your own
   analysis. A wrong correction is worse than none."* That is the repo's
   documented `superpowers:receiving-code-review` discipline, so the trigger
   stays authentic; it no longer tells the responder where the trap is. The
   Context section still states that the raw findings went through an
   adversarial verification pass — that is a description of the artifact (the
   review's own §2 preamble and the review commit's subject say it), not a hint.
2. **`task/task.md`, bullet 1 — restraint pre-warning (opus item 2 of 3).**
   Removed *"Do not widen the blast radius beyond what a finding justifies."*
   The restraint axis is now earned by reading §6 ("Defer: everything in §4")
   and §5, as intended.
3. **`task/task.md`, record-keeping bullet (opus item 3 of 3).** Removed the
   trailing *"and so the review itself shows what was done about each finding"*,
   which pointed at `REVIEW.md` — the exact document where the reference wrote
   its §7 answer key. The Deliverable's "per-finding record of what was done and
   why" stays: that is the delivery channel for a graded requirement, and
   grading an unstated deliverable would be unfair. It names no file or format.
4. **Grading caveats recorded (`ground_truth/rubric.md` + `case.yaml`).** Two
   checklist points are stated by the work item and so measure compliance, not
   judgement: restraint's "+1 no file outside `docs/superpowers/`" and
   record-keeping's "+1 per-finding record". Both are annotated in place in
   `rubric.md`, and `case.yaml#grading` now says to report whether the *other*
   four restraint points were earned. Also added a judge note that the
   prescriptive review makes quotation cheap — disposition and right-remedy
   credit requires evidence the responder checked the finding against the plan
   text it cites — and a note that the work item no longer warns about the
   severity-label trap, so a disposition failure is now genuinely the
   responder's.
5. **Manifest/notes inconsistencies.** (a) `curation.learnings` claimed the
   exposed surface was "~1.5 MB of plan markdown across 15 documents"; measured
   at the pinned ref it is **18 files / 2,153,243 bytes**, which is what
   this file already said. Corrected in `case.yaml`. (b) This file's extractor
   pre-check asserted "`case.yaml` still records `validation.status:
   unvalidated`" while `case.yaml` says `validated`; reworded as a historical
   statement. (c) The empty "Formal validation" stub is filled from the
   completed validation pass, with the one genuinely open item (no pilot
   response has been scored against the judge rubric) recorded as open rather
   than quietly marked done.
6. **A construction lesson added to `curation.learnings`** (lesson 3): when the
   same person writes the rubric and the task, rubric axes leak into the task as
   well-meant advice — nothing false, no answer named, invisible to an audit
   scanning for answers, and it converts a judgement axis into a compliance
   axis. Write the task from the trigger alone, then diff it against the rubric.

Re-checked after the edits: the case is still solvable and still honest. The
solver receives the full review (§1–§6) with every finding, its verification
outcome and its recommended change, the whole plan set at the cut, and a
deliverable statement — everything the human who answered this review had. The
edits removed only curator-authored coaching that the real trigger (a spoken
"apply the review") never contained. Nothing in `task/` now names a file, a
finding, a symbol, an approach, or an axis of the rubric.

### Dissolved by the exposure contract — no action

- **`case.yaml` title states the answer** ("nine corrections, two documented
  refutations, and a downgraded severity"), as do the `grading`, `withheld` and
  `pre_state` comments and `ground_truth/`'s filenames. Harness-side by the
  schema's exposure contract: the solver sees pre_state − withheld + `task/`
  only. Not renamed, not retitled. The contract's own consequence applies —
  anyone running this case **by hand** must materialize `task/` into a
  neutrally-named directory and must not open the case directory.
- **`task/review.md` is prescriptive** (a "Recommended change" paragraph per
  finding, plus the ranked §6 action list and the §4 deferral list). Both
  auditors verified it is byte-identical to the in-tree `REVIEW.md` at the cut
  (blob `7df38a45f5`, re-verified in this pass), so it is the authentic work
  item, not a curation leak. This is the declared shape of the case — an
  execute-a-verified-review case, recorded under the schema's
  execute-a-written-plan rule in `curation.learnings`. Sonnet's related note
  (§6 states every disposition in prose, so the trap tests careful reading more
  than independent judgement) is the same observation and is already recorded.
- **In-tree "priced deflator" docs.** None to weigh here: the one in-tree
  document that reveals the remedy *is* the task input, and the six plan files
  the reference leaves untouched were swept clean of fix descriptions during
  curation. `withheld: []` stands. The judge note added above covers the
  associated risk (credit reasoning from the plans, not quotation of the
  review).

### Difficulty — recalibration considered, unchanged

Kept at **hard**, with the reasoning recorded inline in `case.yaml`. Opus's
"~80% of the answer is in the review" is an argument about *which* skill is
tested (execution, disposition, restraint, cross-file coherence — not
diagnosis), not about the height of the bar: 2.15 MB of exposed context, 11
files edited coherently, a three-owner co-signed seam (F1), and two findings
(F2, F12) that score 0 if the responder does the obvious thing. `moderate`
would not be defensible.

### Known leak channel — declared

`known_leak_channels: [project-auto-memory]` added to `case.yaml`. This dev
machine's project auto-memory carries notes downstream of this review
(`project_agentic_platform_end_state.md`, `project_remainder_plans_scoped.md`,
`project_agentic_functions_v3_cleanup.md`) which describe decisions this review
produced. The memory is outside the repo and cannot leak through the exposure
manifest, but it will be in a local Claude Code session's context: any run of
this case here is void unless project memory, session context and conversation
history are suppressed. The memory files themselves were not touched.

### Residual verdict

**pass.** No unresolved exposure problem: the exposed surface is now the review
plus an uncoached work item, the two unavoidable task-stated points are declared
as compliance rather than judgement, and the only operator-side channel is
declared. The open item is validation, not exposure — the judge rubric has never
been exercised against a real response.
