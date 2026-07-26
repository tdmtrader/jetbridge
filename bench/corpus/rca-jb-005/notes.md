# Curation record — rca-jb-005

## Provenance walk

Backed out of a fix commit whose message states the root cause verbatim, with a
second independent restatement one minute later in an in-repo findings log.

| Role | SHA | Date | What it is |
|---|---|---|---|
| pre_state | `c601df7f36af25ea57f0e957cb7f7432e189fd95` | 2026-07-20T15:34:20-07:00 | `fix(agent-runner): remove elm+uglify entirely` — unrelated to this case; it is simply the last commit before the fix |
| **terminal** | `711974f49cf942610f960d472b847cc3eaa8a3a8` | 2026-07-20T16:16:22-07:00 | `fix(exec): ingest agent-step transcript.ndjson where it actually lives` — +59/-0 across `atc/exec/agent_step.go` and `atc/engine/step_factory.go` |
| withheld tests | `9b0049e7ededecb831224f2d29aa8f23218d9912` | 2026-07-20T16:16:27-07:00 | `test(exec): cover agent-step transcript.ndjson ingestion` — +112 in `atc/exec/agent_step_test.go`, direct child of the fix |
| answer restated | `4dc43e1ac24c4cec344d6ed4a40c1bac383ca134` | 2026-07-20T16:17:29-07:00 | `docs(dogfood): … + ingestion-location bug` — +26 in `ci/dogfood/FINDINGS.md` |

Verified: `git rev-parse 711974f49c^` = `c601df7f36`; `git rev-parse 9b0049e7ed^`
= `711974f49c`. So the fix and its tests are cleanly separated by one commit, and
the pre_state is the fix's direct parent.

The feature stack that the pre_state already contains (all before the cut, all
reachable from `c601df7f36`, in commit order):

| SHA | What it added |
|---|---|
| `e73a2fedb5` | `agent-runner` captures `--output-format stream-json --verbose` stdout to `flight/transcript.ndjson`, 512KiB tail-bounded — the PRODUCER |
| `c6d59e9ccd` | migration `1773106093` + `agent_run_transcripts` table + `AgentRunTranscriptFactory` |
| `be3c0bf6c2` | `GetAgentTicketRunTranscript` read route (`atc/api/transcriptserver/`) |
| `bdda60023b` | `go-concourse` client method |
| `14cbfe0d31` | `fly agent runs transcript --ticket … --build …` |
| `edaf43a116` | **the mis-located ingestion**: the `transcript.ndjson` block in `atc/exec/harvest_step.go` |
| `ddc0ed3dc7` | `step_factory.WithAgentTranscriptStore` + `atc/atccmd/command.go` wiring — into `harvestOpts` only |

So the pre_state holds a complete, plausible, unit-tested feature that stores
nothing. That is exactly the shape the case needs.

### Verification performed at build time

- Read the terminal commit body: it claims precisely what the candidate said —
  the transcript is written by the IMPLEMENT step's runner, the ingestion was
  added only to `harvest_step.go`, `agent_run_transcripts` stayed empty.
- Read `atc/exec/harvest_step.go` at the pre_state ref: the transcript block is
  present at ~:722–753, guarded by `if step.transcriptStore != nil`, streaming
  from the harvest step's `flightArtifact`.
- Read `atc/exec/agent_step.go` at the pre_state ref: `ingestFlightRecorder`
  (:684) reads `results.json` (:753) and `events.ndjson` (:779) from the agent
  step's flight artifact and nothing else. `AgentStep` has no `transcriptStore`
  field.
- Read `atc/engine/step_factory.go` at the pre_state ref: `agentTranscriptStore`
  is a real field (:46), set by `WithAgentTranscriptStore` (:95), and appended
  only to `harvestOpts` (:344). `AgentStep()`'s option assembly (:266–300) never
  mentions it.
- Confirmed `agent/harvest/` never writes a transcript: at the pre_state ref the
  only `transcript.ndjson` writers under `agent/` are in `agent/runner/runner.go`
  (`:630`, `:648`).
