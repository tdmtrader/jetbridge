# Agentic Workflows as Functions over Snapshots

- **Date:** 2026-07-21
- **Status:** Approved product direction; implementation active on `codex/agentic-functions`
- **Audience:** The Jetbridge team and future contributors to the Concourse-native agentic workflow work
- **Assessment baseline:** `integration/audit-all` at `357152e6d1`, the 2026-07-21 Jetbridge capability and lifecycle audits, and the implementation checkpoints linked from the program plan

## Executive thesis

Jetbridge Agentic extends Concourse from deterministic automation into stochastic automation without abandoning Concourse's functional model.

A Concourse resource version is an immutable input. A task consumes materialized inputs and produces artifacts. A pipeline composes those transformations as a visible DAG. An agent changes the predictability of a transformation, but not the surrounding model:

```text
immutable snapshots
        ↓
versioned workflow implemented as a visible DAG
        ↓
validated, immutable output snapshots
```

The central product primitive is therefore not a ticket, a chat session, an agent process, or an MCP server. It is a **versioned workflow function over immutable snapshots**.

```text
workflow:
  declared input snapshot types
  -> Concourse DAG of deterministic, stochastic, human, and publishing functions
  -> declared output snapshot types
```

The first users are an engineering team already using Concourse as its primary CI system. They should be able to invoke concrete engineering workflows such as dependency upgrades, code review, anonymization audits, and small fixes, observe them beside their existing pipelines, and improve them through controlled experiments. Wider adoption is useful but is not a product constraint.

The distinguishing promise is:

> Jetbridge makes agentic engineering processes composable, inspectable, and empirically improvable in the same way Concourse makes delivery processes composable and inspectable.

## Product boundaries

Three related components remain distinct:

- **Jetbridge** is the Kubernetes implementation of the Concourse execution runtime.
- **Agentic Workflows** is the Concourse-native product described here: workflow functions, snapshots, runs, typed outputs, experiments, and their control surfaces.
- **`ci-agent`** is the older standalone phase runner. It is an implementation source and compatibility concern, not the product model.

Keeping the runtime and Agentic Workflows in one monorepo remains sensible because agentic execution uses Concourse plans, artifacts, auth, APIs, UI, and the Jetbridge pod runtime. They should nevertheless keep separate readiness and release boundaries.

## Alternatives considered

### Ticket-centric development platform

In this model the product is a ticket system that dispatches agents and happens to use Concourse underneath. It provides a familiar development experience, but it makes tickets mandatory, privileges one spec-plan-implement lifecycle, and obscures the reusable Concourse execution model.

**Disposition:** Retain tickets as one work-item adapter and one useful UI, but do not make them the workflow contract.

### Agent steps added directly to ordinary pipelines

In this model users author normal pipelines containing `agent:` steps. It preserves maximum Concourse compatibility and is already partly implemented. On its own, however, it does not solve immutable workflow history, stable workflow signatures, one-shot runs, durable outputs, controlled variants, or experiments.

**Disposition:** Retain `agent:` as an execution primitive, but place it inside versioned workflow functions and one-shot runs.

### Versioned workflow functions over snapshots

In this model a workflow has a stable identity, immutable versions, a declared input/output signature, and a Concourse DAG implementation. Work items, resources, prior workflow outputs, and explicit invocations can all bind snapshots to a run. Experiments compare interchangeable workflow or step implementations against fixed inputs and evaluators.

**Disposition:** Chosen. This keeps Concourse's functional character while adding the missing semantics required by stochastic execution.

## Semantic model

### Snapshots are immutable values

A snapshot is an immutable, addressable value supplied to or produced by a function. Examples include:

- a repository at a commit;
- a repository change relative to a base;
- a ticket at a particular revision;
- a bounded dump of logs;
- a database schema and anonymized sample;
- an upgrade request;
- a review finding set;
- a diagnosis or report.

“Subject” describes the role played by one or more input snapshots. It is not a special storage class. A repository may be the subject of an upgrade. A log dump may be the subject of a diagnosis. Two repository snapshots may jointly be the subject of a review.

### Filesystem trees are the execution ABI

Concourse already has an effective universal calling convention: inputs and outputs are mounted filesystem trees.

```text
semantic value       Snapshot
physical execution   Artifact mount
external discovery   Resource version + get
external publication Put
```

A snapshot is not identical to a Concourse resource:

- A **resource** discovers and publishes versions in an external system.
- A **resource version** identifies an immutable external value.
- A **get** materializes that value as an artifact.
- A **task or agent output** can produce an artifact without creating an external resource version.
- A **snapshot** is the semantic immutable value represented by either materialized form.

