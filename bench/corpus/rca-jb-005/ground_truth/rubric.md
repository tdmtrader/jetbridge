# Judge rubric — rca-jb-005

**WITHHELD.** Score the agent's diagnosis + change against the behaviours below.
Grade **intent and reasoning**, not diff similarity: the reference change is 59
added lines that are largely a copy of a block already in the tree, so textual
overlap is easy to score highly on for the wrong reason. The discriminating
question is always *which flight volume does the transcript live in, and which
exec step holds a handle to it*.

Read `answer.md` first. `expected_findings.yaml` carries the anchors.

**Where the diagnosis lives.** `task.md` asks for it as `DIAGNOSIS.md` at the
repository root. Grade that file if it exists; if the agent delivered the
diagnosis only in its final written response, grade the response instead and note
the deviation — **do not deduct on channel alone**, the graded content is the
reasoning, not the filename.

## Scoring

Five buckets, weighted. Report each sub-score and the total out of 100.

| # | Bucket | Weight |
|---|---|---|
| A | Root cause identified | 35 |
| B | Evidence and reasoning | 20 |
| C | Fix lands in the right place | 25 |
| D | Constraints and negatives respected | 10 |
| E | Verification / test named | 10 |

A run that scores 0 on A cannot exceed 25 overall regardless of the other
buckets — record the cap if it applies.

---

## A. Root cause identified (35)

**Full credit (35)** requires all three:

- A1. The transcript is **produced by the agent/implement step's runner**
  (`agent/runner/runner.go` writing `flight/transcript.ndjson`), and lands only
  in **that step's** flight volume.
- A2. The ingestion that reads it was placed in **`atc/exec/harvest_step.go`**,
  which streams from the **harvest** step's flight artifact — written by
  `harvest-runner`, which never produces a transcript. Naming the producer and
  the consumer as *different steps with different flight volumes* is the answer.
- A3. Explains the **silence**: ingestion is deliberately best-effort, so the
  absent file yields either a swallowed `failed-to-stream-flight-file` log on the
  web side or a zero-length read skipped by the `len(raw) > 0` guard; either way
  no row, no error to the build, no status change. Bonus (not required) for
  noting the 404/CLI message is correct behaviour for an absent row rather than
  an error report.

**Partial credit:**

- 25 — A1 + A2 without A3 (right mismatch, no account of why it is invisible).
- 15 — "ingestion never runs / the store is nil for the step that has the file"
  — right neighbourhood, but the producer/consumer step mismatch is not named.
  Also award 15 for an answer that only spots the missing `step_factory.go`
  wiring (F3) and stops there, without noticing there is no ingestion seam in
  `agent_step.go` to wire *to*.
