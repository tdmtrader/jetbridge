# Curation record — review-jb-002

## Provenance walk

Backed out of a review-fix commit in this repo's jetbridge-era history. The
review round itself was never committed as a document; the commit message of the
fix is the only surviving record of it, and it is unusually explicit.

| Role | SHA | Date | Subject |
|---|---|---|---|
| terminal artifact | `0b2410f5fc54ae0b6adaedf4b45af4cdcf8ecdb2` | 2026-07-19T20:24:18-07:00 | `review fixes: fail-safe dispatcher mode on DB read error + honest updated_by` |
| pre_state / head of change | `335faaf363cb5085471a0c797c537f09d636b9d2` | 2026-07-19T20:13:05-07:00 | `merge: dispatcher runtime-control elm ui` |
| base of change | `644184e3f011369f3da77dc82caee200bd8fd196` | 2026-07-19T13:46:04-07:00 | `docs(agentic): UX4 live execution log — A0-1 validated, dispatch findings` |
| feature commit (backend) | `0d1deeacaad0febd6360a95eb0df78c4b551272f` | 2026-07-19T20:11:20-07:00 | `feat(agent): dispatcher runtime control (off\|paused\|active)` |
| feature commit (elm) | `87467b31762ebfccd3445753b60cd46caeb48734` | 2026-07-19T19:56:31-07:00 | `feat(web): dispatcher runtime-control UI on the ticket queue page` |
| branch point of both | `6188b2a8c1e3b954434a82ae8c90423cb469c199` | 2026-07-19T08:25:56-07:00 | `Merge agent-elm-perf: …` |

### Verification performed

- `git show -s` on the terminal SHA confirms the commit message says exactly what
  the candidate claimed, in the reviewer's own words: *"Adversarial review (2
  lenses clean, 1 major, 1 minor)"*, then one paragraph per finding. Both
  findings, their severities, and the accepted remedies are stated there. This is
  the whole ground truth; nothing was inferred from the diff alone.
- `git show --stat` on the terminal SHA: exactly 5 files, +65/−9 —
  `agent/dispatch/mode.go` (+16), `agent/dispatch/mode_test.go` (+35, new test
  func), `agent/api/dispatcher/handler.go` (+4/−1),
  `agent/api/dispatcher/handler_test.go` (+5/−4), `atc/atccmd/command.go`
  (+5/−4). No docs companion, no version bump, no migration — nothing to strip
  from the reference diff.
- `git show -s --format=%P 0b2410f5fc` → `335faaf363…`, so pre_state is the
  literal parent: the reviewer's tree is exactly what the fix was applied to.
- `git log --graph 6188b2a8c1..0b2410f5fc` shows the real shape:
  `6188b2a8c1` → three docs-only mainline commits (`05ef24ec69`, `15e4027e50`,
  `644184e3f0`) → `9925f92c3b` (merge of the backend feature) → `335faaf363`
  (merge of the elm feature) → `0b2410f5fc`. Both feature commits branch off
  `6188b2a8c1`, not off each other.
- **Base-ref choice.** The candidate proposed `6188b2a8c1` as `before_sha`, which
  is the feature branch point but not the merge base of the mainline: a diff from
  there carries the three docs commits (10,580 + 196 + 17 lines of plan
  documents) as unrelated churn. `git show --stat` on each confirms all three
  touch only `docs/superpowers/plans/agentic-platform/**`, so pinning the base to
  `644184e3f0` yields a diff that is exactly the union of the two feature commits
  and nothing else. Verified: `git diff 644184e3f0..335faaf363` = 31 files
  (30 after excluding the generated Elm bundle), +1849/−14.
- **Defect really present at pre_state.** Read directly at `335faaf363`:
  `atc/atccmd/command.go:1424-1433` has the `modeResolver` closure whose error
  branch calls `dispatch.ResolveEffectiveMode(false, "", dispatcherBootFlag)`;
  `agent/dispatch/mode.go:34` shows `ResolveEffectiveMode` returns `ModeActive`
  for `found=false, bootFlag=true`; `agent/dispatch/dispatcher.go:104` shows the
  `ModeActive` arm calls `dispatchQueued`. The chain is real and complete.
  `agent/api/dispatcher/handler.go:116-118` has the `identity = "admin"`
  fabrication, and `handler_test.go:85` (`TestPutMissingUserDefaultsToAdmin`,
  *"want admin fallback"*) ships a test pinning it as intended.
