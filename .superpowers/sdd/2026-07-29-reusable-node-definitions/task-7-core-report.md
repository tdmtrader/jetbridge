# Task 7 core service report

## Delivered

- Added the selected-consumer upgrade service and stable request/result/status
  types used by the HTTP and Fly surfaces.
- Compatible upgrades begin from each selected workflow's live immutable
  source, validate its exact persisted predecessor bindings, rewrite every
  matching node-reference object under the workflow plan and supported
  wrappers, and import one new unpromoted revision independently per workflow.
- Upgrade imports consume the store's atomic `Inserted` outcome. An insertion
  reports `created`; an exact concurrent or repeated manifest hit reports
  `unchanged` without consulting `Latest`.
- The service verifies the returned durable workflow-definition identity,
  rewritten manifest hash, non-live state, and exact successor binding content
  by definition ID before reporting success.
- Explicitly breaking successors perform no imports. Valid selected consumers
  receive `recomposition_required` plus sorted added, removed, and changed
  input, output, and parameter obligations.
- Duplicate selections are rejected before reads or writes. Missing live
  revisions, absent predecessor references, stale bindings, and compile/import
  failures remain per-workflow failures so one selected consumer cannot roll
  back another.

## Decisions

- Workflow results are sorted by workflow name; the request slice and all
  returned source objects remain unmodified.
- Only decoded node-reference steps reached through the workflow plan traversal
  are eligible for rewriting. Arbitrary `uses` keys in ordinary step
  configuration and non-source manifest files are retained.
- `workflow.yaml` remains preferred and legacy `workflow.yml` remains legacy;
  the selected source file is re-encoded while every other manifest file is
  copied byte-for-byte.
- Live binding validation rejects unexpected predecessor bindings. Imported
  successor validation verifies every rewritten instance but permits a
  separate pre-existing reference that already selected the successor.
- A post-import binding verification failure is fail-closed and can leave the
  atomically imported revision unpromoted. The production database importer
  writes definition and bindings in one transaction, so this path represents
  durable corruption or an inconsistent store implementation rather than an
  ordinary compile failure.

## Verification

- RED: `go test ./agent/workflow -run 'TestNodeUpgrade' -count=1` failed to
  compile because the upgrade service and atomic import outcome were absent.
- RED: the mixed `code-review@4`/`code-review@5` regression initially reported
  the pre-existing successor instance as an unexpected binding.
- RED: a stale binding with the wrong empty-valued parameter key was initially
  accepted because map comparison did not test key presence.
- PASS:
  `go test ./agent/workflow -run 'TestNodeUpgrade(AllowsPreexistingSuccessorBinding|DetectsStaleEmptyValuedBindingKey)' -count=1`.
- PASS: `go test ./agent/workflow -run 'TestNodeUpgrade' -count=1`.
- PASS:
  `go test ./agent/workflow ./agent/workflow/workflowtest -count=1`.
- PASS: `go test ./agent/api/nodeupgrades -count=1`.
- PASS: `git diff --check -- agent/workflow/node_upgrade.go
  agent/workflow/node_upgrade_test.go`.

PostgreSQL-backed Ginkgo was not started: the single availability check,
`pg_isready -h 127.0.0.1 -p 5434`, returned `no response`. The focused
in-memory service suite and the separate import-outcome store coverage are the
available core evidence for this checkpoint.

## Structural-compatibility follow-up

- Review found that invocation validation still required mappings for every
  successor port even though compatible releases may add optional inputs and
  outputs. Such a successor could be released but could not be composed into
  an existing consumer during upgrade.
- Node invocation validation now requires every required input while accepting
  omitted optional inputs and intentionally ignored outputs. Every authored
  mapping still has to name a declared port, use a safe artifact identifier,
  and remain injective.
- Invocation expansion filters task and agent leaf declarations to the
  composed logical ports. Task typed declarations are then mapped to physical
  artifact names as before; agent typed declarations retain logical keys as
  before. The durable binding retains the exact authored maps and parameters.
- Filtering occurs only after `CompiledNodeDefinition.Instantiate`, inside
  workflow composition. Direct node instantiation/execution therefore retains
  the successor's complete input and output contract.
- Added compiler coverage for both agent and task successors that introduce an
  omitted optional input and an ignored output. Added an end-to-end core
  upgrade regression proving compatible release, selected immutable
  unpromoted revision creation, application of a newly defaulted parameter,
  exact preservation of old maps/bindings, and an unchanged live predecessor.

### Follow-up verification

- RED:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow -run
  'TestCompileDefinitionWithNodesComposes(Agent|Task)FromMappedSuccessorPorts'
  -count=1` rejected both cases with
  `node input_mapping is missing port "policy"`.
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow -run
  'TestCompileDefinitionWithNodesComposes(Agent|Task)FromMappedSuccessorPorts|
  TestCompileDefinitionWithNodesRejectsNonExactOrIncompleteReferences|
  TestCompileDefinitionWithNodesRejectsNonInjectiveMappings' -count=1`.
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow -run
  '^TestNodeUpgradeComposesCompatibleSuccessorAdditionsWithoutChangingBindings$'
  -count=1`.
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow -run
  'Test(CompileDefinitionWithNodes|ApplyNodeInvocation|NodeUpgrade)' -count=1`.
- PASS:
  `git diff --check -- agent/workflow/node_reference.go
  agent/workflow/node_reference_test.go agent/workflow/node_upgrade_test.go
  .superpowers/sdd/2026-07-29-reusable-node-definitions/task-7-core-report.md`.
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow
  ./agent/workflow/workflowtest -count=1`.

The bounded final review accepted the subset-composition semantics and the
agent, task, direct-instantiation, and upgrade coverage with no remaining
blocking finding.
