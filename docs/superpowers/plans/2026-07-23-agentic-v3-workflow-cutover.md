# Agentic V3-Only Workflow Cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make schema-version 3 the only runtime workflow format, make tickets a binder-only `work-item/v1` adapter, and expose durable workflow-run identity in the ticket CLI and UI.

**Architecture:** Move the v1/v2 parser/compiler needed by migration `1773106101` into a migration-local package, then remove legacy definition arms from the runtime `workflow` package. A forward migration demotes old live rows and constrains future live rows to v3; runtime stores leave old rows opaque and never compile them. Ticket dispatch resolves only a v3 definition, captures exact work-item/repository snapshots, delegates to `workflowrun.Binder`, and clients follow `workflow_run_id` instead of a ticket-named pipeline.

**Tech Stack:** Go, PostgreSQL migrations, Ginkgo/Gomega, Go HTTP/Fly clients, Elm.

## Global Constraints

- Schema version 3 is the sole importable, promotable, dispatchable, and executable workflow format; do not add a compatibility flag or dual runtime path.
- Historical migrations are immutable except that migration `1773106101` may import a migration-local legacy decoder; do not renumber it.
- Reserve migration `1773106123` for this plan's forward enforcement migration. Do not use any other migration number.
- Historical v1/v2 rows are opaque audit metadata only: no runtime parser, compiler, renderer, binder, scheduler, or publisher may consume them.
- `1773106123` must demote existing live v1/v2 definitions before adding the live-v3 database constraint; its down migration restores only the prior ability to mark rows live, never re-promotes a row.
- Ticket dispatch must reject a non-v3 workflow before `ReserveDispatch`, snapshot capture, template saving, secret creation, or pipeline-run creation.
- The ticket adapter binds exactly one `work-item/v1` and one `repository/v1` input through `workflowrun.Binder`; ticket content is never an ambient input.
- `workflow_run_id` is the user-facing invocation identity. `pipeline_run_id` remains an execution reference; no active Fly or Elm path may derive `agent-ticket-<id>`.
- Preserve v3 workflows, generic manual runs, retries, and experiments. This plan does not remove generic ATC MCP, snapshot APIs, `await_snapshot`, publishers, or ordinary Concourse pipelines.

### Normative execution amendment

Dependency reconnaissance proved that the original numeric order would create
two non-compiling intermediate commits: Task 2 deleted legacy types before
dispatch and database callers were removed, and Task 4 deleted the renderer
before Task 5 deleted its caller. Preserve the final interfaces below, but
execute the work in this buildable order:

1. Task 1.
2. Task 2A: close import/promotion admission to schema 3, but temporarily
   retain the legacy model/parser for not-yet-removed consumers.
3. Task 5: make ticket dispatch binder-only and remove every legacy dispatch
   dependency.
4. Task 3: make historical rows opaque and add migration `1773106123`.
5. Task 4: delete the now-orphaned renderer/seeds and budget fallback.
6. Task 2B: delete the now-unreferenced legacy model/parser/compiler.
7. Tasks 6, 7, and 8.

Task 2A must inspect the schema header before legacy compilation or asset
resolution so valid schema 1/2 input always receives the stable typed 422.
Use one canonical workflow schema-header admission helper that becomes the
first step of the final v3 compiler; do not add a temporary public legacy
compiler. Test legacy MemoryStore promotion with a package-internal test
fixture, never a production seeding hook.

Task 5 also owns all compile-time consumers of its removed interfaces:
`agent/dispatch/handler.go`, `agent/workflowrun/experiment_binder.go`,
`agent/api/workflowruns/handler.go` and tests, the binder's durable error
mapping, `atc/db/agent_dispatch_test.go`, and `atc/atccmd/command.go`. Delete
the legacy-only run-secret labeler and tests, remove `AgentRepoBaseURL` /
`--agent-repo-base-url`, and replace the schema-2 DB dispatch fixture with a
binder-backed schema-3 fixture. Task 4 owns every `NewTicketBudgets` caller
when it removes the workflow-resolver argument. The no-`workflow.Config`
completion assertion belongs to Task 2B, not Task 4.

Task 7 additionally removes active `agent-ticket-<id>` pipeline-name
derivation from Build and Dashboard Elm code/tests. CSS class names are not
pipeline identity. Task 8 must use exact-cased Ginkgo focuses, module-correct
commands, and the explicit Fly/Elm derivation scan specified in that task.

---

## File map

| File | Responsibility after this plan |
|---|---|
| `atc/db/migration/legacyworkflow/decoder.go` | Private released-format decoder used only by Go migrations; it is outside the embedded migration asset directory so the migration filename parser never sees it. |
| `atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go` | Historical metadata backfill using `legacyworkflow`, never `agent/workflow`. |
| `agent/workflow/{definition.go,parse.go,compile.go,memory_store.go}` | Schema-v3-only source, compiled model, and test store. |
| `atc/db/agent_workflows_factory.go` | Stores v3 source and serves historical v1/v2 rows without compiling them. |
| `atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.*` | Demotes legacy live rows and constrains live definitions to v3. |
| `agent/dispatch/{dispatch.go,budgets.go}` | Binder-only ticket adapter with no legacy renderer/config or workflow-budget fallback. |
| `agent/dispatch/render.go` and legacy seed YAML | Deleted retired ticket-linear execution surface. |
| `fly/commands/agent_tickets.go` | Lists, prints, and watches a ticket through its durable workflow run. |
| `web/elm/src/AgentTickets/AgentTicket.elm` | Ticket shell links durable run and snapshots, not ticket pipeline/build rows. |

