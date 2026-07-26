# Platform Test Hardening and Containment

- **Date:** 2026-07-25
- **Status:** Approved for implementation
- **Base:** `410d9b59f8` — declared record schemas, enforced core validator, coordinated revision-2 bump
- **Inputs:** an external reviewer's testing recommendations for the agentic v3 platform, and a
  six-track coverage audit of this branch performed 2026-07-25 (sealing determinism, record-schema
  conformance, crash/idempotency, DAG/e2e/budgets, adapters/GC-lineage, and a 58-invariant
  design-doc-to-test mapping). Findings below cite that audit; file:line references were verified
  against `410d9b59f8`.

## Why

The audit's headline: the platform's deterministic spine is far better tested than the external
review assumed — seal determinism, canonical JSON, selection-from-exposure, schema-digest history,
and publish crash-recovery all have strong, often excellent, coverage. The real weaknesses are of a
different shape:

1. **Execution, not existence.** The best tests in the repo never run in CI. Every release-gating
   pipeline excludes `/atc/db` (no Postgres on the runner image), `agent/schema` is a separate
   module invisible to root `go list ./...` and runs in no pipeline, and `make test-dev-mcp` is
   wired to nothing automated. The lineage, retention, migration, and publication-store invariants
   are guarded only by a human remembering `make test-quick`.
2. **The two halves of the DAG never meet.** `agent/workflowrun/e2e_test.go` proves plan shape and
   executes nothing; `atc/db/agent_workflow_run_integration_test.go` proves sealing/reconciliation
   by hand-inlining what an agent would produce. Nothing executes the real agent-step path
   (`atc/exec/agent_step.go`) against the real sealer; nothing chains step outputs into step
   inputs; fan-out, gates, and selection are exercised by no test and no seed anywhere.
3. **Containment is admission-time only.** Budget enforcement is pre-flight refusal plus
   `--max-turns`/`--max-budget-usd` flags handed to the Claude CLI and trusted to self-enforce.
   Deleting the `--max-turns` line from the runner keeps its suite green. Agent steps have no
   default wall-clock timeout. The one real kill test triggers on a bare context cancel that
   nothing connects to a budget, turn, or time breach. There is no global switch that suppresses
   external actions without a redeploy.
4. **The publisher's adversary is imaginary.** Every HTTP response the publisher tests ever
   observe is 200. There is no retryable/terminal error taxonomy — a permanently-rejected
   publication retries forever on every lease expiry. The out-of-repo gateway's durable
   idempotency mapping, which all crash-safety rests on, is exercised by fakes that are
   cooperatively consistent by construction.

This design adopts the external review where it lands, adapts it where its premises don't match
this codebase, and adds the audit's own findings. It is organized as seven independently
executable workstreams, each with its own implementation plan under
`docs/superpowers/plans/test-hardening/`.

## Disposition of the external review

| Reviewer item | Disposition |
|---|---|
| Spine-vs-stochastic reframe | Already the operating model; no work |
| 1a. Fixture (stub) agent | **Adopt** — WS2. Highest-leverage item; also the replay primitive (item 6) |
| 1b. Adversarial agent at the seal boundary | **Adapt** — contracts layer already strong; add step-boundary suite (WS2) and scale limits (WS5) |
| 1c. Repair loop hard-fails | **N/A** — no repair loop exists; one-shot failure is stricter. Revisit only if re-prompting is ever built |
| 2. Property tests on canonicalization/sealing | **Adapt** — determinism test already exists; add collision table, true round trip, native Go fuzzing (WS6). No property-testing framework dependency |
| 3. Crash injection at side-effect boundaries | **Adapt** — crash-3 and double-publish already covered; add crash-4 store fake, stale-lookup adversarial arm, TOCTOU documentation, index-repair assertion (WS4, WS7) |
| 4. Adapter conformance suites | **Adapt** — no check/in adapter exists (capture is pinned-get via ATC; lidar checks upstream); the conformance-kit pattern already exists twice. Add a gateway-protocol conformance/adversarial suite and error taxonomy (WS4) |
| 5a. Re-anchoring corpus | **Skip** — no anchor-remapping component exists; anchors bind to immutable digest-verified subjects |
| 5b. Type-conformance corpus | **Adopt the missing 10%** — per-rule negatives largely exist; add the mechanical rule-to-test linkage the `go_only_rules` arrays already enumerate (WS5) |
| 6. Replay mode / dogfooding | Replay was never built; the fixture agent (WS2) is the replay primitive. Dogfooding already the operating model |
| 7. Docs-first go-live gate | Performed as part of the audit: 58 invariants, 43 covered, 2 NONE (CU-11 → WS7; record-refs deferred by design) |
| 7. Kill switch + budget caps with hard termination | **Adopt** — WS3. The strongest items in the review for this codebase |
| 7. Staged rollout ladder | **Adapt** — reversible-only modes and the merge approval gate already exist; the suppression switch (WS3) enables a shadow phase; the gateway sandbox test (WS4) covers the sandbox phase |

