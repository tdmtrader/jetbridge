# review-jb-004 — curation record

## Provenance walk

Terminal artifact: **`793659c7fad09135eabb44420006414d1f1b0c5f`**
`fix(atc)+fix(web): harden C3 linkage; correct build-switch cost staleness`
(2026-07-18T12:17:57-07:00).

Its body is the oracle, verbatim in the relevant part:

> Adversarial review of the previous two commits confirmed two majors:
> - C3 trusted caller-writable `agent_tickets.pipeline_run_id` (F30 id class): a
>   `tickets:write` principal could point a ticket at any victim run and the
>   lifecycler would archive that victim's template forever. …
> - The once-per-build agent fetch guard was unreachable on in-app build switches
>   (Header stamps `model.id` before `BuildFetched` arrives), so build B rendered
>   build A's spend. …

"the previous two commits" resolves against `git log --oneline -8 793659c7fa`:

```
793659c7fa  fix(atc)+fix(web): harden C3 linkage; ...        <- terminal artifact
c4d9fcb914  feat(atc): archive terminal agent-ticket pipelines (C3/F3)
d3daa058c4  feat(web): agent cost chip on the build page (D1/F10)
dbc0545029  docs(plans): remainder plans for the 5 next tracks + ticket manifest
```

So the reviewed change is `dbc0545029..c4d9fcb914` and the reviewer's tree is
`c4d9fcb914`. Timeline: base 11:37:58 → web commit 11:41:12 → atc commit 11:49:37
→ remediation 12:17:57. A 28-minute review window; the case is genuinely
"review this before it goes out", not a retrospective.

`dbc0545029` is a docs-only commit, which makes it a clean base — the exposed diff
contains no unrelated churn from it.

### Each finding verified against the pre-state tree, not just the commit message

**F1 — cross-tenant destructive archival.** Verified end to end at `c4d9fcb914`:

- `atc/db/pipeline_run_factory.go:485-497` — `terminalTicketLinkage()` joins
  `agent_tickets t` → `pipeline_runs r0 ON r0.id = t.pipeline_run_id`, filtered
  only on `t.state IN (terminal…)`. Nothing ties `r0.template_pipeline_id` back to
  `t`.
- Consumers: `RunsForTerminalTickets()` (`:500-521`) and
  `TemplatesForTerminalTickets()` (`:523-544`).
- Destructive consumer: `atc/runlifecycle/lifecycler.go` `Run()` — the two loops
  added by this very commit call `run.Archive()` and `pipeline.Archive()` with no
  further checks, on the component's 10s pass.
- Write path: `agent/api/tickets/handler.go:251-268` `TransitionTicket` copies
  `req.PipelineRunID` straight into `TransitionMeta`; `atc/db/agent_tickets_factory.go:239-240`
  writes it verbatim (`q.Set("pipeline_run_id", *meta.PipelineRunID)`). No
  validation anywhere on that path.
- The fix (`793659c7fa`) adds `JOIN pipelines p0 … JOIN teams tm0 …
  AND p0.name = 'agent-ticket-' || t.id::text AND tm0.name = ?` with
  `atc.DefaultTeamName` appended to args — exactly the pin the oracle describes.

**F2 — build-switch cost staleness.** Verified end to end at `c4d9fcb914`:

- `web/elm/src/Build/Build.elm:682-694` — the guard
  `if not model.hasLoadedYet || build.id /= model.id`. `d3daa058c4` added
  `FetchBuildAgentMetrics build.id` inside it (the guard itself pre-dates the
  change; confirmed present at `dbc0545029:web/elm/src/Build/Build.elm:672-682`
  with only the reviews fetch inside).
- Cause: `web/elm/src/Build/Header/Header.elm:347-379` `changeToBuild` sets
  `updatedModel = { model | id = b.id, … }` from `model.history` — before build
  B's `BuildFetched` lands. `Build.changeToBuild` delegates to it at
  `Build.elm:201`. So at `handleBuildFetched` time `build.id == model.id` and
  `hasLoadedYet` is `True` → guard false → no refetch.
- No clearing: `Build.changeToBuild` (`:185-190`) resets `prep`, `output`,
  `autoScroll`, `highlight` and nothing else; `agentRunMetrics` survives.
- `BuildAgentMetricsFetched (Ok rows)` (`:332-333`) assigns unconditionally
  though `Concourse.Agent.RunMetric` carries `buildId` (F2b).

