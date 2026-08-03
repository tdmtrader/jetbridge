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
| Delivery | `harvest:` step pushed from the pod | `publish_snapshot` → **direct in-ATC publication** |
| Merge compute | `merge:` step (pod-side push) | **`agent/functions/repositorymerge`** via `function-runner` |
| Migration head | `1773106095` | **`1773106148`** |

## Order of operations

### 1. Rebuild the agent-runner image — required, do this first

The image now ships a **second binary**, `function-runner`, which every v3 task
function runs as. Without it, `merge-preflight` / `merge-prepare` steps fail at
exec with "executable file not found".

Trigger the manual `build-agent-runner-image` job in `deploy/concourse-pipeline.yml`
(builds `deploy/agent-runner/Dockerfile`, pushes the deployable commit tag to
`registry.home/agent-runner`, and attempts a best-effort GHCR mirror only after
the local immutable digest has been pulled, platform-checked, smoked, and
written to verified metadata). A GHCR login or push failure warns but does not
block the local deployment authority.
Require its `agent-runner-image-smoke` step to pass, then record the printed
`CONCOURSE_AGENT_STEP_IMAGE=<repository>@sha256:<digest>` only after the job
pulls that immutable reference and verifies it is `linux/amd64`.

`deploy/agent_runner_dockerfile_test.go` asserts the image builds both binaries and
no longer packages `harvest-runner`, so a stale Dockerfile fails CI rather than
shipping quietly.

### 2. Point the web at that exact digest

`--agent-step-image` (`CONCOURSE_AGENT_STEP_IMAGE`) **must be an exact
`@sha256:` digest** — schema-v3 workflow runs and resource snapshot captures
reject a tag. Agent steps error at runtime when it is unset.

### 3. Reset the database

Migrations `1773106100`–`1773106148` all apply in one boot. Because no history is
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
  reads; on a dropped database there is nothing to index;
- `1773106129` moves spend attribution onto `agent_cost_ledger.workflow_run_id`
  + `function_id`, drops the v2 `ticket_id`/`pipeline_run_id` columns, and
  narrows the source CHECK to `agent_step|ci_agent` — **deleting any ledger row
  whose source was `gateway`, `harvest_judge`, `retrospective` or `probe`**;
- `1773106130` drops `agent_run_metrics.workflow_name/version/hash`; workflow
  identity is read through `agent_workflow_runs`.
- `1773106131` drops `agent_ticket_comments` along with the ticket comment
  surface (no route, no reader; `work-item/v1` no longer carries a `comments`
  key). Its down migration recreates the table empty.
- `1773106132` makes `agent_reviews` snapshot-keyed: it **deletes every review
  row with no `snapshot_id`** (the owner-test corpus of the deleted v1 ingestion
  route), projects `conclusion` / `severity_counts` out of the stored record,
  and drops the derived `score`/`pass`/`*_count` columns along with `build_id`,
  `repo`, `commit_sha`, `branch`, `ticket_id` and `pipeline_run_id`.
- `1773106133` reduces the ticket to a queue shell: the terminal dispositions
  fold to `closed`, `sent_back`/`failed`/`errored` fold back to `needs_review`,
  `error_detail` is dropped, and `agent_ticket_specs` / `agent_ticket_tasks` are
  **dropped after folding each ticket's newest spec body into its body**.
- `1773106134` drops `agent_tickets.budget_usd`; budget lives on the experiment.
- `1773106135` makes `agent_feedback` snapshot-keyed: it **deletes every
  feedback row with no `review_snapshot_id`** — unreachable since `1773106132`
  removed the repo/commit columns any such row was keyed against — and drops
  `repo`, `commit_sha` and the write-orphaned `ticket_id`.
- `1773106136` drops the write-only leftovers: PARK-V2 (`agent_run_metrics`
  loses `session_id` and the `parked` status — **surviving `parked` rows become
  `error`**, which is what the ingestion rule now produces), the unread
  `agent_cost_daily_rollup` view, the Jira seams (`agent_user_credentials.
  jira_account_id`, origin `jira`), the read-never-written
  `agent_user_credentials.last_verified_at`, `agent_tickets.branch` (its writer
  was harvest), and the `agent_reviews` occurrence copies
  (`build_name`/`team_name`/`pipeline_name`/`job_name`/`submitted_by`, every
  read already joins the production). It also narrows
  `agent_workflow_outcomes.publication_state` to the two states `1773106115`'s
  evidence-shape constraint can actually hold.