### Task 1: Freeze v1/v2 decoding inside migration `1773106101`

**Files:**
- Create: `atc/db/migration/legacyworkflow/decoder.go`
- Create: `atc/db/migration/legacyworkflow/decoder_test.go`
- Modify: `atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go`
- Modify: `atc/db/migration/add_workflow_schema_signature_test.go`

**Interfaces:**
- Produces: `legacyworkflow.DecodeManifest(files map[string]string) (Metadata, *PublicSignature, error)` where `Metadata` is `{Name string; SchemaVersion int; SignatureVersion int}` and the signature is non-nil only for released schema 3.
- Consumes: stored `definition` plus optional JSON `source_manifest` from migration `1773106101`.
- Does not export or import any runtime `agent/workflow` type.

- [ ] **Step 1: Write the migration-local decoder tests**

```go
It("decodes released schema 1 and manifest-backed schema 2 records", func() {
    v1, signature, err := legacyworkflow.DecodeManifest(map[string]string{"workflow.yml": v1Source})
    Expect(err).NotTo(HaveOccurred())
    Expect(signature).To(BeNil())
    Expect(v1).To(Equal(legacyworkflow.Metadata{Name: "migrated-v1", SchemaVersion: 1}))

    v2, signature, err := legacyworkflow.DecodeManifest(map[string]string{
        "workflow.yml": v2Source, "prompts/work.md": "work from manifest",
    })
    Expect(err).NotTo(HaveOccurred())
    Expect(signature).To(BeNil())
    Expect(v2).To(Equal(legacyworkflow.Metadata{Name: "migrated-v2", SchemaVersion: 2}))
})
```

Also pin the released schema-3 grammar and signature identity explicitly:

- optionality and port order change `PublicSignature.Equal`;
- descriptions and output `from` mappings do not;
- the released no-port capability form is accepted;
- post-release `disposition_output` is rejected;
- post-release long-form plan `output_types: {type, optional}` is rejected.

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `go test ./atc/db/migration/legacyworkflow && ginkgo --focus='workflow schema signature migration' ./atc/db/migration`

Expected: FAIL because `legacyworkflow.DecodeManifest` does not exist.

- [ ] **Step 3: Implement the inert decoder and redirect the historical migration**

Freeze the released schema 1/2 decoder and the schema-3
metadata/public-signature decoder used by this migration inside
`legacyworkflow`. Copy only the validation and manifest-asset logic needed to
reproduce migration `1773106101`; do not import a runtime `agent/*` package.
Its complete public boundary is:

```go
package legacyworkflow

type Metadata struct {
    Name             string
    SchemaVersion    int
    SignatureVersion int
}

type PublicSignature struct {
    SignatureVersion int
    Inputs           []Port
    Outputs          []Port
}

type Port struct {
    Name     string
    Type     string
    Optional bool
}

func DecodeManifest(files map[string]string) (Metadata, *PublicSignature, error) {
    // Require workflow.yml and resolve declared assets. Schema 1/2 return
    // signature_version 0 and nil signature. Schema 3 validates the released
    // function signature and returns its positive version and exact ports.
}
```

Change `Up_1773106101` to call only
`legacyworkflow.DecodeManifest(source)` for every row. Keep the existing
row-qualified error wrapper, stored-name check, and positive-signature
compatibility check, now using the migration-local `PublicSignature.Equal`.
Delete the migration's `agent/workflow` import. Task 2 may then change runtime
parsing without changing the upgrade ABI.

- [ ] **Step 4: Add upgrade regression coverage**

Replace the current v1/v2 expectations in `add_workflow_schema_signature_test.go` with assertions that migration `1773106101` backfills `(1,0)` and `(2,0)` using the local package. Keep malformed YAML, missing manifest asset, missing `workflow.yml`, stored-name mismatch, and incompatible v3 signature cases; add a guard that `rg` finds no `agent/workflow` import in `legacyworkflow`.

- [ ] **Step 5: Run focused migration tests**

Run: `go test ./atc/db/migration/legacyworkflow && ginkgo --focus='workflow schema signature migration' ./atc/db/migration`

Expected: PASS; a DB upgraded through `1773106101` still records legacy schema metadata without importing runtime legacy parsing.

- [ ] **Step 6: Commit the migration ABI isolation**

```bash
git add atc/db/migration/legacyworkflow atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go atc/db/migration/add_workflow_schema_signature_test.go
git commit -m "refactor(migration): isolate legacy workflow decoding"
```

### Task 2A: Close workflow import and promotion admission to schema 3

**Files:**
- Modify: `agent/workflow/definition.go`
- Modify: `agent/workflow/parse.go`
- Modify: `agent/workflow/memory_store.go`
- Modify: `agent/workflow/memory_store_test.go`
- Create: `agent/workflow/memory_store_admission_internal_test.go`
- Modify: `agent/api/workflows/handler.go`
- Modify: `agent/api/workflows/handler_test.go`
- Modify: `atc/db/agent_workflows_factory.go`
- Modify: `atc/db/agent_workflows_factory_test.go`

