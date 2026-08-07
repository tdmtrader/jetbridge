# Next Real PostgreSQL Test-Double Conversions

**Goal:** Replace broad database/domain fakes in the next two bounded targets with behavior observed through each suite's isolated PostgreSQL clone, while retaining only method-specific fault injectors that a real connection cannot express.

**Scope:** `atc/gc/workflow_run_template_collector_test.go` and the resource-type branch of `atc/db/check_factory_test.go`. Do not change production behavior unless a real-database test proves an existing contract is impossible or incorrect. Do not push.

**Reviewed design decisions:** The GC fixture design was independently audited and corrected to rebuild repositories from the per-spec `dbConn`, construct fully eligible retired candidates, use PostgreSQL-relative timestamps, and preserve exact duration/order assertions in the two inline error fakes. The `atc/db` fake audit identified the resource-type branch as a bounded target whose in-memory success contract is impossible in production.

## Task 1: Convert workflow-run template collector success paths

**File:** `atc/gc/workflow_run_template_collector_test.go`

- [ ] Recreate `db.NewWorkflowRunTemplateLifecycle(dbConn)` and `db.NewWorkflowRunTemplateFactory(dbConn, lockFactory)` in the local `BeforeEach`; never retain either across specs.
- [ ] Add helpers adapted from `atc/db/workflow_run_template_lifecycle_test.go` to save an owned template, backdate `agent_workflow_run_templates.created_at` with PostgreSQL `now()`, query pipeline existence, create superseded workflow definitions, and create an archived terminal pipeline run plus a complete terminal durable citation. Multiple candidates in one spec must use unique template names and team idempotency keys, and either deliberately share one pre-created definition lineage or use unique definition names/versions that satisfy the schema's one-live-version constraint.
- [ ] Prove the abandoned pass through outcomes: a two-hour-old unexecuted owned template is deleted and a thirty-minute-old peer remains.
- [ ] Prove the retired pass through otherwise-identical eligible outcomes: a 31-day-old completed run/template/instance is deleted and a 29-day-old peer remains. The expired deletion is the control proving the shared fixture satisfies every retirement guard. Keep durable-run `created_at`/`completed_at` internally ordered to satisfy `completed_at >= created_at`; backdate `pipeline_runs.completed_at` independently because it is the retirement predicate.
- [ ] Prove retirement `0` still removes an abandoned template but retains a fully eligible 31-day retired candidate created by the same helper.
- [ ] Retain exactly two inline `FakeWorkflowRunTemplateLifecycle` values for deterministic abandoned- and retired-method errors. Comment the fault-injection reason. Assert exact grace/retirement forwarding, valid batch bounds, pass order, and short-circuit behavior there.
- [ ] Sensitivity-check at least one success assertion using test code only: temporarily invert/remove the new database-outcome expectation or alter only the fixture age, observe the focused spec fail, then restore the test. Never edit production SQL or collector logic for this check.
- [ ] Run the focused collector specs, then `ginkgo --no-color ./atc/gc/`, `git diff --check`, and commit with a message explaining which database outcome replaces the interaction assertions.

## Task 2: Correct the resource-type check factory contract

**File:** `atc/db/check_factory_test.go`

- [ ] Replace the resource-type branch's fake checkable with `defaultResourceType` and the real `defaultPipeline.ResourceTypes()` collection. Keep the broad resource branch unchanged in this task.
- [ ] For `toDB=true`, assert a real started check build is returned; call `build.Reload()` (or freshly retrieve it) and require `found=true` before asserting persisted `resource_type_id`, started/incomplete state, `manually_triggered=false`, and a private check plan containing the real name/type/source/from-version/default interval/resource-type/type-image fields. Query `SELECT count(*) FROM builds WHERE resource_type_id=$1 AND completed=false` and require one row. Call `TryCreateCheck` a second time, assert `(nil, false, nil)`, and prove the unfinished count remains exactly one.
- [ ] Replace the three fake-backed `toDB=false` success specs with one real contract assertion: build is nil, created is false, the exact production error states resource types do not support in-memory check builds, and `checkBuildChan` remains empty.
- [ ] Do not keep or add a fake for the impossible in-memory state. Leave other resource-branch fakes and sanctioned error injection outside this bounded context untouched.
- [ ] Sensitivity-check a representative persisted outcome or the no-enqueue assertion by changing only the test expectation/fixture, observe failure, restore it, then run the focused resource-type context, `ginkgo --no-color ./atc/db/`, `git diff --check`, and commit with a message explaining the impossible fake state removed.

## Task 3: Review and final verification

