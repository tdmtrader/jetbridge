# Jetbridge Agentic Workflows

Jetbridge Agentic runs versioned workflow functions over immutable, typed
snapshots. A workflow declares an input/output signature and implements it as
an ordinary visible Concourse DAG. Tasks, agents, human waits, and publishers
are explicit nodes; Jetbridge validates and seals every declared output before
another node may consume it.

The product model and the assessment of the earlier ticket-centric work are
documented in [Agentic Workflows as Functions over Snapshots](../superpowers/specs/2026-07-21-agentic-workflows-as-functions-design.md).

## Runtime prerequisites

The web node must enable durable snapshot storage and select the agent runtime
by immutable OCI digest:

```text
--agent-snapshot-enabled
--agent-step-image=registry.example/agent-runner@sha256:<64 lowercase hex characters>
```

Snapshot storage uses the Kubernetes artifact daemon, so its namespace,
host-path, service, port, mTLS certificate/key/CA, and resolve-capability
configuration must also be present. The web command validates this as one
configuration boundary. A schema-v3 workflow or resource capture fails closed
when the runtime image is absent, mutable, or different from the image frozen
at admission.

The bundled Helm chart exposes the storage and experiment lifecycle directly:

```yaml
kubernetes:
  # Required. Resolve a BusyBox-compatible helper to an exact OCI digest; tags fail
  # chart validation.
  artifactHelperImage: registry.example/jetbridge/artifact-helper@sha256:<64-lowercase-hex>
artifactDaemon:
  enabled: true
  tls:
    enabled: true
agentSnapshots:
  enabled: true
  replicationFactor: 2
  scratch:
    # Use a disk-backed emptyDir with this capacity, or set existingClaim.
    sizeLimit: 80Gi
    existingClaim: ""
agentExperiments:
  runnerEnabled: true
  runnerInterval: 10s
  runnerMaxConcurrency: 4
web:
  extraArgs:
    - --agent-step-image=registry.example/agent-runner@sha256:<64 lowercase hex characters>
networkPolicy:
  # Empty is fail-closed: hermetic transformations cannot reach the model.
  # Add complete Kubernetes NetworkPolicy egress rules for only the model
  # endpoint or an operator-controlled egress proxy (and DNS only if needed).
  hermeticEgressTo: []
```

The chart disables ambient service-account token automount on the web pod.
Only `concourse-web` receives a one-hour projected Kubernetes API token, CA,
and namespace at the standard path; no init container receives those
credentials.

Snapshot sealing is deliberately disk-backed. The chart mounts an emptyDir or
existing PVC at `/var/concourse/snapshot-scratch`, then a non-root init creates
the private `0700` child used by `--agent-snapshot-temp-dir` and `TMPDIR`.
At peak, each seal may retain four representations at once: extracted tree,
spool, canonical archive, and upload copy. Allocate at least
`4 * maxBytes * peak concurrent seals`, then add headroom for filesystems,
retries, and workflows sealing multiple outputs. The default `80Gi` corresponds
to roughly two concurrent seals at the default `10Gi` maximum before headroom;
a configured PVC must provide the equivalent capacity.

Content downloads use the same dedicated scratch mount. Jetbridge stages the
bounded archive, verifies its exact byte count and SHA-256 digest, and only
then commits immutable response headers such as `ETag`; corrupt or truncated
storage never receives a cacheable success response.

Schema-v3 task and agent transforms are always hermetic. Jetbridge labels
their pods `concourse.ci/hermetic=true`, disables service-account-token
mounting, and the chart emits their deny-egress policy even when the chart's
general `networkPolicy.enabled` switch is false. Capability sidecars share
that policy and never receive the run principal or model credential. The
standard Kubernetes NetworkPolicy own-node exception remains: it is what lets
the pod fetch snapshots from the node-local artifact daemon, but it also means
operators must not expose unrelated sensitive services on worker-node
addresses.

The chart rejects an experiment runner without snapshots and snapshots without
the mTLS artifact daemon. Deterministic task-only workflow functions do not
need an agent runtime image; any workflow containing `agent:` does.

## Basic workflow loop

Create or capture immutable inputs:

```sh
fly -t TARGET agent snapshots create \
  --type=upgrade-request/v1 \
  --from=./upgrade-request

fly -t TARGET agent snapshots capture-resource \
  --pipeline=repo \
  --resource=source \
  --version=ref:0123456789abcdef0123456789abcdef01234567 \
  --type=repository/v1
```

