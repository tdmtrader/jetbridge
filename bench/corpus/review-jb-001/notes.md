# Curation record — review-jb-001

## Provenance walk

Backed out of a **review round with a recorded outcome**: an 8-angle adversarial
merge-gate audit of the `agentic-ux-wave-2` branch, whose confirmed findings were
fixed in a single commit and merged into `jetbridge` twenty-five seconds later.
The findings themselves were never committed as a document; the commit body IS
the findings list, which is what makes this case gradeable and also what makes
the leakage boundary sharp.

| Role | SHA | Date | Subject |
|---|---|---|---|
| review target / pre_state | `171148fd77232d46d183834c9f2e172073c44805` | 2026-07-19T06:54:10-07:00 | `Merge jetbridge into agentic-ux-wave-2 (pick up blank-id review-card fix 9e1fbf99e1 + F30)` |
| base of the change under review | `c764c395b2497fb1f54930c819ad354d134c1a17` | 2026-07-19T06:07:22-07:00 | `fix(atc): gate ticket-transition pipeline_run_id writes to the ticket's own runs (F30)` |
| terminal artifact (the fix) | `e366c29a1352b27a6537694ead2ae2e2f9779c91` | 2026-07-19T07:18:12-07:00 | `fix(web)+fix(agent): merge-gate audit fixes for the agentic-UX wave` |
| merge (outcome) | `e62a2f8c2a99c95a8cfd74c48c5c8e9038e75f72` | 2026-07-19T07:18:37-07:00 | `Merge agentic-ux-wave-2: UX audit №3 implementation (U1-U24) + merge-gate audit fixes` |
| branch point | `54b541a81e6235ca74256dfbd50666ec45a18d2c` | — | `git merge-base c764c395b2 20b808a92e` |

### Verification performed

Every claim in the mining candidate was re-checked; all held.

- **Topology.** `git rev-list --parents -n1` on each SHA: `171148fd77` is a merge
  of `20b808a92e` (wave tip) and `c764c395b2` (jetbridge); `e366c29a13`'s single
  parent is `171148fd77`; `e62a2f8c2a` is the merge of `c764c395b2` and
  `e366c29a13`. So `diff(c764c395b2..171148fd77)` is exactly the wave's own
  contribution, `e366c29a13` is exactly the audit's remedy, and the outcome is
  `merged`. No rebase, no squash, no gap.
- **The terminal artifact says what the candidate claimed.** `git log -1 --format=%B
  e366c29a13` opens *"Eleven verified findings from an 8-angle adversarial review
  of the wave"* and enumerates them in eleven bullets. Each bullet was traced to
  code in `git diff 171148fd77..e366c29a13`; every one lands. Nothing in the
  commit body is aspirational.
- **The defects are really present at pre_state.** Each anchor in
  `ground_truth/expected_findings.yaml` was read at `171148fd77` via `git show`
  (no checkout) and the line numbers recorded there were verified against that
  output. Spot examples: `AgentBadge.elm:281` opens the body with
  `if runStatus == "parked" then` before any `buildStatus` inspection;
  `AgentTicket.elm:1112-1113` binds `runStatus = forBuild |> List.head |> Maybe.map
  .status`;
  `Agent.elm:643` passes `r.buildId` as the row key; `Agent.elm:600` is
  `href ("#" ++ anchorId)`; `Concourse.elm:775` is a top-level
  `Json.Decode.field "name"`; `types.go:47-48` give failed/errored the single
  exit `{StateQueued}`; `AgentTickets.elm:151` subscribes to `OneSecond` and
  `FiveSeconds` and fires `FetchAgentTicketCosts` on the 5s branch.
- **One candidate claim was corrected.** The candidate asserted the reflection
  test (F13) was `fail_at_parent_pass_at_merge` "plausible". It is **not**:
  `e366c29a13` touches `atc/public_plan_test.go` only — neither `atc/plan.go` nor
  `atc/public_plan.go` changes — and every pointer field of `atc.Plan` at
  `171148fd77` already has a matching field and json tag in `Plan.Public()`'s
  anonymous struct. The new spec therefore **passes at pre_state**. F13 is a
  missing regression guard on a bug class, not a live defect, and is labelled
  `class: test-gap` with its own grading caveat in the rubric. This is the one
  place the mining pass over-promised.