Resources are therefore one adapter for introducing and publishing snapshots, not the universal value model.

### Mutable entities produce snapshots

A Jira ticket illustrates the distinction:

```text
ABC-123             mutable work item
ABC-123 revision 17 immutable ticket snapshot
Jira query/adapter  possible discovery and capture mechanism
```

At dispatch, a workflow binds one exact ticket revision. Later edits do not mutate the active run's input. If an agent asks a question and a human answers between steps, those interactions create later ticket revisions or dedicated message/answer snapshots. A later step can consume the new revision while all earlier lineage remains intact.

Closing a ticket affects whether new work may be dispatched. It does not invalidate historical snapshots or runs. “Ticket at the top of the queue” is scheduling state, not a resource version.

### Functions transform snapshot tuples

A function declares input and output ports:

```text
f : (S1, S2, ... Sn) -> (T1, T2, ... Tm)
```

Examples:

```text
upgrade:
  (Repository, UpgradeRequest)
  -> (Repository, UpgradeReport)

review:
  (RepositoryBefore, RepositoryAfter)
  -> Review

audit-anonymization:
  (Repository, DatabaseSnapshot)
  -> (AuditFindings, RepositoryChange?)

diagnose:
  (LogSnapshot, DeploymentSnapshot)
  -> Diagnosis
```

A deterministic implementation normally produces one output for a given input. A stochastic implementation produces a distribution of possible outputs:

```text
f_v : I -> Distribution(O)
```

This difference does not change how functions compose. It changes how their quality must be measured.

### Workflows are functions implemented as DAGs

A workflow is a function whose implementation is a visible Concourse DAG of other functions:

```text
version-upgrade:
  (Repository, UpgradeRequest)
  -> PullRequest
```

One version might use a single agent. Another may use an implementation agent, deterministic tests, several reviewers in parallel, a reducer, a human checkpoint, and a PR publisher. They remain variants of the same workflow while their public signatures remain compatible.

Traditional Concourse composition semantics remain the model. The current
schema-version-3 boundary admits `in_parallel`, `do`, `try`, retry, timeout,
and hook forms while preserving their visible component nodes. It temporarily
rejects `across`: Concourse expands that template into runtime plans and plan
IDs after admission, so exact frozen provenance requires durable capture of
every expansion before the wrapper can be enabled safely. Combining nodes
never makes value-changing or side-effecting behavior implicit.

### Workflow identity and versions

A workflow name has stable identity. Every imported definition creates an immutable, content-addressed version. Every run pins:

- the workflow version and content hash;
- its bound input snapshot identities;
- the exact plan produced from the definition;
- execution dependencies needed for provenance, including the server-selected
  agent runtime image and every capability image as exact OCI digests.

Promoting a version changes the default used by future runs. It never rewrites prior definitions, runs, inputs, or outputs. History is grouped beneath stable workflow identity across versions so operators can compare behavior without losing the past when definitions change.

Signature changes are versioned explicitly. Compatible implementation changes can be compared directly. An incompatible input/output contract is a new signature version even when the human-facing workflow name remains related.

## Typed outputs

### Write, validate, seal

A function does not directly create a trusted snapshot. It writes candidate bytes into declared output ports. Jetbridge then performs a boundary transition:

```text
function writes candidate bytes
        ↓
output contract validates representation
        ↓
Jetbridge seals immutable typed snapshot
        ↓
downstream consumers receive snapshot
```

Each output contract separates three concepts:

```text
type            review/v1
representation  directory containing record.json and optional content/
snapshot        one validated immutable review with a digest and lineage
```

Or:

```text
type            repository-change/v1
representation  record.json plus a patch, Git bundle, or tree beneath content/
snapshot        one change with a declared base and resulting state
```

The semantic repository-change contract should not require one transport encoding. It identifies at least the repository, base snapshot, resulting snapshot, and content needed to reconstruct or materialize the result. A structural validator proves that the representation is coherent.

### Sealed records and exact subjects

The analytical and content-bearing domain values use a common sealed-record
envelope:

```yaml
record_version: 1.0.0
type: review/v1
schema: sha256:<frozen-contract-descriptor-digest>
subjects:
  - id: primary
    role: primary
    input: change
    type: repository-change/v1
    digest: sha256:<exact-input-snapshot-digest>
body:
  conclusion: accept
  summary: No blocking findings.
  findings: []
```

