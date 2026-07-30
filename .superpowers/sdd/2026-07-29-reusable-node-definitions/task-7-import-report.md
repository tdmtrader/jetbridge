# Task 7 atomic workflow import outcome report

## Delivered

- Added `workflow.ImportOutcome` and the locked
  `Store.ImportManifestWithOutcome` contract.
- Kept `Import` and `ImportManifest` behavior compatible; the established
  methods delegate to the atomic outcome path and unwrap its definition.
- MemoryStore decides inserted versus exact idempotent hit under its mutex,
  preserves the original creator, and returns independent content clones.
- Node-aware MemoryStore content clones now recompile with the store's exact
  configured `NodeResolver`. Node-reference manifests survive import, Get,
  Latest, Promote, and Live without changing workflow-only store behavior.
- AgentWorkflowsFactory decides the outcome inside the existing per-name
  advisory-locked transaction. Exact manifest/hash hits validate durable node
  bindings and report `Inserted: false`; new rows and their bindings commit
  together before reporting `Inserted: true`.
- Regenerated `FakeAgentWorkflowsFactory` for the expanded interface.

## TDD and verification

- RED, captured with the shared Go cache:
  the focused workflow test build failed because
  `Store.ImportManifestWithOutcome` was undefined.
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow/workflowtest -run 'TestMemoryStoreImportManifestWithOutcomeIsAtomic|TestNodeAwareMemoryStoreRecompilesContentClonesWithExactResolver' -count=1`
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./agent/workflow ./agent/workflow/workflowtest -count=1`
- PASS:
  `GOCACHE=/private/tmp/concourse-go-cache go test ./atc/db ./atc/db/dbfakes -run '^$' -count=1`
- PASS: `git diff --check`

The focused serial DB command was attempted while port 5434 was free:

`GOCACHE=/private/tmp/concourse-go-cache ginkgo --focus='AgentWorkflowsFactory' ./atc/db`

The sandbox blocked PostgreSQL initialization at `shmget` with
`Operation not permitted`; BeforeSuite ran zero focused specs. No local
escalation or infrastructure retry was made. The parent task owns any final
host-side focused DB run.
