# Curation record — feedback-jb-001

## Provenance walk

Backed out of the **remedy** side of a merge gate: an 8-angle adversarial review
of the `agentic-ux-wave-2` branch produced thirteen verified defects, all of
which were fixed in a single commit and merged into `jetbridge` twenty-five
seconds later. That fix commit is the terminal artifact; its body is a
finding-by-finding account of what was changed and why, which is what makes the
remedy traceable.

| Role | SHA | Date | Subject |
|---|---|---|---|
| pre_state (branch tip at the gate) | `171148fd77232d46d183834c9f2e172073c44805` | 2026-07-19T06:54:10-07:00 | `Merge jetbridge into agentic-ux-wave-2 (pick up blank-id review-card fix 9e1fbf99e1 + F30)` |
| terminal artifact (the remedy) | `e366c29a1352b27a6537694ead2ae2e2f9779c91` | 2026-07-19T07:18:12-07:00 | `fix(web)+fix(agent): merge-gate audit fixes for the agentic-UX wave` |
| merge (outcome) | `e62a2f8c2a99c95a8cfd74c48c5c8e9038e75f72` | 2026-07-19T07:18:37-07:00 | `Merge agentic-ux-wave-2: UX audit №3 implementation (U1-U24) + merge-gate audit fixes` |
| base of the wave's own contribution | `c764c395b2497fb1f54930c819ad354d134c1a17` | 2026-07-19T06:07:22-07:00 | `fix(atc): gate ticket-transition pipeline_run_id writes to the ticket's own runs (F30)` |

### Verification performed (all done read-only, via `git show` / `git archive`)

- **Topology.** `e366c29a13`'s single parent is `171148fd77`; `e62a2f8c2a` is
  the merge of `c764c395b2` and `e366c29a13`. So `diff(171148fd77..e366c29a13)`
  is exactly the remedy, with nothing else interleaved, and the outcome is
  `merged`. No rebase, no squash, no follow-up fix commit before the merge.
- **The terminal artifact says what the candidate claimed.** `git log -1 --format=%B`
  opens *"Eleven verified findings from an 8-angle adversarial review of the
  wave"*, carries eleven bullets plus a trailing "Also:" paragraph, and every
  bullet lands in the diff. Bullet 5 bundles two independent defects and bullet
  10 bundles two, which is why the exposed review lists thirteen (F1–F13); the
  atomisation and the bullet mapping are inherited from
  `review-jb-001/ground_truth/expected_findings.yaml`.
- **Every defect is really present at pre_state, at the line numbers the exposed
  review cites.** Each anchor was read at `171148fd77` and checked. Spot
  examples confirmed by the extractor: `AgentBadge.elm:281` opens `runOutcome`'s
  body with `if runStatus == "parked" then` before any `buildStatus` inspection;
  `AgentTicket.elm:1112` binds `runStatus = forBuild |> List.head |> …`, `:1118`
  binds `hasResult = List.any …`, `:784` starts `reviewDigest`'s `latestSummary`
  with the same `List.head`; `Agent.elm:79` is `expandedRuns : Set Int` and
  `:600` is `href ("#" ++ anchorId)`; `Concourse.elm:771-778` is the top-level
  `Json.Decode.field "name"` fallback; `types.go:47-48` give failed/errored the
  single exit `{StateQueued}`; `Dashboard.elm:1273` is `List.take 8 active`;
  `AgentTickets.elm:151` subscribes to `OneSecond` and `FiveSeconds`;
  `AgentReview.elm:178` is `Basics.xor model.showObservations (count <= 5)`.
- **`TerminalStates()` at pre_state** returns exactly
  `{merged, merged_with_fixes, abandoned, concluded}` — the invariant the rubric
  and the pass_to_pass command guard.
- **`bench/` does not exist at pre_state** (`git ls-tree 171148fd77 bench/` is
  empty), satisfying the schema's self-hosted-corpus caveat: replaying this case
  cannot expose the corpus or its answers.
- **Mechanical claims were executed, not inherited** — see §Validation. Two of
  the candidate's `test_signal` claims were wrong and are corrected there.

### Why the pre-state is coherent as a feedback moment

`171148fd77` is the tree the review was performed against: the branch tip after
pulling the mainline in, an hour before merge, with the wave complete (the scope
doc's slice 8 is literally "rebuild bundle, full build/test, adversarial review
workflow"). The remedy commit's parent *is* this ref, so "repo at pre-fix ref +
review findings" is not a reconstruction — it is the exact situation the human
fixer was in.

## Leakage analysis

`withheld: []` — nothing **present at pre_state** gives the remedy away.

### The deliberate asymmetry with review-jb-001

This case and `review-jb-001` share a pre_state, a terminal artifact and a
byte-identical `reference.diff`. What differs is which side of the artifact is
exposed:

| | review-jb-001 | feedback-jb-001 |
|---|---|---|
| findings (F1–F13) | **withheld** (`ground_truth/expected_findings.yaml`) | **exposed** (`task/review-findings.md`) |
| the remedy diff | withheld | withheld |
| primary rubric | reference (recall) | judge (behavioral checklist) |

That is the point of the pair: run on the same pre_state, they are a clean A/B
of "find the bugs" against "fix the named bugs". **They must never be run in the
same session, and their results must not be pooled as independent samples.**

### Withheld by construction (must not be reachable)

- **`e366c29a13` and everything after it.** The commit body is a
  finding-by-finding description of the chosen remedy — a complete answer key,
  and for this case a *more* complete one than for review-jb-001, because here
  the findings are already given and only the fixes are secret.
  `git branch -a --contains e366c29a13` puts it on `agentic-ux-wave-2`, on
  `jetbridge` and on `main`. **Materialize as a detached archive at the pinned
  SHA with no other refs and no reflog.** This is the whole git-side leakage
  story.
- **`~/.claude/projects/.../memory/project_agentic_ux_wave_2.md`** — declared in
  `case.yaml` as `known_leak_channels: [project-auto-memory]`; the corpus cannot
  remove it, only require that runs suppress it. The user's
  auto-memory restates the findings *and* the chosen fix mechanisms by name
  (`scrollToId` port, `Set Int` → `Set String`, `Maybe Bool`, the 6-of-8 cap).
  For this case that memory is a near-total spoiler — it is the remedy, not just
  the diagnosis. Outside the repo, so invisible to a git-based exposure audit;
  any run of this case on this machine must be executed **without that memory
  file loaded**, or the result is void.

### Deliberately exposed, and why

- **`AGENTIC_UX_WAVE_2_SCOPE.md`** (repo root at pre_state). In-tree, referenced
  by `task.md` as the statement of intent and the source of the deferrals the
  fixer must not re-open. Names none of the findings and none of the fixes.
- **The refuted list** at the bottom of `task/review-findings.md`. A real review
  round emitted one (review-jb-001's `task.md` asks for it explicitly), and the
  human fixer had it. It hands over two of the rubric's discipline items (B1
  "keep the catch-all", B2 "keep attention-first ranking") almost directly,
  which makes those items a test of instruction-following rather than of
  judgement. Accepted knowingly: withholding it would have been a fabrication of
  a harder task than the one that actually happened, and the over-fixing failure
  mode it guards against is real even when the agent has been told not to.

### Two fan-out hints, exposed on purpose

Both `task.md` ("several of these fixes change a shared model field or a message
constructor, so the fan-out reaches modules and tests that the finding does not
name") and F7's closing sentence ("this state is shared by two pages and the
toggle message is in the common message type") tell the agent that some fixes
have cross-module consequences. Neither says *which* modules, and neither is a
mechanism. They were kept because the alternative — an agent that fixes
`AgentReview.elm` in isolation and ships a tree that does not compile — is a
build failure, not an interesting result, and because a reviewer who read
`PanelState` would genuinely have written the second sentence. The rubric still
scores the fan-out (A4, A8, C2): knowing it exists is not the same as landing
it coherently across `Build/Models.elm`, `Build/Build.elm`,
`AgentTickets/AgentTicket.elm`, `Message/Message.elm` and their specs.

### What was scrubbed from the exposed review

The exposed findings were reconstructed from `review-jb-001`'s
`expected_findings.yaml`, which was itself derived from a commit that had
**already chosen its implementation**. So the `expected_behavior` fields carried
fix mechanisms that a reviewer looking at broken code would not have written,
and that would turn this case into transcription. Each was rewritten to the
behavior it achieves:

| Finding | Removed (mechanism) | Kept (behavior) |
|---|---|---|
| F3 | "key by build id + plan id" | "each row toggles independently; the identity must survive the refetch prepending newer runs, so an ordinal is equally wrong" |
| F4 | "use the existing `scrollToId` port / `Scroll (ScrollDirection.ToId …)`"; "the console body must be its own id'd scroll container" | "clicking must actually move the console to that section; a fragment href cannot do it under `Browser.application`" |
| F7 | "store a `Maybe Bool`" | "the user's choice is absolute and must survive a count change; only the initial state may depend on the count" |
| F10 | "cap attention chips at 6 of 8" | "live work must always keep a couple of the eight slots; attention-first ranking stays" |
| F11 | "move to the `OneMinute` tick, as the /agent page does"; "two subscriptions instead of three"; and (2026-07-25) "belongs on the existing 5s beat, so the page carries one fewer clock subscription" | "the rollup belongs on a minute cadence — its inputs move at run granularity; the `now` advance does not need a clock to itself, and the elapsed labels must keep advancing" |
| F13 | (2026-07-25) "a test that walks `atc.Plan`'s step fields **by reflection**" | "coverage that fails when a step field is added without a matching `Public()` case, deriving the field set from `atc.Plan` itself rather than from a hand-written list" |

Kept verbatim because they are behavior, not mechanism: F1's full precedence
ordering, F2's latest-row/last-non-empty-summary semantics (including the
`hasResult` sub-defect), F8's three concrete decode outcomes, F9's two edges and
the instruction not to disturb the rest of the matrix.

The scrub created a matching obligation on `ground_truth/rubric.md`: every
alternative the scrub opened up is enumerated there as acceptable. A scrub the
rubric does not match is worse than no scrub, because it punishes the agent for
taking the freedom the task gave it.

### On the information cut

`information_cut` is the pre_state commit's timestamp, per the corpus
convention. Note honestly that `task/review-findings.md` describes work done
*after* that instant — it is a review **of** the pre_state tree, so it could not
have existed before it. This is the same relationship every fix case has between
its bug report and its buggy commit: the cut governs what the **repository**
exposes, and `task/` is by construction a post-snapshot artifact. Nothing in the
exposed review references any commit later than `171148fd77`.

### Accepted natural hints (present, not amplified)

The branch history reachable from `171148fd77` contains **`20b808a92e` —
"fix(web): three bugs from adversarial review of the UX wave"**, the earlier,
lighter review round. Its body names the three defects it fixed; two of them sit
directly next to F1 and F3. This is genuine at-the-cut context — the human fixer
had the same log — and suppressing it would require rewriting the snapshot. The
exposed review already names the same relationship explicitly in F1's note
("the parked-first ordering was introduced by the earlier review round … do not
simply revert it"), which is authentic reviewer guidance, so the log adds
nothing the task does not already say.

## Case shape and grading

- **Exposure manifest** = repo at `171148fd77` (detached, no other refs)
  − nothing withheld + `task/` (`task.md`, `review-findings.md`).
- **Primary rubric is `judge`**, against `ground_truth/rubric.md`: three axes
  (13 remedies × 2 pts; 8 discipline pass/fail; 5 delivery pass/fail), reported
  separately. The interesting failure mode this case is built to catch is a run
  that scores well on remedies and badly on discipline — everything fixed, half
  the wave rewritten, not mergeable.
- **Mechanical commands are a floor, not the score.** They cover F1 and F8
  end-to-end, F9's server half, and the delivery gate — 3 of 13 findings. The
  other ten are Elm view/update logic whose only honest oracle is behavioral.
- **Withheld tests are sorted by coupling.** `ground_truth/withheld_tests/`
  carries all five post-fix Elm specs and both Go specs, but only three files
  are promoted to grading commands:
  - *behavior-coupled, fair oracles*: `AgentBadgeTests.elm` (pure function),
    `BuildStepTests.elm` (decoder), `agent/api/tickets/types_test.go`.
  - *semi-fair*: `AgentTicketsPageTests.elm` — asserts the `OneMinute` interval
    specifically; fair only because the exposed review says "minute cadence".
  - *mechanism-coupled, NOT fair*: `AgentPageTests.elm` pins the literal
    expansion key `"100:plan-abc"`, i.e. the reference's exact key format. A
    correct fix using a different stable per-row identity would fail it. Kept
    for reference; must not be used as an oracle.
  - *stale-spec correction*: `BuildAgentReviewTests.elm` — the pre_state red
    (see below). Fair against any A8 fix that keeps short lists open by default.
- **The pre_state Elm suite is RED**: 3128 passed / 1 failed. The failing spec
  (`BuildAgentReviewTests` → "id-less observations show their body read-only, no
  controls") toggles the observations panel *closed* and then asserts the body
  is visible — it predates the wave's own short-list-auto-opens default and the
  wave never reconciled it. This is a genuine part of the work item ("the
  `elm-test` suite must be green") and a trap: an agent can green the bar by
  deleting the spec, which is rubric item B8.
- `ground_truth/reference.diff` is byte-identical to review-jb-001's
  (`diff -q` confirmed). Same exclusions: `web/public/elm.js` (55 481 deleted
  lines) and `web/public/elm.min.js`. The elm.js drop and the scope-doc move are
  bonus items in the rubric rather than diff content.

## Open questions

- **The exposed review is a reconstruction, not a transcript.** The real review
  output lived only in the originating session; what survives is the fix
  commit's body plus review-jb-001's curated atomisation. The findings are
  therefore *provably* the ones that were acted on (that is their source) but
  their prose, ranking and severities are curator-written. Consequence: this
  case tests "fix a well-written review", which is the optimistic end of the
  feedback-loop distribution. A companion case built from a *sloppy* real review
  — vague, partly wrong, over-broad — would test a different and probably more
  common skill. Worth mining for; nothing in the corpus covers it yet.
- **F13 has no red bar.** `go test ./atc/` passes at both refs with the withheld
  spec restored (verified). It is a missing regression guard, not a defect. The
  rubric scores it as "write a reflection-based test that would catch the next
  omission" and explicitly scores 0 for an agent that "fixes"
  `atc/public_plan.go` — there is nothing there to fix.
- **The bundle rebuild (C4) is a real gate the extractor did not execute.**
  `hack/build-web.sh` needs `elm` + `uglify-js`; both are on PATH here, but the
  script was not run (it writes into the tree). A grader checking C4 should
  confirm the agent's `web/public/elm.min.js` actually differs from pre_state
  rather than trusting a claim in the commit message.
- **Elm grading needs a package cache.** `elm-test` was run from a warm
  `ELM_HOME`; a cold, network-isolated environment will fail to resolve
  packages, and the failure will look like a broken case. Note it in any harness.
- **This corrects a review-jb-001 open question.** That case records "Elm test
  execution is unvalidated … the reference fix's own claim of a green suite is
  from the prior round's commit". It is now measured: green (3138/0) at the
  terminal artifact, and **red (3128/1) at the shared pre_state**. A later stage
  should propagate that to `review-jb-001/notes.md`; the extractor did not edit
  another case's directory.
- **Difficulty is `hard` on mechanical proxies**: 13 findings across 8 files
  containing defects, 19 files in the remedy, +383/−116, two languages, three
  fixes with cross-module type fan-out, plus a generated-bundle rebuild. The
  diagnosis is given, which removes the search cost that makes review-jb-001
  hard — but the fan-out and the discipline constraints replace it. If pilot
  runs stall on budget rather than capability, split by surface
  (AgentBadge + Concourse decoder / AgentTicket page / Agent console / cadence
  + state machine) rather than loosening the rubric.

## Validation

### Extractor pre-check (informational; `case.yaml` records `validated` on the strength of the formal run below, which reproduces these numbers exactly)

Method: `git archive` of the full tree at each ref into the scratchpad (no
checkout, no worktree, repo treated as strictly read-only), then run the real
commands in the real module. Go 1.25.6 darwin/arm64, warm module cache, no
network. elm 0.19.1 / elm-test 0.19.1-revision12 from Homebrew, warm `ELM_HOME`.

| # | Command | Tree | Result |
|---|---|---|---|
| 1 | `go test ./agent/api/tickets/ -run TestValidTransitionMatrix` **+ withheld `types_test.go`** | pre | **FAIL** — `ValidTransition(failed, abandoned) = false, want true`; same for `errored` |
| 1 | ″ | post | **PASS** |
| 2 | `go test ./agent/api/tickets/` (no restore) | pre | PASS |
| 2 | ″ | post | PASS |
| 3 | `go test ./atc/` **+ withheld `public_plan_test.go`** | pre | PASS |
| 3 | ″ | post | PASS |
| 4 | `elm-test tests/AgentBadgeTests.elm tests/BuildStepTests.elm` **+ withheld specs** | pre | **FAIL** — 98 passed / **7 failed** (5 × F1 precedence, 2 × F8 labelling) |
| 4 | ″ | post | **PASS** — 105 passed / 0 failed |
| 5 | `elm-test` (full suite, no restore) | pre | **FAIL** — 3128 passed / 1 failed (stale `BuildAgentReviewTests` spec) |
| 5 | ″ | post | PASS — 3138 passed / 0 failed |

Commands 1 and 4 are confirmed fail-to-pass; 2 and 3 are confirmed pass-to-pass;
5 is the delivery gate and documents the pre-existing red. (Row 3 was
reclassified by the 2026-07-25 fixup: restoring the withheld
`atc/public_plan_test.go` would overwrite the test F13 asks the agent to write,
so it is now `grading.negative_control` under a renamed path, and the
`pass_to_pass` leg runs `go test ./atc/` on the agent's tree with no overlay.
The measured results are unaffected — both forms are green at both refs.) The third
`BuildStepTests` case (`{"id":"1"}` → generic label) passes at **both** refs,
independently confirming that the review's refusal to remove the catch-all
(rubric B1) is grounded in the reference behavior and not an inference.

Corrections to the mining candidate, both found by executing rather than reading:

- The candidate listed `web/elm/tests/AgentTicketPageTests.elm` among the test
  files. **No such file is touched** — the remedy changes
  `AgentTicketsPageTests.elm` (plural, the queue page). The ticket *page*'s
  behavior changes (F5, F6, F12) shipped with **no** new spec, which is why the
  rubric scores them by judge only.
- The candidate called `atc/public_plan_test.go` a plausible fail-to-pass
  candidate. It is not (row 3 above) — see the F13 open question.

### Formal validation

_Superseded — filled in by the validation stage in the `## Validation` section
below, which is the anchor `case.yaml` points at. Judge calibration (does a
strong run separate Axis A from Axis B?) is still open; the leakage audit is
recorded in `case.yaml#leakage_audit` (opus + sonnet + the 2026-07-25 curator
fixup)._

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `171148fd77232d46d183834c9f2e172073c44805`, post `e366c29a1352b27a6537694ead2ae2e2f9779c91`
- outcome: **validated** (all five legs reproduce the recorded numbers exactly)

