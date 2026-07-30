# Reusable Node Definitions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add immutable, directly testable, releasable reusable node versions that workflows compose by exact reference and upgrade into validated unpromoted revisions.

**Architecture:** Reuse the existing executable-definition table, compiler, opaque execution envelope, binder, and durable workflow-run lifecycle with a kind discriminator for workflows and nodes. A schema-1 `node.yaml` compiles to one self-contained task, agent, or publish leaf; workflow imports resolve exact released node references into ordinary visible Concourse steps and persist consumer bindings. HTTP and Fly surfaces manage the node lifecycle, direct runs, consumers, and selected-workflow upgrades.

**Tech Stack:** Go 1.25, PostgreSQL migrations/stores, Concourse ATC plan types, Goccy YAML, HTTP handlers/routes, Fly CLI, Ginkgo/Gomega, standard `testing`.

## Global Constraints

- Work only in `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-platform-rebase` on `codex/agentic-platform-rebase`.
- Preserve all accepted workflow, source-admission, validation, Hangar, publisher, checkpoint, recovery, and opaque `workflow.ExecutionEnvelope` behavior.
- Existing definition rows migrate as kind `workflow`; existing workflow APIs, parsing, rendering, and runs remain behaviorally unchanged.
- Names are kind-scoped: `workflow/code-review` and `node/code-review` may coexist.
- Node source is `node.yaml`, schema version 1, and compiles to exactly one visible `task`, `agent`, or `publish_snapshot` leaf.
- Nodes reject wrappers, multiple steps, waits, resource reads/sources, mutable dependencies, and hidden subgraphs.
- Node implementations bake model, prompt, skills, image, command, capabilities, and validation; workflows may supply only input/output mappings and declared string parameters.
- Publication node destination, mode, publisher parameters, approval policy, and authority are always baked; publication nodes do not declare caller-supplied parameters.
- Node references always use an exact released integer version and are resolved during workflow import, never during a run.
- Direct node invocation is permitted for imported, released, deprecated, and unreleased exact versions, but it never accepts implementation overrides or a caller-built execution envelope.
- A compatible node release must preserve every prior valid invocation. A breaking release is allowed only when declared explicitly.
- Upgrades select workflow names, create validated immutable unpromoted revisions independently, and never promote automatically.
- Initial product surfaces are HTTP and Fly CLI. Elm UI, resource-owning nodes, composite nodes, and generalized experiment integration are deferred.
- Current migration head and migrate-preflight target are `1773106148`; this plan owns `1773106149` and `1773106150`.
- PostgreSQL-backed suites run serially. Use focused tests during implementation and broad suites once at the final milestone.
- Follow TDD: add or adapt a behavioral test, observe the expected failure, implement the minimum behavior, then rerun focused tests.
- Review budget follows the active session context: at most three focused rounds per task, fixing only correctness, security, data-integrity, migration, or required-acceptance blockers.

---

### Task 1: Atomic node source model and compiler

**Files:**
- Modify: `agent/workflow/manifest.go`
- Create: `agent/workflow/node_definition.go`
- Create: `agent/workflow/node_parse.go`
- Create: `agent/workflow/node_compile.go`
- Create: `agent/workflow/node_definition_test.go`
- Create: `agent/workflow/node_compile_test.go`
- Modify: `agent/workflow/compile.go`

**Interfaces:**
- Produces: `workflow.DefinitionKind`, `workflow.CompiledNodeDefinition`, `workflow.NodeParameter`, `workflow.CompileNodeDefinition(workflow.Manifest)`, `CompiledNodeDefinition.Instantiate(map[string]string)`.
- Consumes: existing `Manifest`, `snapshot.Port`, `atc.Step`, `compileFunctionAssets`, `ValidateFunction`, and immutable asset checks.

- [ ] **Step 1: Add failing domain tests for kind, parameters, and contracts**

```go
func TestCompiledNodeDefinitionInstantiate(t *testing.T) {
	medium := "medium"
	node := workflow.CompiledNodeDefinition{
		SchemaVersion: 1,
		Name:          "code-review",
		Parameters: []workflow.NodeParameter{
			{Name: "MINIMUM_SEVERITY", Default: &medium},
		},
		Function: workflow.FunctionConfig{
			SignatureVersion: 1,
			Inputs: []snapshot.Port{
				{Name: "repository", Type: "repository/v1"},
			},
			Outputs: []workflow.FunctionOutput{
				{Port: snapshot.Port{Name: "review", Type: "review/v1"}, From: "review"},
			},
			Plan: []atc.Step{
				{Config: &atc.AgentStep{Name: "review", Prompt: "review"}},
			},
		},
	}

	function, err := node.Instantiate(map[string]string{"MINIMUM_SEVERITY": "high"})
	if err != nil {
		t.Fatal(err)
	}
	agent := function.Plan[0].Config.(*atc.AgentStep)
	if agent.Env["MINIMUM_SEVERITY"] != "high" {
		t.Fatalf("env = %#v", agent.Env)
	}
}
```

