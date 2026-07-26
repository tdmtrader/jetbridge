# feedback-jb-002 — curation record

## Provenance walk

Backed out of a single terminal artifact, walking backwards from the fix to the
pre-state to the trigger.

**Terminal artifact — `53736f9c73561708142663587ddad752478e7cce`**
(2026-07-19T21:40:48-07:00, Thomas Moore)

```
review fixes (Wave A): WF-2 narrow elm-gate applicability to bundle-feeding paths;
WF-5 compensating workflow-assign rollback
```

Its body enumerates both findings and both remedies. Diff: 4 files, +202/-23 —
`agent/harvest/gates.go` (+42/-20), `agent/harvest/gates_elm_test.go` (+61),
`fly/commands/agent_tickets.go` (+78/-... ), `fly/integration/agent_tickets_test.go`
(+44). Verified against the candidate's claim: it says exactly what the mining pass
reported, no more and no less.

**Pre-state — `e7810cbb8a9c662fe24b542a0734504fd480b491`**
(2026-07-19T21:35:26-07:00), the sole parent of the terminal artifact. Confirmed with
`git show -s --format='%H %P'`.

The candidate described it as "the UX4 Wave A integration merge (wf2-elm-gate +
s7-spend-backend + wf5-workflow-assign)". Verified — it is the last of a three-merge
chain onto the integration line, all three merges committed within the same second:

```
53736f9c73  review fixes (Wave A)                     <- terminal artifact
e7810cbb8a  merge: wf2-elm-gate (UX4 Wave A)          <- pre_state
3c80264dff  merge: s7-spend-backend (UX4 Wave A)
39e4238266  merge: wf5-workflow-assign (UX4 Wave A)
12feb9397d  merge: #45 runner-image skew visibility   <- wave base
```

So the reviewed change is `12feb9397d..e7810cbb8a` and both reviewed features are inside
it. `task/change.diff` is that range restricted to the WF-2 and WF-5 paths (14 files,
+633/-20); the s7-spend-backend branch is excluded as pure distractor volume.

**Coherence checks run:**

- Both reviewed defects are real at pre_state, read directly out of the source:
  - `agent/harvest/gates.go` at `e7810cbb8a` has `elmSourcePrefix = "web/elm/"` and
    `if strings.HasPrefix(p, elmSourcePrefix) { elmChanged = true }`, then requires
    `web/public/elm.min.js` in the diff.
  - `git ls-tree e7810cbb8a -- web/elm/` returns `.agignore .gitignore benchmarks
    elm.json src tests` — so the unsatisfiable-failure class is not hypothetical; three
    of those six entries cannot affect the bundle.
  - `fly/commands/agent_tickets.go` at `e7810cbb8a` has
    `assignWorkflowIfRequested(...) error`, called before `TransitionAgentTicket` /
    `DispatchAgentTicket`, with no capture of the prior value and no rollback on the
    error paths.
- The fix is reachable with APIs that already exist at pre_state:
  `client.GetAgentTicket(id) (tickets.TicketDetail, bool, error)` is present in
  `go-concourse/concourse/agent_tickets.go`, and `tickets.Ticket` already carries
  `WorkflowName string` / `WorkflowVersion *int`. No route, no wire change, no
  migration — matching the constraint block in `task/task.md`.
- The `*int` version pin is why the reference rollback is *partial*: a prior "live"
  (nil) pin cannot be re-expressed through the partial `UpdateRequest`. The reference
  says so at the rollback site rather than pretending otherwise, which is what rubric
  C-3(b) grades.

