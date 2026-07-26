# Corpus index — v0 (sealed 2026-07-25)

34 cases. A "corpus version" is the git commit that seals this directory; results
must cite it. All-dev in v0 (no holdout split until ~150 cases). Sources: `jb` =
jetbridge-era history of this repo (private, post-cutoff), `ld` = LightingDesign
(private, post-cutoff), `cc` = upstream Concourse (public, pre-cutoff —
`memorization_risk: high`, never headline results on these).

**Counts.** Workflow: small-fix 13 · log-diagnosis 7 · code-review 6 ·
feedback-loop 3 · version-upgrade 3 · triage 2 (negatives are distributed inside
those shapes — 6 cases whose correct answer is decline/no-change; they must not
be distinguishable from the exposure, which is why there is no `negative`
workflow value). Rubric: mechanical 14 · judge 10 · reference 5 · outcome 5.
Difficulty: trivial 2 · moderate 21 · hard 11. Validation: validated 22 ·
partial 4 · unvalidated 8 (judge/outcome cases with no mechanical leg).

**Audit legend.** Each case carries 2–3 leakage-audit entries: independent Opus
and Sonnet auditors over the exposure only, then the curator-fixup resolution.
Sonnet `fail` verdicts were predominantly case.yaml-self-disclosure objections
that dissolve under the exposure contract (case.yaml is harness-side); every
non-dissolved item was fixed — see each case's `notes.md#fixup-2026-07-25`.
Residual borderlines (fix-jb-005, upgrade-cc-001) are documented
priced-deflator decisions, not open leaks.

| case | workflow | src | rubric | difficulty | memorization | validation | audits (opus/sonnet/fixup) | leak channels |
|---|---|---|---|---|---|---|---|---|
| [feedback-jb-001](feedback-jb-001/case.yaml) | feedback-loop | jb | judge | hard | none | validated | borderline/borderline/pass | auto-mem |
| [feedback-jb-002](feedback-jb-002/case.yaml) | feedback-loop | jb | mechanical | moderate | none | validated | pass/pass/pass | auto-mem |
| [feedback-jb-003](feedback-jb-003/case.yaml) | feedback-loop | jb | judge | hard | none | validated | borderline/pass/pass | auto-mem |
| [fix-cc-001](fix-cc-001/case.yaml) | small-fix | cc | mechanical | moderate | high | partial | pass/pass | — |
| [fix-jb-001](fix-jb-001/case.yaml) | small-fix | jb | mechanical | moderate | none | validated | pass/pass/pass | — |
| [fix-jb-002](fix-jb-002/case.yaml) | small-fix | jb | mechanical | moderate | low | validated | pass/pass/pass | — |
| [fix-jb-003](fix-jb-003/case.yaml) | small-fix | jb | mechanical | moderate | none | validated | borderline/fail/pass | auto-mem |
| [fix-jb-004](fix-jb-004/case.yaml) | small-fix | jb | mechanical | moderate | none | validated | borderline/fail/pass | auto-mem |
| [fix-jb-005](fix-jb-005/case.yaml) | small-fix | jb | mechanical | moderate | none | validated | borderline/fail/borderline | auto-mem |
| [fix-jb-006](fix-jb-006/case.yaml) | small-fix | jb | mechanical | trivial | none | validated | borderline/pass/pass | — |
| [fix-jb-007](fix-jb-007/case.yaml) | small-fix | jb | mechanical | moderate | none | validated | pass/pass/pass | auto-mem |
| [fix-ld-001](fix-ld-001/case.yaml) | small-fix | ld | mechanical | moderate | none | validated | borderline/fail/pass | — |
| [fix-ld-002](fix-ld-002/case.yaml) | small-fix | ld | mechanical | moderate | none | validated | borderline/borderline/pass | — |
| [neg-cc-001](neg-cc-001/case.yaml) | code-review | cc | outcome | moderate | high | unvalidated | borderline/borderline/pass | auto-mem |
| [neg-jb-001](neg-jb-001/case.yaml) | small-fix | jb | outcome | hard | none | validated | pass/pass/pass | auto-mem |
| [neg-jb-002](neg-jb-002/case.yaml) | small-fix | jb | judge | moderate | none | validated | borderline/fail/pass | auto-mem |
| [neg-jb-003](neg-jb-003/case.yaml) | log-diagnosis | jb | judge | moderate | none | unvalidated | pass/fail/pass | auto-mem |
| [neg-jb-004](neg-jb-004/case.yaml) | small-fix | jb | outcome | trivial | none | unvalidated | borderline/pass/pass | auto-mem |
| [neg-ld-001](neg-ld-001/case.yaml) | small-fix | ld | outcome | moderate | none | validated | pass/pass | — |
| [rca-jb-001](rca-jb-001/case.yaml) | log-diagnosis | jb | judge | hard | none | unvalidated | borderline/fail/pass | auto-mem |
| [rca-jb-002](rca-jb-002/case.yaml) | log-diagnosis | jb | judge | hard | none | unvalidated | borderline/fail/pass | auto-mem |
| [rca-jb-003](rca-jb-003/case.yaml) | log-diagnosis | jb | mechanical | hard | none | validated | pass/pass/pass | auto-mem |
| [rca-jb-004](rca-jb-004/case.yaml) | log-diagnosis | jb | judge | moderate | none | validated | pass/borderline/pass | auto-mem |
| [rca-jb-005](rca-jb-005/case.yaml) | log-diagnosis | jb | judge | moderate | none | validated | pass/fail/pass | auto-mem |
| [review-jb-001](review-jb-001/case.yaml) | code-review | jb | reference | hard | none | validated | borderline/fail/pass | auto-mem |
| [review-jb-002](review-jb-002/case.yaml) | code-review | jb | reference | moderate | none | validated | pass/fail/pass | auto-mem |
| [review-jb-003](review-jb-003/case.yaml) | code-review | jb | reference | moderate | none | validated | borderline/borderline/pass | auto-mem |
| [review-jb-004](review-jb-004/case.yaml) | code-review | jb | reference | hard | none | validated | borderline/fail/pass | auto-mem |
| [review-ld-001](review-ld-001/case.yaml) | code-review | ld | reference | hard | none | unvalidated | borderline/fail/pass | auto-mem |
| [triage-jb-001](triage-jb-001/case.yaml) | triage | jb | outcome | moderate | none | unvalidated | borderline/fail/pass | auto-mem |
| [triage-ld-001](triage-ld-001/case.yaml) | triage | ld | judge | moderate | none | unvalidated | borderline/pass/pass | auto-mem |
| [upgrade-cc-001](upgrade-cc-001/case.yaml) | version-upgrade | cc | mechanical | moderate | high | partial | borderline/fail/borderline | — |
| [upgrade-cc-002](upgrade-cc-002/case.yaml) | version-upgrade | cc | mechanical | moderate | high | partial | borderline/fail/pass | — |
| [upgrade-cc-003](upgrade-cc-003/case.yaml) | version-upgrade | cc | judge | hard | high | partial | borderline/fail/pass | auto-mem |

