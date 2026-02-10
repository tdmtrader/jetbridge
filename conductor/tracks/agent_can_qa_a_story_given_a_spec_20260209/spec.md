# Spec: CI Agent QA Task

**Track ID:** `agent_can_qa_a_story_given_a_spec_20260209`
**Type:** feature

## Overview

A standalone, plugin-style tool (`ci-agent-qa`) that runs autonomous AI agents to perform specification-driven functional QA within Concourse CI pipelines. The tool accepts a repository and a spec document (in the format produced by the agent plan step), analyzes test coverage against the spec's functional requirements, identifies coverage gaps, and outputs a versioned `qa.json` artifact with a requirement-by-requirement coverage assessment.

The tool is an **independent binary** within the existing `ci-agent/` Go module (shared with `ci-agent-review`). Integration with Concourse is through the task YAML interface (inputs/outputs). It optionally stores QA history in the shared `ci_agent` PostgreSQL schema.

**Two-tier design:**
- **Tier 1 (this track):** Code-level QA — the agent maps spec requirements to existing tests, identifies gaps, generates tests for uncovered requirements, runs them, and scores coverage completeness.
- **Tier 2 (future, spec defined here):** Browser-based functional QA — the agent navigates a running application to verify behavior against the spec. Tier 2 outputs a `browser-qa-plan.md` that can be fed into an interactive browser session (e.g., Claude in Chrome plugin) for manual or assisted execution.

## Motivation

Code review catches defects in implementation. QA catches gaps between *what was specified* and *what was built*. These are fundamentally different questions:

- **Review asks:** "Is this code correct?" (defect detection)
- **QA asks:** "Does this code do what was specified?" (requirement coverage)

An agent that can read a spec, map requirements to tests, and identify uncovered behavior turns QA from a manual checklist into a composable pipeline step. Combined with the review step, you get both correctness and completeness gating.

## Architecture

```
┌─────────────────────────────────────┐
│         Concourse Pipeline          │
│                                     │
│  get: repo ─► task: ai-qa ─► gate  │
│  get: spec     │                    │
│                │ inputs/outputs     │
└────────────────┼────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────┐
│       ci-agent-qa (standalone)      │
│                                     │
│  Shared module: ci-agent/go.mod     │
│  Binary: cmd/ci-agent-qa/          │
│                                     │
│  ┌──────────┐ ┌────────┐ ┌──────┐  │
│  │ Spec     │ │ Mapper │ │ Gap  │  │
│  │ Parser   │ │ (req→  │ │ Test │  │
│  │          │ │  test) │ │ Gen  │  │
│  └──────────┘ └────────┘ └──────┘  │
│  ┌──────────┐ ┌────────┐ ┌──────┐  │
│  │ Runner   │ │ Scorer │ │ Tier2│  │
│  │ (shared) │ │        │ │ Plan │  │
│  └──────────┘ └────────┘ └──────┘  │
└─────────────────────────────────────┘
```

## Input: Spec Format

The QA agent expects a spec document (Markdown) with numbered requirements and acceptance criteria. This matches the output of the agent plan step:

```markdown
## Requirements
1. Widget creation requires a name and type.
2. Duplicate widget names return a 409 Conflict.
3. Widgets are soft-deleted (recoverable within 30 days).

## Acceptance Criteria
- [ ] POST /widgets with valid payload returns 201
- [ ] POST /widgets with duplicate name returns 409
- [ ] DELETE /widgets/:id sets deleted_at timestamp
- [ ] GET /widgets excludes soft-deleted items
- [ ] GET /widgets?include_deleted=true includes soft-deleted items
```

The agent parses this into a structured list of requirements and acceptance criteria, then maps each to existing test coverage.

## Tier 1: Code-Level QA

