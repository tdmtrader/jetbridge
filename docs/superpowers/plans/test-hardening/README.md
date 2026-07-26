# Test Hardening and Containment — plan suite

Implements [the 2026-07-25 design](../../specs/2026-07-25-platform-test-hardening-design.md).
Seven independently executable plans, one per workstream. **Execute 01 first** — it puts the
Postgres-backed suites, `agent/schema`, dev-mcp, the race lane, and the fuzz smoke lane into CI,
so every later plan's tests actually run somewhere. The rest have no hard ordering; the numbers
are the recommended (value-ranked) order.

| Plan | Tasks | What it lands |
|---|---|---|
| [01-ci-execution.md](01-ci-execution.md) | 11 | `db-tests` CI job (postgres-in-task via `postgresrunner`), test-runner image build job, `agent/schema`/`ci-agent` module steps, dev-mcp wiring, scoped `go test -race ./agent/...` lane, guarded fuzz smoke step, docs corrections |
| [02-fixture-agent-e2e.md](02-fixture-agent-e2e.md) | 13 | `agent/fixtureagent` + `cmd/fixture-agent` (deterministic stub agent, hostile catalog), Tier A exec specs against the **real** sealer, Tier B chained workflow run under Postgres, Tier C real `agent:` node in the behavioral suite, `web.agentStepImage` chart value, judge test fix |
| [03-containment.md](03-containment.md) | 8 | Migration `1773106128` (`actions_mode` + nullable `dispatcher_mode`), publisher-level action-suppression switch (API + `fly agent actions`), runner budget/turn/wall-clock watchdog with process-group kill, `--agent-step-default-timeout`, argv pinning, seed `max_turns` |
| [04-publisher-gateway.md](04-publisher-gateway.md) | 10 | `GatewayError` retryable/terminal taxonomy, terminal ⇒ `failed` (ends retry-forever), reference gateway with fault modes, adversarial response table, stale-lookup idempotency-key pin, crash-point-4 + Lookup-first ordering pin, work-item result validation, conformance kit + `//go:build live` gate |
| [05-contract-conformance.md](05-contract-conformance.md) | 15 | Rule→rejection-witness linkage harness over all 52 `go_only_rules` (fragments empirically derived), enumerated per-type negative-test holes, injectable scale limits + boundary tests, 10k-findings guard, raw-bytes UTF-8 seal gate |
| [06-sealing-hardening.md](06-sealing-hardening.md) | 7 | Anti-collision + identity-boundary tables, materialize-to-disk round trip + byte-flip, `FuzzCanonicalCapture`/`FuzzCanonicalJSON` + `make test-fuzz`, capture re-run determinism (dual-outcome), `VerifyExposedPaths` in the sealer, concurrent identical-content seal, real operation-lock exclusion |
| [07-cas-db-integrity.md](07-cas-db-integrity.md) | 9 | Binder lost-CAS branch tests, 16-goroutine ticket `Transition` proof, `task_race_test.go` honest rename, CU-11 (bound inputs survive ticket edits), input-side seal/GC race **fix** (`pg_advisory_xact_lock` on input digests), FK topology pin (24 constraints), publication index-repair assertion, retention-claim reaper |

## Cross-plan contracts

- Migration `1773106128` is assigned **only** by plan 03 (which re-verifies the number first).
- `make test-fuzz` is created by plan 06; plan 01 lands a grep-guarded CI step that no-ops until it exists.
- The contracts-layer limit constants belong to plan 05 alone; plan 02's oversized case uses the
  already-injectable archive-layer limits.
- Plans 04 (Task 3), 07 (all DB tasks), and 02 (Tier B) add `atc/db`-suite tests that run in CI
  only once plan 01's `db-tests` job exists; locally they need `pg_isready` green.
- Plans 01 and 06 both edit the `Makefile` (different targets); execute sequentially.

## Verification per plan

Each plan ends with a self-review task mapping its workstream's acceptance criteria (spec §WS*n*)
to tasks; run that task's commands after the last code task of the plan.