- **Path correction.** The candidate located the branch scope doc at
  `docs/superpowers/plans/2026-07-18-agentic-ux-wave-2-scope.md`. That is its
  path on `main` **after** the fix commit renamed it. At `171148fd77` it is
  `AGENTIC_UX_WAVE_2_SCOPE.md` at the repo root — which is itself finding B2.
- **`bench/` does not exist at pre_state** (`git ls-tree 171148fd77 bench/` is
  empty), so the schema's self-hosted-corpus caveat is satisfied: replaying this
  case cannot expose the corpus or its answers.

### Why the pre-state is coherent as a review moment

`171148fd77` is the branch tip an hour before merge, immediately after pulling
the mainline in so the reviewer is looking at the tree that would actually land.
The wave is complete (slices 1–7 done; the scope doc's slice 8 is literally
"rebuild bundle, full build/test, **adversarial review workflow**"), the bundle
has been rebuilt (`199ab74973`), and nothing further was added before the audit.
A pre-merge review request at exactly this instant is not a reconstruction — it
is what the branch plan scheduled.

## Leakage analysis

`withheld: []` — nothing **present at pre_state** gives the findings away. The
real exposure risk here is *descendants*, not in-tree content.

### Withheld by construction (must not be reachable)

- **`e366c29a13` and everything after it.** The commit body is the answer key,
  verbatim and ranked. The pre_state ref is an ancestor of it, so a detached
  snapshot at `171148fd77` cannot reach it — but a *clone with refs* can:
  `git branch -a --contains e366c29a13` puts it on `agentic-ux-wave-2`, on
  `jetbridge` and on `main`. **Materialize this case as a detached checkout /
  archive at the pinned SHA with no other refs and no reflog.** This is the whole
  leakage story for this case.
- **`/Users/tdmtrader/.claude/projects/.../memory/project_agentic_ux_wave_2.md`.**
  The user's auto-memory restates all eleven findings and the audit protocol.
  Outside the repo, so it cannot leak through the snapshot, but it *will* be in a
  Claude Code session's context on this machine. Any run of this case must be
  executed without that memory file loaded, or the result is void. Flagged here
  because it is invisible to a purely git-based exposure audit.
  **Now declared** as `known_leak_channels: [project-auto-memory]` in
  `case.yaml` (fixup 2026-07-25) — both leakage auditors reached this channel
  independently, and the top-level index `MEMORY.md` alone already names F1, F2,
  F4 and F9 by mechanism, so suppressing only the detail file is not enough.

### Checked and found clean at pre_state