The six built-in record types are `review/v1`, `diagnosis/v1`,
`validation/v1`, `repository-change/v1`, `selection/v1`, and
`measurements/v1`. `validation/v1` is the single execution-observation
contract; it replaces the earlier `validation-report/v1` and
`gate-results/v1` split.

The record contains stable value identity only. Local snapshot IDs, workflow
run IDs, producer identity, model, timing, and attempt provenance remain on
the production occurrence outside the bytes. Subject type and digest bind a
record to the exact values it judges, explains, validates, changes, selects,
or measures. Jetbridge exposes those values and the declared output
type/schema to the producer, then checks them again at sealing; producer
claims never create authority.

Record entity sets are sorted by stable IDs before submission. Evidence
anchors resolve through declared subjects instead of embedding unrelated
snapshot references. `selection/v1` records a decision but resolves to the
already-sealed selected input, so selection never copies or reseals candidate
content.

This record layer is also the boundary for a future Concourse prototype
runtime. A prototype message may consume sealed records and emit candidate
records, but records are immutable values rather than mutable prototype
instances. Prototype configuration/cloning and semantic workflow data
therefore remain distinct.

### Validity is not quality

Boundary validation answers whether a function fulfilled its declared type contract:

- Does a review contain the required structured fields?
- Does a patch apply to its declared base?
- Are paths safe and within the subject?
- Are required outputs present and within declared bounds?

Evaluation answers whether a valid output is good:

- Did the review find the real defects without excessive noise?
- Does the repository change satisfy the request and pass its tests?
- Is the diagnosis accurate and useful?

A malformed output is a contract failure. A valid but poor output is an evaluable result.

Submission tools may help an agent produce the correct format, but Jetbridge is the authoritative validator. An agent cannot self-certify its output.

### Atomic completion and lineage

A function invocation succeeds only after all required outputs validate and are sealed. Sealed values remain durable intermediate evidence as soon as their producer commits, but public workflow-port bindings are staged candidates. Jetbridge promotes the complete public result set in the same transaction that marks the run successful. Failed, errored, aborted, and still-running invocations therefore expose no public outputs, even when one producer emitted a valid intermediate value.

Every sealed snapshot records lineage to:

- the invocation and function/workflow version;
- its input snapshots;
- its type contract version;
- its content digest.

Every declared step output is sealed at its DAG boundary. Workflow outputs and pinned benchmark fixtures receive durable content retention. Intermediate output manifests and lineage are durable; physical intermediate content may expire under an explicit retention policy unless pinned. Scratch files outside declared output ports are not snapshots.

While a workflow run is nonterminal, each internal snapshot has a non-expiring run-scoped retention claim. Terminalization releases those claims atomically; the configured binding-retention window then controls physical content expiry without weakening immutable manifests or lineage. Long human waits and recovery can therefore never outlive their intermediate values.

An authored transformation never performs a live read. Resource checks, Jira/API reads, log collection, and database sampling happen in explicit capture adapters before invocation and bind exact snapshots. Schema-version-3 functions consequently reject top-level resources and variable sources, `get`, and `load_var`; custom resource types remain available only to resolve a task image into the frozen execution dependency set. Every task and agent node in a v3 function has a stable `function_id` and exact type coverage for every declared input and output; untyped transformation islands are invalid. The executor independently repeats coverage checks after resolving file-backed task configuration.

Ordinary retry scopes retain their Concourse behavior for internal typed values. Public outputs directly produced inside `attempts:` are currently rejected at admission: durable public candidates need attempt-aware promotion before Jetbridge can safely distinguish a failed attempt from the winning attempt. Authors may retry an internal producer and consume its successful value in a later, non-retried public-output node.

Because Concourse creates output mounts before a task or agent starts, mount
existence cannot signal whether an optional output was produced. Jetbridge
therefore supplies an explicit, collision-free name-to-marker-path mapping on a
separate control mount. A producer creates the empty marker only after its
optional value is complete. An unmarked output is absent; a malformed marker
is a contract failure; control markers are never part of snapshot content.

### Outputs and run metadata remain separate

Function outputs are semantic values such as repository changes, reviews, findings, or diagnoses. Execution logs, token usage, duration, retries, model identity, and platform errors describe an invocation. They are retained as run metadata and may themselves be captured later as deliberate workflow inputs, but they are not automatically semantic outputs of the function that emitted them.

## Consumers of sealed outputs

Once sealed, a snapshot may fan out to three kinds of consumer.

### Projections

Projections do not change the value:

- render a review in the UI;
- show a repository change as a diff;
- index findings for queries;
- store a large payload in object storage;
- retain summary columns in PostgreSQL.

