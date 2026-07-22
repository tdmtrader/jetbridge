# Adapters, Projections, and Explicit Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect real engineering work to generic workflow functions without restoring ticket centrality: capture exact work-item/resource snapshots, project typed reviews and repository changes, and replace privileged harvest behavior with explicit visible transformations, evaluators, and publishers.

**Architecture:** Adapters capture mutable/external state into immutable snapshots before admission. Type projectors derive query/UI records from sealed snapshots while retaining the snapshot as canonical truth. Ticket dispatch captures one transactionally consistent revision and calls the generic binder for v3 definitions. Existing harvest Git/gate/judge mechanics are extracted behind normal task/agent/publisher contracts; legacy harvest remains only for v1/v2 until dogfood equivalence passes.

**Tech Stack:** Go, ticket/review/outcome stores, Git helpers, ATC APIs, Fly CLI, typed snapshot contracts, generic workflow binder.

## Global Constraints

- No adapter reads a live system from inside the workflow. It captures before a run or at an explicit interaction boundary and binds the resulting snapshot.
- Projection failure never mutates canonical snapshot bytes or retroactively invalidates a successful seal; it is retried and surfaced separately.
- Publishers consume validated snapshots and are explicit plan nodes. Their operation key is SHA-256 over canonical JSON containing publisher type/version, input snapshot ID, destination, mode, normalized side-effect parameters, and approval policy version; branch, PR, and merge operations can never collide.
- V1/v2 ticket attempts continue to use compatibility harvest until equivalent v3 flows are proven.

---

### Task 1: Capture transactionally consistent work-item revisions

**Files:**
- Modify: `agent/api/tickets/types.go`
- Modify: `agent/api/tickets/types_test.go`
- Modify: `agent/api/tickets/memory_store.go`
- Create: `agent/workitem/capture.go`
- Create: `agent/workitem/capture_test.go`
- Modify: `atc/db/agent_tickets_factory.go`
- Modify: `atc/db/agent_tickets_factory_test.go`
- Create: `atc/db/migration/migrations/1773106102_add_ticket_revisions.up.sql`
- Create: `atc/db/migration/migrations/1773106102_add_ticket_revisions.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write tests showing ticket body, latest spec, active plan/tasks, comments/answers, state, workflow selection, and revision counter are captured from one database snapshot.
- [ ] Add concurrent-edit tests proving capture yields either complete revision N or N+1, never a torn mixture.
- [ ] Run focused ticket tests and confirm failure.
- [ ] Add a monotonically increasing `revision` to mutable tickets and increment it in every mutation transaction.
- [ ] Implement `CaptureRevision(ticketID)` returning strict `work-item/v1` bytes plus revision metadata. PostgreSQL uses one repeatable-read transaction; memory store uses one mutex critical section.
- [ ] Seal through the standard upload/capture production path and return the existing snapshot when the same ticket revision was already captured.
- [ ] Advance `jetbridgeHeadMigration` to `1773106102` and pass legacy-to-head plus down/up migration coverage.
- [ ] Re-run tests and commit `feat(ticket): capture immutable work-item revisions`.

### Task 2: Add resource-version snapshot capture

**Files:**
- Create: `agent/resourcecapture/capture.go`
- Create: `agent/resourcecapture/capture_test.go`
- Create: `agent/api/snapshots/resource_handler.go`
- Modify: `agent/api/snapshots/handler_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `fly/commands/agent_snapshots.go`
- Modify: `fly/integration/agent_snapshots_test.go`

- [ ] Write tests for an exact pipeline/resource/version request, resource authorization, version-not-found, disabled checks, idempotent capture, cancellation, and captured source metadata.
- [ ] Run focused tests and confirm failure.
- [ ] Build a one-shot immutable capture template containing the ordinary pinned `get:` plus a deterministic typed pass-through task whose output is sealed. The caller supplies the semantic type; `repository/v1` is the default only for Git resources.
- [ ] Persist resource pipeline/name/version JSON on the capture production occurrence, not the globally deduplicated snapshot row; the captured content digest remains canonical identity and the requesting team receives an explicit grant.
- [ ] Expose `agent snapshots capture-resource --pipeline --resource --version --type` and return the durable workflow/pipeline execution while capture is pending, then the snapshot ID when complete.
- [ ] Re-run tests and commit `feat(snapshot): capture exact Concourse resource versions`.

