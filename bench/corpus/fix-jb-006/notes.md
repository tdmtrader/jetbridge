# fix-jb-006 — curation record

## Provenance walk

Backed out of a merged fix commit in this repo's jetbridge-era history.

**Terminal artifact.** `54767829cc273516b1d4919eac44dc4dafc7999b`
(2026-07-11T13:08:08-07:00, Thomas Moore),
`fix(configvalidate): reject negative run_retention values [review finding]`.
Reachable from `main`, `jetbridge`, `origin/main`, `origin/jetbridge` — verified
with `git merge-base --is-ancestor` against each. Never reverted; the change is
present at current HEAD.

Diffstat is exactly two files, +34/-0:

| file | change |
|---|---|
| `atc/configvalidate/validate.go` | +8: range checks for `KeepLast` / `TTLDays` inside `validateParamsSchema` |
| `atc/configvalidate/validate_test.go` | +26: one `It("rejects negative run_retention values")` spec |

The commit body (withheld from the agent) confirms the candidate's claim
verbatim: the two fields "were unvalidated: negative values passed set-pipeline
and only misbehaved later in the archival query".

**Pre-state.** `d1a8cdcf5331456bb73a87b7ce4d9cff40b63a69`, the parent
(2026-07-11T13:07:28-07:00) — `fix(db): stamp completed_at from CheckComplete's
read snapshot [review finding]`. `information_cut` is this commit's committer
timestamp.

Coherence checks at the pre-state ref:

- `atc/configvalidate/validate.go:588` has only the template-only guard
  (`run_retention is only allowed on template pipelines (set template: true)`);
  no range check anywhere in `validateParamsSchema`.
- `atc.RunRetentionConfig` (`atc/config.go:208`) is
  `{KeepLast int json:"keep_last,omitempty"; TTLDays int json:"ttl_days,omitempty"}`
  — plain ints, so negatives are representable and unmarshal fine.
- The consumer that misbehaves exists: `atc/db/pipeline_run_factory.go:351-362`
  archives runs via
  `WHERE (run_retention ? 'keep_last' AND rank > (run_retention->>'keep_last')::int)
  OR (run_retention ? 'ttl_days' AND completed_at < now() - make_interval(days => (run_retention->>'ttl_days')::int))`.
  A negative `keep_last` makes the rank predicate true for every run; a negative
  `ttl_days` moves the cutoff into the future. The symptom in `task/task.md` is
  read off this query, not invented.
- The validation path the task asks about is real and shared:
  `atc/api/configserver/save.go:64`,
  `fly/commands/internal/setpipelinehelpers/atc_config.go:64` and
  `fly/commands/internal/validatepipelinehelpers/validate.go:33` all call
  `configvalidate.Validate`.
- `atc/configvalidate/validate_test.go` already contains the
  `Describe("params schema validation")` block with its `validate` helper and
  `validJob` fixture, so the withheld spec drops in without new scaffolding.
- The suite (`configvalidate_suite_test.go`) is a plain Ginkgo suite with no DB
  wiring; `go test ./atc/configvalidate/` runs in ~0.25s with no PostgreSQL.
- `git ls-tree d1a8cdcf... -- bench` is empty: the pre-state ref predates
  `bench/` entirely, so the corpus (and this file) are not reachable through the
  exposure manifest. Satisfies the self-hosted-corpus caveat.

**Neighbourhood.** The commit is the last of four consecutive
`[review finding]` fixes (335ae3588a, 518194c96b, d1a8cdcf53, 54767829cc) closing
out the pipeline-runs wave (2fc3a8bf4b … 82647c7693, all 2026-07-11). The three
earlier findings are independent (run-number collision, destroyed-instance
completion, completed_at TOCTOU) and touch `atc/db` only — no overlap with this
case's file set, so pre_state is not contaminated by a half-applied sibling fix.
The immediately preceding commit is itself a fix, which is fine: it is fully
applied at the cut.

## Leakage analysis

**Withheld (never exposed).** The commit message and body — it states the defect,
the mechanism, and the fix in three lines. The added spec and the reference diff
live in `ground_truth/`.

**`withheld:` is empty** — nothing present at pre_state gives the answer away.
What I checked:

- `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (§7.1 item 4,
  line ~1637) and `03-pipeline-runs.md` (decision 6, Task 1, Task 10) describe
  the `run_retention` YAML carrier, the JSONB column, `RunRetentionConfig`'s Go
  shape, and the archival query. They document the *feature*. Grepping both for
  `negative` and for retention-adjacent validation language turns up nothing
  about range-checking `keep_last`/`ttl_days`. `03-pipeline-runs.md:2308` does
  discuss the `omitempty` subtlety (`KeepLast: 0` marshals to `{}`, so
  `keep_last: 0` is not representable in the JSONB and `RunsToArchive` skips it)
  — adjacent and useful context, but not the gap, and an agent reading it is
  reading the same thing the original author had.
- Other plans do contain the *phrasing pattern* the fix uses —
  `07-agent-step.md:1469` `budget_slice_usd must not be negative`,
  `05-workflow-store.md:544` negative-budget table-driven cases,
  `08-platform-mcp-hitl.md` bounds-validation prose. This is house style for
  numeric config validation across the platform and is genuinely part of the
  pre-state; it is prior art, not a pointer at this defect. It does, though,
  make the reference's exact error wording more guessable than it would be in a
  vacuum — worth knowing when reading a mechanical pass.
- No in-tree audit/review-findings file at the cut names this finding. The four
  `[review finding]` commits carry their findings only in commit messages, which
  are outside the exposure manifest (the agent gets a tree, not the log).
- No same-commit companions: no CHANGELOG, no version bump, no doc edit shipped
  alongside the fix.

**Deliberate steers in `task/task.md`** (added by me; the historical trigger was
a review comment, not a work item):

1. *"The fix belongs at config-validation time… clamping in the archival query
   is out of scope."* The symptom alone admits a defensible alternative — clamp
   or guard in the SQL — that the mechanical grader would score as a failure.
   Rather than let the grader punish a reasonable reading, the constraint is
   stated. It names a layer, not a file, function, or approach.
2. *Expected message shape* (`e.g. run_retention.keep_last must not be
   negative`). Needed because the withheld spec string-matches. Idiomatic for
   this repo — the contracts doc pins exact user-visible strings elsewhere (e.g.
   the 409 body for triggering a job on a template) — but it is still a steer,
   and it is the main reason this case is trivial rather than merely easy.
3. *"Report both errors"* and the zero/absent-values constraint. Derived from the
   reference's shape (a separate `if` block, not an `else` on the template
   guard). Both are behavioural requirements a competent reviewer would have
   written; they are what makes rubric items 3, 5 and 6 gradable.

Everything else in the task — the operator symptom, the archive-everything
mechanism, the YAML example — is derived from code that exists at the cut.

**What the task does NOT say:** the file, the function
(`validateParamsSchema`), that the check belongs next to the existing
template-only guard, or that `errorMessages` is the accumulator.

## Open questions

- **Wording brittleness.** `fail_to_pass` asserts the reference's literal
  strings. If a validated run shows agents landing the behaviour but missing the
  text, the honest fix is a second, wording-agnostic grading spec in
  `ground_truth/` rather than loosening the task further. Left as-is for v0
  because the task states the strings.
- **Is a trivial case worth a corpus slot?** Kept deliberately, as a floor/smoke
  anchor per curator guidance. If the corpus later grows a dedicated
  smoke-tier, this case belongs there rather than in the scored set.
- **Sibling cases.** The three other `[review finding]` commits in the same
  batch are plausible candidates at higher difficulty (all `atc/db`, all
  needing PostgreSQL). If any are built, cross-check that their pre_states do
  not accidentally include this fix — 54767829cc is the newest of the four, so
  they are all safely upstream of it.

## Validation plan (pre-run design; superseded by the run below)

Historical record of what the validation stage was asked to prove. Two details
here were later corrected and are struck through in effect — read the
"## Validation" section below as authoritative:

- the expected transitions were written against a `validate_test.go` overlay,
  which curator-fixup replaced with an additive overlay (see "## Fixup
  2026-07-25");
- the spec counts quoted below ("132 at post, 131 at pre") were guesses; the
  measured counts are 118 at pre and 119 at post.

Expected transitions (as designed):

- fail-at-pre: `d1a8cdcf5331456bb73a87b7ce4d9cff40b63a69` + the withheld spec
  applied → focused `go test ./atc/configvalidate/` run FAILS (no error message
  is appended, so `ContainElement` finds nothing).
- pass-at-post: `54767829cc273516b1d4919eac44dc4dafc7999b` → same command PASSES.
- regression guard: `go test ./atc/configvalidate/ -count=1` passes at both.

Environment: no PostgreSQL required; whole-suite wall time measured at ~0.24s at
HEAD, focused run ~0.001s of spec time. `-ginkgo.fail-on-empty` was confirmed to
turn a zero-spec selection into a `FAIL` — that flag is what keeps fail-at-pre
honest if the withheld spec is not applied.

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `d1a8cdcf5331456bb73a87b7ce4d9cff40b63a69`, post `54767829cc273516b1d4919eac44dc4dafc7999b`
- outcome: **validated** (all three legs), with a spec-count correction

### fail_to_pass
> **Superseded 2026-07-25 by curator-fixup**: the whole-file overlay below is no
> longer the grading recipe (it clobbers the spec task.md asks the agent to
> write). It is kept as the record of the run that proved the transition is
> real. The replacement additive overlay was re-validated the same day — see
> "## Fixup 2026-07-25".

setup: overlay the test half of the reference diff. The only difference between the two revisions of `atc/configvalidate/validate_test.go` is the added `It("rejects negative run_retention values")` block, so the whole-file overlay is equivalent:
`git show 54767829cc273516b1d4919eac44dc4dafc7999b:atc/configvalidate/validate_test.go > atc/configvalidate/validate_test.go`
cmd: `go test ./atc/configvalidate/ -count=1 -ginkgo.focus='rejects negative run_retention values' -ginkgo.fail-on-empty`

PRE (FAIL, exit 1):
```
[FAILED] Expected
  to contain element matching