**Derivation of the task.** There is no separately recorded review artifact — the round
happened in-session and the findings survive only in the terminal artifact's commit
message. `task/task.md` is therefore SYNTHESIZED from that message plus the pre-state
code, written as the review comment would have read at the cut. What was carried over:
the two symptoms, their severity, and the two secondary asks (comment honesty, help
text). What was deliberately withheld: every element of the remedy — the path set
(`web/elm/src/**` + `web/elm/elm.json`), the read-then-restore shape, and the name
`restore()`. The task states expected behaviour ("apply only where a change can
genuinely leave the committed bundle stale"; "a command that fails must not leave the
ticket in a worse state than the one it found") without naming a location or approach.

## Leakage analysis

**Scrubbed from the task.**

- The word *bundle-feeding* and the enumeration `web/elm/src/**` + `web/elm/elm.json`.
  This is the whole of finding 1's answer and it is one phrase in the commit subject.
- `elm.json` is never mentioned anywhere in `task/task.md`. Recognising that a
  dependency-manifest change does feed the compiled bundle is the sub-judgment that
  separates a good fix from the attractive shallow one, and it is what
  `TestElmGateAppliesToElmJson` grades.
- The exhaustive list of affected non-bundle paths. The task names `web/elm/tests/**`
  as the motivating example (a reviewer would) and then generalises — "the other
  subtrees under `web/elm` that are not part of that compile graph, and the
  housekeeping files that sit alongside them". Naming `benchmarks/` and `.gitignore`
  explicitly would invite a deny-list of exactly those three, which is a strictly worse
  fix that the grading tests would nonetheless pass. The vaguer phrasing keeps the
  dotfile case as a live discriminator (rubric F1-4).
- The rollback mechanics. The task describes the damage (a failed dispatch leaves the
  typo'd workflow in place) and the required invariant, not the read-before-write or
  the returned closure.

**Withheld from the snapshot: nothing.** `withheld: []` is a positive finding, not a
default. Checked:

- `git grep -i "bundle-feeding\|no-worse-than-before\|worse than before\|compensating"`
  over the whole pre_state tree (excluding `web/public`): no hits.
- `docs/superpowers/plans/agentic-platform/2026-07-19-wf2-elm-build-gate.md` is in-tree
  at pre_state and was the flagged risk in the candidate. Read in full. It prescribes
  the **buggy** behaviour — "only applies when `web/elm/**` is present", and its Task 3
  literally specifies the `return finish("ok", "elm-build gate: no web/elm/** changes
  in the diff — not applicable")` string. It contains no narrowing and no notion that
  non-src paths are a problem. Left exposed, deliberately: the task cites its Open
  Decision #1 by name (that decision is the diff-presence-vs-byte-compare tradeoff the
  reviewer asks to be documented honestly), so the agent must go read it. If anything
  the plan is a mild *anti*-hint, since it asserts the wrong rule with confidence.
- `docs/superpowers/plans/agentic-platform/2026-07-19-ux4-scoping.md` carries the
  original WF-5 entry (line 227). It describes the empty-workflow dead-end and offers
  three fix options — option (b), "add `--workflow` to queue/dispatch", is the feature
  under review, already implemented at pre_state. Nothing about atomicity or rollback.
  Not a leak.
- `ci/dogfood/FINDINGS.md` at pre_state mentions only that Elm work is not dogfoodable
  without an Elm gate. The elm-gate enablement discussion the candidate worried about
  lands in `2e8387f5f8` / `3743c6c3b9`, which are descendants — a pre-state snapshot is
  clean of it, confirmed.
- Grading tests do not exist at pre_state in their post-fix form. The pre_state
  `gates_elm_test.go` (211 lines, shipped with the feature) covers not-applicable /
  stale-bundle / compile-fail / missing-base and asserts nothing about applicability
  scoping. It is exposed both in the snapshot and inside `task/change.diff`, correctly —
  it is the reviewed work, not the answer.

**Branch contamination — the load-bearing risk here.** The terminal artifact is a
DIRECT child of pre_state and its commit message is a verbatim answer key. It is
reachable from `jetbridge`, `main`, `claude/ux-audit-4-execution-269308`,
`claude/agent-swarm-type-selection-686993`, `claude/sealed-output-records-fa8cc1`,
`codex/agentic-functions`, `codex/postgres-delivered-diffs` and from every tag
`v0.2.197` onward. A materialization that leaves a `.git` directory behind hands the
agent `git log -1 HEAD..jetbridge` and the case is over. Hence the `materialize:`
instruction pinned in `case.yaml` (`git archive | tar -x`, detached, no refs, no
reflog) rather than only in these notes.

**Self-hosted corpus caveat.** `pre_state` is `e7810cbb8a` (2026-07-19); `bench/` did
not exist until 2026-07-25. `git ls-tree e7810cbb8a -- bench` is empty. The corpus is
not reachable through the exposure manifest.

**Memorization: none.** Private post-cutoff repository; jetbridge-era fork history,
never published.

## Open questions

1. **Is the fly mechanical gate too tight?** It pins a `GET
   /api/v1/agent/tickets/:id` immediately before the `PUT`. Reading the prior value is
   behaviourally forced, but doing it through that particular route is not — an agent
   could reach it via `ListAgentTickets` or by hoisting the read the dispatch path
   already performs. Recorded in `case.yaml` grading notes and rubric F2-2 as a
   reconcile-with-the-judge instruction rather than fixed, because the alternative is
   rewriting the humans' test. Worth revisiting after the first pilot run: if agents
   routinely take a different-but-correct route, replace the wire assertion with a
   final-state assertion.
2. **Two findings in one task — is that one case or two?** The humans fixed both in one
   commit, so this is faithful to the artifact, and a feedback round genuinely arrives
   as a list. But it makes partial credit the normal outcome and couples two unrelated
   subsystems (`agent/harvest`, `fly/commands`). If pilot scores cluster at exactly
   half, the diagnosis is "the shape is wrong", not "the agents are weak" — split into
   feedback-jb-002a/b at that point rather than reweighting the rubric.
3. **The comment-honesty asks are judge-only.** Rubric C-3 grades two admissions that
   no test can see. If judge agreement on C-3 turns out to be poor, this case degrades
   to a straightforward two-bug fix case and loses what makes it interesting — the
   whole class of "compensating action / scoped applicability" work is about writing
   down what you did *not* cover.
4. **`TestElmGateAppliesToElmJson` is a pass-to-pass that is really an anti-regression
   trap.** The schema has one slot for both. Fine for now; if the corpus grows more of
   these, `pass_to_pass` probably wants an explicit `guards: over-narrowing` annotation
   so a runner can report "the fix passed but the trap fired" distinctly from an
   unrelated regression.
5. The exposed `task/change.diff` includes `deploy/agent-runner/Dockerfile` (+20, the
   Elm toolchain layer) and the counterfeiter fake (+79). Both are honest parts of the
   reviewed change and neither is graded; if change.diff size ever becomes a budget
   problem the fake is the first thing to drop.

## Validation — extractor evidence

Written during extraction, when `case.yaml` still said `status: unvalidated`. The formal
validation stage has since run; its record is the **## Validation** section below, and
`case.yaml` now says `status: validated`. This section is kept as the earlier, independent
evidence — the two agree.

What the **extractor** ran while backing the case out, recorded here as evidence that
the transitions are real and the environment is hermetic. Materialized both ends with
`git archive <sha> | tar -x` into scratch directories (no `.git`), Go 1.25.6 toolchain,
macOS arm64.

| # | Command | pre_state `e7810cbb8a` | terminal `53736f9c73` |
|---|---|---|---|
| 1 | `go test ./agent/harvest/ -run 'TestElmGateNotApplicableForNonBundleElmPaths' -count=1` *(withheld spec restored)* | **FAIL** — all 3 subtests | **PASS** |
| 2 | `go test ./fly/integration/ -count=1 -args -ginkgo.focus='fly agent tickets'` *(withheld spec restored)* | **FAIL** — 3 of 27 specs | **PASS** |
| 3 | `go test ./agent/harvest/ -count=1` *(pre_state's own specs)* | **PASS** (8.6s) | — |
| 4 | `go test ./agent/harvest/ -count=1` *(withheld spec restored)* | — | **PASS** (12.7s) |
| 5 | `go test ./fly/integration/ -count=1` *(withheld spec restored)* | — | **PASS** (38.5s, 650 specs) |

Failure text at pre_state, #1:

```
--- FAIL: TestElmGateNotApplicableForNonBundleElmPaths/web/elm/tests/MainTests.elm
    outcomes = [{Gate:elm-build ... Status:failed ... Detail:elm-build gate: elm make
    --optimize failed: -- NO elm.json FILE ...}], want a single ok gate
    (web/elm/tests/MainTests.elm does not feed the bundle => not applicable)
```

Failure text at pre_state, #2:

```
[FAIL] fly agent tickets dispatch [It] rolls the assigned workflow back to its prior
value when dispatch fails (WF-5 no-worse-than-before)
  [FAILED] Method mismatch  Expected <string>: PUT  to equal <string>: GET
```

**Toolchain-independence check.** #1 was re-run at pre_state with `elm` removed from
`PATH`. The pre_state outcome changes from `failed` ("elm make --optimize failed")
to `error` ("could not run elm: executable file not found in $PATH") — still not the
`ok` the spec requires, so the fail-at-pre holds either way. Post-fix the gate returns
before compiling, so #1 and #4 are green with no Elm toolchain present. Recorded as
`environment.elm_required: false`.

**Also established:** `TestElmGateAppliesToElmJson` passes at pre_state (it was the only
other new spec in the withheld file and it did not appear in run #1's failure list) and
at the terminal artifact — confirming it is a genuine always-green over-narrowing trap
rather than a second transition.

**Also established, negatively:** the pre_state `fly/integration/agent_tickets_test.go`
is NOT pass-to-pass across this fix. Two of its specs (`assigns the workflow before
transitioning`, `assigns the workflow before dispatching`) append only a `PUT` handler
and go red against the corrected code, which reads first. This is why the withheld file
replaces the whole file. Any validation run that keeps the pre_state spec alongside the
fix will see spurious reds.

**Caveat on the timings.** The scratchpad is shared across concurrently running
extractor sessions; a first attempt at this validation was clobbered when another
session reused `./pre` and `./post`. The numbers above are from a re-run in
case-namespaced directories, but wall-clock times were measured under contended
`GOCACHE` and should be treated as upper bounds.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `e7810cbb8a9c662fe24b542a0734504fd480b491`, post `53736f9c73561708142663587ddad752478e7cce`
- outcome: **validated** (all four legs)

### fail_to_pass 1 — WF-2 elm-gate applicability
`cp $CASE_DIR/ground_truth/withheld_tests/agent/harvest/gates_elm_test.go agent/harvest/gates_elm_test.go && go test ./agent/harvest/ -run 'TestElmGateNotApplicableForNonBundleElmPaths' -count=1`
PRE (FAIL, exit 1):
```
--- FAIL: TestElmGateNotApplicableForNonBundleElmPaths (0.49s)
    --- FAIL: TestElmGateNotApplicableForNonBundleElmPaths/web/elm/tests/MainTests.elm (0.17s)
        gates_elm_test.go:98: outcomes = [{Gate:elm-build ... Status:failed ... Detail:elm-build gate: elm make --optimize failed:
            -- NO elm.json FILE ---
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/agent/harvest  0.349s`

### fail_to_pass 2 — WF-5 fly agent tickets
`cp $CASE_DIR/ground_truth/withheld_tests/fly/integration/agent_tickets_test.go fly/integration/agent_tickets_test.go && go test ./fly/integration/ -count=1 -args -ginkgo.focus='fly agent tickets'`
PRE (FAIL, exit 1):
```
Summarizing 3 Failures:
  [FAIL] fly agent tickets queue    [It] assigns the workflow before transitioning when --workflow is given
  [FAIL] fly agent tickets dispatch [It] assigns the workflow before dispatching when --workflow is given
  [FAIL] fly agent tickets dispatch [It] rolls the assigned workflow back to its prior value when dispatch fails (WF-5 no-worse-than-before)
Ran 27 of 650 Specs in 5.186 seconds
FAIL! -- 24 Passed | 3 Failed | 623 Skipped
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/fly/integration  5.148s`

### pass_to_pass
`go test ./agent/harvest/ -run 'TestElmGateAppliesToElmJson' -count=1` (with the withheld file in place) — PRE `ok ... 0.279s`, POST `ok ... 0.267s`.
`go test ./agent/harvest/ -count=1` (no overlay) — PRE `ok ... 8.021s`, POST `ok ... 8.247s`.

- corrected_cmd: none; `$CASE_DIR` must be absolute.
- notes: no Postgres, no cluster. fly/integration builds the fly binary against the mock ATC (~5s per run); harvest full package ~8s.
- (fixup 2026-07-25) the `cp` overlays above are correct for validating the ORACLE, but a
  grading run against a solver must preserve the solver's versions of those two paths
  first — see `case.yaml` `grading.overlay_protocol`.

## Fixup 2026-07-25

Curator-fixup pass over the dual audit. Every audit item resolved; four edits made.

**1. DISSOLVED BY CONTRACT — sonnet's `TestElmGateAppliesToElmJson` flag.** Sonnet asked to
"confirm it never reaches the solver". Per the exposure contract in
`schema/benchmark-case-v1.md`, the solver sees exactly (pre_state − withheld) + `task/`;
`case.yaml`, its grading config, `withheld_test_paths` and the case id/path are harness-side
and never exposed. The test name is therefore free to state the discriminator. **No rename,
no retitle, no edit** — recorded here as dissolved. Same reasoning covers the file names
under `ground_truth/withheld_tests/` and the `feedback-jb-002` directory name itself. The
one real obligation it implies is on the runner, and it was already pinned: the `materialize:`
line in `case.yaml` (`git archive | tar -x`, detached, no refs, no reflog), because the
terminal artifact is a direct child of pre_state and its commit message is a verbatim answer
key.

**2. REAL DEFECT (fixed) — the grading overlay clobbered the tests the task asks for.**
`task/task.md` says "Both findings need regression tests", and rubric C-2 grades exactly
that. But both `fail_to_pass` legs `cp` the withheld oracle spec over
`agent/harvest/gates_elm_test.go` and `fly/integration/agent_tickets_test.go` — the two
files that exist at pre_state and are the natural (and, in the reference change, the actual)
home for a solver's new tests. A naive grading run therefore deletes the C-2 evidence before
the judge ever sees it, and the fly `pass_to_pass` leg, specified "WITH the withheld spec
restored", would never execute a single solver-written fly spec. Added
`grading.overlay_protocol` to `case.yaml`: preserve the solver's versions of every
`withheld_test_paths` entry to `solution_tests/`, run the solver's own suites un-overlaid
once as an informational `solver_tests_self_report`, and only then overlay and run the graded
commands. Amended rubric C-2 to score from `solution_tests/` and to return **unscoreable**
(not fail) if the copies are missing. Note this is a collision with the *grading procedure*,
not with the exposure manifest — the oracle specs themselves stay withheld.

**3. REAL DEFECT (fixed) — internally inconsistent validation record.** `notes.md` opened its
Validation section with "`status: unvalidated` in `case.yaml`" while `case.yaml` said
`validated` and a second, later `## Validation` section recorded a validated four-leg run.
Two headings also made the `notes.md#validation` anchor in `validation.notes` ambiguous.
Retitled the earlier one "## Validation — extractor evidence" with a sentence reconciling the
two, leaving `## Validation` (the formal 2026-07-25 record) as the anchor target. No test
result was altered; the two records agree on every leg.

**4. Opus's "answerable by paraphrase" flag — handled as judge weighting, task left intact.**
Opus observed that the two secondary asks (the overclaiming doc comment, the
`--workflow-version` help text) are close to dictated by the prompt, so roughly half the
judge rubric can be answered by paraphrase. This is **not** leading text to be softened: the
input to a feedback-loop case *is* a review comment, and reviewers do name the secondary
nits. Scrubbing them would falsify the trigger and drop two things the humans actually did.
Fixed on the grading side instead — added `## Scoring notes` to `ground_truth/rubric.md`
telling the judge that F2-5 and C-3 are requested rather than discovered (credit substance
only: C-3(a) must name the concrete divergent case, which the task deliberately does not;
C-3(b) must identify the solver's own residual limit; F2-5 must not change flag semantics),
and that the run's verdict should be carried by F1-3, F1-4, F2-1, F2-2 and C-2.

**5. Priced-deflator doc — KEEP, per the default.** `2026-07-19-wf2-elm-build-gate.md` stays
exposed. It prescribes the buggy `web/elm/**` rule, the task cites its Open Decision #1 by
name, and it is authentic pre_state history; it is an anti-hint, not a leak. Added the
required judge instruction to the Scoring notes: credit causal reasoning from the code and
the changed-path set, not doc-quotation, and do not penalise a solver for contradicting the
plan — contradicting it is the correct move.

**6. KNOWN LEAK CHANNEL (new; neither auditor could have seen it).** The dev machine's
project auto-memory states both remedies verbatim. `project_agentic_ux_audit_4.md`, in the
2026-07-20 "EXECUTED" entry: WF-5 "**compensating-rollback** so a bad --workflow never leaves
a ticket worse", and WF-2 "review-narrowed applicability to web/elm/src/**+elm.json;
diff-presence is a deliberate Open-Decision-1 tradeoff, byte-compare deferred" — that is
finding 1's exact path set (including the `elm.json` inference that
`TestElmGateAppliesToElmJson` exists to grade), finding 2's remedy, and the C-3(a)
admission. Declared `known_leak_channels: [project-auto-memory]` in `case.yaml` with the
quotes. Memory is **not** modified. Consequence, per the README bullet: a hand-run of this
case on this machine is invalid unless project memory, session context and conversation
history are suppressed. The structural lesson (the dual audit is scoped to the case
directory and cannot see the operator environment) is recorded in `curation.learnings`.

**7. DIFFICULTY — reaffirmed `moderate`, reasoning now recorded in `case.yaml`.** Not
trivial: the graded discriminator (elm.json as bundle-feeding, rubric F1-3/F1-4) is named
nowhere in `task/task.md`, and the in-tree plan argues the opposite. Not hard: both remedies
are localized edits inside one function each, every API needed exists at pre_state (no route,
no wire change, no migration), and the review names both symptoms precisely. The
paraphrase-answerable secondaries do not lower it, because the primary score is the two
mechanical transitions plus the always-green over-narrowing trap.

Not changed: `task/task.md`, `task/change.diff`, `ground_truth/reference.diff`, the withheld
specs, `withheld: []`, `information_cut` (already exactly the pre_state commit timestamp,
2026-07-19T21:35:26-07:00, and `task.md` carries no internal dates to contradict it), the
grading commands themselves, and `validation.status`.
