# Plan: Deprecate `produces: registry-image` Syntax

## Phase 1: Add Deprecation Warnings

### 1.1 Config validation deprecation warning
- [x] Write tests for deprecation warning in config validation 42d98e2b6
  - Test in `atc/configvalidate/validate_test.go`: when a resource type has `produces` set,
    validation succeeds but returns a deprecation warning
  - Test: warning message includes the resource type name and recommends `image:` field
  - Test: warning is returned alongside any other validation warnings (not an error)
- [x] Implement deprecation warning in `validateResourceTypes()` 42d98e2b6
  - In `atc/configvalidate/validate.go`, add a warning when `resourceType.Produces != ""`
  - Warning text: `resource type '<name>' uses deprecated 'produces' field; use 'image:' instead`

### 1.2 Plan construction deprecation log
- [x] Add lager deprecation log in `ImageGetPlan()` (atc/config.go:382) 07763ed10
  - When `rt.Produces != ""`, log a warning-level message before wiring the field
- [x] Add lager deprecation log in planner (atc/builds/planner.go:144) 07763ed10
  - Same pattern: log when `rt.Produces != ""` is detected during plan construction

## Phase 2: Update Internal Code to Prefer `image:` Field

### 2.1 Refactor imageURLFromSource to reduce produces branching
- [x] Write tests for imageURLFromSource behavior without produces 7d200b023
  - Test in `atc/engine/build_step_delegate_test.go`: verify URL construction works
    identically when `produces` is empty but the type is `registry-image`
  - Test: verify produces code path still works for backward compat
- [x] Refactor `imageURLFromSource()` in `atc/engine/build_step_delegate.go` 7d200b023
  - Add code comment marking the `produces` check as deprecated
  - No behavioral change -- just documentation and preparation for eventual removal

### 2.2 Refactor get step produces checks
- [x] Write tests for get step behavior with image: field types 7d200b023
  - Test in `atc/exec/get_step_test.go`: resource types using `image:` field bypass
    the produces-based registry-image detection
  - Test: existing produces-based behavior still works for backward compat
- [x] Add deprecation comments to produces checks in `atc/exec/get_step.go` 7d200b023
  - Lines 193 and 587: mark `produces` branch as deprecated
  - Ensure `image:` field types take the ImageRef short-circuit path (already implemented)

### 2.3 Refactor step validator produces check
- [x] Write tests for step validator with image: field types 7d200b023
  - Test in `atc/step_validator_test.go` (or inline): image: field types are
    correctly identified as image types without needing produces
- [x] Update `atc/step_validator.go:160` to check for ImageRef in addition to produces 7d200b023
  - Mark the `produces` check as deprecated

## Phase 3: Update All Tests to Use `image:` Field

### 3.1 Migrate build_step_delegate_test.go
- [x] Update `atc/engine/build_step_delegate_test.go` b68e27782
  - Context "when the type produces registry-image" (line 673): replace `produces`-based
    setup with `image:` field setup using `TypeImage.ImageRef`
  - Keep one backward-compat test case that verifies `produces` still works

### 3.2 Migrate task_delegate_test.go
- [x] Update `atc/engine/task_delegate_test.go` b68e27782
  - Test "resolves a custom type with produces: registry-image" (line 601): migrate
    to use `image:` field instead
  - Test "falls back to plans for a non-registry-image type without produces" (line 793):
    keep as-is (tests the non-produces path)
  - Keep one backward-compat test verifying produces still works with deprecation warning

### 3.3 Migrate get_step_test.go
- [x] Update `atc/exec/get_step_test.go` b68e27782
  - Context "custom type with produces: registry-image" (line 873): migrate to `image:`
  - Context "custom type without produces" (line 902): keep as-is
  - Keep one backward-compat test for produces

### 3.4 Migrate configvalidate tests
- [x] Update `atc/configvalidate/validate_test.go` b68e27782
  - Context "skip_download with produces: registry-image" (line 1275): migrate to `image:`
  - Add a test that `produces` triggers deprecation warning

### 3.5 Migrate resource_type_test.go
- [x] Review `atc/db/resource_type_test.go` for any produces-dependent test setup b68e27782
  - Update any test fixtures that set produces to use image: where appropriate

## Phase 4: Documentation Updates

### 4.1 Update inline code documentation
- [x] Add deprecation doc comments to `Produces` field in `atc/config.go` 4e97e7cdf
  - `// Deprecated: Use Image field instead. Produces will be removed in a future version.`
- [x] Add deprecation doc comments to `Produces` field in `atc/plan.go` 4e97e7cdf
  - Same deprecation notice
- [x] Update comments in `atc/engine/build_step_delegate.go` imageURLFromSource 7d200b023
  - Note that the produces parameter is deprecated

### 4.2 Update pipeline config documentation
- [x] If pipeline configuration docs exist in-repo, add deprecation notice for `produces` 4e97e7cdf
- [ ] Task: Full removal of produces field from structs, runtime code, unit tests, and integration tests
  - Recommend `image:` as the replacement
  - Provide before/after migration example
