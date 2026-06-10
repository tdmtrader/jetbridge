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

---

## Patterns Observed

---

## Missing Capabilities

---

## Insights & Suggestions

---
