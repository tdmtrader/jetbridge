# Agent-Readable Node Parameters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put every resolved reusable-node parameter into the agent's initial context as platform-bound data, while preserving the existing durable configuration, environment compatibility, and exact idempotent replay behavior.

**Architecture:** `CompiledNodeDefinition.Instantiate` already owns parameter declaration validation, default resolution, caller override resolution, and creation of the one parameterized function that is durably hashed. For agent nodes only, it will prepend one canonical JSON parameter block to the cloned `AgentStep.Context` at that same boundary; task nodes continue to receive ordinary task params. The existing renderer, durable `parameterized_config`, planner, exec, and runner then carry that context unchanged, so no second parameter channel or runtime re-resolution is introduced.

**Tech Stack:** Go, Jetbridge reusable-node compiler/binder, Concourse agent-step rendering, Go unit and binder tests.

## Global Constraints

- Work in the assigned implementation worktree; preserve concurrent and user changes, and never revert them.
- Do not read benchmark `case.yaml`, ground-truth, rubric, or notes material.
- Node parameters remain caller-supplied strings, resolved once from supplied values or immutable node defaults before durable binding.
- The parameterized configuration and its hash remain the sole durable execution authority. Retry and resume must reuse the identical context block and must not re-resolve defaults or duplicate the block.
- Agent parameters remain available as ordinary environment variables for backward compatibility. This track adds an agent-readable context projection; it does not remove or rename the existing environment surface.
- Task-node parameters remain task params and receive no agent context block. `publish_snapshot` nodes continue to reject parameters.
- Parameter values are not a secret surface: they are already persisted as literal values in `parameterized_config` and copied into the pod environment. Runtime credentials remain secret references and must never enter this block.
- Render parameters as one compact canonical JSON object with lexicographically ordered keys. Use `encoding/json`; do not hand-escape values, use Markdown fences, or interpolate values into prose.
- The block heading and explanatory sentence are platform-owned. Values are explicitly data that the node prompt may instruct the agent to apply; they are not output-schema authority and cannot override platform authority.
- Empty parameter sets leave `AgentStep.Context` byte-for-byte unchanged.
- Keep `show-run` redaction unchanged. The existing configuration hash and exact server-side rerun are sufficient durable evidence; this track does not add parameter values to public run JSON.
- Use Terra for implementation and one independent Terra reviewer per task. Fix only Critical, High, or acceptance-blocking findings; at most three focused review rounds per task.

---

## File and ownership map

| Area | Files | Responsibility |
|---|---|---|
| Parameter projection and durable bind | `agent/workflow/node_definition.go`, `agent/workflow/node_definition_test.go`, `agent/workflowrun/binder_test.go` | Resolve once, encode canonical JSON, prepend the platform context block only for agent nodes, and prove bind/resume preserve it exactly once. |
| User contract and live probe | `docs/operations/reusable-node-definitions.md`, `agent/workflow/node_compile_test.go`, `agent/workflow/testdata/node-parameter-context-probe/node.yaml`, `agent/workflow/testdata/node-parameter-context-probe/prompts/probe.md` | Document initial-context parameters and provide a non-catalog acceptance fixture whose unpredictable token must be used before any tool call. |

## Public context contract

For a node declaring `MINIMUM_SEVERITY` and `MODE`, the runner's existing `# Workflow context` section begins with this data:

```text
## Resolved node parameters

Platform-bound values for this exact run (canonical JSON data):
{"MINIMUM_SEVERITY":"medium","MODE":"strict"}
```

If the node already supplies context, append it after `\n\n---\n\n`. JSON encoding owns escaping, including newlines and `<`/`>` in values. The runner continues to prepend its existing `# Workflow context` heading; `Instantiate` must not add a second copy of that heading.

### Task 1: Project resolved parameters into agent context

**Files:**
- Modify: `agent/workflow/node_definition.go`
- Modify: `agent/workflow/node_definition_test.go`
- Modify: `agent/workflowrun/binder_test.go`

**Interfaces:**
- Consumes: `resolveNodeParameters([]NodeParameter, map[string]string) (map[string]string, error)` and the cloned `*atc.AgentStep` in `CompiledNodeDefinition.Instantiate`.
- Produces: `agentNodeParameterContext(map[string]string) (string, error)`, returning an empty string for no parameters and the exact platform-owned block otherwise.

