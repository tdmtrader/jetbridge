# Agentic Harvest Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove `harvest:` and ticket-owned `agent-ticket-<id>` pipeline lifecycle behavior from the active product while keeping completed historical builds safely readable as explicit retired steps.

**Architecture:** Delete the active step from the ATC configuration, planner, engine, runner image, and ticket renderer so no new definition can validate, plan, or execute a harvest. Put the sole compatibility boundary in a small `atc/legacyplan` JSON-only package: completed stored plans containing `harvest` are rewritten to a `retired_step` presentation node, while an unfinished build with such a private plan fails closed before an engine can obtain a step.

**Tech Stack:** Go 1.25, Ginkgo/Gomega, PostgreSQL-backed ATC DB tests, Counterfeiter fakes, Elm 0.19/elm-test, existing Concourse build-plan JSON API.

## Global Constraints

- Do not create, renumber, edit, or claim a database migration. Migration ownership, including `1773106124` and `1773106125`, belongs to the outcome plan.
- Do not alter applied historical migrations or their down migrations.
- `harvest:` must be absent from every active `atc.Step`, `atc.Plan`, compiler, validator, planner, engine factory, executable, image, and generated fake.
- Historical compatibility is decode-and-render only: it cannot validate, create a plan, initialize a worker, resume a build, construct `exec.Step`, push, judge, gate, mutate a ticket, or write metrics/reviews/outcomes.
- Preserve `agent/functions/gates`, `agent/functions/judge`, and `agent/functions/repositoryvalidate` unchanged as independently executable schema-v3 functions.
- Preserve historical enum decoding and database values: keep `atc/db.ContainerTypeHarvest`, `agent/budget.SourceHarvestJudge`, and existing migration SQL/check constraints. They are historical cost/container values, not active behavior.
- Do not remove generic pipeline-run completion, reopening, retention archival, workflow-run lifecycle, snapshot waits, `publish_snapshot`, generic outcomes, or generic workflow provenance.
- Do not delete the generic `RunBelongsToPipeline` and `TicketBelongsToRun` checks. Delete only ownership/lifecycle logic derived from the `agent-ticket-<id>` naming convention.
- Update generated Counterfeiter files by running the repository generator; never hand-edit `atc/engine/enginefakes/fake_core_step_factory.go` or `atc/db/dbfakes/fake_pipeline_run_factory.go`.
- Execute the numbered tasks in dependency order **1, 2, 4, 3, 5, 6, 7**.
  Remove downstream schema-v3 type switches immediately after deleting the ATC
  AST, before deleting the now-unreferenced runner package and image.

---

## File Structure

| Path | Change | Responsibility after this plan |
|---|---|---|
| `atc/legacyplan/historical.go` | Create | JSON-only detector and completed-history public-plan rewriter for retired harvest nodes. It imports neither `atc/exec`, `agent/harvest`, worker packages, nor ticket stores. |
| `atc/legacyplan/historical_test.go` | Create | Unit coverage proving nested legacy nodes become `retired_step` nodes and malformed JSON is rejected. |
| `atc/db/build.go` | Modify | Refuse unfinished private plans containing harvest; skip active private-plan decoding only for completed historical builds. |
| `atc/db/build_test.go` | Modify | Exercise the completed-versus-active private-plan boundary through `scanBuild`/build loading. |
| `atc/api/buildserver/plan.go` | Modify | Rewrite a completed stored public plan through `legacyplan.DecodeCompletedPublic` before returning it. |
| `atc/api/buildserver/plan_test.go` | Create | API contract tests for explicit `retired_step` output and rejection of active historical plans. |
| `atc/plan.go`, `atc/plan_factory.go`, `atc/public_plan.go`, `atc/public_plan_test.go` | Modify | Remove the active `HarvestPlan` arm and active public-plan projection. |
| `atc/steps.go`, `atc/step_recursor.go`, `atc/step_validator.go`, `atc/steps_test.go` | Modify | Remove `HarvestStep`, the detector, visitor method, recursor hook, and config validation. |
| `atc/builds/planner.go`, `atc/builds/planner_test.go` | Modify | Remove active harvest planning. |
| `atc/engine/builder.go`, `atc/engine/step_factory.go`, `atc/engine/builder_test.go`, `atc/engine/enginefakes/fake_core_step_factory.go` | Modify/regenerate | Remove the core factory interface arm and every executable construction path. |
| `atc/exec/harvest_step.go`, `atc/exec/harvest_step_test.go` | Delete | Remove the runnable harvest implementation and its execution tests. |
| `agent/harvest/` | Delete | Remove the former config, policy, workspace, runner, flight, gate, judge, and evidence implementation plus its tests. |
| `cmd/harvest-runner/main.go` | Delete | Remove the former pod entrypoint. |
| `deploy/agent-runner/Dockerfile` | Modify | Stop building/copying `harvest-runner`; keep `agent-runner` only. |
| `agent/workflow/typecheck.go`, `agent/workflow/typecheck_test.go`, `agent/workflow/render.go`, `agent/workflow/render_test.go`, `agent/workflow/parse_v3_test.go`, `agent/workflow/seed_test.go` | Modify | Remove branches that know the old AST type; parser tests now reject `harvest` as an unknown core step. |
| `agent/workflowrun/admission_adapters.go` and tests | Modify | Remove harvest-judge budget traversal while retaining agent budget accounting. |
| `atc/workflowprovenance/provenance.go` and tests | Modify | Remove harvest sidecar/provenance handling and leave all supported schema-v3 nodes intact. |
| `agent/dispatch/render.go`, `agent/dispatch/render_test.go` | Delete/modify | Remove terminal-harvest rendering and its conversion helpers; this plan does not replace it with a hidden effect. |
| `web/elm/src/Concourse.elm`, `web/elm/src/Build/StepTree/StepTree.elm`, `web/elm/tests/BuildStepTests.elm` | Modify | Replace active harvest decoding/tree variants with an explicit display-only `BuildStepRetired` variant. |
| `agent/pipelinearchiver/` | Delete | Remove polling archival by ticket-derived pipeline name. |
| `atc/runlifecycle/lifecycler.go`, `atc/runlifecycle/lifecycler_test.go` | Modify | Keep generic lifecycle passes only; remove terminal-ticket archival calls. |
| `atc/db/pipeline_run_factory.go`, `atc/db/pipeline_run_factory_test.go`, `atc/db/dbfakes/fake_pipeline_run_factory.go` | Modify/regenerate | Delete `RunBelongsToTicketTemplate`, `RunsForTerminalTickets`, `TemplatesForTerminalTickets`, and their SQL/tests. |
| `agent/api/tickets/handler.go`, `agent/api/tickets/handler_test.go`, `atc/api/handler.go` | Modify | Remove the HTTP `pipeline_run_id` check tied to ticket-named templates and its injection. |
| `atc/component.go`, `atc/atccmd/command.go` | Modify | Remove `ComponentAgentPipelineArchiver`, package import, and runnable registration. |

