# Agentic V3-Only Workflow Cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` to implement this plan task-by-task. Tasks 1,
> 2A, 5, 3, 4, 2B, 6, and 7 are complete and independently approved. Task 8
> starts from the exact approved Task 7 head recorded below.

**Goal:** Make schema version 3 the only runtime workflow format, make tickets
a binder-only `work-item/v1` adapter, and expose durable workflow-run identity
in Fly and Elm.

**Architecture:** Migration `1773106101` owns an immutable, release-faithful
legacy decoder. The completed cutover closes schema-v3 admission, removes
legacy dispatch, makes historical rows opaque, deletes renderer/budget and
runtime-model compatibility surfaces, and moves Fly and Elm to durable
workflow-run identity. Task 8 supplies the final four-path vertical-slice
proof and active documentation.

**Tech stack:** Go, PostgreSQL migrations, Ginkgo/Gomega, Fly,
go-concourse, Elm, shell audits, and Git.

## Authority and required order

This tracked file is the standalone buildable contract. Ignored briefs,
reports, reviews, manifests, ledgers, and review packages are evidence; they
are never staged in an implementation commit.

The complete buildable order is:

```text
Task 1 -> Task 2A -> Task 5 -> Task 3 -> Task 4 -> Task 2B -> Task 6 -> Task 7 -> Task 8
```

The complete approved predecessor ledger is:

| Boundary | Approved SHA | Approval manifest | Bound report / SHA-256 | Bound PASS review / SHA-256 | Reviewer |
|---|---|---|---|---|---|
| Task 1 | `ea236ad28ee99ac49e5194c9224f437aa616c4fe` | `.superpowers/sdd/v3-cutover-task-1-approval-manifest.sh` | `v3-cutover-task-1-report.md` / `519faf8bb705e86450c70a468914278639af53dcc500b8a104a12225433827f7` | `v3-cutover-task-1-approved-review.md` / `9fc2c52b23b372483b6cfef7f4f1520586fcfe3aad2d9d8f1c922a7367010019` | Codex independent prerequisite reviewer |
| Task 2A | `6e7f68dfd3e7303946dc34776792f49c9526eb9d` | `.superpowers/sdd/v3-cutover-task-2a-approval-manifest.sh` | `v3-cutover-task-2a-report.md` / `afc1fd16ba589584673d66e4b0cc2d6aa5e47c99113e9410c19864732c86a204` | `v3-cutover-task-2a-approved-review.md` / `800869ee43c3c7c9d1f6187ee61b581e0b10bdccbdd3cb0d564ed313f9edea64` | Codex independent Task 2A approving reviewer |
| Task 5 | `da8a08b11dd27168c404e09a5577b5f03d706d2d` | `.superpowers/sdd/v3-cutover-task-5-approval-manifest.sh` | `v3-cutover-task-5-report.md` / `79239dac6f297277b011423412be7f749d8b82bf7f5775df775311ff664ad160` | `v3-cutover-task-5-approved-review.md` / `b835580d8241ead44c28cd507b9c1afb287ea94db59b4b6d8b5131489de692c7` | Codex independent Task 5 approving reviewer |
| Task 3 | `00629a0a5e0d8c5a67afe3969e3cc790a2b98253` | `.superpowers/sdd/v3-cutover-task-3-approval-manifest.sh` | `v3-cutover-task-3-report.md` / `4334c6b551813f50aaa0429005451b494c8d58bf4f51021349ff39ddcd20eff0` | `v3-cutover-task-3-approved-review.md` / `9f8a5a7f340b0fedc2c2193628badc6a7aa48e8a4a6c75a49172f595fb4ba5fd` | Codex independent Task 3 approving reviewer |
| Task 4 | `cfe95f17e5a75a2a9c71053bc6cd901003f31263` | `.superpowers/sdd/v3-cutover-task-4-approval-manifest.sh` | `v3-cutover-task-4-report.md` / `b6609bb2d10349b82aee8e644e864628656d8542d62ac17597a9d98cc5898c3e` | `v3-cutover-task-4-approved-review.md` / `4c03fb373cfffdd26dcf43b4db672644f37342f38c387436bb80ba66d9bc2dbc` | Codex independent Task 4 approving reviewer |
| Task 2B | `d4d111240f4f224a902f9c9ff674b6cbf529fac8` | `.superpowers/sdd/v3-cutover-task-2b-approval-manifest.sh` | `v3-cutover-task-2b-report.md` / `5aae81e5888f54021c4281f8f83a774aaeb887f9a7e24e53b0291fb33aff13ea` | `v3-cutover-task-2b-approved-review.md` / `21b3bbbd00b88fc33a71d9195accd52f7ee75171ed722de9b8855e06c0e9ce27` | Codex independent Task 2B approving reviewer |
| Task 6 | `2bf21dfca5f9bb75409526c17a39631ce10b0189` | `.superpowers/sdd/v3-cutover-task-6-approval-manifest.sh` | `v3-cutover-task-6-report.md` / `4250362d1c5c6951679a3a9c84764348ae066a90e175374ff20c848413ec3320` | `v3-cutover-task-6-approved-review.md` / `615ca358514e6c032fb86c7f5ac1a960dc588bab94a6858e61335ea2e6054380` | Codex independent Task 6 approving reviewer |
| Task 7 | `8161366953573b081b478c45a9d37f45506965b9` | `.superpowers/sdd/v3-cutover-task-7-approval-manifest.sh` | `v3-cutover-task-7-report.md` / `9b4025d524c490e067c48c0917be19a19e9a8bd54d72d35dfdb68afb350cd945` | `v3-cutover-task-7-approved-review.md` / `b71d91c01980b34e218d596584972f9a156ffef77c2ce246fc82f9f8318146b9` | Codex independent Task 7 approving reviewer |

The ledger is an ancestry chain from fixed cutover base
`d13849b8d10953e7d1ec76174780155cb125dc0f` through the exact Task 7 head.
Every section below retains its literal owned/staged inventory, executable
test selections, structural scans, preservation rules, and negative
boundaries.

The corrected Task 5 brief is authoritative at SHA-256
`73f335172066313e2447595c205d5073c85e375b71d54d524851f7aefde54566`;
its rereview verdict is PASS. The corrected Task 8 brief is authoritative at
SHA-256
`b212d4ffed46e60cd78d34a7ee935d12225137158f489b168cb148bcb88daf47`;
its rereview verdict is PASS. These are content hashes of ignored authority
artifacts, not implementation commit SHAs.

## Global constraints

- Schema version 3 is the sole importable, promotable, dispatchable, and
  executable workflow format. Do not add a compatibility flag, alias, or dual
  runtime path.
- Historical schema-1/schema-2 rows are inert audit metadata. No live runtime
  parser, compiler, renderer, binder, scheduler, or publisher may consume
  them.
- Migration `1773106101` and its migration-local decoder are immutable after
  Task 1. Reserve exactly migration `1773106123` for live-v3 enforcement.
- Migration `1773106123` demotes live non-v3 rows before adding the live-v3
  check. Its down migration drops only that check and never re-promotes data.
- Ticket dispatch rejects non-v3 metadata before reservation, snapshot
  capture, binding, secret/template/pipeline creation, or any mutation.
- Accepted ticket dispatch binds exactly one immutable `work-item/v1` and one
  exact `repository/v1` snapshot through the generic workflow-run binder.
- `workflow_run_id` is the durable invocation identity. `pipeline_run_id` is
  only an optional execution diagnostic. Active Fly and Elm paths never infer
  identity from an `agent-ticket-<id>` name.
- Preserve schema-v3 workflows, generic manual runs, retries, experiments,
  generic MCP/dev-MCP, snapshot APIs, `await_snapshot`, publishers, and
  ordinary Concourse pipelines.
- PostgreSQL-backed suites begin with `pg_isready`. Ginkgo commands are
  module-pinned and use `--fail-on-empty` where specified. Never use `--race`.
- RED evidence must select the exact new tests and fail for the intended
  missing behavior. GREEN evidence repeats those exact selections and the
  complete affected regressions.
- Stage literal owned paths only, compare the cached path set exactly, run
  `git diff --cached --check`, and never stage ignored `.superpowers`
  evidence.

Canonical desired-state witnesses used by the final reconciliation audit:

Admission order: Manifest.Validate -> extract `workflow.yml` -> RequireSchemaVersion3.
- Delete: `agent/workflow/validate_test.go`
- Verification-only (read-only; do not modify): `web/elm/tests/WorkflowRunDecoderTests.elm`

---

## Task 1 — COMPLETE: immutable historical decoder

**Status:** COMPLETE and independently approved. Do not rerun this task or
modify its paths in any later boundary.

**Approved implementation:** base
`d13849b8d10953e7d1ec76174780155cb125dc0f`, release authority
`fca502000f`, final implementation
`ea236ad28ee99ac49e5194c9224f437aa616c4fe`
(`fix(migration): keep harvest sidecar decoding typed-only`).

**Approval evidence:** `.superpowers/sdd/v3-cutover-task-1-report.md` and
`.superpowers/sdd/v3-cutover-task-1-approved-review.md`; final verdict PASS,
zero Critical and zero Important findings. The superseded FAIL at
`6575a0973e` is not a dependency authority.

Approval manifest:
`.superpowers/sdd/v3-cutover-task-1-approval-manifest.sh`. The cumulative
correction chain is `c8ea3fcbcada2866f0764b23960d9b6ede3466c3`,
`b27f986f8aa1adf53b8b5f4eb6e5380204b0f920`,
`4123dc86a055c6150fca580c740ea36354af8efe`,
`6575a0973ea0508c0e999ef7aa5e3886fb3dc99d`, then the approved head. The
immutable package is `review-d13849b8d1..ea236ad28e.diff`
(`d0042d9c79d0fb91fc0dcd26e68d4faca57bb8d3cb793914dfe7a013069aedcd`).

**Exact cumulative owned/staged paths:**

```text
atc/db/migration/legacyworkflow/decoder.go
atc/db/migration/legacyworkflow/decoder_test.go
atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go
atc/db/migration/add_workflow_schema_signature_test.go
```

The decoder is migration-local and imports neither live `agent/...` nor
current `atc` workflow-model/compiler packages. It freezes the released
schema-1/schema-2 ordinary wire model and released schema-3 signature grammar,
including pointer/null behavior, union arms, case-insensitive JSON field
matching, typed nested structures, validation/config-validation behavior,
asset/security limits, strict OCI digest validation, local-variable scopes,
cycle detection, and exact `harvest`/`dev_mcp` pointer semantics. Harvest
`dev_mcp` ports receive wire-type decoding only; ordinary inline sidecars
retain protocol semantic validation.

The approved evidence is:

- preserved current-versus-release parity: `19/19`;
- final-rereview current-versus-release parity: `37/37`;
- independent adjacent differential: `54/54`;
- `go test ./atc/db/migration/legacyworkflow -count=1`: PASS;
- `go test ./agent/workflow -count=1`: PASS;
- migration focus
  `workflow schema signature migration|Legacy Database Upgrade`:
  `26 passed, 0 failed, 137 skipped`;
- both forbidden-import scans, `git diff --check`, and `gofmt -l`: clean; and
- the cumulative base-to-approved range contains exactly the four paths
  above.

Every downstream dependency ledger must record the full Task 1 SHA above and
must prove these four paths are unchanged from that SHA.

---
### Schema-v3 cutover Task 2A: Close import and promotion admission

**Status:** COMPLETE and independently approved at
`6e7f68dfd3e7303946dc34776792f49c9526eb9d`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-2a-approval-manifest.sh`, binding the
implementation report and PASS review recorded in the ordered ledger. The
implementation's direct parent is the reconciled plan commit
`7f059d24a456078e5f52df7c75f63881e93a06f2`. Its immutable package is
`review-7f059d24a4..6e7f68dfd3.diff`
(`3f688230526b6b80ccccfd0a070f20fdd10116b68e1d0708fd86bde31d470405`).
The final ten-path range, exact admission order, typed 422 mapping,
no-mutation checks, named Go tests, 22/22 DB focus, structural scans, and
exact staging checks passed.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Dependency:** Start only after v3 cutover Task 1 is committed and
independently approved. Record that SHA as the implementation base.

**Source plan:** Global constraints, normative execution amendment, and Task
2A in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`.

**Goal:** Make schema 3 the only live importable/promotable workflow format
with a stable typed 422, while temporarily retaining legacy runtime
structures needed until Tasks 5, 4, and 2B.

**Owned files:**

- `agent/workflow/definition.go`
- `agent/workflow/parse.go`
- `agent/workflow/parse_v3_test.go` (direct helper coverage; observed plan
  file-list addition)
- `agent/workflow/memory_store.go`
- `agent/workflow/memory_store_test.go`
- create `agent/workflow/memory_store_admission_internal_test.go`
- `agent/api/workflows/handler.go`
- `agent/api/workflows/handler_test.go`
- `atc/db/agent_workflows_factory.go`
- `atc/db/agent_workflows_factory_test.go`

The owned and staged set is exactly these ten paths; do not stage either
parent directory. Before committing, require the cached path set to equal
this list, run `git diff --cached --check`, and commit with:

```bash
git add -- \
  agent/workflow/definition.go \
  agent/workflow/parse.go \
  agent/workflow/parse_v3_test.go \
  agent/workflow/memory_store.go \
  agent/workflow/memory_store_test.go \
  agent/workflow/memory_store_admission_internal_test.go \
  agent/api/workflows/handler.go \
  agent/api/workflows/handler_test.go \
  atc/db/agent_workflows_factory.go \
  atc/db/agent_workflows_factory_test.go
git diff --cached --check
git commit -m "feat(workflow): reject non-v3 admission"
```

**Canonical boundary:**

```go
type UnsupportedSchemaVersionError struct {
    Got int
}

func (e UnsupportedSchemaVersionError) Error() string {
    return fmt.Sprintf(
        "workflow: unsupported schema_version %d; only schema_version 3 is supported",
        e.Got,
    )
}

func RequireSchemaVersion3(source []byte) error {
    version, err := parseSchemaVersion(source)
    if err != nil {
        return err
    }
    if version != 3 {
        return UnsupportedSchemaVersionError{Got: version}
    }
    return nil
}
```

Place the helper beside the existing `parseSchemaVersion`; reuse its goccy
YAML discriminator parser and existing dependency. Return the typed error as a
value and test it with `errors.As`.

Do not add this gate to mixed `CompileDefinition` yet. Do not add a temporary
public legacy compiler.

**Required ordering:**

For both MemoryStore and PostgreSQL `ImportManifest`:

1. Call `src.Validate()` and wrap failure in `InvalidDefinitionError`; this
   preserves file-count/size, path, UTF-8, and empty-manifest security
   boundaries before parsing any file.
2. Extract `workflow.yml` and preserve the existing missing-file diagnostic.
3. Call `RequireSchemaVersion3`.
4. Wrap failure in `InvalidDefinitionError`.
5. Only then call `CompileDefinition`, validate route/name, derive metadata or
   hash, lock/begin persistence, or write a row.

This guarantees a syntactically valid v1/v2 document gets the stable typed
error before missing legacy assets, name mismatches, or legacy validation.
Malformed/missing/non-integer headers remain ordinary parse/definition errors.
A valid schema-3 header with malformed v3 content continues to the compiler
and remains HTTP 400. It is acceptable for the retained mixed compiler to
repeat manifest validation in Task 2A. Tests must pin both
`workflow: manifest has no files` and the existing missing-`workflow.yml`
diagnostic.

For both promotions, inspect persisted `SchemaVersion` immediately after
finding/locking the target. If not 3, return `InvalidPromotionError` wrapping
`UnsupportedSchemaVersionError` before source/manifest decoding, compilation,
validator lookup/call, previous-live lookup, or state mutation. A rejected
legacy target must not clear the existing live v3 row.

**HTTP mapping:**

Use a shared helper based on `errors.As`. Invoke it before generic
`InvalidDefinitionError` handling for both import forms and before generic
`InvalidPromotionError` handling for promotion. Respond 422 with the bare
stable typed message; do not expose the promotion wrapper prefix.
`http.Error` adds a newline, so compare a trimmed body in tests.

**TDD requirements:**

- Direct helper test: versions 1, 2, and 4 return the typed error with exact
  `Got`; version 3 passes; malformed/non-integer/missing headers are not
  typed. Name it
  `TestRequireSchemaVersion3RejectsUnsupportedVersions` so the focused red
  pattern selects it.
- Memory imports:
  - raw v1 with a route-name mismatch still returns unsupported;
  - manifest v2 with a missing asset still returns unsupported;
  - no version is stored;
  - malformed v3 remains ordinary invalid-definition;
  - name the new store/HTTP cases with `NonV3`, `Unsupported`, `Import`, or
    `Promote` so the red command is non-vacuous for every new boundary.
- Package-internal MemoryStore promotion fixture:
  - import/promote a minimal valid v3 with a counting validator;
  - capture the validator count after successful v3 promotion and prove it is
    positive;
  - under `store.mu`, append a same-name version-2 `Definition` with invalid
    source/compiled fields;
  - release the lock, promote legacy, and prove nested error types, exact text,
    validator count unchanged from the positive baseline, v3 still live, and
    legacy still non-live by inspecting private rows under `store.mu` rather
    than calling `Get` (which recompiles in Task 2A);
  - do not expose a production seeding hook.
- HTTP raw import, manifest import, and wrapped promotion return exact 422;
  malformed v3 remains 400.
- PostgreSQL:
  - raw v1 and asset-invalid manifest v2 create no row;
  - malformed v3 stays ordinary invalid-definition;
  - import/promote valid v3, then directly insert a version-2 historical row
    with malformed YAML and `'[]'::jsonb`;
  - promotion returns both wrapper types before decoding, validator count
    remains unchanged from the positive post-v3-promotion baseline, v3
    remains live, and legacy remains non-live by direct metadata SQL rather
    than `Get` (which still decodes history until Task 3).

Convert accepted MemoryStore/handler/DB fixtures from v1/v2 to minimal
digest-pinned schema 3. Replace `Config`/`Legacy` success assertions with
`Compiled.Function`. Do not use runtime import to create historical rows.
Do not convert historical migration fixtures. Also retain the existing
directly inserted legacy historical-read DB fixture: it intentionally proves
the Task-2A intermediate behavior until Task 3 makes reads opaque. Convert
the separate runtime-import legacy success fixture to v3.

**Scope boundaries:**

- Retain `Config`, `Parse`, `Compile`, `compileLegacy`, the compiled `Legacy`
  arm, `Definition.Config`, legacy seeds, and mixed compiler behavior.
- Do not modify Fly, seeds, migration files, or database head.
- Do not add a feature flag, fallback, seeding API, or dual live admission
  path.
- Preserve the authoritative digest-pinned promotion validator.
- Use `apply_patch`; shared worktree, do not revert others.

**Red/green verification:**

```bash
go test ./agent/workflow ./agent/api/workflows \
  -run 'Test.*(NonV3|Unsupported|Import|Promote)' -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentWorkflowsFactory' ./atc/db

go test ./agent/workflow ./agent/api/workflows -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentWorkflowsFactory' ./atc/db

git diff --check
```

**Commit/report:**

- Commit as `feat(workflow): reject non-v3 admission`.
- Write `.superpowers/sdd/v3-cutover-task-2a-report.md` with red/green
  evidence, exact ordering assertions, changed fixtures, SHA, and concerns.

---

### Schema-v3 cutover Task 5: Make ticket dispatch binder-only

**Status:** COMPLETE and independently approved at
`da8a08b11dd27168c404e09a5577b5f03d706d2d`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-5-approval-manifest.sh`, binding the report
and PASS review in the ledger. Initial implementation
`9bb76056def8a20bcb9d16d5a62de5e0434708ec` was followed by the
documentation-only correction at the approved head. The immutable cumulative
package is `review-6e7f68dfd3..da8a08b11d.diff`
(`4a56ad3419338b4bd9f1ef627d1673bb4c717bf50369a0c659938529144702de`).
The final 19-path range, exact binder order, four invocation adapters,
pre-side-effect rejection, deletion/preservation scans, six named tests,
four-package regression, 4/4 DB focus, and exact staging checks passed.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Dependency:** Start only after Task 2A is committed and independently
approved. Record that SHA as the implementation base.

**Execution order:** This remains Task 5 after Task 2A and before Tasks 3, 4,
and 2B. Do not pull Task 4 renderer/budget cleanup or Task 2B legacy model
cleanup forward.

**Source plan:** Global constraints, normative amendment, and Task 5 in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`.

**Goal:** Delete the active legacy ticket-dispatch path. Ticket dispatch must
reject non-v3 metadata before reservation or any side effect and bind valid
tickets only through the durable workflow-run binder.

**Exact known owned/staged surface (19 paths):**

```text
agent/dispatch/dispatch.go
agent/dispatch/dispatch_test.go
agent/dispatch/handler.go
agent/dispatch/handler_test.go
agent/dispatch/dispatcher.go
agent/dispatch/dispatcher_test.go
agent/dispatch/mode_test.go
agent/dispatch/reconcile_test.go
agent/dispatch/labels.go
agent/dispatch/labels_test.go
agent/workflowrun/types.go
agent/workflowrun/binder.go
agent/workflowrun/binder_test.go
agent/workflowrun/experiment_binder.go
agent/workflowrun/experiment_binder_test.go
agent/api/workflowruns/handler.go
agent/api/workflowruns/handler_test.go
atc/db/agent_dispatch_test.go
atc/atccmd/command.go
```

`labels.go` and `labels_test.go` are deletions; all other known paths are
modifications. Add any further direct compile-time caller returned by the
required final symbol inventory as a literal owned and staged path, and
record that expansion in the report. Do not stage a directory.

Do not touch Task 1 migration work or Task 4 renderer/budget files except that
`render.go` becomes deliberately orphaned until Task 4.

**Required v3 dispatch behavior:**

- Resolve the selected definition, then immediately return
  `ErrWorkflowNotV3` for `SchemaVersion != 3`.
- Rejection precedes `ReserveDispatch`, revision capture, snapshot lookup,
  template/secret/pipeline-run creation, or binder call.
- The only accepted path is `dispatchV3`, retaining:
  reservation → immutable `CaptureRevision` → exact `work-item/v1` and
  `repository/v1` bindings → `BindAndCreate` → `RecordDispatchRun`;
  idempotency, transition retry, and orphan cancellation remain.
- Result identity comes from binder:
  `RunID` is pipeline execution reference,
  `WorkflowRunID` is durable invocation identity, and `PipelineName` is the
  binder-owned immutable template.

**Delete from active dispatch:**

- legacy ticket version-freeze update;
- user lookup;
- latest spec/active plan/lint aggregation;
- `RenderLegacyTicket`;
- ticket template saving and direct `CreateRun`;
- direct credential resolution/run-secret attachment;
- `TemplateSaver`, `RunCreator`, `UserLookup`;
- `Deps.Templates`, `Runs`, `Credentials`, `Users`, `Secrets`,
  `SecretLabels`, `ATCExternalURL`, and `RepoBaseURL`;
- `NewTeamTemplateSaver`;
- dead result-warning plumbing (wire `DispatchResponse.Warnings` may remain
  present and empty);
- `RunSecretLabeler`, `NewK8sRunSecretLabeler`, `labels.go`, and
  `labels_test.go`;
- `AgentRepoBaseURL` / `--agent-repo-base-url` and both reads.

Retain `SpecLint` because Fly queueing still calls it. Retain the outer
`TicketBudgets` API and workflow fallback until Task 4. Retain generic
workflow-run secret preparation/reaping.

**Error cleanup:**

- Add
  `ErrWorkflowNotV3 = errors.New("workflow definition is not schema v3")`;
  handler maps it to 422 and dispatcher loop treats it as
  permanent/refused. For the named `smoke` v7/schema-2 fixture, the exact HTTP
  body is
  `workflow definition is not schema v3: workflow smoke v7 uses schema_version 2\n`.
- Reword retained `ErrRenderRefused` away from v0 legacy terminology.
- Delete `workflowrun.ErrLegacyDefinition` only after updating:
  - binder schema mismatch → wrapped `ErrPlatformFailure`;
  - binder durable category list;
  - experiment adapter (must not turn platform failure into invalid request);
  - workflow-run HTTP handler/test (platform failure stays redacted 500);
  - dispatch binder error switch.
- Enforce symbol deletion with scans rather than a vacuous unit assertion.

**Unit fixture/TDD requirements:**

- Build `v3DispatchDeps` independently; do not inherit legacy savers, run
  factories, credentials, or secrets.
- Work-item fake must return the requested ticket ID dynamically so
  dispatcher/mode tests work with multiple tickets.
- Convert handler, dispatcher, mode, pinned-version, refusal, nil-budget, and
  budget tests to v3; preserve their behavioral coverage.
- `reconcile_test` uses minimal `Deps{Tickets: store}`.
- Non-v3 test uses a valid v3 compiled definition with persisted
  `SchemaVersion` changed to 2; assert `errors.Is(ErrWorkflowNotV3)`, zero
  reservation/capture/binder calls, and no ticket linkage.
- Budget reads may occur before definition resolution, so use nil/permissive
  budget and measure only prohibited side effects.
- Add binder and experiment-adapter classification tests as described above.
- Add `TestDispatcherNonV3LogsDispatchRefused` with a lager test sink. It
  must observe `dispatch-refused` with an error wrapping
  `ErrWorkflowNotV3`, observe no `failed-to-dispatch`, leave the ticket
  queued, and prove the next queued ticket is still attempted. State alone
  is not sufficient because the default platform-fault branch has the same
  ticket state/loop-continuation behavior.