- `git grep -i "merge-gate\|merge gate" 171148fd77 -- docs notes ci` → one hit,
  `docs/superpowers/plans/agentic-platform/remainders/2026-07-17-dispatcher-budget-reconciler.md`,
  unrelated (a different plan's own gate).
- `git grep -i adversarial 171148fd77` over docs → `AGENTIC_UX_WAVE_2_SCOPE.md`
  (schedules the review; names no finding),
  `docs/superpowers/plans/agentic-platform/REVIEW.md` and
  `remainders/README.md` (the platform-plans review, F1–F39, a different
  subject entirely).
- No audit or findings document was added by `e366c29a13` — its only doc change
  is the rename of the scope doc. There is no in-tree artifact to withhold.

### Deliberately exposed, and why

- **`AGENTIC_UX_WAVE_2_SCOPE.md`.** In-tree at pre_state and included in
  `task/change.diff` (it is part of the wave's own diff). It names the U1–U24
  targets, the slice sequencing and the deferrals — i.e. the *intent* the review
  is supposed to judge against — and none of the eleven findings. Withholding it
  would make the case harder in an artificial way (a reviewer at the time had it)
  and would remove the basis for the rubric's "don't re-litigate the deferrals"
  precision check. Exposed on purpose; recorded here as a deliberate decision.

### Accepted natural hints (present, not amplified)

The branch history reachable from `171148fd77` contains
**`20b808a92e` — "fix(web): three bugs from adversarial review of the UX wave"**,
whose body names the three defects an *earlier, lighter* review round already
fixed. Two of them sit next to ground-truth findings: the parked-first ordering
it introduced is precisely what F1 over-corrects, and the build-id row keying it
introduced is precisely what F3 refines. An agent that reads `git log` gets a
nudge toward those two neighbourhoods.

This was left as-is. It is genuine at-the-cut context — the human reviewer had
the same log — and suppressing it would require rewriting the snapshot, which
breaks the "pins immutable SHAs" contract. It is not amplified: `task.md` says
only that "it has already been through one lighter review round, so the cheap
bugs are gone", without naming what that round touched. The three already-fixed
defects are recorded as `not_findings` N3/N4 so that re-reporting them is scored
as a duplicate rather than as recall.

### What was scrubbed from `task.md`

`task.md` is a reframing, not a transcript — the real trigger was a one-line
instruction to run the adversarial review workflow at the end of the branch
(scope doc, slice 8). Two rounds of scrubbing were applied:

1. **A "seams to pay attention to" section was written and then cut.** It listed
   four buckets — anything derived from an ordered list of rows; anything that
   survives a refetch (entered / armed / opened / expanded state); anything with
   a Go half and an Elm half; anything the wave made reachable for the first
   time. Each bucket maps almost one-to-one onto a ground-truth finding
   (F2+F3, F5+F6+F7, F9, F8). Keeping it would have converted the case from
   "find the defects" into "check these four buckets", which is a different and
   much easier task. Cut entirely.
2. **No finding, file, symbol or line number is named as suspicious.** The file
   list in "What the branch is" is an orientation to a 31-file diff the agent
   already has; it points at every surface the wave touched, not at the eight
   that contain defects.

3. **Third round, fixup 2026-07-25 — the residue of bucket (1).** The surviving
   sentence still read "Read the code as written rather than the intent stated in
   the comments — *several of these modules describe behavior that the code next
   to them does not quite implement*." The first half is a standing reviewing
   instruction; the italicised half is a case-specific assertion that
   comment-vs-code divergence is present *here*, which is exactly the shape of
   F1, F2, F3, F7 and F8 and is the fourth cut bucket leaking back in one
   clause. Replaced with a general form ("Judge the code as written, not the
   intent stated in comments, commit messages or the scope doc — a claim about
   behavior is only as good as the lines that implement it") that keeps the
   instruction and drops the tip-off.

What survived, and is authentic: the merge-gate framing, the general
judge-the-code-not-the-intent instruction, the generated-bundle exclusion (a
standing repo rule from `hack/build-web.sh`), the severity-by-operator-impact
rule, and the deliverable shape including the refuted list (the workflow that
produced the ground truth emitted one).

## Case shape and grading

- **Exposure manifest** = repo at `171148fd77` (detached, no other refs)
  − nothing withheld + `task/` (`task.md`, `change.diff`).
- `task/change.diff` is `git diff c764c395b2..171148fd77` with
  `web/public/elm.js`, `web/public/elm.min.js` and `web/public/main.css`
  excluded: 31 files, +1865/−234. With the bundles it is +7386/−4584, i.e. ~80%
  generated noise. Shipping the diff as a file (rather than only as a command)
  makes the case runnable by hand and pins exactly what was reviewed; the
  generating command is recorded in `case.yaml#pre_state.change` so drift is
  detectable.
- **Rubric is `reference` + `judge`.** Recall against
  `ground_truth/expected_findings.yaml`; precision judged per-finding because
  human findings are a weak precision oracle, with an explicit `not_findings`
  list (derived from what the reference fix *preserved*, plus what an earlier
  round had already fixed) as the only hard precision penalty.
- **Severities are curator-assigned.** The source commit ranked nothing; the
  ordering here comes from operator impact as argued in each bullet. The rubric
  says not to score severity mismatches as misses.
- **Finding count is 11 or 13 depending on resolution.** The commit's own header
  says eleven, and it has eleven bullets — but bullet 5 bundles two independent
  defects (armed-confirm staleness, stranded edit) and bullet 10 bundles two
  (cost-rollup cadence, post-terminal polling). `expected_findings.yaml`
  atomises to F1–F13 and records the bullet mapping so either resolution can be
  reported. Runs must state which they used.

## Open questions

- **The `not_findings` list is inferred, not recorded.** The audit's real refuted
  list (the mining candidate mentions ~40 candidates narrowed to 11 by six
  verifier agents) exists only in the originating session, not in the repo. N1
  and N2 are inferred from what the reference fix deliberately *preserved* — the
  `BuildStepUnknown` catch-all and attention-first ranking are both retained in
  `reference.diff`, so a report demanding their removal contradicts the ground
  truth. N3–N5 are grounded in code at pre_state (already-landed guards, and the
  server-side CAS). All five are defensible from artifacts; none is a transcript.
  A future case built from a review that committed its refuted list would give a
  much stronger precision oracle — worth mining for.
- **Elm test execution is unvalidated.** `elm` and `elm-test` are both on PATH on
  this machine, and `web/elm/elm.json` is a standard 0.19.1 application, but no
  `elm-test` invocation was run for this case (it needs a materialized tree, and
  the extractor treated the repo as strictly read-only). The reference fix's own
  claim of a green suite is from the prior round's commit ("Full elm-test suite:
  3126 passing"). If a grader wants to run the Elm suite, confirm the invocation
  first; do not assume `elm-test` from the repo root works.
- **This case cannot be mechanically graded on the agent's output**, only on the
  ground truth's reality. That is inherent to review cases: the deliverable is a
  document. The single mechanical command below proves F9 is a real defect (and
  proves the environment), nothing more. If the corpus later wants mechanically
  graded review cases, the shape to look for is a review whose findings were each
  fixed in a *separate* commit with a test — then each finding gets its own
  fail-to-pass.
- **Difficulty is `hard` on mechanical proxies** (31 files, ~1.9k added lines
  across Elm + Go, 13 defects, 8 distinct files containing them, several
  requiring reasoning about data that is not in the diff — e.g. that
  `agent_run_metrics` has one row per step, ordered). It may be *too* large for a
  single review turn; if pilot runs saturate at low recall because of budget
  rather than capability, the honest fix is a companion case scoped to one
  surface, not a rubric adjustment.
- **Pairs with `feedback-jb-001`.** That case is built from the same wave's
  feedback/fix loop; the two share `pre_state` lineage and should never be run in
  the same session, and results on them should not be pooled as independent
  samples. Cross-reference maintained here per curator instruction.
- Sibling candidate not built: `20b808a92e` (the earlier, lighter review round on
  the same branch, three findings) is the same shape one step back in history and
  would make a cheaper, smaller review case — with the caveat that its pre_state
  is an ancestor of this one's, so the two overlap heavily. Noted so it is not
  re-mined blind.

## Validation

### Extractor pre-check (informational — not the formal validation pass)

At the time this was written `case.yaml` recorded `validation.status:
unvalidated`; this subsection is the build-time sanity check only. The formal
pass below ran later the same day and moved the field to `validated`.

Method: three throwaway single-package Go modules under the scratchpad
(`module github.com/concourse/concourse`, `go 1.25.6`), each containing only
`agent/api/tickets/types.go` (+ a `types_test.go`) extracted with `git show`.
No checkout, no worktree — the repo was treated as read-only. Go 1.25.6
darwin/arm64, warm module cache, no network. `agent/api/tickets` has no Postgres,
cluster or network dependency.

| Tree | `types.go` | `types_test.go` | Result |
|---|---|---|---|
| `pre` | `171148fd77` | `e366c29a13` (withheld) | **FAIL** — `ValidTransition(failed, abandoned) = false, want true`; same for `errored` |
| `pre2` | `171148fd77` | `171148fd77` | **PASS** (baseline green) |
| `post` | `e366c29a13` | `e366c29a13` | **PASS** |
| `post2` | `e366c29a13` | `171148fd77` | **PASS** (no regression: `TerminalStates()` unchanged, failed/errored still non-terminal) |

Fail-to-pass and pass-to-pass both confirmed for F9's server half. The
ground truth for that finding is real and the environment is hermetic.

Note the isolation trick used above is a convenience for the extractor only. The
command recorded in `case.yaml#grading` runs the real package in the real module
and therefore needs the whole repository materialized.

Separately confirmed **negative**: F13's reflection spec is *not* fail-to-pass.
`e366c29a13` changes no production file under `atc/`, and all 24 pointer (step) fields
of `atc.Plan` at `171148fd77` already appear under their json tags in
`Plan.Public()`. Recorded in the finding itself so no grader wastes a cycle
looking for a red bar.

### Formal validation

- status: **validated** (mechanical legs only — see the caveat below)
- corpus commit validated against: the commit that seals this case directory
- recall oracle sanity (does a strong reviewer find ≥ 1 of F1/F2/F9?): **not
  run.** No pilot solver has been executed against this case, so the rubric's
  band table (§"Expected bands") is still a set of priors, as it says. This is
  the one validation leg that remains open; it does not affect the exposure
  audit or the mechanical corroboration.
- leakage audit (two independent models): opus `borderline`, sonnet `fail`,
  curator-fixup `pass` — see `case.yaml#leakage_audit` and the Fixup section at
  the end of this file. The disagreement was entirely about one
  operator-environment channel, now declared rather than papered over.
- notes: the run detail is immediately below.

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `171148fd77232d46d183834c9f2e172073c44805`, post `e366c29a1352b27a6537694ead2ae2e2f9779c91`
- outcome: **validated** (both legs; ground-truth corroboration only — this case's agent output is graded by reference+judge)

### fail_to_pass (F9 corroboration)
`cp <case>/ground_truth/withheld_tests/agent/api/tickets/types_test.go agent/api/tickets/types_test.go && go test ./agent/api/tickets/ -run TestValidTransitionMatrix -count=1`

PRE (FAIL, exit 1):
```
--- FAIL: TestValidTransitionMatrix (0.00s)
    types_test.go:49: ValidTransition(failed, abandoned) = false, want true
    types_test.go:49: ValidTransition(errored, abandoned) = false, want true
FAIL	github.com/concourse/concourse/agent/api/tickets	0.144s
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/agent/api/tickets  0.140s`

### pass_to_pass (no overlay)
`go test ./agent/api/tickets/ -count=1` — PRE `ok ... 0.141s`, POST `ok ... 0.141s`.

- corrected_cmd: the `cp ground_truth/...` source path must be absolute (the corpus checkout is not inside the materialized worktree):
  `cp /<corpus-checkout>/bench/corpus/review-jb-001/ground_truth/withheld_tests/agent/api/tickets/types_test.go agent/api/tickets/types_test.go && go test ./agent/api/tickets/ -run TestValidTransitionMatrix -count=1`
- notes: no Postgres, no cluster, no network; ~0.15s per leg. Whole repo must be materialized (main module).

## Fixup 2026-07-25

Curator pass over the dual leakage audit (opus `borderline`, sonnet `fail`).
Every audit item resolved; four further defects found while resolving them.
Residual verdict **pass**, with a declared leak channel and two run constraints.

### Dissolved by the exposure contract (no rename, no move)

- **The sibling case `feedback-jb-001/task/review-findings.md`** (opus's first
  item). It ships all thirteen findings inside a directory literally named
  `task/`, which reads alarming — but the exposure manifest is per case:
  `pre_state − withheld + task/` **of the case being run**. Another case's
  `task/` is no more exposed to this solver than this case's `ground_truth/` is.
  The two are a deliberate mirror pair (same `pre_state`, same terminal
  artifact, opposite exposure), which is the point of both. Nothing renamed.
  What *is* real is the co-run hazard, already recorded under Open questions;
  it is now enforced at the top of `case.yaml` as a second `RUN CONSTRAINT`
  (never co-run, never pool as independent samples).
- **`case.yaml`'s title, id and grading config naming the answer.** Harness-side
  per the exposure contract; the title may state the answer freely. Left as
  written. The corollary for hand-runs — materialize `task/` into a neutrally
  named directory — is the schema's, not this case's, to carry.
- **Terminal-artifact reachability** (`e366c29a13` on `agentic-ux-wave-2`,
  `jetbridge`, `main`; opus's second item). Already mitigated before the audit by
  `pre_state.repository.materialize` — detached `git archive`, no other refs, no
  reflog — and that instruction is already load-bearing enough to sit in
  `case.yaml` rather than only here. Nothing to add.
- **`bench/` in the subject repo.** Re-confirmed `git ls-tree 171148fd77 bench/`
  is empty; a replay at the pinned SHA cannot reach the corpus.

### Known leak channel (declared; not fixable in-package)

Both auditors independently landed on the same blocking item, and it is the only
thing separating opus's `borderline` from sonnet's `fail`: the curation machine's
project auto-memory summarizes this exact review round. The `MEMORY.md` index
entry alone ("Agentic UX wave 2") names four of the eleven fix bullets by
mechanism — `runOutcome` worst-truth-wins (F1), earliest-row masking (F2), dead
fragment nav (F4), the abandoned edge (F9) — and two of those are in the trio
{F1, F2, F9} the rubric's "strong" band keys on. Actions:

1. `known_leak_channels: [project-auto-memory]` added to `case.yaml`, with a
   comment naming which findings leak and why no git-side measure can close it.
2. `RUN CONSTRAINT` header replacing the merger's `# BORDERLINE` marker: a
   hand-run on this machine is **void**, not weakened, unless project memory,
   session context and conversation history are suppressed for the solver.
3. Pointer added in §Leakage analysis above, where the channel was already
   noted as a worry, so the two records agree.

**`memorization_risk: none` stands and is not contradicted** (sonnet argued it
was). That field is about the *subject* sitting in model weights — public,
pre-cutoff history. This subject is private, jetbridge-era, post-cutoff; nothing
about the auto-memory changes that. The schema tracks the operator-environment
channel separately for precisely this reason, and `case.yaml` now cross-
references the two fields so no future reader re-litigates it.

### Real defects fixed

1. **`task/task.md` — leading clause.** "several of these modules describe
   behavior that the code next to them does not quite implement" asserted the
   presence of the comment-vs-code defect class that F1/F2/F3/F7/F8 all belong
   to — the residue of a scrub bucket the curator had already cut wholesale.
   Softened to a general judge-the-code-not-the-intent instruction. The trigger
   is unchanged in kind: still adversarial, still a merge gate, still "verify
   before you report". Recorded as scrub round 3 above.
2. **`task/task.md` — missing delivery channel.** The deliverable was described
   in content terms only, with no channel; under `git archive` materialization
   there is no branch, no PR and no commit to attach a review to, so a hand-run
   had nowhere to put the report. Now: write it to `MERGE_GATE_REVIEW.md` at the
   repository root, and the review — not a fix — is the deliverable.
   `ground_truth/rubric.md` §3 gains a matching checklist line that explicitly
   does **not** discount content for a channel miss (a report delivered only as
   the run's final message is fully gradeable), and §4 now opens by saying a run
   that emits no code is complete, not incomplete.
3. **`case.yaml#grading` — overlay protocol unstated.** The `fail_to_pass` leg
   restores a withheld post-fix `agent/api/tickets/types_test.go` over the tree.
   Harmless for this case as written (the deliverable is a document), but rubric
   §4 invites code, and rubric §4 also names that very file as the thing that
   "pins both directions" — so an agent that wrote it would have had its work
   silently overwritten by the grader. Added `grading.protocol`: the overlay runs
   on a pristine `pre_state` materialization only, never over a tree the agent
   touched; mirrored in rubric §5.
4. **`case.yaml#grading.pass_to_pass` — spurious-pass hole.** `go test
   ./agent/api/tickets/ -count=1` is green over a *deleted* spec. Added
   `pass_condition`: the leg counts only if `types_test.go` is present and
   `TestValidTransitionMatrix` plus the `TerminalStates` assertions actually
   appear in `go test -v` output.
5. **`notes.md` internal inconsistency.** The extractor pre-check asserted
   "`case.yaml` still records `validation.status: unvalidated`" while `case.yaml`
   says `validated`; and the file carried two `## Validation` headings, the first
   holding an empty `### Formal validation` stub that the second silently
   superseded (also breaking the `notes.md#validation` anchor `case.yaml` points
   at). Merged into one section: the pre-check keeps its historical framing, the
   formal stub is filled in for real, and the one leg that genuinely has **not**
   run — recall-oracle sanity, i.e. no pilot solver yet — is now stated as open
   instead of blank.

### Priced-deflator doc: kept

`AGENTIC_UX_WAVE_2_SCOPE.md` stays exposed (it is in the wave's own diff and is
the intent the review is judged against; it names no finding). Per the fixup
rule, `ground_truth/rubric.md` §2 now instructs the judge to credit the causal
chain an agent draws from code at `pre_state` and never a paraphrase of the scope
doc's, a comment's or a commit message's account of what the code does.

### Not changed

- **Difficulty stays `hard`.** Neither auditor argued otherwise; both called the
  task honestly hard, and sonnet singled out F9 as needing real cross-file
  synthesis. A comment in `case.yaml` records the mechanical basis so the value
  is not re-derived. The standing worry that the case may be too *large* for one
  review turn is a companion-case question, not a difficulty label — it stays in
  Open questions where the curator put it.
- **`withheld: []`.** Nothing present at `pre_state` gives the findings away;
  re-checked the greps recorded in §Leakage analysis.
- **Findings, anchors, severities, `not_findings`.** Untouched — no audit item
  reached the ground truth, and editing it would invalidate the validation run.
