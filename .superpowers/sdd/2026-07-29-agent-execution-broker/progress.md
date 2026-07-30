# SDD ledger — plan: docs/superpowers/plans/2026-07-29-agent-execution-broker.md
Scope decision: medium production hardening for a handful of operator-managed clusters; static deployment catalogs and configuration are in scope, while dynamic catalog administration, fleet-wide control planes, and high-scale scheduling are deferred.
Existing implementation checkpoint: branch jetbridge at 10c50949ac, with Tasks 1-8 partially implemented before this SDD ledger was introduced.
Task 1: complete (commit b6ce791569, focused contract tests previously verified)
Task 2: partial — broker catalog exists at 46fd1c30a6; workflow compile-time admission and frozen node subset remain
Task 3: partial — durable ledger exists at e44dca38a7 and ATC authority core at 496aa50f49/f360064eeb; expired-lease reconciliation remains
Task 4: partial — capture implementation exists at 80e59f96dd; broker attachment materialization must be checked at integration review
Task 5: partial — native adapters exist at 9f07e8f1f7/e9930c163c; version/capability preflight and Claude structured-output parity remain
Task 6: complete (commit 865370c83d, broker orchestration and strict output tests previously verified)
Task 7: partial — MCP server exists at a9476fdbed; production command/configuration remains
Task 8: Task 8a HTTP/auth transport complete in `3fa7b91505`, with review-round-1 lifetime/key-binding correction `0ef3694ec3` — direct strict handlers and execution-scoped HMAC authority are covered by `go test ./agent/broker/transport ./atc/api/agentchildexecutions -count=1`; global ATC routes/command wiring, ordinary sealer assembly, inspection, and reconciliation remain.
Task 9: pending — trusted managed sidecar injection and pod-security contract
Task 10: pending — image/Helm packaging, operator documentation, and final verification
Task 2: fix round 1/3 (1 addressed, 0 open — fail-closed in-memory compiled-authority validation; commits 3fe1699321..48844a12af)
Task 2: complete (commits 10c50949ac..48844a12af, review clean)
Task 3: reconciliation implementation complete (commit 42659c505c); focused
compile checks and diff hygiene pass. The one serial Ginkgo PostgreSQL
regression is infrastructure-blocked in BeforeSuite: host initdb failed with
`could not create shared memory segment: No space left on device` / `shmget`,
so 0 specs ran. No source failure was observed; rerun the focused serial spec
in a shared-memory-capable environment before marking runtime verification
green.
Task 3: complete (commit 42659c505c, review clean; PostgreSQL runtime gate remains infrastructure-blocked as recorded above)
Task 4: fix round 1/3 (4 findings addressed, 0 open — staged authority, stable observations, submodule policy, and acceptance matrix; commit cd97224059)
Task 4: complete (commits 80e59f96dd and cd97224059, review clean)
Task 5: fix round 1/3 (3 findings addressed, 0 open — preflight-bound execution, strict version parsing, clean probe environment; commits 3ab8ff0dba..4361ed224c)
Task 5: complete (commits 9f07e8f1f7, e9930c163c, 3ab8ff0dba, and 4361ed224c; review clean)
Task 6: fix round 1/3 (3 original findings addressed; 2 new Important integration findings open; commit 7f265ce1c0)
Task 6: fix round 2/3 (2 addressed, 0 open — exact MCP attachment contract and closed durable terminal authority; commit 592a0220b8)
Task 6: complete (commits 865370c83d, 7f265ce1c0, and 592a0220b8; review clean)
Task 7: fix round 1/3 (3 addressed, 0 open — nonlocal workspace objects, exact config maps, redirect-safe authority client; commit 1ae047858d)
Task 7: complete (commits a9476fdbed, c6d263d9d6, and 1ae047858d; review clean)
Task 8a: fix round 1/3 (2 addressed, 0 open — bounded capability TTL and signer/verifier agreement; commit 0ef3694ec3)
Task 8a: complete (commits 3fa7b91505 and 0ef3694ec3; subtask review clean)
Task 8b: partial checkpoint (`b0a132fde1`) — ATC-owned candidate-only ordinary
sealing is implemented and focused broker/transport/authority tests pass.
Follow-up wiring adds internal routes, team-scoped inspection, command
composition, and bounded expired-lease reconciliation. Full durable terminal
result rehydration for duplicate admissions remains open because the ledger
stores only result_snapshot_id, not a result body/full snapshot reference.
Task 8c: complete (final bounded review fix commit) — capability scopes bind a
positive workflow definition ID and compare complete sealing authority,
ordinary sealing supplies exact definition/run occurrence pointers, and the
durable replay migration/factory receive terminal-column and full-success
round-trip coverage. `go test ./atc/api/agentchildexecutions -count=1` and DB/
migration compile-only checks passed. The one serial PostgreSQL attempt ran 0
specs because initdb's `shmget` shared-memory allocation returned `Operation
not permitted`; do not retry in this sandbox.
Task 8: complete after review round 3 (rebased commits
`197cd08618..3f1726bfe3`; no open P1/P2 authority findings).
Semantic rebase checkpoint: rebased 33 broker commits onto
`origin/jetbridge` at `fabf9b83e5`. Upstream had allocated migration versions
`1773106149`–`1773106154`, so the unshipped child-execution migration was moved
to the next free version, `1773106155`. Snapshot record registries preserve
the upstream PR/publish contracts plus `consultation/v1`; reusable-node
compilation preserves its frozen-node assets while accepting broker selectors.
Post-rebase contract, workflow, full broker, focused ATC authority, and DB
compile checks pass. A package-local nullable helper was renamed to avoid the
new upstream PR-binding helper.

Task 9a: complete pending independent review/commit — frozen compiled broker
profiles now survive catalog-aware reusable-node expansion, rendering, private
ATC AgentStep/AgentPlan conversion, and immutable template save/reuse hashing.
The broker command has fixed authority path plus `/healthz` and `/mcp`; runner
reserves `agent-broker`. Focused workflow/workflowrun/builds/command tests and
ATC compile checks pass. Broker transport network tests remain sandbox-blocked
at IPv6 loopback bind; see `task-9a-report.md`. Task 9b pod injection remains
intentionally untouched.

Task 9a: review fix round 1 complete pending independent review/commit —
ordinary StepValidator rejects authored broker authority and the reserved
broker runner marker; only the renderer's non-serializing discriminator
permits authority through server template validation. Render/template hashing
now strict-decodes canonical broker profiles and compares every outer field to
the recomputed exact authority. Focused workflow/workflowrun/ATC/builds/
command tests pass; see `task-9a-report.md`.

Task 9a: review fix round 2 complete pending independent review/commit — raw
one-off plans now fail closed on `AgentPlan.BrokerAuthority` and the reserved
broker MCP marker, including nested plans and JSON/YAML across templates. Both
one-off API handlers reject before persistence, and Team/Pipeline
CreateStartedBuild enforce the same shared ingress boundary without changing
trusted workflow-run execution. Structural and direct-handler tests pass; the
broad API/DB runtime suites remain sandbox-blocked by IPv6 loopback/shared
memory restrictions as recorded in `task-9a-report.md`.

Task 9a: **Human Review Required** — review round 3 P1 is locally addressed:
the raw across-template gate now parses generic YAML/JSON, rejects every
interpolated mapping key, and fails closed when typed `atc.Plan` decoding
fails. This prevents runtime substitution from materializing broker authority,
the reserved broker MCP marker, or a whole agent object; ordinary scalar
interpolation remains allowed. Focused validator and direct-handler regression
tests pass, with downstream workflow checks recorded in `task-9a-report.md`.
No further automated review round is scheduled. Proposed human check: verify
those exploit and scalar cases through the raw one-off API boundary before
acceptance.
