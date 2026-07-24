# Prototype-Informed Sealed Record Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the pre-release ad hoc analytical snapshot documents with six
prototype-informed, subject-bound sealed record contracts using `record.json`
while preserving separate production provenance.

**Architecture:** Add one strict record envelope and common semantic
components to `agent/snapshot/contracts`. The existing batch sealer remains the
authority boundary: it checks the output declaration and validates every
record subject against its exact `ValidationContext` before hashing and
committing the candidate. Version-3 execution exposes authoritative subject
and schema values to producers, while projections, experiments, deterministic
functions, and publishers consume the new representation.

**Tech Stack:** Go 1.25, standard `encoding/json`, existing snapshot
canonicalizer/sealer, Concourse ATC task and agent execution, PostgreSQL-backed
production/lineage persistence, standard `testing`, Ginkgo/Gomega.

## Global Constraints

- Sealed value identity contains stable type/digest subject references but no
  local snapshot IDs or production occurrence fields.
- Production, workflow, model, evaluator, timestamp, and attempt provenance
  remains outside snapshot bytes.
- Record collections are strict, unique, and lexicographically sorted by ID.
- Record validators have no network or storage authority beyond exact exposed
  inputs opened through `snapshot.ValidationContext`.
- `validation-report/v1` and `gate-results/v1` are replaced by
  `validation/v1`; `selection/v1` is added.
- Legacy `ci-agent` review submission remains compatible and is not redefined
  as a snapshot record.
- Follow red-green-refactor for every production-code behavior.
- Use `GOCACHE=/tmp/codex-go-cache-prototype-record-contracts` and
  `GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts`; run Go packages
  sequentially because the host has little free disk.

---

### Task 1: Common record envelope, schema identity, and components

**Files:**
- Create: `agent/snapshot/contracts/record.go`
- Create: `agent/snapshot/contracts/record_test.go`
- Create: `agent/snapshot/contracts/record_schema.go`
- Create: `agent/snapshot/contracts/record_schema_test.go`

**Interfaces:**
- Produces: `Record[T]`, `Subject`, `StableSnapshotRef`, `Anchor`, `Score`,
  `ContentRef`, `SchemaDigestFor`, `NewRecord`, and strict envelope/component
  validation used by every later task.
- Consumes: `snapshot.TypeRef`, `snapshot.Digest`,
  `snapshot.ValidationContext`.

- [x] **Step 1: Write failing envelope authority tests**

Cover an exact valid subject and rejection of a wrong record type, wrong schema
digest, undeclared input, mismatched subject type/digest, duplicate/unsorted
subjects, and local snapshot IDs in strict JSON.

```go
record, err := contracts.NewRecord(
    snapshot.TypeRef("review/v1"),
    []contracts.Subject{contracts.SubjectFromInput(
        "primary", contracts.SubjectRolePrimary, "change", input,
    )},
    json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`),
)
if err != nil { t.Fatal(err) }
if err := record.ValidateEnvelope("review/v1", context); err != nil {
    t.Fatalf("valid envelope: %v", err)
}
```

- [x] **Step 2: Run the focused test and verify RED**

Run:

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./agent/snapshot/contracts -run 'TestRecord'
```

Expected: compile failure because the record interfaces do not exist.

- [x] **Step 3: Implement the minimal strict envelope**

Implement:

```go
type StableSnapshotRef struct {
    Type   snapshot.TypeRef `json:"type"`
    Digest snapshot.Digest  `json:"digest"`
}

type Subject struct {
    ID    string           `json:"id"`
    Role  SubjectRole      `json:"role"`
    Input string           `json:"input"`
    Ref   StableSnapshotRef `json:"-"`
}

type Record[T any] struct {
    RecordVersion string          `json:"record_version"`
    Type          snapshot.TypeRef `json:"type"`
    Schema        snapshot.Digest  `json:"schema"`
    Subjects      []Subject        `json:"subjects"`
    Body          T                `json:"body"`
}
```

Use an explicit wire form for `Subject` so JSON emits `type` and `digest`
without a nested `ref`. Validate every subject against
`ValidationContext.Input(subject.Input)`.

- [x] **Step 4: Add failing common-component tests**

Cover all anchor locator kinds, identifier rules, sorted entity IDs, score
scales/directions/bounds, finite values, and `content/` confinement.

- [x] **Step 5: Implement component validators and contract descriptors**

`SchemaDigestFor` hashes frozen canonical descriptor strings keyed by the six
record types. Unknown types return `(digest, false)`. Tests pin each digest and
prove a descriptor change changes the digest.

- [x] **Step 6: Run Task 1 tests and commit**

Run the command from Step 2 and expect PASS.