Cover duplicate/blank parameters, missing required values, unknown values,
default application, task `Params`, and agent `Env`. Prove that a
`publish_snapshot` node with declared parameters is rejected and that its
baked publication parameters cannot be changed during instantiation.

- [ ] **Step 2: Run the domain tests and verify RED**

Run:

```bash
go test ./agent/workflow -run 'TestCompiledNodeDefinition|TestNodeParameter' -count=1
```

Expected: build failure because the node types and compiler do not exist.

- [ ] **Step 3: Add the node source and compiled types**

Define:

```go
type DefinitionKind string

const (
	DefinitionKindWorkflow DefinitionKind = "workflow"
	DefinitionKindNode     DefinitionKind = "node"
)

type NodeParameter struct {
	Name    string  `json:"name" yaml:"name"`
	Default *string `json:"default,omitempty" yaml:"default,omitempty"`
}

type CompiledNodeDefinition struct {
	SchemaVersion int             `json:"schema_version"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Parameters    []NodeParameter `json:"parameters,omitempty"`
	Function      FunctionConfig  `json:"function"`
}
```

`Instantiate` must deep-clone the step, validate declared values, apply
defaults, inject parameters only into the permitted leaf environment, and
return a one-step `FunctionConfig` whose public outputs map from the same
logical names. Keeping the compiled one-step function intact preserves
compiled skill files, validation profiles, literal sidecars, and other
accepted compiler-owned authority without duplicating those fields in a
second model. Task parameters enter `TaskStep.Params`; agent parameters enter
`AgentStep.Env`; publication nodes reject a nonempty declared-parameter
contract.

- [ ] **Step 4: Add failing parser/compiler tests for complete node packages**

Use a manifest containing:

```yaml
node.yaml: |
  schema_version: 1
  name: code-review
  description: Review one repository change
  inputs:
    - {name: repository, type: repository/v1}
    - {name: change, type: repository-change/v1}
  outputs:
    - {name: review, type: review/v1}
  parameters:
    - {name: MINIMUM_SEVERITY, default: medium}
  capabilities:
    dev-mcp:
      contract: dev-mcp/v1
      sidecar:
        name: dev-mcp
        image: registry.example/dev-mcp@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        process: {path: /usr/local/bin/dev-mcp}
        port: 8080
  step:
    agent: review
    prompt_file: prompts/review.md
    model: claude-sonnet
    skills: [review]
    capabilities: [dev-mcp]
```

Assert prompt, selected skill files, sidecar, digest-pinned image, ports, and
parameters are frozen into `CompiledNodeDefinition`. Add table cases rejecting
`do`, `in_parallel`, retry, hooks, `await_snapshot`, `get`, `load_var`,
resources/resource_sources, two leaves, mutable images, undeclared assets, and
unknown source fields.

- [ ] **Step 5: Run parser/compiler tests and verify RED**

Run:

```bash
go test ./agent/workflow -run 'TestCompileNode|TestParseNode' -count=1
```

Expected: failures because `node.yaml` is not recognized.

- [ ] **Step 6: Implement strict schema-1 node compilation**

Add `NodeFileName = "node.yaml"` and reuse the manifest path/size/canonical
hash rules. Parse the strict node envelope, decode `step` through ordinary ATC
step decoding, require exactly one permitted leaf, synthesize a one-step
function, run existing asset compilation and type validation, and return the
compiled node with source-only capability names erased.

- [ ] **Step 7: Run the focused workflow package**

Run:

```bash
go test ./agent/workflow/... -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 1**

```bash
git add agent/workflow
git commit -m "feat(workflow): compile atomic reusable nodes"
```

---

### Task 2: Kind-scoped durable node versions and lifecycle

**Files:**
- Create: `atc/db/migration/migrations/1773106149_create_reusable_node_definitions.up.sql`
- Create: `atc/db/migration/migrations/1773106149_create_reusable_node_definitions.down.sql`
- Create: `atc/db/migration/reusable_node_definitions_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`
- Modify: `agent/workflow/definition.go`
- Create: `agent/workflow/node_store.go`
- Create: `agent/workflow/workflowtest/memory_node_store.go`
- Create: `atc/db/agent_nodes_factory.go`
- Create: `atc/db/agent_nodes_factory_test.go`
- Modify: `atc/db/agent_workflows_factory.go`