Summarizing 1 Failure:
  [FAIL] params schema validation [It] rejects negative run_retention values
Ran 1 of 119 Specs in 0.001 seconds
FAIL! -- 0 Passed | 1 Failed | 118 Skipped
```
POST (PASS, exit 0): `Ran 1 of 119 Specs ... SUCCESS`

`-ginkgo.fail-on-empty` confirmed load-bearing — at pre WITHOUT the overlay:
```
$ ... -ginkgo.focus='rejects negative run_retention values' -ginkgo.fail-on-empty   -> FAIL (exit 1)
$ ... -ginkgo.focus='rejects negative run_retention values'                         -> ok   (exit 0, fake pass)
```

### pass_to_pass
`go test ./atc/configvalidate/ -count=1` — PRE `ok ... 0.227s`, POST `ok ... 0.228s`.
`go build ./atc/...` — exit 0 at both.

- corrected_cmd: none.
- **spec-count correction**: the recorded "131 specs at pre, 132 at post" is wrong for these SHAs. Measured: `Ran 118 of 118 Specs` at pre, `Ran 119 of 119 Specs` at post (so the focused leg reports `Ran 1 of 119`, not `Ran 1 of 132`). Grade on exit code, not on the absolute count.
- notes: no Postgres, ~0.23s per leg.

## Fixup 2026-07-25

Curator-fixup pass over the dual leakage audit (opus: borderline; sonnet: pass).
Every audit item resolved; residual verdict **pass**.

### Real defects fixed

1. **Grading-apply collision (opus's curator item 1).** `task/task.md` asks the
   agent to "Add a spec to the existing package suite", and `rubric.md` must-8
   scores that deliverable — but the grading recipe restored the terminal
   commit's whole `atc/configvalidate/validate_test.go` over the candidate tree,
   silently deleting the agent's spec. Worse, the old focus string
   (`rejects negative run_retention values`) is un-namespaced, so on a tree where
   the agent had written a similarly-named spec the focused run could have graded
   the *agent's own test* instead of the withheld oracle.
   Fixed by making the overlay **additive**:
   - new file `ground_truth/withheld_tests/zz_bench_fix_jb_006_test.go` — a
     self-contained Ginkgo spec (its own local `benchValidJob` fixture and
     `benchValidate` helper, no package-level identifiers, so it cannot collide
     with or be broken by anything the agent adds). Assertions are byte-identical
     in substance to `reference.diff`'s spec; only the names differ.
   - spec renamed to `bench-graded: rejects negative run_retention values`, and
     `grading.fail_to_pass[0].cmd` now copies the file in and focuses on that
     namespaced name.
   - `withheld_test_paths` now points at the new destination path
     (`atc/configvalidate/zz_bench_fix_jb_006_test.go`), not at
     `validate_test.go`.
   - `reference.diff` is unchanged — it stays the historical record of the human
     commit; it is no longer the grading apply.
   - `ground_truth/rubric.md` gained a judgement note: score must-8 from the
     submitted diff, and treat any run that patches `validate_test.go` as invalid
     for must-8.

   **Re-validated the same day** (read-only `git archive` of the pre_state SHA
   into a scratch tree; no worktree/checkout):
   - PRE + additive overlay → `FAIL`, `Ran 1 of 119 Specs`, failing on
     `ContainSubstring("run_retention.keep_last must not be negative")`.
   - PRE + `reference.diff`'s `validate.go` hunk + the overlay **plus a simulated
     agent-authored `It("rejects negative run_retention values")` inserted into
     `validate_test.go`** → `SUCCESS`, `Ran 1 of 120 Specs` (i.e. the focus
     selected exactly the graded spec, never the agent's), and the whole-suite
     `pass_to_pass` leg stayed `ok` with both specs present.
   - Overlay removed → the focused command still `FAIL`s, confirming
     `-ginkgo.fail-on-empty` is load-bearing for the new spec name too.

2. **Contradictory validation state in notes.md (opus's curator item 2, "an
   anchor should not remain unvalidated").** The file carried two `## Validation`
   sections: a stub ending `status: unvalidated` followed by the real, passing
   2026-07-25 run. The stub is now retitled "Validation plan (pre-run design;
   superseded by the run below)", the `status: unvalidated` line is gone, and its
   two wrong figures (the "131/132 specs" guesses) are called out as corrected to
   the measured 118/119. `case.yaml` already said `validation.status: validated`;
   the manifest and notes now agree.

