# SDD ledger — plan: docs/superpowers/plans/2026-07-28-agentic-foundations-semantic-rebase.md

## Baseline

- Branch base: `296ef5dd86676fc48abd62bd1615ec4958da2ddd`
- Design commit: `1792f6d12e`
- Plan commit: `313cf2a484`
- Clean target verification passed:
  - `make test-unit`: 121/121 Ginkgo suites plus `agent/schema` in 13m24s.
  - `make test-dev-mcp`: both retained ci-agent packages passed.
  - `make test-fly-integration`: 675/675 specs passed.
  - `make test-integration`: 26/26 specs passed.
  - `helm lint deploy/chart`: passed with the chart's pre-existing informational
    output.
- PostgreSQL-backed suites run serially because they reserve ports 5434–5442.

## Tasks

- [x] Task 1 — direct snapshot team ownership
  - Commit: `179a57f30e feat(agent): make snapshots team-owned`
  - Verification: focused migration/legacy-upgrade, snapshot factory, feedback,
    and all affected DB consumer suites passed serially.
  - Review: approved; no Critical, Important, or Minor findings.
  - Infrastructure note: final rerun required removing only this session's two
    inactive Fly temp directories and one verified-dead PostgreSQL test
    cluster after the Data volume filled.
- [x] Task 2 — remove principals
  - Commit: `4269baf125 feat(agent): remove retired principal authority`
  - Verification: migration, focused authorization/API/ticket/client/ATC/Fly
    packages, focused Fly integration, and 30 Elm tests passed.
  - Review: approved; no Critical, Important, or Minor findings.
- [x] Task 3 — retained dev-capability core
  - Commits:
    - `20ea788325 feat(devcap): share retained execution core`
    - `b90f1e462c fix(devcap): bind outputs and verify logs`
  - Verification: focused `ci-agent` core, MCP, CLI, deploy-image contract,
    `make test-dev-mcp`, and diff checks passed.
  - Review: approved after fixing no-follow output binding and log
    write/close error propagation. One non-blocking cleanup note remains:
    failed multi-log staging can leave an unpublished hidden temporary
    directory in the task filesystem.
- [x] Task 4 — compiled validation authority
  - Commits:
    - `e54f5e1c6f feat(workflow): freeze dev validation authority`
    - `a61ddc33b3 fix(workflow): bind validation authority operationally`
  - Verification: workflow compilation/rendering, workflow-run admission,
    durable resume/template reuse, dispatch, API strict-parser callers,
    focused authority mutations, and diff checks passed.
  - Review: approved after preserving authority-aware identity through runtime
    image injection and durable reuse, enforcing aggregate asset limits and
    strict nested decoding, and proving hybrid interactive dev-MCP reuse does
    not leak runtime sidecar fields into validation authority.
- [x] Task 5 — validation/v1 rev3 provenance
  - Commits:
    - `e6dea50455 feat(snapshot): attest validation provenance at seal time`
    - `87b34ea1ed fix(agent): close validation provenance review gaps`
    - `fe492018f4 fix(db): preserve validation provenance on retry`
  - Verification: snapshot rev3, workflow-run binding, experiment propagation,
    authoritative-provenance migration upgrades, and both affected PostgreSQL
    factories passed. Retry identity covers nonempty mismatch and both
    legacy-empty crossings while preserving identical provenance.
  - Review: approved after enforcing authority before producer streams open,
    restricting it to produced `validation/v1`, wiring provenance through
    durable workflow/experiment identity, using canonical input-name grammar,
    and binding retries to the source run's exact provenance. Final fresh
    review reported no Critical, Important, or Minor findings.
- [x] Integration prerequisite — merge-preflight validation/v1 rev3
  compatibility
  - Commits:
    - `a2d5b939a4 fix(validation): seal merge preflight provenance`
    - `cd85882823 fix(workflow): bind merge preflight identity`
  - Verification:
    `go test ./atc ./atc/exec ./agent/functions/repositorymerge
    ./cmd/function-runner ./agent/workflow ./agent/workflowrun ./atc/builds
    -count=1` passed before review; the focused renderer identity regression
    passed after the blocking fix.
  - Review: accepted in round 2. Round 1 found that the trusted renderer did
    not replace compile-time zero workflow definition/version values. The fix
    now binds the exact persisted identity before fixed config/attestation/hash
    generation. Round 2 reported no blocking or nonblocking findings.
