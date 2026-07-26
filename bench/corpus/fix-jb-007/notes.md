# fix-jb-007 — curation record

## Provenance walk

Backed out of a merged fix commit in this repo's jetbridge-era history.

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `6116d379ef0f22a094f28bd37114613e6036d69f` | 2026-07-20T17:51:37-07:00 | `fix(step-dag): failed pre-gates harvest renders one box instead of vanishing` |
| pre_state (parent) | `8ff9f6558fe3777cc4ae0e4d570a19712f4f4569` | 2026-07-20T17:48:55-07:00 | `merge: ux4-wp (W-polish) into ux4-integ-3` |

Verified with `git rev-parse 6116d379^` → `8ff9f65…` (single parent chain; the
parent is itself a merge commit of the UX-4 integration branch, but that is
irrelevant to replay — the pre-state is a tree, not a history).
The terminal commit is reachable from `main`, `origin/main`, `jetbridge` and
`origin/jetbridge` (`git branch -a --contains`), i.e. it shipped.

Change footprint (`git show --stat`): 2 files, +38 / −1.

- `web/elm/src/AgentTickets/StepDag.elm` — +18/−1, of which ~4 lines are logic
  and the rest is an explanatory comment.
- `web/elm/tests/AgentStepDagTests.elm` — +21, one new test case appended inside
  the existing `describe "attempts composition"` block.

**Claim check.** The candidate's `ground_truth_summary` is accurate in every
particular. `harvestBoxes` at the pre-state ends in
`gateBoxes ++ judgeBoxes ++ pushBoxes` with no empty guard; the fix binds that
concatenation to `expanded` and falls back to `[ agentStepBox buildId rm ]` when
it is empty. `stepBoxesFor` routes any row named `harvest` into `harvestBoxes`,
so a harvest with empty results contributed zero boxes — the vanish.

**Domain check.** The four "fails before any gate" paths named in the commit
message are real and all present at the pre-state in `agent/harvest/runner.go`:
`judge config invalid:` (invalid judge), `workspace is not a git repository`,
`workspace-dirty: uncommitted changes present (F33)`, and the `no-op:` guard
(`HEAD == base`). Each returns before the gate engine runs, so `Results.Metadata`
carries no gates, no judge and no pushed branch. The task's symptom list is
quoted from those actual summary strings, which is what a reporter would have
seen in the run-rows table.

**Fifth, unreported path.** `runner.go` also returns `StatusPass` early when push
is disabled and no gates are configured — a *successful* harvest with empty
metadata. It vanishes at the pre-state too. The humans did not report it and did
not test it; it is the basis of the curator-authored discriminator (below).

## Ground truth

- `ground_truth/reference.diff` — `git diff 8ff9f65..6116d37` restricted to the
  two touched paths. No unrelated churn existed in the commit, so the diff is the
  whole commit.
- `ground_truth/AgentStepDagTests.elm` — the test module **verbatim at the
  terminal commit** (human ground truth, 16 tests).
- `ground_truth/AgentStepDagTests.discriminating.elm` — the same module plus ONE
  curator-authored test, clearly marked as such in an in-file comment. Used as
  the primary grading module; see "Discriminating test" below.
- `ground_truth/rubric.md` — behavioral checklist for judge scoring.

## Discriminating test (curator addition — read before trusting the mechanical score)

The merged commit's own test asserts that a *failed* harvest's fallback box has
tone `Bad`. That pins the reported symptom, not the invariant. I built the
obvious wrong fix —

```elm
if List.isEmpty expanded then
    [ { label = rm.stepName, kind = AgentStep, tone = AgentBadge.Bad, ... } ]
else
    expanded
```

— and it passes the human test **16/16 green**. It is wrong: it paints the
succeeded-with-no-facts harvest red.

So `ground_truth/AgentStepDagTests.discriminating.elm` adds
`"a harvest that SUCCEEDED with no gate/judge/push facts renders one box in its
own (non-red) color"`. Validated three ways (see Validation): fails at pre_state,
passes at the terminal commit, fails against the hardcoded-tone mutant. This is a
deliberate departure from "grading material is exactly what the humans wrote" and
is flagged everywhere it is used; the human-only module is retained so a run can
choose the unaugmented gate.