**F3 — templates hold-back asymmetry.** `RunsForTerminalTickets` has
`Where(sq.NotEq{"r.status": string(PipelineRunRunning)})`;
`TemplatesForTerminalTickets` has no equivalent. The reviewed change's own spec
encodes the asymmetry as intended — "the template itself never runs builds, so it
archives right away". The fix adds the mirrored `NOT EXISTS` and rewrites that
spec to "holds back both the run and the template while the run is still
aggregate-running".

Verdict: the candidate's claims hold under verification. Nothing was overstated.

## What the case exposes

- `pre_state.repository` = `c4d9fcb914` (full tree, change applied).
- `task/task.md` — scrubbed review request (see below).
- `task/change.diff` — `git diff dbc0545029..c4d9fcb914` minus three generated
  files. 841 lines, 12 files, 548 insertions.

## Leakage analysis

### Withheld (never exposed)

- **The terminal artifact `793659c7fa` and every descendant.** Its body states
  both majors in prose; its diff contains both fixes and the comments explaining
  them (`atc/db/pipeline_run_factory.go`'s new comment literally describes the
  attack). This is the whole case. The pre_state ref is the reviewed tip, so no
  descendant is reachable.
- `ground_truth/reference.diff` — the remediation, `c4d9fcb914..793659c7fa`
  (bundles excluded).
- `ground_truth/withheld_tests/` — the post-fix `BuildTicketBarTests.elm` and
  `pipeline_run_factory_test.go`. These exist *only* after the cut, so they are
  not in the exposure manifest at all; they live here for
  `ground_truth_validation` and must never be copied into a tree the agent under
  test can see.

### Considered and deliberately NOT withheld

- **`atc/db/pipeline_run_factory.go:42-64` — the `RunBelongsToPipeline` /
  `TicketBelongsToRun` doc comments**, present since before `dbc0545029`
  (verified at that sha). They say in prose that `AGENT_PIPELINE_RUN_ID` and
  `AGENT_TICKET_ID` are "attacker-writable plan env (F30)" and that server-side
  linkage is required before spending privilege on them. This sits twenty lines
  above the code under review. It is a strong hint toward F1 — and it stays,
  because it is the codebase being reviewable. A reviewer who reads the file it
  is editing sees the precedent; that is exactly the behavior the benchmark
  should reward. Withholding it would make the case measure clairvoyance instead
  of thoroughness. Recorded here so a leakage auditor sees it was a decision, not
  an oversight.