## Frozen Interfaces

The compatibility package is deliberately narrow and must be the only retained harvest-aware production API:

```go
package legacyplan

var ErrActiveHarvestPlan = errors.New("legacy plan: harvest is retired and cannot execute")

// ContainsHarvest reports whether any JSON plan-node object has a string "id"
// and an object-valued "harvest" member. Arbitrary task params/config values
// named "harvest" are not plan nodes. It produces no executable AST.
func ContainsHarvest(raw []byte) (bool, error)

// DecodeCompletedPublic rewrites only completed-build public-plan JSON.
// Every historical {"harvest":{"name":N,...}} becomes
// {"retired_step":{"kind":"harvest","name":N}}. All other fields and
// nesting are preserved. It has no execution, validation, or DB dependencies.
func DecodeCompletedPublic(raw *json.RawMessage) (*json.RawMessage, error)
```

The rendered API shape for a historical leaf is exactly:

```json
{"id":"8/harvest","retired_step":{"kind":"harvest","name":"push-branch"}}
```

Elm owns this display-only shape:

```elm
type BuildStep
    = BuildStepRetired String StepName
    | BuildStepTask StepName
    -- existing non-harvest variants remain unchanged
```

`BuildStepRetired "harvest" "push-branch"` renders `retired: harvest` and the stored name; it has no controls, logs, retry logic, or active plan decoding branch.

### Task 1: Add the inert historical-plan boundary

**Files:**

- Create: `atc/legacyplan/historical.go`
- Create: `atc/legacyplan/historical_test.go`
- Modify: `atc/db/build.go:2068-2074`
- Modify: `atc/db/build_test.go`
- Create: `atc/api/buildserver/plan_test.go`
- Modify: `atc/api/buildserver/plan.go:11-31`

**Interfaces:**

- Consumes: stored encrypted/private and stored public build-plan JSON; `db.BuildForAPI.IsRunning()` already distinguishes completed builds without widening the API interface.
- Produces: `legacyplan.ContainsHarvest([]byte) (bool, error)`, `legacyplan.DecodeCompletedPublic(*json.RawMessage) (*json.RawMessage, error)`, and `legacyplan.ErrActiveHarvestPlan`.

- [ ] **Step 1: Write failing legacy decoder tests**

```go
func TestDecodeCompletedPublicRewritesNestedHarvestOnly(t *testing.T) {
	raw := json.RawMessage(`{"id":"0","do":[{"id":"1","harvest":{"name":"push-branch","repo":"private/repo"}},{"id":"2","agent":{"name":"implement"}}]}`)
	decoded, err := legacyplan.DecodeCompletedPublic(&raw)
	if err != nil { t.Fatal(err) }
	if string(*decoded) != `{"do":[{"id":"1","retired_step":{"kind":"harvest","name":"push-branch"}},{"agent":{"name":"implement"},"id":"2"}],"id":"0"}` {
		t.Fatalf("decoded = %s", *decoded)
	}
}

func TestContainsHarvestDoesNotTreatOrdinaryStringsAsSteps(t *testing.T) {
	found, err := legacyplan.ContainsHarvest([]byte(`{"task":{"name":"harvest"}}`))
	if err != nil || found { t.Fatalf("found=%t err=%v", found, err) }
}
```

