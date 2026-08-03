# Node Model Catalog and Budget Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status: Human Review Required.** The permitted review cap has been reached. Do not treat this track as accepted or request another agent review.
>
> **Remaining acceptance blocker — idempotent latest replay:** Task 3 currently specifies handler-side `LatestReleased` lookup before the binder. If a newer released version appears between the original `latest` request and an idempotency-key replay, that lookup can resolve the new version before the binder's early existing-run path, violating bind-once replay.
>
> **Preferred choice:** Move released-latest resolution into a binder-owned path immediately after its early existing-run lookup. For an existing team/kind/idempotency key, return the stored exact v7 without calling `LatestReleased`; for a new key, resolve and persist v8 once. The handler forwards only the latest intent to this trusted binder path.
>
> **Alternative:** Keep handler resolution only after a trusted preflight lookup for an existing team/kind/idempotency run. On a replay, that preflight returns the durable v7 before any `LatestReleased` call; only a new key proceeds to handler lookup and exact binding.
>
> **Required regression for either choice:** create v7 through `latest` with idempotency key K; release v8; replay K and assert it returns v7 with resolver call count zero on replay; submit a new key and assert it resolves and persists v8 once.

**Goal:** Make reusable agent nodes declare an operator-known exact model and tested budget floor, bind an explicit `latest` selection once to immutable node identity, let callers raise that floor for one durable run, and expose the public model catalog through Fly.

**Architecture:** An operator-owned, deployment-loaded catalog distinguishes a known model ID from its current availability. Node import freezes a known exact model into the immutable node version even if it is temporarily unavailable; fresh direct-node admission checks availability immediately before allocating the durable run. A workflow `name@latest` reference or direct-run convenience is resolved **server-side** once to the highest released version and its content hash before allocation; only those exact facts persist. Unreleased node versions remain available only to an author's explicit exact-version direct run. The direct-run budget override is applied to the rendered agent leaf, so the existing canonical parameterized configuration, immutable template hash, resume path, and global daily budget reservation remain the sole durable authority.

**Tech Stack:** Go, PostgreSQL-backed Concourse ATC APIs, Go flags/Fly CLI, Helm templates, Ginkgo and Go unit tests.

## Global Constraints

- Work in the assigned implementation worktree; preserve concurrent and user changes, and never revert them.
- Do not read benchmark `case.yaml`, ground-truth, rubric, or notes material.
- Nodes are first-class direct-run units. Workflow/catalog authoring may use `name@latest`, and direct invocation may omit a version or use `latest`, but the server resolves that convenience once to the **highest released** version plus content hash before durable binding/run allocation. If no released version exists, return a bounded `node_version_not_found` error; do not fall back to an unreleased version. Persisted bindings, retries, reruns, and execution plans carry only exact facts and never auto-update. An unreleased version is runnable only when the caller supplies that positive exact version for author testing.
- An agent node must declare a nonempty exact model ID and a finite, positive `budget_slice_usd` with at most six decimal places. Active reference nodes, seeded fixtures, and acceptance checks use a tested/default floor of `$100`; observed normal runs consume roughly `$3–4`, so that floor is deliberately non-interfering. Task and `publish_snapshot` nodes do not receive model or budget requirements.
- Import requires catalog membership, not availability. A known catalog entry with `available: false` may be frozen into an immutable/releasable node version.
- Fresh direct-node admission requires the frozen model to be currently available. An idempotent request for an already allocated run returns its durable result without re-admission.
- Callers cannot override a node model. The only new caller execution control is `budget_slice_usd`; it may be omitted or supplied as a finite six-decimal value greater than or equal to the node floor. Equality is a semantically identical no-op.
- Global daily cost admission remains mechanically enforced. Helm configures a deliberately high `10000` USD daily cap so ordinary operation is not constrained; this track adds no estimated-versus-actual cost reporting or UI.
- Hermetic tests use opaque catalog fixture IDs such as `model-test-exact-v1`; never use real-looking provider model IDs. The final active reference node ID is selected from deployed `fly agent models list --json` during rollout, never guessed or copied from this plan.
- Do not duplicate the effective budget in a new database column. `agent_workflow_runs.parameterized_config` and its hash are the durable executable authority and must remain the one value used for replay and reservation.
- Use Terra for implementation and one independent Terra reviewer per task. Fix only Critical, High, or acceptance-blocking findings; at most three focused review rounds per task.
- Run focused tests during each task and broad suites once at the final checkpoint. PostgreSQL-backed suites run serially. Docker and K8s suites are not required for this contract.

---

## File and ownership map

| Area | Files | Responsibility |
|---|---|---|
| Catalog core | `agent/modelcatalog/catalog.go`, `catalog_test.go` | Immutable catalog, membership and availability checks, deterministic public records. |
| Deployment loading | `atc/atccmd/agent_node_model_catalog.go`, `*_internal_test.go`, `atc/atccmd/command.go` | Strict operator file loading and server composition. |
| Helm | `deploy/chart/values.yaml`, `templates/agent-model-catalog-configmap.yaml`, `templates/web-deployment.yaml`, `deploy/chart/tests/agent_model_catalog_test.go` | Operator values, web-only ConfigMap/mount/flag, high daily cap. |
| Node import contract | `agent/workflow/node_definition.go`, `node_definition_test.go`, `node_store.go`; `atc/db/agent_nodes_factory.go`, `agent_nodes_factory_test.go` | Agent-node model/floor validation, catalog membership, release compatibility. |
| Bind-once latest | `agent/workflow/node_reference.go`, `node_reference_test.go`, `node_store.go`, upgrade/test resolvers; `agent/workflow/workflowtest/memory_node_store.go`; `atc/db/agent_nodes_factory.go`, `agent_workflows_factory_test.go`; `agent/api/noderuns/handler.go`, `handler_test.go`; `atc/atccmd/command.go`, `atc/api/handler.go`, `atc/routes.go`, accessor/wrappa tests; `fly/commands/agent_nodes.go`, `agent_nodes_test.go`, `agent_workflows.go`, `agent_workflows_test.go` | Resolve omitted/`latest` once, **from released versions only**, before durable workflow binding or direct allocation, then retain only exact version/hash. |
| Run admission | `agent/workflowrun/types.go`, `binder.go`, `binder_test.go`, `admission_adapters_test.go`, fakes | Model availability and effective budget reconstruction before existing admission/reservation paths. |
| HTTP APIs | `agent/api/models/handler.go`, `handler_test.go`, `route_registration_test.go`; `agent/api/nodes/handler.go`, `handler_test.go`; `agent/api/noderuns/handler.go`, `handler_test.go`; `agent/api/workflowruns/handler.go` | Public model listing, import classification, strict budget request decoding, bounded errors. |
| ATC routing/auth | `atc/routes.go`, `atc/api/handler.go`, `atc/api/api_suite_test.go`, accessor/wrappa route tests | Route registration, viewer authorization, handler construction. |
| Fly | `fly/commands/agent.go`, `agent_models.go`, `agent_models_test.go`, `agent_nodes.go`, `agent_nodes_test.go` | `fly agent models list --json` and direct-node budget flag. |
| Documentation | `docs/agentic/README.md` | Operator catalog and direct-node invocation examples; no cost estimate UI. |