## Non-vacuous RED/GREEN contract

Before changing a production branch, add the fixtures and these exact stable
test names:

- `agent/dispatch/dispatch_test.go`:
  `TestDispatchOneRejectsNonV3BeforeSideEffects`
- `agent/dispatch/handler_test.go`:
  `TestDispatchHandlerNonV3MapsToUnprocessableEntity`
- `agent/dispatch/dispatcher_test.go`:
  `TestDispatcherNonV3LogsDispatchRefused`
- `agent/workflowrun/binder_test.go`:
  `TestBindAndCreateTreatsNonV3DefinitionAsPlatformFailure`
- `agent/workflowrun/experiment_binder_test.go`:
  `TestExperimentBinderPreservesNonV3PlatformFailureClassification`
- `agent/api/workflowruns/handler_test.go`:
  `TestCreateMapsNonV3DefinitionPlatformFailureToRedactedInternalError`

The direct dispatch and ticket HTTP tests must use a valid compiled v3
definition whose persisted `SchemaVersion` alone is changed to 2. The direct
test asserts `errors.Is(err, ErrWorkflowNotV3)`, zero
`ReserveDispatch`/`CaptureRevision`/binder calls, and no workflow version,
definition, snapshot, workflow-run, or pipeline-run linkage. The HTTP test
asserts exact 422 classification/body and the same prohibited-side-effect
counters. A budget read is permitted before definition resolution.

The binder test must drive the real definition-validation path. The
experiment test must drive that same validation through
`ExperimentBinderAdapter`, not return a hand-authored platform error from its
stub. The workflow-run HTTP test must put the real binder (with minimal
fakes) behind the handler and submit the non-v3 definition; a fake that
directly returns `ErrPlatformFailure` is already green at the implementation
base and is forbidden. It asserts status 500, code `internal_error`, the
stable public message, and absence of schema/database/private detail.

It is permissible to add only the `ErrWorkflowNotV3` declaration and minimal
test fixtures first so all six tests compile. Do not add its dispatch branch,
handler mapping, dispatcher switch case, or any binder/adapter/API
classification change before recording RED.

Prove selection before RED. The count and exact-name checks prevent Go's
successful empty `-run` behavior from becoming evidence:

```bash
set -euo pipefail

dispatch_focus='^(TestDispatchOneRejectsNonV3BeforeSideEffects|TestDispatchHandlerNonV3MapsToUnprocessableEntity|TestDispatcherNonV3LogsDispatchRefused)$'
workflow_focus='^(TestBindAndCreateTreatsNonV3DefinitionAsPlatformFailure|TestExperimentBinderPreservesNonV3PlatformFailureClassification)$'
workflow_http_focus='^TestCreateMapsNonV3DefinitionPlatformFailureToRedactedInternalError$'

dispatch_selected="$(go test ./agent/dispatch -list "$dispatch_focus" | awk '/^Test/{print}')"
workflow_selected="$(go test ./agent/workflowrun -list "$workflow_focus" | awk '/^Test/{print}')"
workflow_http_selected="$(go test ./agent/api/workflowruns -list "$workflow_http_focus" | awk '/^Test/{print}')"

test "$(printf '%s\n' "$dispatch_selected" | grep -c '^Test')" -eq 3
test "$(printf '%s\n' "$workflow_selected" | grep -c '^Test')" -eq 2
test "$(printf '%s\n' "$workflow_http_selected" | grep -c '^Test')" -eq 1

for name in \
  TestDispatchOneRejectsNonV3BeforeSideEffects \
  TestDispatchHandlerNonV3MapsToUnprocessableEntity \
  TestDispatcherNonV3LogsDispatchRefused; do
  printf '%s\n' "$dispatch_selected" | grep -qx "$name"
done
for name in \
  TestBindAndCreateTreatsNonV3DefinitionAsPlatformFailure \
  TestExperimentBinderPreservesNonV3PlatformFailureClassification; do
  printf '%s\n' "$workflow_selected" | grep -qx "$name"
done
printf '%s\n' "$workflow_http_selected" |
  grep -qx TestCreateMapsNonV3DefinitionPlatformFailureToRedactedInternalError
```

Run every RED witness independently and record exit status plus the failing
assertion. Each must exit non-zero for its stated pre-change reason:

```bash
go test ./agent/dispatch \
  -run '^TestDispatchOneRejectsNonV3BeforeSideEffects$' -count=1
go test ./agent/dispatch \
  -run '^TestDispatchHandlerNonV3MapsToUnprocessableEntity$' -count=1
go test ./agent/dispatch \
  -run '^TestDispatcherNonV3LogsDispatchRefused$' -count=1
go test ./agent/workflowrun \
  -run '^TestBindAndCreateTreatsNonV3DefinitionAsPlatformFailure$' -count=1
go test ./agent/workflowrun \
  -run '^TestExperimentBinderPreservesNonV3PlatformFailureClassification$' -count=1
go test ./agent/api/workflowruns \
  -run '^TestCreateMapsNonV3DefinitionPlatformFailureToRedactedInternalError$' -count=1
```

Expected RED is, respectively: legacy dispatch is entered and/or a prohibited
side effect/linkage occurs instead of `ErrWorkflowNotV3`; ticket HTTP has the
legacy refusal body/side effects rather than the new sentinel boundary; the
dispatcher log does not carry the new sentinel classification; binder
validation returns non-platform legacy classification; the experiment
adapter converts it to `experiment.ErrBindInvalidRequest`; and workflow-run
HTTP returns legacy 422 instead of redacted 500. A compile error, empty
selection, failure in a different test, or one passing command is not valid
RED evidence.

Before production deletion, run and record these positive inventories as
expected-red. Every command must print at least one owned stale symbol; save
the matching lines in the report:

```bash
set -euo pipefail

rg -n 'ErrLegacyDefinition' agent atc fly go-concourse --glob '*.go'
rg -n 'RunSecretLabeler|SecretLabels|NewK8sRunSecretLabeler' agent atc --glob '*.go'
rg -n 'AgentRepoBaseURL|agent-repo-base-url' agent atc fly go-concourse --glob '*.go'
rg -n \
  'type (TemplateSaver|RunCreator|UserLookup) interface|^[[:space:]]*(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL)[[:space:]]+|deps\.(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL)' \
  agent/dispatch/dispatch.go agent/dispatch/dispatcher.go
rg -n \
  '^[[:space:]]*(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL):' \
  atc/db/agent_dispatch_test.go
awk '
  /dispatcherDeps := dispatch\.Deps\{/ { in_dispatch_deps = 1 }
  /dispatch\.NewHTTPHandler\(dispatch\.Deps\{/ { in_dispatch_deps = 2 }
  in_dispatch_deps &&
    /^[[:space:]]*(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL):/ {
      print FNR ":" $0
    }
  in_dispatch_deps == 1 && /^[[:space:]]*}$/ { in_dispatch_deps = 0 }
  in_dispatch_deps == 2 && /^[[:space:]]*}, func/ { in_dispatch_deps = 0 }
' atc/atccmd/command.go
rg -n '\bWarnings\b|\.Warnings\b' \
  agent/dispatch/dispatch.go agent/dispatch/dispatcher.go \
  agent/dispatch/handler.go
```

After the implementation, rerun the exact-name selection witness unchanged,
then rerun all six commands unchanged and require each to pass. Record the
six selected counts and six GREEN statuses separately.

**DB fixture replacement:**

Replace `atc/db/agent_dispatch_test.go` atomically with a v3-backed
end-to-end fixture:

- digest-pinned `workflowrun.WorkflowTargetRenderer`;
- `db.NewAgentWorkflowsFactory(dbConn, renderer)`;
- v3 manifest with exactly `work-item/v1` and `repository/v1` inputs;
- import and promote;
- available lowercase-SHA256 snapshot rows and team grants;
- capturer returning the authorized work-item snapshot and requested ticket;
- real workflow-run store, template saver, binder, pipeline-run/template
  factories, allow-all budget, existing no-op secret preparer, and canceler;
- keep `dispatch.NewTicketBudgets(..., workflows)` until Task 4.

Persist and select the exact authorized `repository/v1` snapshot ID on each
ticket before queueing or dispatch. Use the supported ticket create/update
boundary (for example,
`ticketsFactory.Update(ticketID, tickets.Update{RepositorySnapshotID:
&repositorySnapshotID})`), re-read the row, and assert the persisted value is
the exact granted repository snapshot before the draft-to-queued transition.
The mere existence/grant of a repository snapshot is not a selection.

After dispatch, inspect the real workflow-run store's durable snapshot
bindings and assert the repository port binds that exact snapshot ID and the
work-item port binds the capturer's exact authorized snapshot ID. Then apply
a permitted mutable ticket edit and attempt to replace the selected
repository snapshot with a second valid/granted repository ID. Assert the
ordinary edit cannot alter the durable binding, the replacement returns
`tickets.ErrDispatchConflict`, and a fresh read of both the ticket and
workflow-run bindings still names the original repository ID. Requeue must
create a new durable attempt whose repository binding is still the original
selected ID.

Assert durable `WorkflowRunID`, matching pipeline run reference, running
ticket with both links, binder template name, no
`agent-ticket-<id>` template, requeue creates a new durable attempt, and
dispatcher/reconciliation cases remain green.

**ATC wiring boundary:**

Update both dispatch constructors in `atc/atccmd/command.go`. Preserve both
`NewVaultedRunSecretPreparer` instances, generic binder, experiment adapter,
`cmd.agentRunSecrets()`, lazy/K8s model-token attacher, secret reaper, retry,
and experiment wiring.

Preserve binder ordering exactly: reservation, immutable
`CaptureRevision`, exact `work-item/v1` and `repository/v1` selection,
`BindAndCreate`, then `RecordDispatchRun`. Preserve idempotency, transition
retry, orphan cancellation, experiment admission/retry fields, both generic
secret preparers, and generic secret reaping. `SpecLint` remains for Fly
queueing even though dispatch result-warning plumbing is deleted.

**Verification:**

```bash
set -euo pipefail

go test \
  ./agent/dispatch \
  ./agent/workflowrun \
  ./agent/api/workflowruns \
  ./atc/atccmd \
  -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='dispatching a ticket end-to-end|the dispatcher loop over real stores' \
  ./atc/db

! rg -n 'ErrLegacyDefinition' agent atc fly go-concourse --glob '*.go'
! rg -n 'RunSecretLabeler|SecretLabels|NewK8sRunSecretLabeler' agent atc --glob '*.go'
! rg -n 'AgentRepoBaseURL|agent-repo-base-url' agent atc fly go-concourse --glob '*.go'
! rg -n \
  'dispatch\.NewTeamTemplateSaver|attachRunSecret|resolveRunCredential' \
  agent/dispatch atc/atccmd/command.go atc/db/agent_dispatch_test.go \
  --glob '*.go'
! rg -n \
  'type (TemplateSaver|RunCreator|UserLookup) interface|^[[:space:]]*(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL)[[:space:]]+|deps\.(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL)' \
  agent/dispatch/dispatch.go agent/dispatch/dispatcher.go
! rg -n \
  '^[[:space:]]*(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL):' \
  atc/db/agent_dispatch_test.go
test -z "$(awk '
  /dispatcherDeps := dispatch\.Deps\{/ { in_dispatch_deps = 1 }
  /dispatch\.NewHTTPHandler\(dispatch\.Deps\{/ { in_dispatch_deps = 2 }
  in_dispatch_deps &&
    /^[[:space:]]*(Templates|Runs|Credentials|Users|Secrets|SecretLabels|ATCExternalURL|RepoBaseURL):/ {
      print FNR ":" $0
    }
  in_dispatch_deps == 1 && /^[[:space:]]*}$/ { in_dispatch_deps = 0 }
  in_dispatch_deps == 2 && /^[[:space:]]*}, func/ { in_dispatch_deps = 0 }
' atc/atccmd/command.go)"
! rg -n '\bWarnings\b|\.Warnings\b' \
  agent/dispatch/dispatch.go agent/dispatch/dispatcher.go \
  agent/dispatch/handler.go
! rg -n 'agent-ticket-' \
  agent/dispatch/dispatch.go agent/dispatch/handler.go \
  agent/dispatch/dispatcher.go atc/atccmd/command.go

rg -n \
  'NewVaultedRunSecretPreparer|agentRunSecrets\(\)|NewExperimentBinderAdapter|RetryOf|ExperimentAdmission' \
  agent/workflowrun atc/atccmd/command.go atc/atccmd/agent_experiments.go

git diff --check
```

Focused DB suites use disposable PostgreSQL; require local binaries and use
the module-pinned Ginkgo.

The `RepoBaseURL` structural scan deliberately names only `dispatch.go`,
`dispatcher.go`, the two `dispatch.Deps` constructor sites, and the DB
fixture. Do not broaden it to `render.go`: Task 4 owns and temporarily retains
`RenderInput.RepoBaseURL`.

**Commit/report:**

- Stage the 19 known paths exactly (including deletions), plus only a
  documented direct caller discovered by the required inventory:

  ```bash
  git add -- \
    agent/dispatch/dispatch.go \
    agent/dispatch/dispatch_test.go \
    agent/dispatch/handler.go \
    agent/dispatch/handler_test.go \
    agent/dispatch/dispatcher.go \
    agent/dispatch/dispatcher_test.go \
    agent/dispatch/mode_test.go \
    agent/dispatch/reconcile_test.go \
    agent/dispatch/labels.go \
    agent/dispatch/labels_test.go \
    agent/workflowrun/types.go \
    agent/workflowrun/binder.go \
    agent/workflowrun/binder_test.go \
    agent/workflowrun/experiment_binder.go \
    agent/workflowrun/experiment_binder_test.go \
    agent/api/workflowruns/handler.go \
    agent/api/workflowruns/handler_test.go \
    atc/db/agent_dispatch_test.go \
    atc/atccmd/command.go
  git diff --cached --name-only
  git diff --cached --check
  git commit -m "feat(dispatch): bind tickets only through workflow runs"
  ```
- Write `.superpowers/sdd/v3-cutover-task-5-report.md` with red/green
  evidence, DB identity proof, exact deletion/preservation audits, SHA, and
  concerns.
- Stage only tracked Task 5 owned paths and exact compile-time callers found
  by the required scans. Never stage `.superpowers`; both this brief and its
  reports remain ignored coordination artifacts.

Use `apply_patch`; shared worktree, never revert other agents’ changes.

---

### Schema-v3 cutover Task 3: Opaque history and v3 live-row enforcement

**Status:** COMPLETE and independently approved at
`00629a0a5e0d8c5a67afe3969e3cc790a2b98253`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-3-approval-manifest.sh`, binding the report
and PASS review in the ledger. Its immutable package is
`review-da8a08b11d..00629a0a5e.diff`
(`fbe11f91f609c4c499211a07acf759160feefe9956fe5f0ccb28c2369a970f87`).
The final eight-path range, opaque `Get`/`Latest`, corrupt-v3 failure,
sole `1773106123` pair, same-row down/reactivate/re-up lifecycle, preflight
heads, 2/2 migration, 17/17 upgrade, 22/22 workflow, 30/30 experiment, and
29/29 combined focus results passed with exact staging.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Dependency:** Start only after corrected Task 2A and Task 5 are committed
and independently approved. Record both SHAs, verify that the Task 2A SHA is
an ancestor of the Task 5 SHA, and use the Task 5 SHA as the implementation
base.

**Source plan:** Global constraints, normative amendment, and Task 3 in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`.

**Goal:** Demote every live non-v3 definition, constrain future live rows to
schema 3, and make runtime reads of historical v1/v2 rows opaque—never parsed
or compiled.

**Migration allocation:** Exactly `1773106123`; no other migration number.

**Owned files:**

- create
  `atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.up.sql`
- create
  `atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql`
- create `atc/db/migration/v3_only_workflows_test.go`
- `atc/db/agent_workflows_factory.go`
- `atc/db/agent_workflows_factory_test.go`
- `atc/db/migration/legacy_upgrade_test.go`
- `docs/migration/migrate-preflight.sh`
- `docs/migration/migrate-preflight_test.sh`

**Exact SQL:**

```sql
UPDATE agent_workflow_definitions
SET live = false
WHERE live AND schema_version <> 3;

ALTER TABLE agent_workflow_definitions
    ADD CONSTRAINT agent_workflow_definitions_live_schema_v3_check
    CHECK (NOT live OR schema_version = 3);
```

Down:

```sql
ALTER TABLE agent_workflow_definitions
    DROP CONSTRAINT agent_workflow_definitions_live_schema_v3_check;
```

The down migration must not re-promote any row. Demotion and constraint
installation are one migration transaction.

**Migration test requirements:**

- Name the new container exactly
  `Describe("v3-only workflow liveness migration", ...)` so its focused RED
  and GREEN runs cannot be satisfied by an unrelated migration spec.
- Start at `1773106122`.
- Seed distinct live schema-1, schema-2, and schema-3 rows.
- Upgrade to `1773106123`; prove both legacy rows demoted, v3 stays live,
  exact named constraint exists, non-live legacy history remains insertable,
  and any insert/update with non-v3 `live=true` fails.
- Downgrade to `1773106122`; prove constraint gone, rows stay demoted, and an
  explicit subsequent legacy live update is possible.
- Add the exact dedicated spec
  `It("demotes a reactivated legacy row again on same-database re-upgrade", ...)`.
  It must use one database lifecycle and the same persisted legacy and v3
  rows throughout:
  1. apply migration `1773106123`;
  2. verify the legacy rows are demoted, the schema-v3 row remains live, and
     the installed constraint rejects a later legacy-live update;
  3. run the `1773106123` down step;
  4. explicitly reactivate the same persisted legacy row while keeping the
     same schema-v3 row live;
  5. apply migration `1773106123` again to that same database;
  6. verify renewed demotion of that same legacy row, retained v3 liveness,
     reinstallation of the exact named constraint, and a second rejected
     legacy-live update.
  A fresh-database second upgrade, a different legacy row, or SQL-text-only
  inspection does not satisfy this behavior.
- Advance `jetbridgeHeadMigration` and `JETBRIDGE_VERSION` to `1773106123`.
- Legacy-to-head assertions prove demotion and the constraint.
- Preflight direction fixtures use `1773106123 down / 1773106122` for rolled
  back HEAD and `1773106124` for the simulated newer migration.

**Opaque runtime contract:**

- Branch on persisted `SchemaVersion` before
  `compileStoredWorkflowSource`.
- Schema 3 retains current source/manifest compilation plus the existing
  stored name, schema, and signature consistency checks.
- Schema 1/2:
  - `Get`/`Latest` return persisted metadata and exact `RawYAML`;
  - `SourceManifest` is nil;
  - `CompiledDefinition` and `Config` are zero;
  - no YAML parsing, JSON manifest decoding, validation, or compilation.
- `List`/`Versions` remain metadata-only.
- `Live` finds no demoted legacy row after migration.
- `Promote` rejects non-v3 immediately after target metadata scan/lock with
  `InvalidPromotionError` wrapping `UnsupportedSchemaVersionError`, before
  source/manifest decode, validator call, current-live lookup, or mutation.

Use a highest-version historical fixture with syntactically malformed YAML
and valid wrong-shape JSONB (`[]::jsonb`). For both `Get(name, version)` and
`Latest(name)`, prove `found=true` and exact persisted ID, name, version,
content hash, description, creator, timestamps, schema/signature metadata,
live marker, and raw YAML. Assert `SourceManifest == nil`,
`Compiled == workflow.CompiledDefinition{}`, and
`Config == workflow.Config{}`. Neither read may report a YAML, JSON,
validation, or compile error. Keep a separate corrupt-v3 fixture proving both
`Get` and, when it is latest, `Latest` still fail closed. Preserve the
metadata-only `List`/`Versions` corrupt-source test. Binder, dispatch, and
experiment consumers already check schema before compiled content and must
remain green.

**Scope boundaries:**

- Migration `1773106101` and its decoder are immutable after Task 1.
- Import admission is already v3-only from Task 2A; do not add a fallback.
- Ticket dispatch is already binder-only from Task 5; do not restore a legacy
  consumer.
- Do not decode source manifests merely to expose historical content.
- Generated interfaces do not change.
- Use `apply_patch`; do not revert shared worktree changes.

**TDD/verification:**

```bash
pg_isready

rg -n -F \
  'It("demotes a reactivated legacy row again on same-database re-upgrade"' \
  atc/db/migration/v3_only_workflows_test.go

go run github.com/onsi/ginkgo/v2/ginkgo \
  --dry-run \
  --fail-on-empty \
  --focus='v3-only workflow liveness migration' \
  ./atc/db/migration

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='v3-only workflow liveness migration' \
  ./atc/db/migration

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='Legacy Database Upgrade' \
  ./atc/db/migration

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentWorkflowsFactory' ./atc/db

go test ./agent/dispatch ./agent/workflowrun -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentExperimentsFactory' ./atc/db

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='workflow schema signature migration|v3-only workflow liveness migration|Legacy Database Upgrade' \
  ./atc/db/migration

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentWorkflowsFactory' ./atc/db

bash docs/migration/migrate-preflight_test.sh
git diff --check
```

Report every selected-spec count separately so focuses are demonstrably
non-vacuous. The exact focus's dry run must select the named
same-database/same-row spec above; its unchanged real run is the dedicated
RED/GREEN proof and must record selected, passed, failed, and skipped counts.
The separate legacy-upgrade command proves the head transition.

**Immutable-history, allocation, and scope audit:**

With `TASK5_BASE_SHA` set to the exact approved Task 5 SHA, run:

```bash
git diff --exit-code "$TASK5_BASE_SHA" -- \
  atc/db/migration/legacyworkflow \
  atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go \
  atc/db/migration/migrations/1773106101_add_workflow_schema_signature.down.sql \
  atc/db/migration/add_workflow_schema_signature_test.go

test -f atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.up.sql
test -f atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql

rg -n \
  'agent_workflow_definitions_live_schema_v3_check|schema_version = 3' \
  atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.*

rg -n \
  'jetbridgeHeadMigration = 1773106123|JETBRIDGE_VERSION=1773106123|1773106124' \
  atc/db/migration/legacy_upgrade_test.go \
  docs/migration/migrate-preflight.sh \
  docs/migration/migrate-preflight_test.sh

! rg -n \
  'jetbridgeHeadMigration = 1773106122|JETBRIDGE_VERSION=1773106122' \
  atc/db/migration/legacy_upgrade_test.go \
  docs/migration/migrate-preflight.sh

git status --short
git diff --name-only
git diff --check
```

Exactly the two `1773106123` assets may be allocated; no other migration
number may be introduced by this task. The final unstaged diff must contain
only the eight owned paths.

**Commit/report:**

- Stage the eight owned paths exactly—never stage the whole migration
  directory:

  ```bash
  git add \
    atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.up.sql \
    atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql \
    atc/db/migration/v3_only_workflows_test.go \
    atc/db/agent_workflows_factory.go \
    atc/db/agent_workflows_factory_test.go \
    atc/db/migration/legacy_upgrade_test.go \
    docs/migration/migrate-preflight.sh \
    docs/migration/migrate-preflight_test.sh

  git diff --cached --name-only
  git diff --cached --check
  ```

  Require the cached path list to equal those eight paths before committing.
- Commit as `feat(db): enforce schema v3 workflow liveness`.
- Write `.superpowers/sdd/v3-cutover-task-3-report.md` with red/green
  evidence, separate selected-spec counts, SQL/down semantics, exact
  `Get`/`Latest` opaque-read proof, binder/dispatch/experiment preservation,
  immutable-history and exact-staging audits, preflight/head updates, and the
  exact same-database/same-row first-up/down/reactivate/re-up lifecycle with
  renewed legacy demotion, retained v3 liveness, reinstalled constraint, and
  second rejected legacy-live update. Record the implementation SHA and any
  concerns.

---

### Schema-v3 cutover Task 4: Remove legacy ticket rendering, seeds, and budget fallback

**Status:** COMPLETE and independently approved at
`cfe95f17e5a75a2a9c71053bc6cd901003f31263`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-4-approval-manifest.sh`, binding the report
and PASS review in the ledger. Its immutable package is
`review-00629a0a5e..cfe95f17e5.diff`
(`15d3af5c78696184a4df12b10fbe597b801a50e5d1aa2b3b2bd3ffd136e35779`).
The final 16-path range deletes the four renderer paths and five root legacy
seeds, retains exactly five v3 seeds discovered with `os.ReadDir("seeds")`,
makes budget zero uncapped, removes the resolver fallback, and passed all six
affected packages, the 4/4 DB focus, deletion scans, and exact staging.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Execution order:** This is the fifth cutover boundary:

`Task 1 -> Task 2A -> Task 5 -> Task 3 -> Task 4`

Start only after the corrected Task 5 and Task 3 commits are independently
approved. Record both SHAs and confirm they are ancestors of the implementation
HEAD. Task 5 must already have removed every production caller of
`RenderLegacyTicket`; Task 3 must already have demoted/constrained legacy live
rows and made historical reads opaque. Do not implement this task on the
current Task-1-only tree.

**Source authority:** Global constraints, normative execution amendment, and
Task 4 in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`, plus the
approved Task 5 and Task 3 execution briefs.