- [ ] **Step 2: Run the new package test to verify it fails**

Run: `go test ./atc/legacyplan -run 'Test(DecodeCompletedPublic|ContainsHarvest)' -count=1`

Expected: FAIL because package `atc/legacyplan` and the two exported functions do not yet exist.

- [ ] **Step 3: Implement a JSON-tree rewriter with no active-plan imports**

```go
package legacyplan

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrActiveHarvestPlan = errors.New("legacy plan: harvest is retired and cannot execute")

func ContainsHarvest(raw []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil { return false, fmt.Errorf("decode legacy plan: %w", err) }
	return containsHarvest(value), nil
}

func DecodeCompletedPublic(raw *json.RawMessage) (*json.RawMessage, error) {
	if raw == nil { return nil, nil }
	var value any
	if err := json.Unmarshal(*raw, &value); err != nil { return nil, fmt.Errorf("decode historical public plan: %w", err) }
	rewritten, err := rewrite(value)
	if err != nil { return nil, err }
	payload, err := json.Marshal(rewritten)
	if err != nil { return nil, fmt.Errorf("encode historical public plan: %w", err) }
	result := json.RawMessage(payload)
	return &result, nil
}
```

Implement `containsHarvest` and `rewrite` as recursive
`map[string]any`/`[]any` walkers. Treat a map as a harvest plan node only when
it has a string `id` and a `harvest` key. `rewrite` must remove that old key,
require its value to be an object, read only its optional string `name`, and
insert exactly `retired_step:
map[string]any{"kind":"harvest", "name":name}`. A nested task parameter or
configuration field named `harvest` must be preserved byte-semantically after
JSON re-encoding and must never trigger retirement. Never construct
`atc.Plan`, `atc.HarvestPlan`, or any `exec.Step`.

- [ ] **Step 4: Gate private-plan loading and normalize only completed API responses**

In `scanBuild`, immediately before the existing `json.Unmarshal(decryptedPlan, &b.privatePlan)`, add this complete branch:

```go
if len(decryptedPlan) > 0 {
	containsHarvest, err := legacyplan.ContainsHarvest(decryptedPlan)
	if err != nil { return err }
	if containsHarvest {
		if !b.completed { return legacyplan.ErrActiveHarvestPlan }
		b.privatePlan = atc.Plan{}
	} else if err := json.Unmarshal(decryptedPlan, &b.privatePlan); err != nil {
		return err
	}
}
```

In `GetBuildPlan`, select `plan := build.PublicPlan()` and run
`legacyplan.ContainsHarvest` first. If it is a running build with a historical
harvest node, return HTTP 409 with `legacy plan: harvest is retired and cannot
execute`; never emit the raw node. For a completed build, use
`legacyplan.DecodeCompletedPublic(plan)`. If detection or decoding fails, log
`failed-to-decode-historical-public-build-plan` and return HTTP 500. Encode the
returned `PublicBuildPlan` with the normalized plan.

- [ ] **Step 5: Add database/API boundary tests**

Add a DB test that scans a completed build with private JSON
`{"id":"h","harvest":{"name":"push"}}` and asserts `PrivatePlan() ==
atc.Plan{}`. Add a second test with the same JSON and `completed=false` that
asserts `legacyplan.ErrActiveHarvestPlan`. In
`atc/api/buildserver/plan_test.go`, fake a non-running `db.BuildForAPI` with
the historical public JSON and assert HTTP 200 contains `retired_step`; fake a
running build and assert HTTP 409 contains the retired-step error and never
returns the raw harvest projection.

- [ ] **Step 6: Run the focused boundary tests**

Run: `go test ./atc/legacyplan ./atc/api/buildserver -count=1 && ginkgo --focus='Build' ./atc/db/`

Expected: PASS. A completed record is displayable as `retired_step`; an unfinished historical private plan fails with `legacy plan: harvest is retired and cannot execute` before step construction.

- [ ] **Step 7: Commit**

```bash
git add atc/legacyplan atc/db/build.go atc/db/build_test.go atc/api/buildserver/plan.go atc/api/buildserver/plan_test.go
git commit -m "feat: decode historical harvest plans as retired"
```

### Task 2: Remove the active ATC harvest AST, validation, planning, and execution factory

**Files:**

- Modify: `atc/plan.go`, `atc/plan_factory.go`, `atc/public_plan.go`, `atc/public_plan_test.go`
- Modify: `atc/steps.go`, `atc/step_recursor.go`, `atc/step_validator.go`, `atc/steps_test.go`
- Modify: `atc/builds/planner.go`, `atc/builds/planner_test.go`
- Modify: `atc/engine/builder.go`, `atc/engine/step_factory.go`, `atc/engine/builder_test.go`
- Regenerate: `atc/engine/enginefakes/fake_core_step_factory.go`
- Delete: `atc/exec/harvest_step.go`, `atc/exec/harvest_step_test.go`

**Interfaces:**

- Consumes: active pipeline YAML and `atc.StepConfig`/`atc.Plan`.
- Produces: no `HarvestStep`, `HarvestPlan`, `VisitHarvest`, `OnHarvest`, `CoreStepFactory.HarvestStep`, or `buildHarvestStep` symbol in active packages.

