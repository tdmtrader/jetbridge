# Real PostgreSQL MCP Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking. Preserve concurrent edits and stage
> only the files owned by the active task.

**Goal:** Replace all 24 generated database-fake constructors in the MCP tools
tests with isolated PostgreSQL state while preserving the JSON-RPC behavior,
the workflow query-shape contract, and the one deliberate workflow-list fault.

**Architecture:** Register one package-local `postgresrunner.GinkgoRunner`,
then let only database-backed specs opt into a unique clone through
`useMCPToolsDB`; pure MCP metadata/protocol specs register tools with zero DB
dependencies and create no clone. Build every successful team, pipeline, job,
build, resource, scope, workflow, ledger, and pipeline-run result through real
factories and rows. Keep exactly one hand-written workflow decorator that
embeds the real store, delegates every successful operation, observes query
selection, and may return a configured `List` error.

**Tech Stack:** Go, Ginkgo v2, Gomega, Concourse `atc/db`,
`postgresrunner`, PostgreSQL advisory locks and notifications, JSON-RPC over
the in-memory `mcpserver.Server` handler.

## Global Constraints

- Use the one machine-wide PostgreSQL service at `127.0.0.1:15432`; every
  opted-in spec creates a unique database from the suite template. Do not add,
  start, stop, or reconfigure Docker, Colima, theborg, or PostgreSQL lifecycle.
- Implementation may modify only
  `atc/api/mcpserver/mcpserver_suite_test.go`,
  `atc/api/mcpserver/tools_test.go`, and this plan's completion evidence. Do not
  change production code, generated fakes, other tests, benchmarks, or corpus
  files. Do not push.
- Keep tests on `mcpserver.NewServer` and the existing in-memory HTTP helpers.
  Do not instantiate the full API server, accessor, authentication wrapper, or
  route stack; MCP endpoint authentication is a separate concern.
- The sole database-interface decorator is
  `observedAgentWorkflowsFactory`. It must embed a real
  `db.AgentWorkflowsFactory`, delegate every successful call, record only
  query selection/arguments, and support exactly one non-delegating outcome: a
  configured `List` error. It must never fabricate a successful definition,
  list, live version, or not-found result.
- No other generated or hand-written database fake/decorator may survive.
  Prove `check_resource` through the real PostgreSQL notification bus, cost
  boundaries through real rows, and list limits through real pipeline runs.
- Never hard-code database IDs, build names beyond values returned by the real
  job, run numbers, deprecated-scope IDs, row order, or sequence order. Compare
  dynamic identities to the objects that created them and use keyed
  maps/`ConsistOf` where SQL order is not the contract. Controlled timestamps
  are allowed only as parameterized duration/window fixtures.
- Register clone drop before every connection cleanup. Ginkgo cleanup must
  unlisten notification signals, close advisory-lock singleton connections,
  close the primary `db.DbConn` and its bus/listener, and only then drop the
  clone.
- Every conversion task captures a persisted RED, restores GREEN, and performs
  a sensitivity check by temporarily making one persisted expectation wrong.
  Restore all sensitivity mutations before formatting, review, or commit.
- Each incremental commit must leave the entire MCP server package green both
  serially and with the converted focus across exactly nine Ginkgo processes.

---

## File Structure

- Modify `atc/api/mcpserver/mcpserver_suite_test.go`: own the synchronized
  PostgreSQL runner, per-spec fixture, production factory composition,
  advisory-lock connection lifecycle, and server-construction dependency
  record. Do not place behavioral assertions in this file.
- Modify `atc/api/mcpserver/tools_test.go`: own persisted graph helpers,
  workflow YAML and observer/fault decorator, tool calls, and decoded response
  assertions.
- Update `docs/superpowers/plans/2026-08-07-real-postgres-mcp-tools.md` only at
  closure to record exact observed evidence.

## Exact Constructor Census

At `c033d05492`, `atc/api/mcpserver/tools_test.go` imports `atc/db/dbfakes` and
contains exactly 24 explicit constructors matching
`new\(dbfakes\.Fake[^)]*\)`:

| Generated fake | Baseline | Final | Replacement |
|---|---:|---:|---|
| `FakeTeamFactory` | 1 | 0 | `db.NewTeamFactory` over the clone |
| `FakeBuildFactory` | 1 | 0 | `db.NewBuildFactory` over the clone |
| `FakeAgentWorkflowsFactory` | 1 | 0 | real store plus the sole delegating observer/fault decorator |
| `FakeAgentCostLedgerFactory` | 1 | 0 | `db.NewAgentCostLedgerFactory` and inserted ledger rows |
| `FakePipelineRunFactory` | 1 | 0 | `db.NewPipelineRunFactory` over the clone |
| `FakeTeam` | 2 | 0 | real `main` and `other` teams |
| `FakePipeline` | 1 | 0 | `Team.SavePipeline` |
| `FakeJob` | 3 | 0 | jobs loaded from persisted pipeline configs |
| `FakeBuildForAPI` | 2 | 0 | real job builds reloaded through `BuildFactory`/`Job.Builds` |
| `FakeBuild` | 3 | 0 | real trigger, abort, and public-plan build state |
| `FakeResource` | 6 | 0 | real resources, scopes, versions, copy, and notification |
| `FakePipelineRun` | 2 | 0 | real template runs |
| **Total** | **24** | **0** | |

The required incremental ledger is exact: Task 1 `24 -> 24`, Task 2
`24 -> 5`, Task 3 `5 -> 3`, and Task 4 `3 -> 0` plus removal of the sole
`dbfakes` import.

### Task 1: Add and prove the opt-in MCP PostgreSQL fixture — 24 to 24

**Files:**
- Modify: `atc/api/mcpserver/mcpserver_suite_test.go`
- Modify: `atc/api/mcpserver/tools_test.go`

**Interfaces:**
- Produces: `mcpToolsPostgresRunner`, `mcpToolDeps`, `mcpToolsDB`,
  `useMCPToolsDB() *mcpToolsDB`, and
  `newMCPToolsServer(mcpToolDeps) *mcpserver.Server`.
- Produces: one clone-local `Main db.Team` and the exact real factories
  consumed by Tasks 2-4.

- [ ] **Step 1: Record the fake-backed baseline and exact census.**

  Run:

  ```bash
  go test ./atc/api/mcpserver -count=1
  rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/api/mcpserver/tools_test.go | sort | uniq -c
  rg -n 'atc/db/dbfakes' atc/api/mcpserver/tools_test.go
  ```

  Expected: package PASS; the type counts equal the 24-row census above; one
  `dbfakes` import is present.

- [ ] **Step 2: Write the fixture smoke assertion before its helper and capture RED.**

  Add this exact outer node to `tools_test.go`:

  ```go
  var _ = Describe("MCP tools PostgreSQL fixture", func() {
      It("reads committed state through separately constructed production factories", func() {
          fixture := useMCPToolsDB()
          loaded, found, err := db.NewTeamFactory(fixture.Conn, fixture.LockFactory).
              FindTeam(fixture.Main.Name())
          Expect(err).NotTo(HaveOccurred())
          Expect(found).To(BeTrue())
          Expect(loaded.ID()).To(Equal(fixture.Main.ID()))
          Expect(loaded.Name()).To(Equal(fixture.Main.Name()))
      })
  })
  ```

  It calls the not-yet-defined `useMCPToolsDB` and constructs a second
  `db.NewTeamFactory(fixture.Conn, fixture.LockFactory)`, finds
  `fixture.Main.Name()`, and compares the loaded ID/name with `fixture.Main`.

  Run:

  ```bash
  go test ./atc/api/mcpserver -run TestMCPServer -count=1
  ```

  Expected: compile FAIL with `undefined: useMCPToolsDB`.