- 0 — any of the following:
  - Blames the runner: "the runner isn't writing `transcript.ndjson`" /
    "the runner image is stale". The evidence bundle states the deployed runner
    includes the capture and that no `agent-runner: write transcript:` error
    appeared. Proposing a runner-side fix as the root cause is a trap hit.
  - Blames the schema, the migration, the `(build_id, plan_id)` key, or the
    factory's `Upsert`/`Get` SQL.
  - Blames the read route / `go-concourse` / `fly` (the 404 is correct).
  - Blames the artifact daemon / streaming layer generically ("StreamFile is
    broken") without identifying which volume is being streamed.
  - Concludes the feature is fine and the operator queried the wrong table/env.

## B. Evidence and reasoning (20)

- B1 (10) — Uses the **metrics-rows-but-no-transcript-row** contrast (evidence
  §3: two `agent_run_metrics` rows, `implement` and `harvest`, zero transcript
  rows for the same build) to establish that flight ingestion *ran and succeeded*
  on both steps and that the defect is therefore specific to the transcript read,
  not to ingestion, streaming, or the flight volumes as a whole. Award only if
  the agent argues it; quoting the table is not enough. Note that the bundle
  states this observation and draws no conclusion from it (the curator removed
  the sentence that did) — the inference from "two metrics rows exist" to "flight
  ingestion ran on both steps" is the agent's to make, and the 10 points are for
  making it.
- B2 (5) — Traces the concrete code path rather than asserting: names
  `agent/runner/runner.go` as the sole writer (and/or shows `agent/harvest/`
  writes no transcript), and names `ingestFlightRecorder` in both step files.
- B3 (5) — Cites at least one corroborating signal:
  - `AgentRunTranscriptFactory.Get`'s doc comment / ORDER BY, which already
    *prefers the implement step over the harvest step* — the design always
    expected the implement step to author the row;
  - the deployment facts that rule out the runner half (evidence §1, §5);
  - the hand-inserted-row experiment (evidence §4) ruling out the read path.

Deduct up to 8 for asserting the mechanism with no chain of evidence (a lucky
pattern-match on "wrong step"). Deduct up to 8 for fabricating evidence not in
the bundle or the repository — invented log lines, pod dumps, build numbers, or
claims about what a flight volume contained.

## C. Fix lands in the right place (25)

- C1 (15) — Adds transcript ingestion to **`AgentStep.ingestFlightRecorder`** in
  `atc/exec/agent_step.go`, inside the `flightArtifact != nil` block, streaming
  `transcript.ndjson` from the agent step's own flight artifact and upserting a
  `db.AgentRunTranscript` keyed on `(BuildID, PlanID)` with `StepName` and a
  non-nil `TicketID` only when the server-verified `ticketID > 0`.
  - Full C1 also requires the store to be **optional and nil-guarded** (no
    ingestion attempt at all when unset) and every error path logged-and-
    swallowed so the step still succeeds.
  - Deduct 5 if the ticket linkage is taken from anywhere other than the
    server-verified `ticketID` already threaded into `ingestFlightRecorder`.
- C2 (5) — Wires it through: `atc/engine/step_factory.go`'s `AgentStep()` passes
  the already-existing `factory.agentTranscriptStore` to the step. A fix that
  adds the seam but never wires it is dead code — cap C at 10.
  - The reference reuses the existing `HarvestTranscriptStore` interface and the
    existing `boundTranscriptTail` helper. Defining a new equivalent interface or
    re-implementing the bounding logic is **not** a deduction on its own, but
    re-deriving a *different* bounding behaviour is (see D).
- C3 (5) — Structural judgement about the pre-existing harvest block. Award for
  either of the two defensible choices, **argued**:
  - leaving it in place and saying why (harmless no-op; the harvest flight simply
    never has the file; purely additive change, smallest blast radius) — this is
    what the reference did; or
  - removing it and saying why (dead code, and the misleading precedent is what
    caused the bug).
  Award 0 for removing it silently, and 0 for *moving* it out of harvest without
  adding it to the agent step. **Removing the harvest block instead of adding the
  agent-step ingestion is a hard fail** — it changes nothing about the empty
  table.

Note for graders comparing against `reference.diff`: the reference is purely
additive and introduces the option name `WithAgentStepTranscriptStore`. **The
name is not load-bearing.** Any equivalent construction (a constructor argument,
a differently-named option, a shared helper extracted from both step types) earns
full C1/C2 provided the behaviour matches.

## D. Constraints and negatives respected (10)

- D1 (4) — No schema change, no new migration, no change to
  `(build_id, plan_id)`.
- D2 (3) — No change to the read route, the `go-concourse` method, or the `fly`
  subcommand.
- D3 (3) — States explicitly that at least two of {schema/factory, read route,
  fly CLI, runner capture} are **correct and unchanged**; and does **not** call
  for an `agent-runner` image rebuild (or, if it does, argues the point and is
  wrong — award 0 for D3 but do not double-penalise A).
  - **Anti-copy-back.** The bundle hands over two of these negatives directly
    (§4 shows the hand-inserted row served correctly; §1/§5 show the deployed
    runner and no write-failure line). Restating them earns at most 1 of the 3.
    Full D3 needs at least one negative the agent *established itself* from the
    repository — e.g. reading `atc/api/transcriptserver/get.go` and showing the
    404 is the `found == false` branch, or reading the factory's `Upsert`/`Get`
    SQL and showing the key and ordering are right.

Any of the following is a **constraint violation**, reported explicitly and
scoring 0 for the whole bucket: changing the 512KiB bound or its truncation
marker semantics; making transcript persistence able to fail a step or change a
build's status; making transcript failure interfere with the metrics/ledger
ingestion in the same function.

## E. Verification / test named (10)

- E1 (6) — Adds or specifies a unit test at the agent-step level that proves the
  transcript row is upserted **from the agent step's own flight volume**, and
  that would have been red before the change. Full credit needs at least the
  happy path; award the full 6 if the agent additionally covers the nil-store
  no-op, the upsert-error-does-not-fail-the-step case, or the >512KiB bounding.
- E2 (4) — Says what a live re-run should now show that this run did not: a row
  in `agent_run_transcripts` for the implement step's `(build_id, plan_id)` and a
  200 from `fly agent runs transcript` / the read route. Bonus for naming the
  general lesson — both halves were unit-tested in isolation and nothing
  exercised producer-flight → transcript-row across the seam.

---

## Mechanical gate caveat (read before scoring C or E)

`case.yaml`'s `fail_to_pass` overlay is the withheld spec file
`atc/exec/agent_step_test.go`, which constructs the step through
`exec.WithAgentStepTranscriptStore`. **That option name is a grading artifact,
not a requirement**: `task.md` leaves the seam's shape entirely open, so a
correct fix that names its option differently, passes the store as a constructor
argument, or extracts a shared helper will fail the overlay with a *compile*
error rather than a behavioural one.

Consequences for the judge:

- A red overlay is evidence of nothing until you have checked whether the failure
  is `undefined: exec.WithAgentStepTranscriptStore` (or an equivalent symbol
  mismatch). If it is, rename the symbol in the overlay to whatever seam the agent
  built, re-run, and score C on the re-run. Record that you did.
- If the agent wrote its own specs in `atc/exec/agent_step_test.go`, applying the
  overlay may clobber or conflict with them. Move the agent's file aside (or use
  `git apply -3`), grade E on the agent's own tests, and use the overlay only as
  the behavioural check for C.
