# WS1 — CI Executes What Exists: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make CI actually execute every test suite that already exists and passes locally — the Postgres-backed `atc/db` / `atc/gc` / `atc/integration` suites, the separate-module `agent/schema` and `ci-agent` trees, `make test-dev-mcp`, and a new `agent/` race lane — so the release-gating pipeline (`deploy/concourse-pipeline.yml`) and the dogfood gate (`deploy/dogfood-pipeline.yml`) can no longer go green while a regression sits in a path CI never touches, and so `make test-all` runs the full local surface a human is told it covers.

**Architecture:** No new Go packages and no production behavior changes. This plan edits two live Concourse pipelines (`deploy/concourse-pipeline.yml`, `deploy/dogfood-pipeline.yml`), the CI task rootfs image (`deploy/Dockerfile.test-runner`), the root `Makefile`, one Go test file's skip-gate (`agent/devmcp/e2e/e2e_test.go`), and three docs files (`TESTING.md`, `CLAUDE.md`, `ci/dogfood/README.md`). The Postgres-backed suites need no bespoke database-bootstrap script: `atc/postgresrunner.GinkgoRunner` already self-provisions a throwaway cluster per Ginkgo parallel process (`initdb`/`postgres` invoked directly via `exec.LookPath`, dropping privileges to a `postgres` OS user when running as root — see `atc/postgresrunner/postgresrunner.go` and `ginkgo.go`). The only gap is that the CI task image has neither binary nor that OS user. Adding `postgresql`/`postgresql-contrib` to the existing test-runner image closes it; nothing else about the suites changes.

**Tech Stack:** Concourse pipeline YAML, `sh` task scripts, Ginkgo v2 / plain `go test`, Debian `apt` inside `deploy/Dockerfile.test-runner` (`FROM golang:1.25-bookworm`), GNU Make, `fly validate-pipeline` for YAML verification (confirmed installed and working locally: `fly` 0.2.206).

## Global Constraints

- **Zero production code changes.** Every change in this plan is CI config, a Dockerfile, a Makefile, docs, or a test file's own skip-gate. No migration number is needed (unlike WS3/WS7) — do not reserve one.
- **Only two pipelines are "live" for this plan's purposes: `deploy/concourse-pipeline.yml` and `deploy/dogfood-pipeline.yml`.** Task 0 establishes (and this plan relies on) that `deploy/test-pipeline.yml` and `deploy/borg-pipeline.yml` are historical/superseded and get no parallel `db-tests` treatment. `deploy/k8s-e2e-pipeline.yml` is also out of scope: it already automates the K8s integration/behavioral tier (the audit's "bright spot") and its only `concourse-test-runner` reference is as a builder-image bootstrap for a different image (`kind-runner`), unrelated to unit/db tests.
- **Verify every YAML edit with `fly validate-pipeline -c <file>` before committing.** Confirmed working baseline (run during scouting for this plan): both `deploy/concourse-pipeline.yml` and `deploy/dogfood-pipeline.yml` currently print `looks good` / exit 0.
- **`make` is not confirmed present in the test-runner image.** No existing pipeline task invokes `make` (grep across `deploy/*.yml` during scouting found zero hits — only two comments that *mention* `make test-quick` as a human-run reference). Every new CI step in this plan therefore calls the underlying command directly (`go test ...`, `ginkgo ...`) instead of `make <target>`, so it works regardless of whether `make` is installed. The one exception is Task 9's fuzz-lane guard, which has no choice since the target it runs doesn't exist yet in this repo — that task documents the assumption explicitly.
- **This plan targets a subset of `agent/...`; it never touches `agent/schema` semantics.** `agent/schema` remains a separate module (`agent/schema/go.mod`) invisible to root `go list ./...`; nothing here makes it depend on the main module or vice versa.
- **No new third-party test dependencies.**
- **Shared-workspace caveat (observed while scouting this plan, worth leaving here for whoever implements it):** other agents may be concurrently editing this same working tree. A transient `open agent/snapshot/contracts/zz_scratch_X_test.go: no such file or directory` build failure during a full `./agent/...` test run is a sibling process's untracked scratch file disappearing mid-run, not a real bug — it does not reproduce on a second run of the same command. Don't chase it; don't touch files named `zz_scratch_*` — they aren't yours.

---

### Task 0: Mark `test-pipeline.yml` and `borg-pipeline.yml` as historical

**Problem this closes:** the mission brief for this plan explicitly asks whether `deploy/test-pipeline.yml` and `deploy/borg-pipeline.yml` (which run the identical excluded-package `go test` script the audit flagged) should get the same `db-tests` treatment as the two live pipelines, or are dead. This task makes that determination explicit and permanent so nobody re-derives it later, and so the rest of this plan can say "the two live pipelines" without hedging.

**Files:**
- Modify: `deploy/test-pipeline.yml`
- Modify: `deploy/borg-pipeline.yml`

- [ ] Re-confirm the dates and messages this determination rests on:
  ```
  git log -1 --format="%ai %H %s" -- deploy/test-pipeline.yml
  git log -1 --format="%ai %H %s" -- deploy/borg-pipeline.yml
  git log -1 --format="%ai %H %s" -- deploy/concourse-pipeline.yml
  ```
  Expected (verified 2026-07-25, will only move forward in time as more commits land):
  ```
  2026-02-11 09:29:01 -0800 3b7495c17d1fbc32db7e2a3263fa3b2a84dfd655 fix(pipeline): use local registry for test-runner image
  2026-02-10 22:24:25 -0800 3bef0e5c8bc68ed1260544f7d15f098306c173f7 feat(pipeline): split into primary and agent pipelines
  2026-07-23 21:00:09 -0700 7ffe715a50bfd45069c98297eabcf15531701598 ci: use scoped credentials for agent publishing
  ```
  Both legacy files' last commits are from **2026-02**, five and a half months before `concourse-pipeline.yml`'s last commit. `borg-pipeline.yml`'s own last commit message says it was split **into** "primary and agent pipelines" — i.e. into `concourse-pipeline.yml` + `deploy/agent-pipeline.yml`. Corroborating structural evidence (already gathered, no need to re-derive): `test-pipeline.yml` triggers off a bare `time` resource (not the `repo` git resource) and its tasks `cd /src` — the image's baked-in `COPY . .` from `deploy/Dockerfile.test-runner` — instead of using a `get: repo` / `inputs: [repo]` overlay the way every job in `concourse-pipeline.yml` does. `borg-pipeline.yml` ends in a `promote-to-main` job that force-pushes `jetbridge` straight to `main` with no RC-tag stage at all, superseded by `concourse-pipeline.yml`'s `tag-rc` → `build-image` → `release` chain.
- [ ] Confirm nothing activates them:
  ```
  grep -rn "test-pipeline\.yml\|borg-pipeline\.yml" --include="*.sh" --include="*.md" --include="Makefile" .
  ```
  Expected: no hits outside the files' own content (verified during scouting for this plan — there is no committed `fly set-pipeline` script for *any* of the four `deploy/*-pipeline.yml` files in this repo; activation is a manual, out-of-band `fly set-pipeline` a human runs against the live cluster, per the operational notes already in `ci/dogfood/README.md`).
- [ ] At the very top of `deploy/test-pipeline.yml` (line 1, before the existing `resource_types:`), insert:
  ```yaml
  # HISTORICAL — superseded by deploy/concourse-pipeline.yml. This file predates
  # the `repo` git-resource-input pattern: it triggers off a bare `time`
  # resource and its tasks `cd /src` (the image's baked-in COPY . . from
  # deploy/Dockerfile.test-runner, refreshed only when concourse-test-runner
  # itself is rebuilt), and it has no tag-rc/build-image/release chain. Nothing
  # in this repo references or activates it (verified 2026-07-25: no
  # set-pipeline script, doc, or Makefile target names it). Kept for history
  # only. WS1 (docs/superpowers/plans/test-hardening/01-ci-execution.md)
  # deliberately does not add a db-tests job here — see that plan's Task 0.
  ```