- [ ] **Step 3: Implement the synchronized runner and fixture.**

  Register exactly once in `mcpserver_suite_test.go`:

  ```go
  var mcpToolsPostgresRunner postgresrunner.Runner

  var _ = postgresrunner.GinkgoRunner(&mcpToolsPostgresRunner)
  ```

  Define the dependency and fixture shapes exactly:

  ```go
  type mcpToolDeps struct {
      TeamFactory       db.TeamFactory
      BuildFactory      db.BuildFactory
      WorkflowsFactory  db.AgentWorkflowsFactory
      CostLedgerFactory db.AgentCostLedgerFactory
      PipelineRunFactory db.PipelineRunFactory
  }

  type mcpToolsDB struct {
      Conn                  db.DbConn
      LockFactory           lock.LockFactory
      ResourceConfigFactory db.ResourceConfigFactory
      Main                  db.Team
      Deps                  mcpToolDeps
  }
  ```

  `useMCPToolsDB` must perform these operations in this order:

  1. call `CreateTestDBFromTemplate` and immediately register
     `DropTestDB` cleanup;
  2. open the primary connection, register its close cleanup, and call
     `db.CleanupBaseResourceTypesCache()`;
  3. open all `[lock.FactoryCount]*sql.DB` singleton connections, build a real
     `lock.NewLockFactory`, and register one `errors.Join` close cleanup;
  4. construct `db.NewResourceConfigFactory` and a real `db.NewCheckFactory`
     using `credsfakes.FakeSecrets`,
     `credsfakes.FakeVarSourcePool`, a buffered `chan db.Build` of size 64,
     and `util.NewSequenceGenerator(1)`;
  5. create `teamFactory := db.NewTeamFactory(conn, lockFactory)` and call
     `teamFactory.CreateTeam(atc.Team{Name: atc.DefaultTeamName})`;
  6. construct the five `mcpToolDeps` factories. The workflow store needs a
     real promotion validator so live-version fixtures can use `Promote`:

     ```go
     workflowValidator := workflowrun.WorkflowTargetRenderer{
         RuntimeImage: "registry.example/agent-runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
     }
     deps := mcpToolDeps{
         TeamFactory:        teamFactory,
         BuildFactory:       db.NewBuildFactory(conn, lockFactory, 0, time.Hour),
         WorkflowsFactory:   db.NewAgentWorkflowsFactory(conn, workflowValidator),
         CostLedgerFactory:  db.NewAgentCostLedgerFactory(conn),
         PipelineRunFactory: db.NewPipelineRunFactory(
             lagertest.NewTestLogger("mcp-tools-postgres"),
             conn,
             lockFactory,
             checkFactory,
         ),
     }
     ```

  Do not clone from a suite-wide or top-level `BeforeEach`; only a spec that
  calls `useMCPToolsDB` gets a database.

  Implement `newMCPToolsServer` by registering all five supplied dependencies,
  `https://concourse.example.com`, and `1.0.0`. A zero-valued `mcpToolDeps` is
  valid only for `tools/list` and `get_info`, whose handlers do not dereference
  database dependencies.

- [ ] **Step 4: Run GREEN and prove fixture sensitivity.**

  Run:

  ```bash
  pg_isready -h 127.0.0.1 -p 15432 -U postgres
  ginkgo --focus='MCP tools PostgreSQL fixture' ./atc/api/mcpserver
  ginkgo -p --procs=9 --focus='MCP tools PostgreSQL fixture' ./atc/api/mcpserver
  ```

  Expected: PostgreSQL reports `accepting connections`; the focused fixture
  spec passes serially and across nine processes.

  Temporarily expect the loaded team name to equal `fixture.Main.Name()+"-wrong"`.
  Rerun the serial focus and require the name assertion to fail. Restore the
  correct expectation and rerun to PASS. Do not alter production code or a
  database row for this sensitivity check.

- [ ] **Step 5: Verify lifecycle, review, and commit.**

  Confirm cleanup registration yields notification cleanup added by a spec,
  then singleton closes, primary close, and clone drop. Run:

  ```bash
  gofmt -w atc/api/mcpserver/mcpserver_suite_test.go atc/api/mcpserver/tools_test.go
  go test ./atc/api/mcpserver -run '^$'
  go test ./atc/api/mcpserver -count=1
  go vet ./atc/api/mcpserver
  git diff --check
  ```

  Expected: all commands PASS and the constructor census remains exactly 24.
  Obtain independent review of runner registration, LIFO cleanup, and opt-in
  behavior, then commit only the two MCP test files:

  ```bash
  git add atc/api/mcpserver/mcpserver_suite_test.go atc/api/mcpserver/tools_test.go
  git commit -m "test(mcp): add isolated postgres fixture"
  ```

### Task 2: Persist teams, pipelines, jobs, builds, resources, and scopes — 24 to 5

**Files:**
- Modify: `atc/api/mcpserver/tools_test.go`

**Interfaces:**
- Consumes: `useMCPToolsDB`, `mcpToolsDB.Main`,
  `mcpToolsDB.ResourceConfigFactory`, `mcpToolsDB.Deps`, and
  `newMCPToolsServer`.
- Produces: persisted pipeline/job/build/resource/scope helpers used only by
  tool specs; no new database interface wrapper.

