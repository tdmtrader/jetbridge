# notes — review-jb-003

## Provenance walk

Terminal artifact: `20b808a92e977f38c9b903f3c103a76c389c912d`
(`fix(web): three bugs from adversarial review of the UX wave`,
2026-07-18T22:22:50-07:00, Thomas Moore, co-authored by Claude Fable 5). Its
message enumerates the three findings with the reviewer's own severity labels
(HIGH / MEDIUM / LOW) and closes by flagging a fourth, pre-existing issue as a
separate follow-up. That message IS the ground truth; the case is the review
that produced it.

Resolved and checked, not trusted:

- `git rev-list --parents -n 1 20b808a92e…` → single parent
  `199ab7497399aa157065b660537caa652373791c`
  (`build(web): rebuild elm.min.js + main.css bundle for the agentic-UX wave`,
  2026-07-18T21:55:30-07:00). That parent is the branch state the reviewer saw:
  the last commit of the wave before the review's fixes. It is the `after` input.
- `AGENTIC_UX_WAVE_2_SCOPE.md` at that ref opens with "Branch
  `agentic-ux-wave-2` off `jetbridge@54b541a81e`", and
  `git rev-list --parents -n 1 4f64b29745` (the first wave commit) confirms
  `54b541a81e6235ca74256dfbd50666ec45a18d2c` is its parent. `git branch
  --contains 54b541a81e` shows it on `jetbridge`. So `before` is a real mainline
  branch point, not a reconstruction.
- `git log 54b541a81e..199ab749` → exactly 9 commits (2 server-side halves,
  6 Elm slices, 1 bundle rebuild); `git diff --stat` excluding the generated
  bundles → 31 files, +1798/−229. That is the reviewable surface.
- Lineage after the cut: `199ab749 → 20b808a9 → … → e366c29a13`, merged to
  jetbridge as `e62a2f8c2a` (tag v0.2.194). The fix is an ancestor of the merge,
  so taking the MERGE commit's first parent as `after` would have been wrong —
  it would have contained the answers. Backing out from the fix commit and
  taking *its* parent is the correct recipe.

Each of the three findings was verified against the pre-state tree rather than
read off the commit message:

- **F1** — `AgentTicket.elm` at `after`: `handleDelivery` fires
  `ClockTicked FiveSeconds → FetchAgentTicket` (lines 213–220, subscription at
  326–328), and `handleCallback`'s `AgentTicketFetched (Ok …)` branch (130–143)
  re-seeds `editTitle`/`editBody`/`editBudget` from the payload with no guard on
  `model.editing`. `ClickAgentTicketSave` (248–259) submits those same buffers.
  The chain is real, including the silent-successful-save leg.
- **F2** — `AgentBadge.runOutcome` (275–301) matches `buildStatus` first;
  `"started" → Running` short-circuits before `fromRunStatus` can map
  `"parked" → AwaitingHuman`. The enabling fact — that a parked step
  deliberately keeps its build `started` — is in the repo at the cut
  (`docs/superpowers/plans/agentic-platform/00-shared-contracts.md:166`,
  `03-pipeline-runs.md:22,50`), so the finding is derivable from the exposed
  snapshot alone. Two call sites affected (`Agent.elm:743`,
  `AgentTicket.elm:1106`).
- **F3** — `Agent.elm:643` `List.indexedMap (runRow zone expandedRuns) runs`
  passes the ordinal; `AgentRunExpandToggled` (339–351) stores it in a
  `Set Int`; the page refetches on a timer (362–390) and the list is
  newest-first.
- **D1** (deferred) — `Build/AgentReview.elm` keys `expandedFindings`,
  `expandedDescriptions`, the note box and the verdict POST off `finding.id`,
  and the same file already special-cases `finding.id == ""` when rendering the
  anchor id, so blank ids are known to occur. Present identically at `before`
  (`54b541a81e:web/elm/src/Build/AgentReview.elm:17,202,254`), which confirms the
  reviewer's "pre-existing" call. Fixed separately in `9e1fbf99e1`.

One correction to the candidate's summary, carried into the ground truth: F3's
commit message blames "a 5s refetch", but the `/agent` page subscribes to
`OnClockTick OneMinute` (`Agent.elm:390`) — the 5s cadence belongs to the ticket
page. The defect is unchanged; the rubric explicitly forgives "60s" or
"periodic". Similarly, F1 is more precisely *aggravated* than *introduced* by
this branch: the unconditional re-seed exists at `before` too
(`54b541a81e:…/AgentTicket.elm:125–138`), paired with a one-minute clock. The
branch's move to five seconds is what turns it into constant, load-bearing data
loss. Both attributions are credited.

