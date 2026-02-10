# Implementation Plan: CI Agent Review Task

## Phase 1: Schema & Types (standalone module)

### Task 1: Bootstrap standalone Go module

- [x] fd0782c04 Initialize `ci-agent/go.mod` as independent module
- [x] fd0782c04 Verify module builds and tests run independently of parent repo
- [x] fd0782c04 Add `.gitignore` and basic package structure

### Task 2: Define review output schema types

- [x] f4dab860e Write tests for review schema
  - `ReviewOutput` round-trips JSON marshal/unmarshal
  - `ProvenIssue` requires id, severity, title, file, line, test_file, test_name
  - `Observation` requires id, title, file, line, category
  - `Score` computes `pass` correctly from `value` vs `threshold`
  - `Metadata` captures all required fields
  - `TestSummary` counts are consistent (total = passing + failing + error)
  - `SchemaVersion` is always `"1.0.0"`
  - Severity enum validates: critical, high, medium, low
  - Category enum validates: security, correctness, performance, maintainability, testing
  - Invalid severity/category returns error
- [x] aa1de73cf Implement review schema types
  - New package: `ci-agent/schema/`
  - Types: `ReviewOutput`, `Metadata`, `Score`, `ScoreDeduction`, `ProvenIssue`, `Observation`, `TestSummary`
  - `Severity` and `Category` types with constants + `Validate()`
  - `ReviewOutput.Validate() error` — structural validation

### Task 3: Define review config types and profiles

- [x] e7cacb611 Write tests for review config
  - Parse valid `review.yml` with severity weights, categories, include/exclude
  - Missing weights use defaults
  - Empty config returns default profile
  - Built-in profiles `default`, `security`, `strict` load correctly
  - File include/exclude patterns match correctly (glob)
  - Unknown fields rejected (strict parsing)
- [x] ca4ba6965 Implement review config types
  - New package: `ci-agent/config/`
  - Types: `ReviewConfig`, `SeverityWeights`, `CategoryConfig`
  - `LoadConfig(yamlBytes []byte) (*ReviewConfig, error)`
  - `LoadProfile(name string) (*ReviewConfig, error)` — built-in profiles
  - `DefaultConfig() *ReviewConfig`
  - `ReviewConfig.ShouldReview(filePath string) bool` — include/exclude logic

- [x] cbb876059 Phase 1 Checkpoint — module builds independently, schema + config tested [checkpoint: cbb876059]

---

## Phase 2: Scoring Engine

### Task 4: Implement scoring computation

- [x] 823db09ee Write tests for scoring
  - Zero proven issues → score 10.0
  - One critical issue → score 7.0 (10 - 3.0)
  - One high issue → score 8.5 (10 - 1.5)
  - One medium issue → score 9.0 (10 - 1.0)
  - One low issue → score 9.5 (10 - 0.5)
  - Multiple issues: deductions are additive
  - Score floors at 0.0 (never negative)
  - Custom severity weights override defaults
  - Pass/fail: score >= threshold → pass
  - Pass/fail: `fail_on_critical=true` + any critical → fail regardless of score
  - Deductions array in output matches proven issues
- [x] 823db09ee Implement scoring engine
  - New package: `ci-agent/scoring/`
  - `ComputeScore(issues []schema.ProvenIssue, weights config.SeverityWeights) schema.Score`
  - `EvaluatePass(score schema.Score, threshold float64, failOnCritical bool) bool`

- [x] 823db09ee Phase 2 Checkpoint — scoring is deterministic, all tests pass

---

## Phase 3: Test Runner

### Task 5: Test file executor

- [x] 5d09eb196 Write tests for test runner
  - Runs a Go test file against a repo directory, captures pass/fail and output
  - Passing test returns `TestResult{Pass: true, Output: "..."}`
  - Failing test returns `TestResult{Pass: false, Output: "panic: ..."}`
  - Compilation error returns `TestResult{Error: true, Output: "..."}`
  - Timeout returns error result
  - Multiple test files run independently, results collected per file
  - Test file placed in correct package directory for Go compilation
- [x] 5d09eb196 Implement test runner
  - New package: `ci-agent/runner/`
  - `TestResult` type: `Pass`, `Fail`, `Error` bools, `Output string`, `Duration`
  - `RunTest(ctx, repoDir, testFile string) (*TestResult, error)`
  - `RunTests(ctx, repoDir string, testFiles []string) (map[string]*TestResult, error)`
  - Executes `go test -run <TestName> -count=1 -timeout 30s` per file

