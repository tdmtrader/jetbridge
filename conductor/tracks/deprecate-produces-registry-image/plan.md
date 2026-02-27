# Plan: Deprecate `produces: registry-image` Syntax

## Phase 1: Add Deprecation Warnings

### 1.1 Config validation deprecation warning
- [ ] Write tests for deprecation warning in config validation
  - Test in `atc/configvalidate/validate_test.go`: when a resource type has `produces` set,
    validation succeeds but returns a deprecation warning
  - Test: warning message includes the resource type name and recommends `image:` field
  - Test: warning is returned alongside any other validation warnings (not an error)
- [ ] Implement deprecation warning in `validateResourceTypes()`
  - In `atc/configvalidate/validate.go`, add a warning when `resourceType.Produces != ""`
  - Warning text: `resource type '<name>' uses deprecated 'produces' field; use 'image:' instead`

### 1.2 Plan construction deprecation log
- [ ] Add lager deprecation log in `ImageGetPlan()` (atc/config.go:382)
  - When `rt.Produces != ""`, log a warning-level message before wiring the field
- [ ] Add lager deprecation log in planner (atc/builds/planner.go:144)
  - Same pattern: log when `rt.Produces != ""` is detected during plan construction

## Phase 2: Update Internal Code to Prefer `image:` Field

### 2.1 Refactor imageURLFromSource to reduce produces branching
- [ ] Write tests for imageURLFromSource behavior without produces
  - Test in `atc/engine/build_step_delegate_test.go`: verify URL construction works
    identically when `produces` is empty but the type is `registry-image`
  - Test: verify produces code path still works for backward compat
- [ ] Refactor `imageURLFromSource()` in `atc/engine/build_step_delegate.go`
  - Add code comment marking the `produces` check as deprecated
  - No behavioral change -- just documentation and preparation for eventual removal

### 2.2 Refactor get step produces checks
- [ ] Write tests for get step behavior with image: field types
  - Test in `atc/exec/get_step_test.go`: resource types using `image:` field bypass
    the produces-based registry-image detection
  - Test: existing produces-based behavior still works for backward compat
- [ ] Add deprecation comments to produces checks in `atc/exec/get_step.go`
  - Lines 193 and 587: mark `produces` branch as deprecated
  - Ensure `image:` field types take the ImageRef short-circuit path (already implemented)

### 2.3 Refactor step validator produces check
- [ ] Write tests for step validator with image: field types
  - Test in `atc/step_validator_test.go` (or inline): image: field types are
    correctly identified as image types without needing produces
- [ ] Update `atc/step_validator.go:160` to check for ImageRef in addition to produces
  - Mark the `produces` check as deprecated

## Phase 3: Update All Tests to Use `image:` Field

### 3.1 Migrate build_step_delegate_test.go
- [ ] Update `atc/engine/build_step_delegate_test.go`
  - Context "when the type produces registry-image" (line 673): replace `produces`-based
    setup with `image:` field setup using `TypeImage.ImageRef`
  - Keep one backward-compat test case that verifies `produces` still works

### 3.2 Migrate task_delegate_test.go
- [ ] Update `atc/engine/task_delegate_test.go`
  - Test "resolves a custom type with produces: registry-image" (line 601): migrate
    to use `image:` field instead
  - Test "falls back to plans for a non-registry-image type without produces" (line 793):
    keep as-is (tests the non-produces path)
  - Keep one backward-compat test verifying produces still works with deprecation warning

### 3.3 Migrate get_step_test.go
- [ ] Update `atc/exec/get_step_test.go`
  - Context "custom type with produces: registry-image" (line 873): migrate to `image:`
  - Context "custom type without produces" (line 902): keep as-is
  - Keep one backward-compat test for produces

### 3.4 Migrate configvalidate tests
- [ ] Update `atc/configvalidate/validate_test.go`
  - Context "skip_download with produces: registry-image" (line 1275): migrate to `image:`
  - Add a test that `produces` triggers deprecation warning

### 3.5 Migrate resource_type_test.go
- [ ] Review `atc/db/resource_type_test.go` for any produces-dependent test setup
  - Update any test fixtures that set produces to use image: where appropriate

## Phase 4: Documentation Updates

### 4.1 Update inline code documentation
- [ ] Add deprecation doc comments to `Produces` field in `atc/config.go`
  - `// Deprecated: Use Image field instead. Produces will be removed in a future version.`
- [ ] Add deprecation doc comments to `Produces` field in `atc/plan.go`
  - Same deprecation notice
- [ ] Update comments in `atc/engine/build_step_delegate.go` imageURLFromSource
  - Note that the produces parameter is deprecated

### 4.2 Update pipeline config documentation
- [ ] If pipeline configuration docs exist in-repo, add deprecation notice for `produces`
  - Recommend `image:` as the replacement
  - Provide before/after migration example
