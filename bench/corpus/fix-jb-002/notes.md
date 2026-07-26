# notes — fix-jb-002

## Provenance walk

Terminal artifact: `d21437fd10adae99b340dc8c7c233fa8f86c7886`
(`fix(ci-agent): extract valid JSON from prose-wrapped review output`,
2026-07-10T07:37:58-07:00, Thomas Moore, co-authored by Claude Opus 4.8).
Contained in local branches `jetbridge` and several `claude/*` and `codex/*`
branches; it is on the jetbridge mainline, not a dangling experiment.

Verified by direct resolution:

- `git rev-list --parents -n 1 d21437fd10…` → single parent
  `a5b1cf267f7ec88b56ee6cd4fd3eb136b9a84187`
  (`ci(agent): route Claude credential to support API key or OAuth token`,
  2026-07-10T06:11:16-07:00). Pre-state is the true parent, 86 minutes earlier.
- `git show --stat` → exactly two files:
  `ci-agent/llm/client.go` (+34/−2) and `ci-agent/llm/client_test.go` (+37).
  No same-commit doc/CHANGELOG/version companions to scrub.
- The commit message's claims were checked against the tree rather than trusted:
  - `ci-agent/publish/publish.go:56` at the parent really does
    `return fmt.Errorf("review file %s is not valid JSON", opts.ReviewPath)`
    behind a `json.Valid(review)` guard.
  - `ci/tasks/ci-agent-review.yml` at the parent really does swallow that:
    `if ci-agent publish …; then … else echo "WARNING: failed to publish review
    to ATC (results still available as artifact)"` — non-fatal, build stays green.
  - The producer chain is real: `phaserunner/runner.go` takes `cr.Result` and
    writes it as the artifact; `cr.Result` comes from
    `llm.ParseCLIEnvelope` → `llm.ExtractJSON`. So a fence-only `ExtractJSON`
    genuinely lands prose-wrapped bytes in `review.json`.
  The candidate's ground-truth summary holds up in full. Nothing was overstated.

Pre-state coherence: `ci-agent` at the parent is a self-contained Go module.
Materialized it out-of-tree (`git ls-tree -r --name-only` + `git show` per file
into scratchpad — no checkout, no worktree, no ref mutation) and built/tested it
standalone. It builds and its full test suite is green, so the pre-state is a
coherent, non-broken starting snapshot.

## Mechanical verification (done during extraction)

Materialized both SHAs' `ci-agent/` trees side by side.

| run | result |
|---|---|
| pre-state, own tests, `go test ./llm/` | ok (12/12) |
| pre-state code + post-state `client_test.go` | **FAIL — 3 failed / 12 passed** |
| post-state, `go test ./llm/` | ok (15/15) |
| post-state, `go test ./...` (whole module) | all 22 packages ok, ~25s |
| pre-state, `go test ./...` (whole module) | all ok |

The 3 failures at pre are exactly the 3 added specs. Fail-to-pass and
pass-to-pass both confirmed; no Postgres, no network, no Claude CLI.

## Leakage analysis