**Interfaces:**
- Produces: `workflow.UnsupportedSchemaVersionError{Got int}` with stable text
  `workflow: unsupported schema_version <n>; only schema_version 3 is supported`.
- Produces: `workflow.RequireSchemaVersion3(source []byte) error`, the
  schema-header-first boundary used before any legacy asset compilation.
- Temporarily retains: `Config`, `Parse`, `Compile`, the compiled `Legacy`
  arm, `Definition.Config`, and legacy seeds until Tasks 5, 4, and 2B.

- [ ] **Step 1: Write failing admission tests**

Assert raw and manifest imports of valid schema 1/2 return the exact typed
error before missing legacy assets or other legacy validation can win. Assert
malformed schema-3 YAML remains a normal invalid-definition error. Cover both
MemoryStore and PostgreSQL imports.

In a package-internal MemoryStore test, seed one live v3 row and one legacy
row directly into the private store. Assert legacy promotion returns
`InvalidPromotionError` wrapping `UnsupportedSchemaVersionError` before the
validator and leaves the v3 row live. Add the equivalent PostgreSQL promotion
coverage using a direct historical-row insert.

At the HTTP boundary, assert raw and manifest legacy imports and legacy
promotion return 422 with the stable message; malformed v3 remains 400.

- [ ] **Step 2: Run the focused tests and verify red**

```bash
go test ./agent/workflow ./agent/api/workflows -run 'Test.*(NonV3|Unsupported|Import|Promote)' -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory' ./atc/db
```

Expected: legacy import/promotion still succeeds or produces a non-stable
legacy compiler error.

- [ ] **Step 3: Implement schema-header-first rejection**

Decode only `schema_version` first. Return the typed error for any valid
non-3 header before `CompileDefinition`, legacy asset resolution, name
validation, persistence, validator invocation, or live-state mutation.
Memory and PostgreSQL imports use this boundary. Both promotions inspect
persisted `SchemaVersion` and reject non-3 immediately. Map the typed error
before the generic invalid-definition 400 branch in the handler.

Do not delete or narrow the legacy runtime model in this task, and do not add
a temporary legacy compiler.

- [ ] **Step 4: Run complete admission suites**

```bash
go test ./agent/workflow ./agent/api/workflows -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory' ./atc/db
git diff --check
```

- [ ] **Step 5: Commit the admission cutover**

```bash
git add agent/workflow/definition.go agent/workflow/parse.go agent/workflow/memory_store.go agent/workflow/memory_store_test.go agent/workflow/memory_store_admission_internal_test.go agent/api/workflows atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go
git commit -m "feat(workflow): reject non-v3 admission"
```

### Task 3: Keep historical workflow rows opaque and enforce v3 live rows in PostgreSQL

**Files:**
- Create: `atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.up.sql`
- Create: `atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql`
- Create: `atc/db/migration/v3_only_workflows_test.go`
- Modify: `atc/db/agent_workflows_factory.go`
- Modify: `atc/db/agent_workflows_factory_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`
- Modify: `docs/migration/migrate-preflight_test.sh`

**Interfaces:**
- Produces: `agent_workflow_definitions_live_schema_v3_check CHECK (NOT live OR schema_version = 3)`.
- Produces: `Get`/`Latest` responses for historical rows containing persisted
  metadata and exact `RawYAML`, a nil `SourceManifest`, and a zero
  `CompiledDefinition`; `List`/`Versions` remain metadata-only.
- Consumes: `1773106101` metadata columns and the immutable historical decoder from Task 1.

- [ ] **Step 1: Write failing DB and migration specs**

```go
It("demotes legacy live definitions and forbids future legacy live rows", func() {
    _, err := database.Exec(`UPDATE agent_workflow_definitions SET live = true WHERE name = 'legacy-v2'`)
    Expect(err).NotTo(HaveOccurred())
    Expect(migrator.Migrate(nil, nil, 1773106123)).To(Succeed())

    var live bool
    Expect(database.QueryRow(`SELECT live FROM agent_workflow_definitions WHERE name = 'legacy-v2'`).Scan(&live)).To(Succeed())
    Expect(live).To(BeFalse())
    _, err = database.Exec(`UPDATE agent_workflow_definitions SET live = true WHERE name = 'legacy-v2'`)
    Expect(err).To(HaveOccurred())
})
```

Add factory tests proving `Get("legacy", 1)` returns the historical metadata/raw YAML without a parse error, `Live("legacy")` returns no row after migration, and `Promote("legacy", 1, "alice")` returns `InvalidPromotionError` wrapping `UnsupportedSchemaVersionError` before any source compilation.

- [ ] **Step 2: Run the focused specs to verify they fail**

```bash
go run github.com/onsi/ginkgo/v2/ginkgo --focus='v3-only workflow|Legacy Database Upgrade' ./atc/db/migration
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory' ./atc/db
```

Expected: FAIL because the forward migration and opaque-row behavior do not exist.

