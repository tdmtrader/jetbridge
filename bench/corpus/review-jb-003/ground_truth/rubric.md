# Rubric — review-jb-003 (adversarial review of the agentic UX wave)

Score the **review**, not a diff. The workflow's output is a set of findings;
there is no code change to compare. Judge whether the review would have caught
what a competent, skeptical human caught, ranked it the way impact demands, and
scoped it honestly.

Ground truth: `expected_findings.yaml` (F1 high, F2 medium, F3 low; D1
pre-existing-and-deferred). Each of F1–F3 is pinned by a regression test in the
fix commit, so all three are facts, not opinions.

## Recall — did it find them? (primary axis)

- **MUST** report F1 (the periodic refetch overwriting the open edit form). This
  is the one finding whose absence should fail the review outright: it is silent
  data loss on a routine interaction, on a branch about to be deployed.
- **SHOULD** report F2 (parked runs rendering as Running).
- **SHOULD** report F3 (row expansion keyed by list position).
- A finding counts as reported when it satisfies the `required` clauses of that
  finding's `match_criteria`. Different wording, a different suggested fix, and
  different line numbers are all fine; a finding that overlaps the anchor region
  but describes an unrelated defect does not count.
- Do **not** require the agent to reproduce the reference fix. Reporting the
  defect is the deliverable.

## Ranking — is the ordering defensible?

- **MUST** rank F1 above F3 if both are reported. Getting silent-data-loss below
  a cosmetic re-render is a judgement failure even when recall is perfect.
- **SHOULD** assign F1 `high` (or `critical`) and F3 `low`. Treat one step of
  severity drift as acceptable; treat F1 at `low`, or F3 at `high`/`critical`, as
  a ranking failure.
- Severity must be argued from user impact and likelihood. A severity with no
  stated consequence is unscored.

## Scoping and precision

- **MUST NOT** claim a finding without a described failure path — a state, an
  action, and what the user loses or sees. "Fragile", "could be improved",
  "consider extracting" are not findings at any severity.
- **SHOULD** report D1 (blank finding ids colliding in the review card) **and**
  mark it pre-existing / out of scope for this branch, as follow-up work.
  Reporting it as a defect *introduced by this branch* is a partial credit:
  right defect, wrong attribution. Silence on D1 is a miss, not an error.
  Scoping caveat (2026-07-25): the task asks for per-finding attribution but no
  longer spells out how pre-existing findings should be handled — that coaching
  was removed as leading text. Grade accordingly: reporting D1 with a correct
  pre-existing attribution is full credit whether or not the agent uses the words
  "follow-up" or "out of scope"; reporting D1 with no attribution at all is
  partial credit, not a scoping failure. Reserve the full scoping signal for an
  agent that reports it, attributes it correctly, and keeps it off the branch's
  blocking list.
- **MUST NOT** be penalized for true findings outside the human's set. See
  `precision_guidance.plausible_true_findings_not_in_the_human_set` — the human
  oracle is a floor on recall, not a ceiling. Penalize only findings that are
  wrong, unfalsifiable, or explicitly out of scope
  (`precision_guidance.treat_as_false_positive`).
- **MUST NOT** pad. A review whose true findings are buried under a dozen
  speculative ones has failed at its job even if recall is perfect; weight
  precision against the count of unsupported findings, not the count of findings.

## Evidence quality

- **MUST** anchor every finding to a file, with a line range or an unambiguous
  function/branch name in the `after` state.
- **SHOULD** ground F2 in the parked-run behaviour rather than in taste — cited
  from the contract or reconstructed from the code, either is fine. The fact
  that a parked step deliberately keeps its build `started` is written down in
  `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` and
  `03-pipeline-runs.md`, and it is what makes the rendering rule wrong. A review
  that asserts "parked should win" with no reference to why the build is
  `started` has the right answer with weak evidence.
- **Credit causal reasoning, not quotation.** Two documents in the exposed tree
  price the task down a little and are kept for authenticity: the parked-run
  contract (`00-shared-contracts.md`, `03-pipeline-runs.md`) and the branch's own
  `AGENTIC_UX_WAVE_2_SCOPE.md`. Score the chain from evidence to failure, not the
  citation: an agent that derives the parked/`started` collision from the code and
  the run lifecycle alone earns the same credit as one that cites the contract,
  while quoting the contract without connecting it to `runOutcome`'s precedence —
  or restating the scope doc's deferred-items list as if it were a review finding
  — earns none.
- **SHOULD** distinguish blocking from non-blocking, since the branch is about to
  merge. F1 and F2 are blocking; F3 and D1 are not.
- **MUST NOT** assert that tests are failing or that the branch does not compile.
  Both are false (`elm-test` is green at the base and at the reviewed state), and
  a review that manufactures a build failure is worse than one that misses a bug.

## Traps (what separates a real review from a plausible one)

- The refetch handler carries a comment stating it "Only replaces fetched data."
  That comment is false — the handler writes the edit buffers. An agent that
  reads the comment and moves on has been talked out of F1 by the code.
- `runOutcome`'s doc comment argues, persuasively and at length, that build
  status must win over step status. It is right about the case it was written
  for and wrong about `parked`. F2 requires disbelieving a correct-sounding
  rationale in a specific case, not spotting an obviously sloppy branch.
- All three findings are about *time* — a second render, a later fetch, a
  re-ordered list. Nothing is wrong on first paint, and every one of these
  functions is correct when read once, statically. A review that only reads each
  function in isolation will pass the branch.

## Credit notes for the judge

- F2's reference fix puts `parked` ahead of every build status, including
  terminal ones. The platform's later, settled rule is narrower: a terminally bad
  build (failed/errored/aborted) still wins, and parked beats everything else. An
  agent that lands on the narrower rule is more correct than the reference.
- F3's fix-commit message describes a "5s refetch" on the /agent page; the page
  actually polls on the one-minute clock. Do not require the "5s" detail, and do
  not mark "60s refetch" or "periodic refetch" wrong.
- F1 is best described as *aggravated* by this branch rather than introduced by
  it: the clobber existed at the previous one-minute cadence, and the branch's
  move to five seconds turned a rare annoyance into constant, load-bearing data
  loss. Both attributions are defensible — credit either, and treat an agent that
  notices the distinction as having read the base state properly.