- [x] Task 6 — hermetic non-agent validation
  - Commits:
    - `a57a04f027 fix(validation): close private secret substitution race`
    - `5f87d2671c fix(jetbridge): preserve private mounts after ambiguous pod create`
    - `477c4d554a test(jetbridge): assert typed eviction interruptions`
  - Status: **Accepted** after two user-authorized reopened iterations.
  - The lifecycle correction makes zero-private-mount binding a no-op before
    Pod UID validation, retains precreated authority after an ambiguous Pod
    Create unless a fresh exact-name read proves absence, and gives
    owner-bound reaper deletion the observed Secret UID precondition.
  - The restored zero-mount path exposed three stale uppercase `Evicted`
    assertions from the already-accepted typed interruption change. The
    test-only compatibility commit now asserts
    `runtime.InterruptionError`/`runtime.InterruptionEvicted`.
  - Focused lifecycle and replacement-race regressions passed. A fresh
    host-access `go test ./atc/worker/jetbridge -count=1` passed all 381 specs.
  - Independent blocking-only review passed after each reopened iteration with
    no Critical, High, or blocking findings. `HUMAN-REVIEW-001` is resolved.
- [x] Task 7 — exact validation gates
  - Commits:
    - `2946f9f9af Add exact validation gates for governed workflows`
    - `7571f5f846 Fix validation gate review blockers`
  - Behavior: governed review, merge-approval await, and
    repository-change publication reopen an exact authoritative
    validation/v1 rev3 record and match its candidate, bases, profile,
    protected config, image, toolchain, workflow definition, and workflow
    version before their first side effect.
  - Verification:
    `go test ./agent/workflow ./atc/exec ./atc/engine -count=1` and
    compile-only `go test ./atc/db ./atc/builds -run '^$' -count=1` passed.
    The focused DB spec ran zero specs because PostgreSQL shared memory was
    unavailable in the sandbox and the one host retry found port 5434 in use;
    it was not repeated.
  - Review: round 1 found a post-validation repository-change producer and a
    malformed-private-plan nil-authority panic. The correction makes the
    public change the exact validated candidate and validates the private
    requirement before base traversal. Round 2 found no remaining Critical,
    High, or acceptance-blocking issue.
  - Status: **Accepted** in review round 2. `DEPENDENCY-003` is resolved.
- [x] Task 8 — output-builder core
  - Commits:
    - `bb567a16c5 feat(agent): add managed output builder core`
    - `fc31f229d8 fix(agent): harden managed output builder boundaries`
  - Verification: `go test ./agent/outputbuilder ./cmd/agent-output
    ./agent/snapshot/... -count=1` passed. Focused raw-codec, CLI/MCP, command,
    and agent-runner Dockerfile suites also passed.
  - Review: accepted in round 2. Round 1 found five High blockers in protected
    authority loading, production limits, repository-change input reopening,
    mount TOCTOU, and staged publication. The single correction pass added
    fixed read-only authority, bounded streaming, exact canonical input
    opening, retained `os.Root` handles, and record-last rollback publication.
    Round 2 found all five addressed with no new blocker.
  - Deferred: `DEFERRED-003` tracks fsynced crash-recovery journaling for
    abrupt host loss; ordinary operation failures restore the prior candidate.
  - Fix round 1: addressed all five scoped High findings with fixed read-only
    authority loading, default snapshot limits, exact mounted-input reopening,
    descriptor-anchored output operations, and transactional record/content
    publication. Commit: `fc31f229d8`. Required checkpoint passed; pending
    final scoped review.