**Goal:** Delete the now-orphaned schema-1/2 ticket renderer and its
file-materialization helpers, delete the five root legacy seed YAMLs and their
tests, and make an explicit positive `tickets.budget_usd` the only per-ticket
budget source. Preserve binder-only ticket dispatch, opaque historical rows,
the five v3 engineering seeds, and the legacy workflow model/compiler until
Task 2B.

## Owned files

Delete:

- `agent/dispatch/render.go`
- `agent/dispatch/render_test.go`
- `agent/api/tickets/render.go`
- `agent/api/tickets/render_test.go`
- `agent/workflow/seeds/develop-fable.yaml`
- `agent/workflow/seeds/develop.yaml`
- `agent/workflow/seeds/direct-dev.yaml`
- `agent/workflow/seeds/standard-dev.yaml`
- `agent/workflow/seeds/test-first-dev.yaml`

Modify:

- `agent/workflow/seed_test.go`
- `agent/dispatch/budgets.go`
- `agent/dispatch/budgets_test.go`
- `agent/budget/budget.go`
- `atc/atccmd/command.go`
- `atc/db/agent_dispatch_test.go`
- `fly/commands/agent_tickets.go`
- any additional `NewTicketBudgets` caller exposed by the required
  execution-time scan after Tasks 5 and 3

The ticket Markdown files, budget interface comments, and Fly help text are
reconnaissance additions to the tracked file list. They are live dependencies
of the behavior being retired, not unrelated cleanup.

## Prerequisite contract

Before editing, verify all of the following:

1. `git status --short` is empty. Do not absorb or revert another agent's
   changes.
2. The approved Task 5 SHA and approved Task 3 SHA are both ancestors of HEAD.
   Record the Task 3 SHA as this task's implementation base.
3. Task 5's package and DB report is green, and production dispatch has only
   the v3 `dispatchV3`/binder path. A pre-delete scan for
   `RenderLegacyTicket|RenderAgentStep|RenderInput|func Render\(` may find only
   `agent/dispatch/render.go`, its test, or archival text; any production
   caller outside `render.go` is a blocker and belongs to Task 5.
4. Task 3's migration report is green and migration `1773106123` is the
   database head. Do not modify its migration, preflight, or opaque factory
   behavior here.
5. Run the exact constructor inventory after Task 5:

   ```bash
   rg -n 'NewTicketBudgets\(' agent atc fly go-concourse --glob '*.go'
   ```

   Update every production and test caller atomically. On the reconciled
   pre-Task-5 tree the known callers are four in `atc/atccmd/command.go`, one
   in `atc/db/agent_dispatch_test.go`, and the unit tests; Task 5 may reformat
   or relocate them, so the execution-time scan is authoritative.

## Final interfaces

`TicketBudgets` retains the `budget.TicketBudgets` interface but no longer
knows about workflow definitions:

```go
type TicketBudgets struct {
    tickets TicketGetter
}

func NewTicketBudgets(tg TicketGetter) *TicketBudgets {
    return &TicketBudgets{tickets: tg}
}

func (b *TicketBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
    ticket, found, err := b.tickets.Get(ticketID)
    if err != nil {
        return 0, false, err
    }
    if !found {
        return 0, false, nil
    }
    if ticket.BudgetUSD != nil && *ticket.BudgetUSD > 0 {
        return *ticket.BudgetUSD, true, nil
    }
    return 0, false, nil
}
```

The constructor has exactly one argument. Remove the `workflows
WorkflowResolver` field and every live/pinned workflow lookup; do not retain an
ignored compatibility parameter. Unknown tickets and tickets with nil,
zero, or non-positive budgets are uncapped and return `(0, false, nil)`.
Ticket-store errors continue to propagate so the checker remains fail-closed.
`WorkflowResolver` itself remains because binder-only dispatch still resolves
workflow definitions.

Update active contract text consistently:

- `agent/dispatch/budgets.go`: describe explicit ticket budget or uncapped;
- `agent/budget/budget.go`: both `Checker.TicketRemaining` and
  `TicketBudgets` comments must no longer promise a workflow default;
- `atc/atccmd/command.go`: remove “frozen-workflow default” wiring comments;
- `fly/commands/agent_tickets.go`: change `--budget` help from
  `0 = workflow default` to `0 = uncapped`.

The five retained seed manifests are exactly:

```text
agent/workflow/seeds/anonymization-audit-v3/workflow.yml
agent/workflow/seeds/code-review-v3/workflow.yml
agent/workflow/seeds/log-diagnosis-v3/workflow.yml
agent/workflow/seeds/small-fix-v3/workflow.yml
agent/workflow/seeds/version-upgrade-v3/workflow.yml
```

`seed_test.go` must retain the existing detailed compile, signature, function
render, wait/publisher, no-implicit-harvest, and no-ticket/workspace assertions
for all five. Delete the legacy `workflow.Parse` tests and their shared helper;
do not weaken the v3 render assertions.

## RED-first tests

### 1. Budget contract

First rewrite `agent/dispatch/budgets_test.go` against the one-argument
constructor. Remove `budgetWorkflows`, all `workflow.Config` fixtures, and the
live/pinned workflow-default expectations. Cover:

- a positive explicit ticket budget is returned even when the ticket carries
  workflow name/version metadata;
- nil, zero, and negative explicit values are uncapped;
- an unknown ticket is uncapped;
- ticket-store errors propagate unchanged.

The core regression case is:

```go
func TestTicketBudgetsDoesNotReadWorkflowDefaults(t *testing.T) {
    getter := budgetTicketGetter{rows: map[int]tickets.Ticket{
        42: {ID: 42, WorkflowName: "small-fix"},
    }}

    amount, found, err := dispatch.NewTicketBudgets(getter).BudgetUSD(42)
    if err != nil || found || amount != 0 {
        t.Fatalf("amount=%v found=%v err=%v, want 0/false/nil", amount, found, err)
    }
}
```

This is RED before production changes because the constructor still requires a
`WorkflowResolver`.

### 2. Exact seed inventory

Add `TestOnlyVersionThreeEngineeringSeedsRemain` to
`agent/workflow/seed_test.go`. Use `os.ReadDir("seeds")` and compare the sorted
root entry names with exactly:

```go
[]string{
    "anonymization-audit-v3",
    "code-review-v3",
    "log-diagnosis-v3",
    "small-fix-v3",
    "version-upgrade-v3",
}
```

Also assert every retained root entry is a directory. This test must fail
before deletion because the five legacy root YAML files are still present.
The existing v3 compile/render test then proves the retained directories are
not merely named correctly.

### 3. Demonstrate RED

Run:

```bash
go test ./agent/dispatch ./agent/workflow \
  -run 'Test.*(TicketBudget|OnlyVersionThreeEngineeringSeeds)' -count=1
```

Expected: FAIL. The budget package does not compile against the new
constructor call, and/or the seed inventory reports the five extra root YAML
files. Record the exact failure.

Also run the pre-delete structural checks and record that they fail:

```bash
! rg -n \
  'RenderLegacyTicket|RenderAgentStep|type RenderInput|func Render\(|RenderSpecMarkdown|RenderPlanMarkdown' \
  agent/dispatch agent/api/tickets --glob '*.go'

! rg -n \
  'def\.Config\.Budget\.TicketUSD|workflow default|frozen-workflow default|0 = workflow default' \
  agent/dispatch/budgets.go agent/budget/budget.go \
  atc/atccmd/command.go fly/commands/agent_tickets.go
```

These are intentionally RED before removal and make the deletion proof
non-vacuous.

## Implementation

1. Implement the one-argument `TicketBudgets` shown above. Remove the workflow
   import if it becomes unused, the resolver field, and all live/pinned
   resolution branches. Keep the `budget.TicketBudgets` compile-time
   assertion.
2. Update every `NewTicketBudgets` call from the execution-time inventory.
   The command's dispatcher, engine, API, and any duplicate construction
   sites must all pass only the ticket factory. Update the Task-5 replacement
   DB dispatch fixture the same way; preserve its real workflow store for
   dispatch/binder use, but do not pass it to budgets.
3. Update the active comments and Fly help text listed above.
4. Delete `agent/dispatch/render.go` and `render_test.go` in full. This removes
   `RenderInput`, `RenderAgentStep`, `RenderLegacyTicket`, the deprecated
   `Render` alias, prompt templating, legacy skill/ticket tasks, legacy harvest
   conversion, repo-resource construction, and all renderer-only imports.
5. Delete `agent/api/tickets/render.go` and `render_test.go`. Recon confirms
   `RenderSpecMarkdown`, `RenderPlanMarkdown`, and `taskGlyph` are consumed
   only by the legacy renderer; Task 5 removes the last dispatch comment/call.
   Do not delete the tickets API models or ordinary task/spec endpoints.
6. Delete the five root legacy YAML files and delete their legacy-only tests
   from `seed_test.go`. Retain the five v3 directories, their complete
   compile/render table, and the new exact inventory test.
7. Run `gofmt` on modified Go files. Deletions must be real deletions, not
   empty shims, aliases, or deprecated wrappers.

## Required preservation

- Preserve Task 5's v3 binder flow, durable `WorkflowRunID`, pipeline-run
  reference, retry/requeue semantics, work-item/repository bindings, secret
  preparation/reaping, and dispatcher error mapping.
- Preserve Task 3's opaque historical reads, non-v3 promotion rejection,
  migration `1773106123`, constraint, and preflight/head changes.
- Preserve generic v3 `await_snapshot`, publisher, task, agent, and ordinary
  Concourse plan rendering. `workflow.RenderFunction` is the surviving
  renderer.
- Preserve the `agent/harvest` package and generic harvest execution; only its
  legacy dispatch-renderer import/conversion disappears.
- Preserve `workflow.Config`, `Definition.Config`, `Parse`, `Compile`,
  `compileLegacy`, `Budget.TicketUSD`, and their parser/compiler tests until
  Task 2B. Do not use Task 4's focused scan as a premature repository-wide
  `workflow.Config` deletion check.
- Preserve the `budget.TicketBudgets` interface and `NoTicketBudgets`; only the
  dispatch implementation's source of truth changes.
- Do not edit migration-local `legacyworkflow.TicketUSD` or any migration.
- Do not edit Fly workflow import behavior in this task. A temporary file
  called `standard-dev.yaml` and UI/test data using workflow names such as
  `develop` or `standard-dev` are examples, not dependencies on the deleted
  seed files.
- Historical plans/specs under `docs/superpowers` describe the superseded
  architecture and may retain old symbols and seed paths. No active
  non-historical Markdown or shell dependency was found; do not rewrite
  archival documents to satisfy production scans.
- The runner regression comment mentioning `develop-fable` records the origin
  of output-path hardening and does not load the seed; leave its behavior and
  test intact.

## GREEN verification

PostgreSQL is required for the focused DB suite:

```bash
pg_isready
```

Then run:

```bash
go test \
  ./agent/dispatch \
  ./agent/workflow \
  ./agent/api/tickets \
  ./agent/budget \
  ./atc/atccmd \
  ./fly/commands \
  -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='dispatching a ticket end-to-end|the dispatcher loop over real stores' \
  ./atc/db
```

Report the selected DB spec count so the focus is demonstrably non-vacuous.
Do not use `--race`.

Run all structural checks:

```bash
! rg -n \
  'RenderLegacyTicket|RenderAgentStep|type RenderInput|func Render\(|RenderSpecMarkdown|RenderPlanMarkdown' \
  agent/dispatch agent/api/tickets --glob '*.go'

! rg -n 'github.com/concourse/concourse/agent/harvest|harvest\.' \
  agent/dispatch --glob '*.go'

! rg -n \
  'def\.Config\.Budget\.TicketUSD|workflow default|frozen-workflow default|0 = workflow default|budgetWorkflows\(' \
  agent/dispatch/budgets.go agent/dispatch/budgets_test.go \
  agent/budget/budget.go atc/atccmd/command.go fly/commands/agent_tickets.go

rg -n '0 = uncapped' fly/commands/agent_tickets.go

! rg -n \
  'workflows[[:space:]]+WorkflowResolver|b\.workflows' \
  agent/dispatch/budgets.go

rg -n 'NewTicketBudgets\(' agent atc fly go-concourse --glob '*.go'

! test -e agent/workflow/seeds/develop-fable.yaml
! test -e agent/workflow/seeds/develop.yaml
! test -e agent/workflow/seeds/direct-dev.yaml
! test -e agent/workflow/seeds/standard-dev.yaml
! test -e agent/workflow/seeds/test-first-dev.yaml

rg -n 'schema_version:[[:space:]]+3' \
  agent/workflow/seeds/*-v3/workflow.yml

git diff --check
```

The positive constructor inventory must show the one-argument definition and
only one-argument callers; Go compilation is the authoritative arity check.
The positive seed scan must report all five retained manifests.

## Clean-scope and commit

Before committing:

1. Run `git status --short` and `git diff --name-only`.
2. Confirm every path is in the owned-file list above and all nine requested
   deletions are shown as deletions. Stop if an unrelated path appears.
3. Confirm no Task 5 binder file, Task 3 migration/factory file, historical
   document, or Task 2B legacy-model file changed except the explicitly owned
   callers/comments.
4. Run `git diff --check` after staging.

Stage exactly:

```bash
git add -- \
  agent/dispatch/render.go \
  agent/dispatch/render_test.go \
  agent/dispatch/budgets.go \
  agent/dispatch/budgets_test.go \
  agent/api/tickets/render.go \
  agent/api/tickets/render_test.go \
  agent/budget/budget.go \
  agent/workflow/seeds/develop-fable.yaml \
  agent/workflow/seeds/develop.yaml \
  agent/workflow/seeds/direct-dev.yaml \
  agent/workflow/seeds/standard-dev.yaml \
  agent/workflow/seeds/test-first-dev.yaml \
  agent/workflow/seed_test.go \
  atc/atccmd/command.go \
  atc/db/agent_dispatch_test.go \
  fly/commands/agent_tickets.go

git diff --cached --name-only
git diff --cached --check
git commit -m "refactor(agent): remove legacy ticket workflow rendering"
```

If the execution-time constructor scan finds another caller, add that exact
path to ownership, verification, and staging, and document why in the report;
do not keep a two-argument compatibility constructor.

Write `.superpowers/sdd/v3-cutover-task-4-report.md` with:

- Task 5 and Task 3 prerequisite SHAs and implementation base;
- RED failures for budget arity, seed inventory, and structural scans;
- GREEN package/spec counts;
- exact deleted files and retained five-seed inventory;
- final `NewTicketBudgets` caller inventory;
- preservation and negative-scan evidence;
- commit SHA and any concerns.

Use `apply_patch`; this is a shared worktree, so never revert another agent's
changes.


---

# Schema-v3 cutover Task 2B: Delete the legacy runtime workflow model

**Status:** COMPLETE and independently approved at
`d4d111240f4f224a902f9c9ff674b6cbf529fac8`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-2b-approval-manifest.sh`, binding the report
and PASS review in the ledger. The exact 18-path range deletes all four
legacy-model test/model files, creates the Fly command test, and modifies the
13 other literal paths below. Compiler security order, opaque history,
current-model scans, seven named workflow tests, three Fly command tests,
22/22 workflow and 30/30 experiment DB focuses, 10/10 Fly integration focus,
five affected packages, 668/668 Fly integration, and exact staging passed.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Execution order:** This is the sixth cutover boundary:

```text
Task 1 -> Task 2A -> Task 5 -> Task 3 -> Task 4 -> Task 2B
```

Start only after Task 4 is committed and independently approved. Record the
approved Task 1, Task 2A, Task 5, Task 3, and Task 4 SHAs. Verify that each is
an ancestor of the next and that the approved Task 4 SHA is an ancestor of the
implementation HEAD. Use the Task 4 SHA as the implementation base. Do not
implement this task on the current Task-1-only tree.

**Source authority:** The global constraints, normative execution amendment,
and Task 2B in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`, plus the
approved Task 2A, Task 5, Task 3, and Task 4 execution briefs.

**Goal:** Make the runtime workflow package structurally schema-v3-only.
Delete the released v1/v2 runtime model, parser, compiler, compatibility
fields, and tests while preserving the migration-local released decoder,
opaque historical PostgreSQL reads, v3 import/promotion/dispatch, generic
workflow runs, and Fly's ability to display historical raw YAML.

## Prerequisite gate

Before editing:

1. Require a clean worktree. Do not absorb or revert another agent's changes.
2. Verify the approved commit chain:

   ```bash
   git status --short --untracked-files=all
   git merge-base --is-ancestor "$TASK1_SHA" "$TASK2A_SHA"
   git merge-base --is-ancestor "$TASK2A_SHA" "$TASK5_SHA"
   git merge-base --is-ancestor "$TASK5_SHA" "$TASK3_SHA"
   git merge-base --is-ancestor "$TASK3_SHA" "$TASK4_SHA"
   git merge-base --is-ancestor "$TASK4_SHA" HEAD
   ```

   Use task-specific shell variable names exactly as above; do not reuse a
   system environment variable.
3. Confirm the prerequisite boundaries are present:

   ```bash
   rg -n 'type UnsupportedSchemaVersionError|func RequireSchemaVersion3' \
     agent/workflow/{definition.go,parse.go}

   rg -n 'agent_workflow_definitions_live_schema_v3_check' \
     atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.*

   ! rg -n \
     'RenderLegacyTicket|RenderAgentStep|type RenderInput|func Render\(.*RenderInput' \
     agent/dispatch agent/api/tickets --glob '*.go'

   ! find agent/workflow/seeds -maxdepth 1 -type f -name '*.yaml' -print -quit \
     | grep .
   ```

4. Read the Task 2A package-internal fixture
   `agent/workflow/memory_store_admission_internal_test.go` after rebasing.
   It must still seed historical state only by holding `MemoryStore.mu`; do
   not replace it with a production fixture API.
5. Run the exact post-prerequisite inventory below. Any active compile-time
   consumer is part of Task 2B and must be added as one exact path to
   ownership, verification, and staging. Do not keep an alias or shim:

   ```bash
   rg -n \
     'workflow\.(Config|Parse|Compile)\b|Compiled\.Legacy\b|compiled\.Legacy\b|definition\.Legacy\b' \
     agent atc fly go-concourse ci-agent --glob '*.go' \
     --glob '!atc/db/migration/legacyworkflow/**'

   rg -n \
     'type Config struct|type Step struct|Legacy[[:space:]]+\*Config|Config[[:space:]]+Config|func Parse\(|func Compile\(|func compileLegacy\(' \
     agent/workflow --glob '*.go'
   ```

   The first scan deliberately uses word boundaries: it must not match
   `workflow.ParseCompiled` or `workflow.CompileDefinition`. Ordinary
   `atc.Config`, `atc.Step`, step `.Config`, outcome/repository-change
   `Legacy` booleans, and migration-local `legacyworkflow` types are unrelated
   and must not be edited.

## Owned files

Delete:

- `agent/workflow/config.go`
- `agent/workflow/parse_test.go`
- `agent/workflow/parse_v2_test.go`
- `agent/workflow/validate_test.go`

Modify:

- `agent/workflow/definition.go`
- `agent/workflow/parse.go`
- `agent/workflow/compile.go`
- `agent/workflow/parse_v3_test.go`
- `agent/workflow/compile_test.go`
- `agent/workflow/typecheck_test.go`
- `agent/workflow/memory_store.go`
- `agent/workflow/memory_store_test.go`
- `agent/workflow/memory_store_admission_internal_test.go`
- `atc/db/agent_workflows_factory.go`
- `atc/db/agent_workflows_factory_test.go`
- `fly/commands/agent_workflows.go`
- `fly/integration/agent_workflows_test.go`

Create:

- `fly/commands/agent_workflows_test.go`

Verification-only; do not modify unless the post-Task-4 inventory contradicts
the approved Task 4 result:

- `agent/workflow/seed_test.go`
- `agent/api/workflows/handler.go`
- `agent/api/workflows/handler_test.go`
- `agent/dispatch/**`
- `agent/workflowrun/**`

Task 4 already deletes all legacy seed tests and adds the exact five-directory
inventory. Task 2A already converts accepted API fixtures to v3 and retains
the typed 422 mapping. Tasks 5 and 4 already remove the dispatch/runtime
consumers. These packages remain required verification targets, but staging
them speculatively would blur ownership.

## Hidden consumer disposition

Reconnaissance found these consumers that are easy to miss when following
only the tracked Task 2B file list:

- `agent/workflow/memory_store_admission_internal_test.go` is created by Task
  2A after the current base. It directly inserts a schema-2 row under the
  store lock and therefore owns the MemoryStore no-decoder regression.
- Task 3's `atc/db/agent_workflows_factory_test.go` opaque-history assertions
  deliberately mention the temporary zero `Definition.Config`; Task 2B must
  remove that assertion without weakening the malformed-YAML/wrong-shape-JSON
  proof.
- `agent/workflow/parse_v3_test.go` retains inline v1/v2 parity cases after
  Task 4 deletes the root seed YAMLs. Task 2B must replace that parity test,
  not assume Task 4 removed all parser fixtures.
- `fly/integration/agent_workflows_test.go` currently shares one schema-1
  constant between import and show. Import must become v3; show must retain a
  separate schema-1 raw fixture to prove historical display remains opaque.
- Task 2A's workflow handler tests should have no deleted-field success
  assertion after their accepted fixtures are converted to v3. They are a
  compile/regression target, not owned unless the execution-time scan proves
  the approved Task 2A result differs.
- Task 5 and Task 4 should leave no active dispatch reference to
  `workflow.Config`, `workflow.Step`, `workflow.Parse`, or `workflow.Compile`.
  Any such post-prerequisite match is a blocker/ownership addition, not a
  reason to retain compatibility.
- `atc/db/migration/legacyworkflow` intentionally contains similarly named
  released types. It is migration-private and immutable. Unrelated
  `atc.Config`, `atc.Step`, and `Legacy` boolean fields elsewhere are not
  hidden workflow-model consumers.

The Store interface does not change, so generated fakes are not expected to
change. Package compilation plus the exact scan is authoritative if a
prerequisite refactor relocates any consumer.

## Final runtime interfaces

`CompiledDefinition` is no longer a tagged union:

```go
type CompiledDefinition struct {
    SchemaVersion int             `json:"schema_version" yaml:"schema_version"`
    Name          string          `json:"name" yaml:"name"`
    Description   string          `json:"description,omitempty" yaml:"description,omitempty"`
    Function      *FunctionConfig `json:"function,omitempty" yaml:"function,omitempty"`
}
```

`Definition` retains durable metadata, `Compiled`, `RawYAML`, and
`SourceManifest`, but has no `Config` field. `CompiledDefinition.Validate`
accepts only `SchemaVersion == 3`, a nonblank name, and a non-nil valid
`Function`. `VersionMetadata` and `PublicSignature` operate only on that v3
model; they have no schema-1/2 arm.

The surviving source entry points are:

```go
func RequireSchemaVersion3(source []byte) error
func ParseCompiled(source []byte) (*CompiledDefinition, error)
func CompileDefinition(source Manifest) (*CompiledDefinition, error)
```

Delete all of the following rather than deprecating or aliasing them:

- runtime `Config` and its associated `Defaults`, `Budget`, `Sidecar`, `Step`,
  `HITL`, `GatePolicy`, `Gate`, `Judge`, and `RubricDimension` types;
- `Config.SourceFormatField`, `Config.Validate`, `Config.validateJudge`, and
  the legacy prompt/template/skill validators used only by that model;
- public `Parse`;
- public `Compile`;
- private `compileLegacy`;
- `CompiledDefinition.Legacy`;
- `Definition.Config`;
- every schema-1/2 switch arm and legacy compatibility comment.

Do not rename `ParseCompiled` or `CompileDefinition` in this task. They remain
the established v3 APIs.

## Compiler ordering and safety

`CompileDefinition` must keep manifest security ahead of content parsing and
make the Task 2A helper the first content-level compiler boundary:

```text
Manifest.Validate
  -> require workflow.yml
  -> RequireSchemaVersion3(workflow.yml)
  -> parseFunctionDefinition
  -> compileFunctionAssets
  -> ValidateFunction
```

Implement that ordering explicitly:

```go
func ParseCompiled(raw []byte) (*CompiledDefinition, error) {
    if err := RequireSchemaVersion3(raw); err != nil {
        return nil, err
    }
    return parseFunctionDefinition(raw)
}

func CompileDefinition(source Manifest) (*CompiledDefinition, error) {
    if err := source.Validate(); err != nil {
        return nil, err
    }
    raw, found := source["workflow.yml"]
    if !found {
        return nil, fmt.Errorf("workflow: manifest has no workflow.yml")
    }
    if err := RequireSchemaVersion3([]byte(raw)); err != nil {
        return nil, err
    }
    definition, err := parseFunctionDefinition([]byte(raw))
    if err != nil {
        return nil, err
    }
    if err := compileFunctionAssets(source, definition); err != nil {
        return nil, err
    }
    if _, err := ValidateFunction(definition.Function); err != nil {
        return nil, err
    }
    return definition, nil
}
```

Keep `parseFunctionDefinition` private and retain its own schema-3
defense-in-depth check. Do not route through a legacy parser or infer a
default schema. For a syntactically valid schema-1/2 header, the stable
`UnsupportedSchemaVersionError` must win before legacy structural validation,
missing legacy assets, or name validation. Manifest bounds/path/UTF-8 checks
and the missing-`workflow.yml` error still precede the schema gate.
Malformed, missing, or non-integer headers remain ordinary parse errors, not
unsupported-version errors. A schema-3 multi-document source passes the
header gate and is then rejected by the strict function parser.