- [ ] **Step 3: Add migration `1773106123`**

Use this ordered SQL body:

```sql
UPDATE agent_workflow_definitions SET live = false WHERE live AND schema_version <> 3;
ALTER TABLE agent_workflow_definitions
    ADD CONSTRAINT agent_workflow_definitions_live_schema_v3_check
    CHECK (NOT live OR schema_version = 3);
```

The down migration is exactly:

```sql
ALTER TABLE agent_workflow_definitions
    DROP CONSTRAINT agent_workflow_definitions_live_schema_v3_check;
```

It must not set `live = true`. After the authority plan advances the branch
head to `1773106122`, advance both `jetbridgeHeadMigration` and
`JETBRIDGE_VERSION` to `1773106123`; extend the legacy-to-head assertions to
verify the constraint and demoted rows. In
`docs/migration/migrate-preflight_test.sh`, change the simulated newer
migration from `1773106123` to `1773106124` while keeping rolled-back HEAD at
`1773106123 down / 1773106122`.

- [ ] **Step 4: Stop DB reads from compiling legacy source**

In `agent_workflows_factory.go`, branch on persisted `def.SchemaVersion`
before `compileStoredWorkflowSource`: schema 3 follows the existing
compile/metadata consistency path; schema 1/2 returns exact raw YAML and
persisted metadata as opaque history without decoding YAML or
`source_manifest`. Use malformed legacy YAML and valid wrong-shape JSONB
(`[]::jsonb`) in the test so success proves neither decoder ran. `Promote`
must reject non-3 immediately after scanning metadata, before
`compileStoredWorkflowSource`; `ImportManifest` can only receive v3 after
Task 2A.

- [ ] **Step 5: Run focused migration and DB tests**

```bash
go run github.com/onsi/ginkgo/v2/ginkgo --focus='workflow schema signature migration|v3-only workflow|Legacy Database Upgrade' ./atc/db/migration
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory' ./atc/db
bash docs/migration/migrate-preflight_test.sh
git diff --check
```

Expected: PASS; old databases upgrade, no legacy row remains live, and runtime DB reads never parse v1/v2 source.

- [ ] **Step 6: Commit database enforcement**

```bash
git add atc/db/migration atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go docs/migration/migrate-preflight.sh docs/migration/migrate-preflight_test.sh
git commit -m "feat(db): enforce schema v3 workflow liveness"
```

### Task 4: Delete the legacy ticket renderer, legacy seeds, and workflow-budget fallback

Execute after Task 5, when `RenderLegacyTicket` has no production caller.

**Files:**
- Delete: `agent/dispatch/render.go`
- Delete: `agent/dispatch/render_test.go`
- Delete: `agent/workflow/seeds/develop-fable.yaml`
- Delete: `agent/workflow/seeds/develop.yaml`
- Delete: `agent/workflow/seeds/direct-dev.yaml`
- Delete: `agent/workflow/seeds/standard-dev.yaml`
- Delete: `agent/workflow/seeds/test-first-dev.yaml`
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/dispatch/budgets.go`
- Modify: `agent/dispatch/budgets_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/db/agent_dispatch_test.go`

**Interfaces:**
- Produces: seed validation over only `*-v3/workflow.yml` manifests.
- Produces: `TicketBudgets.BudgetUSD` that returns an explicit ticket budget or `(0,false,nil)`; it never reads a workflow config default.

- [ ] **Step 1: Write failing removal/regression tests**

```go
func TestTicketBudgetDoesNotReadLegacyWorkflowDefaults(t *testing.T) {
    amount, found, err := NewTicketBudgets(ticketGetter{ticket: ticketWithoutBudget}).BudgetUSD(42)
    if err != nil || found || amount != 0 { t.Fatalf("amount=%v found=%v err=%v", amount, found, err) }
}
```

Update `seed_test.go` to enumerate only the five v3 directories and assert
every manifest compiles/renders as v3. Checkpoint reconciliation is already
removed by the authority plan and is not reimplemented or retested here.

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./agent/dispatch ./agent/workflow -run 'Test.*(Budget|Seed)' -count=1`

Expected: FAIL because ticket budgets inspect `def.Config` and legacy seeds are
still discovered.

- [ ] **Step 3: Remove the retired code paths**

Delete `RenderInput`, `RenderAgentStep`, `RenderLegacyTicket`, all
`agent/harvest` renderer imports, ticket-file materialization, and all legacy
YAML seeds/tests. Delete the workflow-default fallback from
`TicketBudgets.BudgetUSD` so only `tickets.budget_usd` is authoritative. Do not
delete generic v3 `await_snapshot` handling.

- [ ] **Step 4: Run focused package tests**

```bash
go test ./agent/dispatch ./agent/workflow ./atc/atccmd -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='dispatching a ticket end-to-end|the dispatcher loop over real stores' ./atc/db
```

Expected: PASS; no production package contains `RenderLegacyTicket`, a legacy
seed name, or the workflow-budget fallback. The `workflow.Config` absence
assertion runs in Task 2B.

- [ ] **Step 5: Commit renderer and seed removal**

```bash
git add agent/dispatch agent/workflow/seeds agent/workflow/seed_test.go atc/atccmd/command.go atc/db/agent_dispatch_test.go
git commit -m "refactor(agent): remove legacy ticket workflow rendering"
```

