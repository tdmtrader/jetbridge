# Zero Mocks Plan Set Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every generated and project-owned interaction mock while preserving externally meaningful behavior and keeping the affected test tiers deterministic and fast.

**Architecture:** Execute seven independently reviewable subsystem plans in dependency order, then apply a repository-wide semantic audit and architecture guard. Production-only interfaces become concrete dependencies; the few deterministic lifecycle/concurrency seams become named function types that expose outcomes rather than generic call-recording APIs.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, PostgreSQL advisory locks and fixtures, `net/http/httptest`, Kubernetes client-go in-memory API, Go AST/package enumeration, Make.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Zero means no generated mocks and no project-owned substitutes configured per method or used primarily for call-count, captured-argument, or collaborator-call assertions.
- Retain deterministic in-memory models, controlled clocks, protocol servers, the mock-resource image, and small channel-gated functions/scripted steps only when assertions use outputs, persisted state, protocol messages, or externally observable timing.
- Preserve likely user/operator failures; do not preserve improbable injected database, planner, starter, or panic failures solely for line coverage.
- Map each removed interaction assertion to an existing or replacement behavioral scenario before deletion.
- Run database-backed packages with `ginkgo`, never concurrent plain `go test ./...`.
- Do not run the K3s tier locally on macOS; record it as a CI gate.
- Every source-tree guard must prove that it inspected a substantial non-zero set without asserting an exact repository count.
- Exclude nested `.worktrees` and `.claude/worktrees` from inventories and leave `AUDIT-2026-08-11.md`, `FAKES-REVIEW-2026-08-11.md`, and unrelated worktree changes untouched.
- Record clean before-and-after wall time for every affected package suite; investigate any unexplained material regression without deleting meaningful behavioral protection.

---

### Task 1: Establish Reproducible Baselines

**Files:**
- Read: `CLAUDE.md`
- Read: `Makefile`
- Read: `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`
- Create locally only: `/private/tmp/jetbridge-zero-mocks-baseline/`

**Interfaces:**
- Consumes: the repository's documented Ginkgo unit runner and the approved mock definition.
- Produces: one wall-time measurement per affected suite and a tracked-file inventory used by all child plans.

- [ ] **Step 1: Create an out-of-tree evidence directory**

Run: `mkdir -p /private/tmp/jetbridge-zero-mocks-baseline`

Expected: the directory exists and `git status --short` is unchanged.

- [ ] **Step 2: Capture the generated-mock inventory without honoring `atc/.ignore`**

Run:

```bash
git grep -n -i -E 'counterfeiter(:generate)?|package .*fakes' -- '*.go' '*.md' go.mod go.sum tools.go CONTRIBUTING.md ':(exclude)docs/superpowers/**'
```

Expected: the output identifies eight live directives and eight generated files (their headers and package declarations) in `accessorfakes`, `lockfakes`, `enginefakes`, and `schedulerfakes`, plus tooling/dependency/documentation references. Approved specs/plans are excluded because they intentionally name the estate; `git grep` does not inspect the two untracked review documents.

- [ ] **Step 3: Measure each affected suite serially**

Run each command separately and record the `real` value in `/private/tmp/jetbridge-zero-mocks-baseline/times.txt`:

```bash
/usr/bin/time -p ginkgo ./atc/db/lock
/usr/bin/time -p ginkgo ./atc/engine
/usr/bin/time -p ginkgo ./atc/exec
/usr/bin/time -p ginkgo ./atc/scheduler
/usr/bin/time -p ginkgo ./atc/api
/usr/bin/time -p ginkgo ./atc/api/accessor
/usr/bin/time -p ginkgo ./atc/creds
/usr/bin/time -p ginkgo ./atc/creds/conjur
/usr/bin/time -p ginkgo ./atc/db
/usr/bin/time -p ginkgo ./atc/lidar
/usr/bin/time -p ginkgo ./atc/component
/usr/bin/time -p ginkgo ./atc/worker/jetbridge
/usr/bin/time -p go test ./vars -count=1
/usr/bin/time -p go test ./cmd -count=1
/usr/bin/time -p go test ./cmd/artifact-daemon -count=1
```

Expected: every suite passes. If an existing failure appears, stop implementation of that subsystem and document the exact failure before changing its code.

