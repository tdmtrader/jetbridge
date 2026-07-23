# Agentic V3-Only Cleanup Design

**Date:** 2026-07-23

**Status:** Approved for implementation planning

## Executive decision

Jetbridge Agentic becomes a schema-version-3-only product. The active product
contract is:

```text
typed snapshots
  -> schema-v3 workflow DAG
  -> durable workflow run
  -> await_snapshot / publish_snapshot
  -> typed outputs, projections, and outcomes
```

The ticket system remains only as an optional work-item adapter and human-facing
board. It is not an execution identity, an implicit artifact source, or a
privileged side channel.

This is an aggressive runtime cutover, not a soft deprecation. Legacy behavior
is removed rather than hidden behind flags. Historical database and build-plan
records receive only the inert compatibility needed for a safe database upgrade
and audit readability. Historical compatibility must never admit, schedule,
resume, or publish new work.

## Goals

1. Make schema version 3 the only importable, promotable, dispatchable, and
   executable workflow source format.
2. Remove the ticket-linear workflow renderer and every `agent-ticket-<id>`
   execution assumption.
3. Remove `harvest:` as an executable Concourse step while retaining its
   extracted gate, judge, and repository-validation functions.
4. Remove the incomplete platform-MCP question/checkpoint architecture in favor
   of `await_snapshot`.
5. Remove unused per-run principals and static bearer-token bypasses.
6. Make durable workflow runs, snapshots, publications, and generic outcomes
   the canonical identity of agentic execution.
7. Preserve tickets as revisioned `work-item/v1` inputs and projection shells.
8. Preserve safe upgrades from previously released Jetbridge database versions
   without preserving legacy runtime behavior.

## Non-goals

- Deleting ordinary Concourse pipelines, resources, puts, tasks, or privileged
  task support.
- Removing the generic ATC MCP server or generic digest-pinned workflow
  capabilities.
- Removing the `dev-mcp` contract or its repository capability image.
- Deleting reviews, feedback, run metrics, the cost ledger, credentials, or
  scoped principals used by real external publishers.
- Replacing the external publisher gateway.
- Erasing historical attribution strings merely because their former principal
  row is retired.
- Automatically converting arbitrary v1/v2 workflow YAML into schema v3.

## Product boundary after cleanup

### Workflow definitions

Workflow imports must declare `schema_version: 3`. The server and Fly client
reject schema versions 1 and 2 with a stable validation error. Only schema-v3
definitions can be promoted live.

The runtime workflow representation has one arm: the function definition.
Legacy `Config`, linear steps, workflow-global ticket delivery, implicit
workspace, implicit harvest, checkpoint declarations, and signature-version
zero are removed from production packages.

Historical v1/v2 rows remain opaque metadata records. They are never parsed by
the runtime compiler and are forcibly non-live by a forward migration.

### Workflow runs

Every operational, ticket-origin, scheduled, and experimental invocation uses
`workflowrun.Binder`. A durable workflow-run ID is the canonical invocation
identity. Pipeline run, build, and template IDs are subordinate execution
references.

The binder accepts only schema-v3 definitions. `ErrLegacyDefinition` disappears
because legacy definitions cannot reach admission.

### Ticket adapter

Tickets remain mutable work items with revisions, repository selection, human
authorship, and optional specs/plans. Dispatch performs exactly these actions:

1. reserve the current ticket revision;
2. seal that revision as `work-item/v1`;
3. bind the selected exact `repository/v1` snapshot;
4. invoke the selected schema-v3 workflow through the generic binder;
5. record the durable workflow-run ID and pipeline-run ID;
6. project run and outcome state back onto the ticket page.

Ticket content is data, not ambient authority. Agents do not read or mutate
tickets through hidden APIs. A workflow that changes a work item must contain a
visible `publish_snapshot` node targeting the work-item publisher.

Ticket CLI and UI links use `workflow_run_id`; they never derive a pipeline name
such as `agent-ticket-<id>`.

Manual ticket terminal transitions are removed where they would compete with
canonical workflow outcomes. Human rejection or acceptance is recorded through
the exact workflow output/outcome APIs.

