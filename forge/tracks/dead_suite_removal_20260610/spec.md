# Spec: Dead Suite Removal

## Overview (Why)

A repo-wide cleanup audit (2026-06-10, six parallel exploration passes + manual
verification) found ~5k LOC of verified-dead code that survived the earlier legacy
cleanup tracks (`legacy-cleanup-20260210`, `dead_code_cleanup_20260310`,
`deprecate_pvc_and_spdy_artifact_backends_20260327`). It falls into three buckets:
a pre-JetBridge E2E test suite that no CI target runs, a standalone MCP server
superseded by the ATC-embedded one, and a handful of small orphaned files. Removing
them shrinks the surface a maintainer (or agent) has to read and reason about, and
eliminates misleading code that documents an architecture (TSA workers, GKE/PKS,
BOSH) that no longer exists. No JetBridge functionality is affected — everything
removed is unreachable from the binaries, the Makefile, and CI.

## Requirements (What)

1. Delete the orphaned `topgun/k8s/` **root** suite (the 15 `*_test.go` files in
   `topgun/k8s/` itself — `package k8s` — plus its fixtures `topgun/k8s/pipelines/`,
   `topgun/k8s/certs/`, and `topgun/tasks/`). Verified: no Makefile target or
   deploy/*.yml pipeline runs `./topgun/k8s/`; the suite configures TSA SSH worker
   registration (`tsa.hosts[0]`) that no longer exists in the binary; untouched
   since the Feb 2026 legacy-removal commit.
2. Delete the superseded standalone MCP server `cmd/concourse-mcp/` (~2,800 LOC).
   Verified: the embedded server at `atc/api/mcpserver/` (wired into
   `atc/api/handler.go`) implements all 10 of the standalone's tools plus ~9 more;
   nothing in Makefile/build.sh/deploy/docs references `concourse-mcp`.
3. Delete small verified-dead files:
   - `vars/varsfakes/` + the `//go:generate` / `//counterfeiter:generate`
     directives in `vars/variables.go` (lines 8–10) so `go generate` doesn't
     recreate the fake (zero importers).
   - `atc/cmd/atc/` — vestigial entrypoint superseded by `cmd/concourse`.
   - `skymarshal/logger/` — logrus→lager bridge, zero importers.
   - `atc/db/migration/cli/` — standalone migration CLI, unreferenced by any
     build/CI; also fix the dangling reference at `CONTRIBUTING.md:395`.
   - `Dockerfile.testrunner` — zero references (active runners are in `deploy/`).
   - `hack/bosh-topgun` — BOSH-era script targeting removed infrastructure.
   - `package-lock.json` — build is Yarn-only (`Dockerfile.build` uses corepack +
     `yarn.lock`); the npm lockfile is misleading drift.
   - `atc/integration/team_migration_test.go` — `XDescribe`'d "ATC 3.13" migration
     test, permanently disabled; its only helper (`randomString`) has no other
     callers in the package.

## Must Preserve

- `topgun/exec.go` and `topgun/fly.go` (`package topgun`) — dot-imported by the
  **live** suites `topgun/k8s/integration/` and `topgun/k8s_behavioral/`.
- `topgun/k8s/integration/` and `topgun/k8s_behavioral/` in full (the live K8s
  test tiers; their fixtures are inline strings, not files from the deleted dirs).
- `atc/api/mcpserver/` (the embedded MCP server) and its wiring in
  `atc/api/handler.go`.
- `cmd/oom-trigger/` — used by the behavioral suite.
- Root `Dockerfile` (used by `docker-compose.yml` `build: .`) and
  `Dockerfile.local` (TESTING.md's `concourse-local:latest` for K8s tests).
- All DB migrations (history), `deploy/borg-pipeline.yml` (live theborg deploy).

## Acceptance Criteria

- All listed paths removed from git; `git grep` finds no dangling references to
  the deleted packages/paths in active code, Makefile, CI pipelines, or docs.
- `go build ./...` and `go vet ./...` pass (modulo the pre-existing
  `atc/exec/artifact_input_step_test.go` vet failure, which is out of scope).
- `make test-quick` green; `ginkgo ./atc/api/mcpserver/ ./vars/ ./atc/integration/`
  green; `go vet ./topgun/...` compiles the live suites.
- CI `k8s-e2e` pipeline (integration + behavioral jobs) green on the merge commit.

## Out of Scope (separate tracks proposed by the same audit)

- Helm chart TSA remnants (`tsaPort`, `tsa` service/container ports) and the
  broader docs drift (CONTRIBUTING.md BOSH/worker sections, TESTING.md/CLAUDE.md
  KinD→K3s) — "chart-and-docs-drift" track.
- Legacy worker DB layer (`worker_resource_certs.go`, `worker_resource_cache.go`,
  `worker_base_resource_type.go`, `worker_task_cache.go`) — needs live-deployment
  verification; "db-legacy-worker-tables" track.
- Shared-helper extraction for the duplicated K8s test helpers.
- The fate of the top-level `integration/` docker-compose suite (LDAP/vault/
  upgrade-downgrade coverage) — user decision pending.