## Workstreams

Ordered by value. Each is independently executable and lands working, tested software on its own.
WS1 should land first (it makes every other workstream's tests actually run); the rest have no
ordering constraints between them.

---

### WS1 — CI executes what exists

**Problem.** `deploy/concourse-pipeline.yml` `unit-tests` (which gates `tag-rc` → `build-image` →
`release`) and `deploy/dogfood-pipeline.yml` `test-quick-gate` run an identical plain
`go test -count=1` over `go list ./...` with `/atc/db`, `/atc/gc`, and friends excluded, on an
image with no PostgreSQL. `agent/schema` (own module) is invisible to that glob and compensated
nowhere. `make test-all` omits `test-dev-mcp`; no pipeline invokes it. `agent/devmcp/e2e`'s
`TestLiveImageContract` skips on a CI job name (`build-mcp-dev-image`) that has never existed.
There is no `-race` execution anywhere; the repo-wide ban exists because of ginkgo `-p` parallel
compilation failures, which does not apply to a plain `go test -race` over the plain-`testing`
`agent/...` packages.

**Decision.**

1. **A `db-tests` job in both pipelines** running the Postgres-backed suites
   (`atc/db`, `atc/gc`, `atc/integration`) against a real PostgreSQL started inside the task.
   Mechanism: extend the test-runner task image (or add a variant image) with `postgresql` +
   `postgresql-contrib`, start it in the task script (`pg_ctlcluster`/`pg_ctl` + `pg_isready`
   gate), then run `ginkgo -r -p ./atc/db ./atc/gc` and `go test ./atc/integration/...`.
   `tag-rc` (release) and the dogfood gate acquire the new job as an input alongside
   `unit-tests`. The template-database optimization already used locally applies unchanged.
2. **Module coverage**: append `(cd agent/schema && go test ./...)` and
   `(cd ci-agent && go test ./...)` steps to the unit-tests task script in **both** pipelines.
3. **dev-mcp**: add `test-dev-mcp` to `make test-all`; add a `dev-mcp-tests` step (or fold into
   unit-tests) running `make test-dev-mcp` in CI. Re-gate `TestLiveImageContract` on an explicit
   env flag (`DEV_MCP_IMAGE_TEST=1`) with a comment stating who sets it, replacing the reference
   to the never-created job.
4. **A scoped race lane**: new Makefile target `test-agent-race` = `go test -race -count=1` over
   the `agent/...` packages (plain `testing`, no ginkgo `-p`, so the documented ban does not
   apply), wired as a CI step. The plan must first run it locally and triage any real races it
   finds — a red result here is a finding, not a rollback reason.
5. **Docs**: TESTING.md gains an `agent/` tier row and the race-lane exception; fix the stale
   `Makefile` comment claiming ginkgo skips plain-testing packages (verified false).

**Acceptance.** A deliberately broken `atc/db` test turns the release pipeline red. `go list`
output in the unit-tests job log shows no silently-skipped agent package. `make test-all` runs
dev-mcp. The race lane is green in CI.

---

### WS2 — Fixture agent and the first true DAG e2e

**Problem.** No test executes `AgentStep.Run` against the real sealer (`agent_step_test.go` uses
a `recordingOutputSealer` fake); no test chains one step's sealed output into the next step's
input; the `await_snapshot` → `publish_snapshot` approval binding and disposition recording have
no end-to-end proof; the live-cluster behavioral e2e substitutes a busybox `cp` **task** node for
the agent node because the suite never sets `--agent-step-image`. Hostile agent output is proven
only inside `agent/snapshot/contracts`, never as an operator-visible step failure.

**Decision.** Three tiers, one fixture mechanism.

1. **A fixture agent** with two faces:
   - `cmd/fixture-agent`: a small Go binary honoring the runner's env contract — it reads
     `AGENT_OUTPUT_<NAME>` destinations, copies a recorded fixture tree (`record.json` + blobs,
     selected by `FIXTURE_CASE`) into each, writes a minimal flight-recorder trio
     (`events.ndjson`, `results.json`, `transcript.ndjson`), and exits 0. An adversarial mode
     (`FIXTURE_CASE=hostile-*`) emits the hostile catalog below. Built into a tiny image for the
     behavioral tier by the existing `docker save` + `nodeutils.LoadImageArchive` path.
   - An in-process equivalent for exec-level tests: a scripted step process that writes the same
     fixture trees into the step's output mounts.
2. **Tier A — exec-level, real sealer.** New specs in `atc/exec` wiring `AgentStep` to the *real*
   `snapshot.BatchSealer` + `contracts.NewRegistry()` over in-memory stores, with the in-process
   fixture writing outputs. Positive cases: a `review/v1` seals and lands in `build.Repository`;
   optional-output markers honored. Adversarial cases (each asserting the exact operator-visible
   error text, not just non-nil): `../` path traversal in the output tree; a symlink escaping the
   tree; an anchor subject not in the declared exposure; a record whose `schema` digest differs
   from the platform-injected value; a duplicated entity ID; a missing `record.json`; an output
   exceeding the (test-injected, see WS5) size limit.
3. **Tier B — in-process chained DAG.** Extend the
   `atc/db/agent_workflow_run_integration_test.go` pattern into a two-plus-step function run
   under real Postgres: step 1 seals a typed output via the real sealer, the run's plan feeds it
   to step 2 as a typed input (`load_snapshot`), a `await_snapshot` wait is answered
   server-side, and the run reaches a terminal disposition with the outcome row asserted. The
   plan's scouting step must confirm the cheapest honest execution seam (existing build/exec
   fakes vs. driving `build.Start`/`Finish` per step as the current integration test does) — the
   requirement is that *typed output → typed input chaining and disposition* are asserted, not
   that the full engine scheduler runs in-process.