**Requirement Mapping** — For each requirement/acceptance criterion in the spec, the agent:
1. Searches the codebase for existing tests that exercise that behavior.
2. Classifies coverage: `covered` (test exists and passes), `partial` (test exists but doesn't cover all cases), `uncovered` (no test found), `failing` (test exists but fails).
3. For `uncovered` requirements, generates a test that would verify the behavior.
4. Runs generated tests against the codebase.
5. A generated test that **passes** proves the requirement is implemented (just untested). A generated test that **fails** proves the requirement is not implemented or broken.

**Coverage Model:**
- `covered` — Existing test found that exercises this requirement. +1.0 per requirement.
- `partial` — Test exists but doesn't cover all acceptance criteria. +0.5 per requirement.
- `uncovered_implemented` — No test, but generated test passes (code works, just missing test). +0.75 per requirement.
- `uncovered_broken` — No test, and generated test fails (requirement not met). +0.0 per requirement.
- `failing` — Existing test that fails. +0.0 per requirement.

**Score:** `(sum of coverage points) / (total requirements) * 10.0` — normalized to 0-10 scale like the review step.

## Tier 2: Browser QA Plan (output only, not executed)

For requirements that involve UI behavior, HTTP endpoints, or user-facing flows, the agent produces a `browser-qa-plan.md`:

```markdown
# Browser QA Plan

## Generated from: spec.md
## Target: http://localhost:8080

### Flow 1: Widget Creation (Happy Path)
**Verifies:** Requirement 1, AC 1

1. Navigate to /widgets/new
2. Fill "Name" field with "Test Widget"
3. Fill "Type" dropdown with "Standard"
4. Click "Create" button
5. **Assert:** Redirected to /widgets/:id
6. **Assert:** Success banner visible with text "Widget created"
7. **Assert:** Widget name displayed as "Test Widget"

### Flow 2: Duplicate Widget Name (Error Path)
**Verifies:** Requirement 2, AC 2

1. Navigate to /widgets/new
2. Fill "Name" field with "Existing Widget" (pre-seeded)
3. Click "Create" button
4. **Assert:** Error banner visible with text containing "already exists"
5. **Assert:** Form remains on /widgets/new (no redirect)
```

This file is a **pure output artifact**. It is not executed by the QA agent. An operator can:
- Review it manually as a QA checklist
- Feed it into an interactive browser session with Claude in Chrome for assisted execution
- Use it as input for future automated browser QA tooling

## User-Facing Design

### Pipeline YAML

```yaml
jobs:
- name: qa
  plan:
  - get: my-repo
    trigger: true
  - get: spec
    resource: spec-repo
  - task: ai-qa
    file: my-repo/ci/tasks/ci-agent-qa.yml
    input_mapping:
      repo: my-repo
      spec: spec
    params:
      AGENT_CLI: claude-code
      QA_PROFILE: default
      SCORE_THRESHOLD: "7.0"
      SPEC_FILE: spec/spec.md
  - task: gate
    file: my-repo/ci/tasks/qa-gate.yml
```

### Task Definition (`ci/tasks/ci-agent-qa.yml`)

```yaml
platform: linux
image_resource:
  type: registry-image
  source:
    repository: ghcr.io/jetbridge/ci-agent
    tag: latest

inputs:
- name: repo
- name: spec
- name: qa-config
  optional: true

outputs:
- name: qa              # qa.json + tests/ + browser-qa-plan.md

params:
  AGENT_CLI: claude-code
  QA_PROFILE: default
  SCORE_THRESHOLD: "0"
  SPEC_FILE: ""
  GENERATE_TESTS: "true"
  BROWSER_PLAN: "true"
  TARGET_URL: ""
  DATABASE_URL: ""

run:
  path: /usr/local/bin/ci-agent-qa
```

### Output Schema (`qa.json` v1.0.0)

```json
{
  "schema_version": "1.0.0",
  "metadata": {
    "repo": "github.com/org/repo",
    "commit": "abc123def",
    "branch": "feature/widgets",
    "timestamp": "2026-02-09T17:00:00Z",
    "duration_seconds": 60,
    "agent_cli": "claude-code",
    "agent_model": "claude-opus-4-6",
    "spec_file": "spec.md",
    "requirements_total": 5,
    "requirements_covered": 3,
    "tests_generated": 2,
    "tests_passing": 1,
    "tests_failing": 1
  },
  "score": {
    "value": 7.5,
    "max": 10.0,
    "pass": true,
    "threshold": 7.0
  },
  "requirements": [
    {
      "id": "R1",
      "text": "Widget creation requires a name and type",
      "status": "covered",
      "coverage_points": 1.0,
      "existing_tests": [
        { "file": "widget/widget_test.go", "name": "TestCreateWidget_ValidPayload" }
      ],
      "generated_test": null,
      "notes": ""
    },
    {
      "id": "AC4",
      "text": "GET /widgets excludes soft-deleted items",
      "status": "uncovered_broken",
      "coverage_points": 0.0,
      "existing_tests": [],
      "generated_test": {
        "file": "qa/tests/ac4_soft_delete_filter_test.go",
        "name": "TestGetWidgets_ExcludesSoftDeleted",
        "result": "fail",
        "output": "Expected 2 widgets, got 3 (soft-deleted included)"
      },
      "notes": "Soft-delete filtering not implemented in list query"
    }
  ],
  "gaps": [
    {
      "requirement_id": "AC4",
      "severity": "high",
      "description": "Soft-delete filtering not implemented — deleted widgets appear in list results"
    }
  ],
  "browser_qa_plan": "qa/browser-qa-plan.md",
  "summary": "5 requirements analyzed. 3 covered, 1 implemented but untested, 1 broken. Score: 7.5/10 (PASS)."
}
```

### QA Config (`qa.yml` v1)

```yaml
version: "1"

score_threshold: 7.0
generate_tests: true
browser_plan: true
target_url: ""

coverage_weights:
  covered: 1.0
  partial: 0.5
  uncovered_implemented: 0.75
  uncovered_broken: 0.0
  failing: 0.0

include:
  - "**/*.go"
exclude:
  - "vendor/**"
  - "**/*_test.go"
  - "**/fakes/**"
```

### DAG Integration Patterns

```yaml
# Pattern 1: Review + QA → Gate → Deploy
- name: deploy
  plan:
  - get: repo
    passed: [review, qa]

# Pattern 2: QA feeds into agent-fix
- name: fix
  plan:
  - get: repo
    passed: [qa]
  - get: qa
    passed: [qa]
  - task: agent-fix
    params:
      QA_REPORT: qa/qa.json

# Pattern 3: Review + QA parallel
- name: review
  plan:
  - task: ai-review
- name: qa
  plan:
  - task: ai-qa
- name: gate
  plan:
  - get: repo
    passed: [review, qa]
```

### Database Schema (`ci_agent` — optional)

```sql
CREATE TABLE ci_agent.qa_results (
  id                SERIAL PRIMARY KEY,
  repo              TEXT NOT NULL,
  commit_sha        TEXT NOT NULL,
  branch            TEXT,
  score             NUMERIC(4,2) NOT NULL,
  pass              BOOLEAN NOT NULL,
  threshold         NUMERIC(4,2) NOT NULL,
  requirements_total INTEGER NOT NULL,
  requirements_covered INTEGER NOT NULL,
  gaps_count        INTEGER NOT NULL,
  qa_json           JSONB NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_qa_results_repo_commit ON ci_agent.qa_results (repo, commit_sha);
CREATE INDEX idx_qa_results_repo_created ON ci_agent.qa_results (repo, created_at DESC);
```

## Requirements

1. Shared `ci-agent/go.mod` module (alongside `ci-agent-review`), no Concourse imports.
2. A CLI binary (`ci-agent-qa`) that orchestrates: spec parsing → requirement extraction → test mapping → gap test generation → test execution → coverage scoring → output.
3. A versioned JSON output schema (`qa.json` v1.0.0) with metadata, score, per-requirement coverage, gaps, and optional browser QA plan reference.
4. Spec parser handles Markdown with numbered requirements and checkbox-style acceptance criteria.
5. Requirement mapping searches the codebase for existing tests that exercise each requirement.
6. Coverage classification: `covered`, `partial`, `uncovered_implemented`, `uncovered_broken`, `failing`.
7. Gap tests are generated for uncovered requirements and executed. Pass = implemented but untested. Fail = requirement not met.
8. Score is deterministic: sum of coverage points / total requirements * 10, normalized to 0-10.
9. Configurable via `qa.yml`: score threshold, test generation toggle, browser plan toggle, include/exclude paths.
10. Agent CLI is pluggable via shared adapter interface (reuse from ci-agent-review).
11. Browser QA plan (`browser-qa-plan.md`) generated as output artifact for UI-related requirements.
12. Browser QA plan format is structured enough to be fed into an interactive browser session for manual/assisted execution.
13. Task exit code: 0 = pass, 1 = fail (score below threshold).
14. Companion `qa-gate.yml` task reads `qa.json` and enforces pass/fail.
15. Optional PostgreSQL storage in `ci_agent` schema for QA history.
16. Concourse task YAML definitions are the only Concourse integration for v1.

## Acceptance Criteria

- [ ] `ci-agent-qa` binary builds from shared `ci-agent/go.mod`
- [ ] Spec parser extracts requirements and acceptance criteria from Markdown
- [ ] Requirement mapper finds existing tests for each requirement
- [ ] Coverage classification is correct for all statuses
- [ ] Gap tests are generated for uncovered requirements
- [ ] Generated tests execute and results are captured
- [ ] `qa.json` validates against the v1.0.0 schema
- [ ] Score computation is deterministic from coverage data
- [ ] Browser QA plan is generated for UI-relevant requirements
- [ ] Browser QA plan format is actionable (step-by-step, with assertions)
- [ ] Config controls threshold, test gen, browser plan, include/exclude
- [ ] Task exits 0/1 based on threshold
- [ ] `qa-gate.yml` reads and gates on `qa.json`
- [ ] PostgreSQL QA history works when `DATABASE_URL` provided
- [ ] No imports from `atc/`, `worker/`, or any Concourse package
- [ ] Shared code with ci-agent-review (adapter, runner, storage) works correctly

## Out of Scope

- Browser QA execution (Tier 2 — interactive session, not pipeline automation)
- Multi-language support beyond Go (v1 targets Go repos)
- Visual regression / design diff tooling
- Spec format beyond Markdown (no Jira/YAML/JSON spec parsing in v1)
- Web UI integration
- PR comment posting from QA results
- Historical QA trend dashboard
- Test prioritization or risk-based testing
- Mutation testing