Preserve all v3 compiler behavior below:

- strict source keys and ordinary Concourse declaration/step decoding;
- public input/output order and optionality;
- `disposition_output`;
- prompt and system-prompt file resolution;
- context order/framing and author-only field erasure;
- selected skill-tree union and both compiled-byte budgets;
- named capability validation, deep copies, MCP endpoint injection, and
  deterministic errors;
- privileged transformation-task rejection and hermetic task execution;
- ordinary Concourse validation, DAG type checking, output annotation, and
  public signature compatibility.

## Historical opaque-read contract

Task 2B deletes a runtime decoder; it must not delete historical rows or make
reads compile them.

For PostgreSQL, preserve Task 3's branch on persisted `SchemaVersion` before
`compileStoredWorkflowSource`:

- schema 3 decodes its stored source, compiles it, and checks stored name,
  schema, and signature metadata;
- schema 1/2 returns exact persisted metadata and `RawYAML`, nil
  `SourceManifest`, and zero `CompiledDefinition`;
- malformed legacy YAML and valid wrong-shape JSONB source manifests remain
  readable by both `Get` and `Latest`;
- `List` and `Versions` remain metadata-only;
- promotion rejects persisted non-v3 metadata before source decoding,
  validator invocation, live lookup, or mutation;
- corrupt v3 rows still fail closed.

In `populateCompiledWorkflowDefinition`, retain only compiled-v3 and source
population. Remove the `Definition.Config` zeroing and `compiled.Legacy`
branch. Do not move the schema branch below `compileStoredWorkflowSource` and
do not import the migration decoder.

Mirror the same no-decoder rule in the MemoryStore's private historical test
fixture. `cloneMemoryDefinition` must clear content first as it does today.
For a directly seeded non-v3 row and `includeContent == true`, return its exact
stored `RawYAML`, nil `SourceManifest`, and zero `CompiledDefinition` without
calling `CompileDefinition`. Normal imported rows are v3 and continue to be
recompiled from their cloned manifest so caller mutation cannot change store
authority. Promotion retains Task 2A's metadata-first rejection.

The migration-local package and migration are immutable:

```text
atc/db/migration/legacyworkflow/**
atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go
atc/db/migration/migrations/1773106101_add_workflow_schema_signature.down.sql
atc/db/migration/add_workflow_schema_signature_test.go
atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.*
```

## Fly behavior after the cutover

Fly import is v3-only, but Fly show remains format-agnostic:

- `importWorkflowDir` continues to call `CompileDefinition` before any HTTP
  request;
- change `importWorkflowFile` from `ParseCompiled(raw)` to
  `CompileDefinition(Manifest{"workflow.yml": string(raw)})`. A raw file is a
  one-file manifest, so this also rejects a v3 file that refers to absent
  prompt, context, or skill files locally instead of deferring the same error
  to the server;
- valid v1 and v2 file/directory imports fail locally with the stable typed
  error and make zero HTTP requests;
- valid v3 file imports still POST the exact raw YAML body;
- valid v3 directory imports still POST the canonical `{"files": ...}`
  manifest and exclude hidden junk;
- `--set-live` remains a separate post-import request;
- `show` must continue to print exact `RawYAML` supplied by the server even
  when its persisted metadata says schema 1 or 2. It must not call the
  compiler on a historical response.

In `fly/integration/agent_workflows_test.go`, split the current shared
schema-1 constant into a v3 import fixture and a deliberately legacy
historical-show fixture. Keep the show assertion over the legacy raw YAML;
convert only accepted imports and promotion responses to schema 3.

## RED-first tests

Write or convert the tests before changing production code.

### 1. Schema gate and model shape

In `agent/workflow/parse_v3_test.go`:

- replace `TestParseV3LegacyArmsAreExclusive` with
  `TestCompiledDefinitionPublicMethodsRejectNonV3`:
  a valid parsed function passes; schema 1/2/4 direct models fail
  `Validate`, `VersionMetadata`, and `PublicSignature`; a v3 model with nil
  `Function` fails all applicable methods; and a schema-3 model with a blank
  name is rejected directly;
- replace `TestParseV1V2StoredFixturesUnchanged` with
  `TestParseCompiledRejectsLegacySchemas`. Use self-contained, otherwise
  valid schema-1 and schema-2 YAML; assert `errors.As` to
  `UnsupportedSchemaVersionError`, exact `Got`, exact stable text, and a nil
  definition;
- retain `TestRequireSchemaVersion3RejectsUnsupportedVersions` added by Task
  2A and all v3 strictness/round-trip tests;
- remove the legacy seed glob and the now-unused `os`/`filepath` imports.

In `agent/workflow/compile_test.go`:

- add
  `TestCompileDefinitionRejectsLegacyBeforeContentOrAssetValidation`;
- use a schema-1 document with legacy-invalid content and a schema-2 manifest
  with a missing prompt file; both must return the typed unsupported error,
  not the legacy validation/asset error;
- add/retain cases proving an empty manifest and a nonempty manifest without
  `workflow.yml` fail before the schema gate, while malformed schema-3 source
  is not typed as unsupported;
- delete `v2Manifest`, `validV1YAML`,
  `TestCompileResolvesEverything`, `TestCompileSingleFileV1Passthrough`, the
  legacy cases in `TestCompileErrors`, and
  `TestCompileDefinitionKeepsLegacyCompileBehavior`;
- retain every `TestCompileV3...` test and its asset/limit/capability coverage.

In `agent/workflow/typecheck_test.go`, keep the ordinary-v3 validation half of
`TestTypeCheckCompileDefinitionRunsOrdinaryValidationAndPreservesLegacy`,
rename it to `TestCompileDefinitionRunsOrdinaryValidation`, and delete only
the legacy comparison half.

Delete `validate_test.go` in full. Reconnaissance shows every test in it calls
the legacy `Parse`/`Config.Validate` path; the v3 parser, compiler, typecheck,
render, and seed tests are the surviving validation contract.

### 2. Definition wire shape and MemoryStore opacity

In `agent/workflow/memory_store_test.go`, add
`TestDefinitionJSONOmitsLegacyCompatibilityFields`:

1. import a v3 manifest;
2. marshal the returned `Definition` to a `map[string]any`;
3. assert the top-level object has no `"config"` key;
4. assert its `"compiled"` object has no `"legacy"` key and does contain a
   non-nil `"function"` object.

This is RED before `Definition.Config` is deleted because that field does not
have `omitempty`.

Extend `memory_store_admission_internal_test.go` rather than adding a
production seeding hook. Name the top-level regression exactly
`TestMemoryStoreHistoricalNonV3ReadsRemainOpaque`. For its directly inserted
malformed schema-2 row, assert `Get` and `Latest` return the exact metadata
and raw YAML with nil `SourceManifest` and zero `CompiledDefinition`, without
a parse error. Retain the existing promotion assertions: nested error types,
no validator call, the v3 row still live, and the legacy row still non-live.

Convert or remove any Task-2A-era MemoryStore assertion still naming
`Config`/`Legacy`; do not weaken monotonic versioning, idempotent manifest
hashing, immutable clone, pagination, signature compatibility, promotion, or
v3 validator tests.

### 3. PostgreSQL preservation and wire shape

In Task 3's historical-row `Get`/`Latest` test:

- remove the now-impossible `Config == workflow.Config{}` assertion;
- retain exact malformed raw YAML, wrong-shape JSONB, every persisted metadata
  field, nil `SourceManifest`, and zero `CompiledDefinition` assertions;
- marshal at least one returned historical definition and assert there is no
  top-level `"config"` key, making field removal RED before production
  changes;
- retain the separate corrupt-v3 fail-closed case and metadata-only
  `List`/`Versions` case.

All accepted import fixtures remain v3 from Task 2A. Do not reintroduce
runtime legacy imports to create historical test rows; continue inserting
them directly in SQL.

### 4. Fly local rejection and historical display

Create `fly/commands/agent_workflows_test.go` in package `commands`. Use a
fake `rc.Target` whose HTTP transport increments a request counter. Table-test
the helpers under these exact top-level tests:

- `TestImportWorkflowFileRejectsNonV3Locally`: an otherwise valid schema-1
  file passed to `importWorkflowFile`;
- `TestImportWorkflowDirRejectsNonV3Locally`: an otherwise valid schema-2
  directory passed to `importWorkflowDir`;
- `TestImportWorkflowFileRejectsMissingAssetsLocally`: a schema-3 raw file
  whose one-file manifest omits a referenced asset.

For the non-v3 cases, assert the typed error with exact `Got` and stable text.
Every case must assert the request counter remains zero.

In the Fly integration test:

- make successful file and directory imports schema 3;
- assert file import posts exact raw bytes and directory import posts the
  exact manifest;
- add valid legacy file and directory cases whose stderr contains the stable
  unsupported-version message and for which the mock server observes no API
  request;
- retain a historical schema-1 `show` response and exact raw-YAML output.

### 5. Demonstrate RED non-vacuously

After the test edits but before production deletion, run:

```bash
go test ./agent/workflow \
  -run 'Test(ParseCompiledRejectsLegacySchemas|CompiledDefinitionPublicMethodsRejectNonV3|CompileDefinitionRejectsLegacyBeforeContentOrAssetValidation|DefinitionJSONOmitsLegacyCompatibilityFields|MemoryStoreHistoricalNonV3ReadsRemainOpaque)' \
  -count=1 -v

go test ./fly/commands \
  -run 'TestImportWorkflow(FileRejectsNonV3Locally|DirRejectsNonV3Locally|FileRejectsMissingAssetsLocally)' \
  -count=1 -v

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentWorkflowsFactory' \
  ./atc/db

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='fly agent workflows.*import' \
  ./fly/integration
```

Record the `=== RUN` line for every exact Go test named above. Package-level
`ok` output is not sufficient evidence because Go succeeds when `-run`
selects no tests.

Expected:

- workflow tests fail because mixed `ParseCompiled`/`CompileDefinition` still
  accept v1/v2, `Definition` still serializes `"config"`, and MemoryStore
  still recompiles a directly seeded historical row;
- Fly tests fail because file parsing still accepts v1, directory compilation
  still accepts v2, and raw-file validation does not resolve missing assets;
- the DB test fails on the serialized `"config"` field while its opaque-read
  assertions continue to pass;
- the Fly integration focus reports selected specs and fails its local legacy
  rejection cases.

Before deleting code, record the matches from these positive inventories:

```bash
rg -n \
  'type Config struct|type Step struct|Legacy[[:space:]]+\*Config|Config[[:space:]]+Config' \
  agent/workflow --glob '*.go'

rg -n \
  '^func Parse\(|^func Compile\(|^func compileLegacy\(' \
  agent/workflow --glob '*.go'

rg -n \
  'workflow\.(Config|Parse|Compile)\b|Compiled\.Legacy\b|compiled\.Legacy\b|definition\.Legacy\b' \
  agent atc fly go-concourse ci-agent --glob '*.go' \
  --glob '!atc/db/migration/legacyworkflow/**'
```

These recorded pre-delete matches make the final negative scans
non-vacuous. The Ginkgo commands must report selected-spec counts; a zero-spec
focus is a failure.

## Implementation sequence

1. Collapse `CompiledDefinition` and `Definition` in `definition.go`. Simplify
   `Validate`, `VersionMetadata`, and `PublicSignature` to v3 only. Preserve
   the Task 2A typed error/helper and the Store interface.
2. In `parse.go`, make `ParseCompiled` require schema 3 and directly invoke the
   function parser. Delete public `Parse` and every legacy-only validator from
   the end of the file. Remove only imports made unused by that deletion;
   `io` remains required by the v3 YAML/JSON decoder.
3. In `compile.go`, delete public `Compile` and private `compileLegacy`.
   Implement the explicit manifest/security/schema order above. Preserve all
   function asset compiler code.
4. Delete `config.go`, `parse_test.go`, `parse_v2_test.go`, and
   `validate_test.go`. Perform the precise test conversions listed above;
   avoid wholesale rewrites of v3 test tables.
5. Remove compatibility population from `memory_store.go`. Add the private
   opaque historical clone branch and preserve v3 defensive recompilation.
6. Remove compatibility population from
   `atc/db/agent_workflows_factory.go`. Preserve Task 3's pre-compile
   historical branch and metadata-first promotion rejection exactly.
7. Change Fly raw-file validation to the one-file `CompileDefinition` path,
   add the unit tests, and convert only import fixtures in integration. Keep
   historical show format-agnostic.
8. Run `gofmt` on modified Go files. Deletions must be real deletions; no
   `Deprecated:` wrapper, type alias, ignored field, or compatibility
   function may remain.

## GREEN verification

PostgreSQL is required for the focused DB suite:

```bash
pg_isready
```

If the check fails because a stale local test server owns the test port,
identify that exact process and stop it before restarting PostgreSQL. Do not
run with `--race`.

Run focused tests first:

```bash
go test ./agent/workflow \
  -run 'Test(ParseCompiledRejectsLegacySchemas|CompiledDefinitionPublicMethodsRejectNonV3|RequireSchemaVersion3|CompileDefinition|DefinitionJSONOmitsLegacyCompatibilityFields|MemoryStoreHistoricalNonV3ReadsRemainOpaque)' \
  -count=1 -v

go test ./fly/commands \
  -run 'TestImportWorkflow(FileRejectsNonV3Locally|DirRejectsNonV3Locally|FileRejectsMissingAssetsLocally)' \
  -count=1 -v

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentWorkflowsFactory' \
  ./atc/db

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='AgentExperimentsFactory' \
  ./atc/db

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='fly agent workflows.*(show|import)' \
  ./fly/integration
```

Record every exact focused Go `=== RUN` line and report the
AgentWorkflowsFactory, AgentExperimentsFactory, and Fly selected-spec counts
separately.

Then run the complete affected packages:

```bash
go test \
  ./agent/workflow \
  ./agent/api/workflows \
  ./agent/dispatch \
  ./agent/workflowrun \
  ./fly/commands \
  -count=1

make test-fly-integration
```

Run the exact absence checks:

```bash
! rg -n \
  'workflow\.(Config|Parse|Compile)\b|Compiled\.Legacy\b|compiled\.Legacy\b|definition\.Legacy\b' \
  agent atc fly go-concourse ci-agent --glob '*.go' \
  --glob '!atc/db/migration/legacyworkflow/**'

! rg -n \
  'type Config struct|type Step struct|Legacy[[:space:]]+\*Config|Config[[:space:]]+Config|^func Parse\(|^func Compile\(|^func compileLegacy\(' \
  agent/workflow --glob '*.go'

! rg -n 'Legacy[[:space:]]*:' \
  agent/workflow \
  atc/db/agent_workflows_factory.go \
  atc/db/agent_workflows_factory_test.go \
  fly/commands/agent_workflows.go \
  fly/commands/agent_workflows_test.go \
  fly/integration/agent_workflows_test.go \
  --glob '*.go'

! rg -n \
  'legacy schema|legacy compatibility|schema[-_ ]version[- ]1/2|schema 1/2' \
  agent/workflow/{definition.go,parse.go,compile.go,memory_store.go}

test ! -e agent/workflow/config.go
test ! -e agent/workflow/parse_test.go
test ! -e agent/workflow/parse_v2_test.go
test ! -e agent/workflow/validate_test.go
```

Run positive survival checks so success cannot be caused by deleting the
whole surface:

```bash
rg -n \
  '^func ParseCompiled\(|^func CompileDefinition\(|^func RequireSchemaVersion3\(' \
  agent/workflow/{parse.go,compile.go}

rg -n \
  'Function[[:space:]]+\*FunctionConfig|func .*PublicSignature|func ValidateFunction\(' \
  agent/workflow/{definition.go,typecheck.go}

rg -n \
  'schema_version:[[:space:]]+3' \
  agent/workflow/seeds/*-v3/workflow.yml

rg -n \
  'SchemaVersion != 3|compileStoredWorkflowSource|RawYAML|SourceManifest' \
  atc/db/agent_workflows_factory.go

rg -n \
  'CompileDefinition|unsupported schema_version|RawYAML' \
  fly/commands/agent_workflows.go \
  fly/commands/agent_workflows_test.go \
  fly/integration/agent_workflows_test.go

! rg -n 'github.com/concourse/concourse/agent/workflow' \
  atc/db/migration/legacyworkflow \
  atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go

git diff --check
```

The first two negative scans are the completion gate. Do not weaken them to
make unrelated `atc.Config` or `atc.Step` matches disappear; use the exact
qualified/structural expressions above.

## Preservation audit

Before committing, explicitly confirm:

- all five v3 engineering seed directories and Task 4's exact inventory test
  remain unchanged and green;
- raw/manifest import and promotion still return Task 2A's exact typed 422 for
  any valid non-v3 header;
- Task 5's binder-only ticket dispatch and no-side-effect non-v3 rejection
  remain green;
- Task 3's `1773106123` migration, live-v3 constraint, opaque historical
  `Get`/`Latest`, and promotion ordering are unchanged;
- generic manual workflow runs, retries, experiments, render/typecheck,
  `await_snapshot`, publishers, tasks, agents, capabilities, and ordinary
  Concourse declarations remain green;
- Fly accepts only v3 imports but still shows historical raw YAML;
- no migration file or migration-local decoder changed;
- no generated fake changed, because `workflow.Store` and the handler
  interfaces do not change.

## Exact scope and staging

Inspect scope before staging:

```bash
git status --short --untracked-files=all
git diff --name-status
git diff --check
```

The diff must contain exactly the four deletions, the thirteen modified
tracked files, and the one new Fly unit-test file listed under **Owned
files**. Stop if `seed_test.go`, `agent/api/workflows`, dispatch,
workflow-run, migration, documentation, or another unowned path appears
unless the post-prerequisite exact compile-time scan found and documented
that path.

Stage exactly:

```bash
git add \
  agent/workflow/config.go \
  agent/workflow/definition.go \
  agent/workflow/parse.go \
  agent/workflow/compile.go \
  agent/workflow/parse_test.go \
  agent/workflow/parse_v2_test.go \
  agent/workflow/parse_v3_test.go \
  agent/workflow/compile_test.go \
  agent/workflow/typecheck_test.go \
  agent/workflow/validate_test.go \
  agent/workflow/memory_store.go \
  agent/workflow/memory_store_test.go \
  agent/workflow/memory_store_admission_internal_test.go \
  atc/db/agent_workflows_factory.go \
  atc/db/agent_workflows_factory_test.go \
  fly/commands/agent_workflows.go \
  fly/commands/agent_workflows_test.go \
  fly/integration/agent_workflows_test.go

git diff --cached --name-status
git diff --cached --check
```

Require the cached path set to equal the reviewed owned set. If the
execution-time scan added a compile-time consumer, add only that exact path
and document why.

Commit with:

```bash
git commit -m "refactor(workflow): remove legacy runtime model"
```

Write `.superpowers/sdd/v3-cutover-task-2b-report.md` with:

- all prerequisite SHAs and the Task 4 implementation base;
- RED failures and the recorded pre-delete symbol inventories;
- focused and complete GREEN results, including every expected Go `=== RUN`
  line and separate workflow-factory, experiment-factory, and Fly spec counts;
- exact deleted types, functions, fields, files, and test fixtures;
- compiler ordering/error-precedence proof;
- MemoryStore and PostgreSQL opaque-history proof;
- Fly v3 import/no-request rejection and historical-show proof;
- positive survival and non-vacuous negative-scan output;
- exact staged path list, commit SHA, and concerns.

Use `apply_patch`; this is a shared worktree, so never revert another agent's
changes.


---

### Schema-v3 cutover Task 6: Durable workflow-run navigation in Fly tickets

**Status:** COMPLETE and independently approved at
`2bf21dfca5f9bb75409526c17a39631ce10b0189`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-6-approval-manifest.sh`, binding the report
and PASS review in the ledger. The immutable cumulative binary diff has
SHA-256
`ffc2f596380899b1fdbd8b0d41dc17402171c7af6e9a7643e106e0c03474e0c7`.
The exact five-path range passed all eight top-level plain-Go tests (ten RUN
witnesses), 9/9 client specs, 31/31 Fly ticket specs, 676/676 full Fly
integration, one-target watch routing, pipeline-only and malformed-201
negatives, durable-navigation scans, and exact staging.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Dependency:** Start only after Tasks 2A, 5, 3, 4, and 2B are committed and
independently approved, in the tracked plan's normative order. Record the
approved Task 2B SHA as the implementation base. The worktree must be clean
apart from this ignored brief before RED tests are added.

**Source plan:** Global constraints, normative amendment, and Task 6 in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`.

**Goal:** Make `workflow_run_id` the sole user-facing ticket invocation
identity in Fly. Listing, showing, dispatch output, and watching a ticket must
never derive or discover an `agent-ticket-<id>` pipeline. `pipeline_run_id`
remains an optional execution diagnostic, not an address or identity.

## Owned paths

- modify `fly/commands/agent_tickets.go`
- create `fly/commands/agent_tickets_test.go`
- modify `fly/commands/agent_workflow_runs.go`
- modify `go-concourse/concourse/agent_tickets_test.go`
- modify `fly/integration/agent_tickets_test.go`

Do not modify:

- `agent/api/tickets/types.go` or ticket HTTP handlers/routes;
- `go-concourse/concourse/agent_tickets.go`;
- `fly/commands/agent_workflows.go`;
- workflow import/store/compiler code;
- migrations, dispatch, Elm, or generated interfaces.

The added workflow-run command ownership is limited to factoring the existing
show-run body behind an unexported validated-target helper. Do not change its
flags, output, polling, routes, or public command behavior.

Task 5 already produces `Ticket.WorkflowRunID`, `Ticket.WorkflowName`,
`Ticket.PipelineRunID`, and `DispatchResponse.WorkflowRunID`. The generic
workflow-run command and HTTP contract already exist. Task 6 consumes those
interfaces; it must not redesign them. Task 7 owns Elm, and Task 8 owns the
repository-wide final audit.

## Exact retained workflow source/show semantics

This task has no workflow archive or export command:

- do not add `fly agent workflows export`;
- do not accept or produce tar, tar.gz, or zip workflow archives;
- do not add a route, content negotiation, media type, or go-concourse client
  method for archive transfer;
- do not reconstruct files on disk from `Definition.SourceManifest`;
- do not treat `fly agent workflows show` as a lossless source export.

After Task 2B, workflow admission remains schema-v3-only:

- a raw YAML file is the single-file degenerate manifest
  `{"workflow.yml": <exact bytes>}`;
- a source directory is packaged as the existing JSON
  `{"files": <workflow.Manifest>}` after the existing hidden-file, UTF-8,
  path, count, per-file, and total-size checks;
- a directory containing immediate workflow subdirectories retains the
  existing deterministic multi-import behavior;
- local compilation and server admission reject schema 1/2. There is no
  compatibility flag or alternate parser.

The existing definition GET remains JSON. For v3 it may carry exact
`RawYAML` and `SourceManifest`; `fly agent workflows show` continues to print
the stored `workflow.yml` bytes to stdout and only a source-file summary to
stderr. Historical schema-1/2 GETs may still print their exact opaque raw YAML
for audit after Task 3, but that is read-only history: it does not compile,
re-import, export a source tree, promote, dispatch, or execute it.

If lossless archive/export is desired later, it needs a separately approved
contract and plan. It must not be smuggled into Task 6.

## Exact ticket command behavior

### List

- Keep the existing filters and ticket columns.
- Rename the final header from the ambiguous `run` to `workflow run`.
- When `Ticket.WorkflowRunID` is non-nil, render its decimal `String()`
  losslessly, including values above JavaScript's exact integer range.
- When it is nil, render an empty workflow-run cell.
- Never infer a value from ticket ID or `PipelineRunID`.
- Unit and integration fixtures must include a pipeline-only ticket and prove
  neither its ticket ID nor `PipelineRunID` appears in the workflow-run cell.

### Dispatch and create-with-dispatch

- Update successful output to lead with the durable ID, for example:

  ```text
  dispatched ticket #7 as workflow run 9007199254740993 (pipeline run 321)
  ```

- `DispatchResponse.WorkflowRunID` is the invocation identity.
  `DispatchResponse.RunID` is labeled only as `pipeline run`.
- Do not print `PipelineName`.
- Preserve advisory `spec-lint:` warnings and the existing partial-success
  context for `create --dispatch`.
- A successful v3 dispatch fixture must carry a non-nil durable ID. If a
  malformed success response omits it, return a clear error rather than
  presenting the pipeline run as the ticket identity. Do not attempt to undo
  the already-completed server mutation.
- Exercise that malformed-201 boundary through both `tickets dispatch` and
  `tickets create --dispatch`. Direct dispatch prints no success line.
  Create-with-dispatch retains the existing
  `created #N (queued); dispatch failed: ...` partial-success context. Neither
  path sends a compensating queue or state mutation.

### Show

When a ticket has a durable ID, print exactly this navigation shape:

```text
workflow run: <workflow-run-id> · inspect with: fly -t <target> agent workflows show-run <workflow-name> <workflow-run-id>
```

If `PipelineRunID` is non-nil, print it separately:

```text
pipeline run: <pipeline-run-id>
```

Do not place the pipeline reference in the inspector command. A ticket with a
pipeline reference but no durable ID may show the diagnostic pipeline
reference, but it gets no `workflow run:` line, `inspect with`, `show-run`,
derived pipeline name, or watch hint.
Preserve the ticket, body, budget, branch, spec, and plan output.

### Watch

`fly -t <target> agent tickets watch --id N` must:

1. load and validate the target;
2. GET `/api/v1/agent/tickets/N`;
3. return `ticket N not found` for a 404;
4. return a clear, stable error when `workflow_run_id` is absent;
5. return a clear data-integrity error when a durable ID exists but
   `workflow_name` is blank;
6. construct `WorkflowsShowRunCommand` with the exact `WorkflowName`,
   `WorkflowRunID.String()`, and `Follow: true`;
7. prepare/validate that command locally, then call its unexported
   prepared-command target helper with the already validated target and
   return its result.

Refactor `WorkflowsShowRunCommand` into three narrow layers:

1. `prepare() (preparedWorkflowsShowRun, error)`, a pure step that preserves
   the existing option validation and run-ID parsing;
2. `executeWithTargetLoader`, called by public `Execute`, which prepares
   first, returns local argument errors before invoking the loader, then
   loads/validates once;
3. `executePreparedWithTarget`, an unexported helper that receives
   `rc.Target`, performs the existing detail/output request, follow polling,
   and printing, and never reloads a target.

Ticket watch uses the same preparation step and passes its existing target to
the prepared helper. It must not call public `Execute`, because doing so would
load a fresh target and issue a second `GET /api/v1/info`. Do not regress the
current public `show-run` error ordering: invalid flag combinations or run IDs
must fail without target validation or any API request.

The workflow-run command then addresses:

```text
GET /api/v1/agent/workflows/<escaped-workflow-name>/runs/<workflow-run-id>
```

and polls that same durable resource until terminal, printing status
transitions to stderr and one final run detail to stdout.

After the command's single initial target-validation request, the watch
resource sequence is exactly ticket GET followed by workflow-run detail GETs;
there is no second `/api/v1/info`.

Watch must never call:

- `/api/v1/teams/main/builds`;
- `/api/v1/builds/<id>/events`;
- `Team.Builds`;
- `BuildEvents`;
- `eventstream.Render`.

Delete `ticketPipelineName`, the build-pagination loop, `atc.DefaultTeamName`,
the `eventstream` and go-concourse imports, related comments, and the
`Timestamp` field. There is no `--timestamps` replacement in this task.

## User-facing usage and help

In `AgentTicketsCommand`, use descriptions equivalent to:

```go
Dispatch AgentTicketsDispatchCommand `command:"dispatch" description:"Dispatch a queued ticket as a durable workflow run (manual trigger)"`
Watch    AgentTicketsWatchCommand    `command:"watch" description:"Follow a ticket's durable workflow run"`
```

The generated help must:

- describe dispatch/watch in workflow-run terms;
- omit build-event and ticket-pipeline language;
- omit `--timestamps` from `agent tickets watch -h`;
- retain `--id` as the only watch option;
- retain the other ticket subcommands and their existing options.

No top-level `fly/commands/agent.go` edit is required.

## RED-first test work

Write tests before changing production code.

### `fly/commands/agent_tickets_test.go`

Use plain Go tests named with the `TestAgentTickets...` prefix. A small pure
helper/constructor may be introduced to make delegation testable without
loading a real target. Cover:

- lossless list/show formatting for
  `snapshot.WorkflowRunID(9007199254740993)`;
- pipeline-only list/show formatting: the pipeline diagnostic remains visible
  in show, but the workflow-run list cell is empty and no workflow-run or
  inspector hint is emitted;
- construction of `WorkflowsShowRunCommand` with exact workflow name, exact
  decimal run ID, `Follow: true`, and all unrelated flags false;
- watch missing-durable-ID rejection;
- blank workflow name rejection;
- dispatch-result formatting that labels the two IDs distinctly and never
  prints `PipelineName`;
- a shared dispatch-result validator rejects a successful response whose
  `WorkflowRunID` is nil, even when it carries pipeline ID/name diagnostics.
- the public show-run orchestration seam returns invalid option/run-ID errors
  before invoking a counting target loader; assert loader calls are zero and
  no request-capable target is needed.

Do not reimplement workflow-run polling in the helper. The target-aware
show-run execution remains in `agent_workflow_runs.go`. A narrow unexported
loader-injected orchestration helper is acceptable solely to prove the public
`Execute` ordering; production `Execute` passes `loadAgentTarget`.

### `go-concourse/concourse/agent_tickets_test.go`

Change the dispatch fixture to include:

```go
runID := snapshot.WorkflowRunID(9007199254740993)
WorkflowRunID: &runID
```

Assert the decoded pointer contains that exact value. Retain the request path,
HTTP method, pipeline execution reference, and any still-valid response-field
coverage. This is a wire characterization test: the production client already
decodes the shared response type, so it may be green before the Fly behavior
changes.

### `fly/integration/agent_tickets_test.go`

Convert every dispatch fixture to a v3 durable result and add/update specs for:

- list prints the large durable ID in the `workflow run` column;
- a pipeline-only list fixture leaves the workflow-run cell empty, and its
  show fixture prints only the pipeline diagnostic with no durable identity,
  inspector command, or watch hint;
- dispatch and create-with-dispatch print durable identity first, label the
  pipeline execution reference separately, preserve warnings, and never print
  the pipeline name;
- malformed successful dispatch responses with no `workflow_run_id` fail
  through both direct dispatch and create-with-dispatch: direct dispatch
  emits no success line, create retains its created/queued partial-success
  context, neither substitutes a pipeline identity, and neither sends a
  compensating mutation;
- show prints the corrected `show-run` inspection command and separate
  pipeline diagnostic;
- watch performs one initial target validation, then gets the ticket and uses
  only workflow-run detail endpoints—no second `/api/v1/info`;
- watch follows at least one non-terminal-to-terminal transition;
- missing `workflow_run_id`, blank workflow name, and missing ticket fail
  clearly without a builds/events request;
- ticket and watch help contains the new wording and omits
  `--timestamps`;
- captured stdout/stderr contains no `agent-ticket-`.

Remove the now-unused `atc` import and the SSE/build-event fixture.

### Prove RED

Run:

```bash
go test ./fly/commands -run '^TestAgentTickets' -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='fly agent tickets' \
  ./fly/integration
```

Expected: RED because the command still derives a ticket pipeline, scans
builds, lacks the unit-test seam, exposes `--timestamps`, and prints the old
pipeline/build wording. Record the selected integration spec count. The
client characterization may already pass and is not the RED proof.

## Implementation order

1. Add the pure display/delegation tests and integration fixtures.
2. Replace list, dispatch, and show output with durable identity.
3. Factor `WorkflowsShowRunCommand` into pure preparation,
   loader-injected public orchestration, and unexported prepared-target
   execution while preserving validation-before-target ordering and every
   public behavior.
4. Replace watch's build discovery/event streaming with ticket lookup and
   target-aware `WorkflowsShowRunCommand{Follow: true}` delegation.
5. Delete the obsolete imports, helper, timestamp option, and comments.
6. Update generated-help integration assertions.
7. Run focused tests, structural audits, then the full Fly integration suite.

Do not duplicate `agentWorkflowRunPath`, polling, status validation, output
rendering, or target HTTP logic from `agent_workflow_runs.go`. Do not call the
public show-run `Execute` from ticket watch.

## Focused and full verification

```bash
go test ./fly/commands -run '^TestAgentTickets' -count=1

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='Agent Tickets' \
  ./go-concourse/concourse

go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty \
  --focus='fly agent tickets' \
  ./fly/integration

make test-fly-integration
```

Expected: all pass. Report the selected spec counts for both focused Ginkgo
runs so success cannot be empty.

## Non-vacuous v3-only prerequisite audit

Task 6 does not itself remove schema-1/2 parsers; Task 2B does. Before relying
on that state, prove both that rejection coverage exists and that active
compatibility symbols/branches are absent:

```bash
test -s agent/workflow/parse.go
test -s agent/workflow/compile.go
test -s fly/commands/agent_workflows.go
test -s fly/commands/agent_workflows_test.go
test -s fly/integration/agent_workflows_test.go

rg -n \
  'RequireSchemaVersion3|UnsupportedSchemaVersionError|only schema_version 3 is supported' \
  agent/workflow \
  fly/commands/agent_workflows_test.go \
  fly/integration/agent_workflows_test.go

rg -n \
  'schema_version:[[:space:]]*(1|2)|unsupported schema_version' \
  agent/workflow/*_test.go \
  fly/commands/agent_workflows_test.go \
  fly/integration/agent_workflows_test.go

! rg -n \
  'workflow\.Config|Legacy[[:space:]]+\*Config|compiled\.Legacy|definition\.Legacy|Compiled\.Legacy|Legacy:' \
  agent/workflow \
  atc/db/agent_workflows_factory.go \
  fly/commands/agent_workflows.go

! rg -n \
  'func Parse\(|func Compile\(|compileLegacy' \
  agent/workflow

! rg -n \
  'SchemaVersion[[:space:]]*==[[:space:]]*[12]|case[[:space:]]+[12]:' \
  agent/workflow/definition.go \
  agent/workflow/parse.go \
  agent/workflow/compile.go \
  agent/workflow/memory_store.go \
  fly/commands/agent_workflows.go
```

The positive scans make the absence claims non-vacuous: the v3 admission
boundary and explicit legacy-rejection fixtures must exist while active
schema-1/2 runtime arms do not. Historical migration-local decoder files are
deliberately outside this scan and must remain untouched.

## Durable-navigation structural audit

```bash
test -s fly/commands/agent_tickets.go
test -s fly/commands/agent_tickets_test.go
test -s fly/commands/agent_workflow_runs.go
test -s fly/integration/agent_tickets_test.go
test -s go-concourse/concourse/agent_tickets_test.go

rg -n \
  'WorkflowRunID|WorkflowName|WorkflowsShowRunCommand|prepare\(|executeWithTargetLoader|executePreparedWithTarget|show-run' \
  fly/commands/agent_tickets.go \
  fly/commands/agent_tickets_test.go \
  fly/commands/agent_workflow_runs.go \
  fly/integration/agent_tickets_test.go \
  go-concourse/concourse/agent_tickets_test.go

! rg -n \
  'ticketPipelineName|agent-ticket-|DefaultTeamName|\.Builds\(|BuildEvents\(|eventstream|long:"timestamps"' \
  fly/commands/agent_tickets.go

git diff --check
```

Pipeline wording is not globally forbidden because `pipeline run` is the
allowed diagnostic label. The forbidden behavior is ticket-pipeline
derivation/discovery or treating that reference as invocation identity.

## Clean-scope and commit

Before staging, inspect:

```bash
git status --short
git diff --name-only
git diff --check
```

The only tracked paths in the Task 6 diff must be:

```text
fly/commands/agent_tickets.go
fly/commands/agent_tickets_test.go
fly/commands/agent_workflow_runs.go
fly/integration/agent_tickets_test.go
go-concourse/concourse/agent_tickets_test.go
```

Do not revert or absorb unrelated shared-worktree changes. Stage only the
owned paths:

```bash
git add \
  fly/commands/agent_tickets.go \
  fly/commands/agent_tickets_test.go \
  fly/commands/agent_workflow_runs.go \
  fly/integration/agent_tickets_test.go \
  go-concourse/concourse/agent_tickets_test.go

git diff --cached --name-only
git diff --cached --check
git commit -m "feat(fly): follow ticket workflow runs by durable id"
```

Write `.superpowers/sdd/v3-cutover-task-6-report.md` with the approved Task 2B
base SHA, RED/GREEN evidence, selected spec counts, exact help/output examples,
single-target-validation HTTP request proof, pipeline-only identity proof,
both malformed-dispatch/partial-success proofs, v3-only prerequisite scans,
durable-navigation scans, full Fly integration result, clean-scope result,
commit SHA, and concerns.

---

# Schema-v3 cutover Task 7: durable workflow-run ticket UI

**Status:** COMPLETE and independently approved at
`8161366953573b081b478c45a9d37f45506965b9`. Canonical evidence is
`.superpowers/sdd/v3-cutover-task-7-approval-manifest.sh`, binding the report
and PASS review in the ledger. The immutable cumulative binary diff has
SHA-256
`f87db6cf5c9bbed6064ba96ba0b5ced008fd19e9f415e9478fbdd9e21d473a4a`.
The exact nine-path range recorded a 54/66 RED and 66/66 GREEN, then
3,239/3,239 full Elm tests. Pair-key and summary gates, three lossless durable
IDs, cost-only Build behavior, Dashboard filter removal, optimized asset
reproduction (`0e9c93036b98c54080775a914fa3798a388f0c64b2b761cf045e2a2ecb4e7655`),
immutable-dependency scans, and exact staging passed.

> **For the implementation worker:** Use test-driven development. Add the
> decoder, page, Build, and Dashboard assertions before changing production
> Elm, record the RED output, make the smallest source changes described
> here, regenerate the tracked Elm asset, and commit only the exact owned
> paths.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Dependency:** Start only after the corrected Task 6 implementation is
committed and independently approved. Record its exact SHA as
`TASK6_BASE_SHA`, require it to be the implementation base, and require a
clean tracked worktree before adding RED tests. The approved Task 6 ancestry
must already contain Tasks 1, 2A, 5, 3, 4, and 2B in the normative order.

**Source plan:** Global constraints, normative execution amendment, and
Task 7 in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`.

**Goal:** Make the ticket page a projection shell over durable workflow-run
and snapshot identity. A ticket links its exact `workflow_run_id`, captured
snapshot IDs, and workflow outputs; it never infers an invocation or ticket
relationship from a pipeline/build name. Build and Dashboard must likewise
stop interpreting `agent-ticket-<id>` as ownership.

Task 6 establishes the user-facing contract that `workflow_run_id` is the
ticket invocation identity and `pipeline_run_id` is only an execution
diagnostic. Task 7 mirrors that contract in Elm. It does not change Fly, the
ticket/workflow-run HTTP APIs, routes, effect constructors, or server JSON.

## Exact owned paths

Task 7 owns exactly nine tracked paths:

1. modify `web/elm/src/AgentTickets/AgentTicket.elm`
2. modify `web/elm/src/Concourse/AgentTicket.elm`
3. modify `web/elm/src/Build/Build.elm`
4. modify `web/elm/src/Dashboard/Filter.elm`
5. modify `web/elm/tests/AgentTicketPageTests.elm`
6. modify `web/elm/tests/AgentTicketTests.elm`
7. modify `web/elm/tests/BuildTicketBarTests.elm`
8. modify `web/elm/tests/DashboardAgentFilterTests.elm`
9. regenerate `web/public/elm.min.js`

Do not modify:

- `web/elm/tests/WorkflowRunDecoderTests.elm`;
- `web/elm/src/Concourse/WorkflowRun.elm`;
- `web/elm/src/Concourse/Snapshot.elm`;
- `web/elm/src/Routes.elm` or `web/elm/tests/RoutesTests.elm`;
- `web/elm/src/Message/{Effects,Callback}.elm`;
- `web/elm/src/Api/Endpoints.elm`;
- `web/elm/tests/AgenticData.elm`;
- `web/elm/src/Dashboard/Dashboard.elm` or
  `web/elm/tests/DashboardAgentStripTests.elm`;
- server, Fly, go-concourse, migration, workflow, or dispatch code;
- `web/public/bundle.js`, chunk bundles, CSS, package manifests, or lockfiles.

No generated Elm interface exists for these record/view changes. The only
generated tracked dependency is `web/public/elm.min.js`.

## Existing interfaces to consume unchanged

The Task 6 base must retain these existing interfaces:

```elm
-- Concourse.AgentTicket.Ticket
workflowName : String
workflowRunId : Maybe String
workItemSnapshotId : Maybe String
repositorySnapshotId : Maybe String
pipelineRunId : Maybe Int

-- Message.Effects
FetchAgentWorkflowRun String String

-- Message.Callback
AgentWorkflowRunFetched String (Fetched Concourse.WorkflowRun.Detail)

-- Routes
AgentWorkflowRun { workflowName : String, id : String }
AgentSnapshot { id : String }
```

`Concourse.Snapshot.decodeId` is the canonical durable-ID decoder. It accepts
only a quoted canonical positive decimal and preserves it as `String`, so IDs
above JavaScript's exact integer range never pass through `Int` or `Float`.
`Snapshot.decodeOptionalIdField` maps an absent or JSON-null optional field to
`Nothing`; any present non-null value must satisfy `decodeId`.

The existing route/effect stack already preserves the decimal string:

```text
Ticket.workflowRunId
  -> FetchAgentWorkflowRun workflowName runId
  -> /api/v1/agent/workflows/<workflow>/runs/<runId>
  -> AgentWorkflowRunFetched runId
  -> Routes.AgentWorkflowRun { workflowName, id = runId }
```

Do not add another route, convert an ID with `String.toInt`, or navigate
through `pipelineRunId`, `plannedBuildId`, a metric `buildId`, or a pipeline
name.

## Exact UI contract

### Ticket decoder

For `workflow_run_id`, `work_item_snapshot_id`, and
`repository_snapshot_id`:

- absent or null means `Nothing`;
- a present value must be a quoted canonical positive decimal;
- `"9007199254740993"` and larger valid signed-64-bit decimal values remain
  byte-for-byte exact Elm strings;
- a numeric JSON value, zero, a leading-zero string, sign, decimal point, or
  non-digit is rejected rather than silently becoming `Nothing`.

Use `Snapshot.decodeOptionalIdField` directly for all three fields and delete
the duplicate private `optionalDurableId`.

Keep `pipeline_run_id : Maybe Int` as a decoded execution reference. Do not
promote it to ticket identity and do not remove unrelated ticket fields or
the dispatch-response decoder.

### Ticket page

`durableEvidenceLine` remains the single durable identity/projection line.
For a ticket with:

```text
workflow_name             = develop
workflow_run_id           = 9007199254740993
repository_snapshot_id    = 9007199254740995
work_item_snapshot_id     = 9007199254741003
```

it must render:

- exactly one workflow-run link to
  `/agent/workflows/develop/runs/9007199254740993`;
- the text `workflow run #9007199254740993`;
- one captured-repository link to
  `/agent/snapshots/9007199254740995`;
- one captured-ticket-revision link to
  `/agent/snapshots/9007199254741003`; and
- after the exact workflow-run detail arrives, one link for each output
  snapshot using the output port/type and exact snapshot ID.

Derive one usable durable key as
`Maybe ( workflowName, workflowRunId )`: the name must be nonblank and the ID
must be present. Use this pair—not the ID alone—for cache retention, fetches,
callback acceptance, and rendering.

On each `AgentTicketFetched (Ok fresh)`:

- if the `(workflowName, workflowRunId)` pair equals the current usable key,
  keep a reference-stable matching `durableRun` and issue the existing
  refresh fetch;
- if either member changes, clear the old `durableRun` before fetching the
  new exact pair;
- if the ID disappears or the workflow name is blank, clear `durableRun` and
  issue no workflow-run fetch; and
- accept `AgentWorkflowRunFetched callbackId (Ok detail)` only when the
  current ticket key, `callbackId`, `detail.summary.id`, and
  `detail.summary.workflowName` all agree. A late, misrouted, or
  name-mismatched response must not repopulate the model.

A ticket without a usable durable ID renders no workflow-run link, no cached
output link, no ticket run/build row, and no pipeline/build fallback.

Delete:

- `runMetricsByBuild` from the model and initialization;
- grouping work from the metrics callback;
- the `Html.Lazy.lazy runHistory ...` view entry;
- `runHistory`;
- `runRow`;
- `groupMetricsByBuild`; and
- every ticket-page `Routes.OneOffBuild` reference and
  `agent-ticket-run-row` node.

Continue fetching ticket metrics. They still power spend/budget text, the
review digest, latest-build review loading, and the reusable review card.
Do not remove those diagnostics or the existing ticket polling/edit/state
behavior.

### Build

An ordinary pipeline is ordinary even if its literal name is
`agent-ticket-12`.

Delete `agentTicketId` and all pipeline-name parsing. Replace
`ticketContextBar` with this exact cost-only boundary:

```elm
agentCostBar : List Concourse.Agent.RunMetric -> Html Message
```

Call it with `model.agentRunMetrics`; do not pass the Build `job` or
`createdBy`. When the list has positive total cost, it must render:

```text
build-agent-cost-bar
  └── build-agent-cost: agent spend $X.XX · N run(s)
```

when metrics exist. It must never render:

- `build-ticket-context`;
- a `Routes.AgentTicket` link;
- `agent ticket #<id>`;
- ticket attribution derived from `Build.job.pipelineName`;
- pipeline instance variables as a ticket run label; or
- `createdBy` as a ticket dispatcher attribution.

Preserve `FetchBuildAgentMetrics`, cost aggregation/formatting, late-response
guards, history switching, live-build refresh behavior, the normal Build
route, and all non-ticket Build content.

### Dashboard

Delete:

- `AgentOwned` from `TeamFilter`;
- the `runFilter` branch for it;
- `agentFilter`;
- its `teamFilter` parser branch;
- the `suggestions` branch; and
- `isAgentPipeline`.

After removal:

- no filter query infers ownership from `agent-ticket-<id>`;
- a literal `agent-ticket-12` pipeline remains visible normally and remains
  searchable as an ordinary pipeline name;
- `is:agent` has no special ownership meaning; and
- `-is:agent` cannot hide a pipeline merely because of that prefix.

Do not remove or rename the dashboard's actual ticket strip/chips, its ticket
fetches, or CSS classes. Those are backed by
`Concourse.AgentTicket.Ticket`, not pipeline-name derivation.

## RED-first test work

Write all test changes before production changes. The decoder and existing
route/workflow-run tests are prerequisite characterizations and may already
pass; the combined RED command must fail on the legacy page, Build, and
Dashboard behavior.

### `web/elm/tests/AgentTicketTests.elm`

Add one exact three-ID decode test:

```elm
test "keeps quoted ticket durable IDs above 2^53 exact" <|
    \_ ->
        Json.Decode.decodeString AT.decodeTicket
            """
            { "id": 12
            , "workflow_run_id": "9007199254740993"
            , "repository_snapshot_id": "9007199254740995"
            , "work_item_snapshot_id": "9007199254741003"
            }
            """
            |> Result.map
                (\ticket ->
                    ( ticket.workflowRunId
                    , ticket.repositorySnapshotId
                    , ticket.workItemSnapshotId
                    )
                )
            |> Expect.equal
                (Ok
                    ( Just "9007199254740993"
                    , Just "9007199254740995"
                    , Just "9007199254741003"
                    )
                )
```

Add table-style cases proving each of the three fields rejects a numeric
`9007199254740993`. Also prove absent/null optional IDs decode as `Nothing`
and a noncanonical quoted ID such as `"01"` fails.

Retain the dispatch-result decoder test but replace its legacy
`"agent-ticket-12"` pipeline fixture with a neutral v3 workflow/template
name. `pipeline_name` remains decoded only as a response diagnostic.

### `web/elm/tests/AgentTicketPageTests.elm`

Add/update tests that prove:

1. `AgentTicketFetched` for the large durable ID emits exactly
   `FetchAgentWorkflowRun "develop" "9007199254740993"`;
2. the durable evidence node contains exactly one exact workflow-run route,
   the two exact captured-snapshot routes, and the output projection route
   after a matching workflow-run callback;
3. step-level metrics do not create `agent-ticket-run-row` or
   `/builds/561978`, while budget/review metric behavior remains;
4. a ticket with no `workflow_run_id`, even after metrics arrive, has no
   workflow-run, output, build, or pipeline fallback;
5. the same `(workflowName, workflowRunId)` key preserves the matching cached
   detail while issuing its refresh fetch;
6. after run A detail is loaded, a refetch with no durable ID clears all run-A
   output links, issues no `FetchAgentWorkflowRun`, and rejects a late
   response;
7. a retained run ID with a newly blank workflow name clears immediately,
   issues no fetch, and rejects the late old-name response;
8. after run A detail is loaded, a refetch naming run B clears run-A outputs
   immediately and fetches only run B;
9. the same run ID under a different nonblank workflow name clears
   immediately, fetches only the new `(name, ID)` pair, and rejects the
   old-name response; and
10. a callback whose `detail.summary.id` or
    `detail.summary.workflowName` differs from the callback/current pair
    cannot populate the page.

Delete the two legacy assertions that expect build-linked run-history rows.
Keep all unrelated ticket edit, state, polling, budget, review, compare-link,
and timestamp tests.

Use `9007199254740993` and nearby values as strings in every durable identity
assertion. Do not use a small `Int` stand-in for a route/effect test.

### `web/elm/tests/BuildTicketBarTests.elm`

Keep the literal pipeline fixture named `agent-ticket-12`; it is the negative
proof, not a compatibility fixture. Replace the old back-link expectation
with assertions that this ordinary pipeline:

- has no `build-ticket-context`;
- has no `/agent-tickets/12` link or `agent ticket #12` text;
- still triggers `FetchBuildAgentMetrics`;
- renders `build-agent-cost-bar` and the exact summed cost after metrics; and
- behaves identically to another ordinary pipeline for empty metrics,
  build-history switching, late responses, and live-build completion.

The test module/file name may remain `BuildTicketBarTests`; filenames and CSS
labels are not pipeline derivation.

### `web/elm/tests/DashboardAgentFilterTests.elm`

Keep `agent-ticket-12` and `my-service` as ordinary pipeline fixtures. Replace
the old positive ownership expectations with:

- no query shows both;
- the ordinary name query `agent-ticket-12` finds that pipeline;
- `is:agent` does not classify or reveal `agent-ticket-12`; and
- `-is:agent` does not hide `agent-ticket-12` or `my-service` because no
  special ownership filter exists.