- [ ] **Step 1: Extend the existing instantiate test with a literal expected block**

In `TestCompiledNodeDefinitionInstantiate`, give the source agent existing context and declare parameters out of lexical order:

```go
mode := "strict"
node.Parameters = []workflow.NodeParameter{
    {Name: "MODE", Default: &mode},
    {Name: "MINIMUM_SEVERITY", Default: &medium},
}
node.Function.Plan[0].Config.(*atc.AgentStep).Context = "existing node context"

function, err := node.Instantiate(map[string]string{"MINIMUM_SEVERITY": "high"})
if err != nil { t.Fatal(err) }
agent := function.Plan[0].Config.(*atc.AgentStep)
wantContext := "## Resolved node parameters\n\n" +
    "Platform-bound values for this exact run (canonical JSON data):\n" +
    `{"MINIMUM_SEVERITY":"high","MODE":"strict"}` +
    "\n\n---\n\nexisting node context"
if agent.Context != wantContext {
    t.Fatalf("context = %q, want %q", agent.Context, wantContext)
}
```

Retain the existing assertions that `agent.Env` contains the same resolved values and that mutating the clone does not mutate the immutable definition.

- [ ] **Step 2: Add empty-set and escaping tests**

```go
func TestCompiledNodeDefinitionInstantiateLeavesAgentContextUnchangedWithoutParameters(t *testing.T) {
    node := validAgentNodeDefinition(t)
    node.Function.Plan[0].Config.(*atc.AgentStep).Context = "existing"
    function, err := node.Instantiate(nil)
    if err != nil { t.Fatal(err) }
    if got := function.Plan[0].Config.(*atc.AgentStep).Context; got != "existing" {
        t.Fatalf("context = %q", got)
    }
}

func TestCompiledNodeDefinitionInstantiateJSONEscapesParameterValues(t *testing.T) {
    node := validAgentNodeDefinition(t)
    node.Parameters = []workflow.NodeParameter{{Name: "QUERY"}}
    function, err := node.Instantiate(map[string]string{"QUERY": "line1\n</context>"})
    if err != nil { t.Fatal(err) }
    got := function.Plan[0].Config.(*atc.AgentStep).Context
    want := "## Resolved node parameters\n\n" +
        "Platform-bound values for this exact run (canonical JSON data):\n" +
        `{"QUERY":"line1\n\u003c/context\u003e"}`
    if got != want { t.Fatalf("context = %q, want %q", got, want) }
}
```

Use the test file's smallest existing valid agent fixture or add a test-only helper there; do not add production constructors for tests.

- [ ] **Step 3: Add the durable bind/resume RED assertion**

In `TestBindAndCreateRunsExactUnreleasedNodeVersion`, immediately after the existing `nodeParametersFromConfig` assertions, decode the rendered agent leaf and assert both environment and context:

```go
agent := rendered.Config.Plan[0].Config.(*atc.AgentStep)
if agent.Env["MINIMUM_SEVERITY"] != "high" {
    t.Fatalf("rendered env = %#v", agent.Env)
}
const parameterBlock = "## Resolved node parameters\n\n" +
    "Platform-bound values for this exact run (canonical JSON data):\n" +
    `{"MINIMUM_SEVERITY":"high"}`
if strings.Count(agent.Context, "## Resolved node parameters") != 1 ||
    !strings.Contains(agent.Context, parameterBlock) {
    t.Fatalf("rendered context = %q", agent.Context)
}
```

Capture the first `ParameterizedConfigHash` and context bytes before resetting `rendered`. After `binder.resume`, assert the resumed render has the same hash, the same context bytes, the same environment value, and exactly one heading. Derive the literal expected JSON independently; do not call the production context helper from the test.

- [ ] **Step 4: Run RED tests**

Run:

```sh
go test ./agent/workflow -run 'TestCompiledNodeDefinitionInstantiate' -count=1
go test ./agent/workflowrun -run 'TestBindAndCreateRunsExactUnreleasedNodeVersion' -count=1
```

Expected: both commands FAIL because instantiated/rendered agent context has no parameter block. Confirm the task-parameter test remains green. These failures must be observed before editing `node_definition.go`.

