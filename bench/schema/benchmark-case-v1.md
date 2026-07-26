# benchmark-case/v1 — corpus case schema

Status: v0 working format, 2026-07-25. This is the **harvest format** — the file-based
precursor a future harvest adapter converts into sealed snapshots and
`agent/experiment` fixtures (`Fixture{label, role, inputs: port -> snapshot ID}`).
It deliberately mirrors the platform's v3 function signatures so every case is a
future experiment fixture, not just a document.

## Case directory layout

```
bench/corpus/<case-id>/
  case.yaml            # the manifest (this schema)
  task/                # EXPOSED: everything the agent may see besides the repo snapshot
    task.md            # scrubbed trigger content (work-item body, review request, ...)
    ...                # optional extra exposed inputs (log bundle, upgrade request, ...)
  ground_truth/        # WITHHELD, ALWAYS: grading material from after the cut
    reference.diff     # reference change (fix/upgrade/feedback cases)
    expected_findings.yaml  # anchored findings (review cases)
    answer.md          # named root cause / decision (rca/triage/negative cases)
    rubric.md          # behavioral checklist for judge grading
  notes.md             # curation record: provenance walk, leakage audit, validation log
```

## case.yaml fields

```yaml
schema: benchmark-case/v1
id: fix-jb-001                  # <workflow>-<source>-<NNN>; source: jb|cc|ld|pub
title: one-line human name
workflow: small-fix             # small-fix | code-review | version-upgrade |
                                # log-diagnosis | feedback-loop | triage
signature:                      # explicit typed ports (v3 snapshot types)
  inputs:  {repository: repository/v1, work-item: work-item/v1}
  outputs: {change: repository-change/v1}
source:
  system: git-commit | github-pr | incident | ticket | audit-doc
  repo: jetbridge | concourse-upstream | lightingdesign | <public url>
  terminal: <sha | doc path>    # the terminal artifact this case was backed out of
information_cut: <ISO8601>      # instant T; everything exposed existed before T
pre_state:                      # one entry per input port; how to materialize it
  repository: {repo: jetbridge, ref: <parent sha>}
  work-item:  {path: task/task.md}
ground_truth:
  outcome: merged | reverted | closed-unmerged | wont_fix | no-change-correct
  reference_change: ground_truth/reference.diff     # optional
  expected_findings: ground_truth/expected_findings.yaml  # optional
  answer: ground_truth/answer.md                    # optional
rubric: mechanical | reference | judge | outcome
grading:                        # rubric-specific detail
  fail_to_pass:                 # mechanical: tests that fail at pre_state, pass with ground truth
    - {cmd: "go test ./atc/foo/ -run TestBar", withheld_test_paths: [...]}
  pass_to_pass:                 # regression guard: must pass before AND after
    - {cmd: "go test ./atc/foo/"}
withheld: []                    # paths PRESENT at pre_state that leak the answer and
                                # must be excluded from exposure (in-tree design docs,
                                # audit findings, plans describing the fix)
difficulty: trivial | moderate | hard
memorization_risk: none | low | high   # high = public pre-cutoff history
known_leak_channels: []                # optional; e.g. project-auto-memory (see README)
validation:
  status: validated | partial | unvalidated
  notes: notes.md#validation
leakage_audit:                  # two independent models; disagreement = borderline
  - {auditor: <model>, verdict: pass | borderline | fail, notes: "..."}
curation:
  quality: 1-5
  learnings: "what this case teaches about good cases / platform gaps"
```

## Semantics that matter

**The exposure contract.** The solver sees exactly: (pre_state materialized at
its pinned refs) − (withheld paths) + (task/ directory). Everything else in the
case directory — `case.yaml`, `notes.md`, `ground_truth/`, and the case id/path
itself — is harness-side and never exposed. Consequences: case titles and
grading configs may state the answer freely; but anyone running a case BY HAND
must materialize `task/` into a neutrally-named directory, because paths like
`neg-jb-001/` announce the expected outcome.

**The information cut.** `information_cut` is the authoritative line: everything
exposed existed before it. Nothing in `ground_truth/` is ever exposed. For fix cases the
grading tests usually do not exist at pre_state (they were written with the fix),
so they live only in `ground_truth/` — `withheld` is for the subtler leak: content
that *does* exist at pre_state and gives the answer away (an in-tree plan or audit
doc describing the fix). If the in-tree plan IS the task (execute-a-written-plan
cases), say so in `curation.learnings` — that is a different skill being tested,
not a leak, but it must be deliberate.

**Rubrics.**
- `mechanical` — fail-to-pass / pass-to-pass test transitions, or build success.
  Strongest signal; requires validated environment.
- `reference` — compare against ground-truth findings/diff (recall for reviews;
  remember human findings are a weak oracle for precision — an unmatched agent
  finding may still be true).
- `judge` — LLM judge scores against `rubric.md` behavioral checklist (intent,
  not diff similarity).
- `outcome` — exact/fuzzy match against a recorded decision (triage, negatives).

**Negatives** carry `ground_truth.outcome` of `reverted | wont_fix |
no-change-correct` and grade on whether the agent declines / diagnoses
"working as intended" rather than manufacturing a change. These are ordinary
cases with outcome rubrics — distinct from the experiment system's
`negative_control` fixtures, which calibrate evaluators, not agents.

**Sealing.** v0 sealing is git: a case is sealed by the corpus commit that adds
it; its pre_state pins immutable SHAs. When the harvest adapter lands, each case
imports as sealed snapshots (`repository/v1` at ref, `work-item/v1` from task/,
...) and an `agent/experiment` fixture binding those snapshot IDs to the
workflow's input ports. Nothing in this format should require information the
adapter cannot derive from the case directory.

**Self-hosted corpus caveat.** Cases whose subject repo is this repo pin
pre_state SHAs that predate `bench/` — replay materializes the repo at that SHA,
so the corpus (and its answers) is not reachable through the exposure manifest.
Never construct a case whose pre_state ref contains `bench/corpus`.