Jetbridge may select projections automatically from snapshot type. Database rows, object-store documents, and UI renderings are representations of the same canonical snapshot, not independent truth.

### Transformations

Transformations produce new snapshots and are explicit DAG nodes:

- rebase a repository change onto a newer repository snapshot;
- reduce multiple reviews into one;
- apply requested revisions;
- convert one contract version to another.

Because transformations can fail or materially change values, they must not hide in output handling.

### Publishers

Publishers cross an external side-effect boundary and are explicit DAG nodes, analogous to Concourse puts:

- push a branch or open a PR;
- merge;
- update Jira;
- publish an audit report;
- set an external CI gate.

Publishers consume validated snapshots. A workflow may place publishers inside composition wrappers, but their presence and results remain visible.

A direct merge is a stronger boundary than opening a branch or pull request.
It must consume durable approval evidence tied to the exact workflow run,
`await_snapshot` resolution, authenticated resolver, answer snapshot, change
snapshot, publisher, mode, destination, complete parameter map, base revision,
and approval-policy version. Jetbridge, not an agent, synthesizes that question
after the change has been sealed and the run identity is known. Merely
attributing the build creator or accepting an agent-authored question is not
approval. Implementations without that complete linkage must fail closed for
merge while still permitting reviewable branch and pull-request publication.

## Capabilities and agents

An `agent:` node is one implementation kind for a function. Deterministic tasks, human checkpoints, evaluators, adapters, and publishers are peers in the DAG.

Agents receive only declared inputs and capabilities. A capability is a named, versioned interface exposed to a function. MCP is a useful protocol and sidecar packaging mechanism, but it is not the semantic product primitive.

Transformation nodes never receive ambient access to live systems. A resource
or capture adapter may read a live system and seal the result as an input
snapshot; a publisher may perform an explicit outbound side effect. Between
those boundaries, capabilities operate only on mounted snapshots and local
execution state. Human interaction is likewise a visible durable wait, not an
agent tool that silently contacts a person.

Examples include:

```text
repository.build
repository.test
repository.affected-components
snapshot.inspect-work-item
snapshot.query-logs
snapshot.submit-review
```

The existing `dev-mcp` idea remains valuable as a standard repository capability pack. The workflow model must also permit custom capability contracts rather than freezing all tools into `dev`, `platform`, and `gateway` roles.

For the Kubernetes runtime, this boundary is fail-closed. Workflow admission
forces transformation tasks and agents into hermetic pods, refuses privileged
tasks, disables service-account token mounting, and selects a default-deny
egress policy. Operators may allow only explicit model infrastructure through
that policy. Standard Kubernetes NetworkPolicy still permits traffic to the
pod's own node; Jetbridge currently relies on that exception for node-local
artifact bootstrap, so installations needing a stronger boundary must add
CNI host policy/firewall enforcement or replace that bootstrap transport. A
capability image is digest-pinned, declares one localhost TCP
MCP endpoint, receives no run principal or model secret, and is passed to the
agent only through strict generated MCP configuration. Workflow-authored
endpoint overrides and the retired privileged role names are invalid. The
contract name is durable declared identity tied to the frozen image; custom
contract conformance remains the responsibility of the corresponding
contract-test kit until a general runtime attestation protocol exists.

## Experiments and benchmarks

Experimentation is part of the core product loop because stochastic implementations cannot be improved reliably through static configuration review.

An evaluator is another versioned function:

```text
e : (workflow inputs, workflow outputs) -> measurements
```

An experiment fixes:

1. the input fixture snapshots;
2. the function signature under test;
3. the evaluator version;
4. the comparison budget and repetition policy.

It then runs candidate implementations as ordinary pinned workflow runs. A candidate can change prompts, models, capability sets, context strategy, internal DAG topology, parallelism, or deterministic support steps while preserving the tested signature.

Experiment admission is deliberately finite: at most 16 variants, 256
fixtures, 1,000 repetitions, 2,000 materialized cells, and 32 distinct
measurements. These limits bound database expansion, API/UI cell lists, stored
scorecard evidence, and deterministic bootstrap work. The team index uses
exclusive opaque keyset cursors over `(created_at, id)` and pages at most 100
experiments at a time. Validation executes the
same authoritative target resolution, rendering, static budget proof, and
fixture-availability preflight as start without mutating the draft. Validation
and start fail closed when no experiment runner is enabled. Candidate and
evaluator executables containing `publish_snapshot` are rejected so laboratory
runs cannot perform outbound effects.

