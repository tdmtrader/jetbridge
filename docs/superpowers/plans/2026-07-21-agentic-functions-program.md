# Agentic Functions Program Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved Jetbridge Agentic product model: versioned workflow functions execute visible Concourse DAGs over immutable, typed snapshots; every declared output is validated and sealed; operational runs and experiments share one durable lineage model; tickets remain an optional work-item adapter.

**Architecture:** Add a semantic control plane above the existing Concourse artifact and plan engines. A content-addressed snapshot layer owns immutable values and lineage. A durable workflow-run layer pins workflow definitions, signatures, rendered plans, and snapshot bindings independently of disposable pipeline templates. Workflow schema version 3 embeds ordinary `atc.Step` plans with typed boundary annotations. A generic binder creates normal one-shot pipeline runs. Type-driven projectors feed the existing review and diff surfaces. Experiments invoke the same binder against pinned fixtures and evaluators.

**Tech Stack:** Go 1.25, PostgreSQL, Concourse ATC plan/exec engine, Jetbridge artifact-daemon and Kubernetes runtime, Elm 0.19, Ginkgo/Gomega, standard `testing`, Fly CLI.

## Global Constraints

- Preserve schema versions 1 and 2 and their current ticket renderer until a version-3 dogfood workflow has equivalent behavior. Compatibility code must be labeled and isolated; it must not leak ticket or `workspace` assumptions into version 3.
- The filesystem tree is the execution ABI. Snapshot semantics live above `runtime.Artifact`; do not introduce a second pod-to-pod file-transfer system.
- A snapshot is visible only after its bytes are durably stored and one database transaction commits every required node output, its production record, and input lineage. Public workflow-port rows remain hidden candidates until terminal reconciliation atomically promotes the complete result set together with successful run status.
- Candidate outputs from a failed process or a malformed/incomplete node output set are never registered as typed artifacts. Values sealed by an earlier successful node remain durable intermediates if the overall function later fails, but are never exposed as public workflow outputs.
- Invocation telemetry (`agent_run_metrics`, flight events, logs, cost, retries) remains separate from semantic snapshots.
- Snapshot and workflow-run history must survive build, pipeline instance, and template deletion. Foreign keys to ephemeral Concourse rows therefore use `ON DELETE SET NULL`, or the durable row copies the required identity. Every snapshot production carries immutable team and principal attribution, and every readable snapshot has an explicit durable team grant.
- `review/v1`, `repository-change/v1`, `work-item/v1`, `log-bundle/v1`, and `measurements/v1` are authoritative server-validated contracts. Agent submission tools may help produce them but cannot certify them.
- Workflow version, signature version, source content hash, concrete post-interpolation config and plan hash, input snapshot IDs, digest-resolved image/capability declarations, and origin are immutable after admission.
- External captures and publishers remain explicit DAG nodes. Transformations consume only bound snapshots; no version-3 compiler path performs a resource `get`, appends `harvest:`, or performs an implicit push.
- Type validation answers structural validity only. Evaluators answer quality.
- All new APIs enforce team visibility and use the existing route/wrappa authorization conventions. A template pipeline must not be able to load a snapshot solely by guessing its ID.
- Every migration has a tested down migration. New tables use migration numbers beginning at `1773106100`; the abandoned benchmark reservations in historical plans are superseded by this program.
- Use TDD for every behavior change. A task is not complete until its focused tests pass. Before final handoff run formatting, generated-fake checks, Elm tests, database tests, and the repository-prescribed quick suite where the environment permits.
- Preserve unrelated and pre-existing user files. Do not modify `.agents/`, `.claude/`, `AGENTS.md`, or `forge/notes/`.

## Frozen Semantic Contracts

### Type references and ports

Canonical type references use `<name>/v<positive integer>`, for example `review/v1`. Names match `[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*`. A workflow signature is identified by `(workflow name, signature_version)` and contains ordered, unique input and output ports:

```go
type TypeRef string

type Port struct {
	Name        string  `json:"name" yaml:"name"`
	Type        TypeRef `json:"type" yaml:"type"`
	Optional    bool    `json:"optional,omitempty" yaml:"optional,omitempty"`
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
}
```

Implementations may change without changing `signature_version` only when all port names, types, and optionality remain identical.