- [ ] **Step 1: Write failing parser/planner/engine absence tests**

Add this table case to `atc/steps_test.go` and remove any test that constructs `atc.HarvestStep`:

```go
It("rejects retired harvest as an unknown core step", func() {
	var step atc.Step
	err := yaml.Unmarshal([]byte("harvest: push-branch\nworkspace: workspace\nrepo: example/repo\n"), &step)
	Expect(err).To(MatchError(atc.ErrNoStepConfigured))
})
```

Add an engine test that builds a normal `AgentPlan` and asserts `AgentStepCallCount() == 1`; compilation of that test must use a fake interface that has no harvest method. This makes a stale `CoreStepFactory.HarvestStep` declaration a compile-time failure rather than a behavior hidden by a negative assertion.

- [ ] **Step 2: Verify the tests fail against the active AST**

Run: `ginkgo --focus='rejects retired harvest' ./atc/ && ginkgo --focus='BuildStep' ./atc/engine/`

Expected: the parsing expectation fails because `StepPrecedence` still recognizes `harvest`; the engine fake still exposes `HarvestStep`.

- [ ] **Step 3: Delete every active AST and visitor arm**

Remove the following exact declarations and references:

```text
atc.Plan.Harvest
atc.HarvestPlan
atc.PlanFactory.NewPlan case HarvestPlan
atc.Plan.Public Harvest field and HarvestPlan.Public
atc.StepVisitor.VisitHarvest
atc.StepRecursor.OnHarvest and VisitHarvest
StepPrecedence detector {Key: "harvest"}
atc.HarvestStep and its Visit method
StepValidator.VisitHarvest
planVisitor.VisitHarvest
```

Also remove the `agent/harvest` imports made solely by `atc/plan.go` and `atc/steps.go`, and delete the two Harvest public-plan specs rather than weakening the public-plan exhaustiveness test.

- [ ] **Step 4: Delete the factory/engine route and regenerate its fake**

Remove this interface line and its complete implementation chain:

```go
HarvestStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, DelegateFactory) exec.Step
```

Delete the `if plan.Harvest != nil` branch, `buildHarvestStep`, `coreStepFactory.HarvestStep`, and all harvest-only factory fields/options/imports (`streamer`, tickets, reviews, outcomes, platform token/user resolver, and budget dependencies remain only if another surviving step actually uses them). Delete `atc/exec/harvest_step.go` and its test. Regenerate with the repository's Counterfeiter command for `atc/engine` so `FakeCoreStepFactory` exactly matches the reduced interface.

- [ ] **Step 5: Run ATC tests and ensure no active symbols remain**

Run: `ginkgo ./atc/ ./atc/builds/ ./atc/engine/ ./atc/exec/`

Expected: PASS, with no `HarvestStep` compile errors. Then run:

```bash
rg -n 'HarvestPlan|HarvestStep|VisitHarvest|OnHarvest|buildHarvestStep|CoreStepFactory\.HarvestStep' atc --glob '*.go'
```

Expected: no matches outside `atc/legacyplan` compatibility tests; `ContainerTypeHarvest` is allowed and must remain.

- [ ] **Step 6: Commit**

```bash
git add atc
git commit -m "refactor: remove active harvest step from atc"
```

### Task 3: Remove the runner, image packaging, and legacy workflow rendering

**Files:**

- Delete: `agent/harvest/evidence.go`, `agent/harvest/flight.go`, `agent/harvest/flight_test.go`, `agent/harvest/gates.go`, `agent/harvest/gates_test.go`, `agent/harvest/judge.go`, `agent/harvest/judge_test.go`, `agent/harvest/policy.go`, `agent/harvest/policy_test.go`, `agent/harvest/runner.go`, `agent/harvest/runner_test.go`, `agent/harvest/workspace.go`, `agent/harvest/workspace_test.go`
- Delete: `cmd/harvest-runner/main.go`
- Create: `deploy/agent_runner_dockerfile_test.go`
- Modify: `deploy/agent-runner/Dockerfile`

**Interfaces:**

- Consumes: the agent-runner image build after the v3 cutover has already
  deleted the legacy ticket renderer.
- Produces: an image containing `/usr/local/bin/agent-runner` only.

- [ ] **Step 1: Write failing packaging and renderer assertions**

Create `deploy/agent_runner_dockerfile_test.go` (`package deploy`) and read
`agent-runner/Dockerfile` relative to the package test working directory. Use
these exact predicates:

```go
if strings.Contains(string(dockerfile), "harvest-runner") {
	t.Fatal("agent runner image still packages harvest-runner")
}
if !strings.Contains(string(dockerfile), "go build -o /out/agent-runner ./cmd/agent-runner") {
	t.Fatal("agent runner image no longer builds agent-runner")
}
```

The v3 cutover's negative scan is the proof that no renderer remains. Do not
recreate a renderer test or substitute a `publish_snapshot` node: publishers
belong to authored schema-v3 definitions.

- [ ] **Step 2: Run the targeted tests to verify the current code fails**

