# Judge rubric — rca-jb-002

**WITHHELD.** Score the agent's diagnosis + change against the behaviours below.
Grade **intent and reasoning**, not diff similarity: the reference change is three
one-token edits in a YAML file, so textual overlap is a near-useless signal. An
agent that lands the same three edits for the wrong reason scores worse than one
that names the mechanism correctly and proposes a stronger remedy.

Read `answer.md` first.

## Scoring

Five buckets, weighted. Report each sub-score and the total out of 100.

| # | Bucket | Weight |
|---|---|---|
| A | Root cause identified | 35 |
| B | Decisive evidence used | 25 |
| C | Negative result on the application code | 15 |
| D | Fix | 15 |
| E | Verification named | 10 |

A run that scores 0 on A cannot exceed 25 overall regardless of the other
buckets — record the cap if it applies.

---

## A. Root cause identified (35)

**Full credit (35)** requires all three:

- A1. The failing tests were **not running the code under investigation** — the
  binary under test was built from stale source.
- A2. The stale artifact is the **CI task rootfs** (the `concourse-kind-runner`
  image supplying `/src` to the `k8s-integration-tests` and
  `k8s-behavioral-tests` tasks), **not** the `concourse-local:latest` application
  image that Phase 2a already refuted.
- A3. The mechanism is **mutable-tag reuse**: `build-kind-runner` pushes to a
  fixed tag (`v33`) and the worker serves that tag from cache, so a fresh push to
  the same tag is silently ignored.

**Partial credit:**

- 25 — A1 + A2, mechanism gestured at ("the image is cached / not being pulled")
  but tag mutability not named.
- 15 — A1 only: "CI ran stale code" without identifying which artifact. This is a
  real advance over the record and should be credited, but it is not the answer;
  the record already had "staleness" as a leading hypothesis.
- 0 — any of the following:
  - Re-asserts the **refuted** hypothesis (stale `concourse-local:latest` /
    `ensureConcourseImage` build-if-absent) as the root cause. The record refutes
    it explicitly for CI; restating it is a trap hit, not an answer. *(Note: the
    `ensureConcourseImage` footgun is real for local/reused environments and may
    be mentioned as a secondary finding without penalty — it just cannot be the
    answer to this run.)*
  - Concludes the FK guard or `IsForeignKeyViolation` is defective and proposes a
    code fix there (see C).
  - Concludes "a real GC race the guards don't cover" and adds a third guard.
  - Concludes the flake is inherent/environmental (DinD, K3s, timing) and
    proposes a retry.

## B. Decisive evidence used (25)

- B1 (15) — Uses the **absence** of the freshly-pushed instrumentation from the
  run output as evidence, and states the inference explicitly: instrumentation
  that is on the pushed branch and whose build job succeeded cannot be missing
  unless a different binary ran. Reasoning from evidence-of-absence is the
  discriminating skill this case tests; award this only if the agent actually
  argues it, not if it merely quotes the observation.
- B2 (5) — Reads `deploy/k8s-e2e-pipeline.yml` and cites the concrete coupling:
  `IMAGE_TAG="v33"` pushed by `build-kind-runner`, consumed by two
  `rootfs_uri: docker:///registry.home/concourse-kind-runner:v33` declarations,
  each followed by `cd /src` and `go build ./cmd/concourse`.
- B3 (5) — Corroborates with at least one of:
  - the file's own history of cache-busting tag bumps
    (`git log -- deploy/k8s-e2e-pipeline.yml` → v3, v5, v6, v29, v30, v32, v33);
  - the second failure landing on a **different** guarded surface
    (`PointToCheckedConfig` / `resources_resource_config_scope_id_fkey`), which is
    expected if neither guard is present and awkward if both are;
  - explicitly overturning the record's "Ruled out" premise that the behavioral
    task "recompiles from that `/src`" after a successful push — correctly
    separating *push succeeded* from *pull happened*.

Deduct up to 10 for asserting the mechanism with no evidentiary chain (a lucky
guess that reads as pattern-matching on "image tag"). Deduct up to 10 for
fabricating evidence not present in the two attached documents or the repository
— e.g. inventing log lines, build numbers, or registry digests.

## C. Negative result on the application code (15)

- C1 (10) — States plainly that `atc/exec/check_step.go` and `atc/db/errors.go`
  are **correct** and require no change, and that the CI signal that implicated
  them was invalid.
- C2 (5) — **Makes no change** to `atc/exec/check_step.go`, `atc/db/errors.go`,
  `atc/db/resource_config_scope.go`, or the FK-guard tests. Any weakening or
  removal of a guard, or any change to `IsForeignKeyViolation`'s signature or
  semantics, scores **0 for the whole bucket** and is called out as a constraint
  violation.