### Task 6: Issue classification from test results

- [x] 5d09eb196 Write tests for issue classification
  - Failing test → proven issue (keeps severity from agent)
  - Passing test → discard (agent's concern was unfounded)
  - Compilation error → demote to observation with note "test could not compile"
  - Agent concern with no test generated → observation
- [x] 5d09eb196 Implement issue classifier
  - `ci-agent/runner/classify.go`
  - `AgentFinding` intermediate type: what the agent produced before verification
  - `ClassifyResults(findings []AgentFinding, results map[string]*TestResult) ([]schema.ProvenIssue, []schema.Observation)`

- [x] 5d09eb196 Phase 3 Checkpoint — runner executes tests, classifier separates proven vs unproven

---

## Phase 4: Agent Adapter Layer

### Task 7: Define adapter interface and finding format

- [x] 21fee0347 Write tests for agent finding parsing
  - Parse agent's structured JSON output into `[]AgentFinding`
  - Each finding has: title, description, file, line, severity_hint, category, test_code
  - Missing test_code → finding becomes observation directly
  - Malformed output returns parse error
- [x] 21fee0347 Implement adapter interface
  - New package: `ci-agent/adapter/`
  - Interface: `Adapter { Review(ctx, repoDir string, cfg *config.ReviewConfig) ([]AgentFinding, error) }`
  - `AgentFinding` type: intermediate between agent output and classified results
  - `ParseFindings(raw []byte) ([]AgentFinding, error)`

### Task 8: Claude Code adapter and review prompt

- [x] 21fee0347 Write tests for Claude Code adapter
  - Constructs correct CLI invocation
  - Passes repo path and config constraints
  - Captures structured JSON output
- [x] 21fee0347 Implement Claude Code adapter
  - `ci-agent/adapter/claude/claude.go` — implements `Adapter`
  - Builds CLI command with review prompt
  - Review prompt template instructs agent to:
    1. Analyze the code for real defects (not style)
    2. For each concern, write a failing Go test that proves it
    3. Output structured JSON with findings + test code
    4. Classify severity by what the test demonstrates
- [x] 21fee0347 Write tests for prompt template
  - Prompt includes config constraints (categories, include/exclude)
  - Prompt includes output format specification
  - Diff-only mode includes only changed files in prompt context
- [x] 21fee0347 Implement review prompt template
  - `ci-agent/adapter/claude/claude.go` — BuildReviewPrompt
  - Diff mode references `git diff <baseRef>...HEAD --name-only` to scope files

- [x] 21fee0347 Phase 4 Checkpoint — adapter dispatches to Claude Code, parses findings

---

## Phase 5: Orchestrator & CLI

### Task 9: Orchestrator (wires all components)

- [x] f45d21fbc Write tests for orchestrator
  - Full pipeline: config → adapter → test runner → classifier → scorer → output
  - Writes `review.json` to output directory
  - Writes test files to `review/tests/` in output directory
  - Exit code 0 when score passes
  - Exit code 1 when score fails or critical issues found
  - Handles adapter errors gracefully (writes partial output with error metadata)
- [x] f45d21fbc Implement orchestrator
  - New package: `ci-agent/orchestrator/`
  - `Run(ctx context.Context, opts Options) (*schema.ReviewOutput, error)`
  - Options: repoDir, outputDir, configPath, adapter, threshold, failOnCritical, diffOnly, baseRef
  - Sequence: load config → dispatch adapter → write test files to repo → run tests → classify → score → write review.json

### Task 10: CLI binary

- [x] f45d21fbc Implement CLI entrypoint
  - `cmd/ci-agent-review/main.go`
  - Reads env vars: `AGENT_CLI`, `AGENT_MODEL`, `REVIEW_CONFIG`, `SCORE_THRESHOLD`, `FAIL_ON_CRITICAL`, `REVIEW_DIFF_ONLY`, `BASE_REF`
  - Resolves input paths: `REPO_DIR`, `OUTPUT_DIR`
  - Calls orchestrator, sets exit code based on result

- [x] f45d21fbc Phase 5 Checkpoint — CLI runs end-to-end with mocked adapter, produces valid review.json

---

## Phase 6: Database Storage (optional)

### Task 11: PostgreSQL review history

- [x] 93029e8a8 Write tests for storage interface
  - Storage skipped gracefully when no DATABASE_URL provided
  - NoopStore handles all operations gracefully
- [x] 93029e8a8 Implement storage interface
  - New package: `ci-agent/storage/`
  - `Store` interface: `SaveReview`, `GetReview`, `ListReviews`
  - `NoopStore` for graceful degradation without database
  - PostgreSQL implementation deferred until database provisioned

- [x] 93029e8a8 Phase 6 Checkpoint — noop store tested, no-op when DATABASE_URL absent

---

## Phase 7: Concourse Integration

### Task 12: Task YAML definitions

- [x] e9e549e87 Write `ci/tasks/ci-agent-review.yml` task definition
  - Inputs: `repo` (required), `review-config` (optional)
  - Outputs: `review` (contains review.json + tests/)
  - Params with defaults
  - Builds from source, runs ci-agent-review
- [x] e9e549e87 Write `ci/tasks/review-gate.yml` companion task
  - Input: `review` (reads review.json)
  - Params: `SCORE_THRESHOLD`, `FAIL_ON_CRITICAL`
  - Script: parse review.json with jq, check pass/fail, exit 0/1

### Task 13: Container image

- [x] e9e549e87 Write `deploy/Dockerfile.ci-agent`
  - Base: golang:1.25-bookworm (multi-stage)
  - Includes: ci-agent-review binary, git, jq
  - Claude Code CLI mounted at runtime

- [x] e9e549e87 Phase 7 Checkpoint — task definitions created, Dockerfile written, pipeline updated

---

## Phase 8: Self-Review & Pipeline Integration

### Task 14: Self-review validation

- [x] Verified: 70 tests across 8 packages, all GREEN
  - schema: 15 specs — ReviewOutput, ProvenIssue, Observation, Score, TestSummary
  - config: 11 specs — LoadConfig, LoadProfile, ShouldReview with ** glob
  - scoring: 14 specs — ComputeScore, EvaluatePass, custom weights
  - runner: 10 specs — RunTest pass/fail/error/timeout, RunTests, ClassifyResults
  - adapter: 5 specs — ParseFindings JSON parsing
  - adapter/claude: 7 specs — BuildCommand, BuildReviewPrompt
  - orchestrator: 5 specs — full pipeline with fake adapter
  - storage: 3 specs — NoopStore graceful degradation
- [x] Binary builds successfully (4.5MB)

### Task 15: Pipeline integration

- [x] e9e549e87 Added ci-agent-review job to `deploy/borg-pipeline.yml`
  - Runs in parallel with unit-tests (after build-and-vet)
  - Non-blocking (|| true) for initial rollout
  - Builds from source, runs review, outputs review.json

- [x] Phase 8 Checkpoint — all tests green, binary builds, pipeline updated

---

## Key Files

| File | Change |
|------|--------|
| `ci-agent/go.mod` | NEW — standalone Go module |
| `ci-agent/schema/review.go` | NEW — ReviewOutput, ProvenIssue, Observation types |
| `ci-agent/schema/review_test.go` | NEW — Schema tests |
| `ci-agent/config/config.go` | NEW — ReviewConfig, profiles, parsing |
| `ci-agent/config/config_test.go` | NEW — Config tests |
| `ci-agent/config/profiles/` | NEW — Built-in profile YAML files |
| `ci-agent/scoring/scoring.go` | NEW — Score computation |
| `ci-agent/scoring/scoring_test.go` | NEW — Scoring tests |
| `ci-agent/runner/runner.go` | NEW — Test file execution |
| `ci-agent/runner/classify.go` | NEW — Issue classification |
| `ci-agent/runner/runner_test.go` | NEW — Runner tests |
| `ci-agent/adapter/adapter.go` | NEW — Adapter interface, AgentFinding |
| `ci-agent/adapter/claude/claude.go` | NEW — Claude Code adapter |
| `ci-agent/adapter/prompt.go` | NEW — Review prompt template |
| `ci-agent/orchestrator/orchestrator.go` | NEW — End-to-end orchestration |
| `ci-agent/storage/postgres.go` | NEW — Optional review history storage |
| `cmd/ci-agent-review/main.go` | NEW — CLI entrypoint |
| `ci/tasks/ci-agent-review.yml` | NEW — Concourse task definition |
| `ci/tasks/review-gate.yml` | NEW — Gate companion task |
| `deploy/Dockerfile.ci-agent` | NEW — Container image |
| `deploy/borg-pipeline.yml` | MODIFY — Add review job |
