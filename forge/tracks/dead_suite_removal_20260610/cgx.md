# Conductor Growth Experience (CGX)

**Track:** `dead_suite_removal_20260610`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Origin

- [2026-06-10] Created from a repo-wide cleanup audit (six parallel exploration
  agents + manual verification of every load-bearing claim). Tier-1 scope only:
  items verified dead with low removal risk. Audit corrections worth remembering:
  the agent-feedback API endpoints ARE consumed (web/elm/src/AgentFeedback/),
  root `Dockerfile` IS used (docker-compose `build: .`), `Dockerfile.local` IS
  used (TESTING.md → concourse-local:latest), and `deploy/borg-pipeline.yml` is
  the live theborg deployment — none of those may be removed despite initial
  audit reports flagging them.
- Sibling tracks proposed by the same audit (not yet created):
  chart-and-docs-drift (Helm tsaPort + CONTRIBUTING/TESTING/CLAUDE.md staleness),
  db-legacy-worker-tables (worker_resource_certs & friends, needs live
  verification), topgun helper dedup, and a user decision on the top-level
  `integration/` docker-compose suite.

## Frustrations & Friction

- [2026-06-10] Audit miss caught during Phase 1: the spec claimed
  `topgun/k8s/pipelines/` was referenced only by the deleted root suite, but the
  LIVE integration suite's pending spec (`k8s_pipeline_e2e_test.go` PIt
  "runs the full K8s validation pipeline") loaded
  `topgun/k8s/pipelines/k8s-validation.yml` from disk via CONCOURSE_REPO_ROOT.
  The original audit grep only checked actively-running specs' fixture usage.
  Fix: inlined the 94-line pipeline as `k8sValidationPipeline` const using the
  file's existing `writePipelineFile` idiom (sourced verbatim from git to avoid
  transcription errors). Lesson: when verifying "fixture only used by X", grep
  for the fixture's *filename* repo-wide, not just direct path references —
  path-join construction (`filepath.Join(root, "topgun", "k8s", ...)`) defeats
  full-path greps.

---

## Patterns Observed

- [2026-06-10] Phase 1 verification surfaced TWO unrelated pre-existing failures
  in `make test-quick`, both attributed via a clean-HEAD worktree
  (`git worktree add /tmp/headcheck HEAD` → run same suite there):
  1. REAL pre-existing bug: migrations 1773105502/1773105503 landed without
     bumping `jetbridgeHeadMigration` (legacy_upgrade_test.go:37) or
     `JETBRIDGE_VERSION` (docs/migration/migrate-preflight.sh:29) — 5 specs
     red on HEAD. Fixed as standalone commit 2fe8226c08. Note: there is no CI
     guard tying "new migration added" → "legacy-upgrade constants updated";
     docs (DATABASE-MIGRATION-RUNBOOK.md, schema-delta.md) still say 1773105501.
  2. Flake ROOT-CAUSED and fixed (85110f79b5): `atc.EnableGlobalResources` is
     package-global; ~6 atc/db specs set it true in BeforeEach without reset,
     leaking into later specs in the same Ginkgo process. The new
     scope-deprecation specs need the unique-history default (deprecation is
     skipped for global scopes), so they failed whenever a global-resources
     spec was randomly scheduled before them — full-suite runs flaked by seed,
     focused runs always passed. Fix: reset the flag in the suite-level
     BeforeEach (db_suite_test.go). Diagnostic that cracked it: failures
     correlated with full-suite parallel runs but not focused runs → suspect
     global state, grep writes to the global in _test.go files.
- [2026-06-10] Git hygiene gotcha: `git rm` stages immediately — a later
  `git add <specific files> && git commit` swept the staged deletions into the
  wrong commit. Had to `git reset --mixed` and recommit in two clean pieces.
  When mixing staged deletions with selective commits, commit the deletions
  first or use `git commit -- <paths>`.

---

## Missing Capabilities

---

## Insights & Suggestions

---