Candidate and evaluator bind requests carry an experiment/cell/phase gate
into ordinary workflow-run persistence. A short allocation transaction locks
and verifies the running parent and cell before returning an idempotent child
or inserting a new durable child. Cancellation takes the conflicting parent
lock, so it commits either before allocation (which then fails closed) or
after a durable origin-addressable child exists. Rendering, secret
preparation, and build creation remain outside the lock; exact cell
association is a separate short write that may finish while cancellation is
in progress. Origin discovery closes that association window, so
cancellation and finalization need no guessed lease or grace interval and do
not require extra database-pool headroom. A platform fault after allocation
leaves the cell retryable against the same idempotency key rather than
terminalizing while an origin-addressable child is still admitting.

Budget denial becomes the distinct terminal `skipped_budget` result, while
invalid admission remains a contract failure. Candidate reservations are made
serially in the repetition-first rotated materialization order before admitted
cells execute concurrently, preventing scheduler timing from biasing a scarce
budget toward one authored variant. The cell reservation is the single
candidate-plus-evaluator liability under the deployment cap; child workflow
runs validate their exact slices but do not reserve that liability again.
Static proof allows a deterministic evaluator to run at exact candidate usage,
while a final actual overage becomes `skipped_budget` before measurements are
admitted. Scorecards are terminal-only and durably freeze the cell matrix plus
then-known selected/anomalous build telemetry. Budget-skipped matrices report
no winner, and late metrics cannot rewrite a completed benchmark.

The system reports distributions rather than only winners: quality metrics, variance, cost, latency, platform-error rate, and human intervention. Negative controls and pinned evaluators detect evaluator blindness and moving goalposts.

Operational and experimental runs share the execution substrate but have distinct views:

- the operational view shows the promoted version handling real work;
- the laboratory view shows fixtures, variants, repetitions, evaluators, controls, and comparisons;
- promotion remains an explicit human action informed by both controlled experiments and production outcomes.

## Product experience

The primary product surface centers workflows, much as Concourse centers pipelines:

- a dashboard shows named workflows and their current operational state;
- each workflow groups versions, operational runs, experiments, costs, and outcomes;
- starting a run binds concrete snapshots to the workflow's declared inputs;
- run detail shows the visible DAG, input and output snapshot lineage, projections, and invocation metadata.

A Pivotal-style board can be an excellent repository-oriented work-item surface. It assigns mutable work items to workflows and shows their progress, questions, and outputs. It is an adapter and control view over the workflow model rather than a second orchestration engine. Jira may remain the system of record while Jetbridge supplies a more useful interaction surface.

## Platform guarantees around the semantic model

The following are important Jetbridge guarantees, but they are not extra function inputs or output categories:

- exact definition and dependency provenance;
- isolated execution;
- declared credential and capability access;
- separate work failure and platform failure;
- restart-safe execution and idempotent ingestion;
- cost, time, and resource accounting;
- human authorization at consequential boundaries;
- durable history and lineage.

These properties make stochastic functions operable. They should remain outside the minimal semantic algebra of snapshots, functions, composition, and evaluation.

## Alignment with the existing implementation

This section records the implementation baseline that was audited before the
workflow-functions program began. It is intentionally historical: the
approved implementation now carries out the migration described below through
schema-version-3 workflow functions, durable snapshots and runs, explicit
capture/wait/publisher nodes, experiments, and workflow-first operator
surfaces. The reuse matrix explains why each older subsystem was retained,
adapted, isolated for compatibility, or removed from the new primary path; it
is not a list of current missing features.

### Overall verdict

There is substantial alignment below the workflow grammar and substantial misalignment in the current product-shaped control plane.

The implementation already contains many difficult primitives worth preserving: the Kubernetes execution backend, Concourse's plan engine, artifact mounting, first-class agent execution, immutable workflow versions, one-shot pipeline runs, credentials, budgets, provenance, review projections, and early benchmark architecture.

“Retain” in this assessment means the code or contract is aligned and worth carrying forward; it does not imply that every path is production-ready today. The capability audit correctly found that MCP delivery, workflow sidecars, checkpoints, and HITL still have component code without a complete renderable and deployable path.

The current end-to-end path, however, assumes one specific product:

```text
ticket -> linear agent phases -> mandatory workspace -> implicit harvest -> pushed branch
```

That shape is useful as one workflow, but it cannot remain the platform contract. The migration is therefore not a rewrite of Jetbridge or the agent executor. It is a replacement of the workflow schema and ticket-centric renderer, plus a new durable typed-snapshot boundary.

### Reuse and change matrix

