# The Bench corpus — benchmark cases for agentic workflows

Status: v0, started 2026-07-25. This directory holds hand-curated benchmark cases
for evaluating jetbridge's agentic workflows (small-fix, code-review,
version-upgrade, log-diagnosis, and the not-yet-seeded feedback-loop and triage
shapes). Format: [`schema/benchmark-case-v1.md`](schema/benchmark-case-v1.md).
Case list: [`corpus/INDEX.md`](corpus/INDEX.md).

Design lineage: the surviving principles of the superseded ticket-era bench
plans — fixtures, repetitions, versioned evaluators, controls, honest
reporting — as rebased by the approved
[workflows-as-functions design](../docs/superpowers/specs/2026-07-21-agentic-workflows-as-functions-design.md)
§"Experiments and benchmarks" and §6 (bench rebased on generic snapshots and
signatures). Fixtures in the running system are port→snapshot bindings
(`agent/experiment.Fixture`); this corpus is the file-based harvest form that a
future adapter imports into that system. Until then, cases are runnable by hand:
materialize `pre_state`, present `task/`, grade per `rubric`.

## The recipe

Every case is backed out of a **terminal artifact** — something with a known
outcome — by walking backwards:

```
terminal artifact (merged fix, revert, review round, closed ticket, incident)
  → reconstruct pre-state   : snapshot at time T, before the work began
  → derive task             : what the trigger would have carried at T
  → derive ground truth     : what humans actually produced after T
  → derive rubric           : mechanical | reference | judge | outcome
  → audit for leakage       : two independent models; disagreement → human review
  → seal as a fixture       : commit to bench/corpus/ with pinned SHAs
```

The discipline that makes or breaks a case is the **information cut**:
everything in the pre-state and task must have existed at T; everything used for
grading comes from after T and must never be reachable by the agent under test.

## Leakage checklist (each has bitten someone)

- **Solution in the prompt** — trigger text saying where/how to fix. Scrub or discard.
- **Tests in the snapshot** — grading tests present at pre-state spell out the answer.
- **Same-commit companions** — docs/CHANGELOG/version bumps written alongside the fix.
- **In-tree plans** — this repo writes design docs *before* implementing; a plan
  describing the fix, committed before T, is inside the exposure manifest. Either
  withhold it, or deliberately reframe the case as execute-a-written-plan.
- **Future state** — lockfiles/dep versions that only exist post-fix.
- **Branch contamination** — later commits reachable from the captured ref.
- **Memorization** — public pre-cutoff repos are in model weights. Upstream
  Concourse cases carry `memorization_risk: high` and must never anchor an
  efficacy claim on their own; private post-cutoff sources (this repo's
  jetbridge-era history, LightingDesign) are the trustworthy core.
- **Operator-environment leakage** — a channel the dual audit discovered in v0:
  the dev machine's project auto-memory (and CLAUDE.md-style context) states
  several ground truths verbatim for cases harvested from this repo's own
  incidents. Replay harnesses must not mount project memory, session context, or
  conversation history into the solver. Affected cases declare
  `known_leak_channels: [project-auto-memory]`; a local hand-run of those cases
  on this machine is invalid unless memory is suppressed.

## Validation

The filter that does most of the work: **validate the environment by validating
the case**. For mechanical cases, run the grading tests at pre_state (they must
fail) and with the reference change applied (they must pass). One check proves
the environment is hermetic, the ground truth is real, and the case is
non-trivial. Cases record `validation.status`; discarding 60–80% of candidates
is normal. Synthetic mutation (injecting bugs into known-good code) is a
supplement for smoke tests, never a substitute — perfect ground truth, terrible
realism.

## Sources

| Source | `repo:` | Memorization | Notes |
|---|---|---|---|
| jetbridge-era history of this repo (2026-06+) | `jetbridge` | none | richest source: real fixes, reviews, reverts, incidents, tickets |
| upstream Concourse history (pre-fork, public) | `concourse-upstream` | high | review/upgrade variety; label the risk, never headline results |
| ~/LightingDesign (private sibling repo) | `lightingdesign` | none | Go + non-code (cue audit) domains; tests exist |
| public post-cutoff repos | `pub` | low | future work: needs gh + hermetic per-repo containers |

## Corpus versioning

The corpus is sealed by git: a "corpus version" is a commit of this directory.
Results must cite the corpus commit they ran against so comparisons stay valid
across months. Do not edit a case's exposed content after results exist against
it — add a new case (or a new corpus version) instead. Dev/holdout splitting
starts when the corpus reaches ~150 cases; v0 is all-dev.

## What we are trying to learn (v0)

Two questions, deliberately entangled:
1. **What works well on the platform** — which workflow shapes, task sizes, and
   grading styles produce signal; where the platform lacks a runnable workflow
   (triage, feedback-loop) or a needed capability.
2. **What works well as a test case** — which terminal artifacts back out into
   clean cases; where leakage hides; what the discard rate per source is.
Each case records its lessons in `curation.learnings`; roll-ups belong in
`corpus/INDEX.md`.