Import and explicitly promote a workflow definition:

```sh
fly -t TARGET agent workflows import agent/workflow/seeds/version-upgrade-v3
fly -t TARGET agent workflows set-live version-upgrade 1
```

Bind exact snapshot IDs and create a durable run:

```sh
fly -t TARGET agent workflows run version-upgrade \
  --input=repository=101 \
  --input=request=102 \
  --follow
```

The durable run ID is distinct from its disposable pipeline-run ID. Inspect
the pinned definition, concrete plan, lineage, and public outputs with:

```sh
fly -t TARGET agent workflows runs version-upgrade
fly -t TARGET agent workflows show-run version-upgrade RUN-ID
fly -t TARGET agent workflows show-run version-upgrade RUN-ID --outputs
fly -t TARGET agent snapshots show SNAPSHOT-ID
```

Workflow-version history is keyset-paginated and hard-bounded (50 versions by
default, 100 maximum). `fly` follows the response cursor when it needs to
resolve a live or requested version. The browser currently renders the newest
default page; older versions remain available through the API and CLI.

Snapshot, workflow-run, and experiment history use the same exclusive,
opaque keyset-cursor contract over `(created_at, id)`. Responses carry
`X-Next-Cursor` and an RFC 8288 `Link` to the next page. A page boundary is
stable even when several records have the same timestamp; malformed,
repeated, or unknown query parameters fail closed. The corresponding `fly`
list commands follow all pages unless the operator explicitly supplies a
cursor for bounded inspection.

Public outputs appear only after the entire run succeeds. Each producer seals
its values and lineage durably at its own DAG boundary, but those values remain
intermediate candidates while the run is active. Successful finalization
promotes the complete declared result set atomically; failed or partial runs
return an empty public-output list while their intermediate snapshots remain
available through snapshot history.

An internal value also carries a non-expiring retention claim tied to its exact
workflow run. That claim survives long human waits, restarts, and recovery, and
is released in the same transaction that makes the run terminal. The ordinary
binding-retention window then governs physical intermediate content; immutable
manifests and lineage remain as durable history.

Schema-version-3 transforms accept only bound snapshots. They reject
top-level resources and variable sources plus `get` and `load_var`; use the
resource-capture API before dispatch for external reads. Typed nodes must type
every declared input and output. Every task and agent in a v3 function is such
a typed node: it needs a stable, nonblank `function_id`, and there are no
legacy untyped transformation islands. A public-output producer cannot
currently sit inside `attempts:`; retry an internal producer, then export its
successful value from a non-retried typed node.

All snapshot, workflow-run, experiment, and experiment-cell IDs are quoted
decimal strings in JSON. Clients must not decode them as JavaScript numbers.

## V3-only admission and durable ticket identity

Only schema-v3 workflow manifests can be imported, promoted, or executed.
Manifest validation (paths, UTF-8, file sizes, aggregate size, and required
shape) runs before the schema gate and compiler.

Historical schema-1/schema-2 records are inert metadata with no compiler, renderer, or runtime behavior.

They may still be listed or read for audit identity, but have no source
manifest and cannot become live.

Manual, ticket, retry, and experiment invocations all use the generic
workflow-run binder. Ticket dispatch first captures the exact ticket revision
as one immutable `work-item/v1` snapshot and uses the ticket's exact
`repository/v1` snapshot. It binds those two snapshots before recording the
durable `workflow_run_id`. That ID remains the invocation identity after
associated build, pipeline-run, instance-pipeline, or template-pipeline rows
are deleted; `pipeline_run_id` is only an execution diagnostic.

The ticket is a `work-item/v1` projection shell, not an execution identity. Its
state is the QUEUE lifecycle and nothing else — `draft`, `queued`, `running`,
`needs_review`, `closed` — and `closed` is the single terminal state, reached by
a human from `needs_review` after a run, or from `draft`/`queued` to drop a work
item that never ran. Its web page renders only
ticket content (title and markdown body), the captured revision and repository
selection, and the human queue/dispatch/close controls. Every piece of execution
evidence — the agent review, the repository-change diff, run cost, and above all
the run's outcome and DISPOSITION — belongs to the durable workflow run and its
`agent_workflow_outcomes` row, and is reached only through canonical links to
that run (`Routes.AgentWorkflowRun`) and its promoted output snapshots.