Do not touch `DashboardAgentStripTests.elm`; the API-backed ticket strip must
remain green in the full suite.

### Prove RED

Run from the repository root:

```bash
(cd web/elm && npx elm-test \
  tests/AgentTicketPageTests.elm \
  tests/AgentTicketTests.elm \
  tests/WorkflowRunDecoderTests.elm \
  tests/BuildTicketBarTests.elm \
  tests/DashboardAgentFilterTests.elm)
```

Expected: nonzero. Record the total selected/passed/failed counts and the
exact failing test names. The new decoder characterizations and existing
workflow-run decoder tests may pass; RED must come from at least the
ticket-build-row removal, Build prefix non-derivation, Dashboard prefix
non-classification, and stale durable-run clearing cases. If the command is
green before production changes, the new behavioral tests are vacuous or
the Task 6 base already contains Task 7 and execution must stop for review.

## Implementation order

1. Add all decoder, page/navigation, Build, and Dashboard tests.
2. Run the exact focused command and record RED evidence.
3. Replace the ticket's duplicate optional durable-ID helper with
   `Snapshot.decodeOptionalIdField`.
4. Make ticket refetches clear stale durable-run detail and retain only exact
   `(workflowName, workflowRunId)` fetch/callback behavior, including returned
   detail-summary verification.
5. Delete ticket metric-to-build row projection and its model/grouping state,
   preserving metric budget/review uses.
6. Reduce Build's ticket context to a metric-only cost bar and delete every
   pipeline-name/ticket-ID derivation.
7. Delete Dashboard's entire `AgentOwned` special filter path.
8. Run focused tests and formatting validation.
9. Run the complete Elm suite.
10. Regenerate and verify the tracked optimized Elm asset.
11. Run structural, immutable-dependency, and exact-scope audits.
12. Stage the nine exact paths and commit.

Do not combine Task 7 with UI redesign, route changes, ticket metric removal,
server changes, CSS cleanup, or generic Build/Dashboard filtering changes.

## Focused and full verification

Run the corrected focused files:

```bash
(cd web/elm && npx elm-test \
  tests/AgentTicketPageTests.elm \
  tests/AgentTicketTests.elm \
  tests/WorkflowRunDecoderTests.elm \
  tests/BuildTicketBarTests.elm \
  tests/DashboardAgentFilterTests.elm)
```

Expected: PASS. Report the selected and passed counts. Then run the complete
Elm suite:

```bash
(cd web/elm && npx elm-test)
```

Expected: PASS, including `RoutesTests`,
`DashboardAgentStripTests`, generic Build tests, snapshot decoders, workflow
run pages, and ticket queue pages.

Validate formatting without rewriting unrelated Elm:

```bash
npx elm-format --validate \
  web/elm/src/AgentTickets/AgentTicket.elm \
  web/elm/src/Concourse/AgentTicket.elm \
  web/elm/src/Build/Build.elm \
  web/elm/src/Dashboard/Filter.elm \
  web/elm/tests/AgentTicketPageTests.elm \
  web/elm/tests/AgentTicketTests.elm \
  web/elm/tests/BuildTicketBarTests.elm \
  web/elm/tests/DashboardAgentFilterTests.elm
```

Regenerate and compile the production Elm:

```bash
yarn build-elm
test -s web/public/elm.min.js
git check-ignore -q web/public/elm.js
```

Expected: the optimized Elm build succeeds, `web/public/elm.min.js` changes
with the source, and the transient `web/public/elm.js` remains ignored.
Do not run a broad formatter or stage the transient file.

## Non-vacuous durable-navigation and removal audit

Positive checks prove the retained durable route/effect/cost surfaces still
exist:

```bash
test -s web/elm/src/AgentTickets/AgentTicket.elm
test -s web/elm/src/Concourse/AgentTicket.elm
test -s web/elm/src/Build/Build.elm
test -s web/elm/src/Dashboard/Filter.elm
test -s web/public/elm.min.js

rg -n 'Snapshot\.decodeOptionalIdField "workflow_run_id"' \
  web/elm/src/Concourse/AgentTicket.elm
rg -n 'Snapshot\.decodeOptionalIdField "repository_snapshot_id"' \
  web/elm/src/Concourse/AgentTicket.elm
rg -n 'Snapshot\.decodeOptionalIdField "work_item_snapshot_id"' \
  web/elm/src/Concourse/AgentTicket.elm
! rg -n 'optionalDurableId' web/elm/src/Concourse/AgentTicket.elm

rg -n \
  'FetchAgentWorkflowRun|Routes.AgentWorkflowRun|Routes.AgentSnapshot|ticket-durable-evidence' \
  web/elm/src/AgentTickets/AgentTicket.elm \
  web/elm/tests/AgentTicketPageTests.elm

rg -n '"build-agent-cost-bar"' web/elm/src/Build/Build.elm
rg -n '"build-agent-cost"' web/elm/src/Build/Build.elm
rg -n 'FetchBuildAgentMetrics' web/elm/src/Build/Build.elm

rg -n 'agent-ticket-12' \
  web/elm/tests/BuildTicketBarTests.elm \
  web/elm/tests/DashboardAgentFilterTests.elm
```

Negative structural checks prove the active derivations are gone:

```bash
! rg -n \
  'Routes.OneOffBuild|agent-ticket-run-row|runMetricsByBuild|runHistory|groupMetricsByBuild' \
  web/elm/src/AgentTickets/AgentTicket.elm

! rg -n \
  'agentTicketId|ticketContextBar|build-ticket-context|String.startsWith "agent-ticket-"|String.dropLeft .*agent-ticket-' \
  web/elm/src/Build/Build.elm

! rg -n \
  'AgentOwned|agentFilter|isAgentPipeline|String.startsWith "agent-ticket-"|String.dropLeft .*agent-ticket-' \
  web/elm/src/Dashboard/Filter.elm

! rg -n 'agent-ticket-' \
  web/elm/src/Build/Build.elm \
  web/elm/src/Dashboard/Filter.elm
```

Do not scan all Elm for the literal `agent-ticket-`: ticket URLs, ticket-page
CSS classes, the API-backed dashboard strip, and the deliberate negative
fixtures in the two focused tests are valid. The forbidden structure is
pipeline-name parsing/classification and metric-to-one-off-build navigation.

## Immutable hidden-dependency and exact-scope audit

Verify the exact Task 6 base before relying on it:

```bash
test "$(git rev-parse HEAD)" = "$TASK6_BASE_SHA"
git merge-base --is-ancestor "$TASK6_BASE_SHA" HEAD
```

Run the first equality before adding RED changes. After implementation, use
the recorded SHA to prove the route/effect/decoder fixtures outside ownership
did not move:

```bash
git diff --exit-code "$TASK6_BASE_SHA" -- \
  web/elm/src/Concourse/WorkflowRun.elm \
  web/elm/src/Concourse/Snapshot.elm \
  web/elm/src/Routes.elm \
  web/elm/src/Message/Effects.elm \
  web/elm/src/Message/Callback.elm \
  web/elm/src/Api/Endpoints.elm \
  web/elm/tests/WorkflowRunDecoderTests.elm \
  web/elm/tests/RoutesTests.elm \
  web/elm/tests/AgenticData.elm \
  web/elm/src/Dashboard/Dashboard.elm \
  web/elm/tests/DashboardAgentStripTests.elm

git status --short
git diff --name-only "$TASK6_BASE_SHA"
git diff --check "$TASK6_BASE_SHA"
```

The complete tracked diff from `TASK6_BASE_SHA` must contain exactly the nine
owned paths. In particular, it must contain `web/public/elm.min.js` and must
not contain `web/public/elm.js`, `package.json`, a lockfile, route/effect
files, `WorkflowRunDecoderTests.elm`, or unrelated generated bundles.

Do not revert or absorb unrelated shared-worktree changes. If the base or
changed-path list is not exact, stop and report it.

## Exact staging, commit, and report

Stage only the nine owned paths:

```bash
git add \
  web/elm/src/AgentTickets/AgentTicket.elm \
  web/elm/src/Concourse/AgentTicket.elm \
  web/elm/src/Build/Build.elm \
  web/elm/src/Dashboard/Filter.elm \
  web/elm/tests/AgentTicketPageTests.elm \
  web/elm/tests/AgentTicketTests.elm \
  web/elm/tests/BuildTicketBarTests.elm \
  web/elm/tests/DashboardAgentFilterTests.elm \
  web/public/elm.min.js

git diff --cached --name-only
git diff --cached --check
```

Require the cached path list to equal those nine paths exactly. Commit:

```text
feat(web): link tickets to durable workflow runs
```

Write `.superpowers/sdd/v3-cutover-task-7-report.md` with:

- the approved `TASK6_BASE_SHA`;
- RED and GREEN selected/passed/failed counts and exact RED failures;
- lossless three-ID decoder and numeric-rejection evidence;
- exact workflow-run/snapshot/output hrefs;
- same-pair retention, no-ID, blank-name, changed-name, changed-ID,
  detail-summary mismatch, and late-callback stale-data proofs;
- proof that metric/build rows and `Routes.OneOffBuild` are absent from the
  ticket page while budget/review metrics remain;
- Build cost preservation plus prefix non-derivation;
- Dashboard special-filter deletion plus API-backed ticket-strip
  preservation;
- full Elm suite and optimized production build results;
- generated-asset, structural, immutable-dependency, clean-scope, and exact
  staging evidence;
- commit SHA and concerns.

---

### Schema-v3 cutover Task 8: final vertical-slice and repository audit

**Status:** COMPLETE from exact approved Task 7 base
`8161366953573b081b478c45a9d37f45506965b9`. Task 8 owns exactly the four
tracked paths named below and no production code. Its initial/final commit
identity is recorded after commit in the ignored Task 8 ledger and report;
`TASK8_INITIAL_SHA` is the direct child of Task 7 and `TASK8_FINAL_SHA`
initially equals it.

Final evidence: the exact Task 8 Go characterization and DB vertical slice
passed; all focused migration, database, workflow, Fly command, Go client,
and Fly integration selections were non-empty and passed. `make test-quick`
passed 126 root Ginkgo suites plus agent/schema, ci-agent, and dev-MCP;
`make test-fly-integration` passed 676/676; `make test-integration` passed
25/25; `yarn test` passed 3239/3239; `yarn build-elm` succeeded; and the
optimized `web/public/elm.min.js` remained unchanged. K8s was not run because
Task 8 owns no K8s path. All removal, v3-survival, documentation,
generated-dependency, and exact-scope audits passed.

The ignored implementation brief's Task 6 fixture scan remained stale: it
looked for the one-target witness only in the unit file and did not match
“sends no compensating mutation.” This tracked plan now scopes the first
scan to both unit and integration fixtures and uses `no compensat`, making
the standalone plan executable without modifying approved Task 6 code.

**Repository:** `/Users/tdmtrader/concourse/concourse/.worktrees/agentic-functions`

**Nature of this task:** verification, integration tests, active documentation,
and tracked-plan reconciliation only. Task 8 does not own a production-code
repair.

**Required execution order:**

```text
Task 1 -> Task 2A -> Task 5 -> Task 3 -> Task 4 -> Task 2B -> Task 6 -> Task 7 -> Task 8
```

Start only after every preceding task has a committed SHA and an independent
PASS review. The corrected, re-reviewed Task 7 SHA is the exact Task 8
implementation base.

**Source authority:** the global constraints, normative execution amendment,
and Task 8 in
`docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`, reconciled
with the final approved Task 1, 2A, 5, 3, 4, 2B, 6, and 7 briefs and all of
their reviews/rereviews. Task 2B is a first-class authority boundary; its
ownership, compiler/security order, historical-opacity rules, tests, scans,
and exact staging rules must not be summarized away.

## Outcome

Task 8 proves, from one clean reviewed commit range, that:

- schema version 3 is the only importable, promotable, dispatchable, and
  executable workflow format;
- historical schema-1/schema-2 rows remain readable only as opaque metadata
  and cannot become live or reach a runtime compiler;
- manual, ticket, retry, and experiment invocations all use the generic
  workflow-run binder;
- ticket dispatch binds exactly one captured `work-item/v1` and one exact
  `repository/v1` snapshot before recording the durable workflow-run ID;
- `workflow_run_id` remains the ticket invocation identity after build,
  pipeline-run, instance-pipeline, and template-pipeline rows are deleted;
- migration `1773106123` demotes legacy live rows, installs the live-v3
  constraint, drops only that constraint on rollback, and, on the same
  database row after rollback/reactivation, re-upgrades by demoting the legacy
  row again while keeping v3 live and reinstating enforcement;
- Fly and Elm use durable workflow-run/snapshot identity and never infer a
  ticket invocation from an `agent-ticket-<id>` pipeline name;
- the retained schema-v3 seeds, generic MCP/dev-MCP, snapshot,
  `await_snapshot`, publisher, manual/retry/experiment, and ordinary Concourse
  surfaces still pass their suites; and
- active operator documentation describes the resulting product rather than
  a removed compatibility path.

Task 8 is accepted only after its initial implementation commit, every
correction commit, immutable review packages, report, and final independent
PASS review all agree on the same final SHA and cumulative four-path range.

## Stop conditions and correction routing

Stop without committing Task 8 if any of these occurs:

1. A prerequisite SHA lacks an independent PASS review, including the current
   Task 1 release-fidelity correction and the Task 3 same-row re-upgrade
   correction.
2. The corrected Task 7 SHA is not the exact starting `HEAD`, the prerequisite
   order is not an ancestry chain, or the tracked worktree is dirty.
3. A focused or full regression exposes a product defect.
4. A production file must change to make a Task 8 test or scan pass.
5. Migration `1773106123` is not the sole added migration pair in the complete
   cutover range from fixed base
   `d13849b8d10953e7d1ec76174780155cb125dc0f`, or a Task 1 decoder path changed
   after its final approval.
6. A required negative scan fails after the preceding task that owns the
   symbol has supposedly passed review.
7. The Task 8 cumulative range contains a path outside the exact owned list.
8. Any selected-test proof is empty, any absence command returns status 2, or
   any review package path already exists.

Route a product failure back to the owning task, land and independently review
that correction, then recreate or rebase and independently re-review every
affected descendant boundary through Task 7. Record the new literal SHA chain
and restart Task 8 from that corrected Task 7 tip. In particular, Task 3 owns
the migration test that must continue through down, same-row legacy
reactivation, and re-upgrade; Task 8 must not edit it. Do not hide a defect by
weakening a test, excluding a package, widening a scan exception, adding a
compatibility alias, or folding production work into the audit commit.

## Deterministic final tracked-plan reconciliation

Before adding Task 8 tests, use `apply_patch` to make this tracked plan the
complete final execution record. It must not require a reader to consult an
ignored brief to learn an executable boundary. Preserve the normative order.

For every predecessor section, record all of the following from its immutable
approved evidence:

- the literal final implementation SHA, approval-manifest path, bound report,
  bound independent PASS review, reviewer identity, and artifact SHA-256
  values;
- every correction commit and immutable review-package filename/checksum;
- the exact owned path inventory and exact-path staging rule;
- the final positive semantics, ordering, error classification, preservation
  requirements, and negative boundaries;
- exact selected-test commands and non-empty selection/count evidence, full
  regressions, structural scans, clean-scope checks, and final observed
  results; and
- a clear completed/approved marker only after the report, manifest, and PASS
  review agree on that literal SHA.

The final Task 8 edit must also record its exact four-path ownership,
characterization tests, cross-boundary matrix, environment checks, audits,
pre-stage/cached/final comparisons, initial and correction commits, immutable
packages, report, and independent review. Record the complete ordered
Task-1/2A/5/3/4/2B/6/7 ledger table in both this plan and the Task 8 report.
Do not replace an executable task section with a pointer, silently copy
unreviewed evidence, or mark a pending implementation complete.

The reconciliation must retain these canonical desired-state lines exactly:

```text
Admission order: Manifest.Validate -> extract `workflow.yml` -> RequireSchemaVersion3.
- Delete: `agent/workflow/validate_test.go`
- Verification-only (read-only; do not modify): `web/elm/tests/WorkflowRunDecoderTests.elm`
```

It must also retain the Task 3 exact same-database/same-row spec, Task 4
`os.ReadDir("seeds")` inventory, Task 6 `executePreparedWithTarget` helper,
Task 7 `Maybe ( workflowName, workflowRunId )` identity and summary-name/ID
gates, generated `web/public/elm.min.js`, and the distinct
`TASK8_INITIAL_SHA`/`TASK8_FINAL_SHA` semantics. Run the plan's complete
desired-state, stale-directive, task-order, owned/staged-count, Bash-fence,
and whitespace checks after reconciliation and record the results.

## Approved SHA ledger and immutable approval manifests

Create
`.superpowers/sdd/v3-cutover-task-8-approved-shas.sh` with `apply_patch`
before running the prerequisite gate. This ignored evidence file is not a
tracked Task 8 path. Fill every assignment with a literal value—never a shell
substitution, abbreviated SHA, branch name, or commit subject:

```text
CUTOVER_BASE_SHA='d13849b8d10953e7d1ec76174780155cb125dc0f'
TASK1_SHA='<literal 40-character independently approved SHA>'
TASK2A_SHA='<literal 40-character independently approved SHA>'
TASK5_SHA='<literal 40-character independently approved SHA>'
TASK3_SHA='<literal 40-character independently approved SHA including same-row correction>'
TASK4_SHA='<literal 40-character independently approved descendant SHA>'
TASK2B_SHA='<literal 40-character independently approved descendant SHA>'
TASK6_SHA='<literal 40-character independently approved descendant SHA>'
TASK7_SHA='<literal 40-character independently approved descendant SHA>'
TASK1_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK2A_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK5_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK3_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK4_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK2B_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK6_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
TASK7_APPROVAL_MANIFEST='<literal relative independent approval-manifest path>'
```

The angle-bracket forms above describe the tracked contract only; none may
remain in the actual ledger. Source and validate the ledger afresh in every
implementation, package, correction, and review gate so no command depends on
shell state from an earlier invocation.

Each approval manifest is a new immutable ignored artifact written by an
independent prerequisite reviewer after reading the final implementation or
correction report and the final PASS review. It contains exactly these
canonical single-quoted fields:

```text
BOUNDARY='Task 1'
REPORT_PATH='.superpowers/sdd/<exact final implementation or correction report>'
REPORT_SHA256='<exact lowercase 64-character SHA-256>'
FINAL_SHA='<exact 40-character implementation head>'
REVIEW_PATH='.superpowers/sdd/<exact independent PASS review or rereview>'
REVIEW_SHA256='<exact lowercase 64-character SHA-256>'
REVIEWED_SHA='<exact 40-character reviewed head>'
VERDICT='PASS'
REVIEWER='<independent reviewer identity>'
```

Use the matching boundary label for each manifest.
`FINAL_SHA == REVIEWED_SHA` and both must equal that boundary's ledger SHA.
`REPORT_SHA256` and `REVIEW_SHA256` bind the canonical fields to the exact
existing artifacts, so a base SHA mentioned inside a report or review cannot
be mistaken for the approved head. `REVIEWER` must identify a reviewer
independent of the implementation.

Never rewrite an existing report, review, approval manifest, or immutable
package to change its identity. If evidence is superseded, create a new
round-specific artifact and a new manifest path. Task 1's existing report and
approved review remain byte-for-byte unchanged: a different independent
reviewer creates the Task 1 approval manifest by binding their exact paths,
checksums, and approved head. No boundary is accepted solely because its
branch contains a commit or a report says PASS; the checksum-bound independent
review and manifest gate below must agree.

## Prerequisite review and ancestry gate

Read every final brief, implementation report, PASS review/rereview, and
correction report completely. Confirm each report's implementation SHA equals
the corresponding ledger SHA. A current FAIL review is a hard stop even when
the branch happens to contain its commit.

Run this self-contained gate before any edit:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"

require_sha() {
  local name="$1"
  local value="$2"
  test -n "$name"
  printf '%s\n' "$value" | rg -x '[0-9a-f]{40}' >/dev/null
  git cat-file -e "${value}^{commit}"
}

manifest_field() {
  local manifest="$1"
  local field="$2"
  local value
  value="$(
    sed -n "s/^${field}='\\([^']*\\)'$/\\1/p" "$manifest"
  )"
  test -n "$value"
  test "$(printf '%s\n' "$value" | wc -l | tr -d '[:space:]')" = '1'
  printf '%s\n' "$value"
}