Defence-in-depth work that the track's own plan already scheduled — notably
handling FK violations on the native `atc/lidar/scanner.go` `SaveVersions` path —
is **not** a violation and is not scored here either way. Note it if present.

## D. Fix (15)

- D1 (10) — Applies the cache-bust: bump `concourse-kind-runner` from `v33` to a
  new tag in all three places in `deploy/k8s-e2e-pipeline.yml` (`IMAGE_TAG` plus
  both `rootfs_uri`s), and says the pipeline must be re-`set-pipeline`d for it to
  take effect. Award D1 in full for any equivalent forced-refresh that is
  actually effective (e.g. pinning by digest, tagging with the source SHA);
  the specific number `v34` is irrelevant.
  - Deduct 5 if only some of the three occurrences are changed — a partial bump
    leaves the consuming tasks on the cached tag and fixes nothing.
- D2 (5) — Names the durable remedy beyond the cache-bust: immutable
  tags/digests, tag derived from the source commit, or decoupling the tested
  source from the toolchain image (fetch via a git resource instead of baking it
  into the rootfs). This is **better than the ground truth** — the project needed
  a second bump (`v34→v35`) before landing exactly this structural fix. Award it.

Do not require the agent to have edited the track's `cgx.md`; note whether it did
(the task names
`forge/tracks/resource_config_scope_fk_leak_fix_20260530/cgx.md` as the place to
record the conclusion) but do not weight it. **Grade the diagnosis wherever it
appears** — the final reply, the `cgx.md` entry, or both. Score the union of the
two; do not deduct for the agent having used only one channel, and do not score
the same statement twice.

## E. Verification named (10)

- E1 (7) — States that the next run must contain the previously-absent
  instrumentation — the `Using Concourse image "…": <id> created=…` provenance
  line, and the `concourse-web` log dump on any failure — and that their presence
  is what proves current code is finally under test.
- E2 (3) — Notes that the FK question is only answerable *after* that, i.e. the
  flake may or may not persist and this fix does not by itself close the original
  bug.

---

## Hard fails (report explicitly)

- Proposes a test-side retry around `fly check-resource` as the **primary** fix
  (task constraint).
- Weakens/removes an FK guard, or changes `IsForeignKeyViolation` semantics
  (task constraint).
- Redesigns GC ordering or scope lifecycle (declared out of scope).
- Presents the refuted `concourse-local:latest` hypothesis as newly-discovered
  root cause without engaging with the record's refutation of it.

## Calibration notes for the judge

- The record contains a pre-cut hypothesis line — *"Leading hypothesis:
  deployed-binary / KinD image-propagation staleness"* — followed by a Phase 2a
  entry refuting it for CI. This is authentic at-the-cut content, deliberately
  retained. Because of it, the bare word "staleness" is **cheap**; A2 and A3
  (which artifact, and why the push didn't take) carry the discrimination.
- Do not reward an agent for reproducing the record's structure or vocabulary.
  Reward the one inferential step the record could not make.
- **Credit causal reasoning from evidence, never doc-quotation.** The snapshot
  contains authentic prior-art documents that make the *action* cheap without
  making the *diagnosis* cheap — notably
  `forge/tracks/k8s_e2e_ci_failures_20260407/plan.md`, which records a completed
  task *"Bump kind-runner image tag (v28→v29→v30) to bust K8s node image cache"*,
  and the older `fix_k8s_e2e_pipeline_kind_runner_build_and_test_execution_20260328`
  track, plus the tag-bump history of `deploy/k8s-e2e-pipeline.yml` itself. These
  are deliberately exposed: they are what the engineer had. But they mean an agent
  can reach the *right edit* by pattern-matching "CI is weird → bump the tag" with
  no diagnosis at all. Therefore: award A1/A2/A3 only for an argued chain that
  connects *this run's* evidence to the conclusion. A tag bump that arrives with a
  citation of the prior track (or the file's history) **in place of** an argument
  scores D1 and B3 at most, 0 on A — and the judge must record the phrase
  **"cargo-culted bump"** in the score report so the pattern is visible in
  roll-ups. Quoting the prior art *in addition to* an evidence chain is
  corroboration and is credited normally under B3.
- An agent that says "I cannot resolve this from the attached evidence; the next
  step is X" scores by whether X is the right experiment. If X is "check whether
  the task rootfs image is actually being re-pulled" that is a strong partial
  (treat as A1+A2, no A3, and B1 if the absence argument drove it). If X repeats
  the run-and-look step that was already run, it is a 0 on A — the agent did not
  read the evidence it was given.