```bash
git add agent/snapshot/contracts/record.go \
  agent/snapshot/contracts/record_test.go \
  agent/snapshot/contracts/record_schema.go \
  agent/snapshot/contracts/record_schema_test.go
git commit -m "feat(snapshot): define sealed record envelope"
```

### Task 2: Review, diagnosis, and validation contracts

**Files:**
- Modify: `agent/snapshot/contracts/review.go`
- Modify: `agent/snapshot/contracts/review_test.go`
- Modify: `agent/snapshot/contracts/audit.go`
- Modify: `agent/snapshot/contracts/audit_test.go`
- Modify: `agent/snapshot/contracts/engineering.go`
- Modify: `agent/snapshot/contracts/engineering_test.go`
- Modify: `agent/snapshot/contracts/registry.go`
- Modify: `agent/snapshot/contracts/registry_test.go`

**Interfaces:**
- Produces: `ReviewBody`, `Finding`, `DiagnosisBody`, `Hypothesis`,
  `DiagnosisAction`, `ValidationBody`, `ValidationCheck`,
  `ValidationAttempt`.
- Consumes: Task 1 record envelope, anchors, scores, entity-set validation.

- [x] **Step 1: Replace review tests with failing record semantics**

Test conclusion/blocking rules, severity/blocking relationships, evidence
requirements, anchor subject resolution, empty findings, and sorted IDs.

```go
body := contracts.ReviewBody{
    Conclusion: "changes-required",
    Summary: "one blocking issue",
    Findings: []contracts.Finding{{
        ID: "F-1", Severity: "high", Blocking: true,
        Category: "correctness", Title: "race", Description: "unsafe",
        Evidence: []contracts.Anchor{fileAnchor("primary", "main.go", 12, 12)},
    }},
}
```

- [x] **Step 2: Verify review tests fail, then implement and pass**

Run `go test ... ./agent/snapshot/contracts -run TestReview`.

- [x] **Step 3: Write failing diagnosis tests**

Cover contiguous unique ranks, unit-interval confidence, conclusion
requirements, identified rank-one evidence, counterevidence, action references,
and sorted hypothesis/action sets.

- [x] **Step 4: Implement diagnosis and pass focused tests**

Move diagnosis out of the audit switch into its own strict record validator.
Keep database, deployment, and audit-findings validators unchanged.

- [x] **Step 5: Write failing validation tests**

Cover skipped/no-attempt rules, contiguous attempts, final-status consistency,
derived flaky behavior, duration parsing, and overall conclusion precedence.

```go
check := contracts.ValidationCheck{
    ID: "tests", Kind: "test", Name: "unit tests", Status: "passed",
    Attempts: []contracts.ValidationAttempt{
        {Number: 1, Status: "failed", Duration: "2s"},
        {Number: 2, Status: "passed", Duration: "1s"},
    },
}
if !check.Flaky() { t.Fatal("retry recovery must derive flaky") }
```

- [x] **Step 6: Implement validation and registry cutover**

Register `validation/v1`, remove `validation-report/v1` and `gate-results/v1`,
and add `selection/v1` as a placeholder registration supplied by Task 4 only
after its validator exists. Keep the registry compiling after each edit by
adding selection in Task 4 rather than prematurely.

- [x] **Step 7: Run contract tests and commit**

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./agent/snapshot/contracts
git add agent/snapshot/contracts
git commit -m "feat(snapshot): seal analytical record contracts"
```

### Task 3: Repository-change record and subject-derived metadata

**Files:**
- Modify: `agent/snapshot/contracts/repository.go`
- Modify: `agent/snapshot/contracts/repository_change.go`
- Modify: `agent/snapshot/contracts/repository_test.go`
- Modify: `agent/projection/repository_change.go`
- Modify: `agent/projection/repository_change_test.go`
- Modify: `agent/publisher/gateway.go`
- Modify: `agent/publisher/gateway_test.go`

**Interfaces:**
- Produces: `RepositoryChangeBody`, `RepositoryChangeRecord`,
  `ReadRepositoryChangeRecord`, derived `ChangedFiles`.
- Consumes: exact `base` subject from Task 1 and the existing controlled Git
  validators.

- [x] **Step 1: Write failing repository-change record tests**

Use `record.json` plus `content/change.patch`, require one base subject of
`repository/v1`, reject payloads outside `content/`, and preserve patch,
git-tree, and git-bundle proof semantics.

- [x] **Step 2: Verify RED**

Run:

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./agent/snapshot/contracts -run 'Repository(Change)?'
```

Expected: failures because the validator still reads `change.json`.

- [x] **Step 3: Implement body/envelope parsing**

Replace `BaseInput` with the exact `base` subject while retaining validated
repository/base object identifiers needed by publishers:

```go
type RepositoryChangeBody struct {
    RepositoryID  string     `json:"repository_id"`
    BaseSHA       string     `json:"base_sha"`
    Representation string    `json:"representation"`
    Payload       ContentRef `json:"payload"`
    ResultTree    string     `json:"result_tree"`
    ResultCommit  string     `json:"result_commit,omitempty"`
}
```

- [x] **Step 4: Derive changed files**

Return sorted canonical Git paths in `RepositoryChangeMetadata.ChangedFiles`.
For patches use the applied index; for commit-bearing forms diff base to
result. Reject unsafe or duplicate returned paths.

- [x] **Step 5: Update projection and publisher readers**

Both consumers must use the exported strict record reader, take the lineage
port from the base subject's `Input`, and continue rehashing/revalidating
content before use.

- [x] **Step 6: Run focused tests and commit**

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./agent/snapshot/contracts ./agent/projection ./agent/publisher
git add agent/snapshot/contracts agent/projection agent/publisher
git commit -m "feat(snapshot): bind repository changes to sealed subjects"
```

### Task 4: Selection and measurements contracts

**Files:**
- Modify: `agent/snapshot/contracts/measurements.go`
- Modify: `agent/snapshot/contracts/measurements_test.go`
- Create: `agent/snapshot/contracts/selection.go`
- Create: `agent/snapshot/contracts/selection_test.go`
- Modify: `agent/snapshot/contracts/registry.go`
- Modify: `agent/snapshot/contracts/registry_test.go`
- Modify: `agent/experiment/evaluator.go`
- Modify: `agent/experiment/evaluator_test.go`
- Modify: `agent/experiment/scorecard.go`
- Modify: `agent/experiment/scorecard_test.go`
- Modify: `atc/atccmd/agent_experiments.go`
- Modify: `atc/atccmd/agent_experiments_internal_test.go`

**Interfaces:**
- Produces: `SelectionBody`, `CandidateAssessment`, `NamedScore`,
  `ResolveSelection`, revised `MeasurementsBody` and `Measurement`.
- Consumes: Task 1 subjects/scores and existing experiment aggregation.

- [x] **Step 1: Write failing selection tests**

Cover exact candidate exposure, common candidate type, contiguous ranks,
selected membership, score IDs, and resolution to the existing local
`SnapshotRef`.

- [x] **Step 2: Implement and register selection**

`ResolveSelection(record, context)` returns the selected input ref from
`ValidationContext`; it does not call the sealer or create content.

- [x] **Step 3: Write failing measurements tests**

Replace `valid` with `measured|partial|not-applicable`, remove evaluator version
from the body, require finite metric definitions, and use long direction names.

- [x] **Step 4: Implement measurements and update experiment consumers**

Experiments treat only `measured` and `partial` records with metrics as valid
measurements. Aggregation compares metric IDs, units, direction, and bounds
across cells. Rename wire `name` to `id`.

- [x] **Step 5: Update canonical archive reader**

`atc/atccmd` reads `record.json`, validates the record envelope/body shape, and
returns the body to the existing experiment store.

- [x] **Step 6: Run focused tests and commit**

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./agent/snapshot/contracts ./agent/experiment ./atc/atccmd
git add agent/snapshot/contracts agent/experiment atc/atccmd
git commit -m "feat(agent): add selection and stable measurements records"
```

### Task 5: Producers, execution metadata, seeds, and review projection