### Task 3: Project `review/v1` snapshots into review and feedback surfaces

**Files:**
- Create: `agent/projection/projector.go`
- Create: `agent/projection/projector_test.go`
- Create: `agent/projection/review.go`
- Create: `agent/projection/review_test.go`
- Modify: `agent/api/reviews/types.go`
- Modify: `agent/api/reviews/handler.go`
- Modify: `agent/api/reviews/handler_test.go`
- Modify: `atc/db/agent_reviews_factory.go`
- Modify: `atc/db/agent_reviews_factory_test.go`
- Create: `atc/db/migration/migrations/1773106103_link_review_feedback_snapshots.up.sql`
- Create: `atc/db/migration/migrations/1773106103_link_review_feedback_snapshots.down.sql`
- Modify: `agent/api/feedback/handler.go`
- Modify: `atc/db/agent_feedback_factory.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write projector tests that open a sealed review, revalidate its digest/type, derive existing summary fields, and idempotently upsert by `snapshot_id`.
- [ ] Test projection retry, corrupt/unavailable content, duplicate repository/commit reviews, and legacy build-based reads.
- [ ] Add feedback tests proving `(review_snapshot_id, finding_id, reviewer)` is authoritative and two reviews of one commit no longer collide.
- [ ] Run focused review/feedback tests and confirm failure.
- [ ] Add nullable unique `snapshot_id`, `workflow_run_id`, and `production_id` to reviews; backfill remains null. Add `review_snapshot_id` to feedback and preserve legacy repo/commit columns during compatibility.
- [ ] Register the review projector by exact type. Trigger asynchronously after seal commit and provide a reconciliation query for sealed-but-unprojected snapshots.
- [ ] Make snapshot/run review endpoints canonical while existing build/ticket endpoints query linked projections.
- [ ] Advance `jetbridgeHeadMigration` to `1773106103` and pass legacy-to-head plus down/up migration coverage.
- [ ] Re-run tests and commit `feat(review): project sealed review snapshots`.

### Task 4: Project repository changes and bounded diffs

**Files:**
- Create: `agent/projection/repository_change.go`
- Create: `agent/projection/repository_change_test.go`
- Create: `atc/db/migration/migrations/1773106104_create_repository_change_projections.up.sql`
- Create: `atc/db/migration/migrations/1773106104_create_repository_change_projections.down.sql`
- Create: `atc/db/agent_repository_changes_factory.go`
- Create: `atc/db/agent_repository_changes_factory_test.go`
- Modify: `agent/gitcheck/detect.go`
- Modify: `agent/gitcheck/detect_test.go`
- Modify: `agent/api/outcomes/diff_handler.go`
- Modify: `agent/api/outcomes/diff_handler_test.go`
- Modify: `agent/api/snapshots/handler.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write tests deriving changed files/statistics and the existing 64 KiB bounded unified diff from each supported repository-change representation.
- [ ] Test binary files, rename/delete, truncation metadata, invalid base, large changes, projection retry, and no network access.
- [ ] Run focused tests and confirm failure.
- [ ] Persist `snapshot_id`, repository, base/result SHA, counts, bounded diff, truncation flag/reason, and projection status. The row is explicitly a projection, not canonical content.
- [ ] Add `/api/v1/agent/snapshots/:snapshot_id/projections/repository-change` and adapt ticket diff to resolve the ticket's output snapshot first, falling back to legacy live mirror computation only for v1/v2 attempts.
- [ ] Advance `jetbridgeHeadMigration` to `1773106104` and pass legacy-to-head plus down/up migration coverage.
- [ ] Re-run tests and commit `feat(diff): project repository change snapshots`.

### Task 5: Add generic workflow outcomes and human-intervention linkage