- [ ] **Step 1: Capture a persisted RED before fake teardown.**

  While the existing broad fake-backed server still compiles, save the
  default-private `my-pipeline`, invoke that old server, and require both the
  returned dynamic ID and `public` field to equal the real pipeline. The
  persisted pipeline reports `Public()==false`, while the existing fake is
  configured `Public()==true`, so RED is guaranteed even if both first-row
  sequences happen to allocate ID 1. Only after recording that behavioral RED
  should Step 3 remove the broad fake setup and rewire the context to the clone
  dependencies for GREEN.

- [ ] **Step 2: Add exact persisted helper operations.**

  Add helpers that:

  - save an initial pipeline with
    `fixture.Main.SavePipeline(atc.PipelineRef{Name: name}, config, 0, false)`;
  - reload a pipeline with
    `fixture.Main.Pipeline(atc.PipelineRef{Name: pipeline.Name()})` after every
    pause, unpause, scope, or SQL mutation;
  - load a named job/resource through the returned real pipeline and assert
    `found` plus no error;
  - create a job build with `Job.CreateBuild`, start it with a real `atc.Plan`,
    finish it through `Build.Finish`, and reload through
    `fixture.Deps.BuildFactory`;
  - normalize only `builds.start_time` and `builds.end_time` with
    `UPDATE builds SET start_time=$2, end_time=$3 WHERE id=$1`, using the
    dynamic build ID and parameterized timestamp arguments, then reload before
    checking duration;
  - attach an initial resource scope by calling
    `FindOrCreateResourceConfig(resource.Type(), resource.Source(), nil)`,
    `FindOrCreateScope(&resourceID)`, `SaveVersions`, and
    `resource.SetResourceConfigScope`, then reload;
  - deprecate the current scope by creating a resource config with a changed
    source, calling `newScope := FindOrCreateScope(&resourceID)`, and then
    `resource.SetResourceConfigScope(newScope)` before reload; derive the old
    scope ID from `DeprecatedScopes`, never from a literal.

  Use `db.SpanContext{}` and a small real version set such as `v1`, `v2`, and
  `v3`. Compare deprecated scope IDs with `ConsistOf`; timestamps may tie and
  must not be used to assume row order.

- [ ] **Step 3: Isolate pure and not-yet-converted tool registration.**

  After Step 1's behavioral RED is recorded, remove the broad root fake setup.
  Let `tools/list` and `get_info` construct
  `newMCPToolsServer(mcpToolDeps{})` without a clone. Until Tasks 3 and 4,
  create the remaining five generated constructors in their narrow lexical
  contexts only: one workflow factory, one ledger factory, one pipeline-run
  factory, and two pipeline runs. Every converted context calls
  `useMCPToolsDB`, copies `fixture.Deps`, substitutes only a not-yet-converted
  factory where required for incremental GREEN, and registers a fresh server
  before the request. The not-yet-converted pipeline-run contexts must already
  use a real team and persisted template pipeline in Task 2, but copy
  `fixture.Deps` and replace only `PipelineRunFactory` with the remaining fake;
  this removes the shared fake team/pipeline while preserving the three run
  constructors until Task 4. Rewire `list_pipelines` to those real clone
  dependencies first and rerun Step 1's assertion to GREEN before converting
  the other core contexts.

- [ ] **Step 4: Convert every team/pipeline/job/build success and absence.**

  Convert `list_pipelines`, unknown team, `get_pipeline`, pause, unpause,
  `list_jobs`, `list_builds`, `get_build`, missing build, `trigger_job`,
  `abort_build`, `list_teams`, and `get_build_plan` as follows:

  - use real absence for unknown team/build rather than a configured
    `(nil, false, nil)` result;
  - compare response IDs, names, config versions, status, owner names, and
    build URLs with values returned by the real graph;
  - pause/unpause through the tool, reload, and assert the stored `Paused`
    value;
  - for list/get build duration, start and finish a real build, set fixed
    start/end instants only through the helper SQL, and assert the decoded
    duration from the reloaded row;
  - for `trigger_job`, load the build created by the handler through the real
    job, compare the response to that dynamic build ID/name, and assert its
    persisted creator is `mcp`;
  - for `abort_build`, reload and assert `IsAborted()==true`; do not expect
    `MarkAsAborted` to synthesize terminal `aborted` status;
  - for `get_build_plan`, start a real build with
    `atc.Plan{ID: "plan-1", Task: &atc.TaskPlan{Name: "build"}}` and assert the
    decoded public plan rather than a fake call count;
  - create `other` through `TeamFactory.CreateTeam` and compare team results by
    name/ID without relying on query order.