Run: `go test ./deploy -run TestAgentRunnerDockerfile -count=1`

Expected: FAIL because the current Dockerfile still builds and copies
`harvest-runner`.

- [ ] **Step 3: Delete active runner code and strip image packaging**

Delete `agent/harvest/` and `cmd/harvest-runner/main.go`. In `deploy/agent-runner/Dockerfile`, retain only this build/copy pair:

```dockerfile
RUN CGO_ENABLED=0 go build -o /out/agent-runner ./cmd/agent-runner
COPY --from=build /out/agent-runner /usr/local/bin/agent-runner
```

Remove the harvest build command, the harvest copy command, and comments describing the retired step. Do not delete git, CA certificates, the Go toolchain, or the `agent-runner` entrypoint.

- [ ] **Step 4: Verify deletion and preserve extracted functions**

Run:

```bash
test ! -d agent/harvest
test ! -e cmd/harvest-runner/main.go
rg -n 'harvest-runner|github.com/concourse/concourse/agent/harvest' deploy agent cmd atc --glob '*' --glob '!deploy/agent_runner_dockerfile_test.go'
go test ./deploy -run TestAgentRunnerDockerfile -count=1
go test ./agent/functions/gates ./agent/functions/judge ./agent/functions/repositoryvalidate -count=1
```

Expected: the first two checks succeed; the search has no active runner/package imports; all three extracted-function package tests PASS.

- [ ] **Step 5: Commit**

```bash
git add -A agent/harvest cmd/harvest-runner deploy/agent-runner/Dockerfile deploy/agent_runner_dockerfile_test.go
git commit -m "refactor: retire harvest runner and packaging"
```

### Task 4: Remove harvest awareness from schema-v3 validation, budgets, and provenance

**Files:**

- Modify: `agent/workflow/typecheck.go`, `agent/workflow/typecheck_test.go`
- Modify: `agent/workflow/render.go`, `agent/workflow/render_test.go`, `agent/workflow/parse_v3_test.go`, `agent/workflow/seed_test.go`
- Modify: `agent/workflowrun/admission_adapters.go` and its tests
- Modify: `atc/workflowprovenance/provenance.go` and its tests
- Preserve unchanged: `agent/functions/gates/`, `agent/functions/judge/`, `agent/functions/repositoryvalidate/`

**Interfaces:**

- Consumes: schema-v3 YAML decoded through `atc.Step` and compiled `atc.Config`.
- Produces: strict rejection of a YAML `harvest` key as an unknown step, agent-only budget traversal, and provenance that never accepts a harvest plan.

- [ ] **Step 1: Replace typed-harvest tests with source-level rejection tests**

Remove tests that construct `&atc.HarvestStep{}`. Add this parse assertion:

```go
func TestParseCompiledRejectsRetiredHarvest(t *testing.T) {
	_, err := workflow.ParseCompiled([]byte(v3WithPlan("\n  - harvest: publish\n    workspace: workspace\n    repo: example/repo\n")))
	if err == nil || !strings.Contains(err.Error(), "no step configured") {
		t.Fatalf("error = %v, want retired harvest parse rejection", err)
	}
}
```

Remove the five `unknown harvest ...` strict-field cases from `parse_v3_test.go`; once `harvest` is not a step, field-level harvesting grammar is intentionally gone. Replace the seed recursor's `harvests` counter with an assertion that the rendered plan contains only supported visible node classes (`await_snapshot` and `publish_snapshot` remain covered).

- [ ] **Step 2: Run tests to establish the old AST coupling**

Run: `go test ./agent/workflow ./agent/workflowrun ./atc/workflowprovenance -count=1`

Expected: FAIL until references to `atc.HarvestStep`, harvest immutable-sidecar handling, and harvest budget traversal are removed.

- [ ] **Step 3: Delete every harvest-specific type switch and recursor branch**

Delete these exact cases:

```go
case *atc.HarvestStep:
	return snapshotFlow{}, fmt.Errorf("workflow: %s.harvest(%q): ...", path, config.Name)
```

from `typecheck.go`; both `*atc.HarvestStep` cases from `render.go`; the `OnHarvest` closure from `boundedWorkflowBudgetUSD`; and harvest entries in `workflowprovenance` declaration walking, image capture, and `planKind`. Keep existing error handling for `put`, `get`, `run`, `set_pipeline`, `load_var`, and all schema-v3 snapshot nodes.

- [ ] **Step 4: Prove agent-only global budget accounting**

Add a `workflowrun` test with one `atc.AgentStep{BudgetSliceUSD: 1.25}` and assert `boundedWorkflowBudgetUSD` returns `(1.25, 1, nil)`. It must compile without an `OnHarvest` field and must continue to reject `RetryStep`/`AcrossStep` under a global cap.

- [ ] **Step 5: Run focused checks and a negative symbol scan**

Run:

```bash
go test ./agent/workflow ./agent/workflowrun ./atc/workflowprovenance -count=1
rg -n 'HarvestStep|HarvestPlan|OnHarvest|VisitHarvest|agent/harvest' agent/workflow agent/workflowrun atc/workflowprovenance --glob '*.go'
```