**Files:**
- Create: `agent/api/workflowoutcomes/types.go`
- Create: `agent/api/workflowoutcomes/types_test.go`
- Create: `agent/api/workflowoutcomes/handler.go`
- Create: `agent/api/workflowoutcomes/handler_test.go`
- Create: `atc/db/migration/migrations/1773106105_create_workflow_outcomes.up.sql`
- Create: `atc/db/migration/migrations/1773106105_create_workflow_outcomes.down.sql`
- Create: `atc/db/agent_workflow_outcomes_factory.go`
- Create: `atc/db/agent_workflow_outcomes_factory_test.go`
- Modify: `agent/outcomewatcher/watcher.go`
- Modify: `agent/outcomewatcher/watcher_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write tests for outcomes keyed by durable workflow run and output snapshot: accepted/rejected/merged/abandoned dispositions, publication state, human-modified flag, optional human modification snapshot, intervention count, labels, actor, and audit time.
- [ ] Test multiple outputs per run, non-ticket runs, authorized human updates, idempotent watcher ingestion, and legacy ticket outcome projection without identity collisions.
- [ ] Run focused outcome tests and confirm failure.
- [ ] Create `agent_workflow_outcomes` independently from the legacy one-row-per-ticket table. Link `workflow_run_id`, `output_snapshot_id`, optional `modification_snapshot_id`, and publication IDs durably.
- [ ] Adapt outcome watching to write the generic record when snapshot/run linkage exists and preserve legacy behavior otherwise. Expose run/output outcome APIs used by operational scorecards.
- [ ] Advance `jetbridgeHeadMigration` to `1773106105`, pass migration coverage, and commit `feat(outcomes): link production results to workflow snapshots`.

### Task 6: Route version-3 tickets through the generic binder

**Files:**
- Modify: `agent/dispatch/dispatch.go`
- Modify: `agent/dispatch/dispatch_test.go`
- Modify: `agent/dispatch/dispatcher.go`
- Modify: `agent/dispatch/reconcile.go`
- Modify: `atc/db/agent_dispatch_test.go`
- Modify: `agent/api/tickets/types.go`
- Modify: `atc/db/agent_tickets_factory.go`
- Create: `atc/db/migration/migrations/1773106106_link_tickets_workflow_runs.up.sql`
- Create: `atc/db/migration/migrations/1773106106_link_tickets_workflow_runs.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write dispatch tests for v3 workflow selection, atomic claim/reservation, work-item snapshot capture, repository snapshot binding, explicit port mapping, idempotent concurrent dispatch, secret attachment, budget admission, and exact definition pinning.
- [ ] Prove edits after capture do not alter active run inputs; interaction continuation is exercised through the explicit wait mechanism in Task 7.
- [ ] Prove v1/v2 dispatch behavior stays on `RenderLegacyTicket` and response fields remain compatible.
- [ ] Run unit/DB dispatch tests and confirm failure.
- [ ] Add `workflow_run_id`, `work_item_snapshot_id`, and dispatch reservation key to tickets. Claim/reserve in one DB transaction before any template/run side effect.
- [ ] Configure per-workflow adapter port mappings in ticket dispatch settings: defaults are `work_item -> work-item/v1` and `repository -> repository/v1`, but import does not reserve these names.
- [ ] Call `workflowrun.Binder` in-process, record both durable workflow-run and underlying pipeline-run IDs, then transition ticket state.
- [ ] Advance `jetbridgeHeadMigration` to `1773106106` and pass legacy-to-head plus down/up migration coverage.
- [ ] Re-run tests and commit `refactor(dispatch): make tickets a workflow binding adapter`.

### Task 7: Add explicit human question and answer snapshot waits

