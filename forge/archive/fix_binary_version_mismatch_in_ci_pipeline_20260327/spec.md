# Spec: Fix Binary Version Mismatch in CI Pipeline

**Track ID:** `fix_binary_version_mismatch_in_ci_pipeline_20260327`
**Type:** bugfix

## Overview

The CI pipeline injects `concourse.Version` via ldflags but the binary displays `concourse.JetBridgeVersion` (hardcoded). This causes version mismatch in built images.

## Requirements

1. Pipeline must inject `JetBridgeVersion` via ldflags so the binary reports the correct version.
2. Pipeline must verify the built binary's version output matches expectations, failing on mismatch.

## Acceptance Criteria

- [x] Built binary `--version` includes the version from the `VERSION` file
- [x] Pipeline fails if binary version does not match expected version

## Out of Scope

- Version display format changes
- Version bump logic changes
- Local dev build changes