**Scrubbed from the task.** The commit body spells out the algorithm
("returns input as-is when it already parses, else strips a ```json fence, else
slices the first bracket to the last") and names `ExtractJSON` and the file. None
of that appears in `task/task.md`. The task names no file, no function, and no
strategy; it stops at the symptom, the observable artifact shape, and the
constraints.

**Deliberately exposed.** The `publish error: review file … is not valid JSON`
line and the surrounding `WARNING:` are in the task on purpose. They are genuine
operator-visible evidence — the CI task script prints both — and without them the
case is under-determined. They also point at the *wrong* file (`publish.go`),
which is the discriminating decoy: the rubric denies credit for relaxing that
guard. Exposing real evidence that misleads is fair; exposing the fix location is
not.

**Reconstructed, not captured.** The log excerpt in `task/task.md` is a
reconstruction, not a verbatim build log (no build log survived). Its shape is
derived from the actual pre-state `ci/tasks/ci-agent-review.yml` echo statements
(`=== Review complete ===`, `Review output:`, `cat … | head -50`,
`=== Publishing review to ATC ===`, the `WARNING:` fallback) plus the failure
mode the commit body describes. The findings payload inside it is elided
(`"findings": [ ... ]`) so no invented content is presented as real. Flagged here
so a downstream auditor does not mistake it for a captured artifact.

**Curator-assigned check — `docs/superpowers/plans/agentic-platform/09-harvest-step.md`.**
Verified at the parent SHA. Its two hits are benign, and one is actively
*anti*-leaky:

- Line ~1995 defines a judge-side `extractJSON` with the comment
  "mirrors ci-agent/llm.ExtractJSON: unwrap ```json fences" and reproduces the
  **buggy, fence-only** implementation. It describes the pre-fix behaviour, not
  the fix. If anything it nudges a reader toward believing fence-stripping is the
  whole contract.
- Line ~2115 is prompt text ("Respond with ONLY a JSON object, no prose") — the
  same request the review prompt already makes, which the task explicitly says is
  not the fix.

Not withheld. Two further sweeps found nothing else: `git grep -l ExtractJSON`
at the parent hits only `ci-agent/llm/{client.go,client_test.go,result.go}` and
that one doc; `git grep -l "is not valid JSON"` hits `publish.go`, an unrelated
`alias_store_test.go`, `docs/superpowers/plans/2026-07-05-agent-review-presentation.md`
(which merely quotes the same `publish.go` snippet), and the compiled Elm bundles.
Hence `withheld: []`.

**Self-hosted corpus caveat.** Pre-state SHA is 2026-07-10, two weeks before
`bench/` was created (2026-07-25). `git ls-tree` at the pre-state SHA confirms no
`bench/` path exists, so the corpus and its answers are unreachable from the
exposure manifest.

**Memorization.** `low`. jetbridge-era private history; the module and this
function do not exist upstream. The *general* technique (recover JSON embedded in
LLM prose) is common knowledge, which is why the risk is `low` rather than
`none` — a model may well reach for the right approach without reasoning from
the evidence. That does not invalidate the case: the discriminating axis here is
choosing the producer layer over the `publish.go` decoy, and covering the
CLI-envelope path, not inventing the bracket trick.

## Open questions

- **Fail-to-pass on a modified file.** The withheld artifact replaces an existing
  test file rather than adding a new one. Grading must overwrite whatever the
  agent leaves at `ci-agent/llm/client_test.go`; if the harness merges instead of
  replaces, an agent that rewrites that file grades itself. Flagged in
  `grading.fail_to_pass[0].note`. If the harness cannot overwrite, extract the 3
  added specs into a separate withheld `_test.go` file in the same package —
  they use only the exported `llm.ExtractJSON` / `llm.ParseCLIEnvelope` surface,
  so they relocate cleanly.
- **Partial-credit boundary.** An agent that handles only the raw
  `ExtractJSON` path passes 2 of 3 fail-to-pass specs and leaves the real
  production bug live (the failure went through the CLI envelope). Mechanical
  grading already catches this. Worth checking whether the harness reports
  per-spec or only per-package pass/fail — per-package loses that signal.
- **Over-strictness of `pass_to_pass`.** `go test ./...` on the whole module is
  cheap (~25s) and green at both ends, so it is used as-is. If module-wide runs
  turn out flaky in the harness, narrow to `./llm/ ./publish/ ./phaserunner/`.

## Validation

_(stub — filled by the validation stage)_

- [ ] fail_to_pass reproduced in the harness environment
- [ ] pass_to_pass reproduced at pre_state and with reference change
- [ ] leakage audit (two independent models)

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `a5b1cf267f7ec88b56ee6cd4fd3eb136b9a84187`, post `d21437fd10adae99b340dc8c7c233fa8f86c7886`
- outcome: **validated**

### fail_to_pass
setup: `git show d21437fd10adae99b340dc8c7c233fa8f86c7886:ci-agent/llm/client_test.go > ci-agent/llm/client_test.go`
cmd: `cd ci-agent && go test ./llm/`

PRE (FAIL, exit 1) — matches the recorded 3 failed / 12 passed exactly:
```
Summarizing 3 Failures:
  [FAIL] ExtractJSON [It] extracts bare JSON preceded by prose (no code fence)
  [FAIL] ExtractJSON [It] extracts bare JSON followed by trailing prose (no code fence)
  [FAIL] ParseCLIEnvelope [It] extracts valid JSON when the result field wraps it in prose
Ran 15 of 15 Specs in 0.001 seconds
FAIL! -- 12 Passed | 3 Failed | 0 Pending | 0 Skipped
FAIL	github.com/concourse/ci-agent/llm	0.282s
```

POST (PASS, exit 0) — overlay is byte-identical to the committed file (`git status` clean):
```
Ran 15 of 15 Specs in 0.001 seconds
SUCCESS! -- 15 Passed | 0 Failed | 0 Pending | 0 Skipped
ok  	github.com/concourse/ci-agent/llm	0.221s
```

### pass_to_pass
`cd ci-agent && go test ./...` (no overlay)

PRE exit 0, 22 packages ok (adapter, adapter/claude, browserplan, cmd/ci-agent, cmd/validate-output, config, envconfig, feedback, gapgen, integration, llm, mapper, phaseconfig, phaserunner, provenance, publish, runner, schema, scoring, specparser, storage, tracing).
POST exit 0, 22 packages ok, no FAIL lines.

- corrected_cmd: none — both commands ran verbatim.
- notes: no Postgres, no network. f2p ~0.3s, p2p ~25s.

## Fixup 2026-07-25

Curator-fixup pass over the two leakage audits. Every audit item resolved below;
`task/task.md` was **not** touched (no item required it) and the validation
evidence above is unchanged.

### REAL DEFECTS — fixed

**(a) `fail_to_pass` pins the fix at `llm.ExtractJSON` though `task.md` permits a
producer-side fix one layer out.** Confirmed against the pre-state tree: task.md
states only that "whatever `ci-agent` hands to `publish` must be valid JSON", and
`ci-agent/phaserunner/runner.go` really is a second legitimate producer site —
`output := cr.Result` (line ~118) flows to `writeArtifact`, which ends in
`os.WriteFile(path, []byte(output), 0644)` (line ~265). A sanitize-at-the-boundary
fix there satisfies task.md in full while leaving the withheld `llm` specs red.
Edits:

- `ground_truth/rubric.md` → "Localization": rewrote the first MUST to name both
  qualifying layers (`ci-agent/llm` and the `ci-agent/phaserunner` artifact
  boundary), granting a boundary fix **full credit on the rubric**, and stating
  explicitly that it will fail `fail_to_pass` and must therefore be judge-graded
  rather than scored wrong.
- `ground_truth/rubric.md` → "Required behaviour": made the CLI-envelope MUST
  layer-neutral — for a boundary fix the equivalent obligation is that a
  prose-wrapped envelope `"result"` produces **valid JSON bytes at the artifact
  path**, so the partial-fix trap (raw path only) still fails at either layer.
- `ground_truth/rubric.md` → "Coverage": regression tests are required in the
  package where the fix lands, not unconditionally in `ci-agent/llm/`.
- `case.yaml` → new `grading.protocol` field: mechanical stays PRIMARY, but a
  submission that changes a producer layer other than `ci-agent/llm` and does not
  change `ci-agent/llm` must be graded by judge against `rubric.md` (run
  `pass_to_pass` + the agent's own tests; do not score from the `fail_to_pass`
  leg). Only in-`llm` submissions are decided mechanically.

**(b) `publish_test.go` has no `json.Valid` guard spec.** Verified at the parent
SHA: `ci-agent/publish/publish_test.go` is plain Go (not Ginkgo), 120 lines, six
funcs (`writeReview`, `TestPublishSuccess`, `TestPublishRetriesOn5xx`,
`TestPublishGivesUpAfterRetries`, `TestPublishDoesNotRetry4xx`,
`TestPublishValidatesInputs`); `TestPublishValidatesInputs` covers empty
URL/build/token and a nonexistent path only — nothing ever hands `Publish` an
unparseable review file. So the decoy MUST NOT is invisible to the suite: an
agent that fixes the producer correctly **and also** relaxes
`if !json.Valid(review)` in `publish.go` passes both `fail_to_pass` and
`pass_to_pass`. (A *pure* decoy fix is still caught — `fail_to_pass` stays red.)
Edits:

- `ground_truth/rubric.md` → new "Scoring guidance for the grader (coverage gaps
  in the pre-state suite)" section: names the gap, enumerates the six existing
  test funcs so a grader can see the absence, and instructs the grader to enforce
  the guard MUST NOT by reading the submitted diff for `publish.go` and
  `ci/tasks/ci-agent-review.yml`. Also says an agent that *adds* the missing
  guard spec earns a plus, not a scope-creep penalty.