## Legacy workflow retirement

The following production concepts are removed:

- schema-v1/v2 `workflow.Config` and its child types;
- v1/v2 runtime parsing, compilation, and validation;
- the legacy ticket renderer and its deprecated alias;
- legacy template saving and ticket-specific run creation;
- workflow budget fallback through legacy `Config.Budget.TicketUSD`;
- checkpoint and question reconciliation seams;
- all schema-v1/v2 seed workflows;
- `--agent-repo-base-url`;
- all runtime branching on `definition.Legacy`.

Ticket dispatch retains only its schema-v3 reservation, work-item capture,
snapshot binding, generic cancellation, and exact port-resolution logic.

## Harvest retirement

`harvest:` is removed as an executable plan step. The following are removed:

- `agent/harvest`;
- `cmd/harvest-runner`;
- harvest image build and copy instructions;
- harvest config validation and planning;
- harvest engine and exec factories;
- workflow compiler/type-checker harvest cases;
- active public-plan and Elm step-tree support;
- implicit branch naming, pushing, judging, gates, and ticket mutation;
- harvest budget traversal.

The reusable implementations remain:

- `agent/functions/gates`;
- `agent/functions/judge`;
- `agent/functions/repositoryvalidate`;
- `agent/publisher`;
- the relevant snapshot contracts.

Historical build-plan JSON may contain harvest nodes. An inert historical
decoder may remain in a clearly named legacy-plan package or type. It exists
only to render completed history. It cannot validate, plan, initialize, resume,
or execute a harvest step. Active builds containing harvest must be rejected by
upgrade preflight before rollout.

Historical container and cost-source enum values remain decodable so totals and
records do not become corrupt.

## Human interaction and MCP cleanup

`await_snapshot` is the only supported human-wait mechanism. It produces exact
`question/v1` and `human-answer/v1` snapshots, records the resolving user
server-side, and remains durable across ATC restarts.

The following are deleted:

- `agent/platformmcp`;
- `cmd/platform-mcp`;
- platform-MCP contract tests;
- the unimplemented ticket-question HTTP paths assumed by that client;
- dormant dispatcher checkpoint reconciliation;
- `agent/notify`;
- `questions:answer`;
- privileged `dev`, `platform`, and `gateway` runtime-role assumptions.

The generic ATC MCP endpoint, generic sidecar mechanism, and named workflow
capabilities remain.

## Credential and authorization cleanup

### Workflow run secrets

Workflow runs stop minting agent principals. The per-run Kubernetes secret
contains only the model credential required by the agent main container.
Sidecars receive neither the model credential nor a platform principal.

The following disappear:

- `principal-token` from run secrets;
- the principal-token parameter on secret attachment;
- schema-v3 `RunPrincipalStore`;
- `questions:answer`;
- ticket read/write scopes granted to generic workflow runs;
- per-run principal revocation once the pre-cutover expiry window has drained;
- `--agent-run-timeout` once it no longer controls any surviving behavior.

Secret deletion and reaping remain because model credentials must still be
removed after terminal runs.

### Review and cost publishers

Review and cost ingestion accept only scoped `cap1` principals. The static
`--agent-review-publish-token` bypass is removed from:

- web configuration;
- API handler construction;
- auth wrapping;
- review and cost handlers;
- integration tests;
- deployment pipeline variables.

CI publishers use explicit scoped credentials. Separate `reviews:write` and
`costs:write` tokens are preferred; one token carrying both scopes is accepted
only where the client cannot yet split them.

The `legacy-publish` sentinel principal is deleted by a forward migration.
Historical `submitted_by = 'legacy-publish'` text is preserved.

## Outcome consolidation

`agent_workflow_outcomes` becomes canonical. The legacy `agent_outcomes`
service, outcome watcher, Git mirror cache, ticket outcome/diff routes, and
generic compatibility projector are removed from active code.

### Migration rules

A legacy outcome is migrated only when its identity can be proven:

1. resolve the ticket's exact `workflow_run_id`, with a unique
   `origin_kind = 'ticket'` lookup permitted only for historical rows;
2. resolve the explicit `disposition_output` port from the pinned definition,
   or accept exactly one promoted public output;
