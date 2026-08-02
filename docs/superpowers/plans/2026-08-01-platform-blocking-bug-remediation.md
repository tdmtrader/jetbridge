# Platform Blocking Bug Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair three independently reproduced data-integrity, credential-disclosure, and release-concurrency bugs found by the 2026-08-01 blocking-bug audit.

**Architecture:** Keep each correction at the boundary where the defect originates. Snapshot revival remains digest-coordinated but becomes team-scoped, secret tracing is rejected by a parsed-pipeline regression, and publishing `main` moves into a small fail-closed Git helper exercised against a real bare remote.

**Tech Stack:** Go 1.25, Ginkgo, PostgreSQL, POSIX shell, Git, Concourse pipeline YAML.

## Global Constraints

- Work only in `.worktrees/agentic-platform-rebase` on `codex/agentic-platform-rebase`; preserve all pre-existing dirty ledger and findings edits.
- Fix only the three reproduced blocking bugs. Do not add deployment features, refactor adjacent release logic, or reopen accepted semantic-rebase tasks without new evidence.
- PostgreSQL-backed tests run serially. Use task-specific `/tmp` Go caches and do not interfere with the separate unit suite already running in another worktree.
- Every production correction starts from a regression that fails for the observed defect, then receives focused verification, `git diff --check`, and one blocking-only review.
- Do not push, merge, open a pull request, deploy, release, or mutate external home-infra.

---

### Task 1: Keep expired snapshot revival inside the sealing team

**Files:**

- Modify: `atc/db/agent_snapshots_factory_test.go`
- Modify: `atc/db/agent_snapshots_factory.go`
- Modify: `.superpowers/sdd/2026-08-01-platform-blocking-bug-remediation/progress.md`

**Interfaces:**

- Consumes: `snapshot.SealCommit.Context.TeamID` as the direct manifest owner established by migration `1773106139`.
- Preserves: digest-global location storage and same-team revival of every expired manifest sibling for the committed digest.
- Produces: no state transition on another team's independently owned equal-digest manifest.

- [ ] **Step 1: Add the cross-team revival regression**

  Extend the direct-team-ownership context in `agent_snapshots_factory_test.go` with two team-owned manifests for the same digest. Set both manifests to `expired`, reseal the digest only for `defaultTeam`, and assert by exact snapshot ID that the default-team row is `available`, the other-team row remains `expired`, and `GetAuthorized` does not expose the expired other-team row.

  The load-bearing assertions are:

  ```go
  var defaultState, otherState string
  Expect(dbConn.QueryRow(`SELECT content_state FROM agent_snapshots WHERE id = $1`, int64(ref.ID)).Scan(&defaultState)).To(Succeed())
  Expect(dbConn.QueryRow(`SELECT content_state FROM agent_snapshots WHERE id = $1`, int64(otherRef.ID)).Scan(&otherState)).To(Succeed())
  Expect(defaultState).To(Equal("available"))
  Expect(otherState).To(Equal("expired"))
  _, found, err = factory.GetAuthorized(ctx, other.ID(), otherRef.ID)
  Expect(err).NotTo(HaveOccurred())
  Expect(found).To(BeFalse())
  ```

- [ ] **Step 2: Run RED**

  Run serially:

  ```bash
  GOCACHE=/tmp/jetbridge-blocking-bugs-go-cache ginkgo --procs=1 --focus='does not revive another team' ./atc/db
  ```

  Expected: the other team's row is `available` because `CommitSealBatch` currently updates expired rows by digest alone.

- [ ] **Step 3: Scope revival to the sealing team**

  Change the revival query in `CommitSealBatch` to:

  ```sql
  UPDATE agent_snapshots SET content_state = 'available'
  WHERE digest = $1 AND team_id = $2 AND content_state = 'expired'
  ```

  Pass `digest.String()` and `commit.Context.TeamID`. Do not change digest locking, locations, retention, or same-team sibling behavior.

- [ ] **Step 4: Run GREEN and the neighboring lifecycle specs**

  ```bash
  GOCACHE=/tmp/jetbridge-blocking-bugs-go-cache ginkgo --procs=1 --focus='expires only after effective retention|direct team ownership|does not revive another team' ./atc/db
  git diff --check
  ```

### Task 2: Prevent pipeline xtrace from logging Git credentials

**Files:**

- Modify: `deploy/pipeline_secret_trace_test.go`
- Modify: `deploy/concourse-pipeline.yml`
- Modify: `deploy/borg-pipeline.yml`
- Modify: `.superpowers/sdd/2026-08-01-platform-blocking-bug-remediation/progress.md`

**Interfaces:**

- Consumes: parsed task params and shell scripts from both deployment pipelines.
- Produces: a regression that follows shell xtrace state and rejects expansion of token/password/secret params while tracing is enabled.
- Preserves: safe command logging and existing explicitly bounded `set +x` credential consumers.