Expected: tests PASS and the scan returns no matches. The three `agent/functions/*` directories remain present and their APIs remain unchanged.

- [ ] **Step 6: Commit**

```bash
git add agent/workflow agent/workflowrun atc/workflowprovenance
git commit -m "refactor: remove harvest from workflow compilation"
```

### Task 5: Render retired history explicitly in Elm without an active harvest variant

**Files:**

- Modify: `web/elm/src/Concourse.elm:450-980`
- Modify: `web/elm/src/Build/StepTree/StepTree.elm:180-190,1380-1390,1455-1470`
- Modify: `web/elm/tests/BuildStepTests.elm:498-507,1105-1123`

**Interfaces:**

- Consumes: `{"retired_step":{"kind":"harvest","name":"push-branch"}}` from the completed-build API only.
- Produces: `BuildStepRetired kind name`; no `BuildStepHarvest` constructor or `decodeBuildStepHarvest` function.

- [ ] **Step 1: Write a failing decoder/render test**

```elm
test "renders a retired harvest node from completed history" <|
    given iVisitABuildWithARetiredHarvestStep
        >> then_ (iSeeText "retired: harvest")
        >> then_ (iSeeText "push-branch")
```

Construct `iVisitABuildWithARetiredHarvestStep` with `Concourse.BuildStepRetired "harvest" "push-branch"`. Delete the existing two tests that assert an active `harvest:` header.

- [ ] **Step 2: Run the Elm test to verify it fails**

Run: `cd web/elm && npx elm-test --fuzz 1 --seed 1`

Expected: FAIL because `BuildStepRetired` and its decoder/tree rendering do not exist.

- [ ] **Step 3: Replace the active variant and decoder**

Make these exact structural changes:

```elm
-- delete
| BuildStepHarvest StepName

-- add
| BuildStepRetired String StepName
```

Replace the `Json.Decode.field "harvest"` branch with `Json.Decode.field "retired_step" decodeBuildStepRetired`, where:

```elm
decodeBuildStepRetired : Json.Decode.Decoder BuildStep
decodeBuildStepRetired =
    Json.Decode.succeed BuildStepRetired
        |> andMap (Json.Decode.field "kind" Json.Decode.string)
        |> andMap (Json.Decode.field "name" Json.Decode.string)
```

In every `StepTree` case split that formerly handled `BuildStepHarvest`, use `BuildStepRetired kind name` and render `simpleHeader ("retired: " ++ kind) Nothing name`. Return no children and expose the name only for normal header selection. Keep the generic `BuildStepUnknown` fallback for truly future step types.

- [ ] **Step 4: Run Elm checks**

Run: `cd web/elm && npx elm-test --fuzz 1 --seed 1 && npx elm make src/Main.elm --output=/tmp/concourse-elm.js`

Expected: PASS. Searching `web/elm/src` and `web/elm/tests` for `BuildStepHarvest` and `decodeBuildStepHarvest` produces no results.

- [ ] **Step 5: Commit**

```bash
git add web/elm/src/Concourse.elm web/elm/src/Build/StepTree/StepTree.elm web/elm/tests/BuildStepTests.elm
git commit -m "feat: render historical harvest steps as retired"
```

### Task 6: Remove ticket-named pipeline archiving and ownership checks

**Files:**

- Delete: `agent/pipelinearchiver/archiver.go`, `agent/pipelinearchiver/archiver_test.go`
- Modify: `atc/runlifecycle/lifecycler.go`, `atc/runlifecycle/lifecycler_test.go`
- Modify: `atc/db/pipeline_run_factory.go`, `atc/db/pipeline_run_factory_test.go`
- Regenerate: `atc/db/dbfakes/fake_pipeline_run_factory.go`
- Modify: `agent/api/tickets/handler.go`, `agent/api/tickets/handler_test.go`, `agent/api/tickets/types.go`, `agent/api/tickets/types_test.go`, `atc/api/handler.go`
- Modify: `atc/component.go`, `atc/atccmd/command.go`
- Create: `fly/commands/agent_cleanup_legacy_pipelines.go`
- Create: `fly/commands/agent_cleanup_legacy_pipelines_test.go`
- Modify: `fly/commands/agent.go`

**Interfaces:**

- Consumes: generic `db.PipelineRunFactory` lifecycle methods and ticket HTTP transitions.
- Produces: a factory exposing only generic lifecycle and linkage methods; no
  method, component, handler dependency, SQL query, or runnable derived from
  `agent-ticket-<id>`.
- Produces: a one-time, main-team-only Fly cleanup command that enumerates
  exact legacy pipeline targets before optionally archiving them through the
  ordinary pipeline API.

- [ ] **Step 1: Write failing generic-lifecycle tests**

Replace the terminal-ticket lifecycle test with this preservation test:

```go
It("still finishes, reopens, and archives generic runs", func() {
	running := new(dbfakes.FakePipelineRun)
	running.CheckCompleteReturns(db.PipelineRunSucceeded, true, nil)
	expired := new(dbfakes.FakePipelineRun)
	factory.RunningRunsReturns([]db.PipelineRun{running}, nil)
	factory.RunsToArchiveReturns([]db.PipelineRun{expired}, nil)
	Expect(lifecycler.Run(context.Background())).To(Succeed())
	Expect(running.FinishCallCount()).To(Equal(1))
	Expect(expired.ArchiveCallCount()).To(Equal(1))
})
```