3. never infer an output by type, insertion order, or newest timestamp;
4. never overwrite an existing native workflow outcome;
5. preserve the known disposing actor, or use the named migration actor
   `legacy-ticket-outcome-migration`;
6. attach a `legacy-ticket-migrated` label;
7. record publication as `not_requested`, because the old table cannot prove a
   publication occurrence.

Disposition mapping is:

| Legacy disposition | Generic disposition | Additional evidence |
|---|---|---|
| `merged` | `merged` | none |
| `merged_with_fixes` | `merged` | `human_modified=true`, intervention count 1 |
| `sent_back` | `rejected` | none |
| `abandoned` | `abandoned` | none |
| `concluded` | `accepted` | none |

The migration produces an unresolved-row report. Destructive removal of the
legacy table is refused unless every required row was migrated or deliberately
exported and waived. If unresolved history remains, the table stays inert and
has no production reader.

Ticket pages consume canonical workflow-run outcomes and snapshot review or
repository-change projections directly.

## Metrics identity

`agent_run_metrics` remains because invocation telemetry is distinct from
semantic workflow outputs. It gains a nullable durable `workflow_run_id` and,
where available, stable function/node identity.

`(build_id, plan_id)` remains the ingestion idempotency key, not semantic
ownership. Existing rows are backfilled from exact workflow-run build
relationships. New workflow execution always writes durable workflow identity.

After successful backfill and client migration, ticket-specific metrics queries,
routes, and denormalized ticket identity are removed. Metrics must remain
queryable after build or pipeline retention removes subordinate execution rows.

## Pipeline and lifecycle cleanup

The following ticket-specific cleanup machinery is removed:

- `agent/pipelinearchiver`;
- `ComponentAgentPipelineArchiver`;
- `RunBelongsToTicketTemplate`;
- `RunsForTerminalTickets`;
- `TemplatesForTerminalTickets`;
- lifecycle scans based on `agent-ticket-<id>`;
- dashboard and CLI filters derived from the legacy pipeline name.

Before rollout, a one-time, main-team-scoped operational command archives
remaining `agent-ticket-*` templates and runs. The command must enumerate its
targets before mutation and must not affect schema-v3 workflow templates.

Generic workflow template and run lifecycle management remains.

## `ci-agent`

`ci-agent` remains only where it is an independently deployed review/cost
producer. It is not a workflow orchestrator or product-level phase runner.

Existing pipelines are updated to authenticate with scoped principals. A later
deployment change may replace those jobs with the schema-v3 code-review
workflow; that replacement is not required to complete this code cleanup.

The shared `agent/schema` module and repository `dev-mcp` implementation remain
because the primary runtime uses their contracts independently.

## Database upgrade strategy

Historical migrations are immutable.

Migration `1773106101` currently relies on v1/v2 compilation during upgrade.
Its required legacy decoder and signature backfill logic move into a
migration-local package with no runtime imports. Migration tests continue to
exercise upgrades from old fixtures.

A new forward migration:

- demotes live schema-v1/v2 definitions;
- prevents schema-v1/v2 definitions from becoming live;
- removes the exact inert `legacy-publish` sentinel row;
- removes obsolete `questions:answer` entries from stored principal scopes;
- adds workflow-run identity to metrics;
- performs exact legacy outcome backfill;
- records unresolved legacy outcomes without inventing identity.

Applied historical migrations and their down migrations are not edited.

Upgrade preflight fails before rollout when it finds:

- queued or running tickets bound to schema v1/v2;
- active pipeline runs or builds containing executable harvest plans;
- live schema-v1/v2 definitions that cannot be demoted safely;
- unresolved legacy outcomes when destructive table removal was requested.

## API, CLI, and UI consequences

Removed public behavior:

- schema-v1/v2 import and promotion;
- ticket outcome, ticket diff, and ticket metrics compatibility routes;
- manual disposition mutation that competes with workflow outcomes;
- static-token review/cost publication;
- platform-MCP question/checkpoint operations;
- active harvest plan rendering.

Retained behavior:

- ticket CRUD, revisioning, queueing, repository selection, and v3 dispatch;
- workflow definition/version/run APIs;
- snapshot APIs and projections;
- workflow waits and resolution;
- workflow outcomes and publication evidence;
- review and feedback surfaces;
- experiments and scorecards.

The ticket page becomes a shell over canonical workflow-run data: bound input
snapshots, run state, waits, outputs, review/diff projections, publications, and
outcomes.

## Error handling

- Importing or promoting schema 1/2 returns an explicit unsupported-schema
  response rather than a generic parse error.
- Attempting to dispatch a ticket whose selected workflow is not schema 3
  fails before reservation or secret creation.
- Historical harvest nodes return an explicit retired-step state and never
  construct execution.
- Ambiguous legacy outcomes are reported and left unmigrated.
- Missing scoped publisher credentials fail closed with 401/403.
- Upgrade preflight reports exact blocking definition, ticket, run, build, and
  outcome IDs.

## Testing strategy

Every behavior change follows red-green TDD.

### Focused acceptance tests

- schema 1/2 import and promotion fail with stable errors;
- schema-v3 import, promotion, manual invocation, ticket invocation, retry, and
  experiment invocation continue to pass;
- ticket dispatch captures exact work-item and repository snapshots and records
  a durable workflow-run ID;
- later mutable ticket edits do not alter bound run input;
- no v3 rendered plan contains harvest or an implicit publisher;
- a historical harvest plan decodes as inert and cannot execute;
- generic workflow runs create model-only secrets and no principals;
- static bearer tokens cannot publish reviews or costs;
- scoped principals can publish only within their granted scopes;
- platform-MCP, notification, checkpoint, and question authority are absent;
- exact legacy outcomes migrate and ambiguous rows do not;
- metrics remain linked after subordinate build deletion;
- ticket UI and Fly output link to canonical workflow runs and snapshot
  projections.

### Repository verification

Run, in order:

1. focused Go package tests for each slice;
2. focused DB and migration Ginkgo suites;
3. `make test-ci-agent`;
4. `make test-fly-integration`;
5. Elm tests;
6. `make test-unit`;
7. `make test-integration`;
8. Kubernetes integration covering schema-v3 snapshot workflows;
9. Kubernetes behavioral coverage for waits, output sealing, cancellation, and
   publication.

PostgreSQL must be available for the database-backed suites. Ginkgo versions
must match the repository module version.

## Implementation decomposition

The cleanup is executed as four independently reviewable plans:

1. **Authority and orphan removal**
   - delete platform-MCP and notify;
   - remove question/checkpoint authority;
   - make run secrets model-token-only;
   - remove static-token bypasses and migrate CI publisher configuration.

2. **V3-only workflow and ticket dispatch**
   - make workflow source/runtime representation schema-v3-only;
   - isolate migration-only v1/v2 decoding;
   - remove legacy renderer and seeds;
   - make ticket dispatch and clients use durable workflow-run identity.

3. **Harvest retirement**
   - delete harvest execution and packaging;
   - preserve only inert historical decoding;
   - remove ticket-specific pipeline lifecycle machinery.

4. **Outcome, metrics, and surface consolidation**
   - migrate exact outcomes and remove active legacy outcome paths;
   - add durable metrics identity;
   - remove compatibility routes and projections;
   - update UI, Fly, deployment files, and active documentation.

Each plan must leave the repository compiling and its relevant focused tests
green. Cross-plan interfaces are frozen in this design and may not introduce a
new compatibility flag or dual execution path.

## Completion criteria

The cleanup is complete when:

- runtime code admits and executes only schema-v3 workflow functions;
- no active code constructs, plans, or executes `harvest:`;
- tickets invoke only the generic binder using exact snapshots;
- platform-MCP, legacy checkpointing, and orphan notifications are absent;
- workflow run secrets contain no principal token;
- review and cost publication requires scoped principals;
- generic workflow outcomes and durable run metrics are canonical;
- no production code derives agentic identity from `agent-ticket-<id>`;
- old databases upgrade through migration-local compatibility code;
- historical compatibility cannot schedule or resume legacy work;
- all repository verification tiers required above pass.
