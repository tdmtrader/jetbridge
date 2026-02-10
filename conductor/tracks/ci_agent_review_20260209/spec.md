# Spec: CI Agent Review Task

**Track ID:** `ci_agent_review_20260209`
**Type:** feature

## Overview

A standalone, plugin-style tool (`ci-agent-review`) that runs autonomous AI agents to perform test-driven code review within Concourse CI pipelines. The tool accepts a repository, instructs an AI agent to write failing tests that prove defects, runs those tests, and outputs a versioned `review.json` artifact with scored, file-anchored findings. Only issues demonstrated by a failing test are scored — everything else is advisory commentary.

The tool is an **independent binary** with its own Go module, no imports from the Concourse codebase. Integration with Concourse is through the task YAML interface (inputs/outputs). It optionally stores review history in a dedicated PostgreSQL schema (`ci_agent`) in the shared Concourse database.

## Motivation

Code review is a bottleneck. AI agents can provide consistent, tunable review — but unstructured prose can't be programmatically consumed. More importantly, subjective style complaints are noise. The only objective proof of a defect is a failing test.

By defining a TDD-oriented review process with a versioned output schema, review becomes a composable pipeline step: score thresholds gate deploys, findings annotate PRs, and metrics trend over time — all grounded in reproducible test evidence.

## Architecture: Separation Model

```
┌─────────────────────────────────────┐
│         Concourse Web Node          │
│                                     │
│  ┌─────────────┐  ┌──────────────┐  │
│  │ Pipeline     │  │ Web UI       │  │
│  │ Scheduler    │  │ (future:     │  │
│  │              │  │  review tab) │  │
│  └──────┬──────┘  └──────┬───────┘  │
│         │                │          │
│         │ task YAML      │ REST API │
│         │ (inputs/       │ (future) │
│         │  outputs)      │          │
└─────────┼────────────────┼──────────┘
          │                │
          ▼                ▼
┌─────────────────────────────────────┐
│     ci-agent-review (standalone)    │
│                                     │
│  Own Go module: ci-agent/go.mod     │
│  No imports from atc/, worker/      │
│  Binary: cmd/ci-agent-review/       │
│                                     │
│  ┌─────────┐ ┌────────┐ ┌───────┐  │
│  │ Adapter  │ │ Runner │ │ Score │  │
│  │ (Claude, │ │ (go    │ │ (from │  │
│  │  Codex)  │ │  test) │ │ tests)│  │
│  └─────────┘ └────────┘ └───────┘  │
└─────────────────┬───────────────────┘
                  │ optional
                  ▼
┌─────────────────────────────────────┐
│  PostgreSQL (shared instance)       │
│                                     │
│  Schema: public    → Concourse      │
│  Schema: ci_agent  → Review history │
│                                     │
└─────────────────────────────────────┘
```

### Integration Points (clearly bounded)

| Integration | Mechanism | Required | Notes |
|-------------|-----------|----------|-------|
| Pipeline execution | Concourse task YAML (inputs/outputs) | Yes | Only interface for v1 |
| Review history | PostgreSQL `ci_agent` schema | No | Opt-in via `DATABASE_URL` env |
| Web UI review tab | REST API on web node | No | Future track, not v1 |
| PR comments | Downstream task reads `review.json` | No | Separate consumer task |

### Independence Guarantees

- `ci-agent/` has its own `go.mod` — no dependency on Concourse modules
- Buildable and runnable outside the Concourse repo
- Can be used in any CI system that supports running a binary with inputs/outputs
- Concourse-specific behavior (env vars, input/output paths) is an adapter layer, not core logic

## User-Facing Design

### Pipeline YAML

```yaml
jobs:
- name: review
  plan:
  - get: my-repo
    trigger: true
  - task: ai-review
    file: my-repo/ci/tasks/ci-agent-review.yml
    input_mapping:
      repo: my-repo
    params:
      AGENT_CLI: claude-code
      REVIEW_PROFILE: default
      SCORE_THRESHOLD: "7.0"
      FAIL_ON_CRITICAL: "true"
  - task: gate
    file: my-repo/ci/tasks/review-gate.yml
    input_mapping:
      review: review
```