## Run notes

- **Hand-running a case:** materialize `task/` + pre_state into a NEUTRAL
  directory (case ids/paths announce outcomes); never expose `case.yaml`,
  `notes.md`, `ground_truth/`. On THIS machine, suppress project auto-memory
  first — 24 cases carry `known_leak_channels: [project-auto-memory]`.
- **Environment:** Postgres needed only where a case's grading says so
  (e.g. fix-jb-005, review-jb-004 V2). rca-jb-003's mechanical gate needs
  `dash` on PATH (macOS-only harnesses report green at both ends — see its
  notes). fix-jb-007 needs the Elm toolchain. Upstream (`cc`) cases need
  era-appropriate Go toolchains for full validation (why they sit at
  `partial`).
- **neg-jb-001 materialization:** requires the refs-deleted clone procedure in
  its case.yaml (the answer key is reachable from branch refs otherwise).
- **Pairings:** feedback-jb-001 consumes review-jb-001's ground truth as input —
  never run both in one session/context.

## What v0 taught us (roll-up of per-case curation.learnings)

**About building cases:**
1. The richest terminal artifacts are in-repo *written records* (FINDINGS.md,
   REVIEW.md, audit docs, dogfood logs) — they yield reviews, feedback loops,
   negatives, and RCA cases with authentic ground truth; bare git history only
   yields fixes. A team that writes down findings is accidentally building a
   benchmark.
2. ~80% of dual-audit flags were curator hygiene, not snapshot contamination —
   and half of those dissolved once the exposure contract (case.yaml is
   harness-side) was explicit. Write the contract before the first case.
3. Two-model auditing earns its cost: the models fail differently (Opus argues
   about exposure reasoning; Sonnet is strict about self-disclosure), and
   agreement/disagreement is a useful borderline signal.
4. Operator-environment leakage is real: the dev machine's project auto-memory
   states several answers verbatim. Any local replay harness must run
   memory-suppressed; cluster replay is naturally clean.
5. Fail-to-pass validation at build time (extractors validated their own cases
   via `git archive` scratch trees) is cheap on private Go repos and caught
   real grading defects early. On public history it is the binding constraint,
   exactly as predicted: all four `cc` fix/upgrade cases are `partial` for
   toolchain reasons.
6. Negatives need a *symmetric* delivery channel (a `Disposition:` line every
   ticket carries) so declining is expressible without the task hinting that
   declining is expected.
7. Grading overlays collide with tests the task itself asks the agent to write.
   Cases now carry explicit overlay protocols (preserve the solver's version,
   run un-overlaid first, overlay on a scratch copy).
8. Withholding naturally-available evidence (a stack trace that names the fix
   line) is a legitimate curation lever but must be recorded in notes.md —
   otherwise a scrubbed task is indistinguishable from a thin one.

**About the platform (gaps this corpus surfaced):**
1. No seeded `triage` or `feedback-loop` workflow exists — 5 cases have no
   runnable signature yet and declare their target signatures explicitly.
2. The single-value `rubric:` enum cannot express mechanical+judge dual grading,
   which 4 cases genuinely need (recorded in-case as a format gap for v1).
3. Crash-shaped test failures (process panic) break spec-count parsing —
   graders must key on exit codes (fix-jb-001).
4. `expected-findings` anchoring (file+region+direction matching semantics,
   `also_true` neutral sets, `non_findings` hard misses) is a schema the
   platform's `review/v1` type does not yet carry — needed for recall grading.
5. Replay harnesses need a refs-suppression option for cases whose answer key
   is reachable from branch refs (neg-jb-001), and a way to seal non-git
   working files as inputs (review-ld-001).
