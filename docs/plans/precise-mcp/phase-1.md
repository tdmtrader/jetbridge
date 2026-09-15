# Phase 1: operation contracts and client feasibility

Implementation branch: `codex/mcp-precise-implementation`, created clean from
`origin/core` at `62afc93a6b`. The reviewed plan was preserved in `32283f9cfe`.
This phase is implemented and tested; the phase checkpoint awaits owner review.

The operation table now defines the first slice's exact argument/result schemas,
canonical actions, scopes and retry/effect metadata. Follow-on capabilities have
descriptions but no executable schema or binding. A definition cannot claim MCP
support without a handler. Existing production registration remains the single
`pipeline_status` tool until the later adapter phases.

API authorization wrapper selection is extracted into one shared classifier.
Catalog eligibility uses the real access factory and each action's effective
custom role, preserving public reads, the private-job output distinction, team
authority and administrator bypass. MCP consent remains independent; `admin`
consent does not authorize another category. Factory failures remain temporary
errors instead of permanent permission denials.

The schema helper generates a closed root object with `request.oneOf`, an exact
operation constant and closed per-operation arguments. It can remove branches
without leaving their fields behind. Returned schema trees are owned by the
caller, avoiding cross-principal mutation. Runtime byte/version constraints and
per-call authorization still belong in the later dispatch/adapters.

## Verification

Each new feature test was first observed failing because its production API did
not exist, then passed after implementation. All affected package suites passed:

```text
go test ./atc/mcp ./atc/api/auth ./atc/api/accessor ./atc/wrappa ./atc/api/mcpserver
go test ./hack/mcp-schema-probe
```

The authorization tests use real PostgreSQL and the existing access factory.
Coverage includes every actual API route (with a nonempty guard), unknown actions,
independent pause/unpause custom roles, changed team authority, public paths,
public pipeline configuration restrictions, administrator consent separation,
factory failure, precise schema pruning and closed arguments.

Independent repository review: satisfied, no actionable findings. Design panel:
keep precise groups, with the client-policy distinction described below.

## Client evidence and limits

The recorded HTTP experiment (out of tree in `~/jetbridge-evidence/precise-mcp-20260915/` (sha256 in its `MANIFEST.sha256`, out of tree), under `docs/plans/precise-mcp/evidence/http-schema-20260914-production-contract/`)
imports the actual operation schemas and production HTTP wrapper. The actual
reference client accepted five valid reads and rejected seven invalid/pruned
calls. Codex 0.153.1 completed five reads; two mixed-group reads were blocked by
its configured approval policy. No mutation was requested or dispatched.

The reference client negotiated `2025-11-25`; Codex negotiated `2025-06-18`.
The current SDK transport accepts both. This establishes compatibility with
synthetic bearer grants, not OAuth enrollment/renewal/revocation or interactive
approval-dialog behavior. Those checks remain in phase 6.

Codex approval applies to an entire resource tool. A mixed read/write group may
therefore require approval even for a read operation. A pruned read-only group
must retain read-only annotations; metadata-only writes must not affect it.
`capabilities_explain` reports server eligibility and must not suggest broader
OAuth consent as a remedy for a client-policy refusal.

## Owner checkpoint

Review the operation/authority coverage and the client evidence above. To inspect
the concrete behavior, the recorded `reference.json` contains native schemas and
validation failures; `codex-results.json` shows the successful and locally blocked
calls. The [probe instructions](../../../hack/mcp-schema-probe/README.md) reproduce
the same isolated experiment without touching live JetBridge.

After checkpoint approval, phase 2 connects these definitions to permission-
filtered catalogs, capability explanations and grouped dispatch. The strict-save,
bounded-event, remaining mutation and full-client phases are still pending.
