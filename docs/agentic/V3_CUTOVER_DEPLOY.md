# v3 cutover — deployment runbook

One-time runbook for the deployment that follows the v3-only cutover merge
(`merge: absorb origin/main into the v3 snapshot-workflow platform`).

This is a **hard cutover, not a rolling upgrade**. Schema v1/v2 is removed, and
the platform intentionally starts with zero live workflows until v3 sources are
imported. Written for the single deployment, on the explicit basis that no
historical agent data needs to be preserved.

## What changes at runtime

| | Before | After |
|---|---|---|
| Workflow schema | v1 / v2 | **v3 only** — `parse.go` rejects anything else |
| Execution identity | ticket + `agent-ticket-<id>` pipeline | **durable workflow run** |
| Delivery | `harvest:` step pushed from the pod | `publish_snapshot` → publisher → **gateway** |
| Merge compute | `merge:` step (pod-side push) | **`agent/functions/repositorymerge`** via `function-runner` |
| Migration head | `1773106095` | **`1773106128`** |

## Order of operations

### 1. Rebuild the agent-runner image — required, do this first

The image now ships a **second binary**, `function-runner`, which every v3 task
function runs as. Without it, `merge-preflight` / `merge-prepare` steps fail at
exec with "executable file not found".

Trigger the manual `build-agent-runner-image` job in `deploy/concourse-pipeline.yml`
(builds `deploy/agent-runner/Dockerfile`, pushes `ghcr.io/tdmtrader/agent-runner`).
Record the resulting `@sha256:` digest.

`deploy/agent_runner_dockerfile_test.go` asserts the image builds both binaries and
no longer packages `harvest-runner`, so a stale Dockerfile fails CI rather than
shipping quietly.

### 2. Point the web at that exact digest

`--agent-step-image` (`CONCOURSE_AGENT_STEP_IMAGE`) **must be an exact
`@sha256:` digest** — schema-v3 workflow runs and resource snapshot captures
reject a tag. Agent steps error at runtime when it is unset.

### 3. Reset the database

Migrations `1773106100`–`1773106128` all apply in one boot. Because no history is
being preserved, dropping the database is cleaner than migrating through:

- it skips `1773106124`'s backfill, which would otherwise NULL every historical
  metric's attribution (`agent_workflow_runs` is created empty in the same boot);
- it skips `1773106125`, which would archive every legacy outcome as
  `missing-run` for the same reason;
- `1773106126` is written defensively (`CREATE TABLE IF NOT EXISTS` /
  `DROP TABLE IF EXISTS`) so it applies either way;
- `1773106127` — exposure lineage tables (`agent_snapshot_exposures`,
  `agent_snapshot_exposure_paths`) plus a backfill that records every existing
  lineage row as a `full` exposure of its input snapshot's own digest; on a
  dropped database there is nothing to backfill.
- `1773106128` — partial index on `agent_workflow_runs (template_pipeline_id)`,
  probed by the workflow-run template collector and the resource-capture
  reads; on a dropped database there is nothing to index.

Verify afterwards: `docs/migration/migrate-preflight.sh` expects
`JETBRIDGE_VERSION=1773106128`.

### 4. Deploy the web, then import v3 workflow sources

Migration `1773106123` sets `live = false` on every non-v3 workflow definition and
adds a CHECK preventing re-promotion, so **the platform boots with no live
workflows**. That is expected. Import the six v3 seeds:

```bash
fly -t <target> agent workflows import agent/workflow/seeds/<name>-v3 --set-live
```

Seeds: `small-fix-v3`, `code-review-v3`, `log-diagnosis-v3`,
`version-upgrade-v3`, `anonymization-audit-v3`, `merge-delivery-v3`.

### 5. Smoke test

1. `fly agent workflows list` — six live workflows.
2. Create and dispatch a ticket against `small-fix-v3`; confirm the dispatch
   response carries `workflow_run_id` and the run reaches a terminal state.
3. Open the run in the web UI; confirm steps, metrics and outputs render.

## Post-upgrade sequence — every deploy after the cutover

The cutover order above is one-time. Every subsequent deploy of this branch runs
this sequence, **in this order**:

1. **Push, self-build, migrate, restart web.** The push triggers the self-build
   chain; the new web image applies any pending migrations on boot and restarts.
2. **Rebuild the agent-runner image from the *same commit*** via the manual
   `build-agent-runner-image` job, then bump the digest in home-infra. The web
   and the pod-side binaries (`agent-runner`, `function-runner`) are two
   halves of one contract — record schema descriptors, gate wording and
   contract types are compiled into both.
3. **Re-import all six v3 seeds with `--set-live`**:
   ```bash
   fly -t <target> agent workflows import agent/workflow/seeds/<name>-v3 --set-live
   ```
   Seed prompt or plan changes are not picked up by the web restart — they are
   stored workflow versions, created only by an import.
4. **Only then dispatch.**

Why the order is not advisory:

- A run dispatched between (1) and (2) executes against the **old** pod image.
  Its steps burn the full budget slice and then fail the seal gate, or worse
  succeed against stale contract code. That spend is not recoverable.
- Dispatch **freezes the workflow version** onto the ticket
  (`agent/dispatch/dispatch.go` pins `WorkflowVersion` at dispatch and
  subsequently resolves by that exact version instead of `Live`). A run queued
  before (3) therefore stays bound to the pre-import version forever — a later
  import does not repair it. Re-dispatch on a fresh ticket instead.

### Permanent coupling: descriptor bumps require an agent-runner rebuild

Any future **record-schema descriptor bump** must ship an agent-runner rebuild
**in the same deploy**. The pod-side `function-runner` stamps `record.schema`
from its own compiled-in descriptors (`contracts.NewRecord` →
`contracts.SchemaDigestFor`), while the web's seal gate admits only the current
digest by exact equality (`currentSchemaDigestOnly` in
`agent/snapshot/contracts/record.go`). A web that has moved to revision *n+1*
against a pod still stamping revision *n* rejects every record that pod
produces, after each step has already been paid for. This coupling is structural
and does not expire.

## Known limitations

- **`merge-delivery-v3` has not been exercised against a real remote.** The merge
  compute, contracts and approval gate are unit- and integration-tested, but the
  end-to-end path through the gateway to a live git host has never run. Treat the
  first real delivery as a supervised experiment.
- **`expected_base_sha` is server-derived** from the merged change's sealed
  `base_sha`; authoring it in a workflow is now **rejected**. Any hand-authored
  merge definition carrying it must be re-imported without it.
- A `repository-change/v1` snapshot sealed without contract-validator intrinsic
  metadata cannot be merged (fails closed with "merge base is unavailable").
- Deliberately dropped in the cutover, tracked as follow-ups: the step DAG, the
  in-app diff, per-section `/agent/*` routes, the transcript viewer, and the web
  ticket create-form (`fly agent tickets create` still works). Transcripts are
  captured and served by API; only the viewer is gone.

## Rollback

`git checkout v3-prototype-verified-20260724` restores the pre-merge branch state.
There is no in-place database downgrade path worth trusting across 27 migrations —
roll back by restoring the previous image and dropping the database again.