| Existing area | Disposition | Analysis |
|---|---|---|
| Concourse plan/DAG engine | **Retain** | This is the foundation. Existing step types and composition wrappers already provide visible orchestration, retries, parallelism, failure hooks, and build history. |
| Jetbridge Kubernetes runtime | **Retain** | Pod execution, sidecars, artifact movement, restart behavior, and operational coverage are direct advantages. Agentic semantics should not move into the runtime. |
| Concourse resources and puts | **Retain** | They remain the standard adapters for discovering/materializing and publishing external snapshot versions. They should not be stretched into the universal snapshot model. |
| Pipeline templates and `pipeline_runs` | **Retain and augment** | One-shot numbered runs solve a real Concourse gap and preserve active run history. Add generic snapshot input bindings, workflow-version identity, durable output references, and plan provenance that survives pipeline-instance retention. |
| Workflow definition store | **Retain** | Immutable content hashes, monotonically assigned versions, source manifests, live promotion, and import idempotency align strongly with the vision. |
| Current workflow YAML grammar | **Replace** | It is a linear list of agent/checkpoint steps with reserved `repo`, `ticket`, and `skills` artifacts, workflow-wide ticket delivery, one output schema, and a mandatory `workspace`. It does not declare a workflow signature or a general Concourse DAG. |
| Workflow source format, prompts, skills, and context compilation | **Retain and adapt** | These are useful implementation assets inside an agent node. They should not define the workflow's public contract. |
| Ticket dispatcher and claim loop | **Refactor** | Preserve admission, claiming, race handling, credential attachment, and version freezing. Extract a generic run binder; make ticket dispatch one adapter that captures a ticket revision and binds it to a workflow input. |
| `agent:` ATC/plan/exec implementation | **Retain and augment** | It already mounts declared artifacts, starts sidecars, runs the agent, registers outputs, ingests telemetry, and survives important runtime failures. Add typed output ports, authoritative boundary validation, sealing, and provider-neutral invocation seams. |
| `agent-runner` | **Refactor** | Preserve health checks, prompt/context materialization, process supervision, and flight recording. Stop treating `AGENT_OUTPUT_SCHEMA` as advisory and stop conflating the Claude result envelope with semantic outputs. |
| `agent/schema` results and event stream | **Retain as invocation metadata** | The status, summary, usage, and event contracts are valuable telemetry. They are not the universal workflow-output schema. |
| Artifact repository and artifact-daemon | **Retain and augment** | They provide the physical file-tree data plane. Add a snapshot registry, content identity, seal-time validation, durable manifests/lineage, and retention-aware durable storage. Current build-scoped artifact registration alone is insufficient. |
| Task sidecars and MCP transport | **Retain** | The runtime mechanism is well aligned. Capability declarations should become general named contracts rather than a closed role taxonomy. |
| `dev-mcp` | **Retain as a standard capability pack** | Its typed build/test/lint/component contract and contract-test kit are useful. It becomes one conventional dependency that a function may request, not a mandatory workflow layer. |
| `platform-mcp` | **Split and adapt** | Snapshot-local inspection and structured submission helpers are reusable. Live ticket reads move to capture/resource adapters, and `ask_human` becomes the visible durable `await_snapshot` boundary. Remove the assumption that every workflow has a ticket/spec/plan. |
| Agent gateway plans | **Defer and redesign at the capability boundary** | Provider-independent review/agent calls remain useful, but the unimplemented gateway should target general capability interfaces and typed outputs rather than the old fixed workflow roles. |
| `harvest:` step | **Remove as a privileged terminal primitive** | It hard-codes committed workspace verification, fixed gates, judging, branch naming, pushing, and ticket transitions. Decompose reusable logic into visible validator, evaluator, repository transformation, and publisher nodes. |
| Harvest gate and judge implementations | **Salvage as functions** | Real-Git helpers, gate result structures, retry taxonomy, and rubric judging are reusable. Fixed Go command maps and implicit pre-push execution are not. |
| Ticket/spec/plan persistence | **Retain as a work-item adapter** | The state machine, revisioned specs/plans, concurrency controls, and UI are useful for a repository board. Remove their privileged position in workflow definitions and output handling. |
| Reviews and feedback | **Retain and promote into typed projections/evaluator labels** | The structured finding model, six-verdict feedback, ticket/build linkage, and Elm rendering are a strong starting point for `review/v1` validation, projection, and benchmark labels. |
| Delivered diff work | **Adapt into a repository-change projection** | Durable bounded diffs are useful UI/audit projections, but a diff is not the canonical repository-change snapshot. Persist canonical lineage separately and derive/store the bounded diff as a projection. |
| Outcomes and human-touch tracking | **Retain as evaluation data** | Merge state, human modifications, and disposition are valuable production evaluators. Decouple them from a universal ticket lifecycle and associate them with workflow outputs/runs where possible. |
| Credentials, principals, and budgets | **Retain and generalize** | The security and accounting work is valuable. Move ticket-specific keys toward run, workflow, capability, and publisher identities; preserve user attribution. |
| Agent web and `fly agent` surfaces | **Refactor around workflows and runs** | Reuse status components, review/diff views, cost presentation, run links, and ticket board. Replace the operator-console collection of features with workflow-first navigation, snapshot lineage, and experiment views. |
| Bench design | **Retain the principles; supersede implementation plans** | Captured fixtures, replay as ordinary runs, versioned evaluators, controls, repetitions, production-vs-fixture scorecards, and human promotion align extremely well. Generalize beyond hard-coded review/implement/plan kinds, ticket/build/plan join keys, and repository-only workspace pins. |
| `ci-agent` phase runner | **Retire from the primary product path** | Preserve useful prompts, schemas, and test cases. Do not maintain a second orchestration model beside first-class workflow functions unless a concrete compatibility use remains. |
| Original 14-workstream end-state roadmap | **Archive as historical design input** | It produced valuable components but encodes the ticket→spec→plan→workspace→harvest product. Continuing it as the governing roadmap would deepen the wrong abstractions. |