## Public contracts

```go
// agent/modelcatalog/catalog.go
package modelcatalog

var ErrUnknownModel = errors.New("agent model catalog: unknown model")
var ErrModelUnavailable = errors.New("agent model catalog: model unavailable")

type Entry struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
}

type Reader interface {
	List() []Entry
	RequireKnown(model string) error
	RequireAvailable(model string) error
}
```

```go
// agent/workflowrun/types.go
type NodeModelAdmitter interface {
	AdmitNodeModel(context.Context, string) error
}

type BindRequest struct {
	// existing fields unchanged
	NodeBudgetSliceUSD *float64
	// Server-derived only; never decoded from a direct-run request.
	ExpectedDefinitionContentHash string
}
```

```json
// POST /api/v1/agent/nodes/:node_name/versions/:version/runs
{
  "inputs": {"repository": "41"},
  "params": {"MINIMUM_SEVERITY": "high"},
  "budget_slice_usd": 100,
  "idempotency_key": "review-001"
}
```

`POST /api/v1/agent/nodes/:node_name/runs` accepts that same body with no version selector. It is the only direct omitted/`latest` endpoint, and the server binds it to the highest released version before calling the binder; a positive numeric version continues to use the versioned route.

```json
// GET /api/v1/agent/models
[
  {"id": "model-test-exact-v1", "available": true},
  {"id": "model-test-unavailable-v1", "available": false}
]
```

`unknown_model` is an import-time invalid node contract (`400`). A known but unavailable model imports successfully, while a fresh direct run returns `422` with `code: "model_unavailable"`. The endpoint’s strict request decoder continues to reject `model` and every other implementation override as `400 invalid_request`.

### Task 1: Operator model catalog and deployment wiring

**Files:**
- Create: `agent/modelcatalog/catalog.go`
- Create: `agent/modelcatalog/catalog_test.go`
- Create: `atc/atccmd/agent_node_model_catalog.go`
- Create: `atc/atccmd/agent_node_model_catalog_internal_test.go`
- Create: `deploy/chart/templates/agent-model-catalog-configmap.yaml`
- Create: `deploy/chart/tests/agent_model_catalog_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `deploy/chart/values.yaml`
- Modify: `deploy/chart/templates/web-deployment.yaml`

**Consumes:** Existing strict catalog-file conventions in `atc/atccmd/agent_broker_catalog.go` and web-only ConfigMap mounting conventions in `deploy/chart/templates/agent-broker-configmap.yaml`.

**Produces:** `modelcatalog.Reader`, `loadAgentNodeModelCatalog(flag.File)`, and a single catalog instance shared by node import, direct-run admission, and the later read API.

- [ ] **Step 1: Write catalog and loader RED tests**

```go
func TestCatalogSeparatesKnownFromAvailable(t *testing.T) {
	catalog, err := modelcatalog.New([]modelcatalog.Entry{
		{ID: "model-test-unavailable-v1", Available: false},
		{ID: "model-test-exact-v1", Available: true},
	})
	if err != nil { t.Fatal(err) }
	if err := catalog.RequireKnown("model-test-unavailable-v1"); err != nil { t.Fatal(err) }
	if !errors.Is(catalog.RequireAvailable("model-test-unavailable-v1"), modelcatalog.ErrModelUnavailable) { t.Fatal("want unavailable") }
	if !errors.Is(catalog.RequireKnown("model-test-unknown-v1"), modelcatalog.ErrUnknownModel) { t.Fatal("want unknown") }
}
```

Add loader cases for an empty flag path (empty catalog), valid one-document JSON, duplicate IDs, unknown JSON fields, trailing JSON, and whitespace IDs.

- [ ] **Step 2: Run catalog RED tests**

Run: `go test ./agent/modelcatalog ./atc/atccmd -run 'Test(CatalogSeparatesKnownFromAvailable|LoadAgentNodeModelCatalog)' -count=1`

Expected: FAIL because `agent/modelcatalog` and `loadAgentNodeModelCatalog` do not exist.

- [ ] **Step 3: Implement the catalog and strict file loader**

```go
func (c *Catalog) RequireKnown(model string) error {
	if _, found := c.byID[model]; !found { return ErrUnknownModel }
	return nil
}