- [ ] **Step 1: Add a flow-sensitive secret-trace regression**

  Add `TestPipelineTasksDoNotTraceParameterizedSecrets`. For every task in both pipelines, collect config-param names containing `TOKEN`, `PASSWORD`, or `SECRET`; initialize trace state from the shell arguments; walk script lines in order; apply `set +x` before checking a line, reject `$NAME` or `${NAME...}` expansion while trace is active, and apply `set -x` after checking a line. The current test must identify `tag-rc/create-rc-tag` and `promote-to-main/push-to-main`.

- [ ] **Step 2: Run RED**

  ```bash
  GOCACHE=/tmp/jetbridge-blocking-bugs-go-cache go test ./deploy -run '^TestPipelineTasksDoNotTraceParameterizedSecrets$' -count=1
  ```

  Expected: both authenticated `git remote set-url` lines are reported because their tasks start with `sh -exc`.

- [ ] **Step 3: Disable xtrace for the two credential-bearing Git tasks**

  Change only `create-rc-tag` and Borg `push-to-main` from `sh -exc` to `sh -ec`. Keep `errexit`, the current Git commands, and all credential interpolation unchanged; the correction is that the shell never echoes those expanded commands.

- [ ] **Step 4: Run GREEN and the existing token policy**

  ```bash
  GOCACHE=/tmp/jetbridge-blocking-bugs-go-cache go test ./deploy -run '^TestPipelineTasksDoNotTrace(ServiceAccountTokens|ParameterizedSecrets)$' -count=1
  git diff --check
  ```

### Task 3: Make release publication fail closed on concurrent `main`

**Files:**

- Create: `deploy/push-jetbridge-main.sh`
- Create: `deploy/push_jetbridge_main_test.go`
- Modify: `deploy/concourse-pipeline.yml`
- Modify: `deploy/concourse_pipeline_release_test.go`
- Modify: `.superpowers/sdd/2026-08-01-platform-blocking-bug-remediation/progress.md`

**Interfaces:**

- Produces: `deploy/push-jetbridge-main.sh [REPOSITORY_DIR]`, defaulting to `.`, fetching `origin/main`, requiring the fetched remote main to be an ancestor of `HEAD`, and publishing with an ordinary non-force push.
- Preserves: release ordering—image and final tag succeed before `main` publication, and deployment begins only after publication succeeds.
- Rejects: a divergent or concurrently advanced `main` without rewriting remote history.

- [ ] **Step 1: Add real-Git RED regressions**

  In `push_jetbridge_main_test.go`, create a bare remote and working clones. Cover:

  - fast-forward publication from `M0` to release commit `R` succeeds and remote `main` becomes `R`;
  - after a second clone advances `main` from `M0` to sibling `M1`, publishing `R` fails and remote `main` remains `M1`.

  Add a parsed release-pipeline assertion that the release task calls `sh deploy/push-jetbridge-main.sh .` after the final tag push, contains no `git push --force` or `git push -f` for `main`, and reaches deployment only after the helper.

- [ ] **Step 2: Run RED**

  ```bash
  GOCACHE=/tmp/jetbridge-blocking-bugs-go-cache go test ./deploy -run '^(TestPushJetbridgeMain|TestConcourseReleasePublishesMainFailClosed)$' -count=1
  ```

  Expected: the helper is absent and the release task still contains an unconditional force-push.

- [ ] **Step 3: Implement the bounded helper and wire it into release**

  The helper must use:

  ```sh
  #!/bin/sh
  set -eu
  repo=${1-.}
  git -C "$repo" rev-parse --is-inside-work-tree | grep -Fx true >/dev/null
  git -C "$repo" fetch origin main
  git -C "$repo" merge-base --is-ancestor refs/remotes/origin/main HEAD || {
    echo 'FATAL: origin/main is not an ancestor of the release commit' >&2
    exit 1
  }
  git -C "$repo" push origin HEAD:refs/heads/main
  ```

  Replace the release task's force-push with `sh deploy/push-jetbridge-main.sh .`. Do not add force-with-lease, merge, rebase, or automatic conflict resolution.

- [ ] **Step 4: Run GREEN and complete deployment verification**

  ```bash
  sh -n deploy/push-jetbridge-main.sh
  GOCACHE=/tmp/jetbridge-blocking-bugs-go-cache go test ./deploy -count=1
  helm lint deploy/chart
  git diff --check
  ```

## Final Review

- [ ] Run one blocking-only review over the exact remediation delta.
- [ ] Record verification and review evidence in `.superpowers/sdd/2026-08-01-platform-blocking-bug-remediation/progress.md`.
- [ ] Update the semantic-rebase deferred catalog only for intentionally deferred nonblocking findings; do not add the three fixed blockers there.
