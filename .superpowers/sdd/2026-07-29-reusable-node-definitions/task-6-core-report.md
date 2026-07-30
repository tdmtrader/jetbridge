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
- PASS: `git diff --check`

Focused Ginkgo DB regressions for the factory and lifecycle are compiled but
not executed in this subtask: the suite cannot start while PostgreSQL is
already listening on fixed port `5434` (PID `82186`). No process was stopped;
the parent task owns the final serialized DB retry.