- [ ] **Step 4: Record the starting worktree**

Run: `git status --short`

Expected: only the two pre-existing untracked review documents are present.

### Task 2: Execute the Subsystem Plans in Dependency Order

**Files:**
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-lock.md`
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-engine-exec.md`
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-scheduler.md`
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-api-access.md`
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-supporting-fixtures.md`
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-kubernetes-runtime.md`
- Read and execute: `docs/superpowers/plans/2026-08-14-zero-mocks-guard-cleanup.md`

**Interfaces:**
- Consumes: the baseline evidence from Task 1.
- Produces: a mock-free source tree with a permanent structural guard.

- [ ] **Step 1: Execute the lock plan and stop at its green commit**

Expected commit subject: `test(lock): replace fake database with real concurrency`

- [ ] **Step 2: Execute the engine/exec plan and stop at its green commits**

Expected final commit subject: `test(exec): remove interaction-style fixtures`

- [ ] **Step 3: Execute the scheduler plan and stop at its green commits**

Expected final commit subject: `test(scheduler): replace mocks with persisted outcomes`

- [ ] **Step 4: Execute the API access plan and stop at its green commits**

Expected final commit subject: `test(api): exercise real route authorization`

- [ ] **Step 5: Execute the supporting-fixtures plan and stop at its green commits**

Expected final commit subject: `test: replace service mocks with real protocols`

- [ ] **Step 6: Execute the Kubernetes-runtime plan and stop at its green commits**

Expected final commit subject: `test(jetbridge): remove injected Kubernetes API failures`

- [ ] **Step 7: Execute the semantic audit, guard, and tooling cleanup plan**

Expected final commit subject: `build: remove counterfeiter tooling`

### Task 3: Prove Completion and Runtime

**Files:**
- Modify only if evidence requires it: the files named by a failing child-plan checkpoint.
- Create locally only: `/private/tmp/jetbridge-zero-mocks-final/`

**Interfaces:**
- Consumes: all seven completed child plans.
- Produces: final test, search, runtime, and CI evidence for the zero-mocks completion criteria.

- [ ] **Step 1: Create the final evidence directory and run the architecture guard independently**

Run:

```bash
mkdir -p /private/tmp/jetbridge-zero-mocks-final
go test . -run TestNoInteractionMocks -count=1
```

Expected: PASS and the test reports no banned directive, dependency, package, generated header, or spy-shaped declaration.

- [ ] **Step 2: Run the complete unit tier through its supported runner**

Run: `make test-unit`

Expected: PASS. Do not substitute `go test ./...` because database-backed packages must receive distinct Ginkgo process indexes.

- [ ] **Step 3: Re-measure affected suites serially**

Run every `/usr/bin/time -p ...` command from Task 1 and save its `real` value under the same package key in `/private/tmp/jetbridge-zero-mocks-final/times.txt`.

Expected: no unexplained material regression. Record an explanation for any increase large enough to exceed normal run-to-run noise.

- [ ] **Step 4: Verify formatting, buildability, and worktree scope**

Run:

```bash
git ls-files -z '*.go' > /private/tmp/jetbridge-zero-mocks-final/tracked-go-files
test -s /private/tmp/jetbridge-zero-mocks-final/tracked-go-files
xargs -0 gofmt -l < /private/tmp/jetbridge-zero-mocks-final/tracked-go-files > /private/tmp/jetbridge-zero-mocks-final/unformatted-go-files
test ! -s /private/tmp/jetbridge-zero-mocks-final/unformatted-go-files
go vet ./...
go build ./...
git diff --check
git status --short
```

Expected: all tracked Go files are formatted, vet/build/diff checks pass, and the two pre-existing untracked review documents remain untouched.

- [ ] **Step 5: Record the CI-only behavioral gate**

Run in CI, not on local macOS: the repository's documented K3s behavioral tier covering exec step composition and Kubernetes worker behavior.

Expected: PASS before the zero-mocks branch is merged.

- [ ] **Step 6: Commit any evidence-driven fixes only after rerunning their owning checkpoint**

Stage the exact files named by the failed checkpoint with explicit per-file `git add` arguments, then run:

```bash
git commit -m "test: complete zero-mocks verification"
```

Expected: no commit is created when verification required no code changes.
