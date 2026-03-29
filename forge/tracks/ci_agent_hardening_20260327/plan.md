# Implementation Plan: ci-agent hardening

## Phase 1: Fix shell expansion in `runVerify()`

- [x] Task 1.1: Write tests for shell parameter expansion in `runVerify()` 61fece371
  - File: `ci-agent/phaserunner/runner_test.go`
  - Add test: `verify_cmd: "${MY_VAR:-fallback_value}"` with `MY_VAR` unset in env map -- verify the shell falls back to `fallback_value` (use a command like `test "${MY_VAR:-fallback_value}" = fallback_value`)
  - Add test: `verify_cmd: "${MY_VAR:-fallback_value}"` with `MY_VAR=custom` in env map -- verify `custom` is used
  - Add test: verify env map values are accessible as env vars inside the child shell (e.g., `verify_cmd: "test \"$repo_dir\" = /expected/path"`)
  - Add test: verify existing simple commands (`"true"`, `"false"`) still work as before (regression guard)

- [x] Task 1.2: Fix `runVerify()` to stop pre-expanding and export env to child process 39a20e58d
  - File: `ci-agent/phaserunner/runner.go` lines 268-282
  - Remove `os.Expand()` call (lines 270-275) -- pass `cmdStr` directly to `sh -c` as the command string
  - Build `cmd.Env` by starting with `os.Environ()` then appending `key=value` pairs from the resolved env map (so phase env vars override system env vars with same name)
  - Keep `cmd.Dir` logic unchanged (lines 278-280)
  - The shell will now handle `${VAR:-default}` expansion natively using the exported env vars

- [x] Task 1.3: Run existing `phaserunner` tests and verify no regressions 39a20e58d
  - Command: `cd ci-agent && ginkgo ./phaserunner/`
  - All existing tests (single-step, multi-step, verify pass/fail, provenance, markdown artifacts) must still pass

---

## Phase 2: Normalize env var naming across phase YAMLs

- [x] Task 2.1: Audit all 5 phase YAMLs and document current naming 39a20e58d
  - Files: `ci-agent/phases/{plan,implement,review,fix,qa}.yaml`
  - Current mismatches identified:
    - `implement.yaml`: `test_cmd` / `TEST_CMD` vs `fix.yaml`: `test_command` / `TEST_COMMAND` (same concept: test command to verify)
    - `implement.yaml`: `branch_name` / `BRANCH_NAME` vs `fix.yaml`: `fix_branch` / `FIX_BRANCH` (same concept: target branch)
  - Convention to adopt: use `implement.yaml` naming as canonical (it's the most commonly used phase)

- [x] Task 2.2: Normalize `fix.yaml` env vars to match `implement.yaml` conventions 80e8e01b4
  - File: `ci-agent/phases/fix.yaml`
  - Change config key `test_command` -> `test_cmd`, env var `TEST_COMMAND` -> `TEST_CMD`
  - Change config key `fix_branch` -> `branch_name`, env var `FIX_BRANCH` -> `BRANCH_NAME`
  - Update `verify_cmd` from `"${TEST_COMMAND:-go test ./...}"` to `"${TEST_CMD:-go test ./...}"`

- [x] Task 2.3: Update prompt templates that reference the old config keys 80e8e01b4
  - Search `ci-agent/prompts/fix/` for references to `.Env.test_command` and `.Env.fix_branch`
  - Update to `.Env.test_cmd` and `.Env.branch_name` respectively
  - Also search for any shell references to `$TEST_COMMAND` or `$FIX_BRANCH` in templates

- [x] Task 2.4: Run all ci-agent tests to verify no regressions 80e8e01b4
  - Command: `cd ci-agent && go test ./...`

---

## Phase 3: Add schema validation and dependency checks

- [x] Task 3.1: Write tests for `input_from` validation 8169c52b0
  - File: `ci-agent/phaseconfig/config_test.go`
  - Test: config with `input_from: ["nonexistent_step"]` where no step named `nonexistent_step` exists -- expect validation error with descriptive message
  - Test: config with valid `input_from: ["step1"]` where step1 is defined before the referencing step -- expect pass
  - Test: config with self-referential `input_from` (step references itself) -- expect error
  - Test: config with `input_from` referencing a step defined AFTER the current step (ordering violation) -- expect error
  - Test: config with empty `input_from: []` -- expect pass (no-op)

- [x] Task 3.2: Implement `input_from` validation in `Validate()` 8169c52b0
  - File: `ci-agent/phaseconfig/config.go` lines 72-88
  - After existing step validation loop, build a `map[string]int` of step name -> index
  - For each step with `input_from`, verify:
    - Each referenced step name exists in the map
    - Each referenced step has a lower index than the current step (ordering)
    - Step does not reference itself
  - Return descriptive error: `"step %q: input_from references unknown step %q"`

- [x] Task 3.3: Write tests for cross-phase dependency validation 8169c52b0
  - File: `ci-agent/phaseconfig/config_test.go`
  - Test: `ValidateSuite()` with implement phase requiring `spec_dir` (required=true) and plan phase providing `output_dir` -- returns no warnings
  - Test: `ValidateSuite()` with implement phase requiring `spec_dir` but no upstream phase provides a matching output -- returns warning
  - Test: `ValidateSuite()` with single config (no cross-phase deps) -- returns no warnings
  - Test: `ValidateSuite()` with empty input -- returns no warnings

- [x] Task 3.4: Implement `ValidateSuite()` for cross-phase checks 8169c52b0
  - File: `ci-agent/phaseconfig/config.go`
  - New exported type: `Warning struct { Phase string; Message string }`
  - New exported function: `ValidateSuite(configs []*Config) []Warning`
  - Logic: collect all env var config keys that have `Required: true` across all configs. For each required var, check if any other config's env map provides a matching key (by convention, `output_dir` in upstream maps to `spec_dir`/`input_dir` in downstream). Return warnings (not errors) since phases can be run independently with explicit env vars.

- [x] Task 3.5: Run all ci-agent tests 8169c52b0
  - Command: `cd ci-agent && go test ./...`

---

## Phase 4: Remove committed binary and update `.gitignore`

- [x] Task 4.1: Delete the committed binary 9567daa59
  - File: `ci-agent/ci-agent` (6MB Mach-O arm64 binary)
  - Run: `git rm ci-agent/ci-agent`

- [x] Task 4.2: Update `.gitignore` to prevent re-committing 9567daa59
  - File: `ci-agent/.gitignore`
  - Add `/ci-agent` line to the existing binary ignore section (alongside `/ci-agent-review`, `/ci-agent-fix`, etc.)

- [x] Task 4.3: Verify binary is no longer tracked 9567daa59
  - Command: `cd ci-agent && git status`
  - Confirm `ci-agent` binary is staged for deletion
  - Confirm building a new `ci-agent` binary would be ignored by git

---