- [ ] **Step 5: Convert every resource, notification, and scope outcome.**

  Convert `list_resources`, `check_resource`, both deprecated-scope specs, and
  both copy-version specs using the real pipeline resource graph.

  Scope-history specs must save and restore the package global and run with
  `atc.EnableGlobalResources=false`; these tests require a resource-owned scope
  so a changed config deprecates the old scope for that exact resource. The
  two-scope listing spec attaches the initial scope, installs two successive
  changed-source scopes through `SetResourceConfigScope`, and compares the two
  returned dynamic deprecated IDs with `ConsistOf`. The empty case uses a fresh
  resource with only its initial current scope.

  For `check_resource`, subscribe before the tool call:

  ```go
  channel := fmt.Sprintf("resource_scan_%d", resource.ID())
  signal, err := fixture.Conn.Bus().ListenSignal(channel)
  Expect(err).NotTo(HaveOccurred())
  DeferCleanup(func() {
      Expect(fixture.Conn.Bus().UnlistenSignal(channel, signal)).To(Succeed())
  })
  ```

  Invoke the tool, require success, and `Eventually(signal.C()).Should(Receive())`.
  The deferred unlisten must run before `fixture.Conn.Close`; no fake resource
  or listener may replace this protocol evidence.

  For copy success, seed three versions on the deprecated scope, call the tool
  with that dynamic scope ID, assert `versions_copied==3`, and query/reload the
  current scope to prove those three versions exist. For membership failure,
  create a real deprecated scope and request a positive dynamic value that is
  not in the returned scope-ID set; assert error and unchanged current-scope
  version count.

- [ ] **Step 6: Prove sensitivity, reconcile 24 to 5, review, and commit.**

  Temporarily expect one reloaded build's ID to be `build.ID()+1` and one copy
  response to report two versions instead of three. Run their focused specs
  and require both to fail for the persisted mismatch. Restore and run:

  ```bash
  ginkgo --focus='list_pipelines|get_pipeline|pause_pipeline|unpause_pipeline|list_jobs|list_builds|get_build|trigger_job|abort_build|list_resources|check_resource|list_teams|get_build_plan|deprecated_scopes|copy_resource_versions' ./atc/api/mcpserver
  ginkgo -p --procs=9 --focus='list_pipelines|get_pipeline|pause_pipeline|unpause_pipeline|list_jobs|list_builds|get_build|trigger_job|abort_build|list_resources|check_resource|list_teams|get_build_plan|deprecated_scopes|copy_resource_versions' ./atc/api/mcpserver
  go test ./atc/api/mcpserver -count=1
  go vet ./atc/api/mcpserver
  git diff --check
  rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/api/mcpserver/tools_test.go | sort | uniq -c
  ```

  Expected: all tests PASS; the only five generated constructors are one
  `FakeAgentWorkflowsFactory`, one `FakeAgentCostLedgerFactory`, one
  `FakePipelineRunFactory`, and two `FakePipelineRun`. Obtain independent
  review of state reloads, resource notification cleanup, scope membership,
  and absence of ID/order assumptions, then commit:

  ```bash
  git add atc/api/mcpserver/tools_test.go
  git commit -m "test(mcp): persist core tool state"
  ```

### Task 3: Persist workflow and cost-ledger tools — 5 to 3

**Files:**
- Modify: `atc/api/mcpserver/tools_test.go`

**Interfaces:**
- Consumes: real `mcpToolsDB.Deps.WorkflowsFactory` and
  `mcpToolsDB.Deps.CostLedgerFactory`.
- Produces: the sole `observedAgentWorkflowsFactory`, which remains through
  final acceptance as the only database-interface seam.