- `git grep EffectiveModeFromRead 335faaf363` → no hits. The reference remedy
  does not exist at pre_state in any form.
- `git branch -a --contains 0b2410f5fc` lists `jetbridge` (the fork mainline) and
  `main`, so the review findings were accepted and shipped, not abandoned.
  `ground_truth.outcome: merged`.
- `git ls-tree 335faaf363 bench/` is empty — pre_state predates this corpus, so
  the schema's self-hosted-corpus caveat is satisfied.

### Pre-state coherence

`335faaf363` is a merge commit whose tree is the two feature branches combined,
which is precisely the state a pre-merge review pass sees. Both feature commits
are self-consistent (the backend landed the wire contract; the Elm commit was
written against it and its `fly/integration` and `elm-test` suites are green in
the tree). The three intervening docs commits are the UX-audit-№4 scoping work
from earlier the same day and touch nothing the change touches.

## Leakage analysis

`withheld: []` — nothing present at pre_state gives either finding away.

Checks run against the pre_state tree (all `git grep <pattern> 335faaf363`, no
checkout):

- `EffectiveModeFromRead` — zero hits anywhere in the tree.
- `updated_by` outside `docs/` and `web/public/` — 17 hits, all of them the
  feature's own plumbing (db factory, migration, handler, fly, Elm decoder, test
  fixtures using `"tdm"` / `"operator"`). None discusses the fallback's honesty.
- `docs/superpowers/plans/agentic-platform/REVIEW.md` (the big in-tree
  findings document) and `ci/dogfood/FINDINGS.md`: greps for `agent_settings`,
  `dispatcher_mode`, `runtime control` return zero hits. Neither anticipates this
  change; both predate it.
- `docs/superpowers/plans/agentic-platform/2026-07-19-ux4-scoping.md` (added by
  the base commit, so it is inside the exposure manifest): two `dispatcher`
  hits, both incidental — a note about tick behavior "guarded by the dispatcher
  flag family" and a principals `kind: operator|run` note. Nothing about a
  settings table, a read path, or a fallback.
- The candidate flagged
  `docs/superpowers/plans/agentic-platform/2026-07-19-dispatcher-runtime-control.md`
  as a possible in-tree plan leak. **It does not exist** — at `6188b2a8c1` or at
  `644184e3f0` the only dispatcher plan documents are `11-dispatch.md` and
  `remainders/2026-07-17-dispatcher-budget-reconciler.md`. This feature was built
  without a committed plan doc, which removes the usual in-tree-plan leak vector
  for this repo entirely.

### Adjacent but deliberately NOT withheld

`remainders/2026-07-17-dispatcher-budget-reconciler.md` (in tree at pre_state)
argues at length that *budget* store errors must propagate rather than
fail open: *"fail-open on a budget read error would bypass the step exec's
fail-closed design"*, with a test asserting it. This is the nearest thing in the
tree to F1, and it is one directory away from the change.

It is left exposed on purpose. It is a genuine, pre-existing house convention
that the actual reviewer had, it concerns a different subsystem (budgets, not
dispatcher mode), and it never mentions `agent_settings` or the dispatcher mode
resolver. Withholding it would sanitize the repo into something the reviewer
never worked in. It does raise the ceiling: a reviewer who reads the repo's own
conventions has a fair path to F1, which is the intended skill. Recorded here so
that a strong result on F1 is not over-read as unaided insight.

### Withheld from the task deliberately (subtractive only)

- **The finding text itself.** The terminal commit message states both findings
  in full; it and its descendants are withheld, as is the branch
  `feat/dispatcher-runtime-control` where later work sits.
- **The lens structure.** The review ran four angles ("2 lenses clean"), but
  their names are not recorded anywhere in the repo. `task.md` therefore asks for
  a generic adversarial pass rather than inventing lens names that would steer
  the search.
- **Any hint at the read path.** An earlier draft of `task.md` closed the
  motivation section with "…so the read path and the fallback semantics have no
  precedent to copy", which points straight at F1. Rewritten to "no in-tree
  precedent for this shape to copy from". `task.md` states the operational stakes
  (a kill switch on a loop that spends money and pushes branches) because the
  reviewer plainly knew them, but never says that the switch can be bypassed,
  never mentions DB faults, and never mentions `updated_by`.
- The out-of-repo memory file `reference_agent_dispatcher_control.md` describes
  the dispatcher flag/settings design and the runtime-toggle plan. It states no
  finding, but it is adjacent and post-dates parts of this work; it must not be
  in context for a run of this case.

