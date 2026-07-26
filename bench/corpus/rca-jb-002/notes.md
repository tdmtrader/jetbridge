# Curation record — rca-jb-002

## Provenance walk

Backed out of a **confirmed root cause with a recorded fix**: a day-long
investigation into a `k8s-e2e` behavioral flake that ended by discovering the
investigation's own instrument was never running. The fix and the narrative
landed 53 seconds apart.

| Role | SHA | Date | Subject |
|---|---|---|---|
| pre_state | `44697823b4e4c7b56b83e2e79e5b8052e0e244cb` | 2026-05-30T09:12:05-07:00 | `chore(forge): instrumentation in place; pushing to run instrumented chain` |
| terminal artifact (the fix) | `4cdf75c6ccf9d884ec3147696d33982dc89c827e` | 2026-05-30T11:53:22-07:00 | `ci(k8s-e2e): bump kind-runner image tag v33 -> v34 to bust stale worker cache` |
| terminal artifact (the narrative) | `c2a0b65d534daae8e02363b8664f8727bdcce92e` | 2026-05-30T11:54:15-07:00 | `chore(forge): record confirmed root cause — CI ran stale kind-runner images` |
| track closed (outcome) | `5de49fc2ea` | 2026-05-31T06:59:34-07:00 | `complete(resource_config_scope_fk_leak_fix_20260530): FK-leak resolved (image staleness root cause)` |
| structural follow-on | `c8407f87ab` | — | `complete(ci_reliability_k8s_e2e_20260530): source decoupled from toolchain image` |

Supporting commits inside the investigation, all ancestors of pre_state:

| SHA | Subject | Role in the case |
|---|---|---|
| `14b5bbaeb8` | scaffold the track | spec.md / plan.md / cgx.md appear |
| `ef4fc3f070` | `test(db,exec): regression coverage for FK-violation detection in scope GC race` | Phase 1 — real-DB proof the detection helper works |
| `b2d4621443` | `chore(forge): Phase 2a image-path audit — stale-binary hypothesis refuted` | the refutation that makes the case ambiguous |
| `ded0ca4ae7` | `test(k8s-behavioral): surface/avoid stale Concourse image reuse` | adds the `Using Concourse image …` provenance line |
| `e9de3901fe` | `test(k8s-behavioral): dump concourse-web logs on spec failure` | adds the on-failure web-log dump |

### Verification performed

Every claim in the mining candidate was re-checked against the repository. All
held; two were sharpened.

- **Topology.** `git rev-parse 4cdf75c6cc^` = `44697823b4` (pre_state is the
  direct parent of the fix). `git rev-parse c2a0b65d53^` = `4cdf75c6cc` (the
  narrative is the direct child of the fix). No rebase, no squash, no gap.
- **The terminal artifact says what the candidate claimed.** `4cdf75c6cc`'s body:
  *"The worker serves concourse-kind-runner by tag (v33) from cache, so
  build-kind-runner's fresh push to the same tag was ignored — integration and
  behavioral compiled Concourse from STALE /src."* Its diff is exactly 3 lines in
  `deploy/k8s-e2e-pipeline.yml`: `IMAGE_TAG="v33"` → `"v34"` and two
  `rootfs_uri: docker:///registry.home/concourse-kind-runner:v33` → `:v34`.
  `c2a0b65d53` adds the 35-line `## ROOT CAUSE CONFIRMED (2026-05-30)` section to
  `cgx.md` plus the plan checkboxes. Both confirm the candidate verbatim.
- **The pre_state is coherent as a diagnostic moment.** `44697823b4` is the
  commit whose own message is *"pushing to run instrumented chain"* — the
  investigation has exhausted static analysis, has just pushed the two
  instrumentation commits, and is waiting on a run. The plan at that SHA has the
  next step marked `[~]` (in progress): *"DECISIVE NEXT STEP (runtime): push to
  origin/jetbridge, rebuild kind-runner, run the full `k8s-e2e` chain."* This is
  not a reconstructed moment; it is the exact instant the trigger would have
  fired.
