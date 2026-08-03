# Output Builder Provider Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a typed-output node succeed when the managed output-builder MCP is injected by preserving platform-resolved fallback authority, negotiating the pinned Claude client’s MCP initialize request, and reporting readiness only from provider-visible evidence.

**Architecture:** The managed builder remains a convenience surface and never becomes sealing authority. ATC always projects immutable input/output authority into the main runner environment, whether or not the private builder sidecar exists. The builder negotiates newer known client initialize versions down to the single protocol version it implements, while authority-bearing tool requests stay strict. Runner preflight remains fail-fast transport validation, but `mcp.ready` is emitted only after Claude’s stream proves the managed server was connected.

**Tech Stack:** Go 1.25, Ginkgo/Gomega ATC execution tests, `net/http` JSON-RPC tests, Claude Code 2.1.212 image smoke, Concourse agent event schema.

## Global Constraints

- The platform owns snapshot types and schema digests; the agent cannot supply or override authority.
- Direct `record.json` authoring remains a supported fallback when the managed builder is not provider-visible.
- The output builder stays loopback-only and receives no sealing credential.
- Strict JSON decoding remains in force for tool calls and every authority-bearing request.
- The server implements MCP `2024-11-05`; newer known client requests negotiate to that version rather than causing initialization failure.
- No model call, token, external API, or paid execution is required by the image smoke.
- Preserve existing node input/output contracts, event wire shapes, and exact runner image pin.

---

### Task 1: Preserve fallback authority when the managed builder is present

**Files:**
- Modify: `atc/exec/agent_step.go`
- Test: `atc/exec/agent_step_test.go`
- Verify: `agent/runner/output_builder_test.go`

**Interfaces:**
- Consumes: `recordAuthorityEnv(snapshotInputBindings, map[string]snapshot.TypeRef, []string) ([]string, error)`.
- Produces: the existing `AGENT_INPUT_<PORT>_SNAPSHOT_{TYPE,DIGEST}` and `AGENT_OUTPUT_<PORT>_RECORD_{TYPE,SCHEMA}` rows in the main container environment for both builder and non-builder execution.

- [ ] **Step 1: Add the managed-builder authority regression**

Inside `Context("with typed snapshot declarations")`, add a focused case that reuses the existing `repository` input and `workspace` output, sets an absolute work root and a digest-pinned runtime image, runs the real AgentStep composition, and asserts both the sidecar marker and fallback authority:

```go
Context("with the managed output builder", func() {
    BeforeEach(func() {
        agentPlan.RuntimeImage = "registry.example/agent@sha256:" + strings.Repeat("a", 64)
        containerMetadata.WorkingDirectory = "/work"
        chosenContainer.ProcessDefs[0].Spec.Dir = "/work"
        for index := range chosenContainer.Mounts {
            chosenContainer.Mounts[index].MountPath = strings.Replace(
                chosenContainer.Mounts[index].MountPath,
                "some-artifact-root",
                "/work",
                1,
            )
        }
    })

    It("keeps sealed-record authority in the main env when the managed output builder is injected", func() {
        ok, err := step.Run(ctx, state)
        Expect(err).NotTo(HaveOccurred())
        Expect(ok).To(BeTrue())
        Expect(chosenContainer.Spec.ManagedOutputBuilder).NotTo(BeNil())
        Expect(chosenContainer.Spec.Env).To(ContainElements(
            "CONCOURSE_OUTPUT_BUILDER_MCP=1",
            "AGENT_INPUT_REPOSITORY_SNAPSHOT_TYPE=repository/v1",
            "AGENT_INPUT_REPOSITORY_SNAPSHOT_DIGEST="+inputRef.Digest.String(),
            "AGENT_OUTPUT_WORKSPACE_RECORD_TYPE=repository-change/v1",
            HavePrefix("AGENT_OUTPUT_WORKSPACE_RECORD_SCHEMA=sha256:"),
        ))
    })
})
```

Use the fixture’s actual work-root variable name rather than introducing a second root. Keep the test on the real `outputBuilderAuthority` composition path; do not stub the managed builder.

- [ ] **Step 2: Run the test and verify RED**

Run:

```sh
go test ./atc/exec -count=1 -ginkgo.focus='keeps sealed-record authority in the main env when the managed output builder is injected'
```

Expected: FAIL because `ManagedOutputBuilder` and its marker exist but one or more `AGENT_INPUT_*` / `AGENT_OUTPUT_*` authority rows are absent.

- [ ] **Step 3: Make authority projection unconditional**

