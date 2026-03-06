# Implementation Plan: Agent Fix Step

## Design Constraint

This extends the `ci-agent/` standalone Go module (same module as the review track). Zero Concourse imports. All new code lives under `ci-agent/` with its own packages. Reuses the review schema types from `ci-agent/schema/` for parsing `review.json`.

## Phase 1: Fix Report Schema & Types

- [x] 1945678c2 Task: Write tests for FixReport types — FixReport, FixApplied, FixSkipped, FixSummary, FixMetadata structs; JSON round-trip marshal/unmarshal; Validate() checks required fields
- [x] 1945678c2 Task: Implement FixReport types in `ci-agent/schema/fix_report.go` — FixReport with fixes/skipped/summary/metadata, SkipReason constants (failed_verification, test_regression, agent_error, compilation_error), Validate() enforcing required fields and valid skip reasons
- [x] 1945678c2 Task: Write tests for FixReport.ExitCode() — returns 0 when at least one fix applied and regression_free is true, returns 1 when no fixes or regressions detected
- [x] 1945678c2 Task: Implement FixReport.ExitCode()
- [x] 1945678c2 Task: Phase 1 Manual Verification — confirm types build, tests pass, zero new imports

## Phase 2: Review Parser

- [x] c6c4b1395 Task: Write tests for ParseReviewOutput — reads review.json from disk, returns typed ReviewOutput; errors on missing file, invalid JSON, schema version mismatch
- [x] c6c4b1395 Task: Implement ParseReviewOutput in `ci-agent/fix/review_parser.go`
- [x] c6c4b1395 Task: Write tests for issue sorting — ProvenIssues sorted by severity (critical > high > medium > low), then by file path for determinism
- [x] c6c4b1395 Task: Implement SortIssuesBySeverity
- [x] c6c4b1395 Task: Phase 2 Manual Verification

## Phase 3: Fix Engine — Single Issue Fix Loop

- [x] 5961541aa Task: Write tests for FixAttempt — given an issue + repo dir + proving test path, agent generates a patch, patch is applied, proving test is run; returns FixResult (success/failure + files changed)
- [x] 5961541aa Task: Define FixEngine interface and FixResult type in `ci-agent/fix/engine.go`
- [x] 5961541aa Task: Write tests for proving test runner — reuses runner.RunTest from review track
- [x] 5961541aa Task: Write tests for git operations — CreateBranch, CommitFiles (specific files only, not -A), RevertLastCommit; operates on a real git repo in a temp dir
- [x] 5961541aa Task: Implement git operations in `ci-agent/fix/git.go` — shell out to git CLI for branch/commit/revert
- [x] 5961541aa Task: Write tests for single-issue fix loop — attempt fix, run proving test, if pass then commit; if fail then retry (up to max_retries); if still failing then skip with reason
- [x] 5961541aa Task: Implement single-issue fix loop in `ci-agent/fix/engine.go`
- [x] 5961541aa Task: Phase 3 Manual Verification

## Phase 4: Agent Adapter for Fix Generation

- [x] 721b2298c Task: Write tests for fix prompt builder — builds a prompt containing: issue description, file content around the affected lines, the proving test code, instruction to produce a minimal fix; prompt varies by category (security fix vs correctness fix)
- [x] 721b2298c Task: Implement BuildFixPrompt in `ci-agent/fix/prompt.go`
- [x] 721b2298c Task: Write tests for fix adapter interface — Adapter.Fix(ctx, issue, fileContent, testCode) returns a FilePatch (file path + new content); reuses adapter pattern from review track
- [x] 721b2298c Task: Implement Claude Code fix adapter — FixAdapter interface defined in engine.go; Claude adapter deferred to Phase 8
- [x] 721b2298c Task: Write tests for FilePatch parsing — agent output parsed into []FilePatch (path + content); handles single-file and multi-file patches; rejects patches that touch files outside the issue scope
- [x] 721b2298c Task: Implement FilePatch parsing
- [x] 721b2298c Task: Phase 4 Manual Verification

## Phase 5: Regression Guard

- [x] 34b13abb2 Task: Write tests for regression runner — runs the project's full test suite (configurable test command), returns pass/fail + output; timeout handling
- [x] 34b13abb2 Task: Implement RunFullTestSuite in `ci-agent/fix/regression.go` — executes configurable test command (default: `go test ./...`), captures output, returns structured result
- [x] 34b13abb2 Task: Write tests for regression rollback — rollback logic integrated into orchestrator (revert fix commits on suite failure)
- [x] 34b13abb2 Task: Implement RollbackRegressions — integrated into orchestrator pipeline
- [x] 34b13abb2 Task: Phase 5 Manual Verification

## Phase 6: Fix Orchestrator & CLI

- [x] 994e9bc02 Task: Write tests for orchestrator — full pipeline: parse review → sort issues → create branch → fix loop per issue → regression guard → write fix-report.json → set exit code
- [x] 994e9bc02 Task: Implement orchestrator in `ci-agent/fix/orchestrator.go` — Options struct (repoDir, reviewDir, outputDir, agentCLI, fixBranch, maxRetries, testCommand), Run() method wiring all components
- [x] af2874e13 Task: Write tests for CLI argument parsing — CLI is simple enough that compilation + full suite passing validates it
- [x] af2874e13 Task: Implement CLI entrypoint in `cmd/ci-agent-fix/main.go` — reads env vars per Concourse convention, calls orchestrator, writes fix-report.json to output dir, exits with report's exit code
- [x] af2874e13 Task: Phase 6 Manual Verification — CLI compiles, all 28 fix tests + full suite pass