- **`docs/superpowers/plans/agentic-platform/REVIEW.md`** — the in-tree findings
  register, present at pre-state, contains an entry "F30 | major | … per-template
  run NUMBER, not `pipeline_runs.id`". That is a *different* defect (id/number
  conflation) sharing the label the remediation commit later borrows ("the F30 id
  class"). It does not describe caller-writability of the column and says nothing
  about archival. Not a leak; left exposed.
- **`docs/superpowers/plans/agentic-platform/11-dispatch.md`** documents the
  `agent-ticket-<id>` naming convention (`:61`, `:1270`). That is the shape of
  F1's reference fix, but it is also load-bearing product documentation that
  predates the change by ten days, and the convention already appears three times
  inside the exposed diff itself (`Build.elm` parses ticket ids out of pipeline
  names; the new factory comment names it). Suppressing it would be theatre.
- Searched and found **nothing** at pre-state describing either defect:
  `git grep` over `docs/` for `RunsForTerminalTickets|TemplatesForTerminalTickets|TerminalStates`
  → zero hits; `C3` → only `CONVENTIONS.md` "C3. ADD, never REPLACE" and an
  unrelated `Task C3` in the delivery-outcomes remainder; `ci/dogfood/FINDINGS.md`
  → nothing about archival, the cost chip, or `pipeline_run_id`. The UI audit that
  numbered "C3/F3" is not in-tree.
- **`bench/` does not exist at `c4d9fcb914`** (verified: `git ls-tree c4d9fcb914 bench`
  is empty; the corpus starts 2026-07-25). No self-hosted-corpus contamination —
  the schema's §"Self-hosted corpus caveat" is satisfied.

### Scrubbing applied to `task/task.md`

The trigger is reconstructed, not recovered — there is no preserved review
request; the real one was a chat instruction. Two drafts:

- **Draft 1 (discarded)** asked for "anything a caller can do to this that the
  author did not intend" and "state that outlives the thing it describes… users
  move around inside the SPA without a reload", and pointed at
  `web/elm/src/Build/Header/` as the home of the history strip. Those three
  phrases map 1:1 onto F1, F2 and F2's cause. Discarded as solution-in-the-prompt.
- **Draft 2 (shipped)** replaces them with a five-lens checklist —
  security/authorization, correctness outside the happy path, silent-over-loud,
  contracts with attached code, do the tests pin the claims — which is what an
  adversarial review request in this repo genuinely looks like and which covers
  the findings without pointing at them. The `Build/Header/` pointer was removed;
  the "main is the default team" line was removed. The remaining repository
  context (where the ticket HTTP surface lives, that the lifecycler runs
  unattended on 10s) is orientation a reviewer would have and does not name a
  defect.

The generated-file exclusion is disclosed in the task so the reviewer does not
waste effort hunting for an omission.

## Difficulty rationale

`hard`. Mechanical proxies say moderate — 12 files, 548 added lines, one SQL
helper and one Elm `let` binding. The difficulty is that **neither defect is
visible inside the diff**:

- F1 requires leaving the diff for `agent/api/tickets/handler.go` to learn the
  column is caller-written, then recognising that this is the first *destructive*
  consumer of a column whose previous consumers were all read-only checks.
- F2 requires reading `Build/Header/Header.elm`, which the diff never touches, and
  reasoning about the order of `changeToBuild` versus `BuildFetched`.
- F3 requires disbelieving a green test that asserts the wrong invariant.

A reviewer who only reads the diff finds none of the three.

## Open questions

- **Port type name.** `review-findings/v1` is invented here; the v3 snapshot type
  registry does not exist yet (`git grep` for `repository-change|work-item/v1|findings/v1`
  over `docs/superpowers/specs/` and `agent/` returns nothing — the schema doc's
  examples are illustrative). First review case in the corpus, so this name will
  become the de facto convention; flag it if a real registry lands with a
  different one.
- **How should `change` be materialized by a future harvest adapter?** This case
  ships the diff as a file *and* pins base/tip SHAs, so an adapter can regenerate
  it. If the adapter materializes a repo with history, the file is redundant; if
  it materializes a bare tree, the file is the only way the reviewer sees the
  change. Both are supported deliberately.
- **Precision scoring for reviews is unsolved.** The oracle is one 28-minute
  review; the `non_findings` list is my attempt to give a judge *some* traps to
  score precision against, but it is curator-authored, not human-observed. If
  several runs surface the same unmatched-but-real finding, extend
  `expected_findings.yaml` rather than scoring it as noise (recorded in
  rubric.md §"Unmatched findings").
- Should a review case also grade the *proposed remediation*? `reference.diff` is
  present and would support it, but the workflow's output port is findings. Left
  as a judge SHOULD in the rubric (acceptable/not-acceptable remediation lists)
  rather than a scored dimension.

## Validation plan

_Written at curation time; the results are in §Validation below._

Ground-truth validation commands live in `case.yaml` under
`grading.ground_truth_validation`. Both must show `fail-at-pre, pass-at-post`:

- `V1-elm-build-switch` — no Postgres, no Docker; `elm` and `elm-test` confirmed
  on PATH at curation time (`/opt/homebrew/bin/elm-test`). Expect the 4 added
  specs to fail at `c4d9fcb914`.
- `V2-db-linkage-pin` — needs a local PostgreSQL per `CLAUDE.md`. Swap the whole
  `Describe("RunsForTerminalTickets / TemplatesForTerminalTickets")` block; the
  withheld file also re-points three pre-existing specs at a correctly-named
  `agent-ticket-7` template on the main team, so cherry-picking individual specs
  will not compile a meaningful comparison.

Neither command grades the agent. They prove the oracle describes real defects.

| date | who | check | result |
|---|---|---|---|
| | | | |

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `c4d9fcb91412ae3be80c34a7e6d1fedf3f9bb355`, post `793659c7fad09135eabb44420006414d1f1b0c5f`
- outcome: **validated** (both ground-truth gates V1 and V2; these validate the case, they do not grade the agent)

### V1-elm-build-switch (F2 / F2b / F2c)
`cp <case>/ground_truth/withheld_tests/web/elm/tests/BuildTicketBarTests.elm web/elm/tests/BuildTicketBarTests.elm && cd web/elm && elm-test tests/BuildTicketBarTests.elm`

PRE (TEST RUN FAILED, exit 2) — `Passed: 6 / Failed: 4`, i.e. exactly the four added specs:
```
✗ refetches agent metrics when switching builds through history
✗ clears the previous build's spend when switching builds
✗ drops a late metrics response that belongs to the previous build
✗ refetches agent metrics when a live-watched build finishes
```
POST: `TEST RUN PASSED / Passed: 10 / Failed: 0` (exit 0)

### V2-db-linkage-pin (F1 / F3) — requires PostgreSQL
`cp <case>/ground_truth/withheld_tests/atc/db/pipeline_run_factory_test.go atc/db/pipeline_run_factory_test.go && ginkgo --focus="RunsForTerminalTickets / TemplatesForTerminalTickets" ./atc/db/`
(`pg_isready` -> `/tmp:5432 - accepting connections`; ginkgo 2.27.3 on PATH matches go.mod)

PRE (FAIL, exit 1):
```
Summarizing 2 Failures:
  [FAIL] PipelineRunFactory RunsForTerminalTickets / TemplatesForTerminalTickets
         [It] holds back both the run and the template while the run is still aggregate-running          (F3)
  [FAIL] PipelineRunFactory RunsForTerminalTickets / TemplatesForTerminalTickets
         [It] never selects a pipeline that is not the ticket's own agent-ticket template
              (poisoned pipeline_run_id, F30)                                                            (F1)
Ran 5 of 1120 Specs in 1.731 seconds
FAIL! -- 3 Passed | 2 Failed | 1115 Skipped
```
POST (PASS, exit 0):
```
Ran 5 of 1120 Specs in 1.606 seconds
SUCCESS! -- 5 Passed | 0 Failed | 1115 Skipped
```
The focused run is cheap (~2s of specs on top of suite setup) — the whole-file swap is required, as documented, because three pre-existing specs are re-pointed at the correctly-named `agent-ticket-7` template.

- corrected_cmd: none; `${CASE_DIR}` must be an absolute path into the corpus checkout.
- notes: V1 needs elm/elm-test only; V2 needs local PostgreSQL.

## Fixup 2026-07-25

Resolution pass over the dual leakage audit (opus = borderline, sonnet = fail).
Every audit item below is bucketed, then either fixed or dissolved.

### Dissolved by the exposure contract — no edit

The solver sees `pre_state` minus `withheld` plus `task/`, and nothing else in
this directory (`schema/benchmark-case-v1.md` §"The exposure contract"). Nothing
was renamed or retitled for any of these:

- **sonnet: `source.terminal` names the remediation SHA.** `case.yaml` is
  harness-side; pinning the terminal artifact is the schema's purpose.
- **sonnet + opus: the finding slugs state each defect outright**
  (`F1-linkage-unpinned`, `F2-cost-chip-stale-on-build-switch`, and the
  `precision_traps` list). Grading configs may state the answer freely.
- **sonnet + opus: `grading.ground_truth_validation` prose describes the withheld
  assertions**, including the `agent-ticket-7` / main-team spec detail. Those
  commands operate on `ground_truth/withheld_tests/`, which is never exposed; the
  block already carries "never expose the withheld_tests/ tree to the agent under
  test".
- **sonnet: `curation.learnings` is a prose walkthrough.** Also harness-side; the
  corpus wants the walkthrough.
- **opus: "an answer key that must never be surfaced."** Agreed as an operational
  constraint, not a case defect. Recorded in `curation.learnings`: a hand-run must
  materialize `task/` into a neutrally-named directory and must not put
  `case.yaml`, `notes.md`, or `ground_truth/` in the solver's context. The case id
  itself (`review-jb-004`) is likewise harness-side.

### Real defects — fixed

1. **`task/task.md` lens 2 named a bonus finding's mechanism** (opus). "…
   concurrency, restarts, retries, partially-migrated databases, out-of-order or
   late responses, whatever applies" enumerated F2b's exact mechanism (a late
   metrics response for the previous build) and simultaneously aimed the reviewer
   at the N1 `to_regclass` precision trap — inviting a finding the rubric then
   penalizes. Replaced with a non-enumerating ask: "A real deployment is not a
   single-threaded walkthrough; consider what it does to this code that a manual
   exercise does not." The lens survives; only the checklist of answers is gone.