require_approval_manifest() {
  local manifest="$1"
  local boundary="$2"
  local approved_sha="$3"
  local report_path
  local review_path
  local report_sha256
  local review_sha256
  local reviewer
  local actual_sha256
  local artifact_path
  local artifact_sha256

  printf '%s\n' "$manifest" |
    rg -x '\.superpowers/sdd/[A-Za-z0-9._/-]+' >/dev/null
  case "/$manifest/" in
    */../*) return 1 ;;
  esac
  test -s "$manifest"
  test "$(manifest_field "$manifest" BOUNDARY)" = "$boundary"
  test "$(manifest_field "$manifest" FINAL_SHA)" = "$approved_sha"
  test "$(manifest_field "$manifest" REVIEWED_SHA)" = "$approved_sha"
  test "$(manifest_field "$manifest" VERDICT)" = 'PASS'

  report_path="$(manifest_field "$manifest" REPORT_PATH)"
  review_path="$(manifest_field "$manifest" REVIEW_PATH)"
  report_sha256="$(manifest_field "$manifest" REPORT_SHA256)"
  review_sha256="$(manifest_field "$manifest" REVIEW_SHA256)"
  reviewer="$(manifest_field "$manifest" REVIEWER)"
  test -n "$reviewer"

  for artifact_path in "$report_path" "$review_path"
  do
    printf '%s\n' "$artifact_path" |
      rg -x '\.superpowers/sdd/[A-Za-z0-9._/-]+' >/dev/null
  done
  case "/$report_path/" in
    */../*) return 1 ;;
  esac
  case "/$review_path/" in
    */../*) return 1 ;;
  esac
  for artifact_sha256 in "$report_sha256" "$review_sha256"
  do
    printf '%s\n' "$artifact_sha256" |
      rg -x '[0-9a-f]{64}' >/dev/null
  done

  test -s "$report_path"
  actual_sha256="$(shasum -a 256 "$report_path" | awk '{print $1}')"
  test "$actual_sha256" = "$report_sha256"

  test -s "$review_path"
  actual_sha256="$(shasum -a 256 "$review_path" | awk '{print $1}')"
  test "$actual_sha256" = "$review_sha256"
  rg -n \
    '^(## Verdict:[[:space:]]*PASS|\*\*PASS([[:space:]]|—|-))' \
    "$review_path" >/dev/null
}

require_sha CUTOVER_BASE_SHA "$CUTOVER_BASE_SHA"
require_sha TASK1_SHA "$TASK1_SHA"
require_sha TASK2A_SHA "$TASK2A_SHA"
require_sha TASK5_SHA "$TASK5_SHA"
require_sha TASK3_SHA "$TASK3_SHA"
require_sha TASK4_SHA "$TASK4_SHA"
require_sha TASK2B_SHA "$TASK2B_SHA"
require_sha TASK6_SHA "$TASK6_SHA"
require_sha TASK7_SHA "$TASK7_SHA"
test "$CUTOVER_BASE_SHA" = 'd13849b8d10953e7d1ec76174780155cb125dc0f'

require_approval_manifest "$TASK1_APPROVAL_MANIFEST" 'Task 1' "$TASK1_SHA"
require_approval_manifest "$TASK2A_APPROVAL_MANIFEST" 'Task 2A' "$TASK2A_SHA"
require_approval_manifest "$TASK5_APPROVAL_MANIFEST" 'Task 5' "$TASK5_SHA"
require_approval_manifest "$TASK3_APPROVAL_MANIFEST" 'Task 3' "$TASK3_SHA"
require_approval_manifest "$TASK4_APPROVAL_MANIFEST" 'Task 4' "$TASK4_SHA"
require_approval_manifest "$TASK2B_APPROVAL_MANIFEST" 'Task 2B' "$TASK2B_SHA"
require_approval_manifest "$TASK6_APPROVAL_MANIFEST" 'Task 6' "$TASK6_SHA"
require_approval_manifest "$TASK7_APPROVAL_MANIFEST" 'Task 7' "$TASK7_SHA"

git merge-base --is-ancestor "$CUTOVER_BASE_SHA" "$TASK1_SHA"
git merge-base --is-ancestor "$TASK1_SHA" "$TASK2A_SHA"
git merge-base --is-ancestor "$TASK2A_SHA" "$TASK5_SHA"
git merge-base --is-ancestor "$TASK5_SHA" "$TASK3_SHA"
git merge-base --is-ancestor "$TASK3_SHA" "$TASK4_SHA"
git merge-base --is-ancestor "$TASK4_SHA" "$TASK2B_SHA"
git merge-base --is-ancestor "$TASK2B_SHA" "$TASK6_SHA"
git merge-base --is-ancestor "$TASK6_SHA" "$TASK7_SHA"

test "$(git rev-parse HEAD)" = "$TASK7_SHA"
test -z "$(git status --porcelain=v1 --untracked-files=all)"
git diff --exit-code "$TASK7_SHA"
git diff --cached --exit-code
```

Record the complete ordered ledger table in the Task 8 report:

```text
boundary | exact approved SHA | approval manifest | bound report | bound PASS review | reviewer
Task 1
Task 2A
Task 5
Task 3
Task 4
Task 2B
Task 6
Task 7
```

## Fixed-base migration and immutable Task 1 gates

Prove that the only migration files added anywhere in the complete cutover
range are the exact `1773106123` up/down pair. Comparing only filenames that
already contain `1773106123` is vacuous.

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"
for sha in "$CUTOVER_BASE_SHA" "$TASK7_SHA"
do
  printf '%s\n' "$sha" | rg -x '[0-9a-f]{40}' >/dev/null
  git cat-file -e "${sha}^{commit}"
done
git merge-base --is-ancestor "$CUTOVER_BASE_SHA" "$TASK7_SHA"

expected=$'atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql\natc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.up.sql'
actual="$(
  git diff --diff-filter=A --name-only \
    "$CUTOVER_BASE_SHA" "$TASK7_SHA" -- atc/db/migration/migrations |
    LC_ALL=C sort
)"
test "$actual" = "$expected"
```

Prove that Task 1's exact four paths are unchanged after its final approved
SHA:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"
for sha in "$TASK1_SHA" "$TASK7_SHA"
do
  printf '%s\n' "$sha" | rg -x '[0-9a-f]{40}' >/dev/null
  git cat-file -e "${sha}^{commit}"
done
git merge-base --is-ancestor "$TASK1_SHA" "$TASK7_SHA"

git diff --exit-code "$TASK1_SHA" "$TASK7_SHA" -- \
  atc/db/migration/legacyworkflow/decoder.go \
  atc/db/migration/legacyworkflow/decoder_test.go \
  atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go \
  atc/db/migration/add_workflow_schema_signature_test.go
```

Prove the corrected same-row re-upgrade spec is present before Task 8 edits:

```bash
set -euo pipefail
spec='atc/db/migration/v3_only_workflows_test.go'
test -s "$spec"
rg -n -F 'It("demotes a reactivated legacy row again on same-database re-upgrade"' "$spec"
rg -n -F 'migrator.Migrate' "$spec"
```

Record independent source lines for calls targeting `1773106122` and
`1773106123` inside that exact spec; do not weaken the behavioral spec-name
requirement.

## Exact Task 8 ownership

Task 8 owns exactly four tracked paths:

1. modify `agent/workflowrun/e2e_test.go`;
2. modify `atc/db/agent_workflow_run_integration_test.go`;
3. modify `docs/agentic/README.md`;
4. modify
   `docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md`.

Task 8 owns no production Go, SQL, Fly, Elm source, generated asset, seed, or
migration test. Ignored ledger/report/review-package artifacts are evidence,
not tracked ownership.

## Task 8 characterization: compiler and execution boundary

Modify `agent/workflowrun/e2e_test.go` with one exact new plain-Go test:

```go
func TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution(t *testing.T)
```

Keep
`TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance`
unchanged except for shared test helpers that remain in this same test file.
Together the two exact tests must prove:

1. a valid schema-v3 manifest passes `CompileDefinition`, renders a generic
   function, materializes exact typed inputs/outputs, and captures frozen
   provenance;
2. schema-1 and schema-2 manifests are each rejected with the exact typed
   unsupported-schema boundary before parse/typecheck/render/planning;
3. a persisted non-v3 `workflow.Definition` is rejected by the execution
   boundary before rendering, snapshot lookup, build allocation, or pipeline
   mutation;
4. the rejection assertions are distinct for schema 1, schema 2, and the
   persisted-definition path—one table row or error cannot satisfy all three;
5. the existing IDs above JavaScript's exact-integer range, exact snapshot
   loads, output declaration identity, canonical plan, dependency capture,
   and frozen-provenance round trip still pass.

Do not mock away `CompileDefinition`, `FullFunctionTarget`, rendering,
materialization, planner creation, or provenance capture. A product failure is
routed to Task 2A, 2B, or 5 as appropriate.

Witness the two declarations separately and select each exact test
non-vacuously:

```bash
set -euo pipefail
file='agent/workflowrun/e2e_test.go'
test -s "$file"
rg -n -x 'func TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution\(t \*testing\.T\) \{' "$file"
rg -n -x 'func TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance\(t \*testing\.T\) \{' "$file"

listed_new="$(go test ./agent/workflowrun -list '^TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution$')"
printf '%s\n' "$listed_new" | rg -x 'TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution'
new_output="$(go test ./agent/workflowrun -run '^TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution$' -count=1 -v)"
printf '%s\n' "$new_output"
printf '%s\n' "$new_output" | rg -x '=== RUN   TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution'
printf '%s\n' "$new_output" | rg -x -- '--- PASS: TestV3OnlyWorkflowCutoverRejectsLegacyBeforeExecution \([0-9.]+s\)'

listed_existing="$(go test ./agent/workflowrun -list '^TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance$')"
printf '%s\n' "$listed_existing" | rg -x 'TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance'
existing_output="$(go test ./agent/workflowrun -run '^TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance$' -count=1 -v)"
printf '%s\n' "$existing_output"
printf '%s\n' "$existing_output" | rg -x '=== RUN   TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance'
printf '%s\n' "$existing_output" | rg -x -- '--- PASS: TestCompiledFunctionMaterializesAndFreezesTypedExecutionProvenance \([0-9.]+s\)'
```

Record the exact `=== RUN` and PASS lines for each test independently.

## Task 8 PostgreSQL vertical slice

Extend
`atc/db/agent_workflow_run_integration_test.go` without adding production
helpers. Keep the existing exact focused spec:

```text
imports, binds, executes, seals, reconciles, and preserves exact history after execution deletion
```

Its final assertions must traverse real factories, dispatcher/binder
composition, and persisted rows to prove one coherent vertical slice:

1. import and promote a schema-v3 workflow;
2. create a ticket and dispatch it through the generic workflow-run binder;
3. capture exactly one `work-item/v1` snapshot and exactly one
   `repository/v1` snapshot, with their exact bound snapshot IDs and immutable
   payload identity;
4. persist the exact workflow definition/version, ticket ID, workflow-run ID,
   build identity, canonical concrete config, and sealed provenance;
5. reconcile execution while preserving the durable ticket/run link;
6. delete the associated build, pipeline-run, instance-pipeline, and
   template-pipeline rows using the real deletion paths already available to
   the suite;
7. read the ticket and workflow-run again and prove their exact IDs,
   workflow summary, snapshots, outputs, and history still match;
8. prove no deleted execution-row or `agent-ticket-<id>` pipeline-name
   inference is needed to recover that identity.

Add or retain independent assertions for manual, retry, and experiment
binding so that each uses the same generic workflow-run binder while
preserving its own invocation metadata. Do not duplicate predecessor state
machines merely to inflate the final test.

Select the exact Ginkgo spec with a dry-run witness and a real run:

```bash
set -euo pipefail
pg_isready
focus='imports, binds, executes, seals, reconciles, and preserves exact history after execution deletion'

go run github.com/onsi/ginkgo/v2/ginkgo \
  --dry-run --fail-on-empty --focus="$focus" ./atc/db
go run github.com/onsi/ginkgo/v2/ginkgo \
  --fail-on-empty --focus="$focus" ./atc/db
```

Record the dry-run selected count and the run's selected, passed, failed, and
pending/skipped counts. A zero selection or focus collision is a blocker.

## Cross-boundary verification matrix

The tracked plan and Task 8 report must map each row to an exact test or
independent scan:

| Boundary | Accepted proof | Rejection/retention proof |
|---|---|---|
| Import/admission | schema-v3 manifest validates and compiles | schema 1 and 2 each fail before parser/compiler/mutation |
| Promotion | v3 can become live | legacy promotion does not load source, invoke validator, or mutate |
| Storage history | exact opaque metadata via both `Get` and `Latest` | corrupt v3 fails closed; legacy has nil source and zero compiled/config |
| Binding | manual, ticket, retry, experiment use generic binder | persisted non-v3 definition fails before side effects |
| Ticket dispatch | exactly one work-item and one repository snapshot | pipeline-only response invents no run; malformed 201 preserves ticket/no compensation |
| Durable identity | ticket/run survives all subordinate execution deletion | no pipeline-prefix reconstruction |
| Migration | first up demotes legacy/keeps v3/enforces constraint | same-row down/reactivate/re-up demotes again and reinstalls rejection |
| Fly | `show-run`, watch reuse, exact client wire behavior | one validation, no second info lookup, no archive/export claim |
| Elm | exact ID/name pair and returned summary gate state | no-ID/name/ID/detail mismatch and late callback clear stale state |
| UI build/dashboard | exact cost-only row and API ticket strip survive | ticket metric/build rows and special agent filter are absent |

The matrix is a routing aid, not permission for Task 8 to change predecessor
production code.

## Active documentation update

Update `docs/agentic/README.md` so it says, positively and without a
compatibility promise:

1. only schema-v3 workflow manifests can be imported, promoted, or executed;
2. historical schema-1/schema-2 records may still be listed/read as inert
   metadata, with no source manifest, compiler, renderer, or runtime behavior;
3. ticket dispatch captures an exact work-item and repository snapshot and
   records durable `workflow_run_id` identity;
4. manual, ticket, retry, and experiment invocations use the generic binder;
5. operators inspect durable runs with `fly agent workflows show-run`, and
   ticket watch delegates to that surface;
6. the retained five schema-v3 examples are the active examples;
7. the compatibility renderer, root legacy seed manifests, workflow-resolver
   budget fallback, and Dashboard special agent filter are gone;
8. no archive/export command is promised by this cutover.

Include this exact polarity-unambiguous sentence:

```text
Historical schema-1/schema-2 records are inert metadata with no compiler, renderer, or runtime behavior.
```

Witness each active statement independently. Absence scans for stale README
language use exact status handling:

```bash
set -euo pipefail
readme='docs/agentic/README.md'
test -s "$readme"

rg -n -i 'schema[- ]?v?3|schema_version:[[:space:]]*3' "$readme"
rg -n -F 'Historical schema-1/schema-2 records' "$readme"
rg -n -F 'inert metadata' "$readme"
rg -n -F 'no compiler, renderer, or runtime behavior' "$readme"
rg -n -i 'work-item/v1' "$readme"
rg -n -i 'repository/v1' "$readme"
rg -n -F 'workflow_run_id' "$readme"
rg -n -F 'fly agent workflows show-run' "$readme"
rg -n -i 'manual' "$readme"
rg -n -i 'ticket' "$readme"
rg -n -i 'retry' "$readme"
rg -n -i 'experiment' "$readme"

require_no_matches() {
  local status
  set +e
  rg -n -i "$@" "$readme"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no stale README match; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -U \
  'legacy schema versions retain[[:space:]]+their[[:space:]]+existing behavior'
require_no_matches -U \
  'the compatibility renderer[[:space:]]+also includes[[:space:]]+a[[:space:]]+legacy[[:space:]]+`?harvest`?[[:space:]]+judge[^[:cntrl:]]+hard cap'
require_no_matches \
  'inspect with:.*agent workflows run[[:space:]]+<(workflow|workflow-name)>[[:space:]]+<(id|workflow-run-id)>'
require_no_matches \
  'fly([[:space:]]+-t[[:space:]]+[^[:space:]]+)?[[:space:]]+agent workflows (archive|export)([[:space:]]|$)'
```

## Environment and toolchain preflight

Run before focused or full suites. The Makefile invokes bare `ginkgo`; its
version must equal the root module-selected Ginkgo version. At brief-review
time, bare Ginkgo was `2.28.1` while the root module selected `v2.27.3`, so
that environment would stop here. Install the exact root-selected CLI before
continuing; do not silently use the mismatched binary for Make targets.

```bash
set -euo pipefail
for tool in \
  git go make ginkgo shasum rg bash \
  pg_isready initdb postgres psql \
  node npx yarn
do
  command -v "$tool" >/dev/null
done

git --version
go version
make --version | sed -n '1p'
ginkgo version
shasum -a 256 /dev/null
initdb --version
postgres --version
psql --version
node --version
npx --version
yarn --version
pg_isready

root_ginkgo="$(go list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2)"
ci_agent_ginkgo="$(
  cd ci-agent
  go list -m -f '{{.Version}}' github.com/onsi/ginkgo/v2
)"
bare_ginkgo="$(ginkgo version | awk '{print $3}')"
printf 'root module ginkgo=%s\n' "$root_ginkgo"
printf 'ci-agent module ginkgo=%s\n' "$ci_agent_ginkgo"
printf 'bare ginkgo=%s\n' "$bare_ginkgo"
test "v${bare_ginkgo#v}" = "$root_ginkgo"

test "$(cd agent/schema && go env GOMOD)" = "$(pwd)/agent/schema/go.mod"
test "$(cd ci-agent && go env GOMOD)" = "$(pwd)/ci-agent/go.mod"
test "$(go env GOMOD)" = "$(pwd)/go.mod"
```

If the Ginkgo versions differ, install the literal version printed by
`root_ginkgo`:

```text
go install github.com/onsi/ginkgo/v2/ginkgo@<literal root_ginkgo value>
```

Then rerun the complete preflight block and record the matching versions.

## Non-vacuous predecessor focus set

### Corrected migration and database focuses

Run each selection separately; do not join focus expressions with `|`:

```bash
set -euo pipefail
pg_isready

for focus in \
  'workflow schema signature migration' \
  'v3-only workflow liveness migration' \
  'Legacy Database Upgrade'
do
  printf 'DRY RUN: %s\n' "$focus"
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --dry-run --fail-on-empty --focus="$focus" ./atc/db/migration
  printf 'RUN: %s\n' "$focus"
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --fail-on-empty --focus="$focus" ./atc/db/migration
done

for focus in \
  'AgentWorkflowsFactory' \
  'AgentExperimentsFactory'
do
  printf 'DRY RUN: %s\n' "$focus"
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --dry-run --fail-on-empty --focus="$focus" ./atc/db
  printf 'RUN: %s\n' "$focus"
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --fail-on-empty --focus="$focus" ./atc/db
done

bash docs/migration/migrate-preflight_test.sh
```

Record a separate positive selected count and result for all five focuses.
The migration focus must execute the exact same-row re-upgrade spec required
above.

### Task 2B exact plain-Go boundary tests

Run every approved named Task 2B boundary separately. The helper first
requires an exact `go test -list` match and then an exact `=== RUN`/PASS line:

```bash
set -euo pipefail

run_exact_go_test() {
  local package="$1"
  local test_name="$2"
  local listed
  local output

  listed="$(go test "$package" -list "^${test_name}$")"
  printf '%s\n' "$listed" | rg -x "$test_name"
  output="$(go test "$package" -run "^${test_name}$" -count=1 -v)"
  printf '%s\n' "$output"
  printf '%s\n' "$output" | rg -x "=== RUN   ${test_name}"
  printf '%s\n' "$output" |
    rg -x -- "--- PASS: ${test_name} \([0-9.]+s\)"
}

for test_name in \
  TestCompiledDefinitionPublicMethodsRejectNonV3 \
  TestParseCompiledRejectsLegacySchemas \
  TestRequireSchemaVersion3RejectsUnsupportedVersions \
  TestCompileDefinitionRejectsLegacyBeforeContentOrAssetValidation \
  TestCompileDefinitionRunsOrdinaryValidation \
  TestDefinitionJSONOmitsLegacyCompatibilityFields \
  TestMemoryStoreHistoricalNonV3ReadsRemainOpaque
do
  run_exact_go_test ./agent/workflow "$test_name"
done

for test_name in \
  TestImportWorkflowFileRejectsNonV3Locally \
  TestImportWorkflowDirRejectsNonV3Locally \
  TestImportWorkflowFileRejectsMissingAssetsLocally
do
  run_exact_go_test ./fly/commands "$test_name"
done
```

The full package and Fly integration runs remain required; these exact
selections are additional non-vacuity evidence.

### Task 6 Fly plain-Go tests

Require the declared `TestAgentTickets...` inventory to equal the Go test
binary's listed inventory, then run every exact top-level test separately and
record its exact `=== RUN`/PASS line. This fails if Task 6 added no plain-Go
tests, if one is misnamed, or if `go test -run` would otherwise select zero.

```bash
set -euo pipefail
file='fly/commands/agent_tickets_test.go'
test -s "$file"

declared="$(
  sed -nE 's/^func (TestAgentTickets[A-Za-z0-9_]*)\(t \*testing\.T\).*/\1/p' "$file" |
    LC_ALL=C sort
)"
test -n "$declared"

listed="$(
  go test ./fly/commands -list '^TestAgentTickets[A-Za-z0-9_]*$' |
    rg -x 'TestAgentTickets[A-Za-z0-9_]*' |
    LC_ALL=C sort
)"
test "$listed" = "$declared"

while IFS= read -r test_name
do
  test -n "$test_name"
  output="$(go test ./fly/commands -run "^${test_name}$" -count=1 -v)"
  printf '%s\n' "$output"
  printf '%s\n' "$output" | rg -x "=== RUN   ${test_name}"
  printf '%s\n' "$output" | rg -x -- "--- PASS: ${test_name} \([0-9.]+s\)"
done <<EOF
$listed
EOF
```

### Go client and Fly integration Ginkgo tests

Use module-pinned Ginkgo and `--fail-on-empty` for each exact behavior group:

```bash
set -euo pipefail
(
  cd go-concourse
  focus='Agent Tickets'
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --dry-run --fail-on-empty --focus="$focus" ./concourse
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --fail-on-empty --focus="$focus" ./concourse
)

for focus in 'fly agent tickets' 'fly agent workflows.*(show|import)'
do
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --dry-run --fail-on-empty --focus="$focus" ./fly/integration
  go run github.com/onsi/ginkgo/v2/ginkgo \
    --fail-on-empty --focus="$focus" ./fly/integration
done
```

Record selected counts separately for all three focus strings.

## Required full regression sequence

Run in this order after all focused checks and audits are green:

```bash
set -euo pipefail
pg_isready
make test-quick
make test-fly-integration
make test-integration
yarn test
yarn build-elm
git diff --exit-code -- web/public/elm.min.js
```

`make test-quick` covers root unit suites, the `agent/schema` module,
`ci-agent`, and `agent/devmcp`; the separate Make targets cover Fly and ATC
integration. Record every command, duration, pass/fail/skip count where
available, and the optimized Elm asset drift result.

Kubernetes integration and behavioral suites are optional because Task 8
owns no chart, pod, or Kubernetes code. Record an explicit “not run—no K8s
owned path” disposition, or record results if policy requires them. They are
never silently omitted.

## Final repository audit

Run every audit below after the focused suites and before staging. All
negative `rg` checks accept exactly status 1. Status 0 means a forbidden
symbol remains; status 2 or any other value means the audit itself failed.

### Complete Task 2B v3-only model audit

First rerun the approved repository-wide word-bounded consumer scan across
all five roots, including nested `ci-agent`:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no matches; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n \
  'workflow\.(Config|Parse|Compile)\b|Compiled\.Legacy\b|compiled\.Legacy\b|definition\.Legacy\b' \
  agent atc fly go-concourse ci-agent --glob '*.go' \
  --glob '!atc/db/migration/legacyworkflow/**'
```

Run the approved package-structural and narrowed legacy-field/comment scans
unchanged in meaning:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no matches; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n \
  'type Config struct|type Step struct|Legacy[[:space:]]+\*Config|Config[[:space:]]+Config|^func Parse\(|^func Compile\(|^func compileLegacy\(' \
  agent/workflow --glob '*.go'

require_no_matches -n 'Legacy[[:space:]]*:' \
  agent/workflow \
  atc/db/agent_workflows_factory.go \
  atc/db/agent_workflows_factory_test.go \
  fly/commands/agent_workflows.go \
  fly/commands/agent_workflows_test.go \
  fly/integration/agent_workflows_test.go \
  --glob '*.go'

require_no_matches -n \
  'legacy schema|legacy compatibility|schema[-_ ]version[- ]1/2|schema 1/2' \
  agent/workflow/definition.go \
  agent/workflow/parse.go \
  agent/workflow/compile.go \
  agent/workflow/memory_store.go
```

Require all four deleted files independently:

```bash
set -euo pipefail
test ! -e agent/workflow/config.go
test ! -e agent/workflow/parse_test.go
test ! -e agent/workflow/parse_v2_test.go
test ! -e agent/workflow/validate_test.go
```

Prove every surviving v3/model boundary independently:

```bash
set -euo pipefail
rg -n '^func ParseCompiled\(' agent/workflow/parse.go
rg -n '^func CompileDefinition\(' agent/workflow/compile.go
rg -n '^func RequireSchemaVersion3\(' agent/workflow/parse.go
rg -n 'Function[[:space:]]+\*FunctionConfig' agent/workflow/definition.go
rg -n 'func .*PublicSignature' agent/workflow/definition.go agent/workflow/typecheck.go
rg -n '^func ValidateFunction\(' agent/workflow/typecheck.go

rg -n 'SchemaVersion != 3' atc/db/agent_workflows_factory.go
rg -n 'compileStoredWorkflowSource' atc/db/agent_workflows_factory.go
rg -n 'RawYAML' atc/db/agent_workflows_factory.go
rg -n 'SourceManifest' atc/db/agent_workflows_factory.go

rg -n 'CompileDefinition' fly/commands/agent_workflows.go
rg -n 'unsupported schema_version' fly/commands/agent_workflows_test.go
rg -n 'RawYAML' fly/integration/agent_workflows_test.go
```

The exact five retained seed manifests are separate witnesses:

```bash
set -euo pipefail
seeds=(
  agent/workflow/seeds/anonymization-audit-v3/workflow.yml
  agent/workflow/seeds/code-review-v3/workflow.yml
  agent/workflow/seeds/log-diagnosis-v3/workflow.yml
  agent/workflow/seeds/small-fix-v3/workflow.yml
  agent/workflow/seeds/version-upgrade-v3/workflow.yml
)

for seed in "${seeds[@]}"
do
  test -s "$seed"
  printf 'retained seed: %s\n' "$seed"
  rg -n 'schema_version:[[:space:]]+3' "$seed"
done
test "${#seeds[@]}" -eq 5
```

Finally prove the Task 1 decoder remains migration-local:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no migration import; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n 'github.com/concourse/concourse/agent/workflow' \
  atc/db/migration/legacyworkflow \
  atc/db/migration/migrations/1773106101_add_workflow_schema_signature.up.go
require_no_matches -n 'github.com/concourse/concourse/atc' \
  atc/db/migration/legacyworkflow
```

### Complete Task 4 deletion and budget audit

Run the renderer, harvest-import, budget-fallback, and exact resolver-removal
scans:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no Task 4 remnant; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n \
  'RenderLegacyTicket|RenderAgentStep|type RenderInput|func Render\(|RenderSpecMarkdown|RenderPlanMarkdown' \
  agent/dispatch agent/api/tickets --glob '*.go'

require_no_matches -n \
  'github.com/concourse/concourse/agent/harvest|harvest\.' \
  agent/dispatch --glob '*.go'

require_no_matches -n \
  'def\.Config\.Budget\.TicketUSD|workflow default|frozen-workflow default|0 = workflow default|budgetWorkflows\(' \
  agent/dispatch/budgets.go \
  agent/dispatch/budgets_test.go \
  agent/budget/budget.go \
  atc/atccmd/command.go \
  fly/commands/agent_tickets.go

require_no_matches -n \
  'workflows[[:space:]]+WorkflowResolver|b\.workflows' \
  agent/dispatch/budgets.go
```

Prove the positive boundary and exact deletions independently:

```bash
set -euo pipefail
rg -n '0 = uncapped' fly/commands/agent_tickets.go
rg -n 'NewTicketBudgets\(' agent atc fly go-concourse --glob '*.go'
rg -n -F 'os.ReadDir("seeds")' agent/workflow/seed_test.go
rg -n -F 'TestOnlyVersionThreeEngineeringSeedsRemain' agent/workflow/seed_test.go

test ! -e agent/dispatch/render.go
test ! -e agent/dispatch/render_test.go
test ! -e agent/api/tickets/render.go
test ! -e agent/api/tickets/render_test.go
test ! -e agent/workflow/seeds/develop-fable.yaml
test ! -e agent/workflow/seeds/develop.yaml
test ! -e agent/workflow/seeds/direct-dev.yaml
test ! -e agent/workflow/seeds/standard-dev.yaml
test ! -e agent/workflow/seeds/test-first-dev.yaml
```

Inspect every `NewTicketBudgets` result manually and record its file/line. The
definition and every call must have exactly one argument; Go compilation is
the authoritative arity proof.

### Task 5 binder-only deletion audit

Require every retired dispatch/runtime dependency to be absent with exact
status:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no Task 5 remnant; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n 'ErrLegacyDefinition' \
  agent atc fly go-concourse --glob '*.go'
require_no_matches -n \
  'RunSecretLabeler|SecretLabels|NewK8sRunSecretLabeler' \
  agent atc --glob '*.go'
require_no_matches -n \
  'AgentRepoBaseURL|agent-repo-base-url' \
  agent atc fly go-concourse --glob '*.go'
require_no_matches -n \
  'dispatch\.NewTeamTemplateSaver|type TemplateSaver interface|type RunCreator interface|attachRunSecret|resolveRunCredential|deps\.(Templates|Runs|Credentials|Users|Secrets)' \
  agent/dispatch \
  atc/atccmd/command.go \
  atc/db/agent_dispatch_test.go \
  --glob '*.go'
require_no_matches -n -F 'agent-ticket-' \
  agent/dispatch/dispatch.go \
  agent/dispatch/handler.go \
  agent/dispatch/dispatcher.go \
  atc/atccmd/command.go
```