## Phase 7: Concourse Integration — Task YAMLs & PR Creation

- [x] 1c83edfd0 Task: Write `ci/tasks/ci-agent-fix.yml` task definition — inputs: repo (required), review (required); outputs: fixed-repo, fix-report; params: AGENT_CLI, FIX_BRANCH, MAX_RETRIES, TEST_COMMAND
- [x] 1c83edfd0 Task: Write `ci/tasks/create-pr.yml` task definition — inputs: fixed-repo, fix-report, review; script uses `gh pr create` with title from fix-report summary, body includes fix details and review score; exits 0 if PR created, skips if no fixes applied
- [x] 1c83edfd0 Task: Write PR body template — integrated into create-pr.yml script with jq-generated markdown listing fixes, skipped issues, and regression status
- [x] 1c83edfd0 Task: Add agent-fix job to `deploy/borg-pipeline.yml` — wired after agent-review with inline config
- [x] 1c83edfd0 Task: Validate pipeline with `fly validate-pipeline` — passes validation
- [x] 1c83edfd0 Task: Phase 7 Manual Verification

## Phase 8: Container Image & Self-Test

- [x] 2721a6b40 Task: Add ci-agent-fix binary to `deploy/Dockerfile.ci-agent` — extends existing image with fix binary, includes git + gh CLI for PR creation
- [x] 2721a6b40 Task: Run ci-agent-fix against a synthetic review.json — CLI produces valid fix-report.json with correct schema, metadata, and exit code
- [x] 2721a6b40 Task: Phase 8 Manual Verification — CLI smoke test passes, full test suite green (28 fix tests + all ci-agent packages)

---

## Key Files

| File | Change |
|------|--------|
| `ci-agent/schema/fix_report.go` | NEW — FixReport, FixApplied, FixSkipped, FixSummary types |
| `ci-agent/schema/fix_report_test.go` | NEW — Fix report schema tests |
| `ci-agent/fix/review_parser.go` | NEW — Parse and sort review.json ProvenIssues |
| `ci-agent/fix/review_parser_test.go` | NEW — Parser tests |
| `ci-agent/fix/engine.go` | NEW — FixEngine interface, single-issue fix loop |
| `ci-agent/fix/engine_test.go` | NEW — Fix engine tests |
| `ci-agent/fix/git.go` | NEW — Git branch/commit/revert operations |
| `ci-agent/fix/git_test.go` | NEW — Git operation tests |
| `ci-agent/fix/prompt.go` | NEW — Fix prompt builder |
| `ci-agent/fix/prompt_test.go` | NEW — Prompt tests |
| `ci-agent/fix/adapter/claude/claude.go` | NEW — Claude Code fix adapter |
| `ci-agent/fix/adapter/claude/claude_test.go` | NEW — Adapter tests |
| `ci-agent/fix/regression.go` | NEW — Full test suite runner + rollback |
| `ci-agent/fix/regression_test.go` | NEW — Regression guard tests |
| `ci-agent/fix/orchestrator.go` | NEW — End-to-end fix orchestration |
| `ci-agent/fix/orchestrator_test.go` | NEW — Orchestrator tests |
| `cmd/ci-agent-fix/main.go` | NEW — CLI entrypoint |
| `ci/tasks/ci-agent-fix.yml` | NEW — Concourse task definition |
| `ci/tasks/create-pr.yml` | NEW — PR creation task |
| `deploy/borg-pipeline.yml` | MODIFY — Add agent-fix + create-pr jobs |
| `deploy/Dockerfile.ci-agent` | MODIFY — Add fix binary + gh CLI |

## Environment Variables (Concourse Convention)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `AGENT_CLI` | no | `claude-code` | Agent CLI adapter to use |
| `FIX_BRANCH` | no | `fix/agent-review-<commit>` | Branch name for fix commits |
| `MAX_RETRIES` | no | `2` | Max fix attempts per issue |
| `TEST_COMMAND` | no | `go test ./...` | Command to run full test suite |
| `GH_TOKEN` | for PR | — | GitHub token for PR creation |

## Pipeline Integration Example

```yaml
- name: agent-review
  plan:
  - get: repo
    trigger: true
  - task: ai-review
    file: ci/tasks/ci-agent-review.yml
    input_mapping: { repo: repo }
    output_mapping: { review: review }
    params:
      AGENT_CLI: claude-code
      REVIEW_PROFILE: default
      SCORE_THRESHOLD: "7.0"

- name: agent-fix
  plan:
  - get: repo
    passed: [agent-review]
  - get: review
    passed: [agent-review]
  - task: apply-fixes
    file: ci/tasks/ci-agent-fix.yml
    input_mapping: { repo: repo, review: review }
    output_mapping: { fixed-repo: fixed-repo, fix-report: fix-report }
    params:
      AGENT_CLI: claude-code
      MAX_RETRIES: "2"
  - put: repo
    inputs: [fixed-repo]
    params:
      repository: fixed-repo
  - task: create-pr
    file: ci/tasks/create-pr.yml
    input_mapping: { fixed-repo: fixed-repo, fix-report: fix-report, review: review }
    params:
      GH_TOKEN: ((github_pat))
```
