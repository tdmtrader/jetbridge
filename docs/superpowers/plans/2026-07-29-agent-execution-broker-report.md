# Agent Execution Broker Implementation Report

**Date:** 2026-07-29
**Branch:** `jetbridge`
**Base:** rebased onto `origin/jetbridge` at
`9872d6445dfb2faf92fe284d128f6358b5396db1`

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

## 2026-07-30 Workspace Authority Checkpoint

The review vertical slice now captures dirty workspace state after durable
admission and before any child harness work:

- the bootstrap scope carries an exact `repository/v1` workspace base
  separately from static attachment inputs;
- capability handoff is staged as phase-only, capture-only, then full
  lifecycle authority, so a pre-capture token cannot update, seal,
  terminalize, or recapture an execution;
- only base commit/tree, result tree, patch bytes/digest, entry count, and the
  fixed capture-policy revision cross from the sidecar to ATC;
- the raw patch limit is 2.75 MiB, leaving bounded headroom for base64 and the
  envelope under the private API's 4 MiB JSON limit;
- ATC derives repository identity from the exact team-authorized base
  snapshot, constructs `record.json` plus `content/workspace.patch`, and uses
  the ordinary snapshot creator/validator to prove base and result lineage;
- migration `1773106155` stores the immutable same-team
  `repository-change/v1` workspace reference, with idempotent replay,
  conflicting-capture rejection, and a monotonic `workspace_captured` event;
- the refreshed execution scope exposes that durable ref as the authoritative
  review subject; and
- the child runtime materializes the already-captured local result and never
  re-reads or mounts the parent's live tree for the child process.

Focused broker, runtime, ATC authority, repository-contract, command, and
compile-only DB/migration checks passed. The broker transport suite passed
outside the loopback sandbox. The previously documented PostgreSQL
shared-memory failure was not retried.

### Workspace authority review round 1

Two load-bearing corrections were applied:

- Workspace and result productions use the same stable
  `agent-child/sha256:<execution-id-hash>` plan identity and distinct output
  ports. Concurrent children therefore cannot collide on the parent
  build/node/attempt production tuple. Parent node-plan and workflow lineage
  remain in ATC provenance and the durable execution identity.
- The ledger stores an immutable canonical workspace-capture fingerprint
  beside the sealed ref. Exact capture replay returns that ref without
  resealing and refreshes lifecycle authority; a different candidate
  conflicts. The broker retries one exact capture after an ambiguous response,
  while the capture-failure action is accepted only for an unbound
  `capturing` review, with the compare-and-advance sequence fencing races.

Focused regressions cover concurrent/sequential child productions, lost
capture responses, exact replay without resealing, conflicting candidates,
and stale capture-failure tokens after binding/running. DB and migration
packages compile; the known PostgreSQL sandbox gate was not retried.

### Workspace authority review round 2

Exact bound-capture replay is now accepted only while the execution remains
in `capturing`. A stale capture token used after `running` is rejected without
receiving a new lifecycle capability. Lost-response recovery is unchanged
because the durable bind itself deliberately leaves the execution in
`capturing`.

## 2026-07-30 Production Catalog Persistence Checkpoint

The medium-hardening production path now treats the deployment catalog as
static import authority rather than runtime lookup state:

- ATC accepts one optional strict JSON catalog file for the cluster. Unknown
  fields, invalid profiles, configured digests, and malformed/trailing JSON
  fail startup composition; no file preserves ordinary catalog-free behavior.
- Valid provider-neutral broker selectors without a local catalog return the
  typed `BrokerCatalogRequiredError` only after tool/tier/effort and duplicate
  selector validation. Fly uses only that typed condition to defer workflow or
  node compilation to ATC; every other local validation error still blocks the
  request.
- Workflow and node API responses project away exact operator profiles and
  their provenance hash, so Fly never receives provider, model, harness,
  credential-slot, or native-control authority. Neutral authored selectors
  remain visible in the source manifest.
- migration `1773106156` adds one nullable `compiled_definition` JSONB column
  to the shared workflow/node definition table. New imports atomically persist
  canonical `json.Marshal` output with source and node bindings.
- reads, prior-signature comparisons, promotion, workflow-run resource-source
  loads, experiment target loads, node release, and released-node expansion
  strictly parse the persisted compiled representation and do not consult the
  current broker catalog.
- byte-identical reimport returns the original persisted compilation, so a
  Catalog A revision retains A under Catalog B. A genuinely changed source
  revision resolves with B, and a released node imported under A retains A
  when expanded into a workflow under B.
- legacy NULL rows may recompile only through catalog-free APIs. Ordinary
  source remains readable (including exact released-node resolution where the
  store already had that resolver), while any legacy source containing broker
  selectors fails closed instead of resolving against today's catalog.

Focused verification passed:

```text
go test ./agent/broker ./agent/workflow ./agent/api/workflows \
  ./agent/api/nodes ./fly/commands ./atc/atccmd -count=1
go test ./atc/db -run '^$' -count=1
go test -c -o /private/tmp/agent-db-catalog.test ./atc/db
go test -c -o /private/tmp/agent-migration-catalog.test ./atc/db/migration
git diff --check
```

The load-bearing Catalog A/B factory and migration runtime specs are present
but were not executed because the previously recorded PostgreSQL shared-memory
failure is a known sandbox limitation and the session rules prohibit retrying
it here.

## 2026-07-30 Managed Broker Companion Checkpoint

Frozen broker authority now activates one dedicated Jetbridge companion. The
exec path rejects missing workflow identity, invalid parent attempts,
non-hermetic plans, noncanonical profiles, and missing or incorrectly typed
review workspaces. Consult-only profiles may start with no workspace or
attachments.

One cached command-scoped signer/verifier is shared by pod minting and the
internal HTTP handler. The pod receives only a bootstrap bearer plus a strict
command-compatible authority document; the HMAC root key remains ATC-only.
The strict runtime file owns the HTTPS endpoint, image paths,
instruction/schema digests, K8s Secret/key slot coordinates, capture limits,
scratch size, and resources.

Jetbridge mounts only the read-only live workspace, selected typed static
attachments, exact broker authority files, selected credential key files, and bounded
scratch into the broker. Existing physical-volume alias checks are reused.
The fixed port-7784 companion has `/healthz`, non-root execution, read-only
rootfs, no privilege escalation, dropped capabilities, RuntimeDefault
seccomp, no service-account token, and a server-owned
`concourse.ci/agent-broker=true` label for Task 10 network policy.

Focused verification passed:

```text
go test ./atc/runtime ./atc/exec ./atc/engine ./atc/worker/jetbridge \
  ./atc/atccmd ./atc/api/agentchildexecutions ./cmd/agent-broker \
  -run 'ManagedAgentBroker|AgentBroker|Capability|Config' -count=1
git diff --check
```

A broader affected-package run passed runtime, exec, engine, atccmd, and child
authority packages. Jetbridge reached an unrelated existing `httptest`
listener and failed before its test body because this sandbox denies IPv6
loopback binds (`listen tcp6 [::1]:0: operation not permitted`). The focused
Jetbridge broker suite is green; the infrastructure-only gate was not retried.

### Managed companion review hardening

The native harness now executes behind a Landlock ABI 3+ filesystem boundary
in a short-lived helper process. The policy grants write access only to the
current run scratch and read access only to pinned immutable runtime assets;
it does not grant the live workspace, broker authority/credentials, `/proc`,
the scratch parent, or sibling runs. Provider networking remains available.
Broker startup executes a disposable create/add/restrict preflight and fails
closed on an old/disabled kernel or a seccomp-denied syscall. Managed clusters
therefore require Linux 6.2+ and a RuntimeDefault profile which admits the
Landlock syscalls; there is no compatibility fallback.

The parent-to-broker loopback MCP connection now uses a random per-parent
bearer stored in separate main-only and broker-only private files. The child
harness receives neither copy and cannot inspect broker process state.
Workspace capture writes its index, object database, and verification checkout
only beneath broker scratch, using the source object database as a read-only
alternate. Finally, readiness uses an in-container exec healthcheck against
the fixed loopback listener rather than an unreachable Pod-IP HTTP probe.

The second review round adds a child-only amd64 seccomp-BPF process boundary
and makes the long-lived broker nondumpable before reading authority or
credentials. The filter denies arbitrary same-UID signaling and direct
process-inspection/mutation routes while allowing signals confined to the
child's own PID/process group. Landlock paths and the executable are now
resolved through every ancestor symlink, checked again against physical
workspace/authority/proc/scratch targets, and used only in canonical form.
Startup also executes each pinned CLI `--version` without credentials through
the production helper and configured read paths, so missing image assets or
denied runtime dependencies prevent readiness.

This remains medium hardening for carefully managed clusters. The child shares
the sidecar cgroup and can still cause resource contention up to existing
container limits; per-child cgroups and hostile multi-tenant availability
isolation are not claimed. The amd64 filter's live helper test and real pinned
CLI smoke are Linux CI/deployment gates.

## 2026-07-30 Task 10 Packaging and Operator Handoff

The managed companion now has a reproducible `linux/amd64` package and an
optional, disabled-by-default Helm surface:

- the multi-stage image pins both base images by digest and fetches Codex
  `0.146.0`, Claude Code `2.1.212`, and Cursor CLI
  `2026.07.23-e383d2b` from exact vendor URLs with literal SHA-256 checks;
- fixed review/consult instructions and output schemas are copied read-only,
  and their instruction digests are recomputed by packaging tests;
- the runtime contains only the broker, native harnesses, certificates, Git,
  ripgrep, and shell support, runs as `65532:65534`, and has no update or
  package-install channel;