**Interfaces:**
- Consumes: Task 1 `CompiledNodeDefinition` and `CompileNodeDefinition`.
- Produces: `workflow.NodeDefinition`, `workflow.NodeStore`, `workflow.NodeRelease`, `db.AgentNodesFactory`.

- [ ] **Step 1: Add a failing migration test**

Prove that upgrading from `1773106148`:

```sql
SELECT definition_kind, count(*)
FROM agent_workflow_definitions
GROUP BY definition_kind;
```

returns only `workflow` for preexisting rows; prove a workflow and node named
`code-review` can each allocate version 1; prove node rows cannot become
`live`; and prove the down migration restores the original unique indexes.

- [ ] **Step 2: Run the migration test and verify RED**

Run:

```bash
ginkgo --focus='reusable node definition migration' ./atc/db/migration/
```

Expected: FAIL because migration `1773106149` is absent.

- [ ] **Step 3: Add migration `1773106149`**

Add:

```sql
ALTER TABLE agent_workflow_definitions
  ADD COLUMN definition_kind TEXT NOT NULL DEFAULT 'workflow',
  ADD COLUMN released_at TIMESTAMPTZ,
  ADD COLUMN released_by TEXT NOT NULL DEFAULT '',
  ADD COLUMN deprecated_at TIMESTAMPTZ,
  ADD COLUMN deprecated_by TEXT NOT NULL DEFAULT '',
  ADD COLUMN release_predecessor_version INTEGER,
  ADD COLUMN release_compatibility TEXT;

ALTER TABLE agent_workflow_runs
  ADD COLUMN definition_kind TEXT NOT NULL DEFAULT 'workflow';
```

Replace `(name, version)` and `(name, content_hash)` uniqueness with
`(definition_kind, name, version)` and
`(definition_kind, name, content_hash)`. Add checks for valid kinds,
workflow-only `live`, release metadata completeness, compatibility values
`compatible|breaking`, and a partial index over released node versions.
Existing rows remain `workflow`.

Node rows store runtime `schema_version = 3` and a positive signature version
because their compiled execution target is the accepted one-step v3 function;
`CompiledNodeDefinition.SchemaVersion = 1` remains the source-format
discriminator exposed by node APIs. Add a run-kind check and index so
workflow and node run queries can filter without name collisions.
Replace the run idempotency uniqueness and lookup contract with
`(team_id, definition_kind, idempotency_key)`, include kind in the
idempotency advisory lock, and require retry targets to have the same kind.
Existing workflow requests normalize an omitted kind to `workflow`.

Advance both migration-head constants to `1773106149`.

- [ ] **Step 4: Add failing node-store tests**

Cover monotonic per-name node versions, canonical-manifest idempotence,
kind-scoped name coexistence, exact `Get`, `Latest`, bounded `Versions`, list,
release, deprecate, direct reads of unreleased/deprecated versions, and
concurrent import/release serialization.

Release tests must prove:

```go
_, err := store.Release("code-review", 2, workflow.ReleaseCompatible, "alice")
// ErrInvalidCompatibility when version 2 removes a required output or parameter.
```

and that an explicit breaking release succeeds while retaining version 1 as
released.

- [ ] **Step 5: Run node-store tests and verify RED**

Run:

```bash
ginkgo --focus='AgentNodesFactory' ./atc/db/
go test ./agent/workflow/workflowtest -run Node -count=1
```

Expected: build failure because `NodeStore` and factory do not exist.

- [ ] **Step 6: Implement the node domain/store and DB factory**

`NodeDefinition` carries ID, name, integer version, content hash, compiled
node, source manifest, created audit, release audit/classification, and
deprecation audit. The DB factory must use
`pg_advisory_xact_lock(hashtext('agent_node_definitions:' || $1))`, filter
every query by `definition_kind = 'node'`, compile stored node manifests on
read, and validate stored metadata against compiled source.

Keep every existing workflow query explicitly filtered to
`definition_kind = 'workflow'`; add regression tests showing node rows never
appear through workflow list/get/live/version APIs.

- [ ] **Step 7: Run focused persistence tests**

Run:

```bash
go test ./agent/workflow/... -count=1
ginkgo --focus='AgentNodesFactory|AgentWorkflowsFactory|reusable node definition migration' ./atc/db/ ./atc/db/migration/
```

Expected: PASS.