Snapshot and workflow-run primary keys are signed 64-bit database integers, but every new HTTP representation and template parameter encodes them as quoted base-10 strings. Go uses validated `SnapshotID` and `WorkflowRunID` wrappers over `int64`. JavaScript/Elm never receives these identifiers as JSON numbers, so identity remains exact above `2^53`.

### Snapshot identity

`agent_snapshots` stores one semantic value per `(type_name, type_version, digest)`. Production history is separate so two invocations that produce identical bytes retain distinct provenance. Source adapter metadata such as resource locator/version, ticket revision, uploader, and capture reason belongs to the production occurrence, not the deduplicated snapshot row. Type-derived intrinsic metadata such as a repository tree's `HEAD` may be cached on the snapshot because it is a deterministic function of the canonical bytes. The canonical digest is SHA-256 over a deterministic tar stream built from sorted slash-normalized paths, normalized safe permission bits, fixed timestamps/ownership, symlink targets, and file bytes.

The canonical archive rejects absolute paths, `..`, duplicate paths, hard links, devices, sockets, FIFOs, setuid/setgid bits, and symlinks that resolve outside the root. Default limits are 100,000 filesystem entries and 10 GiB of regular-file content; ATC flags may lower these limits.

### Persistence tables

Migration `1773106100` creates:

- `agent_snapshots`: immutable type, digest, byte/file counts, representation media type, intrinsic metadata, content availability state (`available` or `expired`), and creation time. Expiry never deletes this manifest.
- `agent_snapshot_locations`: immutable storage driver/key/node locations; one content value may have several replicas.
- `agent_snapshot_staged_uploads`: pre-upload digest/team/attempt records used to recover content written before a seal transaction commits.
- `agent_snapshot_productions`: producer build/plan/attempt/step/output, team/principal, source adapter metadata, plus workflow definition and workflow run; unique per invocation output.
- `agent_snapshot_lineage`: production/input-port/input-snapshot edges.
- `agent_snapshot_grants`: durable team access grants with grantor/reason audit; value deduplication never implies cross-team visibility.
- `agent_snapshot_retention_claims`: independent binding/grant/pin claims with class, optional expiry, actor, and audit reason. Effective retention is the strongest active claim; removing one actor's pin never weakens another reference.
- `agent_workflow_runs`: durable operational or experimental invocation identity, team, definition ID, optional function node ID, copied workflow/signature/hash fields, parameterized template config/hash, concrete post-interpolation config/plan/hash, origin kind/reference, creator, status, pipeline-run linkage, and timestamps.
- `agent_workflow_run_snapshots`: named input bindings and staged output candidates with unique `(workflow_run_id, direction, port_name)`; `promoted_at` is set for inputs immediately and for the complete public output set only in the successful-finalization transaction.

Migration `1773106101` adds indexed `schema_version` and `signature_version` columns to `agent_workflow_definitions`. Existing rows backfill from stored definitions in the Go migration; versions 1 and 2 receive signature version `0`.

Migration `1773106102` adds first-class snapshot upload occurrences and idempotency after the initial snapshot schema shipped. Migration `1773106103` makes workflow completion reconciliation restart-safe while preserving copied execution provenance. Migrations `1773106104` through `1773106110` add ticket revisions, review/diff projections, generic outcomes, ticket workflow-run links, durable human waits, and explicit publication audit. Migration `1773106111` creates experiments, variants, fixtures, cells, and evaluator-run links as specified in the experiments plan. Migration `1773106112` adds concurrency-safe experiment budget reservations; migration `1773106113` separates stable wait-resolution actor identities from mutable display names and records the first accepted answer intent; migration `1773106114` adds atomic worst-case spend reservations for ordinary workflow runs. Migrations `1773106115` and `1773106116` strengthen publication-backed outcomes and append-only experiment actor audit, `1773106117` stages public outputs until atomic successful promotion, `1773106118` separates the provider-idempotent publication operation from each authorized workflow occurrence, `1773106119` freezes terminal scorecards, `1773106120` enforces same-experiment fixture, variant, cell, and budget ownership, and `1773106121` adds exact active-run retention for workflow inputs and internal typed values with atomic terminal release. Every migration task advances and executes `jetbridgeHeadMigration` coverage.

### Seal transaction

The execution seam is a batch operation:

```go
type SealRequest struct {
	BuildID             int
	TeamID              int
	TeamName            string
	CreatedBy           string
	PlanID              string
	Attempt             string
	StepKind            string
	StepName            string
	WorkflowDefinitionID *int
	WorkflowRunID       *WorkflowRunID
	Inputs               map[string]SnapshotRef
	Outputs              []CandidateOutput
}

type OutputSealer interface {
	Seal(context.Context, SealRequest) (map[string]SealedOutput, error)
}
```

The sealer collects and validates every required candidate before publishing anything. After canonicalization determines a digest, it acquires PostgreSQL session advisory locks for every unique digest in lexical order and holds them across staging, upload, and `CommitSealBatch`. While holding those locks it commits durable staged-upload rows, writes canonical archives to immutable storage keys, and calls one database transaction that consumes the staged rows and creates production attribution, team grants, retention claims, lineage, and bindings. Lifecycle recovery takes the same digest lock across its final database recheck, external deletion, and stage cleanup; it skips deletion when the digest is committed or any unexpired sibling stage exists. A crash releases the database session locks and leaves discoverable staged rows. This single lock protocol prevents a commit from publishing locations after GC deleted their shared digest bytes.

### Storage

The first storage driver is `jetbridge-daemon-v1`. It stores canonical tar archives at `snapshots/sha256/<hex>.tar`, outside `/steps`, so the step sweeper and container reaper never delete them. ATC writes to a deterministic sorted set of daemon pods up to `--agent-snapshot-replication-factor` (default 2) and records each acknowledged location. Reads try recorded replicas and then live peers. A lifecycle component removes unreferenced/orphan content after the grace period, expires only claims whose deadlines passed, derives effective retention, and repairs under-replicated referenced content. A single-node cluster is supported but reports one replica; the UI and API expose replica count.

### Workflow schema version 3

Version 3 is a strict function overlay around one ordinary Concourse job plan:

```yaml
schema_version: 3
name: code-review
signature_version: 1
description: Review one repository state relative to another.
inputs:
  - name: before
    type: repository/v1
  - name: after
    type: repository/v1
outputs:
  - name: review
    type: review/v1
    from: review
capabilities:
  dev:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/dev-mcp@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
      ports: [{containerPort: 7780, protocol: TCP}]
plan:
  - agent: review
    function_id: review
    prompt: Compare the declared repository snapshots and submit review.json.
    budget_slice_usd: 5
    capabilities: [dev]
    inputs: [before, after]
    outputs: [review]
    input_types:
      before:
        type: repository/v1
      after:
        type: repository/v1
    output_types:
      review: review/v1
```

Version 3 has exactly one job because build artifacts are build-scoped. Its transformation plan has no ambient live-read surface: top-level resources and variable sources, `get`, and `load_var` are rejected. External resource versions enter through the resource-capture adapter as exact snapshots; task image resource types remain available only to resolve and freeze the task image dependency. Every typed task or agent node authors a stable, definition-unique `function_id` plus declared `{type, optional}` input configs and typed outputs; exact declarations must cover every runtime input and output. IDs are never inferred from display names. Top-level workflow ports retain source order. Internal-node signature ports are ordered lexicographically by artifact name before hashing, giving map-backed declarations one canonical order.

The compiler prepends authorized `load_snapshot:` steps for bound function inputs, expands named capabilities into literal digest-pinned sidecar configurations, and compiles each capability's single declared TCP port into a localhost MCP endpoint. The runner supplies only that complete endpoint set to Claude with strict MCP configuration; authored `*_MCP_URL` overrides and duplicate selected ports are rejected. It stages workflow outputs for durable retention and later atomic promotion, validates type flow through `do`, `in_parallel`, `try`, retry, timeout, and hook wrappers, and emits `atc.Config{Template: true}`. It rejects duplicate producers and cross-branch typed/legacy races in parallel branches. Public producers inside retry are rejected until candidates can be tied to the winning attempt. It currently rejects `across` entirely at the immutable workflow boundary: Concourse expands its uninterpolated template into new runtime plans and plan IDs after the workflow evidence has been frozen, so admitting even an input-only `across` would make “exact execution provenance” untrue. Ordinary pipelines retain `across`; workflow functions may enable it only after every expanded subplan is durably captured and verified. A node-extraction compiler can render one `function_id` as a one-node harness with the same declared signature for step-level experiments.