- [ ] **Step 5: Implement canonical context projection**

Add a focused helper in `agent/workflow/node_definition.go`:

```go
const agentNodeParameterContextHeading = "## Resolved node parameters\n\n" +
    "Platform-bound values for this exact run (canonical JSON data):\n"

func agentNodeParameterContext(parameters map[string]string) (string, error) {
    if len(parameters) == 0 { return "", nil }
    encoded, err := json.Marshal(parameters)
    if err != nil { return "", fmt.Errorf("workflow: encode resolved node parameters: %w", err) }
    return agentNodeParameterContextHeading + string(encoded), nil
}
```

In the `*atc.AgentStep` branch, after cloning `Env`, build the block once. If existing context is nonempty, set `block + "\n\n---\n\n" + step.Context`; otherwise set only `block`. Then copy the same resolved values into `step.Env` as today. Do not change task or publication branches.

- [ ] **Step 6: Run focused GREEN tests**

Run:

```sh
go test ./agent/workflow -run 'TestCompiledNodeDefinitionInstantiate' -count=1
go test ./agent/workflow -count=1
go test ./agent/workflowrun -run 'TestBindAndCreateRunsExactUnreleasedNodeVersion|TestBindAndCreateNodeIdempotencyComparesEffectiveParameters' -count=1
go test ./agent/workflowrun -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 7: Commit task 1**

```sh
git add agent/workflow/node_definition.go agent/workflow/node_definition_test.go \
  agent/workflowrun/binder_test.go
git commit -m "feat(agent): present resolved node parameters in context"
```

### Task 2: Document the contract and build a behavioral acceptance probe

**Files:**
- Modify: `docs/operations/reusable-node-definitions.md`
- Modify: `agent/workflow/node_compile_test.go`
- Create: `agent/workflow/testdata/node-parameter-context-probe/node.yaml`
- Create: `agent/workflow/testdata/node-parameter-context-probe/prompts/probe.md`

**Interfaces:**
- Consumes: the Task 1 context block carried inside the rendered `atc.AgentStep` and the unchanged compatibility environment surface.
- Produces: the supported author-facing rule and an importable test-only node whose first provider-visible content proves an unpredictable bound token was present before tools could inspect environment.

- [ ] **Step 1: Document the node-author contract**

In `docs/operations/reusable-node-definitions.md`, immediately after the direct-run parameter example, state:

```text
For agent nodes, Jetbridge places the fully resolved parameter object in the
initial workflow-context block as canonical JSON. The same values remain in the
agent process environment for compatibility. Node prompts should consume the
context block and must not require the agent to discover ordinary parameters
with `env` or `printenv`. Parameters are persisted run configuration, not a
credential surface.
```

Do not document or expose an implementation-only environment variable; there is none.

- [ ] **Step 2: Write the acceptance-fixture RED test**

Add this test to `agent/workflow/node_compile_test.go` before creating the fixture directory:

```go
func TestNodeParameterContextProbeCompilesAsAnAtomicAgentNode(t *testing.T) {
    manifest, err := workflow.ManifestFromDir("testdata/node-parameter-context-probe")
    if err != nil { t.Fatal(err) }
    definition, err := workflow.CompileNodeDefinition(manifest)
    if err != nil { t.Fatal(err) }
    if len(definition.Parameters) != 1 ||
        definition.Parameters[0].Name != "PROBE_TOKEN" ||
        definition.Parameters[0].Default != nil {
        t.Fatalf("parameters = %#v", definition.Parameters)
    }
    agent, ok := definition.Function.Plan[0].Config.(*atc.AgentStep)
    if !ok || agent.BudgetSliceUSD != 5 || strings.TrimSpace(agent.Prompt) == "" {
        t.Fatalf("probe agent = %#v", definition.Function.Plan[0].Config)
    }
    if len(definition.Function.Inputs) != 1 ||
        definition.Function.Inputs[0].Name != "repository" ||
        definition.Function.Inputs[0].Type != snapshot.TypeRef("repository/v1") ||
        len(definition.Function.Outputs) != 1 ||
        definition.Function.Outputs[0].Name != "review" ||
        definition.Function.Outputs[0].Type != snapshot.TypeRef("review/v1") {
        t.Fatalf("ports = inputs %#v outputs %#v", definition.Function.Inputs, definition.Function.Outputs)
    }
}
```

Run:

```sh
go test ./agent/workflow -run 'TestNodeParameterContextProbeCompilesAsAnAtomicAgentNode' -count=1
```

Expected: FAIL because `testdata/node-parameter-context-probe/node.yaml` does not exist.

- [ ] **Step 3: Create the minimal probe node**

Create `agent/workflow/testdata/node-parameter-context-probe/node.yaml`:

```yaml
schema_version: 1
name: node-parameter-context-probe
description: Acceptance-only probe for platform-bound agent parameters.
inputs:
  - name: repository
    type: repository/v1