**Files:**
- Modify: `agent/functions/gates/runner.go`
- Modify: `agent/functions/gates/runner_test.go`
- Modify: `agent/functions/repositoryvalidate/runner.go`
- Modify: `agent/functions/repositoryvalidate/runner_test.go`
- Modify: `agent/functions/judge/runner.go`
- Modify: `agent/functions/judge/runner_test.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `atc/exec/agent_step_test.go`
- Modify: `atc/exec/task_step.go`
- Modify: `atc/exec/task_step_test.go`
- Modify: `agent/runner/runner.go`
- Modify: `agent/runner/runner_test.go`
- Modify: `agent/projection/review.go`
- Modify: `agent/projection/review_test.go`
- Modify: `agent/workflow/seeds/*`
- Modify: `agent/workflow/seed_test.go`

**Interfaces:**
- Produces: record-aware deterministic outputs and producer-visible authority
  environment.
- Consumes: all domain records from Tasks 2–4.

- [x] **Step 1: Write failing deterministic-function tests**

Pin full retry attempt history in gates, validation record output in repository
validation, and evaluator-version-free measurement bodies in judge.

- [x] **Step 2: Implement built-in producers**

Writers emit strict `record.json`; callers supply subject bindings constructed
from their exact input refs. Legacy harvest adapters convert validation results
to their old local structures without changing legacy files.

- [x] **Step 3: Write failing execution-env tests**

Assert agent and task containers receive stable type/digest input rows and
record type/schema output rows only for record contracts.

- [x] **Step 4: Implement authority metadata injection**

Use one shared deterministic env-name helper. Reject collisions with authored
environment variables and sort emitted names.

- [x] **Step 5: Surface metadata in the agent runner prompt**

Include resolved input references and record schema variables beside output
paths so an agent can author the envelope without guessing.

- [x] **Step 6: Update seed workflows**

Change snapshot-producing prompts from `review.json`, `diagnosis.json`,
`validation-report.json`, `gate-results.json`, and `change.json` to strict
`record.json`; change typed validation ports to `validation/v1`.

- [x] **Step 7: Update review projection**

Parse and revalidate the sealed review record. Derive compatibility summary
columns from conclusion/findings and use the primary stable subject reference
instead of producer-authored repository/model/timing metadata.

- [x] **Step 8: Run focused tests and commit**

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./agent/functions/... ./agent/runner ./agent/projection ./agent/workflow ./atc/exec
git add agent/functions agent/runner agent/projection agent/workflow atc/exec
git commit -m "feat(agent): author and consume sealed records"
```

### Task 6: Cross-boundary verification and documentation alignment

**Files:**
- Modify: `docs/superpowers/specs/2026-07-21-agentic-workflows-as-functions-design.md`
- Modify: `docs/agentic/README.md`
- Modify: any focused test fixture still naming a retired snapshot contract
- Modify: `docs/superpowers/plans/2026-07-24-prototype-sealed-record-contracts.md`

**Interfaces:**
- Consumes: complete cutover.
- Produces: aligned operator/developer documentation and fresh verification
  evidence.

- [x] **Step 1: Search for stale canonical contract references**

```bash
rg -n 'validation-report/v1|gate-results/v1|review\.json|diagnosis\.json|change\.json|measurements\.json' \
  agent atc docs/agentic docs/superpowers/specs/2026-07-21-agentic-workflows-as-functions-design.md
```

Classify every remaining hit as an intentional legacy compatibility surface or
update it.

- [x] **Step 2: Run formatting**

```bash
gofmt -w agent/snapshot/contracts agent/functions agent/experiment \
  agent/projection agent/runner atc/exec atc/atccmd
```

- [x] **Step 3: Run contract and integration package sweep**

```bash
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off \
  ./agent/snapshot ./agent/snapshot/contracts ./agent/functions/... \
  ./agent/experiment ./agent/projection ./agent/publisher ./agent/runner \
  ./agent/workflow ./atc/exec ./atc/atccmd
```

Expected: all selected packages pass. If an ATC package requires unavailable
PostgreSQL or loopback access, rerun the non-database subset and record the
specific environmental failure rather than claiming it passed.

- [x] **Step 4: Run nested schema module regression**

```bash
cd agent/schema
GOCACHE=/tmp/codex-go-cache-prototype-record-contracts \
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./...
```

- [x] **Step 5: Review the diff against the design**

Verify each acceptance criterion in
`docs/superpowers/specs/2026-07-24-prototype-sealed-record-contracts-design.md`
has either a direct test or an explicitly documented compatibility exception.

- [x] **Step 6: Mark this plan complete and commit**

```bash
git add docs agent atc
git commit -m "docs(agentic): define prototype-informed sealed records"
```

Do not claim completion until `git status --short`, the fresh test outputs, and
the acceptance checklist have all been reviewed.

## Completion evidence

The cross-boundary package sweep passed:

```bash
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off \
  ./agent/snapshot ./agent/snapshot/contracts ./agent/functions/... \
  ./agent/experiment ./agent/projection ./agent/publisher ./agent/runner \
  ./agent/workflow ./atc/exec ./atc/atccmd
```

The nested schema regression also passed:

```bash
cd agent/schema
GOTMPDIR=/tmp/codex-go-build-prototype-record-contracts \
go test -p 1 -vet=off ./...
```

`pg_isready` reported no local PostgreSQL response. No selected package in
the sweep required it; repository-wide database integration suites were not
claimed.

Acceptance coverage:

- `record_test.go` covers exact type/schema/subject authority, strict JSON,
  stable cross-installation identity, subject-digest identity, anchors,
  scores, sorted entity sets, and content confinement.
- `record_schema_test.go` pins all six frozen descriptor digests literally.
- Domain contract suites seal review, diagnosis, validation,
  repository-change, selection, and measurement records through the registry.
- Existing batch-sealer tests cover value deduplication with distinct output
  occurrences; the record envelope adds no production fields to those bytes.
- Validation tests and deterministic gate tests preserve all attempts and
  derive conclusion, flakiness, and duration.
- Selection tests prove resolution returns the exact existing input
  `SnapshotRef`.
- Experiment scorecard tests reject direction and bounds drift between cells.
- Legacy `ci-agent`/harvest `review.json` references remain intentionally
  unchanged as the documented compatibility surface.