3. **Stale grading commentary in case.yaml.** The `grading:` NOTE describing the
   `reference.diff` test-half apply was replaced with the additive-overlay
   procedure, plus a `caveats:` block (mirroring the corpus convention set by
   fix-jb-004) recording the exact-wording hazard and the necessary-not-sufficient
   scope of the gate.

### Dissolved by the exposure contract

- The audit's discomfort with the case *stating* the answer in `case.yaml` —
  `title: "Reject negative run_retention keep_last/ttl_days at config-validation
  time"`, the grading focus string, and the `withheld_test_paths` — is
  harness-side per the schema's exposure contract: the solver sees only
  (pre_state − withheld) + `task/`. Nothing renamed or retitled.
- Same for the case id/path `bench/corpus/fix-jb-006/`; and the self-hosted-corpus
  caveat is already satisfied (`git ls-tree d1a8cdcf -- bench` is empty).

### Difficulty recalibration — none (deliberately)

`difficulty: trivial` is retained. Opus is right that the task discloses both
graded error strings and names the fix layer, so the case measures execution
rather than diagnosis — but both steers are load-bearing: the withheld spec
string-matches, and the symptom alone admits a clamp-in-SQL fix the mechanical
grader would fail. That is the declared design of a calibration anchor, not a
defect, and `trivial` is exactly the honest label for it. What changed instead is
the *reporting* guard: `curation.learnings` now states flatly that the case must
**never** be cited as capability evidence — on its own or inside an aggregate —
and that its only legitimate use is as a floor/smoke signal reported separately
from scored cases. The same statement is now a header comment at the top of
`case.yaml` and a judgement note in `rubric.md`. `curation.learnings` also gained
a fourth transferable lesson: grading overlays must be additive whenever the task
asks the agent to write a test.

### Known leak channels — none declared

Neither auditor raised operator-environment leakage for this case, and a grep of
the machine's project auto-memory directory for `run_retention`, `keep_last` and
`ttl_days` returned nothing. No `known_leak_channels` key added. (The generic
README rule still applies: replay harnesses must not mount project memory.)

### Priced-deflator docs

Not applicable — `withheld: []` stands. The in-tree plans
(`00-shared-contracts.md`, `03-pipeline-runs.md`) document the `run_retention`
feature but not the missing range check; the `must not be negative` phrasing
elsewhere is house style for unrelated fields (both auditors agreed). The rubric
already directs the judge at behaviour rather than doc-quotation, and now also
asks for visible reasoning rather than a green gate.

### Files touched

- `bench/corpus/fix-jb-006/case.yaml` — header comment, `grading:` block
  (additive overlay + caveats), `leakage_audit` (curator-fixup entry),
  `curation.learnings` (capability-evidence prohibition + lesson 4).
- `bench/corpus/fix-jb-006/ground_truth/withheld_tests/zz_bench_fix_jb_006_test.go`
  — new; the collision-free graded oracle.
- `bench/corpus/fix-jb-006/ground_truth/rubric.md` — judgement notes.
- `bench/corpus/fix-jb-006/notes.md` — this section; validation-stub
  reconciliation; supersession banner on the old fail_to_pass recipe.
- `task/task.md`, `ground_truth/reference.diff`, `pre_state`,
  `information_cut` — **unchanged**. The exposed surface of the case is
  byte-identical to the sealed version, so results already recorded against it
  remain comparable (the corpus-versioning rule in bench/README.md).