## Mechanical verification (ground truth, not agent grading)

The fix commit ships a regression test per finding, so the ground truth is
checkable. Materialized `web/elm` at each ref with `git archive` (no checkout, no
ref mutation) and ran `elm-test` (elm 0.19.1, elm-test 0.19.1-revision12, both on
PATH; no Postgres, no network).

| tree | command | result |
|---|---|---|
| `199ab749` (reviewed state), own tests | `elm-test` (full suite) | PASSED, 3123 |
| `199ab749` + the 3 test files from `20b808a9` | `elm-test tests/AgentBadgeTests.elm tests/AgentPageTests.elm tests/AgentTicketPageTests.elm` | **FAILED — 59 passed, 4 failed** |
| `20b808a9` (fix), same 3 modules | same | PASSED, 63 |
| `20b808a9` (fix), full suite | `elm-test` | PASSED, 3126 |

The 4 failures are exactly the specs for the 3 findings (2 for F2, 1 for F3, 1
for F1), and 3126 matches the count the commit message claims. So all three
findings are demonstrated defects, not reviewer opinion — which is the whole
reason to prefer review terminal artifacts that ship tests.

Caveat for anyone reusing these commands: `AgentPageTests.elm` is a MODIFIED
file, not a new one (the assertion changes from `AgentRunExpandToggled 0` to
`AgentRunExpandToggled 100`, the sample run's build id). Validation must replace
the file wholesale, never merge.

## Leakage analysis

**Withheld: nothing.** Two greps over the exposed refs found no path that
describes these findings: `git grep -i "clobber|edit form|unsaved|ordinal"` over
`*.md` at `199ab749` returns only unrelated hits — "edit form" and "unsaved" have
zero hits anywhere; "ordinal" hits once, in a DB runbook's `ordinal_position`
SQL; "clobber" hits 27 times across 7 agentic-platform plan documents, every one
of them about metrics rows, harvest-seeded shas, content hashes or parallel
harvests, none about UI state.
`docs/superpowers/plans/agentic-platform/UX.md`
is an older, pre-UI UX assessment (B1/B2/m1-style findings about navigation and
IA) and contains none of F1–F3. Hence `withheld: []`.

**Deliberately exposed — `AGENTIC_UX_WAVE_2_SCOPE.md`.** The branch's own
scope-and-sequencing note is at the root of `after`. It lists the slices, the
deferred items and their rationale, and ends with "adversarial review workflow at
the end". It gives the reviewer the same orientation the human reviewer had, and
it does not name a single one of the findings. Keeping it is both faithful and
useful; scrubbing it would make the case harder than reality, not fairer.

**Deliberately exposed — the parked-run contract.** `00-shared-contracts.md:166`
states outright that a parked step keeps its build `started`. That is the fact
F2 turns on. It is *not* the finding (nothing anywhere says the render rule gets
it wrong), and a reviewer who does not go looking for the contract will not trip
over it. This is the case's fairness guarantee: F2 is solvable from the exposed
snapshot alone.

**Deliberate task instruction, flagged for the auditor.** `task/task.md` asks the
reviewer to label each finding "introduced here or pre-existing". That is a mild
nudge — without it, D1 would not be gradable at all, since nothing else would
prompt a reviewer to attribute provenance — and it points at no file, page or
defect. It also matches what the real reviewer did. Judged worth the nudge; an
auditor who disagrees should downgrade D1 to a bonus rather than rewrite the
task, because the exposed content must stay stable once results exist.

**Withheld by construction — everything after the cut.** `20b808a92e` (whose
message enumerates all three findings verbatim), `9e1fbf99e1` (the deferred D1
fix), `e366c29a13` (later merge-gate audit fixes) and the merge `e62a2f8c2a` are
all descendants of `after` and unreachable from it. This only holds if the
harness materializes a SNAPSHOT at the pinned ref — `git archive`, an export, or
a shallow single-ref clone. A full clone with all branches present hands the
agent `git log --all` and the entire answer, including the fix commit's message.
Flagged loudly here because this is the first review case in the corpus and the
first one where the terminal artifact is a direct descendant of the exposed ref.

**Memorization: none.** Private jetbridge-era history, 2026-07-18, after the
model cutoff; the modules do not exist upstream. The *classes* of bug (polling
overwrites form state, list state keyed by index, precedence bug in a status
fold) are textbook, which is the point — a good reviewer should find them. What
is not memorizable is that these three, in this diff, are the ones that matter.

**Self-hosted corpus caveat.** `git ls-tree 199ab749 bench` is empty: `bench/`
did not exist until 2026-07-25, a week after the cut. The corpus and its answers
are not reachable through the exposure manifest.

## Schema fit (worth resolving before the next review case)

`benchmark-case/v1` assumes a `work-item` input port; the platform's real
`code-review-v3` seed
(`agent/workflow/seeds/code-review-v3/workflow.yml`) has none — its signature is
`(before: repository/v1, after: repository/v1) -> (review: review/v1)`, and the
review request lives in the workflow's own prompt file. This case follows the
seed exactly and exposes `task/task.md` as the request framing without declaring
it a port. A harvest adapter therefore cannot derive the task text from a port
binding for review cases; it needs a rule ("task/ is the prompt context") or the
schema needs an explicit `exposed:` section separate from `pre_state:`.

## Open questions

- **Precision scoring is the weak half.** Recall against three known findings is
  solid; precision against a three-item human oracle is not — the README's own
  warning. `expected_findings.yaml` lists three plausible true findings the human
  did not report (polling amplification being the most likely), but that list is
  a judgement call by the extractor, not evidence. Consider scoring precision
  only as "unsupported findings per finding", never as "not in the oracle".
- **Is D1 gradable as stated?** It rests on the reviewer both reporting the
  blank-id collision and attributing it correctly. If agents routinely report it
  without attribution, the scoping signal degrades to a second recall item. Worth
  watching across runs before leaning on it.
- **Severity vocabulary.** The human wrote HIGH/MEDIUM/LOW; `review/v1` uses
  `critical|high|medium|low`. The task asks for the schema vocabulary and the
  rubric allows one step of drift. If agents cluster everything at `high`, the
  ranking axis will need an explicit forced ordering instead of severity labels.
- **Diff size vs review budget.** 31 files / +1798 lines is small for a human
  reviewer and large for a single-shot agent context that also has to read the
  base state. If results show findings clustered in the first files read, the
  case is measuring context budget rather than review skill — split-by-slice
  variants would isolate that.

## Validation

_(stub — filled by the validation stage)_

- [ ] ground-truth validation reproduced in the harness environment
      (4 targeted failures at `after` with the fix commit's tests; 3126 green at the fix)
- [ ] snapshot materialization confirmed to hide descendants of `after`
- [ ] leakage audit (two independent models)
- [ ] a baseline agent run, to check the case is neither trivially passed nor unsolvable

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `199ab7497399aa157065b660537caa652373791c`, post `20b808a92e977f38c9b903f3c103a76c389c912d`
- outcome: **validated** (both legs, numbers match the recorded observations exactly)
- toolchain: elm 0.19.1 + elm-test on PATH; no Postgres, no network

### Ground-truth gate (F1/F2/F3 are real defects)
setup on the pre tree (wholesale replacement, never a merge):
```
git show 20b808a92e977f38c9b903f3c103a76c389c912d:web/elm/tests/AgentBadgeTests.elm      > web/elm/tests/AgentBadgeTests.elm
git show 20b808a92e977f38c9b903f3c103a76c389c912d:web/elm/tests/AgentPageTests.elm       > web/elm/tests/AgentPageTests.elm
git show 20b808a92e977f38c9b903f3c103a76c389c912d:web/elm/tests/AgentTicketPageTests.elm > web/elm/tests/AgentTicketPageTests.elm
```
cmd: `cd web/elm && elm-test tests/AgentBadgeTests.elm tests/AgentPageTests.elm tests/AgentTicketPageTests.elm`

PRE (TEST RUN FAILED, exit 2) — `Passed: 59 / Failed: 4`:
```
✗ a PARKED run shows Waiting on you even though its build is still 'started'        (F2)
✗ a parked run whose build later succeeded still shows Waiting on you, not OK        (F2)
✗ expanding a ledger row reveals the full run summary                                (F3)
✗ a periodic self-heal refetch does not clobber an open edit form                    (F1)
```
POST: `TEST RUN PASSED / Passed: 63 / Failed: 0` (exit 0)

### Baseline honesty check (pass_to_pass)
`cd web/elm && elm-test` — PRE `TEST RUN PASSED / Passed: 3123 / Failed: 0`; POST `TEST RUN PASSED / Passed: 3126 / Failed: 0`.
Confirms the reviewed state is fully green: "the tests pass" is not evidence of correctness, and any agent claiming a red suite at pre_state is wrong.

- corrected_cmd: none (setup expressed as read-only `git show` redirects rather than a checkout).
- notes: ~2s for the 3-module leg, ~60s for the full suite.

## Fixup 2026-07-25

Curator-fixup pass over the two borderline audits. Both auditors agreed `task/`
carried no named defect; their objections split cleanly into two task-text items
(fixed) and a set of case.yaml-internal spoilers (dissolved by the exposure
contract). Edits below are permitted because no results exist against this case
yet — it was sealed today and the baseline-run checkbox above is still unticked.
After this pass the exposed content (`task/task.md`, the two pinned refs) is
frozen; any further concern must be handled in `ground_truth/` or as a new case.

### Fixed — leading text in `task/task.md`

1. **Bullet 5 ("introduced here or pre-existing")** restated the D1 scoping
   criterion almost verbatim — it told the reviewer to report pre-existing
   findings, say plainly that they are out of scope, and not fold them into the
   wave's bill, which is exactly the judgement the case grades. Softened to: the
   branch reuses and moves existing components, so not everything that surfaces
   will be new — attribute each finding. The attribution ask survives (without it
   D1 is ungradable and the real reviewer did attribute), the coaching does not.
   The earlier note in "Leakage analysis" above ("an auditor who disagrees should
   downgrade D1 to a bonus rather than rewrite the task") is superseded: with no
   results on the books, softening was cheaper than weakening the rubric.
2. **The Go-slice sentence** named the field: "a small Go slice that adds a
   server-derived `build_status` to the run-metrics read path" — a signpost at
   F2's exact seam. Now reads "a small Go slice on the run-metrics read path".
   The name is still reachable in the exposed tree (`AGENTIC_UX_WAVE_2_SCOPE.md`
   slice 2 says "RunMetrics build_status LEFT JOIN"), which is authentic history
   and stays; the task no longer elevates it.
3. Incidental accuracy fix in the same paragraph: "seven sequential slices by
   different sub-agents" → "seven sequential slices, the later ones by
   sub-agents", matching the scope doc (slices 1–3 orchestrator, 4–7 sub-agents).

Not changed, deliberately: the constraint pointing at `docs/superpowers/plans/`
as the authority for server-side state meaning. It names three contract families
and no fact; it is what makes F2 solvable from the exposed snapshot (the fairness
guarantee recorded above), and it is the orientation a real reviewer had.

### Fixed — grading

- `ground_truth/rubric.md`, scoping section: added the D1 caveat that follows from
  edit 1. Correct attribution in any wording = full credit; an unattributed D1
  report = partial credit, not a scoping failure; the full scoping signal needs
  report + attribution + not blocking the branch. `case.yaml` carries the same
  caveat as a comment under `grading.judge_checklist`.
- `ground_truth/rubric.md`, evidence section: added "credit causal reasoning, not
  quotation". The two priced-deflator docs in the exposed tree — the parked-run
  contract and the branch's own scope note — are KEPT for authenticity, so the
  judge is told to score the chain from evidence to failure: deriving F2 from the
  code alone scores the same as citing the contract, and quoting the contract
  without connecting it to `runOutcome`'s precedence (or restating the scope
  doc's deferred list as a finding) scores nothing.

### Dissolved by the exposure contract — no action

Both auditors' remaining item was that `grading.ground_truth_validation.
failing_specs` and `curation.learnings` state F1/F2/F3 and D1 in plain English
outside `ground_truth/`. Per `schema/benchmark-case-v1.md` §"The exposure
contract", the solver sees exactly (pre_state − withheld) + `task/`; `case.yaml`,
`notes.md` and `ground_truth/` are harness-side and never exposed, so a manifest
may state the answer freely. Nothing renamed, nothing retitled, no spoiler moved.
The one operational consequence already has a louder home: a hand-run must
materialize the two refs as snapshots and present only `task/` — which is the
"Withheld by construction" warning above.

### Known leak channel — declared, not fixed

`known_leak_channels: [project-auto-memory]`. This dev machine's project memory
states F2's answer: `MEMORY.md`'s "Agentic UX wave 2 (impl)" entry names
"runOutcome worst-truth-wins", and `project_agentic_ux_wave_2.md` gives the
settled precedence ("terminal-bad build → then parked → …") plus the ticket-row
metric-ordering follow-ups. That is post-cut knowledge of this exact branch. The
memory is correct and stays as it is; the harness must not mount project memory,
session context or conversation history into the solver, and a local hand-run
here is invalid unless memory is suppressed.

### Difficulty

Unchanged at `moderate` — neither auditor contested it, and nothing in this pass
moved the bar: 31 files / +1798 lines of Elm, three time-dependent defects, two of
them argued against by in-source comments that read as correct.
