# Implementation Plan: Dead Suite Removal

> Deletion-only track — no TDD cycle; each phase is delete → compile/test-verify.
> Verification baseline: `go build ./...` + `go vet ./...` (ignore the pre-existing
> `atc/exec/artifact_input_step_test.go` vet failure) + targeted suites per phase.

## Phase 1: Orphaned topgun/k8s root suite

- [ ] Task: Delete the 15 `package k8s` test files directly in `topgun/k8s/`
      (`k8s_suite_test.go`, `container_limits_test.go`, `dns_proxy_test.go`,
      `ephemeral_worker_test.go`, `external_postgres_test.go`,
      `external_worker_test.go`, `https_web_tls_termination_test.go`,
      `k8s_backend_test.go`, `kubernetes_creds_mgmt_test.go`,
      `mainteam_role_test.go`, `prometheus_test.go`, `tsa_node_port_test.go`,
      `web_scaling_test.go`, `worker_lifecycle_test.go`) — do NOT touch
      `topgun/k8s/integration/`
- [ ] Task: Delete fixture dirs `topgun/k8s/pipelines/`, `topgun/k8s/certs/`, and
      `topgun/tasks/` (referenced only by the deleted suite — verified via repo-wide grep)
- [ ] Task: Keep `topgun/exec.go` / `topgun/fly.go`; verify live suites still
      compile: `go vet ./topgun/...`
- [ ] Task: Sweep for dangling references: `git grep -l "topgun/k8s\b" -- ':!topgun/k8s/integration'`
      across Makefile, deploy/, docs, TESTING.md; fix any hits
- [ ] Task: Phase 1 Manual Verification — `go build ./... && make test-quick`

---

## Phase 2: Superseded standalone MCP server

- [ ] Task: Re-confirm tool parity superset (embedded `atc/api/mcpserver/tools.go`
      covers all 10 standalone tools: abort_build, get_build, get_build_log,
      get_pipeline, list_builds, list_jobs, list_pipelines, pause_pipeline,
      trigger_job, unpause_pipeline) — one-command grep check, record output in cgx.md
- [ ] Task: Delete `cmd/concourse-mcp/` (main.go + mcpserver/ + mcpserverfakes/)
- [ ] Task: Sweep for references: `git grep -n "concourse-mcp"` across the repo
      (build scripts, deploy/, docs, .mcp.json) — fix any hits
- [ ] Task: Phase 2 Manual Verification — `go build ./... && ginkgo ./atc/api/mcpserver/`

---

## Phase 3: Small dead-file sweep

- [ ] Task: Delete `vars/varsfakes/` AND remove the generate directives in
      `vars/variables.go` (lines 8–10: `//go:generate ... counterfeiter` +
      `//counterfeiter:generate . Variables`); verify `go generate ./vars/...`
      no longer recreates the fake and `ginkgo ./vars/` is green
- [ ] Task: Delete `atc/cmd/atc/` (vestigial entrypoint; zero references)
- [ ] Task: Delete `skymarshal/logger/` (zero importers)
- [ ] Task: Delete `atc/db/migration/cli/` and update the stale pointer at
      `CONTRIBUTING.md:395`
- [ ] Task: Delete `Dockerfile.testrunner` (zero references; active runners live in deploy/)
- [ ] Task: Delete `hack/bosh-topgun` (BOSH-era script)
- [ ] Task: `git rm package-lock.json` (build is Yarn-only via corepack +
      yarn.lock in Dockerfile.build); add a `package-lock.json` ignore entry if
      npm regenerates it locally
- [ ] Task: Delete `atc/integration/team_migration_test.go` (XDescribe'd since the
      ATC 3.13 era; `randomString` helper has no other callers);
      `ginkgo ./atc/integration/` green
- [ ] Task: Phase 3 Manual Verification — `go build ./... && go vet ./... && make test-quick`

---

## Phase 4: Final verification & CI

- [ ] Task: Full sweep: `git grep -nE "concourse-mcp|bosh-topgun|varsfakes|skymarshal/logger|migration/cli|Dockerfile.testrunner"`
      returns no active-code hits (forge/ archive + memory notes excepted)
- [ ] Task: `make test-quick` + `make test-fly-integration` green locally
- [ ] Task: Compile-check both live K8s suites: `go vet ./topgun/...`
- [ ] Task: Commit (conventional: `chore(cleanup): remove dead topgun/k8s root suite, standalone MCP server, orphaned files`),
      push, and confirm the `k8s-e2e` pipeline (integration + behavioral) goes green
- [ ] Task: Phase 4 Manual Verification

---

## Key locations

- Orphaned suite: `topgun/k8s/*.go` (package k8s), `topgun/k8s/pipelines/`, `topgun/k8s/certs/`, `topgun/tasks/`
- Live suites (preserve): `topgun/k8s/integration/`, `topgun/k8s_behavioral/`, `topgun/exec.go`, `topgun/fly.go`
- MCP: delete `cmd/concourse-mcp/`; embedded server `atc/api/mcpserver/` wired in `atc/api/handler.go`
- Small files: `vars/varsfakes/`, `vars/variables.go:8-10`, `atc/cmd/atc/`,
  `skymarshal/logger/`, `atc/db/migration/cli/`, `CONTRIBUTING.md:395`,
  `Dockerfile.testrunner`, `hack/bosh-topgun`, `package-lock.json`,
  `atc/integration/team_migration_test.go`