- [ ] Request independent review of each task's commit range; resolve all Critical and Important findings and re-run directly affected tests.
- [ ] Count remaining `dbfakes` test files and report retained fakes by category rather than claiming zero.
- [ ] Run `./hack/test-postgres.sh status`, `make test-unit`, `make test-integration`, `make test-fly-integration`, `git diff --check`, and `git status --short` with fresh output. Classify only the seven baseline failures listed below; do not call a nonzero command passing. The first five fail because actual embedded HEAD `1773106159` differs from expected `1773106160`; the sixth has the same integer mismatch; the seventh expects `JETBRIDGE_VERSION=1773106160` in the preflight script:
  - `Legacy Database Upgrade Upgrading from v7.13 to JetBridge HEAD [It] preserves all pipeline data through the full migration`
  - `Legacy Database Upgrade Upgrading from v8.0.1 to JetBridge HEAD [It] preserves all pipeline data through the JetBridge-only migrations`
  - `Legacy Database Upgrade Upgrading from v8.0.1 to JetBridge HEAD [It] demotes a live legacy workflow and enforces v3-only liveness at HEAD`
  - `Legacy Database Upgrade Migration idempotency [It] is safe to call Up() when already at HEAD`
  - `Legacy Database Upgrade Migration rollback [It] can migrate back up after a rollback`
  - `Legacy Database Upgrade Pre-flight validation script [It] keeps fresh installs and exact 1773106138 upgrades pinned to the embedded JetBridge head`
  - `Legacy Database Upgrade Pre-flight validation script [It] targets the same migration as the JetBridge database head`
- [ ] Leave the shared `concourse-test-postgres` container running and do not push.

## Task 4: Remove database fakes from the next ten whole test files

**Reviewed payoff:** Two independent audits converged on this batch. Completing it removes `dbfakes` imports from ten product test files and 22 explicit fake-constructor sites without adding a production seam.

- [x] Add one `postgresrunner.GinkgoRunner` to the existing `atc/api/accessor` suite and a per-spec clone/open/close/drop helper. Convert the five exported-only agent role test files to `accessor_test` Ginkgo specs backed by persisted teams: child execution, snapshots, workflow outcomes, workflow runs, and workflow waits. Preserve database-free route-map assertions and use distinct persisted identities for role cases. Expected payoff: five files and seven static constructors (about 65 runtime allocations through loops).
- [x] Convert `atc/api/runs_test.go` to existing `useRealDB` fixtures for persisted workflow-run templates and pipeline runs. Retain only the non-database access seam. Expected payoff: one file and three constructors.
- [x] Convert `atc/api/users_test.go` to the existing real `UserFactory`; express the failure case with a closed clone-local connection. Expected payoff: one file and one constructor.
- [x] Convert `atc/api/auth/web_auth_handler_test.go` to a persisted completed build and its real event source. Retain token middleware and nested HTTP handler fakes. Expected payoff: one file and two constructors.
- [x] Add per-spec clones to the syslog suite and convert `atc/syslog/drainer_test.go` to persisted builds/events plus the durable drained outcome. Expected payoff: one file and three constructors.
- [x] Add per-spec clones to the wrappa suite and convert `atc/wrappa/api_auth_wrappa_test.go` to persisted teams, pipelines, jobs, and builds. Retain accessor, auditor, and HTTP delegate seams. Expected payoff: one file and six constructors.
- [x] For every sub-batch: capture the focused baseline, make the new persisted outcome fail for the intended reason before implementation where practical, run focused parallel specs, remove the `dbfakes` import, run `git diff --check`, obtain independent review, and commit incrementally.
- [x] After all ten files, recount whole-file imports and explicit constructions against both merge base and pre-Task-4 HEAD; then re-run the directly affected packages concurrently against the one shared PostgreSQL instance.

## Task 5: Exploit the new harnesses and convert the next five whole files

**Reviewed payoff:** The accessor-specific audit recommended the first two follow-ups once its Ginkgo PostgreSQL harness existed. The repository-wide ranking recommended the resource-capture and access-token targets as the next bounded real-database conversions. Expected total: five more `dbfakes` imports and twelve explicit constructor sites removed.

- [ ] Convert `atc/api/accessor/accessor_factory_test.go` to persisted teams for the valid-token success path while retaining token-verifier, error-only team-fetcher, and display-ID seams. Expected payoff: one file and one `FakeTeam` constructor.
- [ ] Convert `atc/api/accessor/teams_cacher_test.go` to a real `TeamFactory`: cache one persisted team, create another, prove it stays hidden until the retained fake notification invalidates the cache, then prove the refreshed result. Expected payoff: one file, one `FakeTeam` and one `FakeTeamFactory` constructor.
- [ ] Convert `agent/resourcecapture/atc_resolver_test.go` and `agent/resourcecapture/template_store_test.go` with one `postgresrunner.StandardTestRunner`, persisted team/pipeline/resource/version/template ownership, and retained resolver/execution/output fault seams. Expected payoff: two files and seven constructors.
- [ ] Convert `skymarshal/token/access_token_test.go` to real access-token and user factories over per-test clones; express store failures through separately closed clone-local connections while retaining generator, claims-parser, and display-ID seams. Expected payoff: one file and two static constructors (about fourteen table-expanded allocations).
- [ ] Run focused red/green and sensitivity checks, package-parallel verification where supported, independent review of every sub-batch, exact recount from the Task 5 start, and incremental commits.