outputs:
  - name: review
    type: review/v1
parameters:
  - name: PROBE_TOKEN
step:
  agent: parameter-probe
  function_id: parameter-probe
  budget_slice_usd: 5
  prompt_file: prompts/probe.md
```

Create `prompts/probe.md` with this complete instruction:

```text
Read `PROBE_TOKEN` only from the platform's resolved-node-parameters block in
the initial workflow context. The token is unpredictable and is not written in
this frozen prompt. Before any tool call, emit exactly one text content item:
`Resolved probe token: <value>.`, replacing `<value>` with the context value.

Do not inspect repository contents or process environment. After that first
text item, use only ToolSearch and the managed output-builder tools. Describe
the `review` output, then write and validate one `review/v1` record with subject
id/input `repository`, role `primary`, `body.conclusion` `accept`, no findings,
and `body.summary` exactly `Resolved probe token: <value>.`. Do not use Bash,
Read, Task, Web, Python, Node, or a fallback filesystem record.
```

The probe is testdata, not a shipped seed or team-catalog recommendation.

- [ ] **Step 4: Run fixture GREEN tests**

```sh
go test ./agent/workflow -run 'TestNodeParameterContextProbeCompilesAsAnAtomicAgentNode' -count=1
go test ./agent/workflow -count=1
```

Expected: PASS.

- [ ] **Step 5: Verify examples and commit task 2**

Read the surrounding import/direct-run example once and confirm the new paragraph follows the `--param` invocation and does not imply that parameter values are returned by `show-run`. Read the complete probe prompt once and confirm it contains no token literal and allows no tool that can inspect environment before its first text item. Prompt behavior is verified by Task 3's consuming live agent, not by grepping prose in a unit test.

```sh
git diff --check
git add docs/operations/reusable-node-definitions.md \
  agent/workflow/node_compile_test.go \
  agent/workflow/testdata/node-parameter-context-probe
git commit -m "test(agent): add node parameter context probe"
```

### Task 3: Live acceptance with an unpredictable first-response token

**Files:**
- No production files.
- Update after acceptance: `JETBRIDGE_FIRST_USER_FINDINGS.md`

**Interfaces:**
- Consumes: an exact deployed commit containing Tasks 1-2, a newly imported
  exact version of the test-only parameter probe, repository snapshot `18`,
  and one unpredictable `PROBE_TOKEN` generated only after the node has been
  compiled and deployed.
- Produces: provider-stream proof that the immutable node version emits the
  bound token in its first actionable response, before any tool can inspect
  the process environment, followed by one validated `review/v1` output whose
  summary contains the exact same token.

- [ ] **Step 1: Run broad local verification**

```sh
go test ./agent/workflow ./agent/workflowrun ./atc/builds ./atc/exec -count=1
git diff --check
```

Expected: PASS. Run PostgreSQL-backed packages serially if the selected workflowrun tests use the shared database suite.

- [ ] **Step 2: Deploy through the normal source-bound pipeline**

Push the reviewed branch to the authorized `jetbridge` source ref, then drive the exact source through every pipeline gate in dependency order:

```sh
SOURCE_COMMIT="$(git rev-parse HEAD)"
git fetch origin jetbridge
git rebase origin/jetbridge
test "$(git rev-parse HEAD)" = "$SOURCE_COMMIT" || SOURCE_COMMIT="$(git rev-parse HEAD)"
git push origin HEAD:jetbridge

FLY=/tmp/fly-jetbridge-first-user
"$FLY" -t home check-resource -r jetbridge/jetbridge-repo
for JOB in set-self build-and-vet unit-tests k8s-runtime-tests tag-rc \
  build-agent-runner-image build-image self-upgrade verify-upgrade \
  k8s-live-tests release; do
  "$FLY" -t home trigger-job -j "jetbridge/$JOB" -w
