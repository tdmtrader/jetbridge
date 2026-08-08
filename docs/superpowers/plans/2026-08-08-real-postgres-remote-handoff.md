# Remote handoff prompt: finish the real-PostgreSQL test conversion

Paste everything below this heading into a new Codex session running directly
on **theborg**.

---

Continue the Concourse real-PostgreSQL test-double conversion autonomously.
Work from the remote branch
`origin/claude/mock-testdouble-audit-b88f3f` in the `tdmtrader/jetbridge`
fork. Fetch that branch and create a dedicated worktree branch to track it. If
a same-named local branch already exists, inspect it before taking any action.
Do not disturb unrelated worktrees or user changes.

## Active goal (do not narrow it)

Finish converting Concourse test success-state database doubles to isolated
template-cloned PostgreSQL databases on one shared machine-wide PostgreSQL
service. Retain only justified algorithmic, timing, or fault-injection seams.
Verify every remaining requirement, commit incrementally, and do not claim
completion until a current whole-branch review and completion audit pass.

The user wants tests to be parallel-safe: one PostgreSQL server, one migrated
template per suite process, and a uniquely named clone per spec/Ginkgo node.
Independent clones must be able to run concurrently.

Proceed without stopping for ordinary implementation choices. Use subagents
for read-only census/review work, review their findings critically, and commit
as you go. Never push to `upstream`; continue on the handoff branch.

## Read first

1. `AGENTS.md`
2. `CLAUDE.md`
3. This handoff document
4. The five modified closure plans named under “Remaining work” below
5. `atc/postgresrunner/shared_server.go`, `atc/postgresrunner/ginkgo.go`,
   `atc/api/real_db_test.go`, and `hack/test-postgres-concurrency.sh`

## Important theborg environment constraints

This continuation runs **on theborg**, not on the Mac that produced the branch.
The Mac used a dedicated shared PostgreSQL service at
`127.0.0.1:15432`; that address is not evidence that theborg has the same
service. Start by running `make test-postgres-status` and inspecting the local
environment. Use or safely provision a dedicated test PostgreSQL service on
theborg, then set `CONCOURSE_TEST_POSTGRES_DSN` if it is not at the default
endpoint.

Do not point the test runner at the live Concourse database. The test role must
be PostgreSQL `SUPERUSER`: migrations create extensions, and the runner creates
and drops only its strictly validated `cc_tpl_*` / `cc_db_*` databases.

theborg hosts live Concourse and k3s/containerd. It has no Docker daemon on the
host; never install Docker there. Read `docs/docker-on-theborg.md` before any
Docker-related action. The remaining closeout should not require Docker or the
CI-only Kubernetes suites.

## Authoritative progress

The exact requested constructor pattern includes both
`new(dbfakes.Fake...)` and `&dbfakes.FakeX{}`, excluding
`bench`, `benchmark(s)`, and `corpus` paths.

- Baseline at merge base `57fae3a5fdba95c1d6507d4766386cd532da7429`:
  **606 constructors / 134 import files**.
- Current branch: **89 constructors / 33 import files**.
- Removed: **517 constructors and 101 import files** (about 85%).
- Current 89 reconcile exactly to **86 reviewed non-suite
  algorithmic/fault/timing seams plus 3 worker-suite bootstrap literals** in
  `atc/api/api_suite_test.go` (`FakeTeamFactory`, `FakeTeam`, and
  `FakeWorkerFactory`). An independent census review passed. Do not convert
  retained seams merely to make the number smaller; challenge any success-state
  database behavior that remains.

Recheck the current census with:

```bash
rg -o --no-filename --glob '*_test.go' \
  --glob '!**/bench/**' --glob '!**/benchmark/**' \
  --glob '!**/benchmarks/**' --glob '!**/corpus/**' \
  'new\(dbfakes\.Fake[[:alnum:]_]+\)|&dbfakes\.Fake[[:alnum:]_]+\{' . | wc -l

rg -l --glob '*_test.go' \
  --glob '!**/bench/**' --glob '!**/benchmark/**' \
  --glob '!**/benchmarks/**' --glob '!**/corpus/**' \
  '"github.com/concourse/concourse/atc/db/dbfakes"' . | wc -l
```

Expected: `89` and `33`. Splitting the first expression yields `86` `new(...)`
sites and `3` address literals.

## Key landed commits

- `d18d5baae3` — artifact repository API state
- `42d52d7ff3` — container API state
- `0ebaf2796a` — builds API state
- `f6ced51f7c` — accessor authorization state
- `20f7b7c4bb` — remaining team API state
- `d3b5617b1b` — remaining pipeline API state
- `5507ea7258` — agent API persistence and default-suite fake retirement
- `7f1bce2ff3` — external shared-service PostgreSQL workflow
- `5e4f903aba` — fail-closed default API database adapters
- `d5cf3deacd` — current environment/test prerequisite documentation
- `a8e33aedfa` — final-review harness hardening: authenticated/superuser status,
  exact per-suite application-name overrides, elapsed observation timeout,
  true process-group cleanup, catalog-attributed simultaneous clones, retired
  local Colima launcher, and actual default-backend wiring regression

Earlier commits on the branch contain the preceding conversion waves and their
plans. Inspect `git log --oneline 57fae3a5fd..HEAD` rather than recreating them.

## Verification already completed

Before the handoff commit, all of the following passed on the Mac’s dedicated
shared service:

- Full `atc/api`: **825/825** serial and **825/825** with 9 processes, repeated
  after the fail-closed correction.
- `go test ./atc/api`.
- `go vet ./atc/api ./atc/postgresrunner`.
- `go test ./atc/postgresrunner -count=1`.
- `make test-integration`: **24/24**.
- `make test-fly-integration`: **680/680**.
- All recorded batch focuses in both serial and 9-process modes: Artifacts 18,
  Containers 47, Builds 95, Accessor/full API focuses, Teams 33, Pipelines 122,
  and combined scopes documented in the plans.
- `hack/test-postgres_test.sh` and the strengthened
  `hack/test-postgres-concurrency_test.sh` signal/process-tree regression.
- Live `make --always-make test-postgres-concurrency` after the final harness
  fix, plus a second live run whose keyword DSN deliberately contained
  `application_name=wrong`; both observed two simultaneous distinct clones and
  passed.
- `make test-postgres-status` authenticated successfully and proved the role
  was a superuser.
- Final exact census: 89 / 33, reconciled 86 + 3.

`make test-unit` ran all 155 suites in 29m48s and exited 2 only because the one
migration suite has the seven predeclared branch-baseline failures: expected
head `1773106160`, embedded/preflight migrations at `1773106159`. The other 154
suites passed. Do not report this command as green, and do not “fix” that
unrelated migration head as part of this goal without new evidence.

## Final-review history

Independent reviewers already passed the exact census and found no missing
success-state conversion. Whole-branch review then found closeout issues rather
than new conversion targets:

- local Colima/KinD instructions contradicted current repository policy;
- readiness checked only `pg_isready`, not actual auth/superuser capability;
- an explicit DSN `application_name` could override suite attribution;
- cleanup killed only the Ginkgo leader, not its process group;
- polling counted fast attempts rather than elapsed time;
- the API regression instantiated an unavailable backend directly instead of
  checking `fakeDBDeps().workflowRuns`;
- two small `TESTING.md`/plan statements were stale.

Commit `a8e33aedfa` addresses every item. Its unit fixture has a TERM-catching
Ginkgo leader and compiled-test grandchild, waits for the grace interval, and
proves both are gone. The live barrier maps each unique application name to its
validated generated run ID and then uses one `pg_database` snapshot to prove
two clones existed simultaneously. Both default and conflicting-keyword DSN
live runs passed. The original reviewers’ remote streams disconnected before
they could issue a final PASS, so obtain a fresh read-only whole-branch review
of current HEAD rather than assuming closure.

## Remaining work

1. Verify the fetched branch is clean and review the handoff commits/diffs.
2. Establish the dedicated shared PostgreSQL endpoint on theborg and rerun at
   least:

   ```bash
   bash hack/test-postgres_test.sh
   bash hack/test-postgres-concurrency_test.sh
   make test-postgres-status
   go test ./atc/postgresrunner -count=1
   go test ./atc/api -run '^TestDefaultAPIDatabaseDepsFailClosed$' -count=1
   make --always-make test-postgres-concurrency
   go vet ./atc/api ./atc/postgresrunner
   git diff --check
   ```

   If code changes beyond tests/docs, rerun the affected package focus in
   serial and 9-process modes and expand verification proportionally. Do not
   rerun expensive broad gates merely for unchanged documentation, but do not
   reuse Mac runtime evidence to claim theborg’s service works.
3. Ask a fresh subagent for a read-only whole-branch review from merge base
   `57fae3a5fd` through HEAD, explicitly checking the 89-site reconciliation,
   runner parallel isolation, cleanup on signals, DSN handling, fail-closed
   defaults, and documentation accuracy. Fix real findings and retest.
4. Finalize these five closure records, which intentionally still say final
   review is pending and leave unperformed mutation-only/RED or per-task commit
   checkpoints open:

   - `docs/superpowers/plans/2026-08-07-real-postgres-api-artifacts-containers.md`
   - `docs/superpowers/plans/2026-08-07-real-postgres-api-builds.md`
   - `docs/superpowers/plans/2026-08-07-real-postgres-accessor-teams.md`
   - `docs/superpowers/plans/2026-08-07-real-postgres-api-agent-cleanup.md`
   - `docs/superpowers/plans/2026-08-07-real-postgres-api-team-pipeline-seams.md`

   Keep the records honest: completed implementation/runtime gates can be
   checked; unrecorded mutation-only sensitivity checks, missing historical
   RED evidence, and prescribed per-task commit granularity stay open. Replace
   “review pending” only after a current reviewer passes. Run a final docs QA
   subagent and commit the closure records.
5. Before declaring the goal complete, prove:

   - current census is still 89 / 33 and every site reconciles to 86 + 3;
   - no generated test databases/templates from dead processes remain (inspect
     first; only drop names that the runner’s strict generated-name validators
     and dead-process evidence make safe);
   - shared PostgreSQL remains ready;
   - `git diff --check` passes and the worktree is clean;
   - the final reviewer and docs reviewer pass;
   - all required fixes/docs are committed.

Do not push to `upstream`, do not install Docker on theborg, do not touch the
live Concourse PostgreSQL data, and do not mark the goal complete merely because
the conversion count is low. Finish the requirement-by-requirement audit, then
report the 517/606 reduction, retained 86 + 3 seam rationale, verification
matrix, exact known baseline failure, commits, and whether the branch was
pushed.

---