**Files:**
- Create: `agent/workflowwait/types.go`
- Create: `agent/workflowwait/store.go`
- Create: `agent/workflowwait/memory_store.go`
- Create: `agent/workflowwait/handler.go`
- Create: `agent/workflowwait/handler_test.go`
- Create: `atc/db/migration/migrations/1773106107_create_workflow_waits.up.sql`
- Create: `atc/db/migration/migrations/1773106107_create_workflow_waits.down.sql`
- Create: `atc/db/agent_workflow_waits_factory.go`
- Create: `atc/db/agent_workflow_waits_factory_test.go`
- Create: `atc/exec/await_snapshot_step.go`
- Create: `atc/exec/await_snapshot_step_test.go`
- Modify: `atc/steps.go`
- Modify: `atc/plan.go`
- Modify: `atc/builds/planner.go`
- Modify: exhaustive ATC step visitors/public plan/factories
- Modify: `agent/workflow/typecheck.go`
- Modify: `agent/workflow/typecheck_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write config/planner/exec and workflow type-flow tests for visible `await_snapshot: <output-name>`, exact expected output type, typed input `question/v1` snapshot, timeout, and `on_timeout` (`fail` or `default`) fields, including use inside `do` and `in_parallel`.
- [ ] Write store/API tests for durable wait creation, run/build authorization, one authorized answer, wrong-type/team rejection, concurrent answer race, cancellation, timeout, restart/resume, and audit attribution.
- [ ] Prove answering creates or binds an immutable `human-answer/v1` snapshot; a ticket adapter may atomically capture a new `work-item/v1` revision as additional context, but earlier snapshots remain unchanged.
- [ ] Run focused tests and capture expected visitor failures.
- [ ] Implement the step by persisting a wait, parking without holding a live-system connection, then materializing the resolved answer snapshot into the build repository with its existing snapshot reference. Extend the v3 type-checker so the declared answer output enters the environment exactly like another typed producer. `default` requires an authored default snapshot ID pinned in the plan.
- [ ] Add resolve/list endpoints under the durable workflow run and update every exhaustive visitor. Advance `jetbridgeHeadMigration` to `1773106107` and pass migration coverage.
- [ ] Re-run tests and commit `feat(agent): continue workflows with immutable human answers`.

### Task 8: Extract deterministic validators and evaluators from harvest

**Files:**
- Create: `agent/functions/repositoryvalidate/runner.go`
- Create: `agent/functions/repositoryvalidate/runner_test.go`
- Create: `agent/functions/gates/runner.go`
- Create: `agent/functions/gates/runner_test.go`
- Create: `agent/functions/judge/runner.go`
- Create: `agent/functions/judge/runner_test.go`
- Modify: `agent/harvest/workspace.go`
- Modify: `agent/harvest/gates.go`
- Modify: `agent/harvest/judge.go`
- Modify: `agent/harvest/runner.go`
- Modify: `atc/exec/harvest_step.go`
- Modify: `atc/exec/harvest_step_test.go`

- [ ] Write function tests showing repository validation consumes `repository-change/v1` and produces `validation-report/v1`; gates consume a repository state/change and produce `gate-results/v1`; judge consumes declared evidence and produces `measurements/v1`.
- [ ] Test timeout/retry taxonomy, deterministic gate command declarations, rubric version pinning, malformed inputs, and no ticket mutation or Git push.
- [ ] Run focused tests and confirm failure.
- [ ] Extract reusable Git helpers and evidence structs without changing compatibility harvest callers.
- [ ] Package each function as an ordinary task/agent runner contract usable from a v3 plan. Fixed command maps become authored task configuration or dev-mcp capability calls.
- [ ] Keep harvest as a composition adapter that calls the extracted functions only for legacy runs.
- [ ] Re-run tests and commit `refactor(harvest): expose validation and evaluation functions`.

### Task 9: Implement explicit repository and work-item publishers

**Files:**
- Create: `agent/publisher/types.go`
- Create: `agent/publisher/store.go`
- Create: `agent/publisher/git.go`
- Create: `agent/publisher/git_test.go`
- Create: `agent/publisher/workitem.go`
- Create: `agent/publisher/workitem_test.go`
- Create: `atc/db/migration/migrations/1773106108_create_agent_publications.up.sql`
- Create: `atc/db/migration/migrations/1773106108_create_agent_publications.down.sql`
- Create: `atc/db/agent_publications_factory.go`
- Create: `atc/db/agent_publications_factory_test.go`
- Create: `atc/steps_agent_publish.go`
- Create: `atc/exec/agent_publish_step.go`
- Create: `atc/exec/agent_publish_step_test.go`
- Modify: exhaustive ATC step visitors/planner/public plan/factories
- Modify: `agent/workflow/typecheck.go`
- Modify: `agent/workflow/typecheck_test.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] Write idempotency tests for push branch, open/update PR, merge, and work-item comment/state update keyed by the canonical semantic operation hash, proving mode/config changes produce distinct operations.
- [ ] Test authorization, v3 type-flow rejection for a missing or mismatched publisher input, explicit human approval requirement for merge, stale-base rejection, rebase-required result, external timeout, retry-safe response persistence, and credential scoping.
- [ ] Run focused tests and capture expected visitor failures.
- [ ] Add visible `publish_snapshot:` step with `publisher`, `input`, `input_type`, `destination`, `mode`, and approval policy. Extend the v3 type-checker to require the input artifact with that exact type through ordinary composition. The exec resolves a sealed snapshot ref from the artifact repository and never accepts untyped candidate bytes.
- [ ] Implement Git publisher modes `branch`, `pull-request`, and `merge`; implement generic work-item comment/state publisher behind an adapter interface. Persist request/result audit rows.
- [ ] Advance `jetbridgeHeadMigration` to `1773106108` and pass migration coverage.
- [ ] Update all exhaustive visitors and public plans; redact credentials/destination secrets.
- [ ] Re-run tests and commit `feat(agent): publish validated snapshots explicitly`.