In `AgentStep.Run`, retain the runtime-image guard only around builder construction. Move output-type collection and `recordAuthorityEnv` outside the `managedOutputBuilder == nil` branch:

```go
if step.plan.RuntimeImage != "" {
    managedOutputBuilder, err = outputBuilderAuthority(
        workdir,
        snapshotInputs,
        step.plan.SnapshotInputs,
        step.plan.SnapshotOutputs,
    )
    if err != nil {
        return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
    }
}

outputTypes := make(map[string]snapshot.TypeRef, len(step.plan.SnapshotOutputs))
for name, declaration := range step.plan.SnapshotOutputs {
    outputTypes[name] = declaration.Type
}
authorityEnv, err := recordAuthorityEnv(snapshotInputs, outputTypes, containerSpec.Env)
if err != nil {
    return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
}
containerSpec.Env = append(containerSpec.Env, authorityEnv...)
```

Do not add a second builder-specific authority representation. The sidecar authority document and the main-container fallback rows must derive from the same frozen declarations and bindings.

- [ ] **Step 4: Verify GREEN and downstream prompt fallback**

Run:

```sh
go test ./atc/exec -count=1 -ginkgo.focus='keeps sealed-record authority in the main env when the managed output builder is injected'
go test ./agent/runner -run TestOutputBuilderPromptRetainsSealedRecordAuthorityFallback -count=1
go test ./atc/exec ./agent/runner -count=1
```

Expected: all PASS.

- [ ] **Step 5: Commit Task 1**

```sh
git add atc/exec/agent_step.go atc/exec/agent_step_test.go
git commit -m "fix(agent): retain managed output fallback authority"
```

---

### Task 2: Negotiate Claude 2.1.212 initialize requests

**Files:**
- Modify: `agent/outputbuilder/adapters.go`
- Test: `agent/outputbuilder/adapters_test.go`
- Modify: `agent/runner/runner.go`
- Test: `agent/runner/output_builder_test.go`
- Modify: `deploy/agent-runner/smoke.sh`
- Test: `deploy/agent_runner_dockerfile_test.go`

**Interfaces:**
- Produces: `negotiateMCPProtocolVersion(requested string) (string, bool)`, returning `2024-11-05` for the known Claude-supported request versions and rejecting malformed/unknown values.
- Changes: `preflightManagedOutputBuilder` sends the pinned client’s preferred initialize version and accepts the server’s negotiated `2024-11-05` response.
- Preserves: strict `mcpRequest` envelope decoding and strict tool-call argument decoding.

- [ ] **Step 1: Add a real HTTP negotiation regression**

Add `TestMCPInitializeNegotiatesClaudeCode212` using `httptest.NewServer(NewMCPServer(builder))`. Send the pinned client-shaped request with extra non-authoritative client metadata:

```go
initialize := `{
  "jsonrpc":"2.0",
  "id":21,
  "method":"initialize",
  "params":{
    "protocolVersion":"2025-11-25",
    "capabilities":{},
    "clientInfo":{
      "name":"claude-code",
      "title":"Claude Code",
      "version":"2.1.212",
      "description":"Anthropic coding agent",
      "websiteUrl":"https://claude.com/claude-code"
    }
  }
}`
```

Assert HTTP 200, JSON-RPC success, and `result.protocolVersion == "2024-11-05"`. Then post `notifications/initialized` and `tools/list`, asserting the exact sorted tool names `describe_output`, `validate_output`, and `write_output` with object schemas.

Also keep a negative table proving `protocolVersion:"bad"`, malformed JSON, authority fields in `tools/call`, and unknown tool names remain rejected.

- [ ] **Step 2: Run the adapter test and verify RED**

Run:

```sh
go test ./agent/outputbuilder -run TestMCPInitializeNegotiatesClaudeCode212 -count=1
```

Expected: FAIL with JSON-RPC `-32602` because the current server requires exactly `2024-11-05` and recursively rejects the larger `clientInfo` object.

- [ ] **Step 3: Implement bounded protocol negotiation**

Keep one implemented server version and an explicit set of client request versions known to the pinned binary:

```go
const mcpProtocolVersion = "2024-11-05"

var compatibleMCPClientVersions = map[string]struct{}{
    "2024-11-05": {},
    "2025-03-26": {},
    "2025-06-18": {},
    "2025-11-25": {},
}

func negotiateMCPProtocolVersion(requested string) (string, bool) {
    _, known := compatibleMCPClientVersions[requested]
    return mcpProtocolVersion, known
}
```