func (c *Catalog) RequireAvailable(model string) error {
	entry, found := c.byID[model]
	if !found || !entry.Available { return ErrModelUnavailable }
	return nil
}
```

Use `json.Decoder.DisallowUnknownFields`, require exactly one JSON value, clone entries into a map, and return sorted clones from `List`. Add `AgentNodeModels.Catalog flag.File` to `RunCommand`; the loader must return an empty catalog when its path is empty.

- [ ] **Step 4: Write Helm RED tests**

```go
func TestAgentModelCatalogRendersOnlyForWeb(t *testing.T) {
	objects := renderChart(t, map[string]string{
		"agentModels.enabled": "true",
		"agentModels.models[0].id": "model-test-exact-v1",
		"agentModels.models[0].available": "true",
	})
	requireWebArg(t, objects, "--agent-node-model-catalog=/run/concourse-agent-model-catalog/catalog.json")
	requireConfigMapData(t, objects, "agent-model-catalog", "catalog.json", `"model-test-exact-v1"`)
	requireNoWorkerMount(t, objects, "concourse-agent-model-catalog")
}
```

Also assert the rendered web args contain `--agent-daily-budget-usd=10000` with the default values file.

- [ ] **Step 5: Implement Helm wiring**

Add:

```yaml
agentModels:
  enabled: false
  models: []
agentBudget:
  globalDailyCapUSD: 10000
```

Render `catalog.json` only when `agentModels.enabled` is true, mount it only into the web deployment, and pass the file flag only in that branch. Always pass the high daily-cap Helm value to web. Leave the ATC command-line default of `0` intact for non-Helm development and test invocations.

- [ ] **Step 6: Run focused GREEN tests**

Run: `go test ./agent/modelcatalog ./atc/atccmd -count=1 && go test ./deploy/chart/tests -run 'TestAgentModelCatalog|Test.*Daily.*Budget' -count=1 && helm lint deploy/chart`

Expected: PASS.

- [ ] **Step 7: Commit task 1**

```bash
git add agent/modelcatalog atc/atccmd/agent_node_model_catalog.go \
  atc/atccmd/agent_node_model_catalog_internal_test.go atc/atccmd/command.go \
  deploy/chart/values.yaml deploy/chart/templates/agent-model-catalog-configmap.yaml \
  deploy/chart/templates/web-deployment.yaml deploy/chart/tests/agent_model_catalog_test.go
git commit -m "feat(agent): add operator node model catalog"
```

### Task 2: Freeze known model and tested floor in node versions

**Files:**
- Modify: `agent/workflow/node_definition.go`
- Modify: `agent/workflow/node_definition_test.go`
- Modify: `agent/workflow/node_store.go`
- Modify: `agent/workflow/node_compile_test.go`
- Modify: `agent/workflow/seeds/code-review-node-v1/node.yaml`
- Modify: `agent/workflow/seeds/log-diagnosis-node-v1/node.yaml`
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/reusablenode/vertical_slice_test.go`
- Modify: `atc/db/agent_nodes_factory.go`
- Modify: `atc/db/agent_nodes_factory_test.go`
- Modify: `agent/workflow/workflowtest/memory_node_store.go`

**Consumes:** `modelcatalog.Reader` from task 1.

**Produces:** A catalog-aware node factory that imports only known models, immutable compatible-release checks for model/floor, and catalog-independent validation of persisted node records.

- [ ] **Step 1: Write node-contract RED tests**

```go
It("accepts a known unavailable model but rejects an unknown model", func() {
	catalog, _ := modelcatalog.New([]modelcatalog.Entry{{ID: "model-test-unavailable-v1", Available: false}})
	factory := db.NewAgentNodesFactoryWithCatalogs(dbConn, nil, catalog)
	_, err := factory.ImportManifest("review", nodeManifest("model-test-unavailable-v1", 100), "alice")
	Expect(err).NotTo(HaveOccurred())
	_, err = factory.ImportManifest("review", nodeManifest("model-test-unknown-v1", 100), "alice")
	Expect(errors.Is(err, modelcatalog.ErrUnknownModel)).To(BeTrue())
})

func TestCompatibleNodeReleaseRejectsModelOrFloorChange(t *testing.T) {
	if workflow.NodeDefinitionsStructurallyCompatible(agentNode("model-test-exact-v1", 100), agentNode("model-test-other-v1", 100)) { t.Fatal("model change is breaking") }
	if workflow.NodeDefinitionsStructurallyCompatible(agentNode("model-test-exact-v1", 100), agentNode("model-test-exact-v1", 101)) { t.Fatal("floor change is breaking") }
}

func TestReferenceNodeFixturesUseOpaqueExactModelAndHighBudgetFloor(t *testing.T) {
	for _, fixture := range referenceNodeFixtures(t) {
		agent := onlyAgentLeaf(t, compileNodeFixture(t, fixture))
		if agent.Model != "model-test-exact-v1" || agent.BudgetSliceUSD != 100 { t.Fatalf("fixture=%s agent=%+v", fixture, agent) }
	}
}
```

Include node validation cases for blank model, whitespace-surrounded model, zero, negative, NaN, infinity, and seven-decimal budget values. Include task and `publish_snapshot` nodes to prove they do not require either field. Before this task's GREEN checkpoint, update both seeded reference `node.yaml` files, `seed_test.go`, `vertical_slice_test.go`, and every affected **test-only** inline manifest to use `model-test-exact-v1` with `budget_slice_usd: 100`; the seed value is an explicit temporary hermetic value, replaced with the deployed exact ID by task 6.

- [ ] **Step 2: Run node-contract RED tests**

Run: `go test ./agent/workflow ./agent/reusablenode ./atc/db -run 'Test(CompatibleNodeReleaseRejectsModelOrFloorChange|ReferenceNodeFixturesUseOpaqueExactModelAndHighBudgetFloor|.*Node.*Model|.*Node.*Budget)' -count=1`

Expected: FAIL because node validation allows blank/zero fields and the DB factory has no model catalog.

- [ ] **Step 3: Implement catalog-independent node validation and import membership**

In `CompiledNodeDefinition.validate`, inspect the one permitted leaf. For `*atc.AgentStep`, require `Model == strings.TrimSpace(Model)` and nonempty, and validate `BudgetSliceUSD` with `math.IsNaN`, `math.IsInf`, positivity, and micro-USD precision. Do not call a catalog there.