- [ ] **Step 1: Define the sole real-backed observer/fault decorator.**

  Define exactly one wrapper with an embedded real store:

  ```go
  type observedAgentWorkflowsFactory struct {
      db.AgentWorkflowsFactory
      listErr error

      listCalls         int
      liveVersionsCalls int
      liveNames         []string
      latestNames       []string
      getArgs           []struct {
          name    string
          version int
      }
      versionsCalls int
  }
  ```

  Implement its complete override surface as follows:

  ```go
  func (factory *observedAgentWorkflowsFactory) List() ([]workflow.Definition, error) {
      factory.listCalls++
      if factory.listErr != nil {
          return nil, factory.listErr
      }
      return factory.AgentWorkflowsFactory.List()
  }

  func (factory *observedAgentWorkflowsFactory) LiveVersions() (map[string]int, error) {
      factory.liveVersionsCalls++
      return factory.AgentWorkflowsFactory.LiveVersions()
  }

  func (factory *observedAgentWorkflowsFactory) Live(name string) (*workflow.Definition, bool, error) {
      factory.liveNames = append(factory.liveNames, name)
      return factory.AgentWorkflowsFactory.Live(name)
  }

  func (factory *observedAgentWorkflowsFactory) Latest(name string) (*workflow.Definition, bool, error) {
      factory.latestNames = append(factory.latestNames, name)
      return factory.AgentWorkflowsFactory.Latest(name)
  }

  func (factory *observedAgentWorkflowsFactory) Get(name string, version int) (*workflow.Definition, bool, error) {
      factory.getArgs = append(factory.getArgs, struct {
          name    string
          version int
      }{name: name, version: version})
      return factory.AgentWorkflowsFactory.Get(name, version)
  }

  func (factory *observedAgentWorkflowsFactory) Versions(
      ctx context.Context,
      name string,
      request workflow.VersionPageRequest,
  ) (workflow.VersionPage, error) {
      factory.versionsCalls++
      return factory.AgentWorkflowsFactory.Versions(ctx, name, request)
  }
  ```

  The buffered MCP call path is synchronous, so the observation record needs
  no cross-spec global state. Do not add `Returns`, stubs, or alternate success
  fields.

- [ ] **Step 2: Add valid workflow and ledger fixture helpers.**

  Import workflows only through `AgentWorkflowsFactory.Import`. Use this exact
  valid schema-v3 source helper, changing `prompt` for each version:

  ```go
  func mcpWorkflowYAML(name, description, prompt string) []byte {
      return []byte(fmt.Sprintf(`schema_version: 3
  name: %q
  description: %q
  signature_version: 1
  inputs: []
  outputs: []
  plan:
    - agent: work
      function_id: work
      prompt: %q
  `, name, description, prompt))
  }
  ```

  Promote through `Promote`; never insert fabricated successful workflow rows.
  Derive expected content hashes and raw YAML from the imported definitions.

  Insert costs only through `AgentCostLedgerFactory.Insert` with explicit UTC
  `OccurredAt`, user/model/step/token/turn/cost values and the mandatory valid
  `Source` (use `budget.SourceCIAgent` for these unbound rows). For time
  boundaries, seed rows before, inside, exactly at, and after `[since, until)`
  and compare decoded rollups by key rather than row position.

- [ ] **Step 3: Capture workflow and ledger RED before rewiring.**

  Import at least two workflow names: for the first, import and promote its
  only version so latest equals live; for the second, import two versions and
  promote the first so latest is newer than live. Initially call the old
  fake-backed server and require both name-keyed summaries' latest/live
  versions and content hashes to equal those persisted definitions. Insert two
  in-window ledger rows whose combined input/output/turn/cost values are
  `17/7/3/0.75` and require the old fake-backed rollup to equal two entries with
  those totals; this cannot accidentally equal the existing fake's five-entry,
  `150/30/10/2.0` result. Run the two focused specs and require failures because
  neither fake reads clone state. Rebuild both servers from real dependencies
  and rerun to GREEN.

- [ ] **Step 4: Convert all workflow outcomes without weakening query-shape coverage.**

  Convert workflow summary, configured list error, requested version, live
  default, latest fallback, unknown workflow, and unknown version specs.

  - Index summaries by workflow name; do not rely on result position.
  - For the summary path keep both persisted workflow names: one whose sole
    version is both latest and live, and one whose second/latest version is
    newer than its first/promoted live version. Require exactly one observed
    `LiveVersions` call and zero observed `Live` calls while indexing by name
    and comparing both returned summaries to their real latest/live
    definitions.
  - For explicit version require one observed `Get(name, version)` with the
    dynamic imported version and compare real YAML/metadata.
  - For live default require the promoted definition and zero observed `Get`
    calls.
  - For latest fallback leave every imported version unpromoted, require one
    observed `Latest(name)`, zero `Versions`/`Get`, and compare the real latest
    version.
  - Use ordinary real absence for unknown workflow/version.
  - For the sole injected error create the observer with
    `listErr: errors.New("boom")`; require the MCP error and one `List` call.
    No successful return override is permitted.