- **The mechanism is verifiable in the tree at pre_state.** `deploy/k8s-e2e-pipeline.yml`
  at `44697823b4`: `build-kind-runner` sets `IMAGE_TAG="v33"`, builds
  `--no-cache`, pushes `${REGISTRY}/concourse-kind-runner:v33` and then re-tags
  and pushes `registry.home/concourse-kind-runner:v33`; `k8s-integration-tests`
  and `k8s-behavioral-tests` each declare
  `rootfs_uri: docker:///registry.home/concourse-kind-runner:v33` and begin
  `cd /src` → `go build ./cmd/concourse`. The coupling the answer names is
  entirely visible to the agent.
- **The second failure's attribution checks out.** The instrumented run's error
  is `update resource config scope: set resource scope: …
  resources_resource_config_scope_id_fkey`. At pre_state
  `atc/exec/check_step.go:169` is `return false, fmt.Errorf("update resource
  config scope: %w", err)` inside the guarded `PointToCheckedConfig` block
  (:162-169), and `atc/engine/check_delegate.go:225` is `return
  fmt.Errorf("set resource scope: %w", err)`. The record's shorthand
  "check_delegate.go:225" resolves to `atc/engine/`, not `atc/exec/` — corrected
  in `task/evidence/instrumented-run.md`.
- **`bench/` does not exist at pre_state** (`git ls-tree 44697823b4 bench/` is
  empty), satisfying the schema's self-hosted-corpus caveat.
- **Candidate correction 1 — the "Phase 1 regression test" is not a grading
  transition.** The candidate listed `atc/db/errors_test.go` (added at
  `ef4fc3f070`) under `test_signal`. That commit is an *ancestor* of pre_state,
  so the test exists and is green at pre_state; it is evidence, not a
  fail-to-pass. Recorded as `pass_to_pass … grades: ground_truth_only`.
- **Candidate correction 2 — the fix needed a second bump.** The candidate says
  the fix is v33→v34. True, but `19b2dda76b` later bumps v34→v35 to ship an
  integration-test fix, and the track-closure commit `5de49fc2ea` describes the
  remedy as "the v33->v35 tag bump". This does not change the root cause; it is
  the clearest possible demonstration that the tag bump is a cache-bust rather
  than a cure, which is why `rubric.md#D2` awards credit for naming the
  structural remedy the project reached at `1e36ad10c0` / `c8407f87ab`.

## The run observations

`task/evidence/instrumented-run.md` is the one file in this case that is not a
verbatim artifact, so its sourcing is recorded line by line. The raw Concourse
build log was not archived anywhere (a platform gap, noted in
`curation.learnings`); the only surviving record is the human's post-run
transcription in `c2a0b65d53`. Every factual claim in the file comes from that
transcription, with interpretation stripped:

| Claim in `instrumented-run.md` | Source | Kept / moved |
|---|---|---|
| `build-kind-runner` #177 succeeded, pushed fresh after the push landed | `c2a0b65d53` cgx | kept (observation) |
| integration retry green 122/122 | `c2a0b65d53` cgx | kept |
| behavioral spec failed, different FK path, quoted error | `c2a0b65d53` cgx | kept verbatim, ellipsis and all |
| attribution to `check_step.go:169` / `check_delegate.go:225` | `c2a0b65d53` cgx | kept, path qualified after verifying it |
| `Using Concourse image …` absent; web-log dump absent | `c2a0b65d53` cgx | kept, reframed as a search result rather than a conclusion |
| old `ensureConcourseImage` string absent, "no-logs when the image exists" | `c2a0b65d53` cgx | kept **with** its neutralising caveat, so it cannot be misread as evidence |
| *"=> the behavioral task compiled Concourse from a STALE image's /src"* | `c2a0b65d53` cgx | **moved to `ground_truth/answer.md`** |
| *"Mechanism: the pipeline reuses the `concourse-kind-runner:v33` TAG; the worker serves a cached v33 by tag"* | `c2a0b65d53` cgx | **moved to `ground_truth/answer.md`** |
| *"Same disease as the registry.home/jetbridge:latest daemon image staleness"* | `c2a0b65d53` cgx | **moved** (it names the precedent that gives the mechanism away) |
| the fix, the anti-pattern paragraph | `c2a0b65d53` cgx | **moved** |

