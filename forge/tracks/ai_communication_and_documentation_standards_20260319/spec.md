# Spec: CI Agent Binary Consolidation

**Track ID:** `ai_communication_and_documentation_standards_20260319`
**Type:** refactor

## Overview

The ci-agent module contains 5 separate binaries (`ci-agent-plan`, `ci-agent-implement`, `ci-agent-review`, `ci-agent-fix`, `ci-agent-qa`) with 4 different orchestration strategies that duplicate significant infrastructure. The orchestrators embed retry loops, revert logic, and state tracking that add complexity but duplicate what the pipeline itself should handle.

This track consolidates everything into a single `ci-agent` binary with one linear orchestration model and YAML-based phase configurations. The orchestrator becomes a dumb pipe: load config → render prompt → call LLM → write output → optionally verify → record result. Retries, reverts, and error recovery are the pipeline's responsibility, not the orchestrator's.

### Why

1. **Unnecessary orchestrator complexity.** The TDD loop, verify-fix cycle, and scan-classify logic exist because the orchestrator assumes the LLM will produce bad output and tries to compensate with retries and reverts. With properly instructed agents that have MCP tool access (git, test runner, file ops), the agent can verify its own work within a single call. If verification still fails, that's a prompting problem — not an orchestrator problem.

2. **Duplicated infrastructure.** Every adapter reimplements Claude CLI invocation, every main.go reimplements env var parsing, every orchestrator reimplements event streaming and result writing.

3. **Poor auditability.** Prompts and configuration are embedded in Go code. No way to inspect what prompt was used for a given run, compare prompt versions, or attach configuration to results as provenance.

4. **Friction testing permutations.** Testing a different prompt or threshold requires editing Go code and recompiling. A/B testing requires different binaries.

5. **Friction adding new phases.** A new phase requires a new cmd/ entry point, adapter package, adapter/claude/ implementation, and orchestrator — all reimplementing the same boilerplate.

### Design Principles

- **The orchestrator is a dumb pipe.** It does not retry, revert, or manage state. It renders a prompt, calls the LLM, writes the output, and optionally runs a verification command.
- **The agent is smart.** With MCP tools exposed, the agent can run tests, read errors, edit files, and commit — all within a single LLM call. The orchestrator doesn't need to loop.
- **The pipeline is the retry mechanism.** If a phase produces a `fail` result, the Concourse pipeline decides what happens next (route to fix step, re-run, notify human). This is visible, auditable, and matches how Concourse already works.
- **Prompts are the product.** The quality of output depends on prompt engineering, not orchestrator logic. Making prompts external files that are versioned, testable, and swappable is the core improvement.

### What This Is NOT

- **NOT a workflow engine.** There is one orchestration model: linear. No loops, no branching, no conditionals.
- **NOT a dynamic pipeline generator.** The Concourse pipeline remains static YAML, updated via `fly sp`.
- **NOT an OTel/observability change.** Agent tracing is tracked separately (`ci_agent_otel_genai_tracing_20260319`).

## Requirements

1. **Single binary with YAML-based phase configs.** Replace 5 binaries with:
   ```
   ci-agent --phase phases/plan.yaml
   ```
   Each phase YAML declares:
   - `name` — phase identifier
   - `steps` — ordered list of prompt steps, each with a template file path, output schema, and optional verification command
   - `scoring` — optional thresholds and weights
   - `env` — environment variable mappings
   - `mcp` — MCP server configurations to expose to the agent (git, test runner, file ops)

2. **Single linear orchestration model.** For every phase, the orchestrator does:
   ```
   for each step in config.steps:
       render prompt template with available inputs
       call LLM (with MCP tools if configured)
       parse response according to output schema
       write artifacts to output dir
       if verify_cmd is set:
           run verify_cmd
           record pass/fail
   write results.json with provenance
   ```
   No retries. No reverts. No state management. If a step fails verification, the result status is `fail` and the pipeline handles it.

3. **Prompt templates as files.** Extract all prompts from Go code into markdown template files (e.g., `prompts/plan/spec.md`, `prompts/implement/task.md`). Templates can reference input variables and prior step outputs. Committed to git alongside phase YAML.

4. **MCP tool exposure.** Phase configs declare which MCP tools the agent has access to during its call. This is how the agent runs tests, commits code, reads files — not through orchestrator logic.
   ```yaml
   mcp:
     - git        # agent can commit, branch, diff
     - test       # agent can run test commands
     - filesystem # agent can read/write files
   ```