- [ ] **Step 5: Convert all cost rollup and validation outcomes.**

  Replace fake rows and argument call counts with real inserts and decoded
  aggregation. Verify group-by-day totals, default `group_by=day`, and an
  explicit `until` by proving the at-boundary/after rows are excluded. The
  explicit-until spec must request `group_by: "day"`; its contract is the
  half-open time window, not workflow attribution, and ordinary ledger rows
  without a durable workflow-run foreign key are intentionally excluded from
  workflow grouping. The
  invalid-group test constructs `newMCPToolsServer(mcpToolDeps{})`, sends
  `bogus`, and expects validation failure without creating a clone; the handler
  rejects it before dereferencing the nil ledger.

- [ ] **Step 6: Prove sensitivity, reconcile 5 to 3, review, and commit.**

  Temporarily expect the wrong promoted live version and include the row exactly
  at `until` in the expected total. Require both focused specs to fail, restore,
  then run:

  ```bash
  ginkgo --focus='list_agent_workflows|get_agent_workflow|agent_cost_rollup' ./atc/api/mcpserver
  ginkgo -p --procs=9 --focus='list_agent_workflows|get_agent_workflow|agent_cost_rollup' ./atc/api/mcpserver
  go test ./atc/api/mcpserver -count=1
  go vet ./atc/api/mcpserver
  git diff --check
  rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/api/mcpserver/tools_test.go | sort | uniq -c
  ```

  Expected: all tests PASS; only one `FakePipelineRunFactory` and two
  `FakePipelineRun` constructors remain. Inspect the decorator method-by-method
  to confirm successful delegation and the sole configured `List` failure.
  Obtain independent review, then commit:

  ```bash
  git add atc/api/mcpserver/tools_test.go
  git commit -m "test(mcp): persist workflow and cost tools"
  ```

### Task 4: Persist pipeline-run tools and close the conversion — 3 to 0

**Files:**
- Modify: `atc/api/mcpserver/tools_test.go`
- Verify: `atc/api/mcpserver/mcpserver_suite_test.go`
- Update completion evidence:
  `docs/superpowers/plans/2026-08-07-real-postgres-mcp-tools.md`

**Interfaces:**
- Consumes: real `mcpToolsDB.Deps.PipelineRunFactory`, real team/template
  pipelines, and `newMCPToolsServer`.
- Produces: zero generated DB-fake constructors/imports and final serial plus
  nine-process evidence.

- [ ] **Step 1: Add the production template-run helper.**

  Save a resource-free pipeline with `Template: true` and a string `branch`
  parameter defaulting to `main`. Create runs only through
  `PipelineRunFactory.CreateRun(template.ID(), params, createdBy)`. A
  resource-free template ensures initial-check enumeration cannot invoke a
  synthetic check result. Finish terminal rows through `PipelineRun.Finish`,
  then reload through `GetRun` because the original object's `completed_at`
  snapshot is not refreshed by `Finish`.

- [ ] **Step 2: Capture persisted RED and convert list behavior.**

  Create and finish a real run, initially invoke the old fake-backed server,
  and require the decoded run ID/number/params/status/creator/timestamps to
  equal the reloaded real row. Require RED because the fake result has unrelated
  identity, then rewire to the real factory and require GREEN.

  Preserve the default-limit contract without a second decorator: create 101
  real runs on one minimal template, call `list_pipeline_runs` without `limit`,
  require exactly 100 decoded rows, require the oldest run number is absent,
  and compare the decoded dynamic run-number sequence to the newest 100 in
  descending order. For the custom limit create six real runs, request five,
  and compare the decoded sequence to the newest five real run numbers in
  descending order. This order is part of the advertised “most recent first”
  contract and the production query's `number DESC`; derive every number from
  the created rows rather than literals.

- [ ] **Step 3: Convert lookup and absence behavior.**

  Call `get_pipeline_run` with the dynamic number returned by `CreateRun` and
  compare every decoded field to the reloaded row. For a missing run, derive a
  positive unused number from the largest created number, verify real
  `GetRun` reports `found=false`, then assert the MCP not-found response. Use a
  real absent pipeline for the unknown-pipeline list error. Remove the three
  remaining generated constructors and the `dbfakes` import.