The file's header states plainly that it is a transcription, not a log dump, so a
grader is never misled about what kind of artifact it is.

## Leakage analysis

`withheld: []` — nothing **present at pre_state** gives the answer away. As with
`review-jb-001`, the whole exposure risk is *descendants*, plus one out-of-repo
channel.

### Withheld by construction (must not be reachable)

- **`4cdf75c6cc` and everything after it.** The fix's commit message is the
  answer in three sentences; `c2a0b65d53`, its direct child, is the answer at
  length. `git branch -a --contains 4cdf75c6cc` returns ~20 tips including
  `jetbridge` and `main`. **Materialize detached, no other refs, no reflog.** A
  plain clone of this repository hands the agent the answer key in one
  `git log -1` on the current branch.
- **The user's auto-memory on this machine** contains
  `project_jetbridge_release_pipeline.md`, whose summary line names *"stale
  embedded web bundle"* and, more importantly, the general "mutable tag →
  stale image" failure family that this repo has hit repeatedly. It is outside
  the repo so it cannot leak through the snapshot, but it will be in a Claude
  Code session's context on this machine. Any run of this case must execute
  without that memory loaded, or the result is void.

### Deliberately exposed, and why

- **`forge/tracks/resource_config_scope_fk_leak_fix_20260530/cgx.md`** (also
  copied into `task/evidence/`). This is the evidence bundle and the reason the
  case exists. It was written before the answer was known. Exposed whole.
