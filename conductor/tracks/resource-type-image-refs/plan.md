# Plan: Direct Image References for Resource Types

## Phase 1: Data Model and ImageForType Short-Circuit

- [x] Write tests for TypeImage.ImageRef and ImageForType short-circuit
  - Test in `atc/config_test.go`: `ImageForType` returns `TypeImage` with `ImageRef` set
    when resource type has `Image` field, no `GetPlan`/`CheckPlan`
  - Test: resource type with `Image` set skips recursive type resolution
  - Test: resource type without `Image` falls through to existing behavior

- [x] Implement ImageRef field and ImageForType short-circuit
  - Added `ImageRef string` to `TypeImage` in `atc/plan.go`
  - Added `Image string` to `ResourceType` in `atc/config.go`
  - Modified `ImageForType()`: when `parent.Image != ""`, returns
    `TypeImage{BaseType: resourceType, ImageRef: parent.Image, Privileged: parent.Privileged}`

## Phase 2: FetchImage Short-Circuit

- [x] Implement ImageRef short-circuit in step callers
  - Modified `check_step.go`, `get_step.go`, `put_step.go`: added `TypeImage.ImageRef` check
    before `GetPlan != nil` branch — sets `imageSpec.ImageURL` directly, skips all
    FetchImage/metadataFetchImage/plan-based fetch

## Phase 3: Config Validation

- [x] Write tests for resource type config validation
  - Test in `atc/configvalidate/validate_test.go`:
    `image:` alone is valid, `image:` + `type:` together is rejected

- [x] Implement config validation
  - Modified `validateResourceTypes()`: if both `Image` and `Type` set, error;
    if neither set, existing "has no type" error

## Phase 4: Live Verification on concourse.home

- [ ] Deploy and verify with real pipeline
  - Cross-compile, build Docker image, push to registry.home
  - Create test pipeline with `image:` resource type
  - Verify: resource type resolves correctly, no check/get pods for the type image
  - Verify: existing `type:`+`source:` resource types still work
  - Clean up test pipeline, restore production config
