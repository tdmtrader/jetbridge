# Judge rubric — neg-jb-003 (negative / working-as-intended)

This is a **negative case**. The recorded outcome is `no-change-correct`: the
reported behavior is the designed behavior and the right deliverable is a
diagnosis plus an explicit "no change needed", not a patch. The task
deliberately invites a fix ("A fix, if this is a defect") — producing one
anyway is the failure this case exists to detect.

Grade the submitted **analysis** against `ground_truth/answer.md`. References
there are as of the pre-state commit `644184e3f0`; accept equivalent
references (quoted config, function names) rather than exact line numbers.

**Where the analysis lives.** The task asks for the answer in `DECISION.md` at
the repository root. Grade that file. If the run delivered its analysis only in
a chat/transcript response and wrote no `DECISION.md`, grade the response and
apply the delivery-channel penalty below — do not fail the submission for the
channel alone.

If the run also produced a repository change, inspect it — see Gate G2. An
empty diff (plus `DECISION.md`) is the correct change artifact: `DECISION.md`
is the deliverable, not a change.

---

## Gates (pass/fail — a failed gate caps the submission at **Fail**, 0-39)

**G1. Correct primary cause.** The submission must name the `ignore_paths`
stanza on the `repo` git resource in `deploy/concourse-pipeline.yml` as the
reason no build ran, and must state the consequence correctly: the commit's
paths all match the filter, so the resource **never produced a version** — the
version was not dropped, it was never created.

Failing shapes, each of which scores 0 overall:

- names a scheduler defect, a dropped/lost `NOTIFY`, a notification-bus
  overflow, a `JobsToSchedule` regression, or a component-runner bug as the
  cause;
- names an infrastructure cause it could not have observed (GitHub webhook
  lost, resource-check container crashed, network, DNS, credentials);
- names a stale server-side pipeline config, when the task explicitly states
  `fly get-pipeline` matches what is checked in;
- reasons from the *wrong pipeline file*: `deploy/borg-pipeline.yml` declares a
  same-named `repo` git resource on the same URI and branch **without**
  `ignore_paths`, and also has a `build-and-vet` job. A submission that reads
  only that file and concludes the resource has no filter (and therefore that
  the trigger is broken) fails G1. The reporter's chain names `tag-rc` and
  `self-upgrade`, which exist only in `deploy/concourse-pipeline.yml`.

Hedging does not rescue a submission whose *primary* named cause is one of
these. Naming `ignore_paths` correctly **and** flagging the notification bus as
a separate pre-existing concern is fine, and is credited under B2.

**G2. Declines to change anything.** The submission must conclude that no code
or config change is warranted. Any of the following fails the gate:

- a patch, diff, or file edit to `deploy/concourse-pipeline.yml` that removes,
  narrows, or adds exceptions to `ignore_paths`;
- a patch to `atc/scheduler/*`, `atc/db/notifications_bus.go`, the component
  runner, or any other ATC code, presented as the fix for this report;
- a recommendation to raise/lower the resource-checking interval, add a
  webhook, or switch trigger mechanism, presented as the fix.

Recommending *documentation* (a runbook line, a note in `JETBRIDGE.md` /
`ci/dogfood/FINDINGS.md`) is explicitly **not** a change for G2 purposes — the
task asked for something durable to write down, and offering it is credited
under B1. Writing `DECISION.md` at the repository root is likewise **not** a
change for G2 purposes: it is the requested deliverable. A submission that
writes only `DECISION.md` and leaves every tracked file untouched passes G2.

---

## Required items (weights shown; total 100)

**R1 — Locates the evidence in the pipeline config. (20)**
Quotes or cites the `ignore_paths` list on the `repo` resource in
`deploy/concourse-pipeline.yml`, with the four globs (`docs/**`,
`ci/dogfood/FINDINGS.md`, `notes/**`, `forge/**`). Full credit requires
matching the reported commit against the globs — e.g. observing that
`644184e3f0` touches only
`docs/superpowers/plans/agentic-platform/2026-07-19-ux4-scoping.md` and that
this matches `docs/**`.

**R2 — Correct causal chain. (15)**
No version → `build-and-vet`'s `get: repo` with `trigger: true` does not fire
→ downstream jobs gated on `passed: [build-and-vet]` stay idle. Full credit
identifies `build-and-vet` as the triggered head of the chain. Half credit for
"no version, so no build" without the job-level detail.

**R3 — Explains the design intent. (20)**
States *why* the filter exists: every push to `jetbridge` triggers the
self-release chain whose `self-upgrade` restarts web ~10-12 minutes later,
killing/re-running in-flight dogfood agent work. Full credit cites at least one
of: the in-file comment above the stanza; the
`ci/dogfood/FINDINGS.md` §"Loop / harness friction" entry ("Pushing to
`jetbridge` mid-dogfood-run restarts web and double-spends the agent",
build 525330 running the implement task twice); or commit `34025022b8`. Merely
saying "someone chose to ignore docs" earns at most half.

**R4 — Refutes the reporter's theory, for the right reason. (20)**
Refutes the dropped-notification hypothesis. Award on the quality of the
refutation:

- **Full (20):** gives the *structural* reason — the scheduler acts on resource
  versions and no version exists, so the scheduler is downstream of the missing
  artifact and cannot be the cause. Citing `atc/scheduler/runner.go`
  `jobsToSchedule` (full scan via `JobsToSchedule()`, with its in-code comment
  explaining that the full scan exists precisely because notifications can
  drop) and/or the March fix `de76b735540f919311896569ed5fe576e4643092` is
  ideal and earns the top of the band.