- the adapter compatibility table now admits only the three packaged
  versions. Real `--version` fixtures cover each native output form;
- Codex exact argv uses `--strict-config`, `--ignore-user-config`, and
  `--ignore-rules`. These were verified directly in the official
  `rust-v0.146.0` `codex-rs/exec/src/cli.rs` command definition (the three
  global args are declared at lines 17-36), and the adapter test pins them;
- Claude receives a validated, non-symlink, at-most-1-MiB JSON schema inline,
  matching its current `--json-schema` contract, and both documented updater
  controls are disabled;
- the manual pipeline job pushes a commit-addressed image and extracts the
  registry-reported digest rather than trusting local `RepoDigests`;
- Helm validates exact image authority, HTTPS authority, a distinct capability
  Secret, nonempty static profiles/credentials, worker-image equality, and
  unique credential slots. Duplicate slots fail before the ConfigMap can
  silently overwrite one;
- `agentBroker.scratch.sizeBytes` is the single authoritative scratch bound;
  Jetbridge converts it to the companion EmptyDir limit. There is no duplicate
  no-op Helm setting;
- NetworkPolicy documentation is explicitly whole-pod and records Kubernetes'
  additive allow-rule semantics; it does not claim sidecar-only enforcement;
  and
- `CONCOURSE_AGENT_BROKER_SMOKE=1 make test-agent-broker-smoke` provides an
  explicit credential-free fake-harness/authority gate across adapters,
  review/consult engine paths, and the synchronous MCP surface.

Fresh focused verification:

```text
go test -ldflags='-s -w' ./deploy/chart/tests -run AgentBroker -count=1
ok github.com/concourse/concourse/deploy/chart/tests 3.902s

CONCOURSE_AGENT_BROKER_SMOKE=1 make test-agent-broker-smoke
ok github.com/concourse/concourse/agent/broker/adapter 0.249s
ok github.com/concourse/concourse/agent/broker 0.162s
ok github.com/concourse/concourse/agent/broker/mcp 0.735s

go test ./deploy -run AgentBroker -count=1
ok github.com/concourse/concourse/deploy 0.274s

go test ./agent/broker/adapter -count=1
ok github.com/concourse/concourse/agent/broker/adapter 0.316s

go test -race ./agent/broker/adapter -count=1
ok github.com/concourse/concourse/agent/broker/adapter 1.389s

helm lint deploy/chart
1 chart(s) linted, 0 chart(s) failed

git diff --check
PASS
```

The chart test used stripped linker symbols only to stay within the local
machine's nearly exhausted disk; it does not alter compiled behavior. An
earlier combined run passed `./deploy` and `./agent/broker/adapter`, then the
chart linker failed with `errno=28` before any chart tests ran. Removing only
the task-specific Go cache restored enough space for the focused chart suite
above.

The image was intentionally not built locally because the remaining disk
budget was too small for its base images and three harness downloads. The
known PostgreSQL shared-memory sandbox failure was not retried. Real
PostgreSQL ledger, Kind/K3s on the managed Linux kernel/CNI, live amd64 BPF
helper, real packaged harness preflight, image build/push, and live-provider
calls remain external promotion gates.

### Hard promotion blocker

Task 9b automated review round 3 found an unresolved same-UID signal boundary:
a child can attempt to join the broker's process group and signal it through
`kill(0)`, while async-I/O ownership provides another possible signal route.
That security/runtime code is outside Task 10 ownership and was not changed.
The review budget requires human review rather than another automated fix
round. Therefore this image and Helm surface are **not promotion-ready** until
a human-approved boundary correction lands and a live Linux regression proves
both routes closed. This is a medium-hardening package for a handful of
carefully managed clusters, not a hostile-multitenant or fleet-scale claim.

### Final rebase verification

The 46 broker commits were rebased without conflicts onto the current
`origin/jetbridge` tip `9872d6445d`. Range-diff reports the first 45 patches
as identical; Task 10 differs only by this final handoff update and report
whitespace cleanup. The upstream tip is an ancestor and the worktree is clean.

Post-rebase passes:

```text
go test -ldflags='-s -w' ./agent/broker/adapter -count=1
go test -race -ldflags='-s -w' ./agent/broker/adapter -count=1
go test -ldflags='-s -w' ./agent/broker/sandbox \
  ./agent/broker/runtime ./cmd/agent-broker -count=1
go test -ldflags='-s -w' ./deploy -count=1
go test -ldflags='-s -w' ./deploy/chart/tests -run AgentBroker -count=1
helm lint deploy/chart
git diff --check origin/jetbridge...HEAD
```

The post-rebase fake smoke passed its adapter and broker-engine stages. Its MCP
stage and the focused ATC/Jetbridge build exhausted the host disk while
compiling dependencies (`no space left on device`) before tests ran. They were
not retried. The same exact MCP and managed-companion tests passed before the
conflict-free rebase, and the range-diff shows those patches unchanged.
