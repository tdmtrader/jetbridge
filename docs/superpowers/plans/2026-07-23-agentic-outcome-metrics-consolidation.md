# Agentic Outcome and Metrics Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make generic workflow outcomes and durable workflow-run metrics the only active disposition and telemetry identities, while retaining tickets solely as links to canonical workflow data.

**Architecture:** Add exact workflow-run identity to metrics, migrate provable legacy ticket outcomes into `agent_workflow_outcomes`, and archive unresolved legacy rows without active readers. Remove the outcome watcher, mirror cache, ticket-specific outcome/diff/metrics APIs, and update Fly and Elm ticket surfaces to follow durable workflow-run, output, and snapshot-projection links.

**Tech Stack:** Go 1.25, PostgreSQL migrations, Ginkgo/Gomega, Elm 0.19, Fly CLI integration tests.

## Global Constraints

- The active workflow model is schema version 3 only.
- Tickets remain an optional `work-item/v1` adapter, never an execution identity.
- Never infer an outcome output by type, insertion order, or timestamp.
- Never overwrite a native `agent_workflow_outcomes` row during legacy migration.
- Preserve unresolved legacy rows as inert audit data; production code must not read them.
- `agent_run_metrics` retains `(build_id, plan_id)` as its ingestion idempotency key.
- Historical migration files remain immutable; new migrations use `1773106124` and `1773106125`.
- Every behavior change follows red-green TDD.
- Execute the numbered tasks in dependency order **1, 4, 3, 2, 5, 6**.
  Ticket compatibility routes must disappear before their backing service is
  deleted, and the legacy table is archived only after no production reader
  remains.

---

### Task 1: Add durable workflow-run identity to metrics

**Files:**
- Create: `atc/db/migration/migrations/1773106124_add_workflow_run_identity_to_agent_metrics.up.sql`
- Create: `atc/db/migration/migrations/1773106124_add_workflow_run_identity_to_agent_metrics.down.sql`
- Create: `atc/db/migration/agent_run_metrics_workflow_identity_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`
- Modify: `agent/schema/metrics.go`
- Modify: `agent/api/metrics/types.go`
- Modify: `agent/api/metrics/memory_store.go`
- Modify: `agent/api/metrics/handler.go`
- Modify: `agent/api/metrics/handler_test.go`
- Modify: `agent/api/metrics/metricsfakes/fake_store.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/api/accessor/roles.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Modify: `atc/auditor/auditor.go`
- Modify: `atc/steps.go`
- Modify: `atc/plan.go`
- Modify: `atc/builds/planner.go`
- Modify: `atc/builds/planner_test.go`
- Modify: `atc/db/agent_run_metrics_factory.go`
- Modify: `atc/db/agent_run_metrics_factory_test.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `atc/exec/agent_step_test.go`
- Modify: `go-concourse/concourse/agent_metrics_test.go`
- Modify: `fly/commands/agent_runs.go`
- Create: `fly/commands/agent_runs_test.go`
- Modify: `web/elm/src/Concourse/Agent.elm`
- Modify: `web/elm/src/Agent/Agent.elm`
- Modify: `web/elm/src/AgentWorkflowRun/AgentWorkflowRun.elm`
- Modify: `web/elm/src/Api/Endpoints.elm`
- Modify: `web/elm/src/Message/Effects.elm`
- Modify: `web/elm/src/Message/Callback.elm`
- Modify: `web/elm/tests/AgentPageTests.elm`
- Modify: `web/elm/tests/AgentWorkflowRunPageTests.elm`

**Interfaces:**
- Consumes: `snapshot.WorkflowRunID`, `agent_workflow_runs.planned_build_id`, and existing `(build_id, plan_id)` metric ingestion.
- Produces: `RunMetrics.WorkflowRunID *snapshot.WorkflowRunID`,
  `RunMetrics.FunctionID string`, and
  `Store.ListByWorkflowRun(string, snapshot.WorkflowRunID)`.
- Produces: viewer-authenticated
  `GET /api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/metrics`,
  which checks both the durable ID and workflow name.
- Removes: `RunMetrics.TicketID`, `RunMetrics.PipelineRunID`, the matching database columns/index, and ticket-derived recent-run labels.

- [ ] **Step 1: Write the migration and store tests first**

Add a migration spec proving exact backfill from the workflow run's planned build, no guessing for unrelated builds, and down-migration removal:

```go
It("backfills exact workflow run identity without changing metric idempotency", func() {
    runID := insertWorkflowRunWithPlannedBuild(9001)
    insertAgentMetric(9001, "plan-agent")
    insertAgentMetric(9002, "unrelated")

    Expect(migrator.Migrate(nil, nil, 1773106124)).To(Succeed())

    Expect(metricWorkflowRun(9001, "plan-agent")).To(Equal(sql.NullInt64{
        Int64: int64(runID), Valid: true,
    }))
    Expect(metricWorkflowRun(9002, "unrelated").Valid).To(BeFalse())
    Expect(metricUniqueKey()).To(Equal([]string{"build_id", "plan_id"}))
    Expect(metricColumns()).NotTo(ContainElements("ticket_id", "pipeline_run_id"))
})
```

Add DB and handler tests using the desired interface:

```go
runID := snapshot.WorkflowRunID(71)
rows, err := store.ListByWorkflowRun("code-review", runID)
Expect(err).NotTo(HaveOccurred())
Expect(rows).To(HaveLen(1))
Expect(rows[0].WorkflowRunID).To(Equal(&runID))
Expect(rows[0].FunctionID).To(Equal("review"))

Expect(deleteBuild(9001)).To(Succeed())
rows, err = store.ListByWorkflowRun("code-review", runID)
Expect(err).NotTo(HaveOccurred())
Expect(rows).To(HaveLen(1))
Expect(rows[0].BuildStatus).To(BeEmpty())
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
ginkgo --focus="workflow run identity" ./atc/db/migration ./atc/db
go test ./agent/api/metrics ./atc/exec
cd web/elm && npx elm-test tests/AgentPageTests.elm tests/AgentWorkflowRunPageTests.elm
```

Expected: compilation or assertion failures because `WorkflowRunID`, `FunctionID`, migration `1773106124`, and `ListByWorkflowRun` do not exist.

- [ ] **Step 3: Implement migration `1773106124`**

The up migration must use exact build identity:

```sql
ALTER TABLE agent_run_metrics
    ADD COLUMN workflow_run_id BIGINT,
    ADD COLUMN function_id TEXT NOT NULL DEFAULT '';

UPDATE agent_run_metrics metric
SET workflow_run_id = run.id
FROM agent_workflow_runs run
WHERE run.planned_build_id = metric.build_id;

ALTER TABLE agent_run_metrics
    ADD CONSTRAINT agent_run_metrics_workflow_run_fkey
    FOREIGN KEY (workflow_run_id)
    REFERENCES agent_workflow_runs (id)
    ON DELETE SET NULL;

CREATE INDEX agent_run_metrics_workflow_run
    ON agent_run_metrics (workflow_run_id, created_at, id)
    WHERE workflow_run_id IS NOT NULL;

DROP INDEX agent_run_metrics_ticket;
ALTER TABLE agent_run_metrics
    DROP COLUMN ticket_id,
    DROP COLUMN pipeline_run_id;
```

The down migration recreates the nullable legacy identity columns and ticket
index, then drops the durable index, constraint, and durable identity columns.
It does not attempt to invent destroyed ticket identity. Advance both Jetbridge
migration-head constants to `1773106124`.

- [ ] **Step 4: Implement the durable metric contract**

Add exact fields and replace ticket listing with workflow-run listing:

```go
type RunMetrics struct {
    WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
    FunctionID    string                  `json:"function_id,omitempty"`
    // existing fields remain
}

type Store interface {
    Upsert(*schema.RunMetrics) error
    UpsertReturningInserted(*schema.RunMetrics) (bool, *schema.RunMetrics, error)
    InsertIfAbsent(*schema.RunMetrics) (bool, error)
    GetByBuild(int) ([]schema.RunMetrics, error)
    ListByWorkflowRun(string, snapshot.WorkflowRunID) ([]schema.RunMetrics, error)
    ListRecent(int) ([]schema.RunMetrics, error)
}
```

Carry `AgentStep.FunctionID` into `AgentPlan.FunctionID` in the planner. Update
both metric insert paths, conflict updates, selected columns, scanners, the
memory store, and fakes. `atc/exec/agent_step.go` must use
`step.metadata.WorkflowRunID` and `step.plan.FunctionID`; never trust
flight-recorder JSON or renderer environment variables. `ParseSubmission`
clears both server-owned values. The cost-ledger append continues to use the
already server-verified local `ticketID` and pipeline `runID` arguments; it must
not require those identities to remain in `RunMetrics`.