### Task 2B: Delete the legacy runtime workflow model

Execute only after Tasks 5, 3, and 4.

**Files:**
- Modify: `agent/workflow/definition.go`
- Modify: `agent/workflow/parse.go`
- Modify: `agent/workflow/compile.go`
- Modify: `agent/workflow/parse_v3_test.go`
- Modify: `agent/workflow/compile_test.go`
- Modify: `agent/workflow/typecheck_test.go`
- Modify or delete: `agent/workflow/validate_test.go`
- Modify: `agent/workflow/memory_store.go`
- Modify: `agent/workflow/memory_store_test.go`
- Modify: `agent/workflow/seed_test.go`
- Delete: `agent/workflow/config.go`
- Delete: `agent/workflow/parse_test.go`
- Delete: `agent/workflow/parse_v2_test.go`
- Modify: `atc/db/agent_workflows_factory.go`
- Modify: `atc/db/agent_workflows_factory_test.go`
- Modify: `fly/commands/agent_workflows.go`
- Create: `fly/commands/agent_workflows_test.go`
- Modify: `fly/integration/agent_workflows_test.go`
- Modify any additional compile-time reference returned by the required exact
  scan; do not retain a compatibility alias.

**Interfaces:**
- Produces:
  `CompiledDefinition{SchemaVersion: 3, Name, Description, Function}` with no
  `Legacy` arm.
- Produces: `Definition` with no `Config` compatibility field.
- Consumes: only schema-3 `workflow.yml` manifests.
- Removes: `Config`, legacy `Step`, `Parse`, `Compile`, `compileLegacy`, and
  every legacy source-format branch/test fixture.

- [ ] **Step 1: Write/convert the v3-only model tests**

Convert accepted fixtures to schema 3. Assert `RequireSchemaVersion3` is the
first `CompileDefinition` boundary and returns the stable typed error for
schema 1/2. Remove tests whose only purpose is legacy runtime parsing, while
retaining v3 asset limits, type checking, rendering, signature, and source
validation coverage.

Update Fly file and directory import tests to use only the v3 compiler and
prove local v1/v2 input cannot be accepted. Historical migration fixtures stay
legacy and remain owned by Task 1's decoder.

- [ ] **Step 2: Run focused tests and verify red**

```bash
go test ./agent/workflow ./agent/api/workflows ./fly/commands -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory' ./atc/db
```

Expected: legacy model arms and entry points remain referenced.

- [ ] **Step 3: Collapse the model and compiler**

Make `ParseCompiled` and `CompileDefinition` schema-v3-only. Delete the legacy
types, compiler, parser branches, compatibility field population, and
legacy-only tests. Preserve schema-v3 prompt/schema/skill resolution,
function extraction, type checking, hermetic execution, and public signature
behavior. PostgreSQL historical reads remain opaque per Task 3 and must not
gain a decoder.

- [ ] **Step 4: Run complete packages and exact absence scans**

```bash
go test ./agent/workflow ./agent/api/workflows ./agent/dispatch ./agent/workflowrun ./fly/commands -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory' ./atc/db
make test-fly-integration
! rg -n 'workflow\.Config|Legacy\s+\*Config|compiled\.Legacy|definition\.Legacy|Compiled\.Legacy|Legacy:' agent/workflow atc/db/agent_workflows_factory.go fly/commands/agent_workflows.go
! rg -n 'func Parse\(|func Compile\(|compileLegacy' agent/workflow
git diff --check
```

Expected: PASS and no runtime legacy model/parser/compiler match.

- [ ] **Step 5: Commit structural deletion**

```bash
git add agent/workflow agent/api/workflows atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go fly/commands/agent_workflows.go fly/commands/agent_workflows_test.go fly/integration/agent_workflows_test.go
git commit -m "refactor(workflow): remove legacy runtime model"
```

### Task 5: Make ticket dispatch a schema-v3 binder adapter only

**Files:**
- Modify: `agent/dispatch/dispatch.go`
- Modify: `agent/dispatch/dispatch_test.go`
- Modify: `agent/dispatch/handler_test.go`
- Modify: `agent/workflowrun/types.go`
- Modify: `agent/workflowrun/binder.go`
- Modify: `agent/workflowrun/binder_test.go`
- Modify: `agent/workflowrun/experiment_binder.go`
- Modify: `agent/api/workflowruns/handler.go`
- Modify: `agent/api/workflowruns/handler_test.go`
- Modify: `agent/dispatch/handler.go`
- Modify: `agent/dispatch/handler_test.go`
- Delete: `agent/dispatch/labels.go`
- Delete: `agent/dispatch/labels_test.go`
- Modify: `atc/db/agent_dispatch_test.go`
- Modify: `atc/atccmd/command.go`

**Interfaces:**
- Produces: `dispatch.ErrWorkflowNotV3`, returned before reservation for selected schema 1/2 metadata.
- Produces: `Result{RunID: pipelineRunID, WorkflowRunID: &durableID, PipelineName: templateName}` only from `workflowrun.BindAndCreate`.
- Removes: `workflowrun.ErrLegacyDefinition`, `Deps.Templates`, `Deps.Runs`, `RepoBaseURL`, and every ticket-specific `agent-ticket-<id>` create path.
- Removes: `RunSecretLabeler`, `SecretLabels`, `AgentRepoBaseURL`, and the
  public `--agent-repo-base-url` flag.

