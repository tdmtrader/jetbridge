# Task 5 report — durable workflow-to-node consumer bindings

## Delivered

- Added migration `1773106150` with literal workflow/node kind columns,
  composite foreign keys, and consumer indexes.
- Workflow import now obtains fresh resolved bindings, inserts them in the
  definition transaction, and compares every persisted idempotent binding
  against the fresh exact resolution using PostgreSQL JSONB equality.
- `NodeStore.Consumers` uses an immutable `(workflow_definition_id,
  instance_name)` cursor, supports `PromotedOnly`, and returns workflow live
  state plus the full resolved binding. `Bindings(definitionID)` provides the
  race-free exact import read.
- Tests cover literal kind/composite-FK enforcement, idempotent replay and
  tamper rejection, transactional rollback on failed binding insertion, and
  two bindings in one workflow across a page boundary.

## Verification

- `go test ./agent/workflow ./agent/workflow/workflowtest ./atc/db -run '^$' -count=1`
- `ginkgo --focus='discovers durable workflow node consumers' ./atc/db`
- `ginkgo --focus='workflow node bindings migration' ./atc/db/migration`
- `ginkgo --focus='composes source-pipeline promotion with exact released-node imports|rolls back a workflow revision' ./atc/db`