Add a constructor that takes both optional existing broker catalog and required model reader:

```go
func NewAgentNodesFactoryWithCatalogs(
	conn DbConn, brokerCatalog *broker.Catalog, models modelcatalog.Reader,
) AgentNodesFactory
```

Before returning a hash-identical stored node, call `RequireKnown` for each agent leaf. For a newly compiled node, call the same check before insertion. Wrap only source-contract failures in `workflow.InvalidDefinitionError`; preserve `modelcatalog.ErrUnknownModel` so the HTTP layer can classify it as `400 unknown_model`.

Extend `NodeDefinitionsStructurallyCompatible` to require equality of the agent leaf kind, exact `Model`, and `BudgetSliceUSD`, in addition to its current ports and parameters comparison. Keep released prior versions immutable.

- [ ] **Step 4: Update test doubles and run GREEN tests**

Keep `workflowtest.MemoryNodeStore` catalog-free for unit tests that exercise source parsing only. Add a catalog-aware test fixture where admission membership is relevant; do not make durable reads compile against the current catalog. Keep all hermetic catalog IDs opaque (`model-test-exact-v1` and `model-test-unavailable-v1`); rollout selects the active real ID only after querying deployed `fly agent models list --json`.

Run: `go test ./agent/workflow ./agent/reusablenode ./atc/db -count=1`

Expected: PASS.

- [ ] **Step 5: Commit task 2**

```bash
git add agent/workflow/node_definition.go agent/workflow/node_definition_test.go \
  agent/workflow/node_store.go agent/workflow/node_compile_test.go \
  agent/workflow/seeds/code-review-node-v1/node.yaml \
  agent/workflow/seeds/log-diagnosis-node-v1/node.yaml agent/workflow/seed_test.go \
  agent/reusablenode/vertical_slice_test.go \
  agent/workflow/workflowtest/memory_node_store.go \
  atc/db/agent_nodes_factory.go atc/db/agent_nodes_factory_test.go
git commit -m "feat(agent): freeze node model and budget contract"
```

### Task 3: Bind `latest` once to exact immutable node identity

**Files:**
- Modify: `agent/workflow/node_reference.go`
- Modify: `agent/workflow/node_reference_test.go`
- Modify: `agent/workflow/node_store.go`
- Modify: `agent/workflow/node_upgrade.go`
- Modify: `agent/workflow/node_upgrade_test.go`
- Modify: `agent/workflow/node_upgrade_budget_test.go`
- Modify: `agent/workflow/broker_profiles_test.go`
- Modify: `agent/workflow/workflowtest/memory_node_store.go`
- Modify: `atc/db/agent_nodes_factory.go`
- Modify: `atc/db/agent_nodes_factory_test.go`
- Modify: `atc/db/agent_workflows_factory.go`
- Modify: `atc/db/agent_workflows_factory_test.go`
- Modify: `agent/workflowrun/types.go`
- Modify: `agent/workflowrun/binder.go`
- Modify: `agent/workflowrun/binder_test.go`
- Modify: `agent/api/noderuns/handler.go`
- Modify: `agent/api/noderuns/handler_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/accessor/roles.go`
- Modify: `atc/api/accessor/agent_workflow_run_roles_test.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/api_auth_wrappa_test.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Modify: `atc/wrappa/agent_workflow_run_archive_test.go`
- Modify: `fly/commands/agent_nodes.go`
- Modify: `fly/commands/agent_nodes_test.go`
- Modify: `fly/commands/agent_workflows.go`
- Modify: `fly/commands/agent_workflows_test.go`

**Consumes:** Immutable node versions and content hashes from the node store.

**Produces:** Server-authoritative `latest` resolution that chooses the highest **released** node version and freezes its exact version/content hash before workflow binding or direct-run allocation. Retries and reruns use the persisted exact facts and never consult `LatestReleased`.

- [ ] **Step 1: Write bind-once RED tests**

```go
func TestWorkflowLatestReferenceFreezesExactNodeBinding(t *testing.T) {
	resolver := resolverWithReleased(node("review", 7, "hash-7"), node("review", 8, "hash-8"))
	resolver.MarkReleased(7) // v8 intentionally remains unreleased.
	compiled, bindings, err := workflow.CompileDefinitionWithNodes(manifestUsing("review@latest"), resolver)
	if err != nil { t.Fatal(err) }
	if compiled == nil || bindings[0].NodeVersion != 7 || bindings[0].NodeContentHash != "hash-7" { t.Fatalf("bindings=%+v", bindings) }
	assertPersistedWorkflowBindingStillEquals(t, bindings[0], 7, "hash-7")
}

func TestDirectLatestRunPersistsResolvedExactVersion(t *testing.T) {
	resolver := resolverWithReleased(node("review", 7, "hash-7"), node("review", 8, "hash-8"))
	resolver.MarkReleased(7)
	result, err := handler.CreateLatest(resolver, requestWithLatest())
	if err != nil { t.Fatal(err) }
	if result.Run.WorkflowVersion != 7 || result.Run.DefinitionContentHash != "hash-7" { t.Fatalf("run=%+v", result.Run) }
}
```

Add cases for (a) released v7 plus unreleased v8 selecting v7 for both workflow and direct `latest`, (b) no released version returning the bounded `node_version_not_found` error, (c) omitted direct version and literal `latest`, (d) explicit numeric v8 executing the unreleased author-test version, (e) concurrent insertion of v9 after latest resolution, (f) retry/rerun not calling `LatestReleased`, and (g) idempotency replay retaining the originally allocated version/hash. Add DB and memory-store tests proving `LatestReleased` filters `Release.ReleasedAt > 0` and orders by version descending.

Add Fly workflow-import tests proving Fly sends the authored `name@latest` source unchanged and does not list or choose a node version locally; server import is the only selector authority. Update every workflow upgrade/test resolver that implements `NodeResolver` so the new latest-released capability is explicit and no test double silently returns an unreleased `Latest` record.

- [ ] **Step 2: Run bind-once RED tests**

Run: `go test ./agent/workflow ./agent/workflowrun ./agent/api/noderuns ./fly/commands ./atc/db ./atc/api ./atc/api/accessor ./atc/wrappa -run 'Test(WorkflowLatestReferenceFreezesExactNodeBinding|DirectLatestRunPersistsResolvedExactVersion|.*LatestReleased.*|.*Latest.*Node)' -count=1`

Expected: FAIL because `parseExactNodeUse` rejects `latest`, the node resolver has no released-only latest capability, direct routes require an integer version, and no server-side selector resolution exists.

- [ ] **Step 3: Implement workflow/catalog `latest` binding**

Define a narrow trusted capability and make it part of the resolver supplied to workflow import:

```go
type LatestReleasedNodeResolver interface {
	LatestReleased(name string) (NodeDefinition, bool, error)
}

