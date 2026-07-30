# Task 6 core report

## Delivered

- Added a kind-normalized workflow-run binder path for exact reusable-node
  execution. Node calls require an explicit version, resolve through the
  trusted server-owned node store, instantiate only declared parameters, and
  reject workflow implementation/source-admission surfaces.
- Kept direct node execution source-free and separated its immutable template
  namespace (`agent-node-...`) from workflow templates.
- Persisted `definition_kind` on durable runs; Create/Get/Find/List now have
  exact kind-aware paths, with idempotency keys isolated by kind and optional
  version filtering for node listings. The legacy public methods intentionally
  retain workflow-only scope.
- Resume rebuilds node functions from the frozen parameterized config, so a
  retry cannot silently reapply current node defaults instead of the original
  non-default values.
- Wired the shared production binder with `WithNodeStore(db.NewAgentNodesFactory(conn))`.
- Repaired kind boundaries around source-admission validation, snapshot sealing,
  workflow-only projections, and retired-template lifecycle successorship.
  Durable all-kind reconciliation/retention/metric paths were intentionally
  left unchanged.

## Verification

- RED: `go test ./agent/workflowrun -run 'TestBindAndCreateRejectsNodeDefaultResolution' -count=1`
  initially failed to compile because the node Binder API did not exist.
- PASS: `go test ./agent/workflowrun -run 'TestBindAndCreateRunsExactUnreleasedNodeVersion|TestBindAndCreateRejectsNodeDefaultResolution' -count=1`
- PASS: `go test ./agent/workflowrun -count=1`
- PASS: `go test ./agent/workflowrun ./atc/atccmd ./atc/db -run '^$' -count=1`
- PASS (outside the filesystem/process sandbox so PostgreSQL could allocate
  shared memory): `ginkgo --focus='creates and reads exact node runs in a
  separate kind-scoped identity|selects completed node runs only after a newer
  node version is released' ./atc/db/` — 2/2 specs passed.
- PASS: `git diff --check`

The first executable DB retry found that the new retirement test fixture set
only `released_at`, violating the real node-release metadata constraint.
Commit `237578358c` made the fixture supply the required release actor and
compatibility classification; the exact two-spec rerun then passed.