- **The same track's `spec.md` and `plan.md`.** Both restate the record's
  hypotheses; `spec.md` ranks them ("1. Deployed-binary / image-propagation
  mismatch … 2. A genuine runtime path that bypasses the guard") and `plan.md`
  carries the `[~] DECISIVE NEXT STEP`. Left in place: they are the same
  information as the cgx, they are what the engineer had, and removing them would
  make the snapshot internally inconsistent (a track with a record but no plan).
- **`deploy/k8s-e2e-pipeline.yml` and its git history.**
  `git log -- deploy/k8s-e2e-pipeline.yml` at pre_state shows a long line of
  cache-busting tag bumps: *"bump kind-runner image tag to v3 to clear stale
  cache"*, *"bump kind-runner tag v4→v5 to bust worker image cache"*, *"bump
  kind-runner image to v29 to bust node cache"*, *"bump kind-runner to v32 to
  bust stale node cache"*, *"revert to registry.home with v33 tag to force fresh
  pull"*. This is a strong pointer, and it is **kept deliberately**: it is the
  domain knowledge the engineer had, it is exactly the artifact a competent
  diagnostician should go looking for, and finding it requires having already
  decided that the task rootfs is worth suspecting — which is the hard step. The
  rubric credits it as corroboration (B3), never as the finding itself.

### The priced leak

The record contains, pre-cut and honestly held:

> Leading hypothesis: deployed-binary / KinD image-propagation staleness
> (consistent with the `registry.home` daemon image rejecting `--mirror-*` flags
> it should support).

The mining pass flagged this as a partial leak and proposed truncating the bundle
before the `## Outstanding` section. **Rejected.** Three reasons:

1. Truncation would delete the Phase 2a refutation, which is what turns the hint
   into a trap. As it stands the bundle says "staleness" and then says "not
   staleness, we checked" — an agent that answers "stale image" without
   identifying *which* image has contradicted the document it was handed.
2. The refuted hypothesis is about `concourse-local:latest`, the **application**
   image built inside the behavioral task. The answer is about the **task
   rootfs**. These are different images in different registries consumed by
   different mechanisms; conflating them is the failure mode the case measures.
3. Truncation is invisible to a later reader and silently changes what the case
   measures.

Instead the leak is **priced in the rubric**: `rubric.md#A` declares the bare
word "staleness" worth 15/35 and puts the discrimination on A2 (which artifact)
and A3 (why the push did not take), and `rubric.md#Calibration` tells the judge
explicitly not to reward vocabulary borrowed from the record.

### What was scrubbed from `task.md`

The real trigger was a plan checkbox plus a returning CI run — no prose work item
exists. `task.md` is therefore a reframing, written to the constraint that it
must read exactly as it would have at 09:12 on 2026-05-30. What was deliberately
kept out:

- any mention of images, tags, registries, caches, rootfs, `/src`, or
  `deploy/k8s-e2e-pipeline.yml`;
- any hint that the fix is outside Go code — hence a phrasing that licenses a
  negative result without announcing one. This was flagged for the leakage
  auditors as the single most debatable line in the file. Both cleared it; the
  fixup pass tightened it anyway, because the original wording (*"If the evidence
  points somewhere other than the code under investigation, say so plainly"*)
  hypothesised the negative direction first. It now reads *"State where the
  evidence points, whether that is inside the code under investigation or outside
  it"* — same licence, no tilt. See `#fixup-2026-07-25`. The licence itself stays,
  because without it an agent may reasonably assume a code fix is mandatory while
  the task's own constraints forbid every obvious one;
- the word "absent" and any pointer at the instrumentation, other than the
  neutral table in `instrumented-run.md`.

What survived and is authentic: the symptom and build numbers, the prior track's
lineage, the constraint against retry wrappers and against redesigning GC (both
verbatim from the track's real `spec.md#Out of Scope`), the constraint against
weakening the guards, and the requirement to record the root cause in `cgx.md`
(verbatim from the real acceptance criteria).

## Case shape and grading

- **Exposure manifest** = repo at `44697823b4` (detached, no other refs)
  − nothing withheld + `task/` (`task.md`, `evidence/diagnostic-record.md`,
  `evidence/instrumented-run.md`).
- **Rubric is `judge`.** There is no fail-to-pass: the reference change is three
  YAML tokens whose effect is only observable on a live worker pulling from
  `registry.home`. Diff similarity is equally useless at three tokens — an agent
  could land the identical edit by guessing "bump the tag" from the file's
  history without understanding anything. So the rubric weights root cause (35)
  and evidence (25) above the fix (15).
- **Difficulty `hard`** on mechanical proxies: 1 file and 6 changed lines, but
  the answer is in neither the failing test nor the failing code; the bundle
  contains a correctly-refuted near-miss hypothesis; and the decisive step is
  reasoning from evidence of absence. Small diff, large inferential distance.
- **This case doubles as a negative** for `atc/exec/check_step.go` and
  `atc/db/errors.go`: the correct action there is *no change*, and `rubric.md#C`
  scores that explicitly. `ground_truth.outcome` is still `merged` (a real change
  landed elsewhere), so it is not a `no-change-correct` negative in the schema's
  sense — but it exercises the same "do not manufacture a change" behaviour on a
  specific, strongly-implicated surface.

## Open questions

- **The information cut is genuinely fuzzy here.** `information_cut` is set to
  the pre_state commit timestamp per the schema, but the log bundle is an
  observation of a run that finished roughly 2h40m later (bounded above by the
  fix at 11:53). This is inherent to log-diagnosis: a log is by construction
  produced *by* the snapshot and therefore *after* it. The invariant actually
  enforced is stricter than the timestamp and was enforced by hand: the bundle
  contains no information that postdates the *diagnosis*. See
  `#the-run-observations` for the observation/inference split. If the corpus
  grows more log-diagnosis cases the schema should probably gain an explicit
  `observation_window` rather than pretending a single instant covers it.
- **`instrumented-run.md` is a transcription, not a capture.** Its factual
  content is fully sourced (table above) but a second party cannot re-derive it
  from a raw artifact, because no raw artifact survives. A future case built on a
  workflow that *persists* the log it reasoned over would be strictly stronger.
  Treated as a known, disclosed weakness rather than hidden.
- **The `pass_to_pass` commands are unvalidated as written.** The extractor
  treated the repository as strictly read-only and never materialized a tree, so
  `ginkgo ./atc/exec/ --focus='FK violation'` has not been executed. The focus
  string was verified to match two real contexts (`check_step_test.go:734,766`)
  and `atc/exec` was verified to have no `postgresrunner` dependency, but the
  invocation itself is a prediction. The `atc/db` one additionally needs
  PostgreSQL.
- **Judge variance is the main risk.** With no mechanical anchor, the score is
  whatever the judge says. The rubric mitigates with a weighted five-bucket
  breakdown, an explicit trap list, and a score cap when bucket A is 0 — but a
  pilot should run the same transcript past two judges and report the spread
  before this case is used to compare agents.
- **Sibling candidate not built:** `c8407f87ab` /
  `ci_reliability_k8s_e2e_20260530` (the follow-on track that decoupled the
  tested source from the toolchain image) is a design-shaped case on the same
  subject. It would overlap heavily with this one and its pre_state is a
  descendant of this one's, so the two must never be pooled as independent
  samples. Noted so it is not re-mined blind.
- **`fix-jb-*` overlap check not performed.** Other cases in this corpus were
  built in parallel by sibling extractors; if any of them pins a pre_state in the
  2026-05-30 window on `jetbridge`, the two should not be run in the same session.

## Validation

### Extractor pre-check (informational — not the formal validation pass)

`case.yaml` records `validation.status: unvalidated`; this is a build-time sanity
check only.

**What was checked:** the negative half of the answer — that
`db.IsForeignKeyViolation` at pre_state really does detect the error shapes seen
in *both* failing builds, so "the detection helper is broken" is a wrong answer
and the FK code needs no change.

Method: a throwaway single-package module in the scratchpad (`module fkcheck`,
`go 1.25.6`) containing only `atc/db/errors.go` extracted with `git show` at
`44697823b4`, plus a probe test. No checkout, no worktree; the repository was
treated as read-only.

| Input | `IsForeignKeyViolation` |
|---|---|
| raw `&pgconn.PgError{Code: "23503"}` | **true** |
| `fmt.Errorf("save versions: %w", err)` — build #100's shape | **true** |
| `fmt.Errorf("update resource config scope: %w", fmt.Errorf("set resource scope: %w", err))` — the instrumented run's shape | **true** |
| `nil`, and an unrelated error | **false** (no false positive) |

`PASS`. The helper handles the doubly-wrapped shape from the *second* failure,
which the original investigation never probed — so the negative result holds for
both builds, not just #100.

Caveat: the throwaway module resolved `github.com/jackc/pgx/v5` to `v5.10.0`
whereas the repo pins `v5.8.0` (and `pgerrcode` matched the pinned pseudo-version
exactly). The behaviour exercised — `errors.As` onto `*pgconn.PgError` and its
`Code` field — is stable across those patch releases, but this is a difference
from the real module graph and is recorded rather than glossed.

Not checked: the two `pass_to_pass` ginkgo invocations (see
`#open-questions`), and anything requiring a cluster.

### Formal validation

_Stub — to be filled by the validation stage._

- status:
- corpus commit validated against:
- judge agreement (two independent judges on the same transcript, score spread):
- leakage audit (two independent models, both run; both blocking findings landed
  on `case.yaml` and dissolved under the exposure contract — see
  `#fixup-2026-07-25`. The `task.md` line they were asked to weigh has since been
  rewritten symmetrically; a re-audit should re-check it as written now):
- pilot result (does a strong agent reach A2/A3, or stop at A1?):
- pilot must additionally report: did the agent bump the tag WITHOUT an evidence
  chain (the "cargo-culted bump" outcome the rubric now names)?
- notes:

## Fixup 2026-07-25

Curator-fixup pass over the two leakage audits. Every audit item resolved; the
case is not quarantined. Residual verdict: **pass**.

### Dissolved by the exposure contract (no action, deliberately)

Both auditors' only blocking findings were about `case.yaml` itself:

- opus: *"case.yaml's title states the root cause verbatim ('reuses a mutable
  image tag')"*;
- opus + sonnet: *"its source comment quotes the companion commit's 'ROOT CAUSE
  CONFIRMED' line"* — sonnet's whole `fail` verdict rests on this pair, and its
  own notes say `task/` and both evidence files are legitimate.

Per `schema/benchmark-case-v1.md#the-exposure-contract`, the solver sees exactly
(pre_state − withheld) + `task/`; `case.yaml`, `notes.md`, `ground_truth/` and
the case id/path are harness-side and never exposed, so a manifest may state the
answer freely. **Nothing was renamed or retitled** — a descriptive title and a
`materialize:` comment naming the answer-key commit are how the manifest earns
its keep, and blurring them would make the case harder to run correctly. Recorded
as dissolved in a header comment on `case.yaml`. The residual obligation is the
one the manifest already carries: materialize detached, no other refs, no reflog.

### Real defects fixed

1. **Prior art was exposed but not priced** (opus: *"forge/tracks/k8s_e2e_ci_failures_20260407/plan.md
   records 'Bump kind-runner image tag … to bust K8s node image cache' as prior
   art, so credit only a causal argument, never a cargo-culted bump"*). Verified
   at pre_state: `forge/tracks/k8s_e2e_ci_failures_20260407/plan.md:22` carries
   that completed task, and `fix_k8s_e2e_pipeline_kind_runner_build_and_test_execution_20260328/`
   is a whole track about the same image. Both **KEPT** — authenticity wins, and
   they are exactly what the engineer had; `withheld` stays `[]` because neither
   doc collapses the task (they make the *edit* reachable, not the *diagnosis*).
   The leak is priced instead: a new bullet at the top of
   `ground_truth/rubric.md#Calibration notes for the judge` tells the judge to
   credit causal reasoning from evidence and never doc-quotation — a tag bump
   arriving with a citation of the prior track *in place of* an argument scores
   D1/B3 at most, **0 on A**, and the judge must write the phrase "cargo-culted
   bump" in the report so the pattern shows up in roll-ups. Citing the prior art
   *alongside* an evidence chain remains ordinary B3 corroboration.
2. **No named delivery channel for the diagnosis — including its negative half.**
   `task.md` asked for "a short written diagnosis … plus the change itself" and,
   in Constraints, to "record it in the track's `cgx.md`", but never said which
   file or whether the reply or the file was the artifact; `rubric.md#D` said not
   to weight the cgx edit at all. An agent that wrote its whole (possibly
   "no change here") conclusion into `cgx.md` and said little in its reply could
   be graded on the wrong surface. Fixed on both sides:
   - `task.md` now names `forge/tracks/resource_config_scope_fk_leak_fix_20260530/cgx.md`
     in Constraints and adds a "Where to put it" paragraph to Deliverable,
     including explicitly that a "no change here, and here is why" conclusion is
     a result to be recorded with the same explicitness as a change. This is the
     authentic ask sharpened, not a new requirement — the real acceptance
     criteria required the root cause in `cgx.md`.
   - `rubric.md#D` now says to grade the **union** of the reply and the `cgx.md`
     entry, not to deduct for using only one channel, and not to double-count.
     The cgx edit itself stays unweighted, so the graded surface is unchanged.
   - `case.yaml#grading` records both as explicit grading caveats.
3. **Asymmetric phrasing in the ask.** `task.md` item 1 read *"If the evidence
   points somewhere other than the code under investigation, say so plainly …"* —
   flagged in `#what-was-scrubbed-from-taskmd` as the file's most debatable line,
   and cleared by both auditors, but it hypothesises the negative direction first.
   Rewritten symmetrically: *"State where the evidence points, whether that is
   inside the code under investigation or outside it; either conclusion is
   acceptable and useful, provided it is argued rather than assumed."* The
   licence to return a negative result survives (without it an agent may assume a
   code fix is mandatory while the constraints forbid every obvious one), the
   tilt does not. The `atc/exec/check_step.go` / `atc/db/errors.go` name-drop was
   dropped from that sentence as redundant — both files are already named in
   "Where the investigation stands".
4. **Manifest/date consistency for the log bundle.** `information_cut` **stays**
   at the pre_state commit timestamp (2026-05-30T09:12:05-07:00). The one exposed
   input that is not literally "state before T" —
   `task/evidence/instrumented-run.md`, an observation *of* that snapshot produced
   ~2h40m later — is now documented in `case.yaml` right under the cut, with the
   observation window `[09:12:05, 11:53:22]` as a comment (the v1 schema has no
   such field) and a pointer to the observation/inference split. Re-verified while
   editing: every date appearing anywhere in `task/` is 2026-05-30, the day of the
   cut, so there is no internal date inconsistency.

### Known leak channel declared

`known_leak_channels: [project-auto-memory]` added to `case.yaml`, per
`README#Leakage checklist` → *Operator-environment leakage*. This formalises what
`#leakage-analysis` already said in prose. Checked during the fixup rather than
assumed: the machine's project memory does **not** state this answer — no memory
file mentions `concourse-kind-runner`, a stale task rootfs, or this FK flake. It
does prime the failure family hard enough to matter:
`project_jetbridge_release_pipeline.md` records a build silently shipping a stale
checked-in artifact ("stale embedded web bundle"), and
`project_theborg_disk_dns_outage_20260717.md` names mutable `kind-runner` tags in
the registry. Memory was not modified and must not be. A hand-run of this case on
this machine is void unless project memory and session context are suppressed.

### Difficulty

Held at **hard**; reasoning recorded inline in `case.yaml` above
`memorization_risk`. The prior-art finding lowers the cost of the *action*, not
of the *diagnosis*; the discriminating steps (A2 which artifact, A3 why the push
was not consumed, both reached from evidence of absence) are untouched, and the
scoring already refuses to pay for the action alone (A+B = 60/100, hard cap of 25
when A is 0, and now the explicit cargo-cult clause).

### Files edited

- `case.yaml` — header comment (dissolution), observation-window comment under
  `information_cut`, grading caveats, difficulty reasoning, `known_leak_channels`,
  `leakage_audit` fixup entry.
- `task/task.md` — item 1 desymmetrised; Constraints bullet given the concrete
  `cgx.md` path; Deliverable given a "Where to put it" paragraph.
- `ground_truth/rubric.md` — prior-art/doc-quotation calibration bullet; D-bucket
  union-grading clause.
- `notes.md` — this section.

Not touched: `task/evidence/*` (both verbatim/sourced; `diagnostic-record.md`
re-diffed against `git show 44697823b4:forge/tracks/…/cgx.md` during the fixup —
identical apart from its 8-line provenance header), `ground_truth/answer.md`,
`ground_truth/reference.diff`, `withheld` (still `[]`), `validation.status`
(still `unvalidated`).

The formal-validation stub lives at `#validation` above and is unchanged in
substance by this pass — the case is still `unvalidated`.