Replace ticket labels in `fly agent runs` and the Elm recent-run table with
durable workflow-run/function labels. A metric without workflow identity is
rendered as an unbound CI invocation, not joined back to a ticket.

Register `ListAgentWorkflowRunMetrics` with viewer access and the same
non-mutating wrapper/audit classification as workflow-run outcomes. Add
`Handler.ListByWorkflowRun`, parse the durable ID with
`snapshot.ParseWorkflowRunID`, validate the workflow-name path segment, and
query by both values. The DB query joins `agent_workflow_runs` only for
authorization/identity matching; the metric row remains returnable after its
`builds` row is deleted.

Replace the workflow-run page's global `FetchAgentRunMetrics` effect with
`FetchAgentWorkflowRunMetrics workflowName runID` and a run-qualified callback,
so two open pages cannot accept each other's results. Keep the global recent
metrics effect only for the operator dashboard.

- [ ] **Step 5: Verify GREEN and commit**

Run:

```bash
ginkgo --focus="workflow run identity" ./atc/db/migration ./atc/db
go test ./agent/api/metrics ./atc/exec
cd web/elm && npx elm-test tests/AgentPageTests.elm tests/AgentWorkflowRunPageTests.elm
git diff --check
```

Expected: all focused specs and tests pass.

Commit:

```bash
git add atc/db/migration agent/schema/metrics.go agent/api/metrics atc/routes.go atc/api/handler.go atc/api/accessor/roles.go atc/wrappa atc/auditor atc/steps.go atc/plan.go atc/builds atc/db/agent_run_metrics_factory.go atc/db/agent_run_metrics_factory_test.go atc/exec/agent_step.go atc/exec/agent_step_test.go go-concourse/concourse/agent_metrics_test.go fly/commands/agent_runs.go fly/commands/agent_runs_test.go web/elm/src/Concourse/Agent.elm web/elm/src/Agent/Agent.elm web/elm/src/AgentWorkflowRun/AgentWorkflowRun.elm web/elm/src/Api/Endpoints.elm web/elm/src/Message/Effects.elm web/elm/src/Message/Callback.elm web/elm/tests/AgentPageTests.elm web/elm/tests/AgentWorkflowRunPageTests.elm docs/migration/migrate-preflight.sh
git commit -m "feat(agent): bind metrics to durable workflow runs"
```

---

### Task 2: Migrate exact legacy outcomes and archive the compatibility table

**Files:**
- Create: `atc/db/migration/migrations/1773106125_archive_legacy_agent_outcomes.up.go`
- Create: `atc/db/migration/migrations/1773106125_archive_legacy_agent_outcomes.down.sql`
- Create: `atc/db/migration/archive_legacy_agent_outcomes_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `atc/db/migration/legacyworkflow/decoder.go`
- Modify: `atc/db/migration/legacyworkflow/decoder_test.go`
- Modify: `docs/migration/migrate-preflight.sh`

**Interfaces:**
- Consumes: legacy `agent_outcomes`, exact ticket-to-workflow-run links, pinned schema-v3 definitions, promoted public output bindings, and native `agent_workflow_outcomes`.
- Produces: migrated generic outcomes, `agent_legacy_outcomes_unresolved`, and inert `agent_legacy_outcomes_archive`.

- [ ] **Step 1: Write failing migration cases**

Cover exact migration, native non-overwrite, ambiguous output refusal, missing run refusal, and disposition mapping:

```go
DescribeTable("migrates exact legacy dispositions",
    func(legacy, generic string, modified bool, interventions int) {
        fixture := insertExactLegacyOutcome(legacy)
        Expect(migrator.Migrate(nil, nil, 1773106125)).To(Succeed())

        outcome := readWorkflowOutcome(fixture.RunID, fixture.OutputID)
        Expect(outcome.Disposition).To(Equal(generic))
        Expect(outcome.HumanModified).To(Equal(modified))
        Expect(outcome.InterventionCount).To(Equal(interventions))
        Expect(outcome.PublicationState).To(Equal("not_requested"))
        Expect(outcome.Labels).To(ContainElement("legacy-ticket-migrated"))
    },
    Entry("merged", "merged", "merged", false, 0),
    Entry("merged with fixes", "merged_with_fixes", "merged", true, 1),
    Entry("sent back", "sent_back", "rejected", false, 0),
    Entry("abandoned", "abandoned", "abandoned", false, 0),
    Entry("concluded", "concluded", "accepted", false, 0),
)
```

Add assertions that ambiguous or unlinked rows appear in the unresolved table and that the original row survives in the archive.

- [ ] **Step 2: Run migration tests and verify RED**

Run:

```bash
ginkgo --focus="legacy agent outcomes" ./atc/db/migration
```

Expected: failure because migration `1773106125`, archive, and unresolved report do not exist.

- [ ] **Step 3: Implement the Go migration**

Implement deterministic helpers in the migration file:

```go
type legacyOutcomeMigrationResult struct {
    WorkflowRunID   int64
    OutputSnapshotID int64
    Disposition     string
    HumanModified   bool
    Interventions   int
    Actor            string
}