type NodeResolver interface {
	Released(name string, version int) (NodeDefinition, bool, error)
	LatestReleasedNodeResolver
}
```

Implement `LatestReleased` in the DB transaction resolver and `workflowtest.MemoryNodeStore` using only `Release.ReleasedAt > 0`, selecting the greatest version. Update all existing workflow upgrade and test resolvers to satisfy the expanded interface. Replace the integer-only parser with a typed selector that accepts either a positive exact integer or literal `latest`; reject all other aliases. In the workflow expander, resolve `latest` through `LatestReleased` exactly once, then create the existing `ResolvedNodeBinding` with the returned immutable `NodeDefinitionID`, `NodeVersion`, and `NodeContentHash`. On a missing released version, return the bounded `node_version_not_found` source/import error; never call the broad `NodeStore.Latest` fallback. Leave only the resolved leaf in the compiled definition.

The source manifest may retain its authoring spelling `name@latest`; the compiled workflow definition and durable binding must contain no `latest` token. Reimporting identical source remains idempotent and therefore retains its prior exact binding. An author intentionally rebinds by creating a new workflow definition version, not by background reconciliation.

- [ ] **Step 4: Implement direct latest convenience server-side**

Add `LatestReleasedNodes workflow.LatestReleasedNodeResolver` to `noderuns.Config`; `NewHandler` must fail closed if it is absent. Wire the ATC's DB-backed released-only resolver through `atc/atccmd/command.go` into `atc/api/handler.go`, and add a route constant/handler for `POST /api/v1/agent/nodes/:node_name/runs`. Give it the same `MemberRole`, authenticated-route and reject-archived classification as the existing exact `CreateAgentNodeRun`; cover the route, accessor, and both wrappa allowlists in their focused tests.

Its strict body initially contains the existing inputs, params, and idempotency key (Task 5 extends the shared request body with `budget_slice_usd`); it never accepts a caller model or numeric version field. The handler calls `LatestReleasedNodes.LatestReleased(name)` exactly once, maps no result to bounded `404 node_version_not_found`, and passes that returned exact `Version`, `ID` (`ExpectedWorkflowDefinitionID`), and `ContentHash` (`ExpectedDefinitionContentHash`) into the binder before allocation. The binder resolves only that exact version and rejects a changed ID/hash before it creates or idempotently matches a run. Keep the existing versioned route for explicit exact execution, including unreleased author-test versions.

Change the Fly run positional from required `int` to optional `string`, so `fly agent nodes run NAME [VERSION]` can accept omitted VERSION or literal `latest` and call only this new server endpoint; parse a positive numeric VERSION locally only to select the existing exact endpoint. Do not use `resolveDefaultNodeVersion`, list endpoints, or a local Fly cache for run selection. Update `fly/commands/agent_workflows.go` likewise: it forwards source containing `name@latest` to server import without calling `CompileDefinitionWithNodes` or `agentWorkflowNodeResolver`; the ATC DB factory is the only component that resolves the selector.

The binder persists `WorkflowVersion` and `DefinitionContentHash` from the resolved exact node. It compares those expected facts during fresh allocation, idempotency, and resume; retry/rerun begins from the existing durable run and must not re-resolve `latest`. Do not add a mutable selector column or a background updater.

- [ ] **Step 5: Run bind-once GREEN tests**

Run: `go test ./agent/workflow ./agent/workflowrun ./agent/api/noderuns ./fly/commands ./atc/db ./atc/atccmd ./atc/api ./atc/api/accessor ./atc/wrappa -count=1`

Expected: PASS.

- [ ] **Step 6: Commit task 3**

```bash
git add agent/workflow/node_reference.go agent/workflow/node_reference_test.go \
  agent/workflow/node_store.go agent/workflow/node_upgrade.go \
  agent/workflow/node_upgrade_test.go agent/workflow/node_upgrade_budget_test.go \
  agent/workflow/broker_profiles_test.go agent/workflow/workflowtest/memory_node_store.go \
  atc/db/agent_nodes_factory.go atc/db/agent_nodes_factory_test.go \
  atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go \
  agent/workflowrun/types.go agent/workflowrun/binder.go agent/workflowrun/binder_test.go \
  agent/api/noderuns/handler.go agent/api/noderuns/handler_test.go \
  atc/atccmd/command.go atc/api/handler.go atc/api/api_suite_test.go atc/routes.go atc/api/accessor \
  atc/wrappa fly/commands/agent_nodes.go fly/commands/agent_nodes_test.go \
  fly/commands/agent_workflows.go fly/commands/agent_workflows_test.go