## Leakage analysis

Exposure manifest = repo at `8ff9f65…` + `task/task.md`. Checked:

- **Grading tests in the snapshot** — no. `web/elm/tests/AgentStepDagTests.elm`
  exists at pre_state with 15 tests; neither graded test is among them. The
  module is overwritten at grading time, so the agent cannot see either.
- **Solution in the prompt** — scrubbed. `task/task.md` never names
  `StepDag.elm`, `harvestBoxes`, `agentStepBox`, empty metadata, list
  concatenation, or a fallback. It states only what is visible on the page: the
  chain stops before the harvest, the attempt badge is red, the run-rows table
  shows the harvest failed with one of four summaries, and a harvest that reached
  its gates renders correctly. The real commit message describes the fallback
  exactly — withheld in full.
- **The observational contrast is not scrubbed, by design.** "Early failures
  vanish, gate-reaching harvests render" is half the diagnosis and also the only
  honest way to write the report. Noted in `curation.learnings` as a cap on what
  this case can measure, not as a leak.
- **In-tree plan doc** — `docs/superpowers/plans/agentic-platform/2026-07-19-s1-ticket-step-dag.md`
  is present at pre_state and quotes `harvestBoxes` verbatim (lines ~708ff) plus
  the intended box chain (line ~1330). Grepped it for `disappear|vanish|fallback|
  List.isEmpty|no gates` near the harvest sections: **nothing about the fix**. It
  reproduces the buggy implementation as the reviewed design, so if it biases the
  agent at all it biases *against* seeing the bug. **Decision: exposed, not
  withheld** (`withheld: []`). This is the "design doc present but not a leak"
  shape; a leakage auditor who disagrees should say so rather than assume it was
  missed.
- **Other harvest docs** — `ci/dogfood/FINDINGS.md` and
  `docs/superpowers/plans/agentic-platform/09-harvest-step.md` discuss harvest
  gates and the no-op guard (server side). Neither mentions the step DAG or any
  rendering behavior. Grepped `2026-07-19-ux4-scoping.md` (the audit that spawned
  S-1) for this symptom: not present — the bug was found by eye after the fact,
  so there is no pre-existing finding text that would have been the "real"
  trigger.
- **Same-commit companions** — none. The commit is source + test only.
- **Branch contamination** — replay materializes a tree at `8ff9f65…`; the fix
  and everything after it is unreachable. `web/public/elm.min.js` at pre_state is
  the compiled *old* bundle (regenerated later by a separate chore commit,
  `2cc4c3b675 chore(web): regenerate elm.min.js bundle for ux4-integ-3`) — which
  is why `task.md` can honestly tell the agent to leave the bundle alone.
- **Self-hosted corpus caveat** — `git ls-tree -d 8ff9f65 -- bench` is empty:
  `bench/` did not exist at the pre-state, so the corpus and its answers are not
  inside the exposure manifest.
- **Memorization** — none. Private repo, 2026-07-20, post-cutoff, fork-local
  code (`AgentTickets.StepDag` does not exist upstream).

## Difficulty

`moderate`, at the trivial/moderate boundary. Arguments for trivial: 2 files,
~4 effective lines, and grepping `harvest` under `web/elm/src` lands on the
function immediately. Arguments for moderate: the trigger is purely visual with
no stack trace or failing test to anchor on; the agent must realize the harvest
row is *expanded* rather than rendered, and must derive the fallback's tone from
the row instead of hardcoding it — which the mutant experiment shows is a live
trap, not a hypothetical one.

## Open questions