func mapLegacyDisposition(value string) (string, bool, int, bool) {
    switch value {
    case "merged":
        return "merged", false, 0, true
    case "merged_with_fixes":
        return "merged", true, 1, true
    case "sent_back":
        return "rejected", false, 0, true
    case "abandoned":
        return "abandoned", false, 0, true
    case "concluded":
        return "accepted", false, 0, true
    default:
        return "", false, 0, false
    }
}
```

Extend migration-local `legacyworkflow.Metadata` with
`DispositionOutput string` populated only from a validated schema-v3 function.
For each row, require an exact ticket-linked run or one unique historical
ticket-origin run. Decode the pinned source manifest with that migration-local
package—never import the mutable runtime `agent/workflow` package—to read
`disposition_output`; if it is empty, require exactly one promoted public
output. Insert with `ON CONFLICT DO NOTHING`, never update.

Create:

```sql
CREATE TABLE agent_legacy_outcomes_unresolved (
    ticket_id INTEGER PRIMARY KEY,
    reason TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent_outcomes RENAME TO agent_legacy_outcomes_archive;
```

The down migration renames the archive back and drops the report. Advance both migration heads to `1773106125`.

- [ ] **Step 4: Verify GREEN and commit**

Run:

```bash
ginkgo --focus="legacy agent outcomes" ./atc/db/migration
git diff --check
```

Expected: all migration cases pass, including round-trip down/up coverage.

Commit:

```bash
git add atc/db/migration docs/migration/migrate-preflight.sh
git commit -m "feat(agent): migrate and archive legacy outcomes"
```

---

### Task 3: Delete the legacy outcome service and watcher

**Files:**
- Delete: `agent/api/outcomes/`
- Delete: `agent/outcomewatcher/`
- Delete: `agent/gitcheck/mirror.go`
- Delete: `agent/gitcheck/mirror_test.go`
- Modify: `agent/gitcheck/detect.go`
- Modify: `agent/gitcheck/detect_test.go`
- Delete: `atc/db/agent_outcomes_factory.go`
- Delete: `atc/db/agent_outcomes_factory_test.go`
- Delete: `agent/api/outcomes/outcomesfakes/`
- Delete: `atc/atccmd/agent_workflow_outcomes.go`
- Delete: `atc/atccmd/agent_workflow_outcomes_internal_test.go`
- Modify: `atc/db/agent_workflow_outcomes_factory.go`
- Modify: `atc/db/agent_workflow_outcomes_factory_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/component.go`

**Interfaces:**
- Consumes: canonical `workflowoutcomes.Store` and `workflowoutcomes.Authorizer`.
- Produces: an outcome factory with no legacy resolver interfaces, no mirror cache, and no background compatibility projector.

- [ ] **Step 1: Add failing canonical-factory tests**

Remove compatibility assertions and add a compile-time interface assertion:

```go
var _ workflowoutcomes.Store = db.NewAgentWorkflowOutcomesFactory(conn)
var _ workflowoutcomes.Authorizer = db.NewAgentWorkflowOutcomesFactory(conn)
```

Add a wiring test proving the component list does not contain `agent_outcome_watcher`.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./agent/api/workflowoutcomes ./agent/outcomewatcher ./atc/atccmd
ginkgo --focus="workflow outcomes|outcome watcher" ./atc/db
```

Expected: the new no-watcher assertion fails while legacy packages and component wiring remain.

- [ ] **Step 3: Remove legacy production paths**

Reduce the DB interface to:

```go
type AgentWorkflowOutcomesFactory interface {
    workflowoutcomes.Store
    workflowoutcomes.Authorizer
}
```

Delete legacy output resolution and disposition selection methods. Remove
outcome mirror flags, cache construction, watcher component wiring, and
`ComponentAgentOutcomeWatcher`. The harvest plan has already removed the
outcome-store injection from the engine; do not reintroduce or edit that seam.
Delete the legacy packages and their tests. In `agent/gitcheck`, delete the
web-node mirror/auth/fetch and merge-detection APIs used only by the watcher;
retain `ChangedFile`, `RepositoryDiff`, `DeriveRepositoryDiff`, patch bounding,
and their tests because repository-change snapshot projections still consume
them.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./agent/api/workflowoutcomes ./agent/gitcheck ./atc/atccmd
ginkgo --focus="workflow outcomes" ./atc/db
rg -n 'agent/api/outcomes|agent/outcomewatcher|AgentOutcomeGit|ComponentAgentOutcomeWatcher|OpenMirror|type Mirror struct' agent atc cmd fly web go-concourse
git diff --check
```

Expected: tests pass and the final search returns no production matches.

- [ ] **Step 5: Commit**

```bash
git add -A agent/api/outcomes agent/outcomewatcher agent/gitcheck atc/db atc/atccmd atc/component.go
git commit -m "refactor(agent): remove legacy outcome runtime"
```

---

### Task 4: Remove ticket-specific outcome, diff, disposition, and metrics APIs

**Files:**
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/api/accessor/roles.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Modify: `atc/auditor/auditor.go`
- Modify: `agent/api/metrics/handler.go`
- Modify: `agent/api/metrics/handler_test.go`
- Modify: `agent/api/metrics/types.go`
- Modify: `agent/api/metrics/memory_store.go`
- Modify: `agent/api/metrics/metricsfakes/fake_store.go`
- Modify: `go-concourse/concourse/client.go`
- Modify: `go-concourse/concourse/agent_tickets.go`
- Modify: `go-concourse/concourse/concoursefakes/fake_client.go`
- Modify: `fly/commands/agent_tickets.go`
- Modify: `fly/integration/agent_tickets_test.go`

**Interfaces:**
- Consumes: workflow-run outcomes, workflow-run metrics, snapshot review projection, and repository-change projection APIs.
- Produces: ticket APIs limited to work-item CRUD/queue/dispatch and clients that follow `Ticket.WorkflowRunID`.

- [ ] **Step 1: Write failing route and Fly integration assertions**

Add route-registration assertions that these names are absent:

```go
for _, retired := range []string{
    "SetAgentTicketDisposition",
    "GetAgentTicketOutcome",
    "GetAgentTicketDiff",
    "ListAgentRunMetrics",
} {
    Expect(routeNames).NotTo(ContainElement(retired))
}
```

Update Fly integration expectations so ticket show follows the linked workflow-run endpoint and output projections instead of calling ticket outcome/diff endpoints.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./atc/api/... ./atc/wrappa/... ./go-concourse/concourse/... ./fly/commands
ginkgo --focus="agent ticket.*workflow run|retired ticket routes" ./fly/integration
```

Expected: failures because retired routes and client methods still exist.

- [ ] **Step 3: Remove the compatibility routes and methods**

Delete route constants, route table entries, handlers, roles, wrappers, audit classifications, Go client methods, Fly disposition commands, and ticket metric listing. Keep the generic workflow outcome endpoints:

```text
GET /api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outcomes
PUT /api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/outputs/:snapshot_id/outcome
GET /api/v1/teams/:team_name/agent/snapshots/:snapshot_id/projections/repository-change
GET /api/v1/agent/snapshots/:snapshot_id/projections/review
```

Ticket show must print canonical workflow-run and snapshot URLs derived from returned IDs.

- [ ] **Step 4: Regenerate the Go client fake and verify GREEN**

Run the existing counterfeiter generation command for `go-concourse/concourse.Client`, then:

```bash
go test ./atc/api/... ./atc/wrappa/... ./go-concourse/concourse/... ./fly/commands
ginkgo --focus="agent ticket.*workflow run|retired ticket routes" ./fly/integration
rg -n 'SetAgentTicketDisposition|GetAgentTicketOutcome|GetAgentTicketDiff|ListAgentRunMetrics' atc agent fly go-concourse
git diff --check
```

Expected: tests pass and no retired route or client symbols remain.

- [ ] **Step 5: Commit**

```bash
git add atc/routes.go atc/api atc/wrappa atc/auditor go-concourse/concourse fly/commands/agent_tickets.go fly/integration/agent_tickets_test.go agent/api/metrics
git commit -m "refactor(agent): make ticket APIs workflow-run native"
```

---

### Task 5: Make the ticket UI a canonical workflow-run shell

**Files:**
- Modify: `web/elm/src/Concourse/AgentTicket.elm`
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm`
- Modify: `web/elm/src/AgentTickets/AgentTickets.elm`
- Modify: `web/elm/src/Agent/Agent.elm`
- Modify: `web/elm/src/Api/Endpoints.elm`
- Modify: `web/elm/src/Message/Effects.elm`
- Modify: `web/elm/src/Message/Callback.elm`
- Modify: `web/elm/src/Message/Message.elm`
- Modify: `web/elm/tests/AgentTicketTests.elm`
- Modify: `web/elm/tests/AgentTests.elm`
- Modify: `docs/agentic/README.md`

**Interfaces:**
- Consumes: ticket `workflow_run_id`, workflow run detail/outputs/waits/outcomes, and snapshot projections.
- Produces: no legacy ticket disposition/diff/metrics requests and canonical links for all execution evidence.

- [ ] **Step 1: Add failing Elm view/effect tests**

Create fixtures with a linked workflow run and assert canonical controls:

```elm
test "linked ticket renders canonical workflow evidence without legacy actions" <|
    \_ ->
        linkedTicket
            |> AgentTicket.view session
            |> Query.fromHtml
            |> Expect.all
                [ Query.has [ text "Workflow run" ]
                , Query.hasNot [ text "Set disposition" ]
                , Query.hasNot [ text "Legacy diff" ]
                ]
```

Assert the effects request workflow-run outcomes and snapshot projections, not retired ticket endpoints.

- [ ] **Step 2: Run Elm tests and verify RED**

Run:

```bash
cd web/elm && npx elm-test tests/AgentTicketTests.elm tests/AgentPageTests.elm
```

Expected: failures because legacy disposition/diff/metric state and actions remain.

- [ ] **Step 3: Remove legacy ticket state and render canonical evidence**

Delete legacy outcome, diff, disposition, and ticket-metric model fields/messages/effects/endpoints. Render links from `workflow_run_id` to:

```elm
Routes.AgentWorkflowRun workflowName workflowRunId
```

Use the run's promoted outputs to link review and repository-change projections. Keep ticket content, revision, repository selection, queue/dispatch controls, and workflow-run status.

Update the active README to state that ticket state is a projection shell and all disposition/diff evidence belongs to workflow outputs.

- [ ] **Step 4: Verify GREEN and commit**

Run:

```bash
cd web/elm && npx elm-test tests/AgentTicketTests.elm tests/AgentPageTests.elm
cd ../.. && git diff --check
```

Expected: all Elm tests pass.

Commit:

```bash
git add web/elm docs/agentic/README.md
git commit -m "refactor(web): link tickets to canonical workflow evidence"
```

---

### Task 6: Verify the consolidated outcome and metrics surface

**Files:**
- Modify only if verification exposes a defect in files already owned by Tasks 1-5.

**Interfaces:**
- Consumes: completed Tasks 1-5.
- Produces: repository-wide proof that no active legacy outcome or ticket-metric surface remains.

- [ ] **Step 1: Run structural searches**

```bash
rg -n 'agent/api/outcomes|agent/outcomewatcher|agent_outcomes|SetAgentTicketDisposition|GetAgentTicketOutcome|GetAgentTicketDiff|ListByTicket' agent atc fly go-concourse web/elm/src cmd deploy --glob '!atc/db/migration/**'
rg -n 'agent-ticket-' agent atc fly go-concourse web/elm/src cmd deploy --glob '!fly/commands/agent_cleanup_legacy_pipelines.go' --glob '!fly/commands/agent_cleanup_legacy_pipelines_test.go'
```

Expected: no production matches. Historical migration/archive names and the
explicitly scoped one-time legacy-pipeline cleanup command are excluded.

- [ ] **Step 2: Run focused and broad verification**

```bash
pg_isready
ginkgo ./atc/db/migration ./atc/db
go test ./agent/... ./atc/api/... ./atc/wrappa/... ./go-concourse/concourse/... ./fly/commands
make test-ci-agent
make test-fly-integration
cd web/elm && npx elm-test
cd ../.. && make test-unit
make test-integration
git diff --check
```

Expected: PostgreSQL is ready and every command exits successfully.

- [ ] **Step 3: Commit verification-driven corrections, if any**

If verification required corrections to files already listed in Tasks 1-5:

```bash
git add -A
git commit -m "fix(agent): close outcome consolidation gaps"
```

If no corrections were necessary, do not create an empty commit.