git commit -m "feat(agent): bind latest node selections once"
```

### Task 4: Admit available models and render durable direct-run budgets

**Files:**
- Modify: `agent/workflowrun/types.go`
- Modify: `agent/workflowrun/binder.go`
- Modify: `agent/workflowrun/binder_test.go`
- Modify: `agent/workflowrun/admission_adapters.go`
- Modify: `agent/workflowrun/admission_adapters_test.go`
- Modify: `agent/workflowrun/workflowrunfakes/fake_model_credential_admitter.go` only if a regenerated fake is required by the chosen interface placement
- Modify: `atc/atccmd/command.go`

**Consumes:** Catalog reader from task 1 and exact node validation/import state from task 2.

**Produces:** `ErrModelUnavailable`, a server-only `NodeModelAdmitter`, and a budget override represented solely in the canonical direct-node config.

- [ ] **Step 1: Write binder RED tests**

```go
func TestDirectNodeRunUsesRaisedBudgetForCanonicalConfigAndAdmission(t *testing.T) {
	budget := 150.0
	result, err := binder.BindAndCreate(ctx, manualAdmission(), workflowrun.BindRequest{
		DefinitionKind: workflow.DefinitionKindNode, WorkflowName: "review", Version: intPtr(1),
		NodeBudgetSliceUSD: &budget, IdempotencyKey: "raised-budget",
	})
	if err != nil || !result.Created { t.Fatalf("result=%+v err=%v", result, err) }
	if got := onlyAgentBudget(t, savedParameterizedConfig(t)); got != 150 { t.Fatalf("budget=%v", got) }
	if got := reservedAmount(t); got != 150 { t.Fatalf("reservation=%v", got) }
}

func TestDirectNodeBudgetEqualToFloorIsIdempotentNoOp(t *testing.T) {
	floor := 100.0
	// A request with floor=100.0 produces the same canonical config as omission.
	assertSameRunForOmittedAndEqualOverride(t, floor)
}

func TestDirectNodeRunFailsBeforeAllocationWhenModelUnavailable(t *testing.T) {
	modelAdmitter.AdmitNodeModelReturns(workflowrun.ErrModelUnavailable)
	_, err := binder.BindAndCreate(ctx, manualAdmission(), exactNodeRequest("unavailable"))
	if !errors.Is(err, workflowrun.ErrModelUnavailable) { t.Fatalf("err=%v", err) }
	assertNoDurableRunOrReservation(t)
}
```

Add cases for lower budget, NaN/infinity, seven-decimal precision, override on a task node, retry/resume reconstruction of the raised value, and the same idempotency key with a different effective budget returning `ErrIdempotencyConflict`.

- [ ] **Step 2: Run binder RED tests**

Run: `go test ./agent/workflowrun -run 'TestDirectNode(RunUsesRaisedBudget|BudgetEqualToFloor|RunFailsBeforeAllocation)' -count=1`

Expected: FAIL because `NodeBudgetSliceUSD`, model admission, and durable re-rendering do not exist.

- [ ] **Step 3: Implement fresh-admission model availability**

Add `NodeModelAdmitter` as a `BinderOption`. Production composition supplies an adapter over `modelcatalog.Reader.RequireAvailable`; direct agent-node admission without this dependency fails closed. Perform this check only after the early idempotent existing-run lookup and after exact node resolution, but before `CreateWithInputs`. Map a removed or disabled current catalog entry to `ErrModelUnavailable`; do not expose catalog implementation details.

- [ ] **Step 4: Implement effective-budget derivation and replay**

Validate `NodeBudgetSliceUSD` in `validateAndClone` only for `DefinitionKindNode`. Instantiate the immutable node first, then:

```go
func applyNodeBudgetOverride(function *workflow.FunctionConfig, override *float64) error {
	agent, ok := function.Plan[0].Config.(*atc.AgentStep)
	if !ok && override != nil { return fmt.Errorf("%w: budget override requires an agent node", ErrInvalidRequest) }
	if !ok { return nil }
	if override == nil { return nil }
	if err := validateNodeBudgetUSD(*override); err != nil { return fmt.Errorf("%w: invalid node budget override", ErrInvalidRequest) }
	if *override < agent.BudgetSliceUSD { return fmt.Errorf("%w: budget override is below node floor", ErrInvalidRequest) }
	agent.BudgetSliceUSD = *override
	return nil
}
```

Use the resulting rendered config for `GlobalDailyBudgetAdmitter`. On `existing`, compare the incoming effective budget with the budget extracted from the stored `ParameterizedConfig`. On `resume`, extract that value and place it on the reconstructed `BindRequest` before exact node instantiation; the re-rendered canonical bytes must still match the durable hash. Do not add a run-table column.

- [ ] **Step 5: Run focused GREEN tests**

Run: `go test ./agent/workflowrun -count=1`

Expected: PASS.

- [ ] **Step 6: Commit task 4**

```bash
git add agent/workflowrun/types.go agent/workflowrun/binder.go \
  agent/workflowrun/binder_test.go agent/workflowrun/admission_adapters.go \
  agent/workflowrun/admission_adapters_test.go atc/atccmd/command.go
git commit -m "feat(agent): admit node models and raised budgets"
```

### Task 5: Expose model availability and direct-run budget control

**Files:**
- Create: `agent/api/models/handler.go`
- Create: `agent/api/models/handler_test.go`
- Create: `agent/api/models/route_registration_test.go`
- Create: `fly/commands/agent_models.go`
- Create: `fly/commands/agent_models_test.go`
- Modify: `agent/api/noderuns/handler.go`
- Modify: `agent/api/noderuns/handler_test.go`
- Modify: `agent/api/nodes/handler.go`
- Modify: `agent/api/nodes/handler_test.go`
- Modify: `agent/api/workflowruns/handler.go`
- Modify: `agent/api/workflowruns/handler_test.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/api/accessor/roles.go`
- Modify: `atc/api/accessor/agent_workflow_run_roles_test.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/api_auth_wrappa_test.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Modify: `atc/wrappa/agent_workflow_run_archive_test.go`
- Modify: `fly/commands/agent.go`
- Modify: `fly/commands/agent_nodes.go`
- Modify: `fly/commands/agent_nodes_test.go`

**Consumes:** `modelcatalog.Reader`, the released-only latest endpoint from task 3, and binder error taxonomy/budget derivation from task 4.