### Task Definition (`ci/tasks/ci-agent-review.yml`)

```yaml
platform: linux
image_resource:
  type: registry-image
  source:
    repository: ghcr.io/jetbridge/ci-agent
    tag: latest

inputs:
- name: repo
- name: review-config
  optional: true

outputs:
- name: review              # review.json + tests/

params:
  AGENT_CLI: claude-code
  REVIEW_PROFILE: default
  SCORE_THRESHOLD: "0"
  FAIL_ON_CRITICAL: "false"
  REVIEW_PATHS: ""
  REVIEW_DIFF_ONLY: "false"
  BASE_REF: ""
  DATABASE_URL: ""           # optional: postgres://... for history

run:
  path: /usr/local/bin/ci-agent-review
```

### Two-Tier Findings Model

**Proven Issues** — demonstrated by a failing test
- The failing test IS the evidence. No test, no proven issue.
- Severity based on what the test demonstrates:
  - `critical` — security exploit, data loss, data corruption
  - `high` — crash, panic, unhandled error that propagates
  - `medium` — wrong result, incorrect behavior under valid input
  - `low` — edge case mishandling, degraded behavior
- Only proven issues affect the score

**Observations** — advisory, no failing test
- Style, complexity, naming, structural suggestions
- Zero score impact, never cause pipeline failure
- Agent acknowledges it could not prove these with a test

### Scoring Model

Start at 10.0. Deduct based on proven issues only:
- `critical`: -3.0 each
- `high`: -1.5 each
- `medium`: -1.0 each
- `low`: -0.5 each
- Floor at 0.0. Weights tunable in config.

Fully objective — score is a deterministic function of proven issues.

### Output Schema (`review.json` v1.0.0)

```json
{
  "schema_version": "1.0.0",
  "metadata": {
    "repo": "github.com/org/repo",
    "commit": "abc123def",
    "branch": "feature/xyz",
    "timestamp": "2026-02-09T17:00:00Z",
    "duration_seconds": 45,
    "agent_cli": "claude-code",
    "agent_model": "claude-opus-4-6",
    "files_reviewed": 24,
    "tests_generated": 8,
    "tests_failing": 3
  },
  "score": {
    "value": 7.5,
    "max": 10.0,
    "pass": true,
    "threshold": 7.0,
    "deductions": [
      { "issue_id": "001", "severity": "high", "points": -1.5 },
      { "issue_id": "002", "severity": "medium", "points": -1.0 }
    ]
  },
  "proven_issues": [
    {
      "id": "001",
      "severity": "high",
      "title": "Nil pointer dereference on empty config",
      "description": "LoadConfig returns nil without error when file is empty.",
      "file": "config/loader.go",
      "line": 42,
      "end_line": 48,
      "test_file": "review/tests/001_nil_config_test.go",
      "test_name": "TestLoadConfig_EmptyFile_ReturnsNilWithoutError",
      "test_output": "panic: runtime error: invalid memory address",
      "category": "correctness"
    }
  ],
  "observations": [
    {
      "id": "OBS-001",
      "title": "High cyclomatic complexity in processOrder",
      "description": "Function has 15 branches. Consider extracting helpers.",
      "file": "service/orders.go",
      "line": 88,
      "category": "maintainability"
    }
  ],
  "test_summary": {
    "total_generated": 8,
    "passing": 5,
    "failing": 3,
    "error": 0
  },
  "summary": "24 files reviewed. 8 tests generated, 3 failing. 3 proven issues. Score: 7.5/10 (PASS)."
}
```

### Review Config (`review.yml` v1)

```yaml
version: "1"

severity_weights:
  critical: 3.0
  high: 1.5
  medium: 1.0
  low: 0.5

categories:
  security: { enabled: true }
  correctness: { enabled: true }
  performance: { enabled: true }
  maintainability: { enabled: true }
  testing: { enabled: true }

include:
  - "**/*.go"
exclude:
  - "vendor/**"
  - "**/*_test.go"
  - "**/fakes/**"
```

### DAG Integration Patterns