- [ ] **Step 1: Write failing binder-only dispatch tests**

```go
func TestDispatchOneRejectsLegacyDefinitionBeforeReservation(t *testing.T) {
    _, err := DispatchOne(context.Background(), depsWithSchema(2), queuedTicketID, "alice")
    if !errors.Is(err, ErrWorkflowNotV3) { t.Fatalf("err = %v", err) }
    if reservations != 0 || captured != 0 || binderCalls != 0 { t.Fatalf("side effects: reserve=%d capture=%d bind=%d", reservations, captured, binderCalls) }
}
```

Keep and tighten the existing v3 tests: selected ticket revision is captured once, exact `work-item/v1` and `repository/v1` IDs are passed in `BindRequest.Inputs`, mutable later ticket edits cannot rebind them, idempotent retry links the same durable run, and a wrong port/type is rejected without a pipeline run. Add binder coverage that no public error category named `ErrLegacyDefinition` exists; a non-v3 definition reaching `validateDefinition` is an inconsistent store/platform failure.

- [ ] **Step 2: Run the focused tests to verify they fail**

Run: `go test ./agent/dispatch ./agent/workflowrun -run 'Test.*(SchemaThree|Legacy|BindAndCreate)' -count=1`

Expected: FAIL because dispatch retains the v1/v2 renderer branch and binder exports `ErrLegacyDefinition`.

- [ ] **Step 3: Delete the fallback branch and simplify dependencies**

Immediately after resolving a definition, perform:

```go
if definition.SchemaVersion != 3 {
    return Result{}, fmt.Errorf("%w: workflow %s v%d uses schema_version %d", ErrWorkflowNotV3, definition.Name, definition.Version, definition.SchemaVersion)
}
return dispatchV3(ctx, deps, ticket, definition, dispatchedBy)
```

Keep `dispatchV3`'s reservation → `CaptureRevision` → exact repository binding → `BindAndCreate` → `RecordDispatchRun` order and orphan cancellation. Remove all legacy freeze/spec/plan/render/template/CreateRun/attachRunSecret code and its dependencies. In the binder, delete `ErrLegacyDefinition` and replace the unreachable schema mismatch with `fmt.Errorf("%w: definition schema_version is not 3", ErrPlatformFailure)`. Update both dispatcher and API construction in `atc/atccmd/command.go` to pass only the binder adapter dependencies; preserve the generic workflow-run secret preparer wired into the binder.

Delete `RunSecretLabeler`, `SecretLabels`, `labels.go`, and `labels_test.go`;
model-secret reaping uses the workflow-run label and does not need the retired
ticket patch. Delete `AgentRepoBaseURL` / `--agent-repo-base-url` and both
runtime reads. Replace the DB schema-2/template-pipeline dispatch fixture with
a binder-backed schema-3 fixture. Remove `ErrLegacyDefinition` handling from
the experiment adapter, workflow-run HTTP handler, and durable error mapper.

- [ ] **Step 4: Map the new dispatch error and run tests**

Map `ErrWorkflowNotV3` to HTTP 422 in `agent/dispatch/handler.go`/tests.

```bash
go test ./agent/dispatch ./agent/workflowrun ./agent/api/workflowruns ./atc/atccmd -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='dispatching a ticket end-to-end|the dispatcher loop over real stores' ./atc/db
! rg -n 'ErrLegacyDefinition|RunSecretLabeler|SecretLabels|AgentRepoBaseURL|agent-repo-base-url' agent atc fly go-concourse --glob '!**/*_test.go'
git diff --check
```

Expected: PASS; ticket dispatch has one v3 path and no ticket-specific template save/run creation.

- [ ] **Step 5: Commit binder-only ticket dispatch**

```bash
git add agent/dispatch agent/workflowrun agent/api/workflowruns atc/db/agent_dispatch_test.go atc/atccmd/command.go
git commit -m "feat(dispatch): bind tickets only through workflow runs"
```

### Task 6: Replace Fly ticket pipeline derivation with durable workflow-run navigation

**Files:**
- Modify: `fly/commands/agent_tickets.go`
- Create: `fly/commands/agent_tickets_test.go`
- Modify: `go-concourse/concourse/agent_tickets_test.go`
- Modify: `fly/integration/agent_tickets_test.go`

**Interfaces:**
- Consumes: `tickets.Ticket.WorkflowRunID`, `WorkflowName`, and `PipelineRunID`.
- Produces: `fly agent tickets show --id N` text `workflow run: <id> · inspect with: fly -t <target> agent workflows run <workflow> <id>`.
- Produces: `tickets watch` delegation to `WorkflowsShowRunCommand{Workflow, RunID, Follow: true}`; it never scans builds by pipeline name.

- [ ] **Step 1: Write failing command/client tests**