- [ ] **Step 8: Commit Task 2**

```bash
git add agent/workflow atc/db docs/migration/migrate-preflight.sh
git commit -m "feat(db): persist reusable node versions"
```

---

### Task 3: Node catalog, import, release, and deprecation APIs and Fly CLI

**Files:**
- Create: `agent/api/nodes/handler.go`
- Create: `agent/api/nodes/handler_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/atccmd/command.go`
- Create: `fly/commands/agent_nodes.go`
- Create: `fly/commands/agent_nodes_test.go`
- Modify: `fly/commands/agent.go`
- Modify: `fly/integration/agent_workflows_test.go`

**Interfaces:**
- Consumes: Task 2 `workflow.NodeStore`.
- Produces: `/api/v1/agent/nodes` routes and `fly agent nodes` commands.

- [ ] **Step 1: Add failing HTTP handler tests**

Cover:

```text
GET  /api/v1/agent/nodes
GET  /api/v1/agent/nodes/:name/versions
GET  /api/v1/agent/nodes/:name/versions/:version
POST /api/v1/agent/nodes/:name/versions
PUT  /api/v1/agent/nodes/:name/versions/:version/release
PUT  /api/v1/agent/nodes/:name/versions/:version/deprecation
```

Manifest import accepts `{"files": {...}}`, enforces existing manifest byte
caps, derives the request user, maps invalid node source to 400, missing
versions to 404, false compatible releases to 422, and returns complete
release/deprecation audit.

- [ ] **Step 2: Run handler tests and verify RED**

Run:

```bash
go test ./agent/api/nodes -count=1
```

Expected: package/build failure because the node API does not exist.

- [ ] **Step 3: Implement handlers, routes, authorization, and wiring**

Release accepts only:

```go
type releaseRequest struct {
	Compatibility workflow.ReleaseCompatibility `json:"compatibility"`
}
```

The store derives the predecessor as the latest previously released version;
callers cannot supply it. Deprecation accepts `{"deprecated":true}` and may
clear deprecation without changing release history.

- [ ] **Step 4: Add failing Fly command tests**

Add `list`, `show`, `import`, `release`, `deprecate`, and `restore`.
Test exact request paths, manifest packaging, compatibility payload, JSON
output, default-version resolution preferring latest released, and readable
tables.

- [ ] **Step 5: Run Fly tests and verify RED**

Run:

```bash
go test ./fly/commands -run 'TestAgentNodes' -count=1
```

Expected: build failure because `AgentNodesCommand` is absent.

- [ ] **Step 6: Implement Fly commands**

Use the existing workflow helpers for authenticated requests, bounded history
pagination, directory manifest loading, and JSON/table output. Do not add
client-side compatibility decisions.

- [ ] **Step 7: Run focused API/Fly suites**

Run:

```bash
go test ./agent/api/nodes ./fly/commands -count=1
go test ./atc/api ./atc/wrappa ./atc/atccmd -run AgentNode -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 3**

```bash
git add agent/api/nodes atc fly
git commit -m "feat(agent): expose reusable node lifecycle"
```

---

### Task 4: Exact workflow node-reference expansion

**Files:**
- Create: `agent/workflow/node_reference.go`
- Create: `agent/workflow/node_reference_test.go`
- Modify: `agent/workflow/parse.go`
- Modify: `agent/workflow/compile.go`
- Modify: `agent/workflow/function_config.go`
- Modify: `agent/workflow/typecheck.go`
- Modify: `atc/steps.go`
- Modify: `atc/plan.go`
- Modify: `atc/builds/planner.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `atc/exec/agent_step_test.go`
- Modify: `agent/workflow/workflowtest/memory_store.go`
- Modify: `atc/db/agent_workflows_factory.go`

**Interfaces:**
- Consumes: Task 2 `NodeStore`.
- Produces: `workflow.NodeReference`, `workflow.NodeResolver`, `workflow.CompileDefinitionWithNodes`, `workflow.ResolvedNodeBinding`.

- [ ] **Step 1: Add failing strict source tests for node references**

Use:

```yaml
- node: review-change
  uses: code-review@5
  input_mapping:
    repository: checked-out-repository
    change: proposed-change
  output_mapping:
    review: review-result
  params:
    MINIMUM_SEVERITY: high
```

Assert rejection of missing/duplicate local IDs, noninteger/latest/channel
references, unknown/unreleased versions, unknown or missing mappings,
undeclared parameters, implementation override fields, and references that
appear anywhere a normal visible leaf may appear inside ordinary wrappers.

- [ ] **Step 2: Run reference tests and verify RED**