- [x] Task 9 — output-builder execution wiring
  - Commits:
    - `fb107b1db1 feat(agent): wire managed output builder authority`
    - `210eafcebf fix(agent): bind output builder runtime authority`
    - `9375fae0b8 fix(agent): reject aliased builder volume sources`
  - Behavior: ATC derives one private canonical authority document from frozen
    typed bindings and built-in record outputs. The pinned Agent runtime gets a
    fixed loopback builder sidecar with only exact typed input read mounts and
    record-output write mounts; prompts, environment, ordinary sidecars,
    credentials, incidental mounts, and service-account tokens receive no
    authority.
  - Runtime admission binds the strict authority to the exact Agent
    `ContainerSpec`, rejects workroot/untyped/secret/cache/scratch and
    input/output overlaps, and fails closed when a selected Kubernetes mount
    aliases another mount by name, cleaned HostPath, or PVC claim.
  - Verification: controller
    `go test ./atc/runtime ./atc/exec ./atc/worker/jetbridge ./agent/runner
    ./cmd/agent-output ./deploy -count=1` passed; after the final correction,
    `go test ./atc/worker/jetbridge ./atc/runtime -count=1` passed.
  - Review: round 1 found malformed-spec projection authority; round 2 found
    DaemonSet output names `dir`/`input-1` could alias forbidden backing under
    different volume names. Both corrections are pinned by negative tests.
    Round 3 found no Critical, High, or acceptance-blocking issue.
  - Status: **Accepted in review round 3.** `DEPENDENCY-001` remains resolved.
- [x] Task 10 — Hangar core
  - Commit: `29e5215b13 feat(hangar): add immutable GCS object storage`
  - Behavior: provider-neutral immutable object contract with canonical
    SHA-256 keys; bounded zstd streaming into generation-pinned, verified GCS
    objects; immutable create/idempotent identical puts; collision, truncation,
    metadata, digest, and byte-limit rejection; cancellation-safe scratch
    handling; and a concurrency-safe test fake.
  - Verification: `go test ./agent/hangar -count=1` and
    `go test ./agent/hangar/hangarfakes -count=1` passed. The tagged emulator
    harness cleanup contract compiled and passed. The exact
    `make test-hangar-integration` target then passed against a temporary
    fake-gcs-server deployment on Borg, including concurrent writers,
    truncated zstd, and complete local-scratch loss. The temporary namespace
    was deleted and its absence verified. `DEFERRED-004` is resolved.
  - Review: independent Terra blocking-only review round 1 found no blocking
    findings and accepted the task. No correction or round-2 review was
    required.
  - Status: Accepted.
- [x] Task 11 — Hangar daemon/deployment integration
  - Commit: `685d09104e feat(hangar): mirror agent snapshots through daemon`
  - Fix commit: `979ca6490a fix(hangar): restore snapshots after cache-root
    loss`
  - Behavior: artifact-daemon commits immutable snapshot bytes to Hangar before
    acknowledging PUT; restores a complete local cache from a generation-pinned
    Hangar read after cache loss; records a single `hangar-v1` durable location;
    keeps legacy node locations readable/adoptable; and never peer-mirrors
    agentic snapshots. Legacy cache cleanup is explicitly unable to delete the
    Hangar authority.
  - Verification: focused daemon cache-loss and Jetbridge
    canonical-location/repair tests, the full daemon and ATC command packages,
    full chart suite, and Helm lint passed. The full Jetbridge package still
    fails 179/380 specs for the single already cataloged Task 6
    zero-private-mount cause; all Task 11-focused tests pass.
    `make test-hangar-integration` was not runnable locally because Docker was
    unavailable; the accepted Task 10 Borg fake-GCS evidence remains the
    provider-store coverage.
  - Review: round 1 found one High blocker: full cache-root absence returned
    404 before the Hangar fallback. The single correction pass routed missing
    roots through verified restoration and strengthened the behavioral
    regression to delete the entire storage root. Round 2 found the blocker
    addressed with no new blocking breakage and accepted the task.
  - Deferred: `DEFERRED-005` tracks unreachable immutable objects after the
    narrow post-Hangar/pre-location failure window; `DEFERRED-006` tracks
    enforcement when operators disable the optional daemon egress policy.
  - Status: Accepted.