4. **Tier C — behavioral agent-node e2e.** In `topgun/k8s_behavioral`, deploy with
   `--agent-step-image` pointing at the fixture-agent image and convert (or add a sibling to) the
   existing workflow spec so the DAG's node is a real `agent:` step: fixture output → seal →
   download → digest assertion. This closes "a `task:` e2e silently stands in for an `agent:`
   one."
5. **Test hygiene in reach:** make `judge`'s unexpected-dimension branch reachable
   (`agent/functions/judge/runner_test.go` fixtures currently cannot hit `runner.go:225-229`).

**Non-goal here:** wiring `gates`/`judge`/`repositoryvalidate` into `cmd/function-runner` (three
tested functions with zero production callers) is real feature work, deliberately out of scope —
see Deferred.

**Acceptance.** `ginkgo ./atc/exec/ --focus="fixture"` exercises the real sealer with zero fakes
between step and contract validation. The chained test proves output→input propagation and
disposition under Postgres. The behavioral suite runs at least one true `agent:` node. Every
hostile case fails with a message an operator can act on.

---

### WS3 — Containment: kill switch, budget watchdog, wall-clock bounds

**Problem.** No mechanism suppresses external actions without a redeploy (the dispatcher toggle
gates only ticket auto-dispatch — not manual dispatch, running builds, experiments, or
publishers). Budget enforcement is admission-time; the documented mid-flight cutoff is
unimplemented; `--max-turns` plumbing is unasserted; agent steps default to *no* timeout
(`MaybeTimeout` with unset default); nothing connects any breach to the (well-tested)
process-group kill.

**Decision.**

