# Agent Execution Broker Implementation Report

**Date:** 2026-07-29  
**Branch:** `jetbridge`  
**Base:** rebased onto `origin/jetbridge` at
`fabf9b83e56cba10f97e743875920b538b3a1a2c`

## Outcome

The first implementation batch establishes the broker's executable core and
ATC persistence boundary. It is not yet deployable end to end: managed-sidecar
injection, execution-scoped HTTP authority, the authoritative result-sealer
adapter, workflow admission wiring, and image/Helm packaging remain.

## Implemented

- Frozen `consultation/v1` record contract with declared schema, append-only
  digest history, semantic validation, raw codec, registry entry, and parity
  fixtures.
- Provider-neutral `economy|balanced|frontier` and `medium|high` selectors.
- Deterministic operator profile catalog with immutable authority digests,
  exact provider/model/harness resolution, pinned worker image, credential
  slots, byte/deadline limits, and honest control capabilities.
- Exact dirty Git workspace capture using a temporary index:
  - staged, unstaged, deleted, nonignored untracked, binary, and ignored-file
    behavior;
  - caller index preservation;
  - binary full-index patch;
  - independent patch application and result-tree proof;
  - double-capture stability check; and
  - entry/byte limits.
- Native Claude Code, Codex, and Cursor CLI invocation builders.
- Native JSONL normalization, terminal result extraction, observed
  token/cost/duration handling, malformed/truncated/error failure semantics,
  controlled environment, direct argv execution, process-group cancellation,
  output bounding, and credential-safe provenance.
- Strict output boundary for `review/v1` and `consultation/v1`, including
  exactly one JSON value, duplicate-key rejection, unknown-field rejection,
  size limits, subject/evidence validation, and raw-prose rejection.
- Synchronous broker engine with:
  - exact admission before child execution;
  - logical attachments only;
  - fixed instructions plus caller-selected fresh context;
  - no transcript field or recursive broker input;
  - concurrent calls without serialization;
  - durable phase updates;
  - static-review/tests-not-run provenance; and
  - server-authoritative terminal sealing interface.
- MCP surface exposing only `request_review` and `consult_agent`, with strict
  provider-neutral schemas, node-profile guidance, synchronous results, safe
  argument errors, generated idempotency identities, and progress support
  through the existing Streamable HTTP transport.
- Migration `1773106149`:
  - team/workflow/node/parent-attempt identity;
  - immutable exact profile and input digests;
  - idempotency uniqueness;
  - database-enforced lifecycle transitions;
  - monotonic event sequence;
  - result snapshot same-team foreign key;
  - leases, protected transcript reference, usage, duration, and terminal
    errors; and
  - reconciliation index.
- PostgreSQL child-execution factory with sequential and concurrent idempotent
  creation, immutable fingerprint verification, team-scoped reads, monotonic
  compare-and-advance, and event insertion.
- ATC authority service that independently re-resolves the frozen profile,
  allocates/adopts the durable execution, advances leased phases, invokes an
  authoritative sealer, and atomically binds the result snapshot while
  terminalizing success.

## Key Decisions Recorded

The complete decision record is in
`docs/superpowers/specs/2026-07-29-agent-execution-broker-design.md`. The most
implementation-significant decisions are:

1. Broker-worker sidecar plus thin ATC authority/ledger; ATC stays off the
   provider data plane.
2. Initial tools are static `request_review` and read-only `consult_agent`;
   writable delegation is deferred.
3. Calls are synchronous but independently concurrent.
4. Neutral selector resolution is exact, frozen, and node-scoped.
5. Children inherit no parent transcript or undeclared context.
6. Dirty Git state is a verified `repository-change/v1`; a commit is not
   required.
7. Review is explicitly static and records that tests were not run.
8. Shared deployment credentials are hidden behind credential slots.
9. Native CLI controls are recorded honestly; filesystem/pod/network
   isolation, not prompts, is the authoritative boundary.
10. Invalid native prose never becomes a typed successful result.

## Verification

Passed:

```text
go test -race ./agent/broker/... -count=1
go test ./agent/snapshot/contracts -count=1
go test ./agent/workflow -count=1
go test ./atc/api/agentchildexecutions -count=1
go test ./atc/db/migration -run TestMigration \
  -ginkgo.focus 'agent child executions' -count=1
go test ./atc/db -run TestDB \
  -ginkgo.focus AgentChildExecutionsFactory -count=1
git diff --check
```

The PostgreSQL suites required execution outside the filesystem/process
sandbox because PostgreSQL allocates local shared memory and a socket. The
first race run also exposed a race in the concurrency test double; that test
was fixed and the complete broker tree then passed under the race detector.

The pre-existing `atc/api/mcpserver` suite was sampled before broker work. Its
49 non-network specs passed, while four `httptest.NewServer` SSE specs could
not bind an IPv6 loopback socket inside the sandbox. The new broker MCP tests
use direct `http.Handler` requests and pass.

## Remaining Before Deployment

The next batch should follow Tasks 2, 8, 9, and 10 of the implementation plan:

1. Compile node-visible selectors and exact operator profiles into immutable
   schema-v3 workflow admissions.
2. Add execution-scoped ATC HTTP routes/authentication for the sidecar; the
   current authority service is transport-neutral and tested in process.
3. Implement the ordinary snapshot pipeline adapter behind `ResultSealer`.
4. Connect workspace capture to the `workspace` attachment resolver and
   materialize the verified patch in the disposable child worktree.
5. Add native binary version/capability preflight and finish Claude structured
   output negotiation for the deployment's pinned CLI version.
6. Add managed broker-sidecar runtime injection, read-only parent workspace
   projection, private scratch, sidecar-only credential secret refs, and the
   Kubernetes security context.
7. Add the `agent-broker` command/image, Helm values/templates, network policy,
   Fly/ATC inspection route, and Kubernetes smoke test.
8. Preserve specific terminal error codes across the authority transport
   rather than the current generic `broker_error` phase fallback.
9. Make a terminal idempotent replay return the already sealed result instead
   of starting another native invocation.
10. Add expired-lease reconciliation using the migration's reconciliation
    index.

No live provider call, image build, cluster mutation, push, or deployment was
performed.
