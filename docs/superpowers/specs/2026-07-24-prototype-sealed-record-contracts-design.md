# Prototype-Informed Sealed Record Contracts

- **Date:** 2026-07-24
- **Status:** Implemented on `codex/prototype-record-contracts`
- **Target:** `codex/prototype-record-contracts`, based on the current
  `codex/agentic-functions` rework

## Purpose

Jetbridge already models agentic workflows as functions over immutable
snapshots. Concourse prototypes add a useful lower-level extension model:
versioned objects advertise typed messages, and handlers consume objects and
emit new objects. Prototype behavior does not replace Jetbridge's semantic
record layer.

The boundary is:

```text
prototype or built-in function
    receives typed inputs
    emits a candidate record
Jetbridge
    validates declared authority fields and semantic invariants
    seals the canonical bytes
sealed record
    is consumed by later prototype messages, workflow functions, projections,
    evaluators, or explicit publishers
```

Records are immutable facts. They are never prototype instances and are never
updated through prototype cloning. Prototype object cloning remains useful for
adapter configuration such as project -> ticket -> observed ticket revision.

## Value identity and production occurrence

The existing snapshot design correctly separates a deduplicated value from
the occurrences that produced it. This design preserves that split.

The sealed value contains only facts that affect semantic identity:

```yaml
record_version: 1.0.0
type: review/v1
schema: sha256:...
subjects:
  - id: primary
    role: primary
    input: change
    type: repository-change/v1
    digest: sha256:...
body:
  conclusion: accept
  summary: No blocking findings.
  findings: []
```

The production occurrence remains outside the value and records:

- workflow definition and immutable version;
- workflow run, build, plan, step, and attempt;
- producer principal;
- output port;
- full exposed input lineage;
- resolved runtime and capability identities;
- source or evaluator configuration metadata;
- creation time.

Two invocations that produce the same record bytes converge on one snapshot
while retaining two production and lineage occurrences.

## Record representation

The six semantic record contracts use one fixed representation:

```text
record.json
content/
```

`content/` is optional. Large evidence and repository-change payloads live
under it. All paths referenced by a record are canonical relative POSIX paths
below `content/`.

`record.json` is strictly decoded and contains:

```yaml
record_version: 1.0.0
type: type-ref
schema: sha256-digest
subjects: entity-set<Subject>
body: type-specific-object
```

`schema` is the digest of Jetbridge's frozen built-in contract descriptor for
the exact type. The friendly type name remains useful for dispatch, while the
digest makes accidental in-place validator drift detectable.

### Candidate authority

The current filesystem ABI lets a producer write candidate bytes directly.
Jetbridge therefore supplies the authoritative values as execution metadata:

```text
AGENT_INPUT_<NAME>_SNAPSHOT_TYPE
AGENT_INPUT_<NAME>_SNAPSHOT_DIGEST
AGENT_OUTPUT_<NAME>_RECORD_TYPE
AGENT_OUTPUT_<NAME>_RECORD_SCHEMA
```

A producer copies those values into its candidate envelope. At seal time the
server checks:

- `type` equals the declared output type;
- `schema` equals the server registry's exact contract digest;
- each subject input was actually exposed;
- each subject type and digest equals the authoritative input reference.

The producer may propose the serialized envelope but has no authority to
choose these values. A mismatch is a contract failure. Future MCP authoring
tools may construct this file for the producer without changing the sealed
format or trust boundary.

Manual upload of a subject-bearing record is rejected unless the upload API is
extended to supply an authorized subject context.

## Common components

### Subjects

```yaml
Subject:
  id: identifier
  role: primary | base | evidence | context | candidate | reference
  input: input-port-name
  type: type-ref
  digest: sha256-digest
```

Subject IDs and input names are unique within a record. Subjects are sorted by
ID in their wire representation. Type and digest are stable cross-installation
identity; local database IDs never enter record bytes.

### Entity sets

An `entity-set<T>` is encoded as an array whose members have unique `id`
fields. The array must be lexicographically sorted by ID at seal time. This
makes semantically unordered collections hash deterministically without a
hidden post-production rewrite.

### Anchors

```yaml
Anchor:
  subject: subject-id
  locator:
    kind: file-lines | log-lines | json-pointer | byte-range | opaque
    path: canonical-relative-path?
    start: integer?
    end: integer?
    pointer: json-pointer?
    value: string?
```

Every anchor resolves through a declared subject. File, log, JSON, and byte
locators have kind-specific validation. An anchor never embeds an independent
snapshot reference.

### Scores

```yaml
Score:
  value: finite-float
  scale: unit-interval | bounded | unbounded
  direction: higher-is-better | lower-is-better | target
  minimum: finite-float?
  maximum: finite-float?
  target: finite-float?
```

Unit-interval scores are within `[0,1]`. Bounded scores require finite ordered
bounds. Target-directed scores require a target.

## Domain contracts

### `review/v1`

The envelope has exactly one `primary` subject and may have `evidence` or
`context` subjects.

```yaml
body:
  conclusion: accept | changes-required | inconclusive
  summary: markdown
  findings: entity-set<Finding>
```

```yaml
Finding:
  id: identifier
  severity: observation | low | medium | high | critical
  blocking: boolean
  category: identifier
  title: string
  description: markdown
  evidence: [Anchor]
  recommendation: markdown?
```

Rules:

- `changes-required` requires a blocking finding.
- `accept` forbids blocking findings.
- observations cannot block.
- high and critical findings must block.
- non-observation findings require evidence.
- empty findings are valid.
- conclusion is judgment, not merge authorization.

### `diagnosis/v1`

The envelope has at least one subject. Primary observations normally use the
`primary` role and supporting state uses `context` or `evidence`.