done
```

Every watched command must exit zero. Capture the two provenance-bearing logs and assert that the runner build used the rebased commit and the release finished at that same source:

```sh
RUNNER_BUILD_ID="$("$FLY" -t home builds -j jetbridge/build-agent-runner-image | awk 'NR == 1 {print $1}')"
RELEASE_BUILD_ID="$("$FLY" -t home builds -j jetbridge/release | awk 'NR == 1 {print $1}')"
RUNNER_LOG="$(mktemp)"
RELEASE_LOG="$(mktemp)"
"$FLY" -t home watch -b "$RUNNER_BUILD_ID" | tee "$RUNNER_LOG"
"$FLY" -t home watch -b "$RELEASE_BUILD_ID" | tee "$RELEASE_LOG"
rg -F "SOURCE_COMMIT=$SOURCE_COMMIT" "$RUNNER_LOG"
rg -F "registry.home/agent-runner:$SOURCE_COMMIT" "$RUNNER_LOG"
rg -F "$SOURCE_COMMIT" "$RELEASE_LOG"
```

Finally prove the normal GitOps rollout consumed immutable images from that release. Tasks 1-2 execute in web; the runner digest is still checked because it is the node execution compatibility authority:

```sh
kubectl get applications -n argocd root concourse \
  -o custom-columns='NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status'
test "$(kubectl get application/root -n argocd -o jsonpath='{.status.sync.status}/{.status.health.status}')" = "Synced/Healthy"
test "$(kubectl get application/concourse -n argocd -o jsonpath='{.status.sync.status}/{.status.health.status}')" = "Synced/Healthy"

WEB_SOURCE="$(kubectl get deployment/concourse-web -n cicd -o jsonpath='{.metadata.annotations.concourse\.ci/source-commit}')"
WEB_IMAGE="$(kubectl get deployment/concourse-web -n cicd -o jsonpath='{.spec.template.spec.containers[?(@.name=="concourse-web")].image}')"
DAEMON_IMAGE="$(kubectl get daemonset/concourse-artifact-daemon -n cicd -o jsonpath='{.spec.template.spec.containers[?(@.name=="artifact-daemon")].image}')"
RUNNER_IMAGE="$(kubectl get deployment/concourse-web -n cicd -o json | jq -er '.spec.template.spec.containers[] | select(.name=="concourse-web") | .env[] | select(.name=="CONCOURSE_AGENT_STEP_IMAGE") | .value')"
test "$WEB_SOURCE" = "$SOURCE_COMMIT"
test "$WEB_IMAGE" = "$DAEMON_IMAGE"
printf '%s\n' "$WEB_IMAGE" "$RUNNER_IMAGE" | rg '^registry\.home/(jetbridge|agent-runner)@sha256:[a-f0-9]{64}$'
rg -F "CONCOURSE_AGENT_STEP_IMAGE=$RUNNER_IMAGE" "$RUNNER_LOG"
```

Do not mutate either workload directly.

- [ ] **Step 3: Import the probe and run its exact allocated version**

```sh
IMPORT_OUTPUT="$("$FLY" -t home agent nodes import agent/workflow/testdata/node-parameter-context-probe)"
printf '%s\n' "$IMPORT_OUTPUT"
NODE_VERSION="$(printf '%s\n' "$IMPORT_OUTPUT" | awk '$1 == "imported" && $2 == "node-parameter-context-probe" && $3 == "version" {print $4}')"
test -n "$NODE_VERSION"

PROBE_TOKEN="probe-$(openssl rand -hex 16)"
RUN_JSON="$("$FLY" -t home agent nodes run node-parameter-context-probe "$NODE_VERSION" \
  --input repository=18 \
  --param PROBE_TOKEN="$PROBE_TOKEN" \
  --idempotency-key="dogfood-parameter-context-v${NODE_VERSION}-20260803a" \
  --json)"