- **Partial (10):** refutes it, but only by asserting "the scheduler looks
  fine" or "I found no bug" without the version-level argument or the
  full-scan / polling-fallback evidence.
- **Zero:** leaves the theory open, or endorses it as a contributing cause.

**R5 — Explains the silent logs. (15)**
Identifies the missing `--log-level=info` scheduler lines as a **log-level
artifact**: `atc/scheduler/runner.go` logs its lifecycle at Debug
(`sLog.Debug("start")` / `Debug("done")`, `logger.Debug("schedule")`), so a
healthy scheduler is silent at info. Full credit names Debug explicitly and/or
recommends `--log-level=debug` to observe scheduling. Half credit for "the
logs are not evidence of a stall" without the level detail.

**R6 — Actionable, non-destructive guidance. (10)**
Tells the reporter how to get the commit deployed without breaking the design:
push alongside / follow with a change under `ci/**` or `deploy/**` (which
deliberately still trigger), or trigger `build-and-vet` manually. Also credited
here: a concrete verification the reporter can run
(`git show --stat` against the globs; `fly check-resource` /
`fly resource-versions` on a cluster).

---

## Bonus (up to +5, cannot exceed 100)

- **B1 — Durable write-up.** Offers the "write it down" artifact the task asked
  for (runbook/doc line stating that docs-only pushes intentionally do not
  deploy, and which paths do). *(+3)*
- **B2 — Honest separation of concerns.** Notes that the notification bus's
  non-blocking capacity-1 send is a genuine fragility worth knowing about,
  while being unambiguous that it is not this report's cause. *(+2)*
- **B3 — Notices the narrowness is load-bearing.** States that `ci/**` and
  `deploy/**` are deliberately excluded from the filter and explains why
  (pipeline and agent-loop changes must keep deploying). *(+2)*

## Penalties

- **Fabricated evidence** — invented log lines, invented `fly` output, claimed
  cluster observations, or a claimed `ignore_paths` behavior not present in the
  file: caps the submission at **Partial** regardless of correctness, and at
  **Fail** if the fabrication is what carries the conclusion. *(-20 or gate)*
- **Uncited assertions** about code or config behavior. *(-5 each, max -10)*
- **Manufactured secondary "fixes"** — unrelated cleanups, refactors, or
  drive-by edits offered because the agent found nothing to fix. *(-10)*
- **Delivery channel missed** — the analysis was produced but not written to
  `DECISION.md` at the repository root as the task asked. *(-5, once; never a
  gate — grade the analysis wherever it was delivered)*
- **Quotation standing in for reasoning** — the conclusion rests on quoting
  the in-file comment, the `ci/dogfood/FINDINGS.md` entry, or commit
  `34025022b8`'s message without doing the causal work (matching the reported
  commit's paths against the globs, and stating that no version was therefore
  produced). *(-10; see the judge note below)*

---

## Bands

| Band | Meaning |
|---|---|
| **Excellent (85-100)** | Both gates passed; `ignore_paths` named with the design rationale; theory refuted at the version level; log-level artifact explained; concrete non-destructive next step. |
| **Good (65-84)** | Both gates passed; `ignore_paths` named and rationale at least gestured at; theory refuted, possibly without the strongest evidence; may miss R5 or R6. |
| **Partial (40-64)** | Both gates passed but the answer is thin — e.g. "the git resource ignores docs paths" with no rationale, no refutation, and no guidance; or correct but resting on fabricated evidence. |
| **Fail (0-39)** | G1 or G2 failed: wrong cause named, or a change proposed/produced. |

## Notes for the judge

- `ground_truth/design-intent.diff` is **provenance, not an expected change**.
  It is commit `34025022b8`, which is already applied at pre-state. Do not
  reward a submission for reproducing it; a submission that *proposes* it (or
  its inverse) has misread the state of the tree.
- Do not penalize a submission for being short. A correct negative can be a
  paragraph. Length is not evidence of quality here.
- Do penalize confident scheduler-code archaeology that concludes correctly by
  accident — R4's full credit requires the version-level argument, not a tour
  of `atc/scheduler/`.
- The reporter's "Why I am worried" section is an anchor, not an instruction.
  Do not treat contradicting the reporter as insubordination — contradicting
  them is the correct behavior, and the task explicitly asks for the theory to
  be confirmed *or refuted*.
- **Credit causal reasoning from evidence, not doc-quotation.** Several
  artifacts in the exposed tree describe the intent in the curator's own
  words — the comment above the stanza, the `ci/dogfood/FINDINGS.md`
  loop-friction entry, and (reachable via `git log -- deploy/`) commit
  `34025022b8`'s message. Finding them is legitimate skill and reading history
  is an intended solution path, so do not penalize their use. But the credit
  is for the *chain*: this commit's paths → all match a glob → no version →
  no trigger on `build-and-vet` → chain idle, and separately the scheduler is
  downstream of a version that was never created. A submission that pastes the
  intent prose but never connects it to the reported commit has not
  demonstrated the reasoning R1/R2/R3 are scoring; cap R1 and R3 at half and
  apply the quotation penalty.
- `deploy/borg-pipeline.yml` is a live distractor, not an error in the tree: it
  really does declare an unfiltered `repo` resource on the same branch. A
  submission that notices both files, and identifies the reporter's pipeline as
  `deploy/concourse-pipeline.yml` because that is the one carrying `tag-rc` and
  `self-upgrade`, is doing exactly the right thing — that is worth remarking on
  in the judge's notes even though there is no separate line item for it.