**Grading tests.** `agent/dispatch/mode_test.go` and
`agent/api/dispatcher/handler_test.go` both exist at pre_state; the post-cut
material is `TestEffectiveModeFromRead` (new) and the rewrite of
`TestPutMissingUserDefaultsToAdmin` → `TestPutMissingUserRecordsUnknown`. Both
live only in `ground_truth/reference.diff`. Note that the pre_state versions of
those files are *inside the change under review* and one of them asserts the
defective behavior — that is intentional exposure, not leakage: catching a test
that pins a bug is part of reviewing the change.

## Open questions

- **Was the real review scoped to the backend commit only?** Both findings are in
  Go, and the fix's parent is the elm merge, so the change presented here is the
  full feature (backend + UI). Presenting only `0d1deeacaa` would concentrate the
  signal but would misreport what the reviewer had in front of them. Kept as the
  merged feature; if pilot runs show the Elm half is pure distraction with no
  discriminating value, a `review-jb-002b` restricted to the backend diff is the
  cheaper variant to build rather than editing this case (results-invalidation
  rule in `bench/README.md#corpus-versioning`).
- **Precision scoring is the shaky half.** Recall on F1 is crisp. Precision leans
  on "2 lenses clean", which bounds the *reported* findings but not the *findable*
  ones — the reviewer may simply have stopped. `rubric.md` §2 therefore requires
  unmatched findings to be judged individually before counting against a report.
  Whether that survives contact with an automated judge is untested.
- **`information_cut` for review cases is a different animal.** For a fix case
  the cut is the parent commit's timestamp and the task is the trigger. Here the
  cut is the timestamp of the tree *under review* (20:13:05), and the exposed
  change is by construction the newest thing in the manifest. Worth stating as a
  convention for the next review case rather than re-deriving it.
- **Sibling candidate not built.** `9925f92c3b`'s backend commit also introduced
  `atc/db/agent_settings.go` and the wrappa tier changes; the wrappa auth-tier
  table (`api_auth_wrappa_test.go`, +103) is the kind of surface where a missing
  route entry is a real security defect. Nothing was found there in this pass —
  possibly one of the two clean lenses. Noted so it is not re-mined blind.
- **F2 may be under-weighted by judges.** It is a `low`, it is four lines, and its
  harm is epistemic (the audit trail asserts something unknown) rather than
  functional. Expect reviewers to skip it; that is the intended discrimination,
  but a judge that treats "found 1 of 2" as a half-score will flatten the
  difference between a report that got the major and one that got only the minor.
  `rubric.md` marks F1 as `required` and F2 as `bonus` for that reason.

## Validation

### Extractor pre-check (informational — not the formal validation pass)

Run by the extractor at build time to confirm the ground truth is real before
sealing. Method: `git archive <sha> > t.tar && tar -x -C <dir>` into throwaway
trees (no checkout, no worktree — the repo was treated as read-only), Go 1.25.6
darwin/arm64, warm module cache.

| Tree | Contents | Command | Result |
|---|---|---|---|
| `pre` | pre_state `335faaf363` as-is | `go test ./agent/dispatch/ ./agent/api/dispatcher/ -count=1` | **PASS** — `ok … 0.743s`, `ok … 0.340s` (baseline green) |
| `pre_with_tests` | pre_state + both post-cut test files | same | **FAIL** — `agent/dispatch`: build failed, `undefined: dispatch.EffectiveModeFromRead` ×4; `agent/api/dispatcher`: `--- FAIL: TestPutMissingUserRecordsUnknown … want unknown sentinel` |
| `post` | terminal artifact `0b2410f5fc` as-is | same | **PASS** — `ok … 0.758s`, `ok … 0.337s` |

Fail-to-pass and pass-to-pass both confirmed. Total runtime ~1.1s, no Postgres,
no cluster, no network beyond the module cache.

Two environment gotchas found while doing this, recorded so they are not
mistaken for real signal:

1. `git archive <sha> | tar -x -C <dir>` silently produced a **corrupt, mixed
   tree** twice on this machine — the first attempt left files from an unrelated
   commit (`atc/db/build_being_watched_marker.go`, absent at both SHAs, mtime
   from 2025) and the second produced a `go.mod` from a different era
   (`go 1.24.2` where `git show 335faaf363:go.mod` is `go 1.25.6`), yielding
   spurious `undefined: Notification` build errors in `atc/db`. Writing the
   archive to a file first (`git archive <sha> > t.tar; tar -xf t.tar`) and
   md5-comparing `go.mod` against `git show <sha>:go.mod` produced correct trees
   every time. **Any harness that materializes pre_state through a pipe should
   verify a checksum before believing a build failure.** Both bogus failures
   looked exactly like a legitimately broken pre-state.