Run:

```bash
go test ./agent/workflow -run 'TestNodeReference|TestCompileDefinitionWithNodes' -count=1
```

Expected: parser rejects `node`/`uses` as unknown.

- [ ] **Step 3: Implement recursive source resolution**

Define:

```go
type NodeResolver interface {
	Released(name string, version int) (NodeDefinition, bool, error)
}

type ResolvedNodeBinding struct {
	InstanceName     string
	NodeDefinitionID int
	NodeName         string
	NodeVersion      int
	NodeContentHash  string
	InputMapping     map[string]string
	OutputMapping    map[string]string
	Parameters       map[string]string
}
```

Validate the authoring-only reference before ordinary Concourse decoding,
resolve the exact released version, instantiate it, apply only mappings and
declared parameters, set the task/agent `FunctionID` (or publish name) to the
stable local instance, and replace the reference with the concrete leaf.
Return both the compiled workflow and sorted durable bindings.

- [ ] **Step 4: Add failing agent artifact-mapping tests**

Prove an agent authored with logical input `repository` receives the mapped
workflow artifact under logical mount name `repository`, and its logical
output `review` registers under workflow artifact `review-result`. Prove
snapshot type checking uses logical contracts before mapping and physical
artifact names after mapping. Add equivalent task coverage using the existing
native mappings. For publication nodes, prove the single logical input maps to
`PublishSnapshotStep.Input`, that public outputs and declared parameters are
rejected, and that destination/mode/publisher parameters remain byte-for-byte
baked.

- [ ] **Step 5: Run agent mapping tests and verify RED**

Run:

```bash
go test ./atc/exec -run 'TestAgent.*Mapping' -count=1
go test ./atc/builds -run 'Agent.*Mapping' -count=1
```

Expected: agent cases fail because `AgentStep`/`AgentPlan` lack mapping
fields; task mapping already exists and publication mapping is performed
during expansion.

- [ ] **Step 6: Add agent input/output mapping support**

Reuse native task `InputMapping`/`OutputMapping`. Add those fields to
`AgentStep` and `AgentPlan`, copy them in the planner, mount mapped sources
under logical names, and register logical outputs under mapped workflow names.
For publication nodes, rewrite only the baked input artifact name during
expansion. Keep direct authored agents with nil mappings byte-compatible.

- [ ] **Step 7: Wire node-aware workflow imports**

Add an optional trusted `NodeResolver` to DB and memory workflow stores.
Ordinary `CompileDefinition` continues to reject node references. Store import
and stored-source recompilation use `CompileDefinitionWithNodes` when a
resolver is configured. Existing workflow-only constructors retain their
behavior.

- [ ] **Step 8: Run focused compiler/planner/exec tests**

Run:

```bash
go test ./agent/workflow/... ./atc/builds ./atc/exec -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 4**

```bash
git add agent/workflow atc
git commit -m "feat(workflow): compose exact reusable node versions"
```

---

### Task 5: Durable workflow-to-node consumer bindings

**Files:**
- Create: `atc/db/migration/migrations/1773106150_create_workflow_node_bindings.up.sql`
- Create: `atc/db/migration/migrations/1773106150_create_workflow_node_bindings.down.sql`
- Create: `atc/db/migration/workflow_node_bindings_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`
- Modify: `atc/db/agent_workflows_factory.go`
- Create: `atc/db/agent_node_consumers.go`
- Modify: `atc/db/agent_nodes_factory.go`
- Modify: `atc/db/agent_workflows_factory_test.go`
- Modify: `atc/db/agent_nodes_factory_test.go`

**Interfaces:**
- Consumes: Task 4 `ResolvedNodeBinding`.
- Produces: durable `agent_workflow_node_bindings` and `NodeStore.Consumers`.

- [ ] **Step 1: Add a failing migration and persistence test**

The table must contain:

```sql
workflow_definition_id INTEGER NOT NULL,
instance_name TEXT NOT NULL,
node_definition_id INTEGER NOT NULL,
node_name TEXT NOT NULL,
node_version INTEGER NOT NULL,
node_content_hash TEXT NOT NULL,
input_mapping JSONB NOT NULL,
output_mapping JSONB NOT NULL,
parameters JSONB NOT NULL,
PRIMARY KEY (workflow_definition_id, instance_name)
```

Add FKs to both definition rows, exact node identity checks, and indexes for
`(node_definition_id, workflow_definition_id)` and
`(node_name, node_version)`.

Test that workflow import and binding insertion are atomic, an idempotent
manifest hit returns the same bindings, persisted bindings match the compiled
node hash, and a node/workflow kind swap is rejected.

- [ ] **Step 2: Run focused DB tests and verify RED**

Run:

```bash
ginkgo --focus='workflow node bindings|AgentNodesFactory consumers' ./atc/db/ ./atc/db/migration/
```

Expected: FAIL because migration `1773106150` and binding persistence are
absent.

- [ ] **Step 3: Implement migration and atomic binding writes**

Advance both migration-head constants to `1773106150`. On new workflow
version insertion, write the compiler-returned bindings in the same
transaction. On idempotent import, read and compare the persisted set to the
fresh resolution and fail closed on mismatch.

- [ ] **Step 4: Implement bounded consumer discovery**

Define a paged read returning workflow name/version/live state, stable local
instance, exact mappings/parameters, and node identity. Support
`PromotedOnly` for upgrade discovery while retaining historical reads for
provenance.

- [ ] **Step 5: Run focused DB/migration tests**

Run:

```bash
ginkgo --focus='workflow node bindings|AgentNodesFactory consumers|AgentWorkflowsFactory' ./atc/db/ ./atc/db/migration/
```

Expected: PASS.

- [ ] **Step 6: Commit Task 5**

```bash
git add atc/db docs/migration/migrate-preflight.sh
git commit -m "feat(db): index reusable node consumers"
```

---

### Task 6: Direct node test runs through the existing binder

**Files:**
- Modify: `agent/workflowrun/types.go`
- Modify: `agent/workflowrun/binder.go`
- Modify: `agent/workflowrun/binder_test.go`
- Modify: `agent/workflowrun/retry.go`
- Modify: `agent/workflowrun/retry_test.go`
- Modify: `atc/db/agent_workflow_run.go`
- Modify: `atc/db/agent_workflow_runs_factory.go`
- Modify: `atc/db/agent_workflow_runs_factory_test.go`
- Modify: `agent/api/workflowruns/handler.go`
- Modify: `agent/api/workflowruns/handler_test.go`
- Modify: kind-blind workflow-run stores/handlers discovered by
  `rg 'AgentWorkflowRun|workflow_run_id|GetByID|Retry|Cancel' agent atc`
- Create: `agent/api/noderuns/handler.go`
- Create: `agent/api/noderuns/handler_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/atccmd/command.go`
- Modify: `fly/commands/agent_nodes.go`
- Modify: `fly/commands/agent_nodes_test.go`

**Interfaces:**
- Consumes: Task 2 `NodeStore` and Task 1 `Instantiate`.
- Produces: kind-aware binder request/run identity and direct node run HTTP/Fly surfaces.

- [ ] **Step 1: Add failing binder tests for exact node versions**

Prove a request with:

```go
workflowrun.BindRequest{
	DefinitionKind: workflow.DefinitionKindNode,
	WorkflowName:   "code-review",
	Version:        ptr.To(5),
	Inputs:         map[string]snapshot.SnapshotID{"repository": 41},
	NodeParameters: map[string]string{"MINIMUM_SEVERITY": "high"},
	IdempotencyKey: "node-test-1",
}
```

resolves only the exact node row, permits unreleased/deprecated versions,
instantiates parameters, uses an `agent-node-` template namespace, records
kind/name/version/hash, and rejects live/default resolution, unknown params,
caller `FunctionID`, implementation overrides, and node rows through workflow
requests. Use the same idempotency key once for a workflow and once for a node
and prove they create distinct runs; prove same-kind retries remain
idempotent and cross-kind retries fail closed.

- [ ] **Step 2: Run binder tests and verify RED**

Run:

```bash
go test ./agent/workflowrun -run 'Test.*Node' -count=1
```

Expected: failures because the binder is workflow-only.

- [ ] **Step 3: Generalize the trusted resolution seam**

Keep `BindAndCreate` as the only public execution authority. Add
`DefinitionKind` with empty normalized to `workflow`, inject a trusted
`NodeStore`, convert an exact instantiated node to the existing one-step
render target, and preserve the private execution-envelope path. Node
requests must require an explicit positive version.

Use the `agent_workflow_runs.definition_kind` column added by migration
`1773106149` while preserving existing workflow JSON fields for compatibility.
The run-creation query must join the durable definition and verify its kind
matches the request before any idempotency lookup. All workflow run
list/get/cancel/retry/status/transcript/review/wait/outcome/metrics entry
points must filter or assert kind `workflow`; node queries filter or assert
kind `node`. Centralize this in kind-aware factory methods where possible and
add cross-kind route tests so a numeric run ID cannot cross the API boundary.

Refactor source-admission target loading around the durable definition kind.
Workflow runs retain the existing stored-workflow recompilation and admission
validation. Node runs reject a supplied resource-source admission ID and,
after verifying the stored node kind, safely report no resource sources
because node compilation forbids them; they must never pass `node.yaml`
through `compileStoredWorkflowSource`. Retry validation reuses the same
kind-aware loader.

- [ ] **Step 4: Add failing node-run HTTP/Fly tests**

Add:

```text
POST /api/v1/agent/nodes/:name/versions/:version/runs
GET  /api/v1/agent/nodes/:name/versions/:version/runs
GET  /api/v1/agent/nodes/:name/runs/:run_id
```

Create accepts only `inputs`, `params`, and `idempotency_key`. It rejects
unknown JSON fields, implementation overrides, zero/default version, and
cross-kind run IDs.

Fly adds `run`, `runs`, and `show-run` under `agent nodes`.

- [ ] **Step 5: Run API/Fly tests and verify RED**

Run:

```bash
go test ./agent/api/noderuns ./fly/commands -run 'Node.*Run' -count=1
```

Expected: package/build failures because direct node run surfaces are absent.

- [ ] **Step 6: Implement direct run surfaces**

Reuse workflow-run input parsing, quoted-decimal ID handling, idempotency,
output manifest, cancellation/status presentation, and authenticated request
helpers. Do not duplicate execution or snapshot logic.

- [ ] **Step 7: Run focused direct-run verification**

Run:

```bash
go test ./agent/workflowrun ./agent/api/noderuns ./fly/commands -count=1
ginkgo --focus='node definition run|workflow definition run' ./atc/db/
```

Expected: PASS.

- [ ] **Step 8: Commit Task 6**

```bash
git add agent/workflowrun agent/api/noderuns atc fly
git commit -m "feat(agent): run exact reusable node versions"
```

---

### Task 7: Selected-consumer upgrade service

**Files:**
- Create: `agent/workflow/node_upgrade.go`
- Create: `agent/workflow/node_upgrade_test.go`
- Create: `agent/api/nodeupgrades/handler.go`
- Create: `agent/api/nodeupgrades/handler_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/atccmd/command.go`
- Modify: `fly/commands/agent_nodes.go`
- Modify: `fly/commands/agent_nodes_test.go`

**Interfaces:**
- Consumes: `NodeStore`, `workflow.Store`, consumer bindings, node release metadata.
- Produces: `workflow.NodeUpgradeService`, consumer and upgrade HTTP/Fly surfaces.

- [ ] **Step 1: Add failing compatible-upgrade service tests**

Given promoted workflows `small-fix@7`, `version-upgrade@3`, and an unselected
`dependency-audit@2` consuming `code-review@4`, releasing compatible
`code-review@5` and selecting the first two must:

```go
result, err := service.Upgrade(ctx, workflow.NodeUpgradeRequest{
	NodeName: "code-review",
	Version:  5,
	Workflows: []string{"small-fix", "version-upgrade"},
	CreatedBy: "alice",
})
```

create one new immutable non-live revision for each selected valid workflow,
rewrite every matching `uses: code-review@4` reference, leave the unselected
workflow and all live pointers unchanged, and return per-workflow success or
failure without cross-workflow rollback.

Add cases for missing live workflow, no matching predecessor, duplicate
selection, stale consumer binding, compilation failure, and an idempotent
repeat.

- [ ] **Step 2: Add failing breaking-upgrade tests**

An explicitly breaking released successor must return
`recomposition_required` with added/removed/changed ports and parameters and
must not call workflow import or persist any revision.

- [ ] **Step 3: Run upgrade service tests and verify RED**

Run:

```bash
go test ./agent/workflow -run 'TestNodeUpgrade' -count=1
```

Expected: build failure because the upgrade service is absent.

- [ ] **Step 4: Implement safe manifest rewriting and independent imports**

Decode only the promoted workflow's `workflow.yaml`/legacy `workflow.yml`,
recursively locate validated node-reference objects, and replace `uses` only
when name and predecessor version match. Preserve all other manifest files.
Re-encode, import through the node-aware workflow store, and verify the
resulting bindings target the successor content hash.

Do not persist authoring drafts. A breaking classification returns a
structured contract diff and exits before mutation.

- [ ] **Step 5: Add failing consumer/upgrade API and Fly tests**

Add:

```text
GET  /api/v1/agent/nodes/:name/versions/:version/consumers
POST /api/v1/agent/nodes/:name/versions/:version/upgrades
```

Upgrade body:

```json
{"workflows":["small-fix","version-upgrade"]}
```

Fly adds `consumers NAME VERSION` and repeatable
`upgrade NAME VERSION --workflow NAME`.

- [ ] **Step 6: Run API/Fly tests and verify RED**

Run:

```bash
go test ./agent/api/nodeupgrades ./fly/commands -run 'Node.*(Consumer|Upgrade)' -count=1
```

Expected: package/build failures because the surfaces are absent.

- [ ] **Step 7: Implement API/Fly upgrade surfaces**

Return a deterministic per-workflow result with old/new workflow version,
status (`created`, `unchanged`, `failed`, or `recomposition_required`), and
error/contract obligations. Never promote from this endpoint.

- [ ] **Step 8: Run focused upgrade verification**

Run:

```bash
go test ./agent/workflow ./agent/api/nodeupgrades ./fly/commands -count=1
ginkgo --focus='AgentNodesFactory consumers|AgentWorkflowsFactory' ./atc/db/
```

Expected: PASS.

- [ ] **Step 9: Commit Task 7**

```bash
git add agent/workflow agent/api/nodeupgrades atc fly
git commit -m "feat(agent): upgrade selected reusable node consumers"
```

---

### Task 8: End-to-end contract, decisions, and acceptance evidence

**Files:**
- Create: `agent/reusablenode/vertical_slice_test.go`
- Modify: `agent/workflow/seeds/code-review-v3/workflow.yaml`
- Create: `agent/workflow/seeds/code-review-node-v1/node.yaml`
- Create: `agent/workflow/seeds/code-review-node-v1/prompts/review.md`
- Modify: `agent/workflow/seed_test.go`
- Modify: `docs/superpowers/specs/2026-07-27-reusable-node-definitions-design.md`
- Create: `docs/operations/reusable-node-definitions.md`
- Modify: `docs/superpowers/plans/2026-07-28-agentic-foundations-semantic-rebase-deferred-items.md`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: one complete import-test-release-compose-run-upgrade proof and operator documentation.

- [ ] **Step 1: Add an end-to-end failing integration test**

The test must:

1. import `code-review` node version 1;
2. invoke it directly before release with exact snapshot inputs;
3. release it;
4. import and promote a workflow referencing `code-review@1`;
5. prove the compiled workflow contains one visible expanded agent leaf and a
   durable consumer binding;
6. import and directly test node version 2;
7. release version 2 as compatible;
8. upgrade the selected workflow;
9. prove the new workflow revision is valid and unpromoted; and
10. prove the original promoted workflow and historical run remain pinned to
    node version 1.

- [ ] **Step 2: Run the integration test and verify RED**

Run:

```bash
go test ./agent/reusablenode -run TestReusableNodeVerticalSlice -count=1
```

Expected: FAIL at the first missing integration seam.

- [ ] **Step 3: Complete the seed and vertical-slice wiring**

Add one node package that freezes its prompt/model/skills/capability contract.
Keep the existing code-review workflow as a compatibility seed unless changing
it is required for the test; do not convert multi-step workflows or introduce
automatic seed upgrades.

- [ ] **Step 4: Record decisions and operator behavior**

Update the design's architecture-alignment section if implementation revealed
a necessary deviation. Document node source syntax, import/test/release flow,
compatible versus breaking releases, direct run commands, consumer discovery,
upgrade behavior, and the separate workflow promotion step. Record every
nonblocking omitted UI/experiment/source-node improvement in the active
deferred-item catalog.

- [ ] **Step 5: Run milestone verification**

Check PostgreSQL first:

```bash
pg_isready
```

Then run, serializing DB suites:

```bash
go test ./agent/workflow/... ./agent/workflowrun ./agent/api/nodes ./agent/api/noderuns ./agent/api/nodeupgrades ./fly/commands -count=1
ginkgo --focus='AgentNodesFactory|AgentWorkflowsFactory|reusable node|workflow node binding|node definition run' ./atc/db/ ./atc/db/migration/
go test ./atc/builds ./atc/exec ./atc/api ./atc/wrappa ./atc/atccmd -count=1
make test-dev-mcp
make test-fly-integration
make test-integration
helm lint deploy/chart
git diff --check
```

Run `make test-unit` once if disk headroom and PostgreSQL prerequisites are
healthy; otherwise record the exact prerequisite blocker and the complete
focused evidence without repeated infrastructure retries.

- [ ] **Step 6: Commit Task 8**

```bash
git add agent atc fly docs
git commit -m "docs(agent): prove reusable node vertical slice"
```