2. **`task/task.md` lens 5 described F3's shape** (opus). "Do the added specs pin
   the behavior the commit messages claim, or only the shape the implementation
   happens to have?" is a prose description of the F3-encoding spec. Replaced with
   "Judge the added specs as evidence: how much of these two commits do they
   actually establish?" Still a genuine test-quality lens — a coverage question,
   not a hint that some spec encodes the wrong invariant.
3. **`task/task.md` repository context said the lifecycler runs "unattended".**
   That word is rubric leg 3 of F1 verbatim. Dropped; the 10s interval and the
   component path stay, because that is orientation any reviewer would have. The
   two remaining pointers (`agent/api/tickets`, `atc/db/agent_tickets_factory.go`)
   were re-examined and kept: they name where a subsystem lives, not that anything
   is wrong with it, and F1 still requires the reviewer to connect writability →
   unconstrained linkage → destructive consumer. Both auditors passed them.
4. **Duplicate `## Validation` heading in this file.** `case.yaml`'s
   `validation.notes: notes.md#validation` anchored to the empty stub rather than
   the recorded results. The stub is now `## Validation plan`; the results section
   keeps the anchor.

### Priced-deflator docs — KEEP, with a rubric guard

The two in-tree items that partially reveal F1 stay exposed, as originally
decided (§"Considered and deliberately NOT withheld"): the
`RunBelongsToPipeline` / `TicketBelongsToRun` doc comments in the file under
review, and `docs/superpowers/plans/agentic-platform/11-dispatch.md`'s
`agent-ticket-<id>` convention. Neither collapses the task — the comments say the
ids are attacker-writable but nothing about archival, and the convention doc
describes a naming rule that already appears three times inside the exposed diff.
Authenticity wins. Added a new `ground_truth/rubric.md` section, **"Evidence, not
quotation — the in-tree deflators"**, instructing the judge to credit the causal
chain built from evidence and *not* the quotable doc: paraphrasing the
"attacker-writable plan env (F30)" comment is not F1 on its own, and proposing the
`agent-ticket-<id>` pin without saying what it bounds is partial credit at most.