### F9 server half — fail_to_pass
`cp $CASE_DIR/ground_truth/withheld_tests/agent/api/tickets/types_test.go agent/api/tickets/types_test.go && go test ./agent/api/tickets/ -run TestValidTransitionMatrix -count=1`
PRE (FAIL, exit 1):
```
--- FAIL: TestValidTransitionMatrix (0.00s)
    types_test.go:49: ValidTransition(failed, abandoned) = false, want true
    types_test.go:49: ValidTransition(errored, abandoned) = false, want true
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/agent/api/tickets  0.140s`

### F1 + F8 elm half — fail_to_pass
`cp $CASE_DIR/ground_truth/withheld_tests/web/elm/tests/{AgentBadgeTests.elm,BuildStepTests.elm} web/elm/tests/ && cd web/elm && elm-test tests/AgentBadgeTests.elm tests/BuildStepTests.elm`
PRE (TEST RUN FAILED, exit 2) — `Passed: 98 / Failed: 7`, exactly the recorded 5xF1 + 2xF8:
```
✗ a parked run whose build ERRORED shows Errored, not Waiting on you
✗ a parked run whose build was ABORTED shows Aborted, not Waiting on you
✗ a step that reported error inside a SUCCEEDED build is Errored, never a green OK
✗ a step that reported error while its build is still open is Errored, not Running
✗ a step that reported failed inside a SUCCEEDED build is Failed
✗ labels an unrecognized step with its nested name
✗ labels an unrecognized step with its type key when it has no name
```
POST: `TEST RUN PASSED / Passed: 105 / Failed: 0` (exit 0)