1. **Global action-suppression switch**, cloning the dispatcher-mode pattern end to end:
   - Migration `1773106128` (verify still-free at implementation time): add
     `actions_mode TEXT NOT NULL DEFAULT 'active' CHECK (actions_mode IN ('active','suppressed'))`
     to the singleton `agent_settings` row.
   - `atc/db/agent_settings.go`: `GetActionsMode` (hot read) / `SetActionsMode(mode, updatedBy)`,
     mirroring the dispatcher methods.
   - Enforcement at the single choke point all external side effects flow through: the publisher
     services (`agent/publisher/git.go`, `workitem.go`) check the mode **before** `Lookup`. When
     suppressed, return a typed `ErrActionsSuppressed`; the durable operation row stays
     `pending` (retryable — the run can be retried after resume), and the step error names the
     switch. Fail-safe direction: **read error ⇒ suppressed** (consistent with the dispatcher's
     paused-on-error; a DB blip that can't read settings could not have completed the durable
     publish protocol anyway). Missing row ⇒ active (the switch is an emergency brake; absence
     means not engaged).
   - API: extend the agent settings surface with GET/PUT for actions mode (same handler family as
     `/api/v1/agent/dispatcher`), plus `fly agent actions suppress|resume|status`.
   - Explicitly **not** gated: dispatch, agent execution, sealing — suppression bounds *external
     effects*, not compute. This is what makes it a shadow-mode enabler.
2. **Runner-side watchdog** in `agent/runner`: a goroutine consuming the already-parsed
   stream-json events, tracking cumulative cost and wall clock against the same values passed as
   CLI flags (plus a new `AGENT_MAX_WALL_CLOCK` with a platform default). On breach: cancel the
   existing process-group kill path and stamp `results.json` with a distinct
   `terminated_reason` (`budget|turns|wall_clock`). The CLI's own `--max-budget-usd` remains the
   first line; the watchdog is the platform-side backstop the reviewer asked for. Tests drive a
   fake CLI script that emits cost events and never exits, asserting kill + classification —
   reusing the existing descendant-kill test's polling technique with the *breach* as trigger.
3. **Web-side default wall-clock bound**: new `--agent-step-default-timeout` (default `2h`,
   `0` disables) applied in `atc/exec/agent_step.go` where `MaybeTimeout` currently receives the
   unset task default; an explicit per-step `timeout:` still wins. A spec asserts the timeout
   fires and surfaces `TimeoutLogMessage` (the assertion task/get/put have and agent lacks).
4. **Pin the plumbing**: runner tests asserting `--max-turns` and `--max-budget-usd` literally
   appear in the constructed argv (killing the delete-the-line-suite-stays-green hole); seeds
   gain explicit `max_turns` values.

**Non-goal:** a web-side ledger-driven build abort (cross-pod enforcement). The runner watchdog
plus default timeout close the runaway-overnight scenario; the ledger abort is a later layer —
see Deferred.

**Acceptance.** `fly agent actions suppress` makes a publish step fail with the switch named and
leaves the operation row pending; resume + retry publishes exactly once (idempotency preserved
across suppression). A never-exiting fake CLI is killed at the configured wall-clock/budget with
the reason recorded. Removing `--max-turns` from the runner makes a test fail.

---

### WS4 — Publisher gateway: adversarial reality and error taxonomy

**Problem.** `gatewayClient.do` collapses every non-200 into one flat string (no `%w`, no status
distinction), so terminal 400/403 retries forever on lease expiry with no backoff. The fake
gateway never returns anything but 200 (and `GatewayConfig` exposes no Transport seam to make it
otherwise). The eventual-consistency arm of lookup-before-write is untested — both fakes flip
themselves consistent. Crash point 4 (provider call succeeded, durable completion failed) is
structurally untestable: no `publisher.Store` fake exists, and nothing pins the Lookup-first
ordering. `TestGatewayRejectsOversizedAndMalformedResponses` never reaches the JSON decoder. The
work-item reconcile path trusts the recovered result verbatim where the git path cross-checks
`HeadSHA`.

**Decision.**

1. **Error taxonomy** (production change): a typed `GatewayError{Status int, Retryable bool}`
   returned by `do`, wrapped with `%w`. Classification: 400/401/403/404/409/422 terminal;
   408/429/5xx and transport errors retryable. In `git.go`/`workitem.go`, a terminal
   classification completes the operation as `failed` (ending the retry-forever loop) while
   retryable preserves today's pending-and-reclaim behavior. Both classes tested.
2. **Transport seam + adversarial fake**: `GatewayConfig` gains an optional
   `Transport http.RoundTripper` (nil ⇒ current behavior). A table-driven adversarial suite
   drives: 429 (+`Retry-After` present but unread — assert we at least classify), 500, 503,
   response-header timeout, mid-body connection reset, malformed JSON *with `MaxResponseBytes`
   raised so `decodeGatewayJSON`, `DisallowUnknownFields`, and `requireJSONEOF` are actually
   reached* (also splitting the misnamed test into its two honest halves).