- Confirmed the runner emits `agent-runner: write transcript: %v` to stderr on a
  write failure (`agent/runner/runner.go:395`) — this is what makes evidence §5's
  negative observation meaningful.
- Confirmed the step names used in the evidence bundle are real:
  `agent/dispatch/render.go:248` renders the terminal harvest step as `harvest`;
  `agent/workflow/seeds/develop.yaml` names the agent step `implement`.
- Confirmed `atc/exec/exec_suite_test.go` at the pre_state ref has no
  postgresrunner dependency (fake policy agent only).
- Confirmed the withheld test patch applies: pre_state blob for
  `atc/exec/agent_step_test.go` is `d5e30a6705aede2fdd62097ab5730406f78f72e1`,
  which is the patch's pre-image, and the fix commit does not touch the file.

The case survived verification intact. Nothing in the candidate had to be
corrected.

## Leakage analysis

### Withheld (post-cut, never exposed)

- `711974f49c`'s commit message — a verbatim answer key.
- `9b0049e7ed` — the fail-to-pass specs, whose spec names say "from the agent
  step's OWN flight". Stored here as
  `ground_truth/withheld_tests/agent_step_test.patch`.
- `4dc43e1ac2` — the `ci/dogfood/FINDINGS.md` entry, which spells out the
  producer/consumer mismatch, the fix, and the meta-lesson.

All three are **descendants** of the pre_state ref, so they are excluded by
materializing detached with refs and reflog stripped (recipe and verification
commands in `case.yaml` `pre_state.repository.materialize`). This is the same
branch-contamination hazard as rca-jb-001 and fix-jb-005; the fix is reachable
from `jetbridge`, `main` and ~20 other local branch tips in the working clone.

### Checked and clean at pre_state

- `ci/dogfood/FINDINGS.md` — verified NOT an ancestor problem:
  `git merge-base --is-ancestor 4dc43e1ac2 c601df7f36` returns false. What the
  file DOES contain at the cut is the pre-cut motivation ("Debugging is blocked
  by the absence of transcript persistence … there is no way to see WHERE it
  wrote") plus the elm/image-build troubles. That is context an operator had, not
  the answer. Left exposed.
- `docs/superpowers/plans/agentic-platform/**` — grepped for
  `transcript.ndjson`, `ingestFlightRecorder`, `agent_run_transcripts`. The only
  hits are `07-agent-step.md`, `REVIEW.md` and the judge-evidence remainder
  discussing `ingestFlightRecorder` w.r.t. `results.json` / `events.ndjson` /
  `review.json`. No plan says where transcript ingestion should live; there is no
  in-tree plan for ticket #43 at all (it was built directly, not through the
  loop — see the FINDINGS entry). `2026-07-19-s2-transcript-viewer.md` is an Elm
  event-viewer plan that consumes a *different* endpoint (the flight-events
  NDJSON route) and states up front that it does not implement it; it says
  nothing about transcript persistence or where it is ingested. Nothing withheld.
- `atc/exec/harvest_step_test.go` — contains green transcript-ingestion specs at
  the cut. Deliberately exposed: they are authentic, they prove nothing about the
  live path, and an agent that treats "there are passing tests for this" as
  evidence the code is correct is making exactly the mistake the humans made.

### Scrubbed from the exposed files

Two nudges were removed on a final sweep of `task/`, both of which would have
pointed at the agent/implement step for free:

- evidence §4 originally said the hand-inserted row used *"the implement step's
  plan id"*; now *"a plan id from that build"*. The experiment is unchanged; the
  hint is gone.
- the best-effort constraint in `task.md` originally read *"must never fail an
  agent step"*; now *"must never fail a step"*.

What deliberately REMAINS in the bundle is the `agent_run_metrics` query in
evidence §3, whose two rows are named `implement` and `harvest`. That is the
case's central piece of evidence (rubric B1) — it establishes that flight
ingestion ran and succeeded on both steps — and the step names are exactly what
an operator's query would have printed. Removing them would gut the case.