For `initialize` only, decode `protocolVersion` with `json.Unmarshal` into a bounded struct whose `Capabilities` and `ClientInfo` fields are `json.RawMessage`. These fields carry no authority and may evolve. Reject empty or unknown requested versions, but do not recursively reject extra client-description fields. Continue using `strictJSON` for the outer JSON-RPC request and every tool call.

Return the negotiated server version in the initialize result.

- [ ] **Step 4: Make runner preflight exercise the pinned-client negotiation**

Split the requested and negotiated constants:

```go
const (
    managedOutputBuilderClientProtocolVersion = "2025-11-25"
    managedOutputBuilderProtocolVersion       = "2024-11-05"
)
```

Send `managedOutputBuilderClientProtocolVersion` in the preflight initialize request and continue requiring `managedOutputBuilderProtocolVersion` in the response. Add a runner test whose HTTP server returns `2024-11-05` for a `2025-11-25` request and then serves the three exact tools.

- [ ] **Step 5: Add the pinned Claude image smoke**

Extend `deploy/agent-runner/smoke.sh` after the binary checks. Create `/tmp/agent-output-smoke/work/review`, install a read-only platform authority at `/run/concourse/output-builder/authority.json`, start `agent-output serve`, and write this strict MCP config:

```json
{"mcpServers":{"output-builder":{"type":"http","url":"http://127.0.0.1:7783/mcp"}}}
```

The authority document must contain no inputs and one `review/v1` output rooted at `/tmp/agent-output-smoke/work/review`. Run:

```sh
claude mcp list --mcp-config /tmp/agent-output-smoke/mcp.json --strict-mcp-config
```

Require the command to exit zero and its bounded output to identify `output-builder` as connected. Use `mktemp -d`, a trap that terminates the sidecar and removes only that exact directory, and no credential/model invocation. Update the Dockerfile behavior test to run the smoke through its existing fake-command harness and assert cleanup occurs on failure.

- [ ] **Step 6: Verify GREEN**

Run:

```sh
go test ./agent/outputbuilder ./agent/runner ./deploy -count=1
```

Expected: all PASS. The cluster image build remains the acceptance test for the real Linux Claude binary because the local development host is Darwin/arm64 and has no Docker daemon.

- [ ] **Step 7: Commit Task 2**

```sh
git add agent/outputbuilder/adapters.go agent/outputbuilder/adapters_test.go agent/runner/runner.go agent/runner/output_builder_test.go deploy/agent-runner/smoke.sh deploy/agent_runner_dockerfile_test.go
git commit -m "fix(agent): negotiate output builder MCP initialization"
```

---

### Task 3: Emit readiness only from provider-visible evidence

**Files:**
- Modify: `agent/runner/runner.go`
- Test: `agent/runner/output_builder_test.go`
- Test: `agent/schema/event_payloads_test.go`
- Test: `atc/exec/agent_step_test.go`
- Modify: `agent/schema/SCHEMA.md`

**Interfaces:**
- Produces: `managedMCPReadyFromProviderStream(stream []byte, server string) bool`.
- Changes semantics: runner-owned protocol preflight no longer emits `mcp.ready`; provider stream evidence does.
- Preserves wire shape: `schema.EventMCPReady` and `schema.MCPReadyData` are unchanged.

- [ ] **Step 1: Add readiness-semantic regressions**

Rename and reshape the two existing preflight-readiness tests. The first keeps
its real loopback preflight server and script-backed Claude adapter, but the
script now emits a pending init event before its success envelope:

```go
func TestRunManagedOutputBuilderDoesNotEmitReadyForRunnerOnlyPreflight(t *testing.T) {
    claudeBody := "#!/bin/sh\n" +
        "echo '{\"type\":\"system\",\"subtype\":\"init\",\"mcp_servers\":[{\"name\":\"output-builder\",\"status\":\"pending\"}]}'\n" +
        "echo '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"model\":\"m\",\"cost_usd\":0,\"num_turns\":1,\"usage\":{}}'\n"
    if err := os.WriteFile(claude, []byte(claudeBody), 0o755); err != nil {
        t.Fatal(err)
    }

    exit, err := Run(context.Background(), Config{
        Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s",
        ClaudePath: claude, OutputBuilderMarker: "1",
    })
    if err != nil || exit != 0 {
        t.Fatalf("run=%d,%v", exit, err)
    }
    events := string(mustRead(t, filepath.Join(flight, "events.ndjson")))
    if strings.Contains(events, "mcp.ready") {
        t.Fatalf("runner-only preflight claimed provider readiness: %s", events)
    }
}
```

