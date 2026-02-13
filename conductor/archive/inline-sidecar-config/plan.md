# Plan: Inline Sidecar Definition in Pipeline Config

## Phase 1: Data Model — Polymorphic Sidecars Field

- [x] Write tests for SidecarSource union type parsing
  - Test: YAML/JSON string entry parses as file reference
  - Test: YAML/JSON object entry parses as inline SidecarConfig
  - Test: mixed list (strings + objects) parses correctly
  - Test: round-trip marshal/unmarshal preserves both forms

- [x] Implement SidecarSource union type
  - New type `SidecarSource` in `atc/sidecar.go` with custom
    UnmarshalJSON/MarshalJSON
  - Contains `File string` (populated when string) or
    `Config *SidecarConfig` (populated when object)
  - Change `TaskStep.Sidecars` from `[]string` to `[]SidecarSource`
  - Change `TaskPlan.Sidecars` from `[]string` to `[]SidecarSource`

## Phase 2: Task Step Loading

- [x] Write tests for loadSidecars handling inline configs
  - Test: inline-only sidecars list skips file streaming
  - Test: file-only sidecars list preserves existing behavior
  - Test: mixed list loads files AND includes inline configs
  - Test: duplicate name across inline + file sidecars is rejected

- [x] Update loadSidecars to handle SidecarSource union
  - Iterate `[]SidecarSource`: if `File` is set, stream and parse as
    before; if `Config` is set, use directly
  - Validate inline configs (name, image, reserved names) same as file
  - Merge all into single `[]SidecarConfig` with duplicate check

## Phase 3: Config Validation

- [x] Write tests for inline sidecar validation in configvalidate
  - Test: inline sidecar missing `name` produces error
  - Test: inline sidecar missing `image` produces error
  - Test: inline sidecar with reserved name produces error
  - Test: valid inline sidecar passes validation

- [x] Implement config validation for inline sidecars
  - Update `validateTaskStep()` in `atc/configvalidate/` to validate
    inline sidecar configs at set-pipeline time (file refs are validated
    at runtime as before)

## Phase 4: Planner Pass-Through

- [x] Verify planner copies SidecarSource list correctly
  - Update `VisitTask` in `atc/builds/planner.go` if needed (should be
    a type change only since field name stays `Sidecars`)
  - Write test confirming inline configs survive plan creation

## Phase 5: Live Verification

- [x] Deploy and verify with real pipeline
  - Pipeline with inline-only sidecars
  - Pipeline with mixed inline + file sidecars
  - Verify sidecar containers appear in K8s pod
  - Verify `fly get-pipeline` round-trips correctly