```go
It("decodes durable workflow_run_id from a ticket dispatch response", func() {
    runID := snapshot.WorkflowRunID(9007199254740993)
    // mock POST /dispatch with WorkflowRunID: &runID
    Expect(result.WorkflowRunID).To(PointTo(Equal(runID)))
})
```

Add command tests for list/show rendering a durable run ID, watch failing clearly when a ticket has no `workflow_run_id`, and watch issuing only workflow-run detail requests. Assert `ticketPipelineName` is absent and that no output contains `agent-ticket-`.

- [ ] **Step 2: Run focused Fly tests to verify they fail**

Run: `go test ./go-concourse/concourse ./fly/commands -run 'Test.*AgentTicket' -count=1`

Expected: FAIL because list/show/watch derive a deterministic pipeline name and the old client fixture has no durable ID.

- [ ] **Step 3: Implement durable-run output and watch behavior**

Remove `ticketPipelineName`, `atc.DefaultTeamName`, build listing, `eventstream`, and `go-concourse` build scanning from `agent_tickets.go`. Render the durable run when `WorkflowRunID != nil`; use `WorkflowName` plus `WorkflowRunID.String()` to invoke the existing workflow-run show/follow implementation. Keep `PipelineRunID` only as optional diagnostic text; never treat it as the ticket identity.

- [ ] **Step 4: Run focused and integration Fly tests**

Run: `go test ./go-concourse/concourse ./fly/commands -run 'Test.*AgentTicket' -count=1 && make test-fly-integration`

Expected: PASS; ticket commands retain CRUD/queue/dispatch behavior and follow durable runs without `agent-ticket-*` lookup.

- [ ] **Step 5: Commit the CLI cutover**

```bash
git add fly/commands/agent_tickets.go fly/commands/agent_tickets_test.go fly/integration/agent_tickets_test.go go-concourse/concourse/agent_tickets_test.go
git commit -m "feat(fly): follow ticket workflow runs by durable id"
```

### Task 7: Make the ticket UI a durable workflow-run shell

**Files:**
- Modify: `web/elm/src/AgentTickets/AgentTicket.elm`
- Modify: `web/elm/src/Concourse/AgentTicket.elm`
- Modify: `web/elm/tests/AgentTicketPageTests.elm`
- Modify: `web/elm/tests/AgentTicketTests.elm`
- Modify: `web/elm/tests/WorkflowRunDecoderTests.elm`
- Modify: `web/elm/src/Build/Build.elm`
- Modify: `web/elm/src/Dashboard/Filter.elm`
- Modify: `web/elm/tests/BuildTicketBarTests.elm`
- Modify: `web/elm/tests/DashboardAgentFilterTests.elm`

**Interfaces:**
- Consumes: `workflow_run_id`, `work_item_snapshot_id`, `repository_snapshot_id`, and `Concourse.WorkflowRun.Detail`.
- Produces: one workflow-run route link and snapshot/output projection links; no `Routes.OneOffBuild` from ticket run metrics.

- [ ] **Step 1: Write failing Elm tests**

```elm
test "ticket durable evidence links the workflow run and exact snapshots" <|
    \_ ->
        view durableTicket
            |> Query.find [ id "ticket-durable-evidence" ]
            |> Query.has [ text "workflow run #9007199254740993" ]
```

Add a decoder test for string durable IDs above JavaScript's safe integer range, and page tests proving a ticket with `workflow_run_id` fetches `FetchAgentWorkflowRun workflowName runId`; a ticket without one renders no build/pipeline fallback. Assert the ticket detail DOM has no `agent-ticket-run-row`/one-off-build route.

Add Build/Dashboard tests proving ordinary pipeline names are not interpreted
through an `agent-ticket-<id>` convention and no ticket bar/filter decision is
derived from a pipeline-name prefix.

- [ ] **Step 2: Run Elm tests to verify they fail**

Run:

```bash
cd web/elm && npx elm-test \
  tests/AgentTicketPageTests.elm \
  tests/AgentTicketTests.elm \
  tests/WorkflowRunDecoderTests.elm \
  tests/BuildTicketBarTests.elm \
  tests/DashboardAgentFilterTests.elm
```

Expected: FAIL because the legacy metric-row build link remains reachable.

- [ ] **Step 3: Remove ticket-pipeline presentation**

Retain `durableEvidenceLine` as the single run identity line. Delete `runRow` rendering and any ticket page branch that links a build derived from ticket metrics. Keep snapshot and workflow-run fetch effects already keyed by `workflowRunId`; make decoders accept only the string durable-id representation for `workflow_run_id`, `work_item_snapshot_id`, and `repository_snapshot_id`.

Delete the pipeline-name-derived ticket bar in `Build.elm` and the
`String.startsWith "agent-ticket-"` dashboard classification in `Filter.elm`.
Retain unrelated ticket-page CSS class names.

- [ ] **Step 4: Run Elm tests**

Run:

```bash
cd web/elm && npx elm-test \
  tests/AgentTicketPageTests.elm \
  tests/AgentTicketTests.elm \
  tests/WorkflowRunDecoderTests.elm \
  tests/BuildTicketBarTests.elm \
  tests/DashboardAgentFilterTests.elm
```

Expected: PASS; the ticket page is a projection shell over canonical workflow-run/snapshot data.

- [ ] **Step 5: Commit the UI cutover**