5. **Provenance in results.** Every `results.json` includes:
   - Phase YAML file path and content hash
   - Prompt template file paths and content hashes
   - Model name and version
   - MCP tools that were available
   - Fully reconstructable: given the same inputs + config, you get the same agent invocation

6. **Shared LLM client.** Extract Claude CLI invocation into a common `llm` package:
   ```go
   type Client interface {
       Call(ctx context.Context, prompt string, opts CallOpts) (json.RawMessage, error)
   }
   ```

7. **Shared infrastructure packages.**
   - `envconfig` — env var parsing helpers
   - `results` — results writing, artifact registration
   - `events` — NDJSON event writer

8. **Backward compatibility.** `ci-agent-plan` (old name) continues to work via wrapper scripts or symlinks mapping to `ci-agent --phase phases/plan.yaml`.

## Example Phase Configs

```yaml
# phases/plan.yaml
name: plan
env:
  input_dir: {var: INPUT_DIR, default: "story"}
  output_dir: {var: OUTPUT_DIR, default: "plan-output"}
steps:
  - name: generate-spec
    template: prompts/plan/spec.md
    output_schema: schemas/spec_output.json
    artifacts:
      - {name: spec, path: spec.md, media_type: text/markdown}
  - name: generate-plan
    template: prompts/plan/plan.md
    input_from: [generate-spec]
    output_schema: schemas/plan_output.json
    artifacts:
      - {name: plan, path: plan.md, media_type: text/markdown}
scoring:
  threshold: 0.6
  weights: {completeness: 0.3, coverage: 0.4, actionability: 0.3}
```

```yaml
# phases/implement.yaml
name: implement
env:
  spec_dir: {var: SPEC_DIR, required: true}
  repo_dir: {var: REPO_DIR, required: true}
  output_dir: {var: OUTPUT_DIR, required: true}
mcp:
  - git
  - test
  - filesystem
steps:
  - name: implement-tasks
    template: prompts/implement/tasks.md
    output_schema: schemas/impl_output.json
    verify_cmd: "${TEST_CMD:-go test ./...}"
    artifacts:
      - {name: summary, path: summary.md, media_type: text/markdown}
```

```yaml
# phases/review.yaml
name: review
env:
  repo_dir: {var: REPO_DIR, required: true}
  output_dir: {var: OUTPUT_DIR, default: "review"}
mcp:
  - test
  - filesystem
steps:
  - name: review-code
    template: prompts/review/findings.md
    output_schema: schemas/review_output.json
    verify_cmd: "go vet ./..."
    artifacts:
      - {name: review, path: review.json, media_type: application/json}
scoring:
  threshold: 7.0
```

```yaml
# phases/fix.yaml
name: fix
env:
  repo_dir: {var: REPO_DIR, required: true}
  review_dir: {var: REVIEW_DIR, required: true}
  output_dir: {var: OUTPUT_DIR, default: "fix-report"}
mcp:
  - git
  - test
  - filesystem
steps:
  - name: fix-issues
    template: prompts/fix/apply.md
    output_schema: schemas/fix_output.json
    verify_cmd: "${TEST_CMD:-go test ./...}"
    artifacts:
      - {name: fix-report, path: fix-report.json, media_type: application/json}
```

## Acceptance Criteria

- [ ] `ci-agent --phase phases/plan.yaml` produces equivalent output to `ci-agent-plan` for the same input
- [ ] `ci-agent --phase phases/implement.yaml` produces equivalent output to `ci-agent-implement` for the same input
- [ ] `ci-agent --phase phases/review.yaml` produces equivalent output to `ci-agent-review` for the same input
- [ ] `ci-agent --phase phases/fix.yaml` produces equivalent output to `ci-agent-fix` for the same input
- [ ] `ci-agent --phase phases/qa.yaml` produces equivalent output to `ci-agent-qa` for the same input
- [ ] All existing integration tests pass against the new single binary
- [ ] NDJSON event files continue to be written
- [ ] `results.json` includes provenance metadata (phase config hash, prompt hashes, model, MCP tools)
- [ ] Changing a prompt requires only editing a template file, not Go code
- [ ] Adding a new phase requires only a new YAML + prompt templates, no Go code
- [ ] Backward compatibility: old binary names still work via wrappers
- [ ] Agent has MCP tool access for phases that declare it

## Out of Scope

- Retry/revert logic in the orchestrator (pipeline handles this)
- Dynamic pipeline generation
- OpenSpec, AGENTS.md, or A2A protocol adoption
- OTel GenAI semantic conventions (separate track)
- Repo context discovery
- Changing the `PlanningInput` schema
- MCP server changes (existing MCP server is unrelated)
- Feedback API changes
