# Task 9a — trusted broker-profile handoff

## Status

Completed on `jetbridge`. This bounded handoff carries frozen broker authority
from schema-v3 compilation to the exact private ATC agent plan without adding
the Task 9b pod mounts, security context, or Helm wiring.

## Delivered

- Added catalog-aware node and workflow-with-nodes compilation APIs while
  preserving the existing catalog-free APIs. Released node broker authority is
  remapped to the exact importing function ID, merged deterministically, and
  re-hashed.
- Rendered agent steps now receive only their function-scoped frozen authority.
  The private `atc.AgentBrokerProfile` carries the exact canonical operator
  profile, identity, digest, and pinned broker worker image. No profile means
  no runtime broker authority.
- Planner validation rejects malformed scope, profile digest, profile body,
  duplicate selector, mutable image, and mixed broker-image authority before
  it constructs an `atc.AgentPlan`; raw authority is defensively copied.
- Immutable template save/reuse includes broker profiles and provenance in the
  same rendered-authority hash calculation, so a durable replay cannot bind a
  template to drifted profile authority.
- `agent-broker` now reads only
  `/run/concourse/agent-broker/authority.json`, serves `GET /healthz`, and
  exposes the existing Streamable HTTP handler only at `/mcp` on its fixed
  loopback listener.
- `agent-runner` reserves `agent-broker` / `127.0.0.1:7784/mcp`; authored MCP
  names or URLs cannot impersonate it, and only the server-created marker
  injects it.

## TDD evidence

The new reusable-node catalog test initially failed because
`CompileNodeDefinitionWithBrokerCatalog` and
`CompileDefinitionWithNodesAndBrokerCatalog` did not exist. The new render
test initially failed because `atc.AgentStep` had no broker authority field.
The command and runner tests likewise initially failed on absent route and
managed-MCP helpers.

Focused passing verification:

```text
GOCACHE=/private/tmp/concourse-go-build go test ./agent/workflow ./agent/workflowrun ./atc/builds ./cmd/agent-broker -count=1
GOCACHE=/private/tmp/concourse-go-build go test ./atc/exec ./atc -run '^$' -count=1
GOCACHE=/private/tmp/concourse-go-build go test ./agent/runner -run TestAdmitBrokerMCPRejectsImpersonationAndInjectsOnlyServerMarker -count=1
git diff --check
```

All listed focused workflow/template/ATC compile checks passed. A separate
`go test ./agent/broker/... -count=1` passed broker, adapter, MCP, output, and
workspace packages, then stopped at the transport package because `httptest`
cannot bind IPv6 loopback (`listen tcp6 [::1]:0: operation not permitted`).

## Risks

Task 9b remains responsible for projecting this already-frozen authority into
the managed sidecar, including its volume, secret, and pod-security boundary.
No dynamic catalog lookup or pod construction changed here.

## Review fix round 1

The initial review found that the `broker_authority` JSON field was shape
validated but not distinguished from authored pipeline input. The fix adds an
in-memory, non-serializing server-derived discriminator set only by
`AgentStep.SetBrokerAuthority`; ordinary step validation rejects any authored
authority before planning. Persisted workflow-run templates retain the exact
authority JSON after their server-side validation, while direct user JSON can
never carry the discriminator.

The workflow hash path now strict-decodes the authority profile with unknown
fields rejected, requires canonical JSON, recomputes the resolved-profile
digest, and compares every outer identity field (function, tool, selector,
ID, revision, digest, and worker image) to that decoded profile. It also
requires the rendered per-agent authority to exactly equal the matching frozen
compiled profile set. Authored `CONCOURSE_AGENT_BROKER_MCP` is rejected by both
ordinary step validation and schema-v3 compilation.

Focused fix verification passed:

```text
GOCACHE=/private/tmp/codex-go-cache go test ./agent/workflow ./agent/workflowrun ./atc ./atc/builds ./cmd/agent-broker -count=1
GOCACHE=/private/tmp/codex-go-cache go test ./agent/runner -run 'Test(AdmittedMCPServersAddsOnlyServerOwnedOutputBuilder|AdmitBrokerMCPRejectsImpersonationAndInjectsOnlyServerMarker)' -count=1
GOCACHE=/private/tmp/codex-go-cache go test ./atc/exec -run '^$' -count=1
git diff --check
```

## Review fix round 2

One-off raw build plans bypass the workflow compiler and planner by design, so
they could otherwise deserialize `AgentPlan.BrokerAuthority` or the reserved
`CONCOURSE_AGENT_BROKER_MCP` environment marker and reach the engine directly.
`atc.ValidateUntrustedPlan` now rejects both fields in direct plans, nested
plans, and JSON or YAML `across` substep templates. Both one-off API handlers
return `400` before calling persistence, while the `Team.CreateStartedBuild`
and `Pipeline.CreateStartedBuild` seams enforce the same rule for any
equivalent raw caller. Workflow-run plans do not use those one-off creation
seams, so server-rendered durable templates retain their trusted authority.

Focused round-2 verification passed:

```text
GOCACHE=/private/tmp/codex-go-cache go test ./atc ./atc/api/buildserver ./atc/api/pipelineserver -run 'Test(ValidateUntrustedPlan|CreateBuildRejectsBrokerFieldsInRawPlan)' -count=1
GOCACHE=/private/tmp/codex-go-cache go test ./atc/db -run 'TestThisPatternMatchesNoTests' -count=1
GOCACHE=/private/tmp/codex-go-cache go test ./atc/builds ./agent/workflow ./agent/workflowrun ./cmd/agent-broker -count=1
git diff --check
```

The focused raw-plan tests and downstream workflow checks passed. The broad
`./atc/api` suite remains sandbox-blocked before its specs run because
`httptest` cannot bind IPv6 loopback; the direct handler tests avoid that
listener and cover both raw one-off endpoints. The DB runtime suite likewise
cannot start PostgreSQL here because its shared-memory allocation is denied;
the compile-only DB package check passed.