3. **The stale-lookup arm**: a fake where `Publish` has landed but `Lookup` keeps answering
   `found:false`. The enforceable in-repo property: the retried publish carries the **same**
   `Idempotency-Key` as the original attempt (assert byte equality across attempts), so a
   contract-honoring gateway dedupes. This is the documented defense; the test makes it
   regression-proof.
4. **Crash-4**: introduce a `failingStore` wrapper (first `Complete` errors, then delegates);
   assert the retry performs exactly one backend `Publish` across both attempts and that
   `Lookup` precedes any write on the retry — pinning the ordering that today can be silently
   reordered.
5. **Work-item recovered-result cross-check** (production change): on reconcile, verify the
   recovered result's identifying fields match the request (the work-item analogue of git's
   `HeadSHA != result_sha` refusal); mismatch ⇒ retryable refusal, tested.
6. **Gateway conformance kit + live gate**: extract the four `/v1` endpoint expectations
   (auth header, idempotency-key rules incl. the `current-base` exemption, lookup-after-publish
   visibility, terminal/retryable status semantics) into a reusable kit in
   `agent/publisher/contracttest` run against the in-repo reference `httptest` implementation,
   and — behind `//go:build live` + env (`PUBLISHER_GATEWAY_URL`,
   `PUBLISHER_GATEWAY_TOKEN`) — against a real deployed gateway with a scratch destination. A
   manually-triggered pipeline job may wire it later; the env-gated test is the deliverable.

**Acceptance.** A 403 from the fake lands the publication in `failed` (no infinite retry); a 503
leaves it pending. The malformed-JSON decoder paths are covered. The crash-4 test fails if
`Lookup` is moved after the write. The kit passes against the reference fake.

---

### WS5 — Contract conformance: close the rule-to-test loop

**Problem.** The `go_only_rules` arrays in the six schema documents are a maintained enumeration
of every semantic rule, and `TestSchemaDocumentGoRuleReferencesResolve` proves the references
resolve — but nothing forces every rule to have a passing negative test, which is exactly the
corpus the reviewer asked for and the parity fuzzer's one blind spot (it is differential: a bug
both validators share is invisible). Enumerated per-type holes exist. Scale limits
(10 GiB payload, 1 MB JSON document, 100k entries, unbounded entity-sets) have zero boundary
tests and no injectable thresholds.

**Decision.**

1. **The linkage harness**: a new `agent/snapshot/contracts` test that (a) collects the union of
   `go_only_rules` IDs across all schema documents, (b) requires a registered *rejection witness*
   per rule — a fixture mutator producing an invalid instance plus the expected error fragment —
   and (c) runs every witness through the real `AdmitForSeal` gate, failing if any rule lacks a
   witness or any witness passes validation. Adding a rule without a witness turns CI red; this
   is the mechanical "every documented malformed case is rejected" gate, permanent.
2. **Fill the enumerated holes** (each a named test): review — duplicate finding ID, garbage
   `conclusion` enum, zero/two primary subjects; validation — non-skipped check with zero
   attempts; measurements — `partial` with zero metrics, missing explanation for
   `partial`/`not-applicable`, `Measurement`-level direction/target cross-rule; diagnosis —
   duplicate rank; repository-change — payload path is a directory/symlink, valid-but-non-
   ancestor `result_commit`, patch failing `git apply --check`, `base_sha` width vs.
   object-format mismatch; empty-ID named tests for each entity-set.
3. **Injectable limits + boundary tests**: convert the hard limit constants
   (`maxJSONDocumentBytes`, `maxRepositoryPayloadBytes`, entry counts) to package-level
   variables overridable in tests (or a `Limits` struct threaded where cheap); add at/over
   boundary tests, plus a 10,000-findings instance that must validate within the suite's normal
   timeout (a size *and* algorithmic-complexity guard).
4. **UTF-8 free text**: add `utf8.ValidString` admission checks to free-text body fields
   (summary, title, rationale, …). Safety argument: canonical JSON already rejects invalid UTF-8
   in *stored* strings, so no sealed record can contain it — tightening the seal gate is a no-op
   for history and needs no digest bump. Tests per field family via the existing mutation-family
   mechanism.

**Acceptance.** Deleting any `go_only_rules` witness fails the linkage test. Every enumerated
hole has a named red-then-green test. Limits are exercised at their boundaries with test-injected
thresholds.