2. The two graded packages are in the main module, so the whole repository must
   be materialized; extracting only `agent/` will not build.

### Formal validation

_Stub — to be filled by the validation stage._

- status:
- corpus commit validated against:
- fail_to_pass observed:
- pass_to_pass observed:
- notes:

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `335faaf363cb5085471a0c797c537f09d636b9d2`, post `0b2410f5fc54ae0b6adaedf4b45af4cdcf8ecdb2`
- outcome: **validated** (both legs)

### Case-validation gate (fail_to_pass; NOT the agent rubric)
setup (read-only equivalent of the `git checkout <post> -- ...` in case.yaml):
```
git show 0b2410f5fc54ae0b6adaedf4b45af4cdcf8ecdb2:agent/dispatch/mode_test.go > agent/dispatch/mode_test.go
git show 0b2410f5fc54ae0b6adaedf4b45af4cdcf8ecdb2:agent/api/dispatcher/handler_test.go > agent/api/dispatcher/handler_test.go
```
cmd: `go test ./agent/dispatch/ ./agent/api/dispatcher/ -count=1`

PRE (FAIL, exit 1 — compile error on one half, red test on the other, exactly as documented):
```
# github.com/concourse/concourse/agent/dispatch_test [github.com/concourse/concourse/agent/dispatch.test]
agent/dispatch/mode_test.go:55:22: undefined: dispatch.EffectiveModeFromRead
agent/dispatch/mode_test.go:61:21: undefined: dispatch.EffectiveModeFromRead
agent/dispatch/mode_test.go:64:21: undefined: dispatch.EffectiveModeFromRead
agent/dispatch/mode_test.go:67:21: undefined: dispatch.EffectiveModeFromRead
FAIL	github.com/concourse/concourse/agent/dispatch [build failed]
--- FAIL: TestPutMissingUserRecordsUnknown (0.00s)
    handler_test.go:97: updated_by = 0x14000053d70, want unknown sentinel
FAIL	github.com/concourse/concourse/agent/api/dispatcher	0.344s
```
POST (PASS, exit 0):
```
ok  	github.com/concourse/concourse/agent/dispatch	0.715s
ok  	github.com/concourse/concourse/agent/api/dispatcher	0.333s
```

### Regression guard (pass_to_pass, no overlay)
`go test ./agent/dispatch/ ./agent/api/dispatcher/ -count=1`
PRE `ok 0.444s / ok 0.729s`; POST `ok 0.446s / ok 0.741s`.

- corrected_cmd: replace `git checkout <post> -- <files>` (mutates index) with the two `git show ... > ...` redirects above; behaviour is identical and the harness stays read-only.
  Shell note: under zsh, `git show $P:agent/...` mis-expands (`:a` is a path modifier) — inline the SHA or quote the whole argument.
- notes: no Postgres, no cluster; ~1.1s. Whole repo must be materialized (main module).

## Fixup 2026-07-25

Curator-fixup pass over the two-auditor leakage audit. Every audit item resolved;
nothing was renamed or retitled; `task/task.md` and `task/change.diff` were **not
edited** (both auditors found the exposed content clean, and sonnet independently
called it "appropriately hard").

### Dissolved by the exposure contract — no action