- [x] Task 12 — resource-source grammar and persistence
  - Commit: `2d160f6c89 feat(workflow): persist resource-source admissions`.

  - Fix commit: `f9de627f40 fix(workflow): harden source admission bindings`.
  - Reopened boundary commit:
    `875d604026 fix(workflow): require opaque execution envelopes`.
  - Behavior: schema-v3 workflows declare a one-to-one ordinary Concourse
    resource source with standard `trigger`/`version` selection semantics;
    the rendered standing pipeline has one `admit` job, and executable
    rendering accepts source-bearing workflows only with exact sealed snapshot
    bindings. Migrations 1773106142-43 persist the team/workflow/revision
    pipeline owner, exact selecting build/version/type/capture provenance, and
    copied pipeline configuration version. The DB factory atomically activates
    (and drains prior), drains, and archives owned source pipelines and accepts
    only completed successful `admit` builds at the registered config version.
  - Verification: `go test ./agent/workflow -count=1` passed; compile-only
    `go test ./agent/workflow ./atc/db -run '^$' -count=1` passed (with a
    task-local Go cache). The serial migration/legacy-upgrade focus initially
    hit sandbox-denied System V shared memory; the identical host-access rerun
    passed 17/17 specs.
  - Review: round 1 found four High blockers: optional exact execution
    binding, caller-asserted admission provenance, archive/create race, and
    absent DB lifecycle/provenance coverage.
  - Fix round 1: execution parameters hard-bind validated snapshot IDs;
    capture derives bindings from the successful selecting build and frozen
    declaration; archive/create serialize on the source-pipeline owner row;
    and focused DB coverage now checks lifecycle ownership, derivation, and
    idempotency. The sandbox PostgreSQL run was blocked by denied System V
    shared memory; the identical host-access rerun passed 2/2 focused specs.
    Workflow and DB/migration compile checks also passed.
  - Round 2 originally left one High: bare `RenderedFunction.Config` was
    launchable and exact-source binding remained opt-in.
  - Reopened iteration 1 implemented the user-selected declarative-render /
    opaque-launch split. Binder and the durable DB factory now require
    `workflow.ExecutionEnvelope`; trusted private canonical/hash/source
    authority survives cloning, source substitutions fail before validation,
    and zero or mismatched envelopes fail closed after the durable row lock.
  - Verification: workflow and workflow-run suites passed; DB compiled; host
    PostgreSQL specs passed for valid workflow-owned execution and for
    forged/canonical-mismatched envelope rejection.
  - Independent blocking-only review reported no Critical, High, or blocking
    findings.
  - Status: **Accepted** after one of three user-authorized reopened
    iterations. `HUMAN-REVIEW-002` is resolved.
- [x] Task 13 — source capture/reuse runtime
  - Implementation and correction commits:
    - `467950f5f1 feat: persist workflow resource source admissions`
    - `18d14b989a feat: bind workflow runs to sealed source admissions`
    - `95e87f6e5f feat: bind workflow runs to sealed sources`
    - `1a551711b6 feat: manage standing workflow source pipelines`
    - `d5ef8284c6 feat: bind experiment runs to prepared sources`
    - `e08f37bc4a feat: compose workflow source runtime`
    - `d84dbaeb93 fix: protect source selection mutations`
  - Behavior: promotion atomically creates a revision-owned standing
    Concourse admission pipeline; automatic and manual admissions persist the
    exact selecting build and versions, capture them once into sealed
    Hangar-backed snapshots, and expose only a ready admission through the
    opaque execution envelope. Runs, retries, replays, and experiment children
    reuse that exact durable admission. Active/draining/archived lifecycle and
    successful-build reconciliation are composed in ATC.
  - Public pipeline, job, build, and source-version mutations fail closed for
    the server-owned admission pipeline. The round-1 correction added
    transactional guards and HTTP 409 mapping for job pause/unpause and
    resource pin/unpin, enable/disable, and clear-versions.
  - Verification: workflow-run, experiment, ATC, and full API suites passed;
    DB and affected API packages compile; seven focused authority API specs
    passed; `git diff --check` passed. Focused DB behavioral specs compile but
    were not executed because fixed port 5434 is owned by an external
    PostgreSQL process; the one previously documented alternate-port attempt
    was not repeated.
  - Review: round 1 found the lower-level mutation authority hole and one
    migration false positive. Round 2 verified the authority correction,
    confirmed the down migration was already correct, and found no new
    blocking issue.
  - Status: **Accepted in review round 2 of at most 3**.