```bash
git add web/elm/src/AgentTickets/AgentTicket.elm web/elm/src/Concourse/AgentTicket.elm web/elm/src/Build/Build.elm web/elm/src/Dashboard/Filter.elm web/elm/tests/AgentTicketPageTests.elm web/elm/tests/AgentTicketTests.elm web/elm/tests/WorkflowRunDecoderTests.elm web/elm/tests/BuildTicketBarTests.elm web/elm/tests/DashboardAgentFilterTests.elm
git commit -m "feat(web): link tickets to durable workflow runs"
```

### Task 8: Prove the v3-only vertical slice and repository invariants

**Files:**
- Modify: `agent/workflowrun/e2e_test.go`
- Modify: `atc/db/agent_workflow_run_integration_test.go`
- Modify: `docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`

**Interfaces:**
- Verifies: import/promote/manual bind/ticket bind/retry/experiment bind only consume schema 3; historic rows remain readable but inert.

- [ ] **Step 1: Add the end-to-end assertions**

```go
// Create v3 source, promote it, bind exact work-item/repository snapshots
// through DispatchOne, and assert ticket.WorkflowRunID == run.ID.
// Then attempt schema 2 import/promotion/dispatch and assert each fails before
// a template, pipeline run, or snapshot binding is created.
```

In the DB integration test, upgrade a database containing a live v2 row and a v3 row, assert only the v3 row is live, then delete the linked pipeline/build rows and assert the durable workflow run and ticket's `workflow_run_id` remain queryable.

- [ ] **Step 2: Run the red focused checks**

Run: `go test ./agent/workflowrun -run 'Test.*(E2E|Legacy|Ticket)' -count=1 && ginkgo --focus='agent workflow run' ./atc/db`

Expected: FAIL until the preceding tasks are complete.

- [ ] **Step 3: Run the complete required verification sequence**

Run these commands in order. The focused DB suites use the repository's
disposable PostgreSQL runner; verify `initdb`, `postgres`, and `psql` are
available and use the module-pinned Ginkgo:

```bash
command -v initdb postgres psql
go test ./agent/workflow ./agent/api/workflows ./agent/dispatch ./agent/workflowrun -count=1
go run github.com/onsi/ginkgo/v2/ginkgo --focus='workflow schema signature migration|v3-only workflow|Legacy Database Upgrade' ./atc/db/migration
go run github.com/onsi/ginkgo/v2/ginkgo --focus='AgentWorkflowsFactory|agent workflow run' ./atc/db
make test-ci-agent
make test-fly-integration
(cd web/elm && npx elm-test)
make test-unit
make test-integration
```

Expected: every command exits 0. If a generated fake changes with an interface deletion, regenerate it with the repository's existing `go generate` target and include the generated diff in the owning task commit.

- [ ] **Step 4: Run invariant scans and record their zero-result output**

```bash
! rg -n 'RenderLegacyTicket|workflow\.Config|ErrLegacyDefinition|schema_version: [12]' agent atc fly web go-concourse --glob '!**/*_test.go' --glob '!atc/db/migration/**'
! rg -n 'github.com/concourse/concourse/agent/workflow' atc/db/migration/legacyworkflow atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go
! rg -n 'AgentRepoBaseURL|agent-repo-base-url' agent atc fly web go-concourse --glob '!**/*_test.go'
! rg -n 'ticketPipelineName|String\.startsWith "agent-ticket-"|String\.dropLeft .*agent-ticket-' fly/commands web/elm/src
```

Expected: all four scans have no matches. Historical migration fixtures may
retain literal v1/v2 YAML only in migration tests; CSS class names do not
match the structural derivation scan.

- [ ] **Step 5: Commit verification evidence**

```bash
git add agent/workflowrun/e2e_test.go atc/db/agent_workflow_run_integration_test.go docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md
git commit -m "test(agent): verify v3-only workflow cutover"
```

## Self-review

- Spec coverage: Tasks 1–3 preserve safe historical upgrades while preventing legacy runtime admission; Tasks 2–3 reject schema 1/2 imports and promotions and demote old live rows; Tasks 4–5 remove renderer/seeds/budget/checkpoint fallback and make ticket dispatch binder-only; Tasks 6–7 replace CLI/UI pipeline identity with `workflow_run_id`; Task 8 verifies imports, promotions, manual/ticket execution, historical readability, and retention independence.
- Explicitly deferred to the other approved cleanup plans: harvest execution/package removal, platform-MCP/notification removal, run-principal/static-token cleanup, outcome-table migration, metrics migration, compatibility routes, and pipeline-archiver operational cleanup.
- Placeholder scan: no prohibited placeholder language remains. Every code-changing task names files, interfaces, a red command, an implementation boundary, a green command, and a commit.
- Type consistency: runtime uses `workflow.UnsupportedSchemaVersionError`, `workflow.InvalidPromotionError`, `dispatch.ErrWorkflowNotV3`, `workflowrun.BindAndCreate`, `snapshot.WorkflowRunID`, and the existing `tickets.RecordDispatchRun` consistently across all tasks.

## Execution handoff

Execution uses subagent-driven development with a fresh implementation agent
and task review at each dependency boundary.