- `1773106137` drops `agent_snapshot_exposure_paths` and narrows
  `agent_snapshot_exposures.materialization_mode` to `full`. The runtime
  materializes whole artifacts, so no static selector was ever written and the
  path table was empty; partial exposure is now unrepresentable rather than
  merely unused.
- `1773106138` seeds the singleton `agent_settings` row with `dispatcher_mode
  'off'`. The `--agent-dispatcher-enabled` and `--agent-dispatcher-max-attempts`
  boot flags are **gone** — remove them from the web command or it will refuse
  to start — and the seeded row is the only authority on whether the dispatcher
  auto-dispatches. A cluster that was auto-dispatching before the upgrade comes
  back dormant until someone runs `fly agent dispatcher resume`. Pausing is now
  strictly about dispatch: terminalizing a ticket whose run finished moved into
  the always-on workflow-run reconciler, so a paused dispatcher can no longer
  strand a running ticket.
- `1773106139` assigns every snapshot exactly one direct team owner and removes
  the retired grant relation; equal content may be separately owned without
  granting cross-team reads.
- `1773106140` removes agent-principal bearer-token authority. Human agent
  routes use ordinary team authorization.
- `1773106141` freezes authoritative validation provenance at seal time; an
  empty hash records historical absence rather than being recalculated from a
  mutable definition.
- `1773106142`–`1773106143` persist immutable resource-source admissions,
  including the exact selecting build and captured pipeline-config revision.
- `1773106144`–`1773106145` add immutable Hangar-backed checkpoint generations
  and fresh durable execution attempts; interrupted work is recovered into a
  new attempt rather than a retired runner.
- `1773106146`–`1773106147` attribute metrics and transcripts to those exact
  attempts while preserving the legacy projections.
- `1773106148` records the started experiment associated with each ready,
  immutable resource-source admission reused across cells and retries.

Verify afterwards: `docs/migration/migrate-preflight.sh` expects
`JETBRIDGE_VERSION=1773106148`.

### 3a. Vault the platform credential — the only model-credential path

Per-run `agent-run-<id>` secrets are gone. Every agent pod now mounts
`CLAUDE_CODE_OAUTH_TOKEN` from **one** secret, and
`--agent-platform-token-secret` now **defaults to `agent-platform-credential`**
— the secret the platform-credential syncer maintains from the vaulted platform
credential:

```bash
fly -t <target> agent auth --platform      # vault the platform credential
```

Without a vaulted credential the syncer deletes that secret, and workflow-run
binding fails closed (`no usable platform Anthropic credential`) before any
build is created.

A deployment that instead created the secret **by hand under a different name**
must keep passing `--agent-platform-token-secret <name>`
(`CONCOURSE_AGENT_PLATFORM_TOKEN_SECRET`). Binding takes an operator-named
secret at face value — the web never reads Kubernetes to check it — so its
contents are the operator's responsibility.

The syncer now also writes a second data key, **`kind`**
(`anthropic_oauth` | `anthropic_api_key`), beside `anthropic-token`, and the
agent step injects it as `AGENT_MODEL_TOKEN_KIND` via an **optional**
`secretKeyRef`. An operator-created secret without that key still starts pods
and is treated as OAuth. This is why the **agent-runner image must be rebuilt
with this web deploy** (step 1): the runner maps `AGENT_MODEL_TOKEN_KIND ==
anthropic_api_key` onto `ANTHROPIC_API_KEY` for the claude child process. An
older runner image keeps working for OAuth tokens, but will not authenticate a
raw API key.

### 4. Deploy the web, then import v3 workflow sources

Migration `1773106123` sets `live = false` on every non-v3 workflow definition and
adds a CHECK preventing re-promotion, so **the platform boots with no live
workflows**. That is expected. Import the seven v3 seeds:

```bash
fly -t <target> agent workflows import agent/workflow/seeds/<name>-v3 --set-live
```

