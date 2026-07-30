# Task 4 report — exact workflow node-reference expansion

## RED

- Added `TestCompileDefinitionWithNodesExpandsExactReleasedAgentReference`.
- Ran `go test ./agent/workflow -run 'TestNodeReference|TestCompileDefinitionWithNodes' -count=1`.
- It failed as intended because `workflow.CompileDefinitionWithNodes` did not exist.

## GREEN

- Added strict authoring-only `node` / `uses` parsing and exact released-node
  resolution before ordinary Concourse decoding.
- Expanded node leaves carry only invocation mappings and declared parameters;
  task/agent `function_id` is the stable local instance, publication rewrites
  only its single baked input and name.
- Resolved bindings are copied and sorted by local instance. The manifest and
  selected `workflow.yaml` / legacy `workflow.yml` bytes are never mutated.
- Agent mapping fields now propagate from source step, through `AgentPlan`, to
  executor mounts and registrations. Logical names remain container paths and
  declaration keys; physical names are repository and output client keys.
- Type flow, untyped parallel reads, dev-validation roots, extraction, and
  public-output annotation account for mapped agent artifacts.
- Node source rejects task/agent mappings. Mappings are exact and injective.
- Added optional node-aware DB and memory workflow-store constructors. The
  existing constructors retain ordinary workflow-only compilation behavior.
- Frozen node skill files and dev-validation profiles are carried as compiled
  authority, dedupe byte-identically, reject conflicting identities, and
  recompute combined validation provenance. Aggregate compiler limits include
  frozen skill/profile bytes.

## Verification

- `go test ./agent/workflow -run 'TestNodeReference|TestCompileDefinitionWithNodes' -count=1` — PASS.
- `go test ./agent/workflow ./atc/builds ./atc/exec -count=1` — PASS.
- `go test ./atc/db -run '^$' -count=1` — PASS.
- `git diff --check` — PASS.

## Changed files

- `agent/workflow/node_reference.go`, `node_reference_test.go`, `parse`/
  compiler/type-flow/extraction helpers, and memory store.
- `atc` step/plan/planner/executor types and DB workflow factory.

## Concern deliberately recorded

Compiled skill-bearing node sources now retain their frozen skill authority at
workflow compilation, but immutable workflow rendering/extraction still has
the inherited blanket refusal for `FunctionConfig.SkillFiles` / selected
agent skills. The required runtime materialization seam is to carry the
selected immutable per-agent files on `AgentStep`/`AgentPlan` and mount a
deterministic read-only tar artifact at the logical `skills` path. This task
does not claim skill-bearing nodes are executable until that seam is added.