- [ ] At the very top of `deploy/borg-pipeline.yml` (line 1, before the existing `resources:`), insert:
  ```yaml
  # HISTORICAL — superseded by deploy/concourse-pipeline.yml +
  # deploy/agent-pipeline.yml. Commit 3bef0e5c8b ("split into primary and agent
  # pipelines", 2026-02-10) split this monolith INTO those two files; this file
  # was never touched again (no dogfood job, no native agent-review step, no
  # db-tests, no RC-tag chain — it force-pushes straight to main via
  # promote-to-main). Nothing in this repo references or activates it (verified
  # 2026-07-25). Kept for history only. WS1
  # (docs/superpowers/plans/test-hardening/01-ci-execution.md) deliberately
  # does not add a db-tests job here — see that plan's Task 0.
  ```
- [ ] Run `fly validate-pipeline -c deploy/test-pipeline.yml` and `fly validate-pipeline -c deploy/borg-pipeline.yml`. Expected: both print `looks good` (a leading comment cannot affect validity; both files validated cleanly before this edit too).
- [ ] Commit `docs(ci): mark test-pipeline.yml and borg-pipeline.yml as superseded`.

---

### Task 1: Correct the stale "ginkgo skips plain-testing packages" claim

**Problem this closes:** the audit found this belief stated in two places (`Makefile`'s `test-dev-mcp` comment and the package comment in `agent/devmcp/e2e/e2e_test.go`) and verified it false. Getting this fact right matters for the rest of this plan: Task 5/6/7 below reason precisely about what already runs versus what needs a new step, and that reasoning depends on this being correct.

**Files:**
- Modify: `agent/devmcp/e2e/e2e_test.go`
- Modify: `Makefile`

- [ ] Reproduce the false claim directly. From the repo root:
  ```
  ginkgo -r --dry-run -v ./agent/devmcp/ 2>&1 | tail -40
  ```
  Expected (this is the actual output captured while scouting this plan, on commit `410d9b59f8`): the run ends with `Ginkgo ran 3 suites in ...` / `Test Suite Passed`. The first suite (`agent/devmcp` itself, which has a real `RunSpecs` bootstrap in `devmcp_suite_test.go`) shows all 8 specs at `[0.000 seconds]` because `--dry-run` short-circuits actual Ginkgo spec bodies. The other two — `agent/devmcp/contracttest` and `agent/devmcp/e2e`, **both plain `testing.T`, no `RunSpecs` anywhere in either package** — run for real: `TestFixtureRepoContract` takes ~2.5s, `TestGoClientRunTestsEndToEnd` takes ~1.2s, `TestLiveImageContract` self-skips via `t.Skip` (unrelated to ginkgo). `--dry-run` has no effect on them because ginkgo's CLI compiles and executes every package its recursive walk finds as an ordinary `go test` binary — whether or not that package happens to call `RunSpecs` is irrelevant to whether ginkgo's walk reaches it. This directly contradicts both comments quoted below.
- [ ] In `agent/devmcp/e2e/e2e_test.go`, change:
  ```go
  // Package e2e builds the real ci-agent/cmd/dev-mcp binary and proves the
  // whole stack: binary + config against the contract-test kit, and the Go
  // client path (the exact call path harvest-step will use).
  //
  // NOTE: this package has no Ginkgo suite, so `ginkgo -r` (make test-unit)
  // skips it — run it via `make test-dev-mcp` or `go test ./agent/devmcp/...`.
  package e2e_test
  ```
  to:
  ```go
  // Package e2e builds the real ci-agent/cmd/dev-mcp binary and proves the
  // whole stack: binary + config against the contract-test kit, and the Go
  // client path (the exact call path harvest-step will use).
  //
  // NOTE: contrary to an earlier belief recorded here, `ginkgo -r` (make
  // test-unit) DOES run this package. It has no Ginkgo suite bootstrap, but
  // ginkgo's CLI still builds and executes plain `Test*` functions in every
  // package its recursive walk finds, regardless of whether that package
  // calls RunSpecs (verified 2026-07-25: `ginkgo -r --dry-run
  // ./agent/devmcp/` runs this package's tests for real, with non-zero
  // durations, even though --dry-run no-ops the sibling agent/devmcp Ginkgo
  // suite's specs). `make test-dev-mcp` / `go test ./agent/devmcp/...`
  // remains the explicit, CI-wired way to run this package on its own — see
  // TESTING.md — it is not the only way it runs.
  package e2e_test
  ```
- [ ] In `Makefile`, change:
  ```makefile
  # dev-mcp contract kit + e2e (plain go tests; ginkgo -r does not pick these up)
  # Requires: nothing (builds ci-agent/cmd/dev-mcp on the fly)
  test-dev-mcp:
  	@echo "==> Running dev-mcp contract/e2e tests..."
  	go test ./agent/devmcp/... -count=1 -timeout 10m
  ```
  to:
  ```makefile
  # dev-mcp contract kit + e2e (plain go tests). NOTE: `ginkgo -r` (test-unit)
  # actually runs these too — see the corrected package comment in
  # agent/devmcp/e2e/e2e_test.go. This target exists as the explicit,
  # CI-wired way to run them on their own, not because ginkgo skips them.
  # Requires: nothing (builds ci-agent/cmd/dev-mcp on the fly)
  test-dev-mcp:
  	@echo "==> Running dev-mcp contract/e2e tests..."
  	go test ./agent/devmcp/... -count=1 -timeout 10m
  ```
- [ ] Run `go test ./agent/devmcp/... -count=1 -timeout 10m` and confirm every package still prints `ok` (this task only changes comments — behavior is unchanged).
- [ ] Commit `docs(testing): fix stale claim that ginkgo -r skips plain-testing packages`.

---

### Task 2: Add PostgreSQL to the test-runner image and a job to build it

**Problem this closes:** `db-tests` (Task 3/4) needs `initdb` and `postgres` binaries and a `postgres` OS user in the task's rootfs. `atc/postgresrunner.Runner.Run` (used by every Postgres-backed Ginkgo suite via `postgresrunner.GinkgoRunner`) calls `exec.LookPath("initdb")` / `exec.LookPath("postgres")` directly and does its own `initdb` + direct `postgres` invocation on an ephemeral port (`5433 + GinkgoParallelProcess()`) — it does **not** use `pg_ctlcluster`, a Debian cluster, or a pre-started system service. So the task script needs no database-bootstrap logic of its own; it only needs the image to provide those two binaries on `PATH` plus the `postgres` OS user Debian's package creates. No pipeline job currently builds `concourse-test-runner` at all (verified: `grep -rln "Dockerfile\.test-runner" .` returns nothing) — it is pushed to `registry.home` manually, out of band. This task fixes that gap the same way `build-agent-runner-image` already does for the agent-runner image: a manually-triggered pipeline job using the existing DinD-builder-pod pattern.

**Files:**
- Modify: `deploy/Dockerfile.test-runner`
- Modify: `deploy/concourse-pipeline.yml`

- [ ] Change `deploy/Dockerfile.test-runner` from:
  ```dockerfile
  # Build environment for Concourse CI pipeline tasks.
  # Contains Go toolchain, kubectl, docker CLI, unzip, and source code at /src.
  FROM golang:1.25-bookworm

  RUN apt-get update && apt-get install -y --no-install-recommends \
      curl \
      git \
      jq \
      unzip \
      && rm -rf /var/lib/apt/lists/*

  # Install kubectl
  RUN curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
      -o /usr/local/bin/kubectl && chmod +x /usr/local/bin/kubectl

  # Install Docker CLI (for build-image job that mounts Docker socket)
  RUN curl -fsSL https://download.docker.com/linux/static/stable/x86_64/docker-27.5.1.tgz \
      | tar xz --strip-components=1 -C /usr/local/bin docker/docker

  # Copy source and cache Go modules
  WORKDIR /src
  COPY go.mod go.sum ./
  RUN go mod download
  COPY . .
  ```
  to:
  ```dockerfile
  # Build environment for Concourse CI pipeline tasks.
  # Contains Go toolchain, kubectl, docker CLI, unzip, PostgreSQL, and source
  # code at /src.
  FROM golang:1.25-bookworm

  RUN apt-get update && apt-get install -y --no-install-recommends \
      curl \
      git \
      jq \
      unzip \
      && rm -rf /var/lib/apt/lists/*

  # Install kubectl
  RUN curl -fsSL "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
      -o /usr/local/bin/kubectl && chmod +x /usr/local/bin/kubectl

  # Install Docker CLI (for build-image job that mounts Docker socket)
  RUN curl -fsSL https://download.docker.com/linux/static/stable/x86_64/docker-27.5.1.tgz \
      | tar xz --strip-components=1 -C /usr/local/bin docker/docker

  # Install PostgreSQL (for the db-tests job). atc/postgresrunner.Runner.Run
  # calls exec.LookPath("initdb")/exec.LookPath("postgres") directly and
  # self-provisions an ephemeral cluster per Ginkgo parallel process — see
  # atc/postgresrunner/postgresrunner.go and ginkgo.go. It does NOT use
  # pg_ctlcluster or a pre-started system service, so we only need the
  # binaries + the "postgres" OS user the package creates, nothing more.
  # policy-rc.d stops the postinst script from trying (and possibly failing)
  # to start a system-wide cluster during the image build, which we don't want
  # and don't use.
  RUN echo "exit 101" > /usr/sbin/policy-rc.d && chmod +x /usr/sbin/policy-rc.d \
      && apt-get update && apt-get install -y --no-install-recommends \
      postgresql \
      postgresql-contrib \
      && rm -rf /var/lib/apt/lists/*

  # Debian's postgresql-common package keeps the versioned server binaries
  # (initdb, postgres, pg_ctl, ...) out of PATH, under
  # /usr/lib/postgresql/<version>/bin, so multiple major versions can coexist.
  # exec.LookPath needs them on PATH; symlink whichever single version apt
  # installed to a stable, version-independent path so this Dockerfile never
  # has to hardcode a PostgreSQL major version.
  RUN ln -s "$(find /usr/lib/postgresql -mindepth 2 -maxdepth 2 -type d -name bin | head -1)" /usr/lib/postgresql/current-bin
  ENV PATH="/usr/lib/postgresql/current-bin:${PATH}"

  # Copy source and cache Go modules
  WORKDIR /src
  COPY go.mod go.sum ./
  RUN go mod download
  COPY . .
  ```
- [ ] In `deploy/concourse-pipeline.yml`, immediately after the `build-agent-runner-image` job's last line (`echo "=== agent-runner image built and pushed: v${NEXT_VERSION} (${SHORT_SHA}) ==="`) and before `- name: self-upgrade`, insert a new job. This mirrors `build-agent-runner-image` exactly (same DinD-builder-pod pattern, same `docker:26-dind` pin and the reason for it), simplified because this image only needs `registry.home`, not GHCR:
  ```yaml
  # Builds the CI task rootfs image (deploy/Dockerfile.test-runner) used by
  # every job's rootfs_uri in this pipeline. Manual trigger, like
  # build-agent-runner-image: this image changes rarely and must not ride the
  # per-commit chain. Whenever Dockerfile.test-runner changes again, bump the
  # tag below AND every rootfs_uri that should pick up the change.
  - name: build-test-runner-image
    serial_groups: [pipeline]
    plan:
    - get: repo
      trigger: false
      params: {depth: 1}
      passed: [unit-tests]
    - task: build-and-push-test-runner
      privileged: true
      attempts: 2
      config:
        platform: linux
        rootfs_uri: docker:///registry.home/concourse-test-runner:v5
        inputs:
        - name: repo
        run:
          path: sh
          args:
          - -exc
          - |
            cd repo
            REGISTRY="registry-docker-registry.cicd.svc.cluster.local:5000"
            BUILDER_POD="test-runner-builder-$$"

            cleanup_builder() { kubectl delete pod -n cicd ${BUILDER_POD} --grace-period=0 --force 2>/dev/null || true; }
            trap cleanup_builder EXIT
            kubectl delete pod -n cicd -l app=test-runner-builder --grace-period=0 --force 2>/dev/null || true

            echo "=== Creating DinD builder pod ==="
            cat <<PODEOF | kubectl apply -n cicd -f -
            apiVersion: v1
            kind: Pod
            metadata:
              name: ${BUILDER_POD}
              namespace: cicd
              labels:
                app: test-runner-builder
            spec:
              containers:
              - name: dind
                # Pinned to 26.x (runc 1.1.x) — same reason as build-image's
                # identical pin: the rolling docker:dind tag's runc >=1.2 /proc
                # masking breaks on theborg's 5.4 kernel.
                image: docker:26-dind
                securityContext:
                  privileged: true
                env:
                - name: DOCKER_TLS_CERTDIR
                  value: ""
                command: ["dockerd", "--host=unix:///var/run/docker.sock", "--insecure-registry=${REGISTRY}", "--insecure-registry=registry.home"]
            PODEOF

            echo "Waiting for builder pod to be ready..."
            kubectl wait --for=condition=ready pod/${BUILDER_POD} -n cicd --timeout=120s

            echo "Waiting for Docker daemon..."
            for i in $(seq 1 30); do
              if kubectl exec -n cicd ${BUILDER_POD} -- docker info >/dev/null 2>&1; then
                echo "Docker daemon ready"
                break
              fi
              sleep 2
            done

            echo "=== Copying build context (repo, sans .git) to builder pod ==="
            tar czf /tmp/test-runner-src.tgz --exclude=.git .
            kubectl exec -n cicd ${BUILDER_POD} -- mkdir -p /tmp/src
            kubectl cp /tmp/test-runner-src.tgz cicd/${BUILDER_POD}:/tmp/test-runner-src.tgz
            kubectl exec -n cicd ${BUILDER_POD} -- tar xzf /tmp/test-runner-src.tgz -C /tmp/src

            echo "=== Building concourse-test-runner:v6 (adds PostgreSQL) ==="
            kubectl exec -n cicd ${BUILDER_POD} -- \
              docker build \
                -t registry.home/concourse-test-runner:v6 \
                -f /tmp/src/deploy/Dockerfile.test-runner /tmp/src

            echo "=== Pushing to local registry ==="
            kubectl exec -n cicd ${BUILDER_POD} -- docker push registry.home/concourse-test-runner:v6

            echo "=== concourse-test-runner:v6 built and pushed ==="
  ```
  Note this job's own `rootfs_uri` deliberately stays `:v5` — it has to run on an image that already exists to go build `:v6`.
- [ ] Run `fly validate-pipeline -c deploy/concourse-pipeline.yml`. Expected: `looks good`.
- [ ] **Deployment/operational note (not a local git step, but write it down so whoever deploys this doesn't get surprised):** after `fly set-pipeline`-ing this change to the live cluster, `registry.home/concourse-test-runner:v6` does not exist until a human runs `fly trigger-job -j <pipeline>/build-test-runner-image` and it succeeds. Nothing breaks in the meantime except `db-tests` (Task 3/4) failing to pull an image that isn't there yet — a clear, loud failure, not a silent one, and scoped to exactly the one new job, because no *existing* job's `rootfs_uri` was changed. Two outcomes once triggered: (a) it succeeds — proceed; (b) `apt-get install postgresql postgresql-contrib` fails or the build hangs — capture the exact apt error text from the build log. The `policy-rc.d` stub above is specifically there to prevent the well-known Debian-in-Docker failure mode where the postinst script tries to start a system service and the build hangs or errors; if the failure persists anyway, that is a blocking finding for this task — do not silently swap the base image or drop `postgresql-contrib` to route around it. Resolve the actual apt/Debian interaction (the error text will say what's wrong) before moving on to Task 3.
- [ ] Commit `ci(pipeline): add postgres to the test-runner image and a job to build it`.

---

### Task 3: Add the `db-tests` job to `concourse-pipeline.yml`

**Problem this closes:** WS1 decision 1. `atc/db`, `atc/gc`, and `atc/integration` never run in the release-gating pipeline today (the `unit-tests` job's inline script explicitly `grep -v`s all three out). This task adds a dedicated job that runs them against the PostgreSQL Task 2 put in the image, and wires it into the `passed:` chain so a failure there blocks `tag-rc` → `build-image` → `release`.

**Files:**
- Modify: `deploy/concourse-pipeline.yml`

- [ ] Confirm the exact three commands to run, and that they're all Ginkgo suites (not a mix of `ginkgo`/plain `go test` — this corrects one imprecision in the source spec, which described `atc/integration` as a plain `go test` target):
  ```
  grep -rl "RunSpecs" atc/integration/*.go
  find atc/gc -maxdepth 1 -iname "*suite_test.go*"
  grep -n "GinkgoRunner" atc/gc/gc_suite_test.go atc/integration/integration_suite_test.go atc/db/db_suite_test.go
  ```
  Expected: all three (`atc/db`, `atc/gc`, `atc/integration`) have a `RunSpecs` bootstrap and all three call `postgresrunner.GinkgoRunner(&postgresRunner)` in their suite file — each is self-sufficient once `initdb`/`postgres` are on `PATH`. `atc/integration` therefore runs via `ginkgo -r --keep-going -p ./atc/integration/` — **identical to the Makefile's existing `test-integration` target** — not a raw `go test`.
- [ ] Immediately after the `unit-tests` job's closing (`echo "=== All unit tests passed ==="`) and before `- name: k8s-runtime-tests`, insert a new job:
  ```yaml
  - name: db-tests
    serial_groups: [pipeline]
    plan:
    - get: repo
      trigger: true
      params: {depth: 1}
      passed: [unit-tests]
    - task: db-tests
      attempts: 2
      config:
        platform: linux
        rootfs_uri: docker:///registry.home/concourse-test-runner:v6
        inputs:
        - name: repo
        run:
          path: sh
          args:
          - -exc
          - |
            cd repo
            echo "=== Verifying PostgreSQL is available in this image ==="
            initdb --version
            postgres --version
            id postgres
            echo ""
            echo "=== Running Postgres-backed atc/db and atc/gc suites ==="
            ginkgo -r -p --keep-going --flake-attempts=1 ./atc/db ./atc/gc
            echo ""
            echo "=== Running atc/integration suite ==="
            ginkgo -r --keep-going -p ./atc/integration/
            echo "=== All Postgres-backed suites passed ==="
  ```
  The `initdb --version` / `postgres --version` / `id postgres` lines are a deliberate fail-fast: if Task 2's image is broken, this job dies in three seconds with an unambiguous "command not found" instead of a confusing Ginkgo stack trace three minutes later.
- [ ] Change the `k8s-runtime-tests` job's `get: repo` from:
  ```yaml
  - name: k8s-runtime-tests
    serial: true
    serial_groups: [pipeline]
    plan:
    - get: repo
      trigger: true
      params: {depth: 1}
      passed: [unit-tests]
  ```
  to:
  ```yaml
  - name: k8s-runtime-tests
    serial: true
    serial_groups: [pipeline]
    plan:
    - get: repo
      trigger: true
      params: {depth: 1}
      passed: [db-tests]
  ```
  This is the one line that makes the whole chain matter: `tag-rc` already requires `passed: [k8s-runtime-tests]`, and `k8s-runtime-tests` now requires `passed: [db-tests]` — so `db-tests` gates `tag-rc`/`build-image`/`release` **transitively**, without needing to also touch `tag-rc`'s own `passed:` list. (Deliberate choice over adding `db-tests` to `tag-rc`'s `passed:` directly: this pipeline already runs every job under `serial_groups: [pipeline]` — i.e. fully serial regardless of the DAG shape — so there is no throughput difference between the two options, and extending the existing single linear chain is a smaller, more obviously-correct diff than fanning `tag-rc` out to depend on two upstream jobs.)
- [ ] Run `fly validate-pipeline -c deploy/concourse-pipeline.yml`. Expected: `looks good`.
- [ ] **This task's core acceptance criterion — "a deliberately broken atc/db test turns the release pipeline red" — cannot be proven by running atc/db locally in this environment** (postgres orchestration isn't available here, and CLAUDE.md's own test-running rules say not to run atc/db tests against a shared/parallel-agent Postgres). It is verified by construction instead: `db-tests` is a real job whose task runs `ginkgo -r ...`; a failing spec gives that command (and therefore the task, and therefore the job) a non-zero exit; `k8s-runtime-tests`'s `get: repo, passed: [db-tests]` then never receives a version to proceed on for that commit; `tag-rc`, `build-image`, and `release` all sit downstream of `k8s-runtime-tests` and never run either. Once this pipeline is deployed, an operator can close the loop for real by pushing a scratch commit with one deliberately-failing `atc/db` assertion to a throwaway branch, pointing a copy of this pipeline at it, and watching `db-tests` go red while the chain stops — track that as the live acceptance check; it is out of scope for a docs-only planning session to perform.
- [ ] Commit `ci(pipeline): add a postgres-backed db-tests job gating tag-rc`.

---

### Task 4: Add the equivalent `db-tests` gate to `dogfood-pipeline.yml`

**Problem this closes:** `dogfood-pipeline.yml`'s single `run` job is a sequential task chain, not a job-graph with `passed:` constraints, so "add a `db-tests` job" here means "add a `db-tests` task between `test-quick-gate` and `agent-review`." Today the `test-quick-gate` task (and the file's own inline comments, and `ci/dogfood/README.md`) explicitly document Postgres-backed suites as excluded and left to a human running `make test-quick` before merge. That claim becomes false once this task lands, so this task also fixes the now-stale comments in the same file plus the one in the README, rather than leaving a self-contradicting pair of docs.

**Files:**
- Modify: `deploy/dogfood-pipeline.yml`
- Modify: `ci/dogfood/README.md`

- [ ] In `deploy/dogfood-pipeline.yml`, change the `test-quick-gate` task's leading comment from:
  ```yaml
    # ---------------------------------------------------------------------
    # 2. Test-quick gate.
    #    This is the CI equivalent of `make test-quick` (unit + ci-agent). The
    #    concourse-test-runner image has no local PostgreSQL and no ginkgo, so —
    #    copying the live unit-tests job's postgres approach exactly — the
    #    postgres-backed packages are excluded here and remain a HUMAN-RUN local
    #    gate (`make test-quick` with pg_isready green) before merging the
    #    pushed branch. See ci/dogfood/README.md.
    # ---------------------------------------------------------------------
  ```
  to:
  ```yaml
    # ---------------------------------------------------------------------
    # 2. Test-quick gate.
    #    This is the CI equivalent of `make test-quick` (unit + ci-agent +
    #    dev-mcp). Postgres-backed packages (atc/db, atc/gc, atc/integration)
    #    are still excluded from THIS task's go-test sweep — they now run as
    #    their own gate below (task 3, db-tests) against the PostgreSQL that
    #    ships in concourse-test-runner:v6. See
    #    docs/superpowers/plans/test-hardening/01-ci-execution.md (WS1) and
    #    ci/dogfood/README.md.
    # ---------------------------------------------------------------------
  ```
- [ ] Immediately after the `test-quick-gate` task's closing (`echo "=== test-quick gate passed ==="`) and before the `# 3. Diff-aware agent review...` comment block, insert a new task, and renumber the two comment blocks that follow it (`3.` → `4.`, `4.` → `5.`):
  ```yaml
    # ---------------------------------------------------------------------
    # 3. Postgres-backed db-tests gate (WS1). Runs atc/db, atc/gc, and
    #    atc/integration against the PostgreSQL that ships in
    #    concourse-test-runner:v6 — see deploy/Dockerfile.test-runner and
    #    atc/postgresrunner (each Ginkgo suite self-provisions its own
    #    ephemeral cluster via postgresrunner.GinkgoRunner; this task only
    #    needs the initdb/postgres binaries + a "postgres" OS user, both of
    #    which the image now provides — no separate database bootstrap step
    #    is needed here).
    # ---------------------------------------------------------------------
    - task: db-tests
      attempts: 2
      config:
        platform: linux
        rootfs_uri: docker:///registry.home/concourse-test-runner:v6
        inputs:
        - name: worked-repo
        run:
          path: sh
          args:
          - -exc
          - |
            cd worked-repo
            echo "=== Verifying PostgreSQL is available in this image ==="
            initdb --version
            postgres --version
            id postgres
            echo ""
            echo "=== Running Postgres-backed atc/db and atc/gc suites ==="
            ginkgo -r -p --keep-going --flake-attempts=1 ./atc/db ./atc/gc
            echo ""
            echo "=== Running atc/integration suite ==="
            ginkgo -r --keep-going -p ./atc/integration/
            echo "=== All Postgres-backed suites passed ==="
  ```
  Change the following block's `# 3. Diff-aware agent review...` to `# 4. Diff-aware agent review...` (text otherwise unchanged), and the final block's `# 4. Push the branch...` to `# 5. Push the branch...` (text otherwise unchanged). This task reuses the `registry.home/concourse-test-runner:v6` image Task 2/3 already built and pushed for `concourse-pipeline.yml` — both pipelines run on the same cicd cluster against the same registry, and `dogfood-pipeline.yml` already reuses `:v5` this same way for `test-quick-gate` and `push-branch`, so no second build job is needed here.
- [ ] In `ci/dogfood/README.md`, change:
  ```markdown
  - **Review-before-merge protocol.** An `agent/dogfood-*` branch NEVER merges unreviewed:
    (1) read the published review on the build page and the branch diff; (2) run
    `make test-quick` locally with PostgreSQL up — the CI gate excludes postgres-backed
    suites (atc/db, atc/gc, ...) because the test-runner image has no postgres; (3) run the
    plan's own Execution-notes suites for the packages touched; (4) merge by hand.
  ```
  to:
  ```markdown
  - **Review-before-merge protocol.** An `agent/dogfood-*` branch NEVER merges unreviewed:
    (1) read the published review on the build page and the branch diff; (2) confirm the
    `db-tests` task passed — it runs atc/db, atc/gc, and atc/integration in CI against the
    PostgreSQL bundled in concourse-test-runner:v6 (WS1,
    docs/superpowers/plans/test-hardening/01-ci-execution.md); (3) run the plan's own
    Execution-notes suites for the packages touched; (4) merge by hand.
  ```
- [ ] Run `fly validate-pipeline -c deploy/dogfood-pipeline.yml`. Expected: `looks good`.
- [ ] Commit `ci(dogfood): add the postgres-backed db-tests gate to the dogfood pipeline`.

---

### Task 5: Run `agent/schema` and `ci-agent` module tests in `concourse-pipeline.yml`'s `unit-tests`

**Problem this closes:** WS1 decision 2, half one. `agent/schema` and `ci-agent` are separate Go modules (`agent/schema/go.mod`, `ci-agent/go.mod`) — invisible to `go list ./...` from the repo root, so the `unit-tests` job's `PACKAGES=$(go list ./... | grep -v ...)` sweep silently never includes them (there is nothing to `grep -v` exclude; they simply never appear). Verified locally while scouting this plan:
```
cd agent/schema && go test ./... -count=1        # ok  github.com/concourse/concourse/agent/schema  0.133s
cd ci-agent && go test ./... -count=1 -timeout 5m  # 23 packages, all ok, ~7.8s wall
```
Both pass today and are fast; they just never run in the release-gating pipeline.

**Files:**
- Modify: `deploy/concourse-pipeline.yml`

- [ ] Change the `unit-tests` job's task script from ending at:
  ```yaml
            go test -count=1 -timeout 10m $PACKAGES
            echo "=== All unit tests passed ==="
  ```
  to:
  ```yaml
            go test -count=1 -timeout 10m $PACKAGES
            echo "=== All unit tests passed ==="
            echo ""
            echo "=== Running agent/schema module tests (separate go.mod; invisible to go list ./...) ==="
            (cd agent/schema && go test ./... -count=1)
            echo ""
            echo "=== Running ci-agent module tests (separate go.mod; invisible to go list ./...) ==="
            (cd ci-agent && go test ./... -count=1 -timeout 5m)
            echo "=== All module tests passed ==="
  ```
  (Parenthesized subshells so a failure in one doesn't leave the script in the wrong directory for the other — matches the source spec's literal wording, `(cd agent/schema && go test ./...)` / `(cd ci-agent && go test ./...)`.)
- [ ] Run `fly validate-pipeline -c deploy/concourse-pipeline.yml`. Expected: `looks good`.
- [ ] Run both commands locally one more time exactly as the task script will invoke them, to pin the expected log output this step should produce in CI:
  ```
  (cd agent/schema && go test ./... -count=1)
  (cd ci-agent && go test ./... -count=1 -timeout 5m)
  ```
  Expected: `ok github.com/concourse/concourse/agent/schema 0.1xxs` and 23 `ok github.com/concourse/ci-agent/...` lines (adapter, adapter/claude, browserplan, cmd/ci-agent, cmd/dev-mcp, cmd/validate-output, config, devmcp, envconfig, feedback, gapgen, integration, llm, mapper, phaseconfig, phaserunner, provenance, publish, runner, scoring, specparser, storage, tracing), zero `FAIL`.
- [ ] Commit `ci(pipeline): run agent/schema and ci-agent module tests in unit-tests`.

---

### Task 6: Run `agent/schema` module tests in the dogfood `test-quick-gate`

**Problem this closes:** WS1 decision 2, half two. `dogfood-pipeline.yml`'s `test-quick-gate` task already runs `ci-agent` module tests (it has for a while — see the existing `cd ci-agent / go test ./... -count=1 -timeout 5m` block); it is only missing `agent/schema`, for the identical invisible-to-`go list` reason as Task 5.

**Files:**
- Modify: `deploy/dogfood-pipeline.yml`

- [ ] Change the `test-quick-gate` task script from:
  ```yaml
            go test -count=1 -timeout 15m $PACKAGES
            echo ""
            echo "=== Running ci-agent module tests ==="
            cd ci-agent
            go test ./... -count=1 -timeout 5m
            echo "=== test-quick gate passed ==="
  ```
  to:
  ```yaml
            go test -count=1 -timeout 15m $PACKAGES
            echo ""
            echo "=== Running agent/schema module tests (separate go.mod; invisible to go list ./...) ==="
            (cd agent/schema && go test ./... -count=1)
            echo ""
            echo "=== Running ci-agent module tests ==="
            cd ci-agent
            go test ./... -count=1 -timeout 5m
            echo "=== test-quick gate passed ==="
  ```
  (The `agent/schema` step uses a subshell since the existing `ci-agent` step deliberately changes the script's cwd with a bare `cd` and nothing after it needs to return to `worked-repo` root except the final no-op `echo` — leaving that line as-is rather than refactoring working code that isn't this task's concern.)
- [ ] Run `fly validate-pipeline -c deploy/dogfood-pipeline.yml`. Expected: `looks good`.
- [ ] Commit `ci(dogfood): run agent/schema module tests in the dogfood test-quick gate`.

---

### Task 7: Wire `test-dev-mcp` into CI and re-gate `TestLiveImageContract`

**Problem this closes:** WS1 decision 3. `make test-dev-mcp` exists and is included in `make test-quick` locally, but (a) it is missing from `make test-all`, and (b) no pipeline names it as an explicit step (it currently only runs as an accidental side effect of `agent/devmcp/...` not being excluded from the broader sweeps — true, per Task 1's finding, but not a *documented guarantee*, and fragile if anyone ever adds an exclusion). Separately, `agent/devmcp/e2e/e2e_test.go`'s `TestLiveImageContract` skips based on `DEV_MCP_ENDPOINT` being unset, with a comment claiming it's "driven by the build-mcp-dev-image CI job (deploy/concourse-pipeline.yml)" — verified during scouting: **no job named `build-mcp-dev-image` exists anywhere in this repo.** (`deploy/Dockerfile.mcp-dev-concourse` and `deploy/MCP_IMAGES.md` exist, but nothing pipelines them into a running container this test could reach.)

**Files:**
- Modify: `Makefile`
- Modify: `deploy/concourse-pipeline.yml`
- Modify: `deploy/dogfood-pipeline.yml`
- Modify: `agent/devmcp/e2e/e2e_test.go`

- [ ] Confirm the never-created job claim: `grep -rn "build-mcp-dev-image" deploy/*.yml` (expected: zero hits — it appears only in the test file's own comment, which this task rewrites).
- [ ] In `Makefile`, change `test-all` from:
  ```makefile
  test-all: test-unit test-ci-agent test-fly-integration test-integration test-k8s
  ```
  to:
  ```makefile
  test-all: test-unit test-ci-agent test-dev-mcp test-fly-integration test-integration test-k8s
  ```
- [ ] Verify with a dry run (does not execute anything): `make -n test-all`. Expected: the existing lines plus a new `echo "==> Running dev-mcp contract/e2e tests..."` / `go test ./agent/devmcp/... -count=1 -timeout 10m` pair inserted between the `ci-agent` and `fly integration` lines — i.e. now matching `make -n test-quick`'s existing composition (`test-unit` → `agent/schema` → `ci-agent` → `dev-mcp`) with the K8s/fly/integration tiers appended after.
- [ ] In `deploy/concourse-pipeline.yml`, append to the `unit-tests` task script (after Task 5's module-test addition):
  ```yaml
            echo ""
            echo "=== Running dev-mcp contract/e2e tests ==="
            go test ./agent/devmcp/... -count=1 -timeout 10m
            echo "=== dev-mcp tests passed ==="
  ```
- [ ] In `deploy/dogfood-pipeline.yml`, change the `test-quick-gate` task script's ending (after Task 6's `agent/schema` addition) from:
  ```yaml
            echo "=== Running ci-agent module tests ==="
            cd ci-agent
            go test ./... -count=1 -timeout 5m
            echo "=== test-quick gate passed ==="
  ```
  to:
  ```yaml
            echo "=== Running ci-agent module tests ==="
            cd ci-agent
            go test ./... -count=1 -timeout 5m
            cd ..
            echo ""
            echo "=== Running dev-mcp contract/e2e tests ==="
            go test ./agent/devmcp/... -count=1 -timeout 10m
            echo "=== test-quick gate passed ==="
  ```
  (Note the added `cd ..`: the preceding block left the script inside `ci-agent/`, and `go test ./agent/devmcp/...` needs to run from `worked-repo` root — this is a real behavioral requirement, not stylistic; without it the dev-mcp step would silently resolve to the wrong path and fail with "no such directory," which is exactly the kind of silent gap this whole plan exists to close.)
- [ ] In `agent/devmcp/e2e/e2e_test.go`, change:
  ```go
  // TestLiveImageContract exercises a running mcp-dev-concourse container.
  // It is driven by the build-mcp-dev-image CI job (deploy/concourse-pipeline.yml)
  // and skipped everywhere else.
  func TestLiveImageContract(t *testing.T) {
  	endpoint := os.Getenv("DEV_MCP_ENDPOINT")
  	if endpoint == "" {
  		t.Skip("DEV_MCP_ENDPOINT not set; this test runs in the build-mcp-dev-image CI job")
  	}
  	contracttest.RunWithOptions(t, endpoint, contracttest.Options{
  		ExerciseComponent: "ci-agent",
  		AffectedPath:      "atc/api/handler.go",
  		ExpectAffected:    []string{"atc"},
  		Timeout:           20 * time.Minute,
  	})
  }
  ```
  to:
  ```go
  // TestLiveImageContract exercises a running mcp-dev-concourse container.
  // It requires DEV_MCP_IMAGE_TEST=1 as an explicit opt-in on top of
  // DEV_MCP_ENDPOINT — this is a live-container test, not something that runs
  // by merely being in the package. No CI job sets these today; the
  // build-mcp-dev-image job this comment used to reference has never existed
  // in this repo (verified 2026-07-25). A human wanting to run this locally
  // against an already-running mcp-dev-concourse container (see
  // deploy/Dockerfile.mcp-dev-concourse, deploy/MCP_IMAGES.md) sets both env
  // vars themselves.
  func TestLiveImageContract(t *testing.T) {
  	if os.Getenv("DEV_MCP_IMAGE_TEST") != "1" {
  		t.Skip("DEV_MCP_IMAGE_TEST != 1; this live-container test does not run by default (see comment above)")
  	}
  	endpoint := os.Getenv("DEV_MCP_ENDPOINT")
  	if endpoint == "" {
  		t.Fatal("DEV_MCP_IMAGE_TEST=1 but DEV_MCP_ENDPOINT is not set")
  	}
  	contracttest.RunWithOptions(t, endpoint, contracttest.Options{
  		ExerciseComponent: "ci-agent",
  		AffectedPath:      "atc/api/handler.go",
  		ExpectAffected:    []string{"atc"},
  		Timeout:           20 * time.Minute,
  	})
  }
  ```
  (This upgrades the unset-endpoint case from a silent skip to a loud `t.Fatal` when someone opts in but forgets the endpoint — deliberately stricter than a bare rename, since a misconfigured opt-in should not look green.)
- [ ] Run `go test ./agent/devmcp/... -count=1 -timeout 10m -run TestLiveImageContract -v`. Expected: `--- SKIP: TestLiveImageContract` with reason `DEV_MCP_IMAGE_TEST != 1; ...`. Then run `DEV_MCP_IMAGE_TEST=1 go test ./agent/devmcp/... -count=1 -timeout 10m -run TestLiveImageContract -v` (endpoint still unset). Expected: `--- FAIL: TestLiveImageContract` with `DEV_MCP_IMAGE_TEST=1 but DEV_MCP_ENDPOINT is not set` — confirms the new opt-in gate and its stricter failure mode both work.
- [ ] Run `fly validate-pipeline -c deploy/concourse-pipeline.yml` and `fly validate-pipeline -c deploy/dogfood-pipeline.yml`. Expected: `looks good` for both.
- [ ] Commit `ci(pipeline): wire test-dev-mcp into CI and re-gate TestLiveImageContract`.

---

### Task 8: Add a scoped `go test -race` lane for `agent/...`

**Problem this closes:** WS1 decision 4. There is no `-race` execution anywhere for `agent/` (or anywhere else). The repo-wide ban in CLAUDE.md ("Do not use `--race` — it causes parallel compilation failures") is about the `ginkgo` CLI's own `-p` flag combined with `-race`; it says nothing about a plain `go test -race` invocation, which never touches the `ginkgo` binary at all. Confirmed during scouting: every package under `agent/...` is either plain `testing.T` or a Ginkgo suite runnable via ordinary `go test` (only `agent/devmcp` and `agent/gitcheck` use Ginkgo; everything else is plain), none use a `//go:build live` (or any other) tag gating a real external dependency (`grep -rl "^//go:build" agent/` found zero files), and the entire `agent/...` tree already runs today inside the postgres-less CI `unit-tests` job's `go test $PACKAGES` sweep with zero exclusions for anything under `agent/` — proof by existing behavior that nothing there needs Postgres, Docker, or a live cluster.

- [ ] **Run it locally, first, before touching any file.** This is the load-bearing step in this task: a red result here is a real finding to triage, not a reason to weaken or narrow the lane.
  ```
  go test -race -count=1 -timeout 300s ./agent/...
  ```
  This exact command was run twice while scouting this plan, against commit `410d9b59f8`:
  - **First run:** one `FAIL github.com/concourse/concourse/agent/snapshot/contracts [build failed]` — `open agent/snapshot/contracts/zz_scratch_b_test.go: no such file or directory`. Investigation: `git status` showed an **untracked** `agent/snapshot/contracts/zz_scratch_d_test.go` (not `_b_`) — a different, concurrently-running agent process in this same shared working tree was creating/renaming/deleting its own scratch test file mid-run. This is the shared-workspace caveat called out in Global Constraints, not a product bug.
  - **Second run**, moments later: **fully green**. All 38 testable packages (`ok`), 5 no-op `[no test files]` packages (the `*fakes` directories), zero `FAIL`, wall clock ~23.6s (`28.67s user 15.24s system 185% cpu 23.614 total`).
  - **Conclusion documented here so re-running this step doesn't have to rediscover it:** as of `410d9b59f8`, `go test -race -count=1 ./agent/...` is green. If your run shows a failure that (a) reproduces on an immediate second run, and (b) is an actual `--- FAIL:` from a test assertion or a `WARNING: DATA RACE` block (not a `[build failed]` citing a missing file under a directory you didn't touch), **stop here.** Do not add the Makefile target or CI step yet. Capture the full `go test -race -v` output for the failing package to a file (e.g. this plan's tracking issue, or a scratch note under `/private/tmp/.../scratchpad/`), including every goroutine stack trace the race detector printed, and treat fixing or triaging that race as a blocking prerequisite for the rest of this task. Do **not** "fix" a real race by dropping the offending package from the lane's scope or by removing `-race` — either of those defeats the entire point of this task.
- [ ] Add the Makefile target. Change the `.PHONY` line from:
  ```makefile
  .PHONY: test-unit test-ci-agent test-dev-mcp test-fly-integration test-integration test-k8s test-k8s-integration test-k8s-behavioral test-quick test-all
  ```
  to:
  ```makefile
  .PHONY: test-unit test-ci-agent test-dev-mcp test-agent-race test-fly-integration test-integration test-k8s test-k8s-integration test-k8s-behavioral test-quick test-all
  ```
  Then add the target itself (placed after `test-dev-mcp`, before `test-fly-integration`, matching that grouping's "no special prerequisites" character):
  ```makefile
  # Agent race lane (plain go test, NOT the ginkgo CLI — the -p + -race
  # parallel-compilation failure documented in CLAUDE.md's Key Notes is a
  # ginkgo-CLI-specific bug and does not apply to a plain `go test -race`).
  # Requires: nothing (verified: no package under agent/... needs postgres,
  # docker, or a live cluster — see this plan's Task 8 for how that was
  # confirmed).
  test-agent-race:
  	@echo "==> Running agent/ race lane..."
  	go test -race -count=1 ./agent/...
  ```
  And change `test-all` (already touched by Task 7) from:
  ```makefile
  test-all: test-unit test-ci-agent test-dev-mcp test-fly-integration test-integration test-k8s
  ```
  to:
  ```makefile
  test-all: test-unit test-ci-agent test-dev-mcp test-agent-race test-fly-integration test-integration test-k8s
  ```
- [ ] Verify: `make -n test-agent-race` (expected: prints the echo line and the exact `go test -race -count=1 ./agent/...` command, does not execute — dry run), then `make test-agent-race` for real (expected: green, matching the second scouting run above; allow it to recompile from scratch so the timing may differ slightly from the ~24s observed above — that's normal, `-race` instrumentation isn't cached the same way as a plain build).
- [ ] In `deploy/concourse-pipeline.yml`, append to the `unit-tests` task script (after Task 7's dev-mcp addition):
  ```yaml
            echo ""
            echo "=== Running agent/ race lane (go test -race) ==="
            go test -race -count=1 ./agent/...
            echo "=== agent/ race lane passed ==="
  ```
  (Direct command, not `make test-agent-race` — see Global Constraints on why new CI steps in this plan don't shell out to `make`.)
- [ ] Run `fly validate-pipeline -c deploy/concourse-pipeline.yml`. Expected: `looks good`.
- [ ] **This task's acceptance criterion — "the race lane is green in CI" — is verified locally above (both as `go test -race` directly and as `make test-agent-race`) but, like Task 3's, cannot be verified as *running inside the actual Concourse pipeline* without deploying it.** The CI step added above runs the identical command verified green locally in an image (`concourse-test-runner:v5`, unchanged by this task) that already successfully builds and runs the whole `agent/...` tree today without `-race` (proof: the existing `unit-tests` job's `$PACKAGES` sweep already includes it) — so there is no new environmental variable between "green locally" and "green in CI" for this particular step, unlike Task 3/4 which depend on the not-yet-built `:v6` image.
- [ ] Commit `test(agent): add a scoped go test -race lane for agent/...`.

---

### Task 9: Wire a guarded fuzz-smoke CI step ahead of the WS6 target

**Problem this closes:** WS1 decision 5. The spec's WS6 plan (Sealing and digest hardening — not yet written; will live at `docs/superpowers/plans/test-hardening/06-*.md` per the parent spec's own naming convention) creates a `make test-fuzz` target running `FuzzCanonicalCapture`/`FuzzCanonicalJSON` time-boxed at `-fuzztime=30s`. This plan's job is only to land the CI *step* that will run it, in a way that does nothing (and does not fail the build) until that target actually exists — an explicit cross-plan contract, not a guess at WS6's implementation.

**Files:**
- Modify: `deploy/concourse-pipeline.yml`

- [ ] Confirm the target doesn't exist yet (it shouldn't, on this branch): `grep -qE '^test-fuzz:' Makefile; echo $?` — expected: `1` (grep found nothing, confirmed while scouting this plan).
- [ ] In `deploy/concourse-pipeline.yml`, append to the `unit-tests` task script (after Task 8's race-lane addition):
  ```yaml
            echo ""
            echo "=== Fuzz smoke lane (make test-fuzz, if wired yet) ==="
            if grep -qE '^test-fuzz:' Makefile; then
              make test-fuzz
            else
              echo "SKIP: 'test-fuzz' Makefile target does not exist on this commit yet."
              echo "It lands via the WS6 plan (Sealing and digest hardening,"
              echo "docs/superpowers/plans/test-hardening/06-*.md). This guard is a"
              echo "deliberate no-op until then - see 01-ci-execution.md Task 9."
            fi
  ```
  **Cross-plan contract, stated explicitly so both plans agree on it:** the CI step lands here (WS1/this plan); the `test-fuzz` Makefile target itself lands in the WS6 plan. The guard above is a `grep` check (no `make` dependency), so it costs nothing today and self-activates the moment WS6 adds the target — no further edit to this file is needed when that happens.
  **Assumption this step does carry once the guard's `if` branch becomes live: `make` must be present in the `concourse-test-runner` image.** Unlike every other new CI step in this plan, this one has no alternative — the recipe it needs to run doesn't exist yet for this plan to inline directly. No existing pipeline task invokes `make` today (Global Constraints), so this is genuinely unverified, not merely unverified-by-choice. If, once WS6 lands `test-fuzz`, this step fails with `make: command not found` rather than a fuzz-test failure, that is the finding — the fix is a one-line addition of `make` to `deploy/Dockerfile.test-runner`'s existing `apt-get install -y --no-install-recommends` list (alongside `curl`/`git`/`jq`/`unzip`), not a reason to redesign this guard or inline a guessed fuzz recipe now.
- [ ] Run `fly validate-pipeline -c deploy/concourse-pipeline.yml`. Expected: `looks good`.
- [ ] Simulate the guard locally to confirm its logic before committing (does not require the fuzz target to exist):
  ```
  if grep -qE '^test-fuzz:' Makefile; then echo "would run: make test-fuzz"; else echo "SKIP: 'test-fuzz' Makefile target does not exist on this commit yet."; fi
  ```
  Expected (verified while scouting this plan): `SKIP: 'test-fuzz' Makefile target does not exist on this commit yet.`, exit 0.
- [ ] Commit `ci(pipeline): wire a guarded fuzz smoke step ahead of the WS6 target`.

---

### Task 10: Docs — TESTING.md, CLAUDE.md, and a final verification pass

**Problem this closes:** WS1 decision 6 (the last remaining piece): `TESTING.md` never mentions `agent/`'s coverage story or the new race lane; `CLAUDE.md`'s race-ban note is correct as far as it goes but doesn't mention the lane this plan just added, which — read alone — looks like it contradicts the ban it sits next to.

**Files:**
- Modify: `TESTING.md`
- Modify: `CLAUDE.md`

- [ ] In `TESTING.md`, inside the existing `### 1. Unit Tests (\`make test-unit\`)` section, immediately after the `- **What it covers:** 79 test suites across atc/, fly/, skymarshal/, go-concourse/, tracing/` bullet, add a new bullet:
  ```markdown
  - **`agent/` note:** `agent/...` is walked by this same `ginkgo -r` — including its plain-`testing.T` subpackages (e.g. `agent/devmcp/contracttest`, `agent/devmcp/e2e`), which ginkgo's CLI builds and runs as ordinary `go test` binaries whether or not they call `RunSpecs`. `agent/schema` is the one exception: it is a separate Go module (own `go.mod`), invisible to any `go list ./...`-based walk, and is run by its own explicit step (see `make test-unit`'s `cd agent/schema && go test ./...` line).
  ```
- [ ] In `TESTING.md`, immediately after the closing of `### 6. K8s Behavioral Tests` (the ```` ```bash ... K8S_PROCS=4 make test-k8s-behavioral ... ```` block) and before `## Prerequisites`, add a new section:
  ```markdown
  ### 7. Agent Race Lane (`make test-agent-race`)

  Runs the entire `agent/...` tree (both plain-`testing.T` packages and the two
  Ginkgo suites it contains, `agent/devmcp` and `agent/gitcheck`) under the Go
  race detector via a plain `go test -race`, not the `ginkgo` CLI — so the
  `-p` + `-race` parallel-compilation failure documented in CLAUDE.md's Key
  Notes (specific to the `ginkgo` binary) does not apply here.

  - **Time:** ~25-60s (varies with `-race` recompilation; not cached the same
    way as a plain build)
  - **Prerequisites:** None — no package under `agent/` reaches Postgres,
    Docker, or a live cluster
  - **What it covers:** every package under `agent/...`, race-instrumented

  ```bash
  make test-agent-race

  # Equivalent direct invocation:
  go test -race -count=1 ./agent/...
  ```
  ```
- [ ] In `CLAUDE.md`, change the Key Notes bullet from:
  ```markdown
  - Unit tests run in parallel (`-p` flag, 9 procs by default). Do not use `--race` — it causes parallel compilation failures (`fork/exec db.test: no such file or directory`).
  ```
  to:
  ```markdown
  - Unit tests run in parallel (`-p` flag, 9 procs by default). Do not use `--race` with the `ginkgo` CLI's own `-p` — it causes parallel compilation failures (`fork/exec db.test: no such file or directory`). Scoped exception: `make test-agent-race` runs a plain `go test -race -count=1 ./agent/...` (no `ginkgo` CLI involved, so this failure mode does not apply) and is wired as a CI step — see TESTING.md.
  ```
- [ ] In `CLAUDE.md`'s Quick Reference table, add a new row immediately after the `make test-quick` row:
  ```markdown
  | `make test-agent-race` | `go test -race` over `agent/...` (plain testing + 2 Ginkgo suites, no ginkgo CLI) | ~1 min | None |
  ```
- [ ] Final verification pass for this whole plan — re-run every check that spans multiple tasks, in one place, before the last commit:
  ```
  fly validate-pipeline -c deploy/concourse-pipeline.yml
  fly validate-pipeline -c deploy/dogfood-pipeline.yml
  fly validate-pipeline -c deploy/test-pipeline.yml
  fly validate-pipeline -c deploy/borg-pipeline.yml
  make -n test-all
  make -n test-quick
  go test ./agent/schema/... -count=1 2>&1 | tail -5    # run from agent/schema/
  (cd ci-agent && go test ./... -count=1 -timeout 5m)
  go test ./agent/devmcp/... -count=1 -timeout 10m
  make test-agent-race
  grep -c "concourse-test-runner:v6" deploy/concourse-pipeline.yml deploy/dogfood-pipeline.yml   # expect >=1 in each
  ```
  Expected: `looks good` x4, both `make -n` dry runs show the fully-updated composition (test-unit → agent/schema → ci-agent → dev-mcp → agent-race → fly-integration → integration → k8s for `test-all`; same minus the last three for `test-quick`), all `go test`/`make` invocations green, and both pipeline files reference `:v6` at least once (the `db-tests` job/task each).
- [ ] Commit `docs(testing): document the agent tier, race-lane exception, and CLAUDE.md note`.

---

## Self-Review Against WS1 Acceptance Criteria

The parent spec's acceptance bar for WS1, checked against this plan's tasks:

1. **"A deliberately broken `atc/db` test turns the release pipeline red."** → Task 3 (`db-tests` job + `k8s-runtime-tests`'s `passed: [db-tests]` edit, transitively gating `tag-rc`/`build-image`/`release`) and Task 4 (dogfood equivalent). Provable by construction now; provable live only after deployment (documented explicitly in both tasks — this plan does not overclaim a live proof it cannot perform from a docs-only session).
2. **"`go list` output in the unit-tests job log shows no silently-skipped agent package."** → Task 1 establishes that `agent/devmcp` (and everything else under `agent/...` in the main module) was never actually silently skipped by `ginkgo -r`'s walk, contrary to prior belief — and Task 5 closes the one gap that *was* real: `agent/schema`, a genuinely separate module invisible to any `go list ./...`-based sweep, now runs via an explicit, logged step in both pipelines (Task 5 for `concourse-pipeline.yml`, Task 6 for `dogfood-pipeline.yml`).
3. **"`make test-all` runs dev-mcp."** → Task 7 (`test-all` gains `test-dev-mcp`; explicit CI steps added to both pipelines; `TestLiveImageContract` re-gated off the never-existent `build-mcp-dev-image` job onto `DEV_MCP_IMAGE_TEST=1`).
4. **"The race lane is green in CI."** → Task 8: verified green locally (twice, with the transient sibling-agent contamination on the first run identified and explained, not hand-waved), Makefile target added, `test-all` updated, and the identical command wired as a CI step in an image already proven to build/run all of `agent/...` today. Same live-vs-local caveat as (1).

All five WS1 decisions in the parent spec map to at least one task: decision 1 → Tasks 2/3/4; decision 2 → Tasks 5/6; decision 3 → Task 7; decision 4 → Task 8; decision 5 (docs) → Tasks 1/10. Task 0 is scope-boundary work the spec's own mission brief asked this plan to resolve explicitly (whether the two legacy pipelines get the same treatment); it doesn't map to a WS1 decision number because the spec's decision list only discusses the two live pipelines.

**Known deviations from the source spec, and why:**
- Decision 1 describes `atc/integration` as run via `go test ./atc/integration/...`; Task 3 uses `ginkgo -r --keep-going -p ./atc/integration/` instead, because scouting found `atc/integration` has a real `RunSpecs` bootstrap (`integration_suite_test.go`) and calls `postgresrunner.GinkgoRunner` exactly like `atc/db`/`atc/gc` — the correct invocation is the one `make test-integration` already uses locally, not a raw `go test`.
- Decision 1 describes the postgres bootstrap as "start it in the task script (`pg_ctlcluster`/`pg_ctl` + `pg_isready` gate)". Task 2/3/4 do neither: `atc/postgresrunner.GinkgoRunner` already starts (and stops) its own ephemeral, per-parallel-process cluster directly via `exec.LookPath("initdb")`/`exec.LookPath("postgres")`, with no dependency on a Debian cluster, `pg_ctlcluster`, or a persistent daemon on a fixed port. The task script only needs to make those two binaries resolvable and ensure a `postgres` OS user exists — both are satisfied by installing the `postgresql`/`postgresql-contrib` apt packages, with no separate start/wait step required.