The v2 disposition verbs the ticket used to mirror (`merged`,
`merged_with_fixes`, `sent_back`, `concluded`, `abandoned`, `failed`, `errored`)
are gone, along with the per-ticket `budget_usd` and `error_detail` columns, the
`agent_ticket_specs` / `agent_ticket_tasks` content tables (write-orphaned once
the agent submit routes were deleted — a ticket's prose is its body), and the
legacy per-ticket disposition, outcome, diff, and metrics endpoints and the Elm
state that called them.

Operators inspect durable runs with `fly agent workflows show-run`; a
target-qualified example is:

```sh
fly -t TARGET agent workflows show-run WORKFLOW WORKFLOW-RUN-ID
```

`fly agent tickets watch` delegates to the same durable run surface. This
cutover does not add or promise a workflow-run archival or export surface.
The compatibility renderer, root legacy seed manifests, workflow-resolver
ticket-budget fallback, and Dashboard's special agent-pipeline filter have
been removed. The seven schema-v3 definitions listed below are the active
examples.

### Dispatcher runtime toggle

Manual dispatch (`fly agent tickets dispatch`) always works. Automatic
dispatch of the whole ticket queue is a separate three-state mode — `off` /
`paused` / `active` — held in a single hot-read row
(`agent_settings.dispatcher_mode`, no restart required to change it):

```sh
fly -t TARGET agent dispatcher          # show current mode (omit ACTION for status)
fly -t TARGET agent dispatcher resume   # mode=active: auto-dispatch queued tickets
fly -t TARGET agent dispatcher pause    # mode=paused: do not auto-dispatch
fly -t TARGET agent dispatcher off      # mode=off: do not auto-dispatch
```

The same three states are readable and (admin-only) settable from the web UI.
The setting is seeded `off`; there is no separate boot flag to configure —
this hot-read row is the only control surface, and a transient read fault
fails safe to `paused` rather than silently resuming against an explicit
pause.

## Sealed record outputs

Five domain contracts share one canonical output representation:

```text
record.json
content/       optional payloads and large evidence
```

They are `review/v1`, `diagnosis/v1`, `validation/v1`,
`repository-change/v1`, and `measurements/v1`.
`validation/v1` consolidates deterministic check execution and agent-authored
validation; every retry is preserved as an attempt, while conclusion,
flakiness, and total duration are derived.

`record.json` has a strict common envelope:

```json
{
  "record_version": "1.0.0",
  "type": "review/v1",
  "schema": "sha256:<frozen-contract-descriptor-digest>",
  "subjects": [
    {
      "id": "primary",
      "role": "primary",
      "input": "change",
      "type": "repository-change/v1",
      "digest": "sha256:<exact-input-digest>"
    }
  ],
  "body": {}
}
```

Agent and task containers receive the authoritative values as environment
variables:

```text
AGENT_INPUT_<PORT>_SNAPSHOT_TYPE
AGENT_INPUT_<PORT>_SNAPSHOT_DIGEST
AGENT_OUTPUT_<PORT>_RECORD_TYPE
AGENT_OUTPUT_<PORT>_RECORD_SCHEMA
```

Copy these exact values into the candidate record. Jetbridge rejects unknown
or mismatched subjects, type/schema mismatches, unsorted entity sets, invalid
evidence anchors, and type-specific semantic inconsistencies before sealing.
Database snapshot IDs, model identity, timing, producer identity, and
workflow/build provenance never belong in the record body; they are stored on
the production occurrence.

Repository changes put their patch, Git bundle, or tree payload below
`content/` and identify the exact `repository/v1` base as a `base` subject.
Review, diagnosis, and validation use anchored evidence. Measurement records
contain stable metric definitions; evaluator identity/version remains
provenance.

The authoritative shape of each record type is the registry in
`agent/snapshot/contracts/record.go` and the descriptor schemas under
`agent/snapshot/contracts/schemas/`, governed by the
[record schema dialect](../superpowers/specs/2026-07-25-record-schema-dialect.md).

## Optional output presence

Jetbridge creates every declared task and agent output mount before execution,
so an empty output directory cannot represent absence. For a typed output
declared `optional: true`, the runtime adds
`JETBRIDGE_OPTIONAL_OUTPUT_MARKERS`, a JSON object mapping the producer-visible
output name to a collision-free marker path on a separate internal mount.

The producer writes the complete output first, then creates the named marker as
an empty regular file. Jetbridge seals the output only when that exact marker
exists. No marker means absent even when the output directory exists or
contains scratch data. A malformed marker fails the node closed. Required
outputs do not use markers, and marker bytes are never included in snapshot
content.

## Human boundaries and publication

`await_snapshot` creates a durable wait tied to an exact workflow run, build,
plan attempt, question snapshot, and answer snapshot. Canceling a workflow run
also terminates its unresolved waits. `publish_snapshot` is the only workflow
primitive that performs an outbound side effect; transformations and
projections never publish implicitly. Schema-v3 admission rejects `put`,
`harvest`, and `set_pipeline` nodes.

Branch, pull-request, work-item comment, and work-item state publication use a
provider adapter supplied by the deployment. Without that adapter the engine
fails closed. A direct merge additionally requires one resolved human wait
whose question binds the exact workflow run, change snapshot, destination,
publisher mode, complete parameter map, base revision, and approval-policy
version. Jetbridge revalidates that wait and its question/answer snapshots in
the publication transaction; the identity of the user who started the build
is not approval.

For a merge, the workflow must name a guaranteed (non-optional,
non-conditional) `human-answer/v1` artifact produced by `await_snapshot`:

```yaml
- await_snapshot: merge-approval
  merge_approval:
    input: change
    publisher: git-publisher/v1
    destination: git.example/acme/widget
    parameters:
      target_branch: main
    approval_policy_version: engineering/v1
    prompt: Merge this exact change?
  type: human-answer/v1
  on_timeout: fail
  timeout: 72h

- publish_snapshot: merge-change
  publisher: git-publisher/v1
  input: change
  input_type: repository-change/v1
  destination: git.example/acme/widget
  mode: merge
  parameters:
    target_branch: main
  approval_policy_version: engineering/v1
  approval: merge-approval
```

Neither block writes `expected_base_sha`. That assertion names the commit the
change is based on, which is only known once the change exists, so authoring it
is refused: both steps derive it from the sealed `base_sha` of the exact
`repository-change/v1` snapshot they bind, and therefore agree by construction.
It is an assertion, not the stale-base gate — a destination that moved during
the wait is caught at publication by comparing the target's current tip to that
base (`stale_base`).

Workflow authors do not supply trusted execution identity or an approval
question. After `change` is sealed, the schema-v3 renderer and
`await_snapshot` executor bind the concrete workflow definition, run, build,
snapshot ID, and digest, then Jetbridge seals a server-owned `question/v1`.
Its strict `MergeApprovalContext` includes the publisher, `merge` mode,
destination, target branch, expected base SHA, policy version, and an intent
digest over the complete parameter map. The following is illustrative; the
IDs and digest are generated by the server:

```json
{
  "schema_version": "1.0.0",
  "prompt": "Merge this exact change?",
  "context": "{\"schema_version\":\"1.0.0\",\"workflow_run_id\":\"17\",\"input_snapshot_id\":\"41\",\"input_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"publisher\":\"git-publisher/v1\",\"mode\":\"merge\",\"destination\":\"git.example/acme/widget\",\"target_branch\":\"main\",\"expected_base_sha\":\"1111111111111111111111111111111111111111\",\"approval_policy_version\":\"engineering/v1\",\"intent_digest\":\"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"}",
  "options": ["approve", "reject"],
  "default": "reject"
}
```

The context accepts no unknown fields or trailing JSON. The merge publisher
must exactly match the preceding `merge_approval` intent at workflow admission
and execution. Options must include both
`approve` and `reject`; the default may be `reject` or omitted but can never be
`approve`. Only a non-timeout `human-answer/v1` value whose answer is exactly
`approve` and whose `answered_by` matches the resolving human is accepted.

Pending waits appear on the durable workflow-run page. An authorized main-team
member chooses or enters an answer there; the browser sends only that answer,
not a caller-authored snapshot or actor. Jetbridge validates the answer against
the sealed `question/v1`, reserves the first accepted decision, and seals the
`human-answer/v1` itself. The audit record stores a stable hash of the
authenticated connector plus subject (or stable user ID) separately from the
mutable display name. A retry of the same decision reuses the reserved time and
idempotency key, so it cannot mint semantically different approval evidence.
That reservation is also a durable outbox entry: the workflow-run reconciler
deterministically materializes and attaches the same answer after an upload,
database, or web-process failure, including after the wait deadline. A
different actor or answer can never replace the accepted intent.

Cancellation is repaired by the same reconciler. It cancels open waits while a
run is `canceling`, and terminal `aborted` runs remain reconcilable only while
an open wait still exists. This closes the crash window between accepting run
cancellation and terminating its human waits without polling every terminal
run forever.

At execution, ATC reopens and re-hashes the exact question and answer snapshot
bytes, verifies their durable wait, team, run, build, resolution source, actor,
and time, and persists the wait ID, both snapshot references, resolution time,
and actor with the publication. ATC resolves the exact direct-publication
policy from that evidence; authored workflow parameters cannot replace it.

### ATC-owned publisher

Jetbridge executes publication inside the ATC trust boundary. Durable
publication state, exact destination policy, snapshot verification,
idempotency, and provider credentials therefore have one owner. Publication is
disabled by default and requires durable snapshots.

The initial production adapter is direct Git. It publishes either a branch or
an already-rebased change directly to trunk (the internal `merge` mode name is
retained for the existing record contract). Pull requests and work-item
publication require a future explicitly configured provider adapter and fail
closed until one exists.

Configure a human-reviewed policy and narrowly mapped credentials in distinct
Secrets:

```yaml
agentPublisher:
  enabled: true
  policySecret:
    name: publisher-policy
    key: policy.json
  credentialSecret:
    name: publisher-credentials
  credentials:
    - reference: widget-git
      key: widget-git
      path: widget-git
  directGit:
    enabled: true
  requestTimeout: 30s
  leaseDuration: 5m
```

The chart mounts the policy read-only at
`/run/concourse-publisher/policy/policy.json` and mapped credentials beneath
`/run/concourse-publisher/credentials`, only in `concourse-web`. Neither
Secret is mounted into the migration init container, workers, artifact daemon,
agent pods, or managed MCP sidecars. Secret values are never command
arguments. Policy and credential Secret names must differ, every reference and
destination path must be unique, and paths must be clean, non-overlapping
relative paths. The credential mapping must exactly cover the policy references
or ATC refuses startup.

The publisher Secrets are dedicated: the chart rejects aliases through extra
volumes, environment Secret references, image-pull settings, Ingress TLS,
PostgreSQL, artifact-daemon, web TLS, and session-signing configuration. When
the Kubernetes credential manager is also enabled, its namespace prefix must
be nonempty and the release namespace must not begin with that prefix. This
keeps the release-owned publisher Secrets outside every
`<namespacePrefix><team>` pipeline-variable namespace.

`policy.json` has a deliberately small exact-match contract:

```json
{
  "schema_version": 1,
  "rules": [
    {
      "team": "engineering",
      "publisher": "git-publisher/v1",
      "mode": "branch",
      "approval_policy_version": "engineering/v1",
      "target_branch": "agent/widget-42",
      "destination": "git.example/acme/widget",
      "adapter": "direct-git",
      "credential_reference": "widget-git",
      "remote_url": "https://git.example/acme/widget.git"
    },
    {
      "team": "engineering",
      "publisher": "git-publisher/v1",
      "mode": "merge",
      "approval_policy_version": "engineering/v1",
      "target_branch": "main",
      "destination": "git.example/acme/widget",
      "adapter": "direct-git",
      "credential_reference": "widget-git",
      "remote_url": "https://git.example/acme/widget.git"
    }
  ]
}
```

A rule matches exactly one persisted team, publisher type, mode, approval
policy version, destination, and target branch. Authored workflow parameters
cannot supply or replace the remote URL or credential reference. There are no
prefixes, wildcards, or administrator bypasses. ATC independently verifies the
build, team, workflow run, sealed snapshot, and—for trunk publication—the
durable approval evidence before resolving credentials.

Policy is loaded and validated at ATC startup; rotate policy by updating its
Secret and restarting the web deployment. Credential bytes are opened and
revalidated for each authorized operation, so Kubernetes Secret rotation does
not require an ATC restart. Credential roots, ancestors, AtomicWriter links,
and file identities are checked around each read; escaping links,
non-regular/empty/oversized files, and mappings unused by policy fail closed.

The ATC runtime image supplies the distribution Git executable used by the
direct adapter. It materializes the exact sealed `repository-change/v1` in
bounded snapshot scratch, verifies its objects and ancestry, and imports only
verified objects into a new private bare repository. It then checks the expected
remote head and pushes the target and idempotency marker refs atomically. A
retry first resolves that marker, so a web-process failure after remote success
does not require a second semantic push. A changed target head returns
stale/rebase-required and does not update either ref.


## Experiments

Experiments run pinned workflow or function variants against the same snapshot
fixtures and a pinned evaluator workflow. Candidate and evaluator executions
are ordinary durable workflow runs, so they share the same admission,
provenance, output validation, and cancellation behavior as operational work.
Admission permits at most 16 variants, 256 fixtures, 1,000 repetitions, 2,000
materialized cells, and 32 distinct measurements. These are server-enforced
work bounds: matrix expansion, cell-list responses, raw scorecard cells, and
deterministic bootstrap comparisons cannot grow beyond them. The team
experiment index is keyset-paginated in pages of at most 100 records. `fly`
follows those pages; the browser labels its current newest-100 view explicitly.

`fly agent experiments validate` runs the same authoritative target lookup,
render and frozen-config identity checks, static budget proof, and retained
fixture availability checks as `start`, but does not freeze targets, allocate
cells, or change the draft revision. Both validation and start fail with
service unavailable when the deployment has not enabled the experiment
runner; a start can therefore never create durable work that no reconciler
will claim. Candidate and evaluator targets must also be effect-free:
`publish_snapshot` is rejected even when the experiment has no budget.

Jetbridge ships one evaluator, `agent/workflow/seeds/measure-review-v3`. It
takes a `review/v1` candidate and emits `measurements/v1` from a single
deterministic task, `function-runner judge`, which counts what the review
already states: its conclusion as bounded indicators, its findings by severity,
how many of them block, and how much summary it carries. Every number is
anchored by JSON pointer to the candidate field it came from, and the record's
single subject is the exact review measured. The seed contains no agent and no
`publish_snapshot`, so an evaluator cell is effect-free, needs no budget slice
even while the deployment daily cap is enabled, and measures every repetition
identically — an evaluator that varied would put its own noise into every
paired comparison on the scorecard. Scoring work against authored guidance is a
different, stochastic job that needs a model, a budget and a transcript; that
belongs to an `agent:` node in a workflow of its own, not to this function.

Candidate and evaluator binding carries an explicit experiment/cell/phase
gate into ordinary workflow-run allocation. The allocator uses one short
transaction to lock and verify the still-running parent and cell before it
either reuses an idempotent child or inserts the durable child row.
Cancellation takes the conflicting parent lock. Rendering, secret preparation,
and build creation never run while that lock or transaction is held. Exact
cell association is a separate short write and may complete while the parent
is canceling; deterministic origin discovery is authoritative during that
window. Consequently, cancellation either commits before allocation and the
child fails closed, or commits after allocation and must discover and cancel
the durable child. Finalization needs no fixed lease or grace-period timing
assumption and the protocol works with a one-connection database pool.
Platform faults after durable allocation remain retryable against the same
idempotency key. Deterministic budget denial terminalizes the cell as
`skipped_budget`, while invalid admission is a contract failure. Candidate
reservations are decided serially in the durable, repetition-first rotated
cell order before admitted runs execute concurrently, so scheduler timing
cannot decide which variant receives scarce budget.

Non-zero USD budgets are enforced at start and execution. Every candidate and
evaluator agent leaf must declare an exact positive `budget_slice_usd`; the
sum must fit the durable per-cell reservation, which is admitted under a
database lock against the experiment total and deployment daily cap. The
runner passes each slice to Claude as `--max-budget-usd`. Retry/across shapes
that could multiply spend are rejected for budgeted experiments. Positive
token limits currently fail closed at start because the runtime has no exact
token-stop primitive; zero keeps the corresponding limit unlimited.
When the deployment daily cap is enabled, an experiment containing an agent
must declare a positive dollar envelope; a fully deterministic experiment may
remain zero-dollar. Evaluator admission relies on the statically proven
combined slices, allowing a zero-cost evaluator even when the candidate used
the exact cell envelope. An actual overage is still marked `skipped_budget`
before its measurements enter the scorecard.

Experiment and ordinary workflow reservations share one database advisory
lock with cost-ledger ingestion, so concurrent web nodes cannot independently
admit the same remaining deployment budget. An ordinary workflow reserves the
sum of the exact durable executable's agent `budget_slice_usd` values before
any template, secret, build, or agent side effect. With a non-zero deployment
daily cap, every ordinary agent leaf therefore needs a finite positive slice;
`attempts` and `across` are rejected because their multiplicity is not bounded
by this static reservation. Active reservations carry across midnight, and a
terminal run's unused amount remains liable through its completion day so
delayed ledger ingestion cannot briefly reopen the cap. Experiment cells own
their combined candidate/evaluator reservation and do not reserve their child
workflow runs a second time.

```sh
fly -t TARGET agent experiments create definition.json
fly -t TARGET agent experiments validate EXPERIMENT --revision=REVISION
fly -t TARGET agent experiments start EXPERIMENT --revision=REVISION
fly -t TARGET agent experiments cells EXPERIMENT
fly -t TARGET agent experiments scorecard EXPERIMENT
```

`definition.json` is a complete definition: it includes the candidate
signature, at least two pinned variants with one control, snapshot fixtures, a
pinned evaluator and port mappings, repetitions, and budget fields. The
`add-variant` and `add-fixture` commands are conveniences for extending an
already-valid draft. Draft mutations use optimistic revisions. Starting
freezes the complete cell matrix. Scorecards report distributions and paired comparisons, including
coverage, variance, cost, latency, token use, platform failures, human
intervention, and negative-control failures; promotion remains a separate
human decision. A scorecard is available only after the experiment is
terminal, when Jetbridge freezes the exact cell results and then-known selected
and anomalous build telemetry. Later metric ingestion cannot rewrite it.
Budget-skipped cells are separate from platform errors and suppress winner
recommendations because the comparison matrix is incomplete.

## Operator MCP surface

`POST /api/v1/mcp` is a separate, authenticated MCP server for driving
Concourse itself from an MCP client (Claude Desktop, an editor, another
agent) — pipelines, jobs, builds, resources, plus a handful of agent-platform
read tools (`list_agent_workflows`, `get_agent_workflow`, `agent_cost_rollup`,
`list_pipeline_runs`, `get_pipeline_run`). It exposes 25 tools over the MCP
Streamable HTTP transport and requires the same bearer authentication as any
other `/api/v1` route (any authenticated user may call it; nothing here is
admin-gated beyond what the underlying operations already require).

This is unrelated to the named-capability sidecars a workflow declares (`dev`,
`gateway`, custom MCP capabilities): those run beside an agent's pod and speak
to external or repo-local tools, not to Concourse's own API. Hermetic agent
and task pods have no route to `/api/v1/mcp` — the deny-egress NetworkPolicy
they run under only exempts the node-local artifact daemon, and this endpoint
carries no run principal or model credential for them to use even if network
policy allowed the connection. It is an operator/human-tooling surface, not a
capability a workflow can declare or reach.

## Included workflows

Schema-v3 examples live under `agent/workflow/seeds/`:

- `code-review-v3`: repository comparison to `review/v1`;
- `small-fix-v3`: bounded work item to reviewed repository change and report;
- `version-upgrade-v3`: upgrade request to validated change and report;
- `anonymization-audit-v3`: captured database evidence to findings and an
  optional repository change;
- `log-diagnosis-v3`: captured logs and deployment state to a diagnosis;
- `merge-delivery-v3`: an approved change rebased onto the current target tip,
  producing the landed `repository-change/v1` and a `validation/v1` merge
  report that is sealed whether or not the merge is clean;
- `measure-review-v3`: one review to deterministic `measurements/v1` — the
  shipped experiment evaluator, described under Experiments above.

These definitions contain placeholder destinations and prompts suitable for
dogfooding, not deployment credentials. Live-system data must be captured as a
snapshot before execution; workflow nodes do not receive ambient access to
live systems.

Named capabilities are local MCP sidecars. Each declaration uses an exact
image digest and exactly one TCP `containerPort`; the compiler derives
`<CAPABILITY>_MCP_URL`, rejects authored endpoint injection and port
collisions, and starts custom capabilities in the mounted workspace. Claude is
always invoked with a generated `--mcp-config` and
`--strict-mcp-config`, including when the admitted set is empty. Sidecar names
`dev`, `platform`, and `gateway` are reserved legacy roles and cannot confer
credentials; use descriptive custom names such as `dev-mcp`.