The second keeps its `provider.FakeAdapter`, returns connected evidence from
the existing `outputBuilderSession`, and checks readiness only after `Run`
returns:

```go
func TestRunManagedOutputBuilderEmitsReadyAfterProviderEvidence(t *testing.T) {
    adapter := &provider.FakeAdapter{
        IdentityValue: provider.Identity{Name: "test", Version: "1"},
        StartFunc: func(context.Context, provider.StartRequest, provider.BoundaryControl) (provider.RunningSession, error) {
            return outputBuilderSession(func(context.Context) (provider.Result, error) {
                return provider.Result{Stream: []byte(
                    `{"type":"system","subtype":"init","mcp_servers":[{"name":"output-builder","status":"connected"}]}` + "\n" +
                        `{"type":"result","subtype":"success","result":"done","is_error":false}` + "\n",
                )}, nil
            }), nil
        },
    }
    exit, err := Run(context.Background(), Config{
        Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s",
        OutputBuilderMarker: "1", Adapter: adapter,
    })
    if err != nil || exit != 0 {
        t.Fatalf("run=%d,%v", exit, err)
    }
    events := string(mustRead(t, filepath.Join(flight, "events.ndjson")))
    if strings.Count(events, "mcp.ready") != 1 {
        t.Fatalf("provider-visible readiness count is not one: %s", events)
    }
}
```

Add a table for malformed lines, another server, `pending`, `failed`, and an actual `mcp__output_builder__write_output` tool-use name. Only `connected` or actual managed-builder tool use returns true.

- [ ] **Step 2: Run the readiness tests and verify RED**

Run:

```sh
go test ./agent/runner -run 'TestRunManagedOutputBuilder(DoesNotEmitReadyForRunnerOnlyPreflight|EmitsReadyAfterProviderEvidence)' -count=1
```

Expected: the pending case FAILS because current preflight emits `mcp.ready`; the connected case does not distinguish provider evidence.

- [ ] **Step 3: Parse provider evidence and move event emission**

Remove `writeEvent(EventMCPReady, ...)` from the preflight block, leaving preflight failure behavior unchanged. After `providerResult.Stream` is available, call `managedMCPReadyFromProviderStream`. Parse NDJSON line by line with bounded per-line structs; ignore malformed and unrelated lines. Emit one existing `MCPReadyData` event only when the target server is `connected` or the stream contains an actual managed-builder tool use.

Do not fail an otherwise valid direct-record run merely because provider readiness evidence is absent; Task 1 is the fallback contract.

- [ ] **Step 4: Update event expectations and schema prose**

Update fixtures that currently place `mcp.ready` before provider execution. Keep the event payload unchanged. In `agent/schema/SCHEMA.md`, define it as provider-visible readiness, explicitly distinguishing it from runner protocol preflight.

- [ ] **Step 5: Verify GREEN and the focused compatibility gate**

Run:

```sh
go test ./agent/runner ./agent/schema ./atc/exec -count=1
go test ./agent/outputbuilder ./agent/runner ./agent/schema ./atc/exec ./deploy -count=1
git diff --check
```

Expected: all PASS and no whitespace errors.

- [ ] **Step 6: Commit Task 3**

```sh
git add agent/runner/runner.go agent/runner/output_builder_test.go agent/schema/event_payloads_test.go atc/exec/agent_step_test.go agent/schema/SCHEMA.md
git commit -m "fix(agent): report provider-visible MCP readiness"
```

---

## Final verification and rollout gate

- [ ] Run the focused packages serially:

```sh
go test ./agent/outputbuilder ./agent/runner ./agent/schema ./atc/exec ./deploy -count=1
```

- [ ] Run neighboring reusable-node packages:

```sh
go test ./agent/workflow ./agent/workflowrun ./agent/snapshot/contracts ./agent/api/snapshots ./fly/commands -count=1
```

- [ ] Obtain one independent blocking-only review. The reviewer must inspect the exact Task 1 authority path, initialize negotiation, strict tool-call boundary, real-client smoke, and readiness event meaning. Maximum three review rounds.

- [ ] Rebase onto current `origin/jetbridge`, rerun focused gates, push through the normal reviewed path, and let the pipeline build/smoke the exact Linux runner image.

- [ ] Before retrying a node, verify web and runner source identity match, Argo is Synced/Healthy, and the new runner image smoke reports the output builder connected.

- [ ] Retry `log-diagnosis` with a new idempotency key and a fresh immutable log snapshot. Success requires: provider-visible `mcp.ready` at most once, one validated `diagnosis/v1` output, and no fabricated type/schema authority.
