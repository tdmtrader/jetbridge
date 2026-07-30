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