- **Sonnet's `fail`.** Its three grounds — the title stating both findings, the
  F1 conclusion inside `curation.learnings`, and `expect_at_pre` naming
  `dispatch.EffectiveModeFromRead` / `TestPutMissingUserRecordsUnknown` — are all
  `case.yaml`. Per `bench/schema/benchmark-case-v1.md` §"The exposure contract"
  the solver sees exactly *(pre_state − withheld) + task/*; `case.yaml`,
  `notes.md`, `ground_truth/` and the case id/path are harness-side and never
  exposed, and grading configs may state the answer freely. The verdict dissolves
  without any content change. The stale `# BORDERLINE: needs human spot-check`
  header was replaced with a note recording that resolution.
- **Opus's "never ship case.yaml".** Same dissolution; recorded as a standing
  hand-run instruction (present `task/` in a neutrally named directory) in a
  comment above `title` rather than as a defect.

### Real defects fixed

1. **Materialization could reach the answer** (opus's second curator action, the
   one that was *not* dissolvable). `git branch -a --contains 335faaf363` lists
   `feat/dispatcher-runtime-control`, `jetbridge` and `main` — every one of them
   carries the terminal fix `0b2410f5fc`, whose commit message states both
   findings verbatim. Any harness that materializes by checking out a branch
   hands the solver the ground truth. `pre_state.repository` now carries an
   explicit MATERIALIZE DETACHED instruction naming those three branches.
2. **Grading-overlay collision with the agent's own evidence** (new; neither
   auditor caught it). `task.md` requires a failing Go test per proven issue, and
   the two natural homes for those tests are exactly the two paths the validation
   overlay restores (`agent/dispatch/mode_test.go`,
   `agent/api/dispatcher/handler_test.go`). Applying the overlay to a solved
   workspace deletes the agent's proof and grades the reference against itself.
   Added CAVEAT 2 to `case.yaml` `grading.validation_only` (run both legs on a
   pristine pre_state) and a matching clause in `rubric.md` §3.
3. **Spurious-fail gate.** `pass_to_pass` runs the same command as the
   fail-to-pass leg, so on a solved workspace the agent's own (correctly failing)
   proving tests read as a regression. CAVEAT 3 in `case.yaml` and the same
   clause in `rubric.md` §3 scope the regression guard to a tree with
   agent-authored test files excluded.
4. **Manifest vs. validation-record inconsistency.** `case.yaml` still showed the
   index-mutating `git checkout 0b2410f5fc -- <files>` setup that the validation
   pass above had already replaced with read-only `git show` redirects. Aligned,
   including the zsh `:a` expansion note.
5. **Fix-variant grading flexibility.** `rubric.md` §4 already freed the exact
   name `EffectiveModeFromRead`; its header now states plainly that a
   fix-after-review variant is graded by the behavioral bullets and never by
   restoring the ground-truth tests, so the pinning noted in CAVEAT 1 cannot leak
   back into agent grading.

### Priced deflator — kept exposed, rubric hardened

`docs/superpowers/plans/agentic-platform/remainders/2026-07-17-dispatcher-budget-reconciler.md`
is in tree at pre_state and argues fail-open on a *budget* read error is wrong
(verified again this pass: four `fail-open` hits at that SHA, all budget-scoped).
Default KEEP applies — it is authentic house convention the real reviewer had, it
names neither `agent_settings` nor the mode resolver, and it does not
single-handedly collapse the task. `rubric.md` §1 now instructs the judge to
credit the causal chain anchored in this change's code, and to give no F1 credit
for quoting the convention alone. `withheld` stays `[]`.

### Difficulty

Unchanged at **moderate**, and the reasoning is now recorded inline in
`case.yaml`. No auditor argued for recalibration: opus called the exposure clean,
sonnet called the exposed task appropriately hard and failed only on harness-side
files. F1 requires disbelieving an in-code safety comment and tracing three hops;
the adjacent budget convention raises the ceiling without naming the subsystem.

### Known leak channel

Added `known_leak_channels: [project-auto-memory]`. The dev machine's project
auto-memory carries `reference_agent_dispatcher_control.md`, which describes this
feature's design (boot flag, DB hot-read gating `dispatchQueued`, `agent_settings`
singleton) and post-dates parts of the work. It states **neither** finding, so
this is a precaution rather than a proven spoiler — but the pre-existing note in
"Withheld from the task deliberately" already said it must not be in context, and
this makes that machine-readable. Memory was not modified.

### Deliberately not changed

- `task/task.md`. Its two most load-bearing passages were re-read against the
  ground truth: the "fallback seed" paragraph describes the *no-row* seeding,
  which `expected_findings.yaml` lists as a **non-finding**, and never mentions
  read errors; the kill-switch stakes paragraph justifies severity without
  naming a defect. Both are authentic change-summary content already visible in
  the diff. Softening them further would falsify the trigger.
- No delivery channel was added. This is not a decline case: the workflow's
  declared output port is `findings: review-findings/v1`, so the report is the
  return value, not a file the agent must place in the tree.
- `information_cut` (`2026-07-19T20:13:05-07:00`) re-verified equal to the
  pre_state commit timestamp of `335faaf363`, and `task.md`'s "Requested:
  2026-07-19" is consistent with it. No date reconciliation needed.
- `task/change.diff` re-grepped for post-cut symbols (`EffectiveModeFromRead`,
  `RecordsUnknown`, `fail-open`, `fail-safe`): zero hits.
