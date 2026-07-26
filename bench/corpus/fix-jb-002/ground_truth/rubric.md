# Rubric — fix-jb-002 (prose-wrapped review JSON)

Behavioral checklist. Score intent and behaviour, **not** similarity to
`reference.diff`. A solution that satisfies every MUST by a different mechanism
is a full-credit solution.

## Localization (did the agent land in the right layer?)

- **MUST** fix the producer side — the code path that turns raw LLM output into
  the bytes written as the review artifact. Two layers qualify, and the task
  text permits either:
  - **`ci-agent/llm`** — `ParseCLIEnvelope` → `ExtractJSON`. Where the reference
    fix lands, and the only layer the mechanical `fail_to_pass` leg can observe.
  - **`ci-agent/phaserunner`** — sanitizing the step output before it becomes the
    artifact (`runner.go`: `output := cr.Result` → `writeArtifact` →
    `os.WriteFile(path, []byte(output), …)`). A fix here that makes the written
    `review.json` valid JSON satisfies the task's stated contract ("whatever
    `ci-agent` hands to `publish` must be valid JSON") just as well.

  A phaserunner-boundary fix that satisfies every MUST below is **full credit on
  this rubric**, but it will FAIL `grading.fail_to_pass`, which pins the
  behaviour at `llm.ExtractJSON`/`llm.ParseCLIEnvelope`. Such a solution must be
  graded by judge against this file instead of by the mechanical leg — see
  `case.yaml` `grading.protocol`. Do not score a phaserunner-layer solution as
  wrong merely because the withheld `llm` specs still fail.
- **MUST NOT** "fix" it by relaxing or removing the `json.Valid` guard in
  `ci-agent/publish/publish.go`, by making the publish failure fatal instead of
  a warning, or by changing only the CI task YAML. Those address the alarm, not
  the cause. Making the publish failure *also* louder is acceptable as an
  addition, never as the whole fix.
- **MUST NOT** attempt to fix it purely by editing the review prompt
  (`ci-agent/phases/prompts/review/findings.md`) to ask harder for JSON-only
  output. The prompt already asks; the task states re-prompting is not the fix.
  Prompt hardening as a *supplementary* change is acceptable.

## Required behaviour

- **MUST** produce valid JSON from output where a bare JSON object is preceded
  by unfenced prose (`Here is my review:\n{...}`).
- **MUST** produce valid JSON from output where a bare JSON object is followed
  by unfenced trailing prose (`{...}\n\nThat concludes the review.`).
- **MUST** apply at the CLI-envelope layer too, not only the raw-string layer —
  i.e. an envelope whose `"result"` string field contains prose-wrapped JSON
  must yield a valid-JSON `CallResult.Result`. (This is the layer the real
  failure went through; a fix that only handles the raw path leaves the bug
  live.) For a phaserunner-boundary fix the equivalent requirement is that a
  step whose envelope `"result"` carries prose-wrapped JSON writes **valid JSON
  bytes** to the artifact path — handling only a raw model string while the
  envelope path still writes prose is the same partial fix and fails this MUST.

## Preserved behaviour (regressions to check)

- **MUST NOT** change the exported signature of `llm.ExtractJSON`
  (`func([]byte) json.RawMessage`) or of `llm.ParseCLIEnvelope`. Adding
  unexported helpers is fine.
- **MUST** keep already-working shapes working: bare JSON (no wrapper), fenced
  ```` ```json ```` blocks with surrounding text, and multiline JSON inside a
  fence.
- **MUST** keep the no-recoverable-JSON path graceful: input like
  `not json at all` must still come back as the raw bytes, with no panic and no
  fabricated payload. (Pinned by the pre-existing spec
  "falls back gracefully on invalid JSON".)
- **MUST NOT** hand back a span that merely *looks* bracket-delimited without
  confirming it parses — e.g. prose containing a stray `{` must not turn into a
  bogus payload that then fails downstream in a new way.
- **MUST NOT** regress any other package in the module: `cd ci-agent && go test ./...`
  passes before and after.

## Coverage

- **MUST** add regression tests covering, at minimum, prose-before and the
  envelope layer, in the package where the fix lands (`ci-agent/llm/` for the
  reference shape; `ci-agent/phaserunner/` for a boundary fix). Trailing-prose
  coverage is expected but a solution that covers only leading prose in tests
  while handling both in code is a partial, not a failure.
- Tests must be hermetic (no Postgres, no network, no Claude CLI).

## Scoring guidance for the grader (coverage gaps in the pre-state suite)

- **The publish-side `json.Valid` guard is not pinned by any test.**
  `ci-agent/publish/publish_test.go` at pre_state is plain Go (not Ginkgo) with
  six funcs — `TestPublishSuccess`, `TestPublishRetriesOn5xx`,
  `TestPublishGivesUpAfterRetries`, `TestPublishDoesNotRetry4xx`,
  `TestPublishValidatesInputs`, plus the `writeReview` helper — and **none of
  them feeds `Publish` an unparseable review file**. (`TestPublishValidatesInputs`
  covers missing URL/build/token and a nonexistent path only.) Consequence: an
  agent that fixes the producer correctly **and also** deletes or relaxes
  `if !json.Valid(review) { … }` in `ci-agent/publish/publish.go` still passes
  both `fail_to_pass` and `pass_to_pass`. The "MUST NOT weaken the publish guard"
  item above is therefore **not mechanically enforced** — the grader must read
  the submitted diff for `publish.go` (and for `ci/tasks/ci-agent-review.yml`)
  and fail that MUST NOT by inspection.
- An agent that *adds* a spec pinning the guard (invalid-JSON review file → error)
  is doing something the pre-state suite should have had; treat it as a plus in
  the write-up, never as scope creep.

## Credit notes for the judge

- The reference solution orders attempts: already-valid → fenced block →
  first-bracket-to-last-bracket span, validating each candidate. Any equivalent
  ordering is fine. A **decoder-based** approach (`json.Decoder` scanning for the
  first complete value) is arguably *better* than the reference's
  last-bracket heuristic — reward it, do not penalize divergence.
- The reference also made the fenced-block branch validate its inner content and
  fall through when it does not parse. An agent that leaves the fence branch
  returning unconditionally still satisfies every MUST above; note it as a
  smaller-scope fix, not a defect.
- Diagnosing and *stating* the silent-warning coupling (publish failure is
  non-fatal, so the loss is invisible) is a plus for the write-up but is not
  required for the code fix.