## Detailed migration implications

### 1. Introduce snapshots without replacing the artifact data plane

Add a semantic registry above existing artifacts rather than inventing another pod-transfer mechanism. At each declared output boundary, the engine should validate, digest, seal, and record a snapshot manifest. Workflow outputs receive durable storage. Declared intermediate outputs receive durable identity and policy-driven content retention.

The artifact-daemon can continue moving and materializing filesystem trees. Snapshot storage may reuse or extend it, but snapshot identity must not depend on an ephemeral build artifact handle or producing pod.

### 2. Add typed ports to task and agent boundaries

The current workflow grammar names artifact inputs and outputs but does not type them. `output_schema` is a single advisory field exported to the runner and explicitly not enforced.

The replacement contract needs named input and output ports with versioned types. Validation occurs after execution and before output registration/sealing. The mechanism may support structural schemas and executable validators, but the platform behavior is uniform: missing or invalid required outputs fail the invocation.

This should work for deterministic tasks as well as agents. Typed snapshots are a workflow property, not an agent-only feature.

### 3. Replace the workflow grammar while retaining its store

The current `agent/workflow.Config` should not be incrementally expanded until it becomes a second copy of Concourse configuration. A new schema version should be a thin function overlay around the ordinary Concourse plan grammar. It should declare:

- workflow input and output signatures;
- a Concourse plan/DAG implementation using the existing `atc.Step` and wrapper semantics;
- typed port mappings between nodes;
- agent implementation details such as prompts, models, skills, and capabilities;
- workflow-level output mappings.

The existing definition table, version allocator, content hashing, source manifest, live promotion, APIs, and fly import path can carry the new source format.

The source format should contain a workflow signature plus an ordinary Concourse plan, with agent implementation assets referenced by its `agent:` nodes. It may provide authoring sugar for reusable functions, but that sugar must expand into visible Concourse nodes and must not invent a parallel wrapper vocabulary.

### 4. Generalize runs and dispatch

`pipeline_runs` already gives templates numbered one-shot executions and preserves their history across template updates. Extend the run record with:

- workflow definition identity;
- bound input snapshot IDs;
- sealed output snapshot IDs;
- invocation origin such as ticket, manual, schedule, or experiment.

Dispatch becomes generic binding and admission:

```text
workflow version + named input snapshots + run policy -> pipeline run
```

Ticket dispatch captures the current ticket revision, resolves repository/resource inputs, and calls that generic path. Experiments call the same path with fixture snapshots. Manual and scheduled operations use the same binder.

### 5. Decompose implicit harvest

The current renderer requires an output named `workspace` and appends `harvest:` whenever a ticket exists. This must be removed from the general contract.

Preserve and expose its useful mechanics through ordinary visible nodes:

- repository structural validation;
- build/test/lint validator functions;
- rubric evaluator functions;
- repository change/diff projection;
- branch/PR/merge publishers;
- ticket/Jira update publishers.

An opinionated “small fix” or “version upgrade” workflow may compose all of them. A review or log-diagnosis workflow need not.

### 6. Rebase the bench on generic snapshots and signatures