Remove tests for `RunBelongsToTicketTemplate`, `RunsForTerminalTickets`, and
`TemplatesForTerminalTickets`; those APIs are the removed behavior, not a
compatibility contract. Replace ticket handler transition tests with ordinary
state-transition tests and add a JSON decoding test proving
`pipeline_run_id` is no longer a writable `TransitionRequest` field.

Create Fly command tests with a fake team named `main` and a mixed pipeline
list. Assert that dry-run mode prints only exact, unarchived
`agent-ticket-[1-9][0-9]*` refs in lexical order and makes zero archive calls;
apply mode prints the same complete list before the first archive call; and a
non-main team fails before listing or mutation.

- [ ] **Step 2: Run tests to verify the old lifecycle calls remain**

Run: `ginkgo ./atc/runlifecycle/ && ginkgo --focus='PipelineRunFactory' ./atc/db/ && go test ./agent/api/tickets -count=1`

Expected: FAIL after interface/test removal because the lifecycler, handler constructor, DB factory, and generated fake still carry ticket-template methods.

- [ ] **Step 3: Delete name-derived component and SQL behavior**

Delete `agent/pipelinearchiver/`. Delete `ComponentAgentPipelineArchiver`, its `atccmd` import and registration, and all comments/configuration for the polling component. Remove the terminal-ticket portion of `Lifecycler.Run`, leaving only `RunningRuns`, `CompletedRunsWithNewActivity`, and `RunsToArchive` passes.

From `PipelineRunFactory`, delete exactly:

```text
RunBelongsToTicketTemplate(ticketID, runID int) (bool, error)
RunsForTerminalTickets() ([]PipelineRun, error)
TemplatesForTerminalTickets() ([]Pipeline, error)
terminalTicketLinkage()
```

Delete their SQL implementations and all imports used exclusively by them (`agent/api/tickets`, `atc.DefaultTeamName`, and `strings` if no other use remains). Regenerate `FakePipelineRunFactory` from its counterfeiter directive.

- [ ] **Step 4: Remove the ticket HTTP dependency, not generic linkage safeguards**

Change the ticket handler constructor to:

```go
func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}
```

Delete `RunForTicketFunc`, `Handler.runForTicket`, and the `PipelineRunID`
ownership branch in `TransitionTicket`. Remove `PipelineRunID` from the public
`TransitionRequest` entirely and pass no pipeline identity from HTTP into
`TransitionMeta`; give `TransitionRequest` a custom `UnmarshalJSON` that
returns `pipeline_run_id is server-owned` when that retired key is present,
instead of silently ignoring it. In-process dispatch remains the sole writer
of the durable workflow/pipeline link. Update `atc/api/handler.go` to call
`ticketsapi.NewHandler(ticketsStore, userNameFunc)` without
`dbPipelineRunFactory.RunBelongsToTicketTemplate`. Leave
`RunBelongsToPipeline` and `TicketBelongsToRun` in `PipelineRunFactory`; they
protect remaining generic workflow/ticket bindings and do not derive identity
from a template name.

- [ ] **Step 5: Add the one-time, scoped operational cleanup command**

Add `fly agent cleanup-legacy-pipelines` with `--apply` and
`--non-interactive` flags. It must:

1. validate the target and require `target.Team().Name() == "main"`;
2. call `ListPipelines`, select only unarchived refs whose base name matches
   `^agent-ticket-[1-9][0-9]*$`, and sort by `PipelineRef.String()`;
3. print every target before any mutation;
4. default to dry-run and print the exact rerun instruction with `--apply`;
5. with `--apply`, require confirmation unless `--non-interactive`, then call
   `ArchivePipeline` for the already printed refs only; and
6. never select `agent-workflow-*`, instance templates with another base name,
   another team, or an already archived pipeline.

- [ ] **Step 6: Verify the generic lifecycle and absence contract**

Run:

```bash
ginkgo ./atc/runlifecycle/ && ginkgo --focus='PipelineRunFactory' ./atc/db/
go test ./agent/api/tickets -count=1
go test ./fly/commands -run TestAgentCleanupLegacyPipelines -count=1
rg -n 'pipelinearchiver|ComponentAgentPipelineArchiver|RunBelongsToTicketTemplate|RunsForTerminalTickets|TemplatesForTerminalTickets' agent atc --glob '*.go'
```

Expected: all tests PASS; the search returns no production matches. Generic
run retention tests still pass. The literal `agent-ticket-` remains only in
the explicitly scoped one-time Fly command and its tests.

- [ ] **Step 7: Commit**

```bash
git add -A agent/pipelinearchiver atc/runlifecycle atc/db/pipeline_run_factory.go atc/db/pipeline_run_factory_test.go atc/db/dbfakes/fake_pipeline_run_factory.go agent/api/tickets atc/api/handler.go atc/component.go atc/atccmd/command.go fly/commands/agent_cleanup_legacy_pipelines.go fly/commands/agent_cleanup_legacy_pipelines_test.go fly/commands/agent.go
git commit -m "refactor: remove ticket pipeline lifecycle"
```