### F9 regression guard — pass_to_pass (no overlay)
`go test ./agent/api/tickets/ -count=1` — PRE `ok ... 0.141s`, POST `ok ... 0.141s`.

### F13 negative control (`grading.negative_control` since the 2026-07-25 fixup; was listed as pass_to_pass)
`cp $CASE_DIR/ground_truth/withheld_tests/atc/public_plan_test.go atc/public_plan_test.go && go test ./atc/ -count=1`
PRE `ok  github.com/concourse/concourse/atc  0.399s`, POST `ok ... 0.416s`. Confirms F13 is a missing regression guard, not a live defect.

### Delivery gate C2/B8 — weak oracle
`cd web/elm && elm-test` (repo's own test files)
PRE: `TEST RUN FAILED / Passed: 3128 / Failed: 1` — the single red spec is `✗ id-less observations show their body read-only, no controls` (BuildAgentReviewTests), exactly as recorded.
POST: `TEST RUN PASSED / Passed: 3138 / Failed: 0`.

- corrected_cmd: `$CASE_DIR` must be an absolute path into the corpus checkout (bench/ does not exist inside either materialized worktree). Otherwise verbatim.
- notes: no Postgres, no cluster, no network (warm ELM_HOME). Go legs <1s; full elm suite ~60s.

## Fixup 2026-07-25

Curator pass over the dual leakage audit (opus `borderline`, sonnet
`borderline`). Every audit item resolved; three further defects found while
resolving them. Residual verdict **pass**, with a declared leak channel.

### Dissolved by the exposure contract (no action)

- **`ground_truth/` reachability.** The exposure manifest is
  `pre_state − withheld + task/`; `case.yaml`, `notes.md` and `ground_truth/`
  are harness-side and never exposed, so the answer key sitting next to the case
  is not a leak. Nothing renamed, nothing moved. (The schema also permits the
  title and grading config to state the answer outright, which this case's do.)
- **`bench/` in the subject repo.** Re-confirmed `git ls-tree 171148fd77 bench/`
  is empty, so a replay at the pinned SHA cannot reach the corpus.
- **Terminal-artifact reachability** (`e366c29a13` on `agentic-ux-wave-2`,
  `jetbridge`, `main`) was already mitigated before the audit by
  `pre_state.repository.materialize` — a detached `git archive`, no other refs,
  no reflog. Left as written; it is the correct and only fix.

### Known leak channel (declared, not fixable in-package)

- Added `known_leak_channels: [project-auto-memory]` to `case.yaml` with a
  comment, plus a `RUN CONSTRAINT` header replacing the merger's stale
  `# BORDERLINE` marker, plus a pointer in §Leakage analysis above. Both
  auditors independently found that the curation machine's project auto-memory
  (`project_agentic_ux_wave_2.md`, indexed from `MEMORY.md`) states the fix
  summaries for F1/F2/F4/F9 and the `elm.js` bonus item — several by mechanism.
  Outside the repo, invisible to a git-side audit, unremovable by the corpus.
  A run of this case with project memory mounted is **void**, not weakened.

### Real defects fixed

1. **`task/review-findings.md` F13 — scrub gap (mechanism).** "a test that walks
   `atc.Plan`'s step fields **by reflection**" prescribed the implementation.
   Rewritten to the behavior: coverage that fails when a step field is added
   without a matching `Public()` case, deriving the field set from `atc.Plan`
   itself rather than from a hand-written list. The behavioral requirement that
   made reflection the obvious answer survives; the word does not.
2. **`ground_truth/rubric.md` A13 — matched to that scrub.** Now scores 2 for
   any self-deriving mechanism (reflection, or a generated/AST-derived list that
   is regenerated and diff-checked), 1 for a hand-enumerated list, 0 unchanged
   for "fixing" `public_plan.go`. Per the standing rule that a scrub the rubric
   does not match is worse than no scrub.
3. **`task/review-findings.md` F11 — scrub gap (mechanism).** "the `now` advance
   belongs on the existing 5s beat, so the page carries one fewer clock
   subscription" named the reference's exact subscription arrangement.
   Rewritten to: the rollup belongs on a minute cadence, the `now` advance does
   not need a clock of its own, and neither change may cost the page its
   live-updating behavior (data still refreshes on its current beat, elapsed
   labels keep advancing).
4. **`ground_truth/rubric.md` A11 — matched to that scrub.** Accepts any
   minute-ish rollup cadence and any surviving tick that advances `now` no
   slower than the data refetch; explicitly forbids requiring the reference's
   subscription count.
5. **Grading collision: the overlays clobber the tests the task asks for.** All
   four withheld grading specs live at paths that **exist at pre_state**
   (verified by `git ls-tree`): `agent/api/tickets/types_test.go`,
   `atc/public_plan_test.go`, `web/elm/tests/AgentBadgeTests.elm`,
   `web/elm/tests/BuildStepTests.elm`. The task asks for regression coverage
   (rubric C3) and F13's entire remedy **is** a test at one of those paths
   (A13). A naive `cp` restore therefore deletes the agent's evidence and, for
   F13, grades the reference's test as if it were the agent's. Added
   `grading.protocol` to `case.yaml` (judge the pristine delivered tree first;
   pass_to_pass with no overlay; fail_to_pass overlays on a scratch copy only),
   annotated both `fail_to_pass` notes, and cross-referenced it from rubric C3,
   A13 and the judge notes.
6. **Grading collision: the F13 leg specifically.** `pass_to_pass`
   `go test ./atc/` previously ran with the withheld `atc/public_plan_test.go`
   restored *over* the agent's file. Split: the `pass_to_pass` leg now runs on
   the agent's tree with no overlay, and the reference-spec run became a
   `negative_control` entry that restores to
   `atc/reference_public_plan_exhaustiveness_test.go` instead. Checked that
   neither file declares a package-level identifier beyond `var _ = Describe`,
   so the renamed copy cannot collide; its duplicated specs merely run twice.
7. **Spurious-pass gate.** The delivery gate carried only
   `expect_at_ground_truth: "3138 passed, 0 failed"`. The cheapest route to
   "0 failed" is deleting the stale `BuildAgentReviewTests` spec, and a correct
   change adds specs so it will never hit 3138 anyway. Added a `pass_condition`:
   0 failed **and** the stale spec still present; the reference count is
   evidence, not a threshold.
8. **Unreachable delivery channel (C5).** `task.md` demanded "a single commit
   … and a commit message that enumerates the findings", but the prescribed
   materialization is `git archive | tar -x`, which hands the agent a tree with
   no repository to commit into — C5 was ungradeable in the harness the manifest
   itself specifies. `task.md` now names both channels (commit message when the
   tree is a git checkout, `MERGE_GATE_RESPONSE.md` at the repo root when it is
   not) and rubric C5 says to fail on content, never on channel.
9. **Manifest/record inconsistencies.** The delivery gate cited rubric item
   "C5/B8" where the item is C2 + B8 (fixed); the pre-check heading still said
   `case.yaml` records `unvalidated` when it records `validated` (fixed).

### Difficulty

Unchanged at **hard**, now with the reasoning inline in `case.yaml`. Neither
auditor contested it and the fixup does not touch the drivers: thirteen findings
across eight defect-bearing files, nineteen files in the remedy, +383/−116, two
languages, three fixes with cross-module type fan-out, plus a bundle rebuild.
The F13/F11 scrubs widen the solution space slightly but do not reduce the work.

### Priced-deflator docs

`AGENTIC_UX_WAVE_2_SCOPE.md` stays exposed (it is cited by `task.md`, states the
deferrals B7 grades against, and names no finding and no fix). Added a judge
note to `ground_truth/rubric.md`: credit reasoning that survives the review's
failure scenarios, not write-ups that quote the in-tree scope doc.

### Not changed on purpose

- The refuted list and the two fan-out hints — both are deliberate, documented
  above, and load-bearing for B1/B2 and for C2.
- `withheld: []` — still correct; nothing present at pre_state gives a remedy
  away, and the fixup found no new candidate.
- The mechanical numbers and the validation record: no exposed *source* changed,
  so the recorded fail-to-pass / pass-to-pass results stand as measured. The
  edits touch `task/` prose, `ground_truth/rubric.md` and grading procedure only.