**Produces:** Authenticated `GET /api/v1/agent/models`, `fly agent models list --json`, and strict direct-run JSON/flag support for `budget_slice_usd`.

- [ ] **Step 1: Write API and CLI RED tests**

```go
func TestModelsListReturnsOnlySortedPublicAvailability(t *testing.T) {
	h := models.NewHandler(catalogWith("model-test-unavailable-v1", false, "model-test-exact-v1", true))
	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest(http.MethodGet, "/api/v1/agent/models", nil))
	if w.Code != http.StatusOK || w.Body.String() != "[{\"id\":\"model-test-exact-v1\",\"available\":true},{\"id\":\"model-test-unavailable-v1\",\"available\":false}]\n" { t.Fatal(w.Body.String()) }
}

func TestNodeRunCreateForwardsBudgetButRejectsModel(t *testing.T) {
	assertCreateBodyPasses(t, `{"budget_slice_usd":100,"idempotency_key":"x"}`)
	assertCreateBodyRejected(t, `{"model":"model-test-exact-v1","idempotency_key":"x"}`)
}
```

Add route tests for exactly one `GET /api/v1/agent/models`, viewer authorization tests, an unavailable-model `422` error envelope test, and Fly tests that `--json` emits the public array while both exact and released-only latest `nodes run --budget-slice-usd=100` calls emit only the new approved body field.

- [ ] **Step 2: Run API/CLI RED tests**

Run: `go test ./agent/api/models ./agent/api/noderuns ./agent/api/workflowruns ./fly/commands -run 'Test(ModelsList|NodeRunCreateForwardsBudget|AgentModels|AgentNodesRun)' -count=1`

Expected: FAIL because the route, command, and strict request field are absent.

- [ ] **Step 3: Implement models API, route, and access wiring**

Define `models.Handler{Catalog modelcatalog.Reader}` with `List` accepting only `GET` and no body. Register `ListAgentModels` at `/api/v1/agent/models`, wire it in `api.NewHandler`, and grant it `ViewerRole`. Add it to both auth and archived-route allowlists using the same teamless agent-route convention as `ListAgentNodes`.

In `agent/api/nodes/handler.go`, recognize `modelcatalog.ErrUnknownModel` before the existing generic store-error branch and return a `400` JSON error with `code: "unknown_model"` and a bounded message. A false `available` flag is not an import error and must not take this branch.

Add this public binder mapping:

```go
case errors.Is(err, workflowrun.ErrModelUnavailable):
	writeError(w, http.StatusUnprocessableEntity, "model_unavailable", "the node model is not currently available")
```

- [ ] **Step 4: Implement request/CLI support without model override**

Add `BudgetSliceUSD *float64 `json:"budget_slice_usd,omitempty"`` to the shared `noderuns.CreateRequest`, decode it as a JSON number once, and pass a defensive copy to `workflowrun.BindRequest.NodeBudgetSliceUSD` from both exact and released-only latest handlers. Keep the decoder’s unknown-key path unchanged, so `model` is rejected.

Add:

```go
type AgentModelsCommand struct {
	List AgentModelsListCommand `command:"list" description:"List operator-configured direct-agent models"`
}
type AgentModelsListCommand struct { Json bool `long:"json" description:"Print command result as JSON"` }
```

to `fly agent`. Add `BudgetSliceUSD *float64 `long:"budget-slice-usd" description:"Raise the node's declared agent budget floor for this run"`` to `NodesRunCommand`; do local finite/precision validation, then marshal the existing request type. Never add a model flag.

- [ ] **Step 5: Run focused GREEN tests**

Run: `go test ./agent/api/models ./agent/api/noderuns ./agent/api/workflowruns ./atc/api ./atc/api/accessor ./atc/wrappa ./fly/commands -count=1`

Expected: PASS.

- [ ] **Step 6: Commit task 5**

```bash
git add agent/api/models agent/api/nodes/handler.go agent/api/nodes/handler_test.go \
  agent/api/noderuns/handler.go agent/api/noderuns/handler_test.go \
  agent/api/workflowruns/handler.go agent/api/workflowruns/handler_test.go \
  atc/atccmd/command.go atc/routes.go atc/api atc/wrappa fly/commands
git commit -m "feat(agent): expose node model catalog and budget flag"
```

### Task 6: Documentation, rollout selection, and bounded review

**Files:**
- Modify: `docs/agentic/README.md`
- Modify: `agent/workflow/seeds/code-review-node-v1/node.yaml`
- Modify: `agent/workflow/seeds/log-diagnosis-node-v1/node.yaml`
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/reusablenode/vertical_slice_test.go`

**Consumes:** All prior task interfaces and the $100 reference-node contract established in task 2.

**Produces:** Accurate operator/runbook documentation plus two materialized active reference sources/runs using an exact ID returned by the deployed catalog, never a guessed provider or hermetic fixture ID.

- [ ] **Step 1: Write the documentation acceptance checklist**

Task 2 may use `model-test-exact-v1` only as its checked-in hermetic intermediate. Before this task's final checkpoint, materialize both active seed sources with an actual exact deployed ID and update `seed_test.go` and `vertical_slice_test.go` to compare the source model to the imported/bound node's model without hard-coding any provider ID. The document must not contain a provider-looking model literal.

Add this checked sequence, using real snapshot IDs supplied by the rollout operator:

```sh
MODEL_ID="$(fly -t home agent models list --json | jq -er '[.[] | select(.available) | .id] | first')"
: "${MODEL_ID:?no deployed available agent model}"
: "${BEFORE_SNAPSHOT_ID:?set the repository/v1 before snapshot ID}"
: "${AFTER_SNAPSHOT_ID:?set the repository/v1 after snapshot ID}"
: "${LOGS_SNAPSHOT_ID:?set the log-bundle/v1 snapshot ID}"
```

- [ ] **Step 2: Materialize both source packages and make assertions ID-agnostic**

Replace the Task 2 intermediate model in exactly both checked-in seed YAMLs with the selected `$MODEL_ID`; retain `budget_slice_usd: 100`. Use a literal-safe YAML patch (not a provider-name substitution), for example:

```sh
for seed in agent/workflow/seeds/code-review-node-v1/node.yaml agent/workflow/seeds/log-diagnosis-node-v1/node.yaml; do
  MODEL_ID="$MODEL_ID" perl -0pi -e 's/^(\s*model:\s*)model-test-exact-v1\s*$/${1}$ENV{MODEL_ID}/m' "$seed"