RUN_ID="$(printf '%s\n' "$RUN_JSON" | jq -er '.workflow_run_id')"
BUILD_ID="$(printf '%s\n' "$RUN_JSON" | jq -er '.planned_build_id')"
TRANSCRIPT="$(mktemp)"
set -o pipefail
"$FLY" -t home watch -b "$BUILD_ID" | tee "$TRANSCRIPT"
```

The random token is generated after the frozen prompt exists. Normalize the
complete provider stream, prove its initialization and successful-result
bookends occur exactly once, and assert that the first non-thinking assistant
content is the required text rather than a tool call:

```sh
PROVIDER_NDJSON="$(mktemp)"
perl -pe 's/\e\[[0-9;]*[[:alpha:]]//g' "$TRANSCRIPT" |
  jq -Rrc 'fromjson?' > "$PROVIDER_NDJSON"

jq -e -s '
  ([.[] | select(.type == "system" and .subtype == "init")] | length) == 1 and
  ([.[] | select(.type == "result" and .subtype == "success")] | length) == 1 and
  (first(.[]).type == "system" and first(.[]).subtype == "init") and
  (last(.[]).type == "result" and last(.[]).subtype == "success")
' "$PROVIDER_NDJSON"

EXPECTED_TEXT="Resolved probe token: ${PROBE_TOKEN}."
jq -e -s --arg expected "$EXPECTED_TEXT" '
  ([.[] | select(.type == "assistant") | .message.content[]? |
    select(.type == "text" or .type == "tool_use")][0]) as $first |
  $first.type == "text" and $first.text == $expected
' "$PROVIDER_NDJSON"
```

Then prove every later tool call is within the probe's closed allow-list. This
is stronger than attempting to enumerate shell or environment-discovery
spelling: no tool ran before the token was already emitted, and no arbitrary
execution or file-reading tool ran afterward.

```sh
jq -e -s '
  [.[] | select(.type == "assistant") | .message.content[]? |
   select(.type == "tool_use") | .name] as $names |
  ($names | length) >= 4 and
  all($names[];
    . == "ToolSearch" or
    . == "mcp__output-builder__describe_output" or
    . == "mcp__output-builder__write_output" or
    . == "mcp__output-builder__validate_output") and
  any($names[]; . == "ToolSearch") and
  any($names[]; . == "mcp__output-builder__describe_output") and
  any($names[]; . == "mcp__output-builder__write_output") and
  any($names[]; . == "mcp__output-builder__validate_output")
' "$PROVIDER_NDJSON"
```

Poll `show-run` to a terminal success, download the exact output snapshot, and
assert that the typed record carries the exact unpredictable value:

```sh
while :; do
  DETAIL="$("$FLY" -t home agent nodes show-run node-parameter-context-probe "$RUN_ID" --json)"
  STATUS="$(printf '%s\n' "$DETAIL" | jq -er '.status')"
  case "$STATUS" in succeeded|failed|errored|aborted) break ;; esac
  sleep 2
done
test "$STATUS" = succeeded
OUTPUT_ID="$(printf '%s\n' "$DETAIL" | jq -er '.outputs[] | select(.port=="review") | .snapshot.id')"
OUTPUT_DIGEST="$(printf '%s\n' "$DETAIL" | jq -er '.outputs[] | select(.port=="review") | .snapshot.digest')"
OUTPUT_TAR="$(mktemp)"
"$FLY" -t home agent snapshots download "$OUTPUT_ID" --to "$OUTPUT_TAR"
tar -xOf "$OUTPUT_TAR" record.json | jq -e --arg expected "$EXPECTED_TEXT" '
  .type == "review/v1" and
  .body.conclusion == "accept" and
  .body.summary == $expected and
  .body.findings == [] and
  any(.subjects[];
    .id == "repository" and .input == "repository" and .role == "primary")
'
```

The token did not exist when the prompt was frozen, and the first actionable
provider response names it before any tool use. The validated typed output
then preserves the same value. Together, these are objective evidence that the
model received the bound parameter in initial context; they do not rely on an
incomplete environment-command deny-list.

- [ ] **Step 4: Record acceptance evidence**

Update `JETBRIDGE_FIRST_USER_FINDINGS.md` with the exact run/build IDs,
definition hash, input/output snapshot IDs and digests, first-action and closed
tool-set observations, and terminal validation status. The probe is testdata
and must not be released or added to a team catalog. Release of the reference
samples remains a separate lifecycle decision.