A third and fourth scrub landed in the fixup pass (see "Fixup 2026-07-25"): the
"producer/consumer mismatch" phrasing in `task.md`'s first ask, and evidence §3's
interpretive paragraph.

### Deliberately exposed, points the WRONG way

`atc/db/migration/migrations/1773106093_agent_run_transcripts.up.sql`'s header
comment: *"The runner writes flight/transcript.ndjson; harvest ingestion persists
it here."* That is the authors' wrong belief, committed before the cut. Kept
verbatim. Priced in `rubric.md` (calibration notes): citing it is not evidence,
and the countervailing pre-cut artifact — `AgentRunTranscriptFactory.Get`'s
"prefers the implement/agent step over the terminal harvest step" — is what an
agent should weigh against it.

### The evidence bundle: what is grounded in what

`task/evidence/observations.md` is **reconstructed operator field notes**, not a
capture — labelled as such in `case.yaml`. Per-claim sourcing:

| Claim | Grounded in |
|---|---|
| table DDL and index | verbatim from migration `1773106093` at the pre_state ref |
| `count(*) = 0` | the terminal commit body ("agent_run_transcripts stayed empty for every real run") and the FINDINGS entry ("stays empty") |
| two `agent_run_metrics` rows named `implement` / `harvest` for one build | derived from code: both `AgentStep.ingestFlightRecorder` and `HarvestStep`'s ingestion upsert a metrics row keyed `(build_id, plan_id)` with `step_name = step.plan.Name`; the rendered names are `implement` (workflow seed) and `harvest` (`agent/dispatch/render.go:248`) |
| `fly` / `curl` 404 text | verbatim from `atc/api/transcriptserver/get.go` ("no transcript available for run") at the pre_state ref |
| absence of `agent-runner: write transcript:` in the build log | that literal string is `agent/runner/runner.go:395`; its absence is the honest reading of "the runner did write it" |
| deployment state (web + runner rebuilt, migration head, dispatcher on) | FINDINGS' pre-cut and at-cut record of the deploy; the runner image carrying the capture is what makes the case's producer half real |
| ticket/build identifiers (#45, 588241) | **normalised/invented**. The real run was ticket #43 run 45; using it verbatim would have been confusing because "#43" is also the name of the feature itself throughout the pre_state code comments |
| cost/turn numbers in the metrics table | plausible fillers, not measurements. They carry no diagnostic weight and are there so the query output reads like real output |

Nothing in the bundle asserts a fact that contradicts the repository.

### The deliberate omission

The web pod's lager log for these builds contains
`failed-to-stream-flight-file` with `{"file":"transcript.ndjson"}` from the
harvest step's ingestion session — `Streamer.StreamFile` returns an error when
the tar has no entry for the path (`atc/worker/streamer.go:35`), and
`harvest_step.go:730` logs it. Attaching that line would identify the wrong-step
consumer almost outright.

It is omitted, and the bundle never claims it does not exist: §5 says the build
log, the build status and the UI are clean (true — that log goes to the web's
lager sink, not the build event stream), and §6 says no cluster access is
available. `rubric.md` records the omission and warns that a variant of this case
WITH the log attached is a different, easier case whose scores must not be
compared against this one.

## Open questions

1. **Mechanical gate vs. option name.** The withheld specs pin
   `exec.WithAgentStepTranscriptStore`. Unlike fix-jb-006 (where the pinned
   surface was an error string that the task could legitimately state), naming
   this option in the task would leak the fix location. Should the corpus keep a
   *rewritten* copy of the withheld specs that constructs the step through a
   neutral seam (e.g. reflection-free assertion on the store's received rows via
   whatever wiring the agent chose)? That is more work per case but would make
   this class mechanically gradable. Flagged for the validation stage.
   **Fixup 2026-07-25: handled procedurally, not closed** — the rename-then-rerun
   escape is now `rubric.md` § "Mechanical gate caveat"; the rewritten neutral-seam
   spec remains a wanted corpus improvement.
2. **`go test ./atc/exec/` health.** The repo memory notes a historical vet
   failure in `atc/exec/artifact_input_step_test.go`. If that still bites at this
   ref, the grading commands need `-vet=off` or the focused Ginkgo variant.
   **RESOLVED at validation (2026-07-25): it did not resurface; `-vet=off` was not
   required.**
3. **Does the case want the fix at all?** The deliverable is diagnosis + change,
   matching the real terminal artifact. A pure-diagnosis variant (drop the patch
   requirement, grade buckets A/B/D/E only) would isolate the reasoning signal
   from implementation ability. Not built; noted as a cheap derived case.
4. **Shared-source independence.** This case shares no terminal artifact with any
   other corpus case, but it shares a *feature area* with nothing else yet. If a
   future case is mined from `edaf43a116` or `e73a2fedb5`, check for pre_state
   overlap before aggregating results.

## Validation plan (pre-run checklist)

_Superseded by the `## Validation` record below (run 2026-07-25, all legs green).
Kept because it defines the checks that record answers to; `case.yaml`'s
`validation.notes: notes.md#validation` points at the record, not at this list._

Required checks:

- [ ] Materialize `c601df7f36af25ea57f0e957cb7f7432e189fd95` per
      `case.yaml` `pre_state.repository.materialize`; confirm
      `git cat-file -e 711974f49c^{commit}`, `9b0049e7ed^{commit}` and
      `4dc43e1ac2^{commit}` all FAIL and `edaf43a116^{commit}`,
      `e73a2fedb5^{commit}` SUCCEED.
- [ ] `git apply ground_truth/withheld_tests/agent_step_test.patch` applies
      cleanly at pre_state.
- [ ] `go test ./atc/exec/ -count=1` with the overlay **FAILS** at pre_state
      (expected: compile error, `exec.WithAgentStepTranscriptStore` undefined).
- [ ] `git apply ground_truth/reference.diff` then rerun: **PASSES**.
- [ ] `go test ./atc/exec/ -count=1` WITHOUT the overlay passes at pre_state and
      after the reference change (pass-to-pass).
- [ ] `go build ./atc/...` succeeds at pre_state and after the reference change.
- [ ] Confirm no PostgreSQL is needed: run the above with `PGHOST=127.0.0.1
      PGPORT=1` in the environment (the technique fix-jb-005 used).
- [ ] Record whether `-vet=off` was required (open question 2).

## Validation

- date: 2026-07-25
- validator: mechanical (detached worktrees, read-only git)
- worktrees: pre `c601df7f36af25ea57f0e957cb7f7432e189fd95`, post `711974f49cf942610f960d472b847cc3eaa8a3a8`
- outcome: **validated** (all three legs)

### fail_to_pass
`git apply <case>/ground_truth/withheld_tests/agent_step_test.patch && go test ./atc/exec/ -count=1`
(the patch applies cleanly at BOTH SHAs — the withheld specs are absent from the committed post file too, only the production symbol differs)

PRE (FAIL, exit 1 — the intended compile-error red):
```
# github.com/concourse/concourse/atc/exec_test [github.com/concourse/concourse/atc/exec.test]
atc/exec/agent_step_test.go:1698:11: undefined: exec.WithAgentStepTranscriptStore
FAIL	github.com/concourse/concourse/atc/exec [build failed]
```
POST (PASS, exit 0): `ok  github.com/concourse/concourse/atc/exec  1.082s`
(`WithAgentStepTranscriptStore` is defined at post in `atc/exec/agent_step.go:111`, absent at pre.)

Focused variant also confirmed:
`go test ./atc/exec/ -count=1 -args -ginkgo.focus='transcript store wired' -ginkgo.fail-on-empty`
PRE -> same build failure (exit 1); POST -> `ok  github.com/concourse/concourse/atc/exec  0.401s`.

### pass_to_pass (no overlay)
`go test ./atc/exec/ -count=1` — PRE `ok ... 1.091s`, POST `ok ... 1.080s`.
`go build ./atc/...` — exit 0 at both.

- corrected_cmd: none. `${CASE_DIR}` must be absolute.
- notes: no PostgreSQL — every leg was run with `PGHOST=127.0.0.1 PGPORT=1` and still passed. The historical `atc/exec/artifact_input_step_test.go` vet failure did NOT resurface; `-vet=off` was not needed.

## Fixup 2026-07-25

Curator-fixup pass over the dual audit (opus `pass` with curator notes, sonnet
`fail`). Every audit item resolved; residual verdict **pass**.

### Dissolved by the exposure contract (no action, nothing renamed)

Sonnet's FAIL rested entirely on `case.yaml` content:

- the title stating the root cause ("the ingestion reads the wrong step's flight
  volume");
- `ground_truth:`'s inline comment naming the two files the fix touches and that
  it is purely additive (+59/-0).

Per `bench/schema/benchmark-case-v1.md` § "The exposure contract", the solver sees
exactly *(pre_state at its pinned ref) − withheld + `task/`*. `case.yaml`,
`notes.md`, `ground_truth/` and the case id/path are harness-side and never
exposed, so a title or grading comment may state the answer freely. Nothing was
renamed or retitled. The same dissolution covers the self-spoiling withheld
filename (`ground_truth/withheld_tests/agent_step_test.patch`) and the
answer-bearing `curation.learnings`. The one operational rider from the contract
already holds here: a hand-run must materialize `task/` under a neutral name —
`rca-jb-005/` announces nothing, but the ground-truth siblings must not be copied
alongside it.

### Real defects fixed

1. `task/task.md`, ask #1 — "identify the specific **producer/consumer
   mismatch**" named the bug class outright (opus's first curator note). Now:
   "pin it down to the specific files and call sites involved." The demand for
   precision (which is what makes "something in ingestion is broken"
   ungradeable) survives; the classification does not. Same sentence now also
   requires each cleared piece to come with the evidence that clears it — see
   defect 3.
2. `task/task.md`, runner-image constraint — "justify why nothing on the web
   side can fix it" pre-empted the runner-blame trap that rubric A scores 0 for,
   by telling the agent in advance that a web-side fix is the expected shape.
   Now: "justify the cost." The authentic operational fact (the image pipeline is
   expensive and was unreliable that week — true at the cut) is unchanged.
3. `task/evidence/observations.md` §3 — the paragraph beginning "So for this
   build the server-side flight-recorder ingestion clearly ran and succeeded for
   both steps…" both contradicted the bundle's own "Observations only; no
   interpretation" header and performed rubric B1's entire inference (10 points)
   on the agent's behalf. Removed; §3 now reports the two queries, their output
   and the four spot-checks. Nothing factual was lost — the metrics-vs-transcript
   contrast, which is the case's central evidence, is still fully visible in the
   query output. The bundle header was also softened from "Observations only; no
   interpretation" to a claim it can actually keep (records what was run and what
   came back; conclusions limited to what an experiment directly showed), which
   keeps §4's hand-inserted-row result honest where it stands.
4. `ground_truth/rubric.md` B1 — now states that the bundle supplies the
   observation and draws no conclusion from it, so the inference is the agent's
   to make and the 10 points are for making it.
5. `ground_truth/rubric.md` D3 — opus's third curator note ("the negative-results
   half is largely copy-back"): the bundle hands over two of the four negatives
   (§4's read-path experiment, §1/§5's runner deployment), so D3 was nearly free.
   Added an anti-copy-back clause — restating the bundle's negatives earns at most
   1 of 3; full credit needs at least one negative the agent established from the
   repository itself (the 404 as `get.go`'s `found == false` branch, or the
   factory's `Upsert`/`Get` SQL).

### Grading defects fixed

6. **Missing delivery channel.** `task.md`'s deliverable asked for "a short
   written diagnosis" with no destination, leaving the judge nothing stable to
   grade. It is now filed as `DIAGNOSIS.md` at the repository root (the
   convention rca-jb-001 already uses for `RCA.md`), with a new
   `grading.deliverable` block in `case.yaml` and a clause at the top of
   `rubric.md`: grade `DIAGNOSIS.md` if present, otherwise the final response, and
   **do not deduct on channel alone**. An untracked file at the repo root affects
   neither `pass_to_pass` leg.
7. **`fail_to_pass` pins a location/shape the task leaves open.** The withheld
   overlay compiles against `exec.WithAgentStepTranscriptStore`; `task.md` says
   nothing about the seam's name or shape, so a correct fix with a different name
   fails the gate with a *compile* error. The flexibility now lives in
   `rubric.md` as a full section, **§ "Mechanical gate caveat"** (rename the
   symbol in the overlay and re-run before treating red as a failure; move the
   agent's own `agent_step_test.go` aside or `git apply -3`; and a green overlay
   is not sufficient for bucket C either, since C3's structural judgement is not
   mechanically observable). `case.yaml`'s `fail_to_pass` note now points at that
   section. This closes open question 1 procedurally — a rewritten neutral-seam
   spec would still be the better asset, and the question stays open as a corpus
   improvement, not as a defect in this case.
8. `notes.md` had two `## Validation` headings (the pre-run stub and the
   2026-07-25 record), making `case.yaml`'s `validation.notes: notes.md#validation`
   ambiguous. The stub is now `## Validation plan (pre-run checklist)` and says it
   is superseded.
9. `case.yaml`'s `grading.environment.note` still read "UNVALIDATED as written:
   the extractor did not run the suite" while `validation.status` was already
   `validated` with all three legs green — an internally inconsistent manifest.
   Replaced with what the validation run actually confirmed (no PostgreSQL —
   `PGHOST=127.0.0.1 PGPORT=1` throughout; `-vet=off` not needed). Open questions
   1 and 2 in this file annotated to match.

### Difficulty

Reaffirmed **moderate**, with the reasoning now recorded in `case.yaml`. Neither
auditor asked for a recalibration and the two available arguments cancel: the
symptom is zero-signal, which pushes toward hard, but a single
`grep -r transcript.ndjson` reaches both the producer (`agent/runner/runner.go`)
and the mis-located consumer (`atc/exec/harvest_step.go`), leaving one "are these
the same flight volume?" hop, which pushes the other way. The enum offers no
level between the two; moderate is the defensible middle.

### Priced deflators kept

Both in-tree artifacts that partially reveal or actively distort the answer stay
exposed, because authenticity wins and `rubric.md` already tells the judge to
credit causal reasoning over quotation:

- migration `1773106093`'s header comment "harvest ingestion persists it here"
  (the authors' wrong belief, pre-cut) — the calibration notes say citing it is
  not evidence;
- `atc/exec/harvest_step_test.go`'s green transcript specs — authentic, and
  believing them is exactly the mistake the humans made.

Neither collapses the task on its own; nothing was added to `withheld`, which
stays empty.

### Known leak channel

`known_leak_channels: [project-auto-memory]` added to `case.yaml`. This dev
machine's project auto-memory (`memory/project_agentic_ux_audit_4.md`) states the
answer verbatim — the wrong-exec bug, both files the fix touches, and the pinned
option name — i.e. rubric A1, A2, C1 and C2 in one sentence. Memory is not edited;
per `bench/README.md` § "Operator-environment leakage", the replay harness must
not mount project memory, session context or conversation history into the solver,
and a local hand-run here is invalid unless memory is suppressed.

### Not changed

`pre_state`, `information_cut` (2026-07-20T15:34:20-07:00 — re-verified as the
pre_state commit's own committer date, and consistent with `task.md`'s "Filed:
2026-07-20"), the materialize recipe, `withheld: []`, the reference diff, the
withheld test patch, `answer.md`, `expected_findings.yaml`, and every validation
result. The exposed surface changed only by subtraction (the §3 editorial) plus
the two softened task sentences and the added delivery instruction; no grading leg
needs re-running.