---

### WS6 — Sealing and digest hardening

**Problem.** Zero `digestA != digestB` assertions exist anywhere (structural prefix-freeness is
real but unpinned); the round trip re-feeds canonical archive bytes rather than re-sealing a
materialized directory; tamper tests assert errors, not digest sensitivity; the repo has zero
fuzz targets despite content-addressing being its security model; capture re-run determinism is
unasserted (and plausibly false — `cp -a` of a git resource's `.git` is unnormalized); exposure
path digests are format-validated but never recomputed against landed bytes; the permission-
normalization identity boundary (only the exec bit survives) is undocumented-by-test; the
sealer/store have zero concurrent tests and `operation_locker`'s mutual exclusion is faked with
a counter.

**Decision.**

1. **Anti-collision table**: seal pairs of near-miss trees and assert digest inequality —
   empty file vs. absent file; empty dir vs. absent dir; `foo` as file vs. `foo` as dir;
   `"a\n"` vs. `"a"`; `a/bc` vs. `ab/c`; exec-bit flip (must differ). Companion
   **identity-boundary test** documenting what deliberately does *not* differ: `0640` vs `0644`,
   uid/gid, mtimes — so a future mode-preserving change is a loud, named identity-migration
   event instead of a silent one.
2. **True round trip**: extract a canonical archive to disk through the real extraction path,
   re-tar the materialized directory in the test, re-capture, assert equal digest; then flip one
   byte in one file and assert the digest differs.
3. **Native fuzzing** (stdlib `testing.F`, no new dependency): `FuzzCanonicalCapture` (arbitrary
   tar bytes ⇒ no panic; on acceptance, re-capture of the canonical output is a fixed point) and
   `FuzzCanonicalJSON` (arbitrary bytes ⇒ no panic; on acceptance, encode∘decode is identity).
   Seed corpora from existing test fixtures. Makefile target `test-fuzz` running each target
   time-boxed (`-fuzztime=30s`) wired into CI as a smoke lane; longer runs stay manual.
4. **Capture re-run determinism, honestly**: extend the existing expired-output re-capture test
   to compare digests. Two specified outcomes: if equal, pin it as an invariant; if drift is
   real (expected, via `.git` internals), assert instead that the new generation binds a *new*
   digest under the *same* capture identity, and document in the capture package that same-
   version snapshot equality is memoization, not determinism — closing the ambiguity either way.
5. **Exposure digest verification** (small production change): wherever static-selector exposure
   paths are materialized to disk, hash while writing and refuse on mismatch with the manifest's
   per-path digest; test with a corrupted store. (Scouting step locates the materialization
   site; if full-tree mounts never consult per-path digests, the check applies to the
   static-selector path only, which is where the claim is made.)
6. **Real contention**: two goroutines sealing byte-identical content through a digest-lease
   implementation that actually excludes (in-memory lease with real blocking, replacing the
   sequential fake for this test) — assert single commit, both callers converge on one digest;
   and an `operation_locker` test with two real goroutines contending on the real lock semantics
   (in-memory `lock.LockFactory` that genuinely excludes, or the Postgres factory under the
   `atc/db` suite), replacing the counter-faked "contention".

**Acceptance.** The collision table and identity-boundary tests exist and are cited from the
archive package docs. Round trip covers disk. `make test-fuzz` is green and wired. The capture
determinism question has a test-backed answer. Concurrent seal converges without corruption
under `-race` (WS1's lane).

---

### WS7 — CAS and durable-state integrity

**Problem.** The `binder.go` lost-CAS branches (`:429-432`, `:459-468`) have no test; ticket
`Transition` has stale-precondition tests but no concurrent proof (and `task_race_test.go`
contains zero goroutines despite its name); CU-11 — a dispatched run's bound input must not
change when the ticket is later edited — is named in the cleanup design's own acceptance list
and untested; the input-side seal race (seal validates inputs via an `available` filter, no row
lock — GC serialization covers only *output* digests) is unlocked and untested; the 20-RESTRICT/
3-CASCADE FK topology on `agent_snapshots` is unpinned by any constraint test; the index-repair
path (`linkSucceededPublicationOutcome` on terminal re-acquire) executes in tests but is never
*asserted* as repair; expired retention-claim rows accumulate forever.

**Decision.** All under the `atc/db` suite (which WS1 puts into CI) unless noted:

1. **Lost-CAS branches**: scripted-store tests driving `transitioned=false` through both binder
   branches, asserting the run neither double-starts nor wedges.
2. **Ticket `Transition` under contention**: N goroutines racing the same `from` state — exactly
   one wins, the rest get `ErrStaleTransition`, final state read back. (The single-statement
   `UPDATE … WHERE state=$from` is atomic; this pins it.) Rename or annotate
   `task_race_test.go`'s decorator tests as sequential TOCTOU regressions, which is what they
   are.
3. **CU-11**: dispatch a ticket, mutate its title/body/bindings, assert the bound run's input
   snapshot IDs and rendered config are byte-identical to pre-edit.
4. **Input-side seal race**: using the existing advisory-lock barrier technique, force GC expiry
   of an input digest between `validateSealInputs` and `CommitSealBatch`. Desired behavior:
   the commit must fail closed (no lineage row referencing expired-at-commit content) — if the
   current code commits, add the input re-check (or `FOR SHARE` on input rows) inside
   `CommitSealBatch` as the fix this test drives.
5. **FK topology pin**: a migration test asserting `pg_get_constraintdef` for every FK into
   `agent_snapshots` — the RESTRICT set and the exact three CASCADEs — so adding a delete path
   that would reach a CASCADE is a deliberate act.
6. **Index repair asserted**: delete the `agent_workflow_outcomes` row for a terminal
   publication, re-`Acquire`, assert the row is recreated with identical content.
7. **Retention-claim reaping** (small production change): the lifecycle sweep deletes claim rows
   whose `expires_at` is more than a grace period (default 30 days) in the past; tested,
   metriced, and off-by-zero-grace-safe (never deletes a claim that still retains).

**Acceptance.** All new suites run under the WS1 `db-tests` CI job. The input-side race test
either proves fail-closed or lands with the fix that makes it so.

---

## Deferred — surfaced, deliberately not in scope

These need their own conversations; folding them in here would bloat every plan they touched.

1. **The 7-day evidence-expiry policy.** Byte expiry behind permanent sealed records was a
   written, deliberate decision — but it quietly undermines "every run is a replayable fixture"
   and "did the judge see the diff" (exposure lineage is currently write-only). If run-as-fixture
   is wanted, the `fixture` retention-claim class already exists as the mechanism.
2. **`RevalidateSealed` on the ordinary load path.** Today only the merge/validate functions
   re-validate; `load_snapshot` trusts digests. Turning the read gate on for ordinary loads is a
   performance/consistency tradeoff to decide explicitly.
3. **Wiring `gates`/`judge`/`repositoryvalidate` into `cmd/function-runner`** (and a seed that
   uses fan-out + selection). Real feature work; WS2's e2e makes the absence visible.
4. **Web-side ledger-driven abort** (cross-pod budget enforcement) — layered above WS3's
   runner watchdog if ever needed.
5. **Record-ref (Fork 2) semantics** — designed, loudly refused in code, correctly deferred.

## Cross-cutting constraints

- `agent/schema` must never import the main module (breaks `ci-agent`); nothing here touches it
  beyond CI wiring.
- Migration numbers: `1773106128` is next-free at time of writing — every plan that assigns one
  must re-verify against `atc/db/migration/migrations/` at implementation time and renumber
  forward if taken.
- Production behavior changes in this design are exactly: WS3 (switch, watchdog, default
  timeout), WS4 items 1/5 (error taxonomy, work-item cross-check), WS5 items 3/4 (injectable
  limits, UTF-8 admission), WS6 item 5 (exposure digest verification), WS7 items 4/7 (input
  seal re-check if driven, claim reaping). Everything else is tests, fixtures, CI, and docs.
- Seal-gate tightenings (WS5 UTF-8) must include the two-gate argument for why stored records
  are unaffected; anything that would reject previously-sealed bytes at read time is forbidden
  (that is the descriptor-digest data-loss class).
- No new third-party test dependencies (fuzzing is stdlib; no property-testing framework).
- The existing test conventions hold: plain `testing` in `agent/`, Ginkgo in `atc/`,
  fakes via counterfeiter where a package already uses them.

## Plans

Implementation plans live in `docs/superpowers/plans/test-hardening/`:
`01-ci-execution.md` … `07-cas-db-integrity.md`, one per workstream, with a `README.md` index.
Each is independently executable; WS1 first is strongly recommended.