- [ ] **Step 4: Prove sensitivity and the exact zero census.**

  Temporarily expect the completed run's number to be `run.Number()+1` and the
  custom-limit set to include the excluded oldest run. Require both focused
  specs to fail, restore, then run:

  ```bash
  ginkgo --focus='list_pipeline_runs|get_pipeline_run' ./atc/api/mcpserver
  ginkgo -p --procs=9 --focus='list_pipeline_runs|get_pipeline_run' ./atc/api/mcpserver
  test "$(rg -o 'new\(dbfakes\.Fake[^)]*\)' atc/api/mcpserver/tools_test.go | wc -l | tr -d ' ')" = 0
  ! rg -n 'atc/db/dbfakes' atc/api/mcpserver/tools_test.go
  ```

  Expected: focused tests PASS serially and across nine processes; both census
  commands succeed at zero.

- [ ] **Step 5: Run complete verification and lifecycle inspection.**

  Run exactly:

  ```bash
  pg_isready -h 127.0.0.1 -p 15432 -U postgres
  gofmt -w atc/api/mcpserver/mcpserver_suite_test.go atc/api/mcpserver/tools_test.go
  go test ./atc/api/mcpserver -run '^$'
  ginkgo --focus='Tools|MCP tools PostgreSQL fixture' ./atc/api/mcpserver
  go test ./atc/api/mcpserver -count=1
  ginkgo -p --procs=9 ./atc/api/mcpserver
  go vet ./atc/api/mcpserver
  git diff --check
  git status --short
  ```

  Expected: PostgreSQL accepts connections; compile-only, focused, uncached
  full package, nine-process full package, vet, and diff checks all PASS.

  Inspect and record that:

  - there is one synchronized runner and no suite-wide clone `BeforeEach`;
  - every database-backed spec calls the fixture once and every pure spec calls
    it zero times;
  - notification signals unlisten before the connection/bus closes;
  - every singleton and primary connection closes before clone drop;
  - no handler goroutine, HTTP server, response stream, or notification wait
    survives cleanup;
  - the direct `tools/list` request closes `resp.Body`, and `callToolRaw`
    closes every `tools/call` response body immediately after decoding (use a
    checked `defer` inside the helper or an equivalent exact-once close);
  - all successful database results come from persisted rows and fresh reloads;
  - the workflow observer has no fabricated successful state;
  - no ID, scope, run number, timestamp, or unordered SQL result is asserted
    from a literal;
  - no full API/auth setup, production file, generated fake, service lifecycle,
    or unrelated concurrent edit changed.

- [ ] **Step 6: Obtain final review, commit, and record evidence.**

  Obtain independent review with no unresolved Critical, Important, or Minor
  finding. Commit the final test conversion:

  ```bash
  git add atc/api/mcpserver/mcpserver_suite_test.go atc/api/mcpserver/tools_test.go
  git commit -m "test(mcp): persist pipeline run tools"
  ```

  Add exact commit IDs, constructor/import counts, RED/GREEN/sensitivity
  observations, serial spec count, nine-process spec count, vet result, and
  reviewer result under `Observed completion evidence` below. Commit only the
  plan update as `docs: record mcp tools postgres conversion`. Do not push.

## Final Acceptance

- [ ] `tools_test.go` reaches exactly 24 to 0 generated database-fake
  constructors and removes `atc/db/dbfakes`.
- [ ] The exact type census reconciles to zero, with no generated fake moved to
  another MCP test file and no successful state moved into a hand-written
  fake/decorator.
- [ ] Every ordinary team, pipeline, job, build, resource, scope, workflow,
  ledger, and pipeline-run result is persisted and asserted through real rows.
- [ ] The sole workflow decorator delegates all successful reads, observes the
  query-shape contract, and only synthesizes the configured `List` failure.
- [ ] `check_resource` is proven by a real PostgreSQL notification that is
  unlistened before clone teardown.
- [ ] Pure MCP metadata/protocol specs create no clone; database-backed specs
  use one unique clone each.
- [ ] Focused and full serial tests plus the full package across exactly nine
  Ginkgo processes pass against the one machine-wide PostgreSQL service.
- [ ] No hard-coded database identity/order, production behavior, API/auth
  scope expansion, service lifecycle change, unrelated file change, or push.
- [ ] Independent final review reports no unresolved Critical, Important, or
  Minor finding.

## Observed Completion Evidence

Record evidence here only after every Final Acceptance item is satisfied.