### Task 10: Rebuild dogfood flows without implicit harvest

**Files:**
- Modify: `agent/workflow/seeds/small-fix-v3/workflow.yml`
- Modify: `agent/workflow/seeds/version-upgrade-v3/workflow.yml`
- Create: `agent/workflow/seeds/anonymization-audit-v3/workflow.yml`
- Create: `agent/workflow/seeds/anonymization-audit-v3/prompts/audit.md`
- Create: `agent/workflow/seeds/log-diagnosis-v3/workflow.yml`
- Create: `agent/workflow/seeds/log-diagnosis-v3/prompts/diagnose.md`
- Modify: `agent/workflow/seed_test.go`
- Create: `agent/dispatch/v3_dogfood_integration_test.go`

- [ ] Compose small-fix and version-upgrade from explicit implementation, validation, review/reducer, `await_snapshot:` human approval where configured, and publisher nodes.
- [ ] Define anonymization audit as `(repository/v1, database-snapshot/v1) -> (audit-findings/v1, optional repository-change/v1)` with no live database access.
- [ ] Define log diagnosis as `(log-bundle/v1, optional deployment-snapshot/v1) -> diagnosis/v1` with no repository/ticket requirement.
- [ ] Run seed and dogfood integration tests, including a review-only path that performs zero external writes.
- [ ] Disable compatibility harvest for v3 at both renderer and exec validation layers; retain v1/v2 behavior.
- [ ] Commit `feat(agent): dogfood explicit typed engineering workflows`.

### Task 11: Verify adapters and delivery

**Files:**
- Modify: `docs/superpowers/plans/2026-07-21-adapters-projections-and-delivery.md`

- [ ] Run formatting and generated fakes.
- [ ] Run `go test ./agent/workitem ./agent/resourcecapture ./agent/projection/... ./agent/publisher/... ./agent/workflowwait ./agent/api/workflowoutcomes ./agent/dispatch -count=1` and generated-fake checks.
- [ ] Run `ginkgo --focus='AgentTicketsFactory|AgentReviewsFactory|AgentRepositoryChangesFactory|AgentPublicationsFactory|AgentWorkflowOutcomesFactory|AgentWorkflowWaitsFactory|agent dispatch' ./atc/db/`; `make test-fly-integration`; and `make test-integration`.
- [ ] Manually inspect one v3 run plan to confirm every transformation and publisher is visible and no implicit harvest node exists.
- [ ] Mark completed checkboxes and commit `test(agent): verify adapters projections and explicit delivery`.