```yaml
# Pattern 1: Review → Gate → Deploy
- name: deploy
  plan:
  - get: repo
    passed: [review]
  - get: review
    passed: [review]

# Pattern 2: Parallel security + quality reviews
- name: security-review
  plan:
  - task: review
    params: { REVIEW_PROFILE: security, FAIL_ON_CRITICAL: "true" }

- name: quality-review
  plan:
  - task: review
    params: { REVIEW_PROFILE: default }

# Pattern 3: Diff-only PR review
- name: pr-review
  plan:
  - task: review
    params: { REVIEW_DIFF_ONLY: "true", BASE_REF: main }
```

### Database Schema (`ci_agent` — optional)

```sql
CREATE SCHEMA IF NOT EXISTS ci_agent;

CREATE TABLE ci_agent.reviews (
  id            SERIAL PRIMARY KEY,
  repo          TEXT NOT NULL,
  commit_sha    TEXT NOT NULL,
  branch        TEXT,
  score         NUMERIC(4,2) NOT NULL,
  pass          BOOLEAN NOT NULL,
  threshold     NUMERIC(4,2) NOT NULL,
  proven_count  INTEGER NOT NULL,
  obs_count     INTEGER NOT NULL,
  review_json   JSONB NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_reviews_repo_commit ON ci_agent.reviews (repo, commit_sha);
CREATE INDEX idx_reviews_repo_created ON ci_agent.reviews (repo, created_at DESC);
```

## Requirements

1. Standalone Go module (`ci-agent/go.mod`) with no imports from Concourse packages.
2. A CLI binary (`ci-agent-review`) that orchestrates: config → agent → test generation → test execution → classification → scoring → output.
3. A versioned JSON output schema (`review.json` v1.0.0) with metadata, score, proven issues, observations, and test summary.
4. Issues are only "proven" if demonstrated by a failing test. All other concerns are observations with zero score impact.
5. Proven issues are anchored to file/line/end_line with the failing test file and captured test output.
6. Severity (critical/high/medium/low) reflects what the failing test demonstrates, not subjective opinion.
7. Score is deterministic: start at 10, deduct per proven issue severity, floor at 0.
8. Configurable via `review.yml`: severity weights, enabled categories, file include/exclude patterns.
9. Built-in profiles (default, security, strict) as preset configs.
10. Agent CLI is pluggable via adapter interface. v1 ships Claude Code adapter.
11. Diff-only mode reviews only files changed since `BASE_REF`.
12. Task exit code: 0 = pass, 1 = fail (score below threshold or critical issues when `FAIL_ON_CRITICAL=true`).
13. Companion `review-gate.yml` task reads `review.json` and enforces pass/fail.
14. Optional PostgreSQL storage in dedicated `ci_agent` schema for review history.
15. Concourse task YAML definitions are the only integration point with Concourse for v1.

## Acceptance Criteria

- [ ] `ci-agent/` builds independently with its own `go.mod`
- [ ] `ci-agent-review` binary runs end-to-end and produces valid `review.json`
- [ ] `review.json` validates against the v1.0.0 schema
- [ ] Proven issues have corresponding failing test files in `review/tests/`
- [ ] Failing tests actually fail when run against the repo
- [ ] Passing agent-generated tests do not appear as proven issues
- [ ] Score = 10 - sum(deductions), deterministic from proven issues
- [ ] Config controls severity weights, categories, include/exclude
- [ ] Built-in profiles produce expected behavior
- [ ] Claude Code adapter dispatches and parses output
- [ ] Diff-only mode scopes review to changed files
- [ ] Task exits 0/1 based on threshold and critical findings
- [ ] `review-gate.yml` reads and gates on `review.json`
- [ ] PostgreSQL history storage works when `DATABASE_URL` provided
- [ ] No imports from `atc/`, `worker/`, or any Concourse package

## Out of Scope

- Multi-language support beyond Go (v1 targets Go repos)
- Web UI integration (review tab on Concourse web node — future track)
- PR comment posting (downstream consumer, separate task)
- Historical score trending / dashboard
- Agent CLI adapters beyond Claude Code (interface defined, others deferred)
- Review caching / incremental review across builds
- Custom rule plugin system
- Readiness probes or health checks for the review container