- `case.yaml` → `grading.protocol` caveat (2) records the same limit next to the
  legs it qualifies.

No production code, no test file, and no exposed content changed — both fixes are
grading-side only, so results parity with the sealed exposure manifest is intact.

### DISSOLVED BY CONTRACT

- "case.yaml's `curation` and `fail_to_pass` name the decoy and the three spec
  titles" (sonnet). Dissolved by the schema's exposure contract: the solver sees
  pre_state − `withheld` + `task/` only; `case.yaml`, `notes.md`,
  `ground_truth/`, and the case id/path are harness-side and never exposed. The
  contract explicitly permits grading configs and titles to state the answer.
  The residual obligation is on hand-runners — materialize `task/` into a
  neutrally-named directory — which is a README-level rule, not a case defect.
  No edit.
- Same dissolution covers the case title ("…when the model wraps its JSON in
  prose") and `curation.learnings`, which names the decoy outright.

### DIFFICULTY — reviewed, not changed

`difficulty: moderate` stands. Opus's "strongest steer is constraint 2 telling
the solver bare+fenced already round-trip" is accurate but is not a leak or a
mis-calibration: it is a genuine preserve-existing-behaviour constraint a real
work item would carry, it names no file/function/strategy, and it constrains the
*regression surface* rather than the fix. Removing it would make the case less
faithful and would legitimize a solution that breaks the fenced path. Scrubbing
would also require editing `task/task.md`, which no item justifies.
`memorization_risk: low` also unchanged (rationale already in "Memorization").

### KNOWN LEAK CHANNELS — none declared

Neither auditor flagged operator-environment / project-auto-memory leakage for
this case. Re-checked directly anyway: grep over
`~/.claude/projects/-Users-tdmtrader-concourse-concourse/memory/` for
`ExtractJSON`, `prose-wrapped`, `ParseCLIEnvelope`, `not valid JSON`, and
`review.json` returns no statement of this ground truth. The three files that
mention `review.json` describe *different* incidents (the `validate-output
summary is required` bug, harvest-gates scope, wave-2 dual-run infrastructure).
`known_leak_channels` therefore stays absent from `case.yaml`.

### Audit trail

Appended `{auditor: curator-fixup, verdict: pass}` to `case.yaml`
`leakage_audit`. Residual verdict: **pass** — no unresolved item; the two real
defects were grading-completeness gaps, both now closed in `rubric.md` and
`grading.protocol`.