The bench design should become the first consumer of durable snapshot identity, not a parallel storage model. A fixture is a pinned set of input snapshots plus expected/evaluator context. A cell invokes a function version and records output snapshots and measurements. Step-level experiments bind directly to a node's declared signature; workflow-level experiments bind to the workflow signature.

The existing bench plans should not be executed as written before this rebase. Their control, evaluator, repetition, scorecard, and production-label ideas should be carried forward.

### 7. Preserve useful ticket work without preserving ticket centrality

The existing ticket board can become a high-value repository control surface. It should:

- retain mutable work-item identity and revision history;
- capture the exact ticket revision used by each attempt;
- bind the ticket and other snapshots to a selected workflow;
- show active questions and later revisions between steps;
- project typed outputs such as reviews, repository changes, and reports;
- link every attempt to its workflow version and run.

The work-item state machine should coordinate dispatch and human attention, not define the lifecycle of every possible workflow.

## Existing audit branches

The unmerged 2026-07-21 audit hardening remains useful:

- strict workflow parsing prevents configuration typos from silently disabling behavior;
- freezing active attempt provenance aligns with immutable run inputs;
- atomic ticket/outcome transitions preserve control-plane consistency;
- capability/readiness documentation preserves the Jetbridge versus Agentic Workflows boundary;
- release and chart fixes remain necessary for the Kubernetes runtime.

These changes should be preserved or reapplied during the migration. They improve honesty and invariants but do not by themselves implement the snapshot/function model.

## What should be stopped now

Until a replacement plan is approved, avoid deepening these assumptions:

- every workflow begins with a ticket;
- every workflow produces `workspace`;
- every successful workflow pushes `agent/ticket-N`;
- every workflow follows spec→plan→implement→review phases;
- `harvest:` is the universal terminal step;
- workflow steps are only a linear agent/checkpoint list;
- `dev`, `platform`, and `gateway` are the complete capability taxonomy;
- one advisory `output_schema` describes all semantic outputs;
- experiment fixtures are inherently repository workspaces tied to tickets.

Runtime hardening, release correctness, security fixes, and preservation of existing dogfood flows can continue. New product capability should target the function/snapshot boundary.

## Recommended migration order

This is sequencing guidance, not an implementation plan.

1. **Freeze the new contracts in a shared design addendum.** Define snapshot manifests, type/version identity, port validation, lineage, and generic run bindings.
2. **Add sealable typed outputs to existing task and agent execution.** Prove one `review/v1` output and one `repository-change/v1` output without changing the workflow UI first.
3. **Persist durable workflow outputs and project them.** Reuse the existing review and diff UI as the first projections.
4. **Introduce the new workflow source version and generic run binder.** Keep the current ticket workflow renderer as a compatibility adapter during migration.
5. **Move ticket dispatch onto generic snapshot bindings.** Capture ticket revisions explicitly and stop injecting privileged ticket semantics into the workflow compiler.
6. **Replace implicit harvest with visible validator and publisher nodes.** Recreate one existing dogfood flow using only general primitives before removing compatibility behavior.
7. **Rebase the bench on snapshot fixtures and function signatures.** Use review and repository-change outputs to prove step- and workflow-level comparisons.
8. **Reframe the UI around workflows, runs, outputs, and experiments.** Retain the repository ticket board as a complementary surface.
9. **Retire obsolete schemas and runners after equivalence is proven.** Archive the old workflow plans and remove compatibility code only after their useful flows run on the new model.

## Success criteria for the product direction

The design is realized when:

- a workflow declares arbitrary typed snapshot inputs and outputs without requiring a ticket or repository;
- its implementation is a visible Concourse DAG using ordinary composition semantics;
- deterministic tasks and agent nodes consume and produce the same snapshot abstraction;
- every declared output is validated and sealed before downstream use;
- workflow outputs remain durable and retain complete lineage;
- resource versions, ticket revisions, prior outputs, fixtures, and manual inputs can all bind to runs;
- updating a workflow creates an immutable version without losing prior run history;
- two compatible workflow or step versions can run against the same fixtures and pinned evaluator;
- scorecards explain quality, variance, cost, failure, and human intervention;
- reviews, diffs, reports, and other UIs are projections of typed snapshots rather than bespoke execution paths;
- publishing and value-changing behavior remain explicit nodes;
- the existing team can operate these workflows beside ordinary Concourse pipelines.

## Final product statement

Jetbridge Agentic is a Concourse-native system for defining, running, and improving versioned stochastic functions over immutable snapshots. It preserves Concourse's visible DAG, resource, artifact, and publication model; adds typed and durable function boundaries; and uses controlled evaluation to turn agentic workflow changes into evidence-backed engineering decisions.