- [x] Task 14 — direct in-ATC publication
  - Implementation commits:
    - `b2774ce2e2 feat(publisher): resolve direct publication authority`
    - `c960867479 feat(publisher): publish direct git refs atomically`
    - `b0f2497738 fix(delivery): rebase candidates before publication`
    - `14e27b5032 feat(atc): compose direct snapshot publisher`
    - `7629e590d6 feat(deploy): configure direct snapshot publisher`
    - `ce227c1abf fix(publisher): pin direct git executable`
  - Behavior: ATC resolves exact direct-publication policy and opaque scoped
    credentials, materializes the sealed change into private Git scratch, and
    atomically updates the target and idempotency marker. A moved target
    returns `rebase_required`; delivery candidates are rebased before
    publication. Helm mounts separate policy and credentials Secrets only into
    `concourse-web`, exposes only the direct Git adapter, and rejects aliases
    through other consumers. The runtime image supplies controlled Git as the
    web pod's non-root identity.
  - Verification: publisher/directgit, repository-merge/function-runner/
    workflow, ATC composition, focused direct-publisher Helm, runtime-image,
    and Helm lint commands passed. `git diff --check` passed.
  - Residue: legacy chart values are rejected by one explicit fail-closed Helm
    tombstone; focused negative tests retain the old names only to prove their
    absence. No legacy gateway transport, chart resource, flag, or operator
    documentation remains.
  - Review round 1 found one Important inherited-`PATH` credential-boundary
    defect. Fix round 1 pinned production to image-owned `/usr/bin/git` and
    added a counterfeit-`PATH` regression. Fresh round 2 verified the finding
    addressed and found no new blocking issue.
  - Status: **Accepted in review round 2 of at most 3**.
- [x] Task 15 — checkpoint and attempt data models
  - Implementation and correction commits:
    - `add954b863 feat(agent): add durable checkpoint attempt models`
    - `d5d591b88b fix(agent): preserve checkpoint workflow provenance`
    - `0b37ddd6a4 feat(agent): classify runtime pod interruptions`
    - `ea286b0772 fix(agent): fail closed on runtime interruption`
    - `c6aebd0b94 fix(agent): fence checkpoint recovery mutations`
    - `43c6d2965d test(agent): exercise fenced checkpoint authority`
    - `5054e0c6b1 test(agent): expire checkpoint fences through db time`
    - `aafd924521 fix(agent): fence mutations against wall-clock expiry`
  - Behavior: immutable Hangar checkpoint identities; staged/committed
    monotonic generations; append-only events; fenced effect records; exact
    same-head recovery-source pins; fresh durable attempts; structured
    Kubernetes interruption classification; and fail-closed manual review.
    Checkpoint heads preserve immutable v3 workflow-run provenance but do not
    own or terminalize workflow runs.
  - Verification: host serial checkpoint/attempt DB focus passed 28/28;
    migration focus passed 2/2; recursive checkpoint/fake tests passed;
    runtime/Jetbridge interruption tests passed; AgentStep interruption focus
    passed 4/4; affected packages compile.
  - Review round 1 found three Important blockers. The single correction pass
    addressed the recovery-source pin and broken fake and added fence
    authorization to checkpoint/effect mutations.
  - Review round 2 found one Important blocker: fence expiry compared
    against transaction-start `now()`, allowing a transaction that waited on a
    lock beyond real lease expiry to mutate under stale time.
  - The user-authorized correction changed the post-lock authority sample to
    `clock_timestamp()` and added a PID-observed lock-wait regression. That
    regression failed against the old implementation and passed after the fix.
  - Status: **Accepted**. The single authorized final Terra review found no
    remaining blocking issue; `HUMAN-REVIEW-003` is resolved.