Seeds: `small-fix-v3`, `code-review-v3`, `log-diagnosis-v3`,
`version-upgrade-v3`, `anonymization-audit-v3`, `merge-delivery-v3`,
`measure-review-v3` (the shipped experiment evaluator — see docs/agentic/README.md
"Experiments"; it takes no budget and produces no side effect, so importing it
live is safe even before any experiment references it).

### 5. Smoke test

1. `fly agent workflows list` — seven live workflows.
2. Create and dispatch a ticket against `small-fix-v3`; confirm the dispatch
   response carries `workflow_run_id` and the run reaches a terminal state.
3. Open the run in the web UI; confirm steps, metrics and outputs render.

## Post-upgrade sequence — every deploy after the cutover

The cutover order above is one-time. Every subsequent deploy of this branch runs
this sequence, **in this order**:

1. **Pause new agent dispatch, `self-upgrade`, and `release`.** Do not admit
   work or start a web promotion while the web and pod-side runtime can
   temporarily disagree.
2. **Reserve the version, then build and smoke the same-commit runner.** Wait
   for `tag-rc` to succeed for the exact commit being deployed and verify that
   its immutable RC tag points at that commit. Only then trigger the manual
   `build-agent-runner-image` job, and confirm its selected `repo` input is the
   same SHA before allowing it to run. Require
   `/usr/local/bin/agent-runner-image-smoke` to pass in the exact commit-tagged
   image. Its final `verified-image.env` is the authority: it must contain one
   `CONCOURSE_AGENT_STEP_IMAGE=registry.home/agent-runner@sha256:<64 lowercase hex>`
   record for the deployment commit. A positive budget slice
   is unsupported unless this smoke has proved the packaged Claude CLI accepts
   `--max-budget-usd`.
3. **Require the bounded GitOps digest write.** The unprivileged update task
   parses `verified-image.env` and makes the single manifest commit. A separate
   supervised, unprivileged task refreshes remote `main`, rebases that commit,
   and performs an ordinary non-force push. A conflict or non-fast-forward
   refusal leaves promotion paused for an operator retrigger.
4. **Require ArgoCD activation before promotion.** The same unprivileged,
   fail-closed GitOps path records the source commit and the resolved
   `registry.home/jetbridge@sha256:...` shared image under `image.digest` and
   `image.sourceCommit`. These fields describe the current candidate; they do
   not claim that it passed live tests. The chart applies those exact
   identities as provenance annotations to both the web Deployment and
   artifact-daemon DaemonSet. In the same bounded manifest commit, the writer
   removes the legacy ArgoCD `ignoreDifferences` entry for the
   `apps/Deployment/cicd/concourse-web` pointer
   `/spec/template/spec/containers/0/image`. It preserves every unrelated
   ignore item and pointer and fails closed if the target is duplicated or
   structurally ambiguous. An Argo sync alone is insufficient if either
   workload value differs; promotion stays paused in that case.

   ```bash
   WANT_IMAGE=$(sed -n 's/^CONCOURSE_AGENT_STEP_IMAGE=//p' verified-image.env)
   printf '%s\n' "$WANT_IMAGE" | grep -Eq '^registry.home/agent-runner@sha256:[a-f0-9]{64}$'
   argocd app get concourse --refresh --hard -n argocd -o json \
     | jq -e '.status.sync.status == "Synced" and .status.health.status == "Healthy"'
   kubectl -n cicd get deploy concourse-web -o json \
     | jq -e --arg want "$WANT_IMAGE" \
       '[.spec.template.spec.containers[].env[]? | select(.name == "CONCOURSE_AGENT_STEP_IMAGE") | .value] == [$want]'
   kubectl -n cicd rollout status deploy/concourse-web --timeout=10m
   ```

