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

Task 9 workspace-authority checkpoint: implementation complete pending
independent review/commit. `request_review` now stages exact capabilities
through phase-only, capture-only, and lifecycle scopes. The broker captures
the live read-only Git root once after durable admission, sends ATC a bounded
path-free candidate (2.75 MiB raw patch maximum), and ATC derives
repository identity from the exact `repository/v1` base before sealing an
ordinary `repository-change/v1`. Migration `1773106155` durably binds the
same-team workspace ref with immutable/idempotent semantics and an event;
the refreshed lifecycle scope carries that ref as `Inputs["workspace"]`.
Runtime materializes only the already-authorized local capture. Focused
broker/runtime/authority/contracts/command tests and DB/migration compile
checks pass; the complete transport suite passes outside the loopback
sandbox. The known PostgreSQL shared-memory runtime gate was not retried.

Task 9 workspace-authority review fix round 1/3: both P1 findings addressed
pending commit. Snapshot productions now use a stable execution-ID-derived
plan identity, keeping workspace/result ports together for one child while
separating concurrent children; parent node/workflow authority remains in
source provenance and the ledger. The durable workspace binding now includes
an immutable canonical capture fingerprint. Exact replay returns the bound
ref without resealing, different replay conflicts, the broker retries once
after an ambiguous capture response, and capture-failure authority is fenced
to an unbound capturing review. Focused broker/authority tests and DB/migration
compile-only checks pass.

Task 9 workspace-authority review fix round 2/3: the remaining P1 is
addressed pending commit. Exact bound-capture replay is now accepted only
while the durable execution remains in `capturing`; after `running`, the old
capture token is rejected and cannot mint another lifecycle capability.
Ambiguous-response recovery remains valid because binding does not advance
the execution out of `capturing`. The focused handler/service regression
passes.

Task 9 production-catalog checkpoint: complete pending independent review.
Migration `1773106156` persists one canonical compiled definition beside the
shared workflow/node source row. New server imports resolve through one
immutable static deployment catalog; reads, promotion, signature comparison,
node release/expansion, workflow-run source loads, experiments, and
idempotent reimport parse the stored authority without catalog lookup. Legacy
ordinary NULL rows retain catalog-free fallback while legacy broker sources
fail closed. Fly defers only the typed, neutral-validated catalog-required
condition to ATC for both workflows and nodes; public workflow/node responses
strip exact operator profiles so Fly never receives provider/model authority.
Focused broker/workflow/API/Fly/atccmd suites, DB/migration compile-only checks,
migration preflight head, and diff hygiene pass. PostgreSQL runtime was not
retried because of the recorded shared-memory sandbox failure.

Task 9b managed-companion checkpoint: implementation complete pending
independent review. Trusted broker profiles activate one dedicated
`ManagedAgentBroker` through an exec-injected command-scoped authority
factory. ATC and its internal HTTP handler share one cached signer/verifier;
the pod receives only a scoped bootstrap bearer and strict command-compatible
authority. Jetbridge projects exact read-only workspace/typed attachments,
broker-only Secret key files, bounded broker-only scratch, and no generic main
mounts. The fixed digest-pinned companion has loopback MCP, readiness,
non-root/read-only-root/drop-ALL/RuntimeDefault security, no service-account
token, and a server-owned network-policy label. Focused runtime, exec, engine,
Jetbridge, atccmd, authority, and command suites pass. A broader Jetbridge run
hit the known sandbox IPv6 loopback bind denial in an unrelated httptest.

Task 9b review fix round 1/3: the three P1 production-boundary findings are
addressed pending commit. Native harnesses now cross a fail-closed Landlock
ABI >=3 helper boundary with only per-run scratch and pinned immutable runtime
reads; `/workspace`, broker authority, credentials, `/proc`, the scratch
parent, and sibling runs remain inaccessible while provider networking stays
available. Startup probes create/add/restrict and refuses unsupported kernels
or seccomp denial. Per-parent MCP bearer capabilities are duplicated only into
main-private and broker-private files, `/mcp` authenticates them, and the
Landlocked harness cannot recursively invoke the broker. Git capture uses a
scratch object database/index/verification checkout with the source object DB
as a read-only alternate. Readiness is now an in-container exec healthcheck of
the loopback endpoint. Linux cross-compilation and exact changed suites pass;
broad runner/Jetbridge tests remain blocked only at unrelated sandbox-denied
IPv6 `httptest` listeners. See `task-9b-report.md`.

Task 9b review fix round 2/3: all three P1 findings are addressed pending
commit. The broker becomes nondumpable before reading authority or credentials.
The child helper installs an amd64 seccomp-BPF boundary before exec, denying
arbitrary cross-process signaling, ptrace/process-VM/pidfd inspection, and
related same-UID mutation routes while preserving signals confined to its own
PID/process group. Landlock now canonicalizes writable, read-only, forbidden,
and executable paths through all ancestor symlinks, repeats physical overlap
checks, and execs only the canonical binary. Startup runs every pinned CLI
`--version` without credentials through the same helper, seccomp filter,
Landlock policy, and configured read paths used by real work. Focused
sandbox/runtime/cmd/runner/Jetbridge tests and linux/amd64 plus linux/arm64
compile checks pass. The live amd64 BPF helper remains a Linux CI/deployment
gate on this arm64 macOS host. The focused adapter preflight/process tests pass
with concurrent Task 10 compatibility changes present but excluded from this
commit. Residual same-cgroup resource exhaustion is accepted within the
medium-hardening, carefully managed cluster scope.

Task 9b review round 3: **Human Review Required / promotion blocked**. The
same-UID child process boundary still admits a process-group/async-I/O signal
escape: a child can attempt to join the broker PGID and use `kill(0)`, while
async-I/O ownership supplies another potential signal route. Per the exhausted
automated review budget this is not changed in Task 10. The broker image must
not be promoted until a human-approved boundary fix and live Linux regression
cover both routes.

Task 10: packaging/documentation implementation complete pending final
verification and commit. The image uses digest-pinned base images, exact
versioned and SHA-verified Codex 0.146.0, Claude Code 2.1.212, and Cursor
2026.07.23-e383d2b downloads, fixed read-only instruction/schema assets, and
an unprivileged runtime. Helm remains disabled by default and supplies static
profiles, exact image authority, Secret coordinates, resources, one
authoritative scratch byte count, and whole-pod NetworkPolicy guidance.
Duplicate credential slots fail chart rendering. The manual pipeline publishes
the registry-reported digest, and an explicit credential-free fake-harness
smoke target covers the supported adapters plus broker/MCP paths. Operator docs
scope the result to medium hardening and mark the Task 9b process-boundary
finding as a hard promotion blocker.

Task 10 review fix round 1: three packaging/runtime blockers are addressed.
Both packaged output schemas use draft-07 `definitions` and
draft-07 references, matching Claude Code 2.1.212's accepted dialect; packaging
tests reject newer `$defs`. Claude runs with `--bare` plus the explicit
Read/Glob/Grep allowlist, requires native output schema authority, and consumes
only the terminal `structured_output` JSON; missing or null structured output
fails closed. Cursor 2026.07.23 remains checksum-pinned in the image but is
removed from supported capabilities: catalog/startup reject it until a
verified native control can disable repository rules/instructions and MCP
configuration. Deployable examples now include only Codex and Claude. The
separate Task 9b signaling promotion blocker is unchanged.