- [x] Task 16 — safe-boundary checkpoint capture
  - Implementation commits:
    - `bd97a94099 feat(checkpoint): add bounded capture archive`
    - `98d0b06c99 feat(daemon): capture checkpoints through Hangar`
    - `b74fee3cd9 feat(jetbridge): add exact-node checkpoint capture client`
    - `d71410465b fix(jetbridge): accept available checkpoint objects`
    - `dac9e57c9e feat(jetbridge): add checkpoint capture quiescence seam`
    - `e7a86a4739 fix(jetbridge): bound checkpoint lease release`
    - `10c71dc81b feat(agent): add provider boundary runner seam`
    - `c865270f37 feat(jetbridge): add exact provider boundary lease`
    - `9f8944f649 feat(agent): finalize successful checkpoint attempts`
    - `fa5aa28972 feat(checkpoint): surface node preemption notices`
    - `eba81810b1 feat(exec): coordinate checkpoint capture commits`
    - `b276bd1533 feat(agent): derive checkpoint capture provenance`
    - `0ac58199d4 feat(jetbridge): capture completed agent workspaces`
    - `0f515a2d97 feat(jetbridge): bind checkpoint preemption intent`
    - `4af4960bdf feat(metric): instrument agent checkpoint capture`
    - `406b260f57 feat(exec): add checkpoint execution lifecycle`
    - `10c39c7476 feat(exec): adapt checkpoint metrics to otel`
    - `e3c31bb77f feat(atc): configure agent checkpoint capture`
    - `296b782541 feat(atc): compose agent checkpoint capture`
    - `c0e118374e feat(exec): integrate agent checkpoint capture lifecycle`
    - `91d8e47fb0 fix(jetbridge): preserve terminal checkpoint reattach`
  - Behavior: authenticated v3 AgentSteps derive server-owned attempt and
    workspace provenance; elapsed, explicit, and exact-node preemption sources
    enqueue intent that can capture only at a provider-declared safe boundary;
    clean completion uses trusted terminal process evidence. Jetbridge binds
    and quiesces the exact pod/process, the node-local daemon packages a
    bounded descriptor-anchored workspace/session archive and writes it to
    Hangar, and ATC completes the exact object upload before its fenced
    generation/head CAS. Failures preserve the prior committed head.
  - Verification: normal-vet exec/engine/ATC command suites passed; checkpoint,
    provider, runner, runtime, artifact-daemon, exact-node Jetbridge capture,
    safe-boundary, terminal, preemption, and metric suites passed. Existing
    loopback-listener tests were rerun with host access. No source or
    source-derived image was uploaded to Borg.
  - Review: round 1 found one reattachment reliability gap. The correction
    preserved exact terminal evidence for same-process/property-preloaded
    reattachment without allowing missing evidence to re-execute completed
    work. Round 2 found it addressed, no new blocking breakage, spec PASS, and
    code quality APPROVED. No lower finding was deferred.
  - Status: **Accepted**.
- [x] Task 17 — fresh-attempt restore/provider resume
  - Commits:
    - `000f8ce295 fix(checkpoint): pin recovery to latest head`
    - `c0dfce8f03 feat(agent): gate checkpoint recovery by provider proof`
    - `11abba6da3 feat(checkpoint): restore exact snapshots through daemon`
    - `9cf73ef12d feat(checkpoint): gate fresh attempt restore`
    - `3777aebcdd feat(checkpoint): prepare durable recovery attempts`
    - `1f6e9f67d0 feat(agent): resume interrupted attempts safely`
  - Behavior: recovery freezes and verifies one exact retained committed
    generation; materializes it through the node-local daemon before launch;
    always uses a fresh durable attempt/process; permits native resume only
    under static compatible provider proof and a complete server-owned effect
    journal; otherwise uses workspace-only, checkpoint-zero, or durable manual
    review. The recovery prompt explicitly requires reconstruction of
    ephemeral process, socket, credential, and mount state.
  - Verification: focused recovery, provider, runner, runtime, daemon,
    Jetbridge, AgentStep, and ATC composition suites passed. No repository
    source or source-derived image was uploaded to Borg.
  - Review: formal Terra blocking-only round 1 found no Critical, High, or
    acceptance-blocking issue. No correction round was required.
  - Status: **Accepted**.