Prove each retained generic boundary independently:

```bash
set -euo pipefail
rg -n -F 'NewVaultedRunSecretPreparer' atc/atccmd/command.go
rg -n 'agentRunSecrets\(\)' atc/atccmd/command.go
rg -n -F 'NewExperimentBinderAdapter' \
  agent/workflowrun atc/atccmd/command.go atc/atccmd/agent_experiments.go
rg -n -F 'RetryOf' \
  agent/workflowrun atc/atccmd/command.go atc/atccmd/agent_experiments.go
rg -n -F 'ExperimentAdmission' \
  agent/workflowrun atc/atccmd/command.go atc/atccmd/agent_experiments.go
```

### Admission, storage, and execution witnesses

Do not let one aggregate regex stand in for several behaviors:

```bash
set -euo pipefail
rg -n '\.Validate\(\)' agent/workflow/compile.go
rg -n '\.Validate\(\)' agent/workflow/memory_store.go
rg -n '\.Validate\(\)' atc/db/agent_workflows_factory.go
rg -n -F 'RequireSchemaVersion3' agent/workflow/compile.go
rg -n -F 'RequireSchemaVersion3' agent/workflow/memory_store.go
rg -n -F 'RequireSchemaVersion3' atc/db/agent_workflows_factory.go
rg -n -F 'InvalidDefinitionError' agent/api/workflows
rg -n -F 'TestRequireSchemaVersion3RejectsUnsupportedVersions' agent/workflow/parse_v3_test.go
rg -n -F 'MemoryStoreHistoricalNonV3ReadsRemainOpaque' agent/workflow
rg -n -F 'SourceManifest' atc/db/agent_workflows_factory_test.go
rg -n -F 'AgentExperimentsFactory' atc/db/agent_experiments_factory_test.go
rg -n -F 'ErrWorkflowNotV3' agent/dispatch
rg -n -F 'dispatchV3' agent/dispatch
```

Record the source line or selected test that proves each of these separately:

- v3 validator invocation count increases;
- schema-1 rejection leaves that count unchanged;
- schema-2 rejection leaves that count unchanged;
- direct MemoryStore state remains unchanged;
- direct database metadata remains unchanged;
- malformed historical `Get` shape;
- malformed historical `Latest` shape;
- corrupt-v3 `Get` failure;
- corrupt-v3 `Latest` failure;
- rejection before snapshot/build/pipeline side effects;
- dispatch, retry, and experiment consumers still execute.

### Migration SQL and same-row re-upgrade audit

The fixed-base added-file equality above is the allocation gate. Add
independent exact-name/content checks:

```bash
set -euo pipefail
up='atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.up.sql'
down='atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql'
spec='atc/db/migration/v3_only_workflows_test.go'

test -s "$up"
test -s "$down"
test -s "$spec"
rg -n -F 'agent_workflow_definitions_live_schema_v3_check' "$up"
rg -n -F 'agent_workflow_definitions_live_schema_v3_check' "$down"
rg -n 'UPDATE[[:space:]]+agent_workflow_definitions|SET[[:space:]]+live[[:space:]]*=[[:space:]]*false' "$up"
rg -n 'schema_version[[:space:]]*=[[:space:]]*3|schema_version[[:space:]]*<>[[:space:]]*3' "$up"
rg -n -F 'demotes a reactivated legacy row again on same-database re-upgrade' "$spec"
rg -n 'jetbridgeHeadMigration[[:space:]]*=[[:space:]]*1773106123' \
  atc/db/migration/legacy_upgrade_test.go
rg -n 'JETBRIDGE_VERSION=1773106123' docs/migration/migrate-preflight.sh
rg -n '1773106123.*down|down.*1773106123' \
  docs/migration/migrate-preflight_test.sh
rg -n '1773106124' docs/migration/migrate-preflight_test.sh
```

The down migration must contain no data mutation:

```bash
set -euo pipefail
down='atc/db/migration/migrations/1773106123_enforce_schema_v3_workflows.down.sql'

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected DDL-only down migration; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n -i \
  '(^|[[:space:]])(UPDATE|INSERT|DELETE|TRUNCATE)([[:space:]]|$)' \
  "$down"

require_no_matches -n \
  'jetbridgeHeadMigration[[:space:]]*=[[:space:]]*1773106122|JETBRIDGE_VERSION=1773106122' \
  atc/db/migration/legacy_upgrade_test.go \
  docs/migration/migrate-preflight.sh
```

Retain the runtime result from the separate
`v3-only workflow liveness migration` focus: same legacy row demoted after
both upgrades, v3 row live after both upgrades, constraint absent after down,
constraint present after re-up, and legacy-live update rejected after re-up.

### Task 6 durable inspection audit

Witness the production surfaces independently from tests:

```bash
set -euo pipefail
test -s fly/commands/agent_tickets.go
test -s fly/commands/agent_tickets_test.go
test -s fly/commands/agent_workflow_runs.go
test -s fly/integration/agent_tickets_test.go
test -s go-concourse/concourse/agent_tickets_test.go

rg -n -F 'WorkflowRunID' fly/commands/agent_tickets.go
rg -n -F 'WorkflowName' fly/commands/agent_tickets.go
rg -n -F 'WorkflowsShowRunCommand' fly/commands/agent_tickets.go
rg -n -F 'show-run' fly/commands/agent_tickets.go
rg -n 'prepare\(' fly/commands/agent_workflow_runs.go
rg -n -F 'executeWithTargetLoader' fly/commands/agent_workflow_runs.go
rg -n -F 'executePreparedWithTarget' fly/commands/agent_workflow_runs.go
rg -n -F 'WorkflowRunID' go-concourse/concourse/agent_tickets_test.go
```

Witness the three critical negative/error fixtures independently:

```bash
set -euo pipefail
tests='fly/commands/agent_tickets_test.go'
integration='fly/integration/agent_tickets_test.go'
rg -n -i 'pipeline.only|pipeline-only' "$tests" "$integration"
rg -n -i 'malformed.*dispatch|dispatch.*malformed' "$tests" "$integration"
rg -n '201|StatusCreated' "$integration"
rg -n -i 'second.*target|target.*once|one target' "$tests" "$integration"
rg -n -i 'no compensat|does not.*(delete|compensat)' "$integration"
```

Reject stale commands/promises with exact status:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no stale Task 6 surface; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n -i \
  'inspect with:.*agent workflows run[[:space:]]+<(workflow|workflow-name)>[[:space:]]+<(id|workflow-run-id)>' \
  fly/commands docs/agentic/README.md
require_no_matches -n -i \
  'fly([[:space:]]+-t[[:space:]]+[^[:space:]]+)?[[:space:]]+agent workflows (archive|export)([[:space:]]|$)' \
  fly/commands docs/agentic/README.md
require_no_matches -n \
  'ticketPipelineName|agent-ticket-|DefaultTeamName|\.Builds\(|BuildEvents\(|eventstream|long:"timestamps"' \
  fly/commands/agent_tickets.go
```

### Task 7 durable navigation, cost, and Dashboard audit

Prove all three shared optional durable-ID decoder uses separately:

```bash
set -euo pipefail
decoder='web/elm/src/Concourse/AgentTicket.elm'
test -s "$decoder"
rg -n -F 'Snapshot.decodeOptionalIdField "workflow_run_id"' "$decoder"
rg -n -F 'Snapshot.decodeOptionalIdField "repository_snapshot_id"' "$decoder"
rg -n -F 'Snapshot.decodeOptionalIdField "work_item_snapshot_id"' "$decoder"
```

Prove each production navigation/state/cost surface independently:

```bash
set -euo pipefail
ticket='web/elm/src/AgentTickets/AgentTicket.elm'
build='web/elm/src/Build/Build.elm'
dashboard='web/elm/src/Dashboard/Filter.elm'
test -s "$ticket"
test -s "$build"
test -s "$dashboard"
test -s web/public/elm.min.js

rg -n -F 'FetchAgentWorkflowRun' "$ticket"
rg -n -F 'Routes.AgentWorkflowRun' "$ticket"
rg -n -F 'Routes.AgentSnapshot' "$ticket"
rg -n -F 'ticket-durable-evidence' "$ticket"
rg -n -F 'workflowName' "$ticket"
rg -n -F 'workflowRunId' "$ticket"
rg -n -F 'detail.summary.workflowName' "$ticket"
rg -n -F 'detail.summary.id' "$ticket"

rg -n -F '"build-agent-cost-bar"' "$build"
rg -n -F '"build-agent-cost"' "$build"
rg -n -F 'FetchBuildAgentMetrics' "$build"

rg -n -F 'agent-ticket-12' web/elm/tests/BuildTicketBarTests.elm
rg -n -F 'agent-ticket-12' web/elm/tests/DashboardAgentFilterTests.elm
```

The quoted cost IDs are distinct: the closing quote makes
`"build-agent-cost"` unable to be satisfied by
`"build-agent-cost-bar"`.

Require every forbidden production derivation to be absent with exact status:

```bash
set -euo pipefail

require_no_matches() {
  local status
  set +e
  rg "$@"
  status=$?
  set -e
  if [ "$status" -ne 1 ]; then
    printf 'expected no stale Task 7 production path; rg status=%s\n' "$status" >&2
    return 1
  fi
}

require_no_matches -n -F 'optionalDurableId' \
  web/elm/src/Concourse/AgentTicket.elm

require_no_matches -n \
  'Routes.OneOffBuild|agent-ticket-run-row|runMetricsByBuild|runHistory|groupMetricsByBuild' \
  web/elm/src/AgentTickets/AgentTicket.elm

require_no_matches -n \
  'agentTicketId|ticketContextBar|build-ticket-context|String.startsWith "agent-ticket-"|String.dropLeft .*agent-ticket-' \
  web/elm/src/Build/Build.elm

require_no_matches -n \
  'AgentOwned|agentFilter|isAgentPipeline|String.startsWith "agent-ticket-"|String.dropLeft .*agent-ticket-' \
  web/elm/src/Dashboard/Filter.elm

require_no_matches -n -F 'agent-ticket-' \
  web/elm/src/Build/Build.elm \
  web/elm/src/Dashboard/Filter.elm
```

Record the focused Elm proofs for exact pair retention, no-ID, blank-name,
changed-name, changed-ID, detail-summary mismatch, and late callback. Test
fixtures may witness those negative cases, but they do not replace the
production source witnesses above.

### Generated and immutable dependency audit

Task 8 must not modify Task 7's generated asset or its read-only decoder and
route/effect dependencies:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"
printf '%s\n' "$TASK7_SHA" | rg -x '[0-9a-f]{40}' >/dev/null
git cat-file -e "${TASK7_SHA}^{commit}"

git diff --exit-code "$TASK7_SHA" -- \
  web/public/elm.min.js \
  web/elm/src/Concourse/WorkflowRun.elm \
  web/elm/src/Concourse/Snapshot.elm \
  web/elm/src/Routes.elm \
  web/elm/src/Message/Effects.elm \
  web/elm/src/Message/Callback.elm \
  web/elm/src/Api/Endpoints.elm \
  web/elm/tests/WorkflowRunDecoderTests.elm \
  web/elm/tests/RoutesTests.elm \
  web/elm/tests/AgenticData.elm \
  web/elm/src/Dashboard/Dashboard.elm \
  web/elm/tests/DashboardAgentStripTests.elm
```

### General repository hygiene

```bash
set -euo pipefail
git diff --check
test -z "$(git ls-files -o --exclude-standard)"
```

Ignored `.superpowers/sdd` evidence is allowed and therefore does not appear
in `git ls-files -o --exclude-standard`. Any other untracked path is a stop.

## Exact pre-stage range comparison

Before staging, require the cumulative working-tree range from the approved
Task 7 base to equal exactly the four owned paths. This is an executable
comparison:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"
printf '%s\n' "$TASK7_SHA" | rg -x '[0-9a-f]{40}' >/dev/null
git cat-file -e "${TASK7_SHA}^{commit}"
git merge-base --is-ancestor "$TASK7_SHA" HEAD
test "$(git rev-parse HEAD)" = "$TASK7_SHA"
git diff --cached --exit-code

expected=$'agent/workflowrun/e2e_test.go\natc/db/agent_workflow_run_integration_test.go\ndocs/agentic/README.md\ndocs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md'
actual="$(
  git diff --name-only "$TASK7_SHA" -- |
    LC_ALL=C sort
)"
test "$actual" = "$expected"
git diff --check "$TASK7_SHA"
```

Review `git diff --stat`, `git diff --name-status`, and the full diff. Stop on
generated files, production edits, broad formatting, prerequisite artifacts,
or unrelated shared-worktree changes.

## Exact staging and cached comparison

Stage only the four tracked paths:

```bash
set -euo pipefail
git add -- \
  agent/workflowrun/e2e_test.go \
  atc/db/agent_workflow_run_integration_test.go \
  docs/agentic/README.md \
  docs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md

expected=$'agent/workflowrun/e2e_test.go\natc/db/agent_workflow_run_integration_test.go\ndocs/agentic/README.md\ndocs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md'
cached="$(
  git diff --cached --name-only -- |
    LC_ALL=C sort
)"
test "$cached" = "$expected"
git diff --cached --check
git diff --exit-code
```

The last command proves there are no unstaged tracked changes. Review the
cached patch completely before committing.

## Initial Task 8 commit

Commit with:

```text
test(agent): verify v3-only workflow cutover
```

Immediately derive the initial SHA and apply its direct-parent gate:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"
printf '%s\n' "$TASK7_SHA" | rg -x '[0-9a-f]{40}' >/dev/null
git cat-file -e "${TASK7_SHA}^{commit}"

TASK8_INITIAL_SHA="$(git rev-parse HEAD)"
printf '%s\n' "$TASK8_INITIAL_SHA" | rg -x '[0-9a-f]{40}' >/dev/null
test "$(git rev-parse "${TASK8_INITIAL_SHA}^")" = "$TASK7_SHA"

expected=$'agent/workflowrun/e2e_test.go\natc/db/agent_workflow_run_integration_test.go\ndocs/agentic/README.md\ndocs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md'
actual="$(
  git diff --name-only "$TASK7_SHA" "$TASK8_INITIAL_SHA" -- |
    LC_ALL=C sort
)"
test "$actual" = "$expected"
test -z "$(git status --porcelain=v1 --untracked-files=all)"
```

Use `apply_patch` to add the resulting literal
`TASK8_INITIAL_SHA='<40-character value>'` and initially identical
`TASK8_FINAL_SHA='<40-character value>'` lines to the ignored SHA ledger.
Do not rely on the temporary shell assignment in later commands.

The direct-parent assertion applies only to `TASK8_INITIAL_SHA`.

## Initial immutable review package

Create a no-clobber binary diff package from the approved Task 7 base to the
current literal final candidate:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"

for sha in "$TASK7_SHA" "$TASK8_INITIAL_SHA" "$TASK8_FINAL_SHA"
do
  printf '%s\n' "$sha" | rg -x '[0-9a-f]{40}' >/dev/null
  git cat-file -e "${sha}^{commit}"
done
test "$TASK8_INITIAL_SHA" = "$TASK8_FINAL_SHA"

head_sha="$(git rev-parse HEAD)"
test "$head_sha" = "$TASK8_FINAL_SHA"
test "$(git rev-parse "${TASK8_INITIAL_SHA}^")" = "$TASK7_SHA"
git merge-base --is-ancestor "$TASK7_SHA" "$TASK8_FINAL_SHA"

expected=$'agent/workflowrun/e2e_test.go\natc/db/agent_workflow_run_integration_test.go\ndocs/agentic/README.md\ndocs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md'
actual="$(
  git diff --name-only "$TASK7_SHA" "$TASK8_FINAL_SHA" -- |
    LC_ALL=C sort
)"
test "$actual" = "$expected"

review_package=".superpowers/sdd/review-${TASK7_SHA:0:12}..${TASK8_FINAL_SHA:0:12}.diff"
test ! -e "$review_package"
(
  set -euo pipefail
  set -o noclobber
  git diff --binary "$TASK7_SHA" "$TASK8_FINAL_SHA" > "$review_package"
)
test -s "$review_package"
shasum -a 256 "$review_package"
test -z "$(git status --porcelain=v1 --untracked-files=all)"
```

The package filename and SHA-256 go in the report. Never overwrite or reuse a
review package; packages are immutable evidence for one exact range.

## Task 8 report

Create `.superpowers/sdd/v3-cutover-task-8-report.md` with `apply_patch`.
Record:

- the fixed cutover base and complete ordered Task 1/2A/5/3/4/2B/6/7 SHA,
  approval-manifest, bound report/checksum, bound PASS-review/checksum, and
  reviewer ledger;
- proof the corrected Task 3 through Task 7 chain was independently approved;
- Task 1 immutable four-path result and complete-range sole-migration-pair
  equality;
- the tracked-plan reconciliation table, including every Task 2B amendment
  and the stale-directive absence audit;
- exact declaration, `go test -list`, `=== RUN`, and PASS evidence for both
  Task 8 plain-Go tests;
- exact source/list/RUN inventory for every Task 6 Fly plain-Go test;
- separate selected/passed/failed counts for every Ginkgo focus;
- compiler/admission typed schema-1/schema-2 rejections and persisted non-v3
  pre-side-effect rejection;
- PostgreSQL vertical-slice IDs, exact snapshot types/IDs, provenance, row
  deletions, and durable post-deletion identity;
- first-up/down/same-row-reactivation/re-up migration results, including v3
  liveness and post-re-up constraint rejection;
- every cross-boundary matrix row and its exact witness;
- Make/bare-Ginkgo/module-Ginkgo/shasum/toolchain versions and PostgreSQL
  readiness;
- focused, full Go module, dev-MCP, Fly, ATC integration, Elm, optimized
  asset, and optional-K8s disposition;
- complete Task 2B, Task 4, Task 6, Task 7, documentation, generated-asset,
  and negative-status audits;
- the executable pre-stage, cached, initial-range, and final-range four-path
  comparisons;
- `TASK8_INITIAL_SHA`, every correction SHA, `TASK8_FINAL_SHA`, each immutable
  package filename/SHA-256, and any concerns.

The report must distinguish command output observed during implementation
from evidence copied out of prerequisite reports.

## Independent review and correction loop

An independent reviewer reads this brief, every authority artifact, the
tracked plan, Task 8 report, and the immutable cumulative package. The reviewer
reruns:

1. ledger/ancestry/fixed-base/Task-1 immutability gates;
2. exact plain-Go selections and every Ginkgo focus;
3. same-row migration focus;
4. complete Task 2B/Task 4/Task 6/Task 7 audits;
5. required full regression sequence;
6. exact cumulative four-path comparison; and
7. package checksum and final-HEAD identity.

The reviewer writes a new, non-clobber
`.superpowers/sdd/v3-cutover-task-8-review.md` (or a new round-specific name
if that path already exists) with the exact reviewed SHA and package hash.
Task 8 is not PASS until that artifact says PASS.

If review finds a defect:

1. keep the original package and review artifact unchanged;
2. edit only one or more of the same four owned tracked paths;
3. rerun every affected focused check, then the complete audits and full
   regression sequence;
4. exact-stage only the changed subset and make a separate correction commit;
5. derive the new `HEAD`, use `apply_patch` to replace only the literal
   `TASK8_FINAL_SHA` in the ignored ledger, and append the correction SHA to
   the report;
6. require Task 7 to be an ancestor of the new final SHA and exact-compare the
   entire cumulative Task 7-to-final range to all four owned paths;
7. create a new immutable package whose filename contains the new final SHA,
   with no-clobber protection; and
8. obtain a fresh independent review of that new cumulative package.

Run this self-contained final gate after every correction:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"

for sha in "$TASK7_SHA" "$TASK8_INITIAL_SHA" "$TASK8_FINAL_SHA"
do
  printf '%s\n' "$sha" | rg -x '[0-9a-f]{40}' >/dev/null
  git cat-file -e "${sha}^{commit}"
done

test "$(git rev-parse HEAD)" = "$TASK8_FINAL_SHA"
test "$(git rev-parse "${TASK8_INITIAL_SHA}^")" = "$TASK7_SHA"
git merge-base --is-ancestor "$TASK7_SHA" "$TASK8_FINAL_SHA"
git merge-base --is-ancestor "$TASK8_INITIAL_SHA" "$TASK8_FINAL_SHA"

expected=$'agent/workflowrun/e2e_test.go\natc/db/agent_workflow_run_integration_test.go\ndocs/agentic/README.md\ndocs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md'
actual="$(
  git diff --name-only "$TASK7_SHA" "$TASK8_FINAL_SHA" -- |
    LC_ALL=C sort
)"
test "$actual" = "$expected"
git diff --check "$TASK7_SHA" "$TASK8_FINAL_SHA"
test -z "$(git status --porcelain=v1 --untracked-files=all)"
```

Then create the correction package with the same no-clobber procedure,
re-derive `HEAD` inside that package command, and record its checksum.

Use this self-contained package block for every corrected final candidate:

```bash
set -euo pipefail
ledger='.superpowers/sdd/v3-cutover-task-8-approved-shas.sh'
test -r "$ledger"
. "$ledger"

for sha in "$TASK7_SHA" "$TASK8_INITIAL_SHA" "$TASK8_FINAL_SHA"
do
  printf '%s\n' "$sha" | rg -x '[0-9a-f]{40}' >/dev/null
  git cat-file -e "${sha}^{commit}"
done

head_sha="$(git rev-parse HEAD)"
test "$head_sha" = "$TASK8_FINAL_SHA"
test "$(git rev-parse "${TASK8_INITIAL_SHA}^")" = "$TASK7_SHA"
git merge-base --is-ancestor "$TASK7_SHA" "$TASK8_FINAL_SHA"
git merge-base --is-ancestor "$TASK8_INITIAL_SHA" "$TASK8_FINAL_SHA"

expected=$'agent/workflowrun/e2e_test.go\natc/db/agent_workflow_run_integration_test.go\ndocs/agentic/README.md\ndocs/superpowers/plans/2026-07-23-agentic-v3-workflow-cutover.md'
actual="$(
  git diff --name-only "$TASK7_SHA" "$TASK8_FINAL_SHA" -- |
    LC_ALL=C sort
)"
test "$actual" = "$expected"

review_package=".superpowers/sdd/review-${TASK7_SHA:0:12}..${TASK8_FINAL_SHA:0:12}.diff"
test ! -e "$review_package"
(
  set -euo pipefail
  set -o noclobber
  git diff --binary "$TASK7_SHA" "$TASK8_FINAL_SHA" > "$review_package"
)
test -s "$review_package"
shasum -a 256 "$review_package"
test -z "$(git status --porcelain=v1 --untracked-files=all)"
```

## Final acceptance

Task 8 is complete only when:

- every prerequisite and Task 8 review is independently PASS;
- `TASK8_INITIAL_SHA` is a direct child of approved `TASK7_SHA`;
- approved Task 7 is an ancestor of `TASK8_FINAL_SHA`;
- the cumulative Task 7-to-final range equals exactly the four owned paths;
- all focused selections are non-empty and all required suites pass;
- the complete negative scans return exactly status 1;
- every immutable package remains present with its recorded checksum;
- the final review names the literal `TASK8_FINAL_SHA`; and
- the tracked plan and active README tell the final, reconciled v3-only
  contract with no contradictory compatibility instructions.

---

## Plan self-review

- Coverage: completed Task 1 freezes historical upgrade behavior; pending
  Tasks 2A, 5, 3, 4, 2B, 6, 7, and 8 close admission, dispatch, storage,
  compatibility, runtime-model, Fly, Elm, and final-proof boundaries in the
  only buildable order.
- Ownership: each task has a literal owned-path inventory and literal staging
  rule. Dynamic expansion is allowed only where its task requires an
  execution-time direct-caller inventory, and every added path must be
  documented and staged literally.
- Type/interface consistency:
  `UnsupportedSchemaVersionError`, `InvalidDefinitionError`,
  `InvalidPromotionError`, `ErrWorkflowNotV3`, `BindAndCreate`,
  `workflow_run_id`, and the exact `(workflowName, workflowRunId)` Elm pair
  retain one meaning across all boundaries.
- Migration consistency: Task 1 stays immutable; Task 3 exclusively allocates
  `1773106123`; its down migration is DDL-only; Task 8 proves same-row
  down/reactivate/re-up behavior.
- Verification consistency: each task uses its approved non-vacuity contract.
  Tasks that require exact declarations, `go test -list`, and `=== RUN`
  witnesses retain them; Tasks 2A and 4 retain their approved focused
  regex/package and explicit failure evidence. Every prescribed Ginkgo focus
  is module-pinned and fail-on-empty, every absence scan distinguishes status
  1 from audit failure, and all PostgreSQL suites begin with readiness checks.
- Task 8 SHA semantics: `TASK8_INITIAL_SHA` is the direct child of approved
  Task 7 and never changes. `TASK8_FINAL_SHA` initially equals it, then moves
  only to separately committed Task 8 correction descendants. Final
  acceptance compares the cumulative Task-7-to-final range to exactly the
  same four owned paths.

## Execution handoff

Resume at Task 2A from the exact approved Task 1 SHA
`ea236ad28ee99ac49e5194c9224f437aa616c4fe`. Do not mark any pending task
complete until its implementation commit, required evidence report, immutable
review package where prescribed, and independent PASS review agree on the
literal reviewed SHA.