5. **Deploy the matching web artifact through `self-upgrade` for the same commit.** Its `repo` gate requires both
   `build-image` and the completed `build-agent-runner-image` GitOps writeback,
   so it cannot select a web image before the runner digest has activated.
   The matching web artifact applies pending migrations on boot. The web and
   pod-side binaries (`agent-runner`, `function-runner`) are two halves of one
   contract: record schema descriptors, gate wording, and contract types are
   compiled into both.
   The `k8s-live-tests` task reads both workloads before testing, requires their
   source and immutable image annotations to agree with the exact repository
   version, and repeats the same checks after the tests pass. Only then does it
   create a two-field `SOURCE_COMMIT`/`TESTED_IMAGE` attestation. An
   unprivileged task commits that attestation to
   `apps/concourse-live-tested-image.env` in `home-infra`, and a supervised,
   unprivileged Git task publishes it with a fresh fetch/rebase and non-force
   push before the job can succeed.
   `release` transports that file through an unprivileged validation task,
   compares its source with the exact repository input, and builds the final
   artifact only from its immutable tested digest; it never re-resolves the
   mutable `rc-<commit>` tag. On an initial release, the current live candidate
   must equal the attested digest, so rebuilding the same commit cannot replace
   a historically tested digest with an untested one. Release-version selection
   is also source-bound: the RC tag on that commit supplies the initial version,
   and a stable tag already on the same commit is reused on a partial-publication
   retry instead of incrementing to the next version. RC tags are immutable
   reservations: overlapping candidates receive distinct patch versions rather
   than moving an already live-tested source's tag.
6. **Verify the running configuration.** Inspect the running web arguments or
   Pod specification and require the configured runner image to equal the
   recorded immutable digest. Require authenticated Fly status to succeed.
7. **Re-import all seven v3 seeds with `--set-live`**:
   ```bash
   fly -t <target> agent workflows import agent/workflow/seeds/<name>-v3 --set-live
   ```
   Seed prompt or plan changes are not picked up by the web restart — they are
   stored workflow versions, created only by an import.
8. **Resume agent dispatch only after every prior check passes.**

Why the order is not advisory:

- A run admitted before step 5 can execute against the **old** pod image.
  Its steps burn the full budget slice and then fail the seal gate, or worse
  succeed against stale contract code. That spend is not recoverable.
- Dispatch **freezes the workflow version** onto the ticket
  (`agent/dispatch/dispatch.go` pins `WorkflowVersion` at dispatch and
  subsequently resolves by that exact version instead of `Live`). A run queued
  before step 6 therefore stays bound to the pre-import version forever — a later
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
- Deliberately dropped in the cutover, tracked as follow-ups: the agentic step
  DAG (the run page's execution card links out to the plain Concourse build
  page instead) and the web ticket create-form (`fly agent tickets create`
  still works). Two items previously listed here are **done**: a genuine
  inline unified-diff viewer for repository changes shipped on both the
  snapshot page and inline in the run page, and the agent sections have their
  own web routes — `/agent`, `/agent/workflows/:name`,
  `/agent/workflows/:name/runs/:id`, `/agent/snapshots/:id`,
  `/agent/experiments`, `/agent/experiments/:id`. Only two sections still sit
  on their pre-split flat paths: `/agent-tickets` (and `/agent-tickets/:id`)
  and `/teams/:team/agent-reviews`. Moving those is a URL change with saved
  links behind it, so it stays a follow-up rather than a cutover step.
- There is no legacy-pipeline cleanup step. The one-time
  `fly agent cleanup-legacy-pipelines` command has been removed: it only
  archived orphaned `agent-ticket-<id>` pipelines left behind by the pre-v3
  per-ticket dispatch lifecycle, and a dropped-and-recreated database never
  creates one (v3 dispatch renders `agent-workflow-*` pipelines instead). A
  deployment that instead upgraded in place from a pre-v3 tree without
  resetting its database can archive those pipelines with
  `fly archive-pipeline`, which is what the command called.
- The transcript viewer is back on the workflow-run page: the run lists which of
  its steps captured a transcript (`GET .../runs/:id/transcripts`) and each one
  opens into the parsed conversation, fetching its ndjson body
  (`GET .../runs/:id/transcripts/:plan_id`) only when opened.

## Rollback

Before using a rollback tag, verify the named remote tag exists and resolves to
a commit; do not rely on an unverified local tag:

```bash
ROLLBACK_TAG=v3-prototype-verified-20260724
git ls-remote --exit-code --refs origin "refs/tags/${ROLLBACK_TAG}"
git fetch --no-tags origin "refs/tags/${ROLLBACK_TAG}:refs/tags/${ROLLBACK_TAG}"
git rev-parse --verify "${ROLLBACK_TAG}^{commit}"
git checkout "${ROLLBACK_TAG}"
```

There is no in-place database downgrade path worth trusting across 36 migrations —
roll back by restoring the previous image and dropping the database again.