```yaml
body:
  summary: markdown
  conclusion: identified | suspected | inconclusive
  hypotheses: entity-set<Hypothesis>
  actions: entity-set<Action>
```

```yaml
Hypothesis:
  id: identifier
  rank: positive-integer
  statement: markdown
  confidence: Score
  evidence: [Anchor]
  counterevidence: [Anchor]
```

```yaml
Action:
  id: identifier
  priority: immediate | next | optional
  description: markdown
  addresses: [hypothesis-id]
  rationale: markdown?
```

Ranks are unique and contiguous from one. `identified` and `suspected`
require hypotheses; only `inconclusive` may have none. An identified rank-one
hypothesis requires evidence. Confidence never mechanically determines the
conclusion.

### `validation/v1`

This replaces `validation-report/v1` and `gate-results/v1`.

```yaml
body:
  conclusion: passed | failed | error | incomplete
  summary: markdown
  checks: entity-set<Check>
```

```yaml
Check:
  id: identifier
  kind: build | test | lint | security | policy | custom
  name: string
  status: passed | failed | error | skipped
  attempts: [Attempt]
  detail: markdown?
```

```yaml
Attempt:
  number: positive-integer
  status: passed | failed | error
  duration: Go-duration-string
  evidence: [Anchor]
  detail: markdown?
```

Attempts are ordered and contiguous from one. Skipped checks have no attempts;
other checks have at least one and use the final attempt status. Flakiness and
duration are derived. Overall conclusion is derived using the precedence
`error`, `failed`, `incomplete`, `passed`; no checks or any skipped check is
incomplete.

### `repository-change/v1`

The envelope has exactly one `base` subject of type `repository/v1`.

```yaml
body:
  repository_id: sha256-digest
  base_sha: git-object-id
  representation: patch | git-bundle | git-tree
  payload:
    path: content/path
    digest: sha256-digest
    media_type: string
  result_tree: git-object-id
  result_commit: git-object-id?
```

Validation reopens and rehashes the exact base subject. Applying or importing
the payload must produce `result_tree`; commit-bearing representations must
resolve `result_commit` to that tree and descend from the base. Intrinsic
metadata records repository ID, base and result IDs, representation, and the
sorted derived changed-file set.

Repository summary and review judgment do not belong in this record.

### `selection/v1`

Every subject has role `candidate`, every exposed candidate is represented
exactly once, and all candidates have the same snapshot type.

```yaml
body:
  selected: subject-id
  candidates: entity-set<CandidateAssessment>
  rationale: markdown
```

```yaml
CandidateAssessment:
  id: subject-id
  rank: positive-integer
  summary: markdown
  scores: entity-set<NamedScore>
```

Ranks are unique and contiguous. The selected subject exists exactly once in
the assessments. Sealing creates the immutable decision record. Resolving the
selection returns the already-sealed selected input reference; it never
reseals candidate content.

### `measurements/v1`

Evaluator identity and version remain in production occurrence metadata.

```yaml
body:
  conclusion: measured | partial | not-applicable
  explanation: markdown?
  metrics: entity-set<Measurement>
```

```yaml
Measurement:
  id: identifier
  value: finite-float
  unit: identifier
  direction: higher-is-better | lower-is-better | target
  minimum: finite-float?
  maximum: finite-float?
  target: finite-float?
  evidence: [Anchor]
```

`measured` requires at least one metric. `partial` requires metrics and an
explanation. `not-applicable` requires no metrics and an explanation. A
malformed evaluator result is a contract or run error, not a valid record
whose body claims `valid: false`.

Cross-variant stability of metric IDs, units, directions, and bounds is checked
by experiment admission and aggregation in addition to per-record validation.

## Prototype messages

Prototype message discovery is frozen for the resolved prototype version. A
message declaration may describe typed record inputs and outputs:

```yaml
messages:
  review:
    inputs:
      change: {type: repository-change/v1, cardinality: one}
      validation: {type: validation/v1, cardinality: optional}
    outputs:
      review:
        type: review/v1
        subjects:
          primary: $inputs.change
          evidence: $inputs.validation
    effects: none
```

This design does not require implementing the full RFC prototype runtime in
the record-contract slice. Existing workflow signatures and typed DAG
boundaries already provide the required input/output declarations. A later
prototype runtime must dispatch over these sealed types rather than creating a
second mutable record model.

## Migration and compatibility

This branch is the pre-release contract cutover point:

- the six canonical types use `record.json`;
- `validation-report/v1` and `gate-results/v1` leave the built-in registry;
- `validation/v1` and `selection/v1` enter the registry;
- version-3 seed workflows and built-in function packages emit the new form;
- review and repository-change projections/publishers read the new form;
- legacy `ci-agent` review submission and harvest documents remain separate
  compatibility surfaces and continue using their existing files;
- persisted production provenance remains separate and unchanged.

The type names remain `/v1` because the new snapshot platform has not shipped
as a stable external contract. Once released, an incompatible body or envelope
change must create `/v2`; changing the descriptor digest beneath an existing
released type is not sufficient.

## Acceptance criteria

- Every record rejects an incorrect type, schema digest, undeclared subject,
  mismatched subject type/digest, unsorted entity set, or invalid anchor.
- Equal record bytes from separate runs deduplicate while retaining separate
  production and lineage rows.
- The same semantic body over different subject digests produces different
  snapshot bytes.
- New review, diagnosis, validation, repository-change, selection, and
  measurements records seal through the authoritative registry.
- Validation retry history survives sealing and flakiness remains derived.
- Selection resolution returns an existing input `SnapshotRef`.
- Experiments aggregate the new measurement direction vocabulary and reject
  cross-cell definition drift.
- Existing legacy review submission and non-record snapshot contracts continue
  to pass their focused suites.