### Difficulty

Unchanged at **hard**, reconsidered rather than assumed. Neither auditor argued
for a different value, and the three-part rationale above (§"Difficulty
rationale") holds after the task edits — if anything the lens-2 and lens-5 scrubs
push it slightly harder. Mechanical size still says moderate; the discriminator is
that neither required defect is visible inside the diff.

### Known leak channel

`known_leak_channels: [project-auto-memory]` added to `case.yaml`. Verified
directly on this host: the project auto-memory file `project_agentic_ui_wave.md`
contains an entry for the terminal artifact `793659c7fa` that states both required
findings in the oracle's own vocabulary — caller-writable
`agent_tickets.pipeline_run_id` letting a `tickets:write` principal archive any
victim template, and `Header.changeToBuild` stamping `model.id` before
`BuildFetched` arrives so build B renders build A's dollars. It also names the
reference fixes. Neither auditor caught this (both audited the case directory, not
the environment). Memory is not ours to edit; the mitigation is that a replay
harness must not mount project memory, session context, or conversation history
into the solver, and a local hand-run on this machine is invalid without
suppression.

### Manifest consistency checks

- `information_cut: 2026-07-18T11:49:37-07:00` == committer date of
  `pre_state.repository` ref `c4d9fcb914` (verified by `git log`). Base
  `dbc0545029` 11:37:58, remediation `793659c7fa` 12:17:57 — all consistent with
  the 28-minute review window described above, and with task.md's "stacked on
  `main` and not yet pushed" framing. No date reframing needed.
- No `fail_to_pass` / `pass_to_pass` gate exists to collide with the task: this is
  a `reference`-rubric review case whose only mechanical commands are
  `ground_truth_validation`, which validate the oracle and do not grade the agent.
  The task does not ask the agent to write tests, so nothing an agent produces can
  be clobbered by the withheld-test overwrite.
- Delivery channel: the workflow's output port is `findings: review-findings/v1`
  and `task.md` already specifies the required per-finding shape (what breaks,
  file + function/query, concrete sequence, major/minor). No decline channel is
  needed — this is not a negative case.
- `withheld: []` re-confirmed correct: the grading material post-dates the cut and
  lives only in `ground_truth/`; `bench/` does not exist at the pre_state ref
  (re-verified: `git ls-tree c4d9fcb914 bench` is empty).

Residual verdict: **pass**. No unresolved exposure problem inside the case; the
one live leak is environmental and declared.

## Retarget 2026-07-30

The output port type changed from the curator placeholder `review-findings/v1`
to the registered `review/v1`. Nothing exposed changed: `case.yaml` is
harness-side, `task/` and the pre-state are byte-identical, and
`ground_truth/expected_findings.yaml` is untouched. No results existed against
this case at the time of the change, so the corpus-versioning rule in
bench/README.md is satisfied. Any result must cite the corpus commit it ran
against.
