# Plan: Pipeline Split — Separate Agent Testing

## Phase 1: Extract agent jobs from primary pipeline

- [x] 1.1 Remove agent jobs (`ci-agent-review`, `agent-fix`, `agent-plan`, `agent-qa`, `agent-implement`) from `deploy/borg-pipeline.yml`
- [x] 1.2 Update `promote-to-main` to require only `passed: [deploy]` (remove `ci-agent-review` dependency)
- [x] 1.3 Verify primary pipeline renders cleanly with `fly validate-pipeline`

## Phase 2: Build the validate-output CLI

- [x] 2.1 Write tests for `validate-output` — covers review.json, fix-report.json, results.json, qa.json validation (valid files pass, missing fields fail, malformed JSON fails)
- [x] 2.2 Implement `ci-agent/cmd/validate-output/main.go` — reads `--output-dir` and `--type` flag (review|fix|plan|qa), deserializes JSON, calls `.Validate()`, exits 0/1
- [x] 2.3 Verify tests pass: `go test ./ci-agent/cmd/validate-output/...` — 15/15 pass

## Phase 3: Create agent pipeline

- [x] 3.1 Create `deploy/agent-pipeline.yml` with git resource (same repo/credentials)
- [x] 3.2 Add `build-agents` job — compiles all 5 agent binaries + validate-output in single task
- [x] 3.3 Add `test-agent-review` job — builds ci-agent-review + validate-output, runs agent with synthetic input, validates review.json
- [x] 3.4 Add `test-agent-qa` job — builds ci-agent-qa + validate-output, runs agent with synthetic input, validates qa.json
- [x] 3.5 Add `test-agent-plan` job — builds ci-agent-plan + validate-output, runs agent, validates results.json
- [x] 3.6 Add `test-agent-fix` job — builds ci-agent-fix + validate-output, runs agent with synthetic review input, validates fix-report.json
- [x] 3.7 Add `test-agent-implement` job — builds ci-agent-implement + validate-output, runs agent, validates results.json
- [x] 3.8 Verify agent pipeline renders cleanly with `fly validate-pipeline`

## Phase 4: Deploy both pipelines

- [x] 4.1 Set primary pipeline: `fly -t home set-pipeline -p jetbridge -c deploy/borg-pipeline.yml`
- [x] 4.2 Set agent pipeline: `fly -t home set-pipeline -p jetbridge-agents -c deploy/agent-pipeline.yml`
- [x] 4.3 Unpause agent pipeline and verify resource check succeeds
- [x] 4.4 Commit all changes and push — `0caa9fda4`