done
! rg -n 'model-test-' \
  agent/workflow/seeds/code-review-node-v1/node.yaml \
  agent/workflow/seeds/log-diagnosis-node-v1/node.yaml
```

Replace Task 2's opaque-literal assertion in `seed_test.go` with a helper that compiles each source and returns its nonempty model/floor. In `vertical_slice_test.go`, compare each imported definition's agent model and floor to that helper's source values, then assert the durable direct run preserves the imported definition ID/version/content hash and agent model. These checks must not contain a real provider ID or `model-test-*` expectation.

- [ ] **Step 3: Import both materialized sources, capture exact versions, and run them**

```sh
CODE_REVIEW_IMPORT="$(fly -t home agent nodes import --json agent/workflow/seeds/code-review-node-v1)"
CODE_REVIEW_VERSION="$(jq -er '.version | select(type == "number" and . > 0)' <<<"$CODE_REVIEW_IMPORT")"
LOG_DIAGNOSIS_IMPORT="$(fly -t home agent nodes import --json agent/workflow/seeds/log-diagnosis-node-v1)"
LOG_DIAGNOSIS_VERSION="$(jq -er '.version | select(type == "number" and . > 0)' <<<"$LOG_DIAGNOSIS_IMPORT")"

fly -t home agent nodes run code-review "$CODE_REVIEW_VERSION" \
  --input before="$BEFORE_SNAPSHOT_ID" --input after="$AFTER_SNAPSHOT_ID" \
  --budget-slice-usd=100 --idempotency-key=rollout-code-review-"$CODE_REVIEW_VERSION"
fly -t home agent nodes run log-diagnosis "$LOG_DIAGNOSIS_VERSION" \
  --input logs="$LOGS_SNAPSHOT_ID" \
  --budget-slice-usd=100 --idempotency-key=rollout-log-diagnosis-"$LOG_DIAGNOSIS_VERSION"
```

The log-diagnosis `deployment` input is optional. Record both JSON import responses and the two returned exact versions in the rollout evidence. Do not use a hard-coded version such as `10`; the explicit returned versions intentionally permit author testing even before the new versions are released.

State that active reference-node versions use `budget_slice_usd: 100`, model identity is chosen by the node author from the deployed available list, is frozen in the node version, and is never a run flag. State that a temporarily unavailable known model can be imported for portable/reference catalog use but a fresh run fails before execution. State that `latest` resolves only the highest released version once and persists that exact identity; it never auto-updates or falls back to an unreleased version. State that the high daily cap remains an operator Helm value and no cost estimate is shown.

- [ ] **Step 4: Run source/binding and focused GREEN verification**

Run:

```sh
! rg -n 'model-test-' \
  agent/workflow/seeds/code-review-node-v1/node.yaml \
  agent/workflow/seeds/log-diagnosis-node-v1/node.yaml
go test ./agent/reusablenode ./agent/workflow ./agent/workflowrun ./atc/db \
  ./agent/api/models ./agent/api/nodes ./agent/api/noderuns ./fly/commands -count=1
```

Expected: PASS.

- [ ] **Step 5: Run final broad checkpoint once**

Run:

```bash
pg_isready
make test-unit
make test-fly-integration
helm lint deploy/chart
git diff --check
```

Expected: PostgreSQL readiness, both repository suites, Helm lint, and diff check pass. Record one external-infrastructure failure without retrying it when the narrower package evidence is green.

- [ ] **Step 6: Conduct one independent focused review**

Review only the task range for: released-only latest selection, mutable model selection, catalog-known versus unavailable confusion, caller model override, lower-budget bypass, canonical-config/reservation mismatch, idempotency/resume drift, source/bound-model drift, secret disclosure, route authorization, and accidental workflow auto-upgrade. Fix only Critical, High, or acceptance-blocking findings. If a blocker is found, run a focused re-review; stop after three total review rounds and mark the track Human Review Required if a blocker remains.

- [ ] **Step 7: Commit task 6**

```bash
git add docs/agentic/README.md \
  agent/workflow/seeds/code-review-node-v1/node.yaml \
  agent/workflow/seeds/log-diagnosis-node-v1/node.yaml \
  agent/workflow/seed_test.go agent/reusablenode/vertical_slice_test.go
git commit -m "docs(agent): document node model and budget contract"
```

## Explicit exclusions

- No workflow node auto-upgrade, mutable persisted selector, or background catalog reconciliation is added. `name@latest` is an authoring convenience resolved once to the highest released version's exact `ResolvedNodeBinding` facts; those persisted facts remain authoritative. It never falls back to the highest imported/unreleased version.
- No caller-selected provider, model, credentials, runtime image, output schema, or skill injection surface is added.
- No database migration or duplicate budget field is added.
- No estimated-versus-actual cost endpoint, cost UI, or pricing calculation is added.
- No external provider liveness probe is added. Operator catalog availability is the admission signal; provider execution errors still terminalize through the existing runner/reconciler path.
- No Docker/Kubernetes suite, benchmark fixture, or protected semantic-rebase evidence file is modified by this track.

## Handoff checklist

- [ ] Update the active local SDD progress record with each implementation commit, focused test evidence, and review round count without touching protected evidence outside the assigned worktree.
- [ ] Record only nonblocking follow-ups in `docs/superpowers/plans/2026-07-28-agentic-foundations-semantic-rebase-deferred-items.md`.
- [ ] Before merge or rollout, verify that the deployed Helm values contain a nonempty model catalog before importing new reusable agent node versions.
