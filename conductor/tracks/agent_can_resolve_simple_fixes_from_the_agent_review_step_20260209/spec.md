# Spec: Agent Fix Step — Resolve proven issues from agent-review output

**Track ID:** `agent_can_resolve_simple_fixes_from_the_agent_review_step_20260209`
**Type:** feature

## Overview

A new agent step that consumes the `review.json` output from the CI Agent Review step plus a git repo, attempts to fix each ProvenIssue, verifies fixes against the review's proving tests, commits changes, runs the full test suite for regression safety, and outputs the modified git repo as a Concourse artifact. A downstream PUT step pushes the branch and a follow-up task creates a PR via the GitHub API.

## How It Fits the Pipeline

```
get: repo
  |
  v
task: agent-review          ← produces review.json + review/tests/
  |
  v
task: agent-fix             ← THIS TRACK: consumes review + repo, outputs fixed-repo + fix-report.json
  |
  v
put: repo                   ← pushes fixed-repo to feature branch
  |
  v
task: create-pr             ← creates GitHub PR from fix branch
```

The fix step is a standalone Concourse task. Its output is a modified git repo directory that the standard `git` resource can push via PUT. PR creation is a separate lightweight task using the GitHub CLI.

## Inputs

| Artifact | Source | Contents |
|----------|--------|----------|
| `repo` | git resource GET | The source repository at the reviewed commit |
| `review` | agent-review task output | `review.json` + `tests/` directory with proving test files |

## Outputs

| Artifact | Contents |
|----------|----------|
| `fixed-repo` | The git repo with fix commits applied on a new branch |
| `fix-report` | `fix-report.json` — structured report of what was fixed, skipped, and why |

## Requirements

1. Parse `review.json` (ReviewOutput schema v1.0.0) and extract ProvenIssues
2. For each ProvenIssue, dispatch an agent to generate a code fix targeting the specific file and line range
3. Verify each fix by running the issue's proving test — the test must go from failing to passing
4. Commit each verified fix individually (one commit per issue, atomic and revertable)
5. After all fixes, run the project's full test suite to detect regressions
6. If a fix causes a regression, revert that fix's commit and mark the issue as skipped
7. Output the modified repo with all surviving fix commits on a feature branch
8. Output a structured `fix-report.json` describing outcomes for every issue
9. Exit code 0 if at least one fix was applied with no regressions; exit code 1 otherwise
10. The fix step binary lives in `ci-agent/` as part of the standalone module (zero Concourse imports)
11. A companion task YAML and PR-creation script provide the Concourse integration

## Fix Report Schema (fix-report.json)

```json
{
  "schema_version": "1.0.0",
  "metadata": {
    "repo": "https://github.com/org/repo.git",
    "base_commit": "abc123",
    "fix_branch": "fix/agent-review-abc123",
    "head_commit": "def456",
    "timestamp": "2026-02-09T17:00:00Z",
    "duration_seconds": 120,
    "agent_cli": "claude-code",
    "review_file": "review/review.json"
  },
  "fixes": [
    {
      "issue_id": "001",
      "status": "fixed",
      "commit_sha": "aaa111",
      "files_changed": ["config/loader.go"],
      "test_passed": true,
      "attempts": 1
    }
  ],
  "skipped": [
    {
      "issue_id": "002",
      "status": "skipped",
      "reason": "failed_verification",
      "attempts": 2,
      "last_error": "test still fails after 2 attempts"
    }
  ],
  "summary": {
    "total_issues": 5,
    "fixed": 3,
    "skipped": 2,
    "regression_free": true,
    "exit_code": 0
  }
}
```

### Skip Reasons

| Reason | Meaning |
|--------|---------|
| `failed_verification` | Agent generated a fix but proving test still fails after max retries |
| `test_regression` | Fix passed its proving test but caused other tests to fail; reverted |
| `agent_error` | Agent adapter returned an error (timeout, crash, malformed output) |
| `compilation_error` | Fix introduced a compilation error |

## Acceptance Criteria

- [ ] Parses review.json and iterates ProvenIssues in severity order (critical first)
- [ ] For each issue, agent receives: issue description, file content, proving test code
- [ ] Each fix is verified by running the proving test before committing
- [ ] Each fix is committed individually with a descriptive message referencing the issue ID
- [ ] Full test suite runs after all fixes; regressions cause per-fix rollback
- [ ] fix-report.json validates against the schema and accurately reflects outcomes
- [ ] Modified repo is output on a feature branch ready for PUT
- [ ] CLI reads Concourse-convention environment variables for configuration
- [ ] Exit code 0 when fixes applied successfully; exit code 1 when no fixes or errors
- [ ] Works end-to-end in a Concourse pipeline: review -> fix -> put -> create-pr

## Out of Scope

- Fixing Observations (advisory only, no proving test, no score impact)
- Complex multi-file refactors that span architectural boundaries
- Interactive human-in-the-loop during fix generation (future track)
- Auto-merge of PRs (human review required)
- Fixing issues in languages other than Go (this repo is Go-only)