1. Should curator-authored grading assertions be a standard part of the format?
   This case argues yes (it converted a case that measures "did you touch the
   right lines" into one that measures "did you get the invariant right"), but it
   needs a convention — a naming rule, mandatory in-file marking, and mandatory
   mutant validation — before it is used routinely. Proposal: any curator test
   must ship with a recorded mutant it kills.
2. `${CASE_DIR}` in `grading.setup` is an invented interpolation. The schema does
   not define a `setup` key or a variable convention; a future runner needs both,
   or the swap has to be expressed some other way (the whole-module overwrite is
   unavoidable here because the graded tests share the module's local `row` /
   `byBuild` helpers). *[curator-fixup 2026-07-25]* The same now goes for
   `grading.teardown` and `${WORK_DIR}` (scratch outside the materialized tree),
   added to preserve the agent's own regression test across the overlay. Three
   invented keys and two invented variables is enough evidence that the schema
   needs a real grading-phase vocabulary — `setup` / `teardown` / `grading_caveats`
   plus `${CASE_DIR}` / `${WORK_DIR}` is the concrete proposal this case makes.
3. `elm-test` needs a warm `~/.elm` package cache or network on first compile.
   Fine on this machine; a containerized runner needs the cache baked in.
4. Should the case also assert `elm make --optimize` succeeds (the repo's
   stale-bundle failure mode)? Left out: the terminal commit did not rebuild the
   bundle, so requiring it would grade something the humans did not do.

## Extractor validation evidence

Environment: macOS, `elm 0.19.1`, `elm-test 0.19.1-revision12`, node from
homebrew. Trees materialized read-only with
`git archive <sha> web/elm | tar -x -C <scratch>` (no checkout, no worktree).

| # | Tree | Test module | Result |
|---|---|---|---|
| 1 | terminal `6116d37` | its own (human, 16 tests) | **PASSED** 16/0 |
| 2 | pre `8ff9f65` | human module swapped in | **FAILED** 15/1 — the vanish test |
| 3 | pre `8ff9f65` | pristine (pre-state's own) | **PASSED** 3265/0 (full suite) |
| 4 | terminal `6116d37` | pristine (its own) | **PASSED** 3266/0 (full suite) |
| 5 | pre `8ff9f65` | discriminating (17 tests) | **FAILED** 15/2 |
| 6 | terminal `6116d37` | discriminating | **PASSED** 17/0 |
| 7 | mutant (pre + hardcoded-`Bad` fallback) | human module | **PASSED** 16/0 ← the problem |
| 8 | mutant | discriminating | **FAILED** 16/1 ← discriminator works |

Failure text at pre_state (run 2/5), tier 1:

```
✗ a harvest that failed before any gate/judge/push still renders exactly one harvest box (never vanishes)
    Just ["ticket","implement","harvest"]
    │ Expect.equal
    Just ["ticket","implement"]
```

Tier 2 against the mutant (run 8):

```
✗ a harvest that SUCCEEDED with no gate/judge/push facts renders one box in its own (non-red) color
    Just [Bad]
    │ Expect.equal
    Just [Good]
```

Full-suite runtime: ~15 s cold (elm compile), 0.7–1.0 s warm. No Postgres, no
Docker, no network after the package cache is warm.

`validation.status` is left `unvalidated` in `case.yaml` per the extractor
contract — the runs above are the extractor's own evidence, to be confirmed (or
contradicted) by the validation stage.

> **[curator-fixup 2026-07-25]** Superseded: the validation stage below ran and
> reproduced these counts exactly, so `case.yaml` now reads
> `validation.status: validated`. The anchor `notes.md#validation` resolves to
> that section (this one was retitled to keep the anchor unambiguous).

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `8ff9f6558fe3777cc4ae0e4d570a19712f4f4569`, post `6116d379ef0f22a094f28bd37114613e6036d69f`
- outcome: **validated** (all three legs reproduce the recorded counts exactly)
- toolchain: elm 0.19.1 + elm-test on PATH (/opt/homebrew/bin/elm-test); no Postgres, no network

### PRIMARY fail_to_pass (discriminating module)
`cp <case>/ground_truth/AgentStepDagTests.discriminating.elm web/elm/tests/AgentStepDagTests.elm && cd web/elm && elm-test tests/AgentStepDagTests.elm`

PRE (TEST RUN FAILED, exit 2):
```
✗ a harvest that failed before any gate/judge/push still renders exactly one harvest box (never vanishes)
✗ a harvest that SUCCEEDED with no gate/judge/push facts renders one box in its own (non-red) color
Passed:   15
Failed:   2
```
POST: `TEST RUN PASSED / Passed: 17 / Failed: 0` (exit 0)

### Human-only variant
`cp <case>/ground_truth/AgentStepDagTests.elm web/elm/tests/AgentStepDagTests.elm && cd web/elm && elm-test tests/AgentStepDagTests.elm`
PRE: `✗ a harvest that failed before any gate/judge/push still renders exactly one harvest box` — `Passed: 15 / Failed: 1` (exit 2)
POST: `TEST RUN PASSED / Passed: 16 / Failed: 0` (exit 0)
Confirms the weaker oracle: only the "never vanishes" spec is red, so a hardcoded-Bad fallback would score 16/16.

### pass_to_pass — `cd web/elm && elm-test` (repo's own test files restored)
PRE: `TEST RUN PASSED / Passed: 3265 / Failed: 0`
POST: `TEST RUN PASSED / Passed: 3266 / Failed: 0`

- corrected_cmd: the `cp bench/corpus/...` path in case.yaml is relative to the corpus checkout, which does not exist inside either materialized worktree (bench/ postdates both SHAs — confirmed). Copy from an absolute corpus path instead:
  `cp /<corpus-checkout>/bench/corpus/fix-jb-007/ground_truth/AgentStepDagTests.discriminating.elm web/elm/tests/AgentStepDagTests.elm && cd web/elm && elm-test tests/AgentStepDagTests.elm`
- notes: elm-test exits 2 (not 1) on a failing run; grade on non-zero exit. Full-suite leg ~60s, single-module leg ~10s.

## Fixup 2026-07-25

Curator-fixup pass over the dual-audit findings. Both auditors voted `pass`; the
substantive items were the three "curator notes only" flags in the opus entry,
plus one leak channel neither auditor could see from inside the case directory.

### Real defects — fixed

1. **Grading overlay clobbered the agent's regression test.** `grading.setup`
   `cp`'d the withheld module straight over `web/elm/tests/AgentStepDagTests.elm`,
   which is exactly where an agent following `task.md`'s "please add a regression
   test for this" would have put its test — so the artifact rubric MUST-8 grades
   was deleted before grading. Fixed in `case.yaml`: an explicit four-phase order
   (pass_to_pass on the agent's own tree → preserve → overlay + fail_to_pass →
   restore), a `setup` that backs the module up to `${WORK_DIR}/agent-tests/`
   (deliberately outside `web/elm/tests/`, since elm-test compiles every `.elm`
   under `tests/`), and a matching `teardown`. `ground_truth/rubric.md` MUST-8
   now says to score from the preserved copy or the diff, never from the
   post-overlay tree, and accepts the test living anywhere under
   `web/elm/tests/`. Recorded as the first `grading_caveats` entry.

2. **Tier 1 pinned a box label the report never stated.** The graded assertion
   is `Just [ "ticket", "implement", "harvest" ]`, i.e. it requires the fallback
   box to carry the row's step name — an expectation `task.md` left open. Rather
   than weaken the test (the label is genuinely what the humans shipped), the
   expectation moved into the trigger in observational terms: "Whatever renders
   should be a single box named for the step it stands for — `harvest` — the way
   the other boxes in the chain are named for theirs." That is what a reporter
   would ask for and reveals no mechanism. The residual flexibility is recorded
   as a `grading_caveats` entry and in a new rubric "Grading caveats" section: a
   behaviorally correct fix with a different label is judged on MUST 1/2/3/5,
   not auto-failed on the count.

3. **A constraint telegraphed the tier-2 discriminator.** The bullet "Colors come
   from the shared agent status palette. The existing rule holds: a recording gap
   is a warning marker on a green box, never a red failure" handed over the exact
   answer to the curator-authored succeeded-with-no-facts test (don't paint the
   empty case red). Softened to "Colors come from the shared agent status palette;
   a box in the chain should get its status the same way the rest of the chain
   gets its own." The trap stays live — the reported case *is* a failure, so
   hardcoding `Bad` is still the tempting wrong move — while a careful agent
   retains a fair, mechanism-free basis for deriving the tone. The pre-existing
   green-with-warn rule remains discoverable in the exposed test module, where it
   always was. Also softened the adjacent bullet from "a harvest that *did*
   produce gate/judge/push detail" to "a harvest that got all the way through",
   which says the same thing without asserting the metadata-expansion mechanism.

4. **Notes/manifest consistency.** `notes.md` had two `## Validation` headings —
   the extractor's evidence and the validator's authoritative run — making
   `case.yaml`'s `validation.notes: notes.md#validation` ambiguous, and the first
   one closed by claiming `validation.status` is `unvalidated` while `case.yaml`
   reads `validated`. The extractor section is retitled
   "## Extractor validation evidence" and carries a dated superseded-by note; the
   authoritative section keeps the `#validation` anchor. No counts were touched.

5. **Overlay compile brittleness** (found during the pass, not by an auditor):
   the graded module compiles against `StepDag.attempts` and the `AgentBadge`
   tone constructors, so a fix that renames or relocates that entry point fails
   the overlay at compile time even if it is behaviorally right. Recorded as a
   `grading_caveats` entry and a rubric caveat: inspect the diff before recording
   a mechanical fail.

### Known leak channel — declared

`known_leak_channels: [project-auto-memory]` added. The dev machine's project
auto-memory file `project_agentic_ux_audit_4.md` contains, in its UX-4 program
roll-up, the string **"S-1 DAG dropped failed-harvest boxes (fixed fallback)"** —
the symptom *and* the fix shape, in six words. A solver running on this machine
with project memory mounted has the answer before it reads a line of Elm. Per the
README bullet, memory is not to be changed; the case declares the channel, and a
local hand-run is invalid unless memory is suppressed. (The two auditors could
not have caught this: it is outside the case directory.)

### Dissolved by contract

- The case id/title ("Failed-early harvest vanishes from the ticket step DAG"),
  the `grading` block's test names and observed-count notes, the `note:` fields
  spelling out the tier-1/tier-2 mechanism, and the `ground_truth/` filenames
  (`AgentStepDagTests.discriminating.elm`) all state or strongly imply the answer.
  All are harness-side under the exposure contract — the solver sees only
  pre_state minus `withheld` plus `task/` — so nothing was renamed or retitled.
  The one operational duty that follows is the schema's own: a hand-run must
  materialize `task/` into a neutrally-named directory.
- `withheld: []` is unchanged. The in-tree S-1 plan doc
  (`docs/superpowers/plans/agentic-platform/2026-07-19-s1-ticket-step-dag.md`)
  stays exposed under the priced-deflator default: it is authentic pre-state
  history, it quotes the *defective* `harvestBoxes` as the reviewed design, and it
  says nothing about the fix — it biases toward believing the buggy code correct.
  The rubric now instructs the judge to credit causal reasoning from code and data
  evidence rather than doc quotation, and to give no credit for deferring to that
  document as proof of correctness.

### Difficulty

Held at `moderate`. No auditor argued for recalibration, and the extractor's own
trivial/moderate argument still resolves the same way: the localization is cheap
(one grep for `harvest` under `web/elm/src`), but the trigger is purely visual
with no stack trace, and the hardcoded-tone trap is demonstrated — a validated
mutant scores 16/16 against the human test. That is more than a trivial case
asks. Note the two soft-pedalled task edits above pull in opposite directions
(the label sentence makes tier 1 easier to satisfy; removing the recording-gap
hint makes tier 2 harder), so the net difficulty is unchanged.

### Not applicable

No decline/negative delivery channel is needed — this is a `merged` small-fix
case whose deliverable is the code change itself, graded mechanically.