- Never treat "the withheld overlay compiled and passed" as sufficient for C
  either — it is a necessary check on behaviour, and C3's structural judgement is
  not mechanically observable at all.

## Hard fails (report explicitly)

- Fix applied only in `harvest_step.go` (tuning, retrying, or "fixing" the
  existing block) — the table stays empty.
- The harvest block is deleted and no agent-step ingestion is added.
- Root cause given as a runner-side failure to write the file.
- A migration or schema change (task constraint).
- Transcript ingestion made able to fail the step or to abort metrics ingestion.

## Calibration notes for the judge

- The pre-state contains one authentic artifact that **agrees with the bug**: the
  migration header comment says *"harvest ingestion persists it here."* An agent
  that cites this comment as proof the harvest step is the right place has been
  trapped by a real, at-the-cut document — that is the intended difficulty, not
  unfairness. Do not credit it as evidence; do credit an agent that notices the
  comment contradicts `AgentRunTranscriptFactory.Get`'s implement-first ordering
  and resolves the contradiction in favour of the code.
- The evidence bundle deliberately omits the web pod's lager log. That log
  contains a `failed-to-stream-flight-file` line naming `transcript.ndjson` from
  the harvest step's ingestion session, which would hand over most of A2. If a
  variant of this case is ever run *with* that log attached, A2 is worth much
  less and the score bands must be re-derived — do not compare results across the
  two variants.
- An agent that says "I cannot resolve this without inspecting a flight volume"
  scores by the experiment it proposes. "List the files in an implement-step
  flight volume vs a harvest-step flight volume" is a strong partial (treat as
  A1, no A2/A3, plus B2 if code-tracing drove it). "Re-run and look again" is 0
  on A — the evidence already rules that out.
