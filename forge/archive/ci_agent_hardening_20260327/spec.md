# Spec: ci-agent hardening

**Track ID:** `ci_agent_hardening_20260327`
**Type:** bugfix

## Overview

The ci-agent phase runner has several bugs and missing safeguards that cause silent failures in production:

1. **Shell expansion bug:** `runVerify()` in `ci-agent/phaserunner/runner.go:268-282` uses `os.Expand()` to pre-expand shell parameter expansion syntax (`${VAR:-default}`) before passing to `sh -c`. This breaks shell semantics -- the env map is never exported to the child process, so `${TEST_CMD:-go test ./...}` either expands to empty string or passes literal syntax to the shell.

2. **Inconsistent env var naming:** Phase YAML configs use inconsistent naming for semantically identical variables. `implement.yaml` uses `test_cmd`/`TEST_CMD` while `fix.yaml` uses `test_command`/`TEST_COMMAND`. `implement.yaml` uses `branch_name`/`BRANCH_NAME` while `fix.yaml` uses `fix_branch`/`FIX_BRANCH`.

3. **No schema validation:** No validation exists for cross-phase dependencies (e.g., implement requires `spec_dir` which is plan's `output_dir`), `input_from` references to nonexistent steps, or template file existence at load time.

4. **Committed binary:** A 6MB compiled `ci-agent/ci-agent` Mach-O arm64 binary is committed to the repository. The `.gitignore` only covers the old per-phase binaries (`/ci-agent-review`, etc.), not the unified binary.

## Requirements

1. `runVerify()` must pass the resolved env map as environment variables to the child shell process, and must NOT pre-expand the command string with `os.Expand()`. Shell parameter expansion (`${VAR:-default}`) must be handled by the shell itself.
2. Normalize env var config keys and OS env var names across all 5 phase YAMLs so semantically identical variables use the same names.
3. Add validation in `phaseconfig.Validate()` for: (a) `input_from` references to step names that exist in the config, (b) ordering constraints on `input_from` (must reference earlier steps). Add a new `ValidateSuite()` method to check cross-phase env var wiring.
4. Remove the committed binary `ci-agent/ci-agent` and add it to `.gitignore`.

## Technical Approach

- **Phase 1:** Remove `os.Expand()` call in `runVerify()`, set `cmd.Env` from `os.Environ()` + resolved env map. Add tests with `${VAR:-default}` syntax.
- **Phase 2:** Rename `test_command` -> `test_cmd` and `fix_branch` -> `branch_name` in `fix.yaml`. Update prompt templates.
- **Phase 3:** Extend `Validate()` in `phaseconfig/config.go` to check `input_from`. Add `ValidateSuite()` for multi-config checks.
- **Phase 4:** Delete binary, update `.gitignore`.

## Key Files

- `ci-agent/phaserunner/runner.go` (lines 268-282) -- `runVerify()` bug
- `ci-agent/phaserunner/runner_test.go` -- test coverage
- `ci-agent/phaseconfig/config.go` (lines 72-88) -- `Validate()` method
- `ci-agent/phaseconfig/config_test.go` -- validation tests
- `ci-agent/phases/implement.yaml` -- reference naming
- `ci-agent/phases/fix.yaml` -- mismatched naming
- `ci-agent/phases/plan.yaml`, `review.yaml`, `qa.yaml` -- other phases
- `ci-agent/.gitignore` -- missing unified binary entry
- `ci-agent/ci-agent` -- committed binary to remove

## Acceptance Criteria

- [ ] `runVerify()` correctly executes `${TEST_CMD:-go test ./...}` when `TEST_CMD` is unset (falls back to `go test ./...`)
- [ ] `runVerify()` correctly executes the value of `TEST_CMD` when it IS set
- [ ] Resolved env map values are accessible as env vars inside the child shell process
- [ ] All 5 phase YAMLs use consistent naming for semantically identical variables
- [ ] `Validate()` rejects configs with `input_from` referencing nonexistent steps
- [ ] `Validate()` rejects configs with `input_from` referencing later steps (ordering violation)
- [ ] `ValidateSuite()` warns when a required env var has no upstream provider
- [ ] No compiled binary in the repo; `.gitignore` prevents re-committing
- [ ] All existing ci-agent tests pass

## Out of Scope

- Changing the phase runner execution model (single-step vs multi-step)
- Adding new phases or modifying prompt template content
- Changing the LLM client interface
- Adding runtime template file existence checks (deferred -- would require baseDir at validation time)