- [x] Task 18 — recovery telemetry/retention
  - Commit: `986f1e591d feat(agent): attribute recovery data to attempts`
  - Behavior: migrations 1773106146-47 persist exact-attempt metrics and
    transcripts while preserving legacy projections; per-attempt cumulative
    deltas, aggregate projection, and append-only cost ledger commit atomically
    under server-owned attribution; one terminal transcript owns the legacy
    view; active replacement attempts pin exact source archives; terminal
    cleanup observes FK order; bounded telemetry, alerts, and the recovery
    operations guide cover interruption through restore/manual review.
  - Verification: focused DB specifications passed 28/28; new migration and
    legacy-upgrade focuses passed; API, exec, metric, ATC command,
    artifact-daemon, generated-fake, and chart-alert suites passed; Helm lint
    and diff hygiene passed.
  - Review: formal Terra blocking-only round 1 found no Critical, High, or
    acceptance-blocking issue. No correction round was required.
  - Status: **Accepted**.
- [x] Task 19 — full verification and residue audit
  - Implementation commit: `4f49252ce9 test(agent): prove final migration and
    residue contract`;
    the ignored implementer evidence is in `task-19-report.md`.
  - Added one serial migration-package regression that proves both fresh
    installation and exact `1773106138` upgrade reach embedded head
    `1773106148`, with `CurrentVersion()`, `SupportedVersion()`, and
    `JETBRIDGE_VERSION` pinned together.
  - Verification: serial migration package and migrate-preflight direction
    suite passed; `make test-unit` and Helm lint passed. The first
    `make test-dev-mcp`, Fly integration, and ATC integration attempts were
    environment-blocked by host volume exhaustion. The independent review found
    no Critical, High, or Important issue but treated those results as
    acceptance blockers. After the parent coordinator removed only the obsolete
    accepted-Task-6 cache and confirmed 2.2 GiB free, iteration 2 reran exactly
    those three targets once, serially: dev-MCP passed all three packages, Fly
    integration passed 666/666, and ATC integration passed 24/24. No space
    error recurred; no unrelated target or gate was rerun.
  - Docker was unavailable, so local Hangar/Kubernetes targets were correctly
    prerequisite-gated. One read-only Borg inventory found only a mutable
    `registry.home/jetbridge:latest` daemon image, not an approved digest or
    recovery environment; the two live recovery proofs are environment-pending
    and made no cluster/image/source mutation.
  - Frozen migration diff, checksums, `git diff --check`, and direct-publisher
    residue gates passed. The sole retired publisher trace is the one
    fail-closed Helm `agentPublisherGateway` tombstone stanza.
  - Status: **Accepted in independent whole-branch review iteration 2.**

## Current milestone acceptance

- `make test-unit`: passed.
- `make test-dev-mcp`: all three packages passed in verification iteration 2.
- `make test-fly-integration`: 666/666 specs passed in verification iteration
  2.
- `make test-integration`: 24/24 specs passed.
- `helm lint deploy/chart`: passed with informational chart messages only.
- Serial migration-package and migrate-preflight direction checks passed.
- Frozen migration immutability/checksums, diff hygiene, and retired-authority
  residue gates passed.
- Merge-preflight revision-3 focused suites: passed.
- Independent whole-branch review found no Critical, High, or Important code
  defect. Review round 2 confirmed the recovered broad-suite evidence resolves
  the disk blockers and that Docker/Borg live proofs are properly
  prerequisite-gated and environment-pending.
- Status: **All scoped implementation tracks and Task 19 are accepted.**