### Task 7: Repository-wide retirement audit and staged verification

**Files:**

- Modify only files identified by the scans below that still import or construct active harvest behavior.
- Do not modify: `agent/functions/gates/`, `agent/functions/judge/`, `agent/functions/repositoryvalidate/`, `atc/db/container_metadata.go`, `agent/budget/budget.go`, cost-ledger migrations, and outcome-plan-owned files.

**Interfaces:**

- Consumes: all prior commits.
- Produces: a repository with historical decode/display compatibility only and no accidentally retained executable path.

- [ ] **Step 1: Add final negative acceptance coverage**

Add a focused test that imports a normal active pipeline containing `harvest:` and asserts it is rejected at configuration parse time, plus a test that a completed stored public plan returns `retired_step`. The assertions must be:

```go
Expect(err).To(MatchError(atc.ErrNoStepConfigured))
Expect(string(responseBody)).To(ContainSubstring(`"retired_step":{"kind":"harvest","name":"push-branch"}`))
```

Add no test that invokes a `harvest` executable: such a test would recreate the retired runtime contract.

- [ ] **Step 2: Run focused red/green acceptance suites**

Run:

```bash
ginkgo ./atc/ ./atc/builds/ ./atc/engine/ ./atc/runlifecycle/
ginkgo --focus='Build|PipelineRunFactory' ./atc/db/
go test ./agent/workflow ./agent/workflowrun ./atc/workflowprovenance ./agent/api/tickets -count=1
go test ./agent/functions/gates ./agent/functions/judge ./agent/functions/repositoryvalidate -count=1
(cd web/elm && npx elm-test --fuzz 1 --seed 1)
```

Expected: PASS. PostgreSQL must be running for the DB Ginkgo suite; check it first with `pg_isready`.

- [ ] **Step 3: Run mandatory retirement scans**

Run:

```bash
rg -n 'harvest-runner|agent/harvest|HarvestStep|HarvestPlan|VisitHarvest|OnHarvest|buildHarvestStep|pipelinearchiver|ComponentAgentPipelineArchiver|RunBelongsToTicketTemplate|RunsForTerminalTickets|TemplatesForTerminalTickets' agent atc cmd deploy web fly ci-agent --glob '!vendor/**' --glob '!node_modules/**' --glob '!deploy/agent_runner_dockerfile_test.go'
rg -n 'agent-ticket-' agent atc cmd deploy web fly ci-agent --glob '!fly/commands/agent_cleanup_legacy_pipelines.go' --glob '!fly/commands/agent_cleanup_legacy_pipelines_test.go' --glob '!vendor/**' --glob '!node_modules/**'
```

Expected: no active-code matches. Review any historical migration/comment match manually; retained `ContainerTypeHarvest` and `SourceHarvestJudge` are permitted historical decoders, but they must have no route to a runner, planner, ticket mutation, or budget reservation.

- [ ] **Step 4: Run repository verification in required order**

```bash
pg_isready
make test-ci-agent
make test-fly-integration
make test-unit
make test-integration
```

Expected: PostgreSQL reports accepting connections; each command exits 0. Do not start Kubernetes tiers until the focused and standard tiers are green. Before release, run the design-required Kubernetes integration and behavioral coverage for schema-v3 snapshots, waits, output sealing, cancellation, and publication; those suites must not contain harvest scenarios.

- [ ] **Step 5: Commit the audit-only cleanup**

```bash
git add -A
git commit -m "test: verify harvest retirement boundaries"
```

## Self-Review

### Spec coverage

| Design requirement | Plan task |
|---|---|
| Delete executable harvest step, runner, packaging, config validation, planning, compiler/type-checker cases, public-plan support, Elm active step support, implicit branch/push/judge/gates/ticket mutation, and budget traversal | Tasks 2-5 |
| Preserve only inert historical decoding and prohibit validation/planning/init/resume/execution | Task 1 |
| Historical harvest node has explicit retired state | Tasks 1 and 5 |
| Keep extracted gates, judge, repository validation | Tasks 3, 4, and 7 |
| Preserve historical container/cost enums | Global Constraints and Task 7 |
| Remove pipeline archiver, component, terminal-ticket selectors, template ownership check, lifecycle scans, dashboard/CLI identity derivation | Task 6 |
| Keep generic pipeline lifecycle and generic safeguards | Task 6 |
| Allocate no migration | Global Constraints |
| Test-first focused checks, then repository verification order | Every task and Task 7 |

### Placeholder and interface review

The plan names every created, edited, deleted, and regenerated file for this slice; has concrete exported compatibility signatures and JSON/Elm contracts; and does not introduce a feature flag, fallback executor, or migration. Later tasks use the `legacyplan` names established in Task 1 and the `BuildStepRetired` variant established in Task 5.

## Execution Handoff

Execution uses subagent-driven development with a fresh implementation agent
and task review at each dependency boundary.