Admission forces every schema-v3 task and agent transformation into hermetic
execution and refuses privileged tasks. On Jetbridge workers the pod receives
the server-owned `concourse.ci/hermetic=true` label, no service-account token,
and a default-on deny-egress NetworkPolicy. Operators explicitly configure the
minimal model endpoint or egress-proxy rules through
`networkPolicy.hermeticEgressTo`; the chart adds no implicit DNS, metadata, or
HTTPS escape. Capability sidecars receive neither the run principal nor the
model credential. Capture/resource and publisher nodes remain the only
live-system boundaries.

### Workflow run admission

The generic binder accepts:

```go
type BindRequest struct {
	WorkflowName string
	Version      *int
	FunctionID   string
	Inputs       map[string]SnapshotID
	Origin       Origin
	CreatedBy    string
	IdempotencyKey string
}
```

It resolves and pins the immutable definition or named internal function, validates exact port coverage/type/team access, compiles the rendered template, inserts the durable workflow run, input bindings, and exact active-run retention claims atomically, creates a target-specific immutable template, creates the ordinary pipeline run, and links the IDs. Full workflow templates are named `agent-workflow-<safe-name>-v<version>-<target-config-hash-prefix>`; extracted nodes are `agent-function-<safe-name>-v<version>-<function-id>-<target-config-hash-prefix>`. Snapshot template parameters are decimal strings. The durable row retains the parameterized template, exact post-interpolation instance config, and—after Concourse planning—the actual build plan plus resolved dependency identities. Repeating the same team/idempotency key returns the existing workflow run.

### Durable experiment safety

Experiment definitions admit no more than 16 variants, 256 fixtures, 1,000
repetitions, 2,000 materialized cells, and 32 distinct measurements. Checked,
overflow-safe cross-product arithmetic runs before PostgreSQL
`generate_series`; cell-list and scorecard queries carry the same hard bound,
and the team experiment index uses stable `(created_at, id)` keyset pages of at
most 100 records. The scorecard validates per-cell and global metric
cardinality before aggregation, uses a fixed bounded deterministic bootstrap,
and freezes terminal cell/telemetry evidence so late metrics cannot rewrite a
benchmark. Budget-skipped cells remain distinct from platform errors and
suppress winner recommendations.

The validation endpoint calls the same non-mutating preflight helper used by
start: immutable target authority, trusted rendering and target-config hashes,
static candidate/evaluator budget slices, and retained fixture availability.
The API knows whether the reconciler set is enabled and rejects validation and
start with service unavailable when it is not. Preflight also rejects
`publish_snapshot` in either target so experiments remain effect-free.

Runner and evaluator requests carry an experiment/cell/phase gate into the
ordinary workflow-run allocator. Its short transaction locks and verifies the
running parent and cell before returning an idempotent child or inserting the
durable child row. `Cancel` takes the conflicting parent lock. Rendering,
secret preparation, and build creation happen outside that transaction, and
exact cell association is a separate short write allowed during cancellation.
Thus cancel-before-allocation fails closed, while allocation-before-cancel is
already discoverable by deterministic experiment origin. Cancellation can
finalize as soon as all discovered runs are terminal, without a 60-second
orphan-race delay or a multi-connection admission dependency. Post-allocation
platform faults keep the cell retryable on its deterministic idempotency key;
budget denial terminalizes as `skipped_budget`, while invalid admission is a
contract failure. The runner makes reservation decisions serially in the
repetition-first rotated cell order before executing admitted children
concurrently. One cell reservation covers both child runs under the global
cap; exact child slices are still validated but are not double-reserved.

### Built-in snapshot contracts

The authoritative registry contains exactly 17 named contracts:

- `opaque/v1`: any safe, non-empty canonical tree.
- `repository/v1`: a Git work tree with a valid `.git`, a full `HEAD` commit, no unsafe paths, and captured repository/commit metadata in the snapshot manifest.
- `repository-change/v1`: `change.json` with schema version, repository ID, full base SHA, required full result-tree SHA, payload digest, and one `git-tree`, `patch`, or `bundle` representation. Commit-bearing `git-tree` and `bundle` changes also declare a full result commit SHA; a `patch` omits that field because it proves only the resulting tree. Validation proves the result against the declared base snapshot.
- `review/v1`: `review.json` strictly decoded into the existing review schema with exact version, nested field validation, score bounds, unique finding IDs, safe paths, valid severities/categories, and consistent test totals.
- `work-item/v1`: `work-item.json` with adapter, external ID, immutable revision, captured timestamp, title/body/state, and optional spec/plan/comment revision data.
- `log-bundle/v1`: at least one safe regular file plus optional `metadata.json`; it never contacts the source system during execution.
- `measurements/v1`: `measurements.json` containing named numeric metrics, unit, direction (`higher` or `lower`), evaluator version, validity flag, and explanations.
- Engineering request/report contracts: `upgrade-request/v1`, `upgrade-report/v1`, `validation-report/v1`, and `gate-results/v1` use strict versioned JSON documents.
- Audit/diagnosis contracts: `database-snapshot/v1`, `deployment-snapshot/v1`, `audit-findings/v1`, and `diagnosis/v1` use safe trees with strict metadata documents and no live-system access.
- Human interaction contracts: `question/v1` and `human-answer/v1` contain immutable prompt/context and attributed answer data. A visible `await_snapshot:` step parks on a durable workflow wait and materializes the answer snapshot when an authorized human resolves it.

## Workstream Plans

Execution order is dependency order, not team ownership:

1. [Snapshot Core and Typed Boundaries](2026-07-21-snapshot-core-and-typed-boundaries.md)
2. [Workflow Version 3 and Generic Runs](2026-07-21-workflow-v3-and-generic-runs.md)
3. [Adapters, Projections, and Explicit Delivery](2026-07-21-adapters-projections-and-delivery.md)
4. [Experiments and Workflow-First UI](2026-07-21-experiments-and-workflow-ui.md)

Each workstream ends in a usable vertical checkpoint. The compatibility dispatcher stays operational throughout. Commits are small and scoped to one checklist task so regressions can be bisected without discarding the program.

## Program Acceptance Tests

- [ ] Upload or capture two snapshots, invoke a version-3 review workflow, observe ordinary Concourse DAG execution, receive one sealed `review/v1`, and render it through the existing review UI projection.
- [ ] Run a repository-change workflow, delete its producer pod, build, pipeline instance, and template, restart ATC, then materialize the sealed change in another workflow and render its bounded diff.
- [ ] Prove a malformed one-of-two required output from one producer leaves zero typed outputs visible and no partial lineage row for that producer; separately prove a later workflow failure leaves earlier snapshots as durable intermediates but promotes zero public workflow outputs.
- [ ] Promote a new compatible workflow version, retain and compare all prior runs beneath the stable workflow identity, and demonstrate that old runs still expose their exact rendered config and snapshots.
- [ ] Dispatch a ticket through a version-3 workflow and prove the run consumes the captured ticket revision even after the mutable ticket changes.
- [ ] Execute two prompt/capability variants across the same pinned fixture set and repetitions, run the same pinned evaluator, and display quality distribution, variance, cost, latency, platform failures, and human-intervention count.
- [ ] Prove publishers are explicit visible nodes and that a review-only workflow has no repository push or ticket mutation behavior.

## Known Environment and Delivery Risks

- `pg_isready` at port 5432 is not a valid repository test prerequisite: ATC database suites launch isolated PostgreSQL instances on ports 5434+ through `atc/postgresrunner`. Homebrew PostgreSQL 14.19 `initdb`, `postgres`, and `psql` are available; database tests need unsandboxed loopback/process execution, not a manually started shared service.
- The managed sandbox blocks loopback listeners used by `httptest`; networked test suites must run with approved unsandboxed test execution.
- Node-local hostPath with replication protects against one node loss but is not equivalent to object-store durability across total cluster loss. The storage interface and manifest deliberately permit a future S3/GCS driver without changing snapshot identity.
- Adding a core `load_snapshot:` step extends the `StepVisitor` exhaustiveness surface and generated fakes. Compile failures are expected until every visitor and planner is updated in the same task.
- Retry execution uses attempt-scoped artifact repositories, but public candidates are not yet attempt-scoped. Schema version 3 therefore rejects public-output producers beneath `attempts:` while retaining retries for internal typed values.
- Historical benchmark plans reserve the same migration range and claim implementations that do not exist. This program is authoritative; the documentation task marks those plans superseded.
