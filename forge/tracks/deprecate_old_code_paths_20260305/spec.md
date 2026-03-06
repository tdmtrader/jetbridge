# Spec: Deprecate Old Code Paths

**Track ID:** `deprecate_old_code_paths_20260305`
**Type:** refactor

## Overview

The major Garden/TSA/Baggageclaim packages were removed in Feb 2026, but the abstractions and patterns designed for the multi-worker Garden model remain embedded in the core. The codebase still selects among heterogeneous workers, balances load across machines, negotiates semver compatibility, and coordinates two-phase container destruction — none of which are necessary when there's one K8s worker per namespace.

This track removes dead code, simplifies vestigial abstractions, and aligns the core with K8s-only reality.

## Requirements

1. Remove dead delegate methods (`StreamingVolume`, `WaitingForStreamedVolume`) that are never called
2. Remove deprecated config flags (`LidarScannerInterval`, `Syslog.DrainInterval`)
3. Remove worker shuffling/randomization (no-op with one worker)
4. Inline or remove the `Factory` interface (always returns K8s)
5. Simplify `FindOrSelectWorker` — fail fast instead of polling for workers
6. Remove platform/tags/resource-type filtering that always passes for K8s
7. Simplify worker version compatibility (remove semver logic)
8. Collapse `EnableGlobalResources` branching (always global in K8s)
9. Remove `worker.Spec` fields that are always satisfied
10. Inline the `Streamer` abstraction (no P2P, just ATC pass-through)
11. Remove volume locality optimization (`FindResourceCacheVolume` cross-worker logic)
12. Simplify two-phase container GC for K8s-direct deletion
13. Simplify check container pooling (pods are cheap in K8s)

## Technical Approach

- Work phase-by-phase from lowest risk (dead code) to highest (GC model)
- Each change is independently testable — existing tests must pass after each task
- Use TDD: write/update tests first, then modify production code
- Regenerate counterfeiter fakes when interfaces change

## Acceptance Criteria

- [ ] No dead methods remain in `BuildStepDelegate` interface
- [ ] No deprecated config flags remain in `atc/atccmd/command.go`
- [ ] `Factory` interface removed or inlined — workers created directly
- [ ] `FindOrSelectWorker` no longer polls; fails fast if no worker
- [ ] `worker.Spec` no longer carries always-satisfied fields
- [ ] `Streamer` inlined into pool or removed as separate type
- [ ] Volume locality / cross-worker cache logic removed
- [ ] GC container destruction simplified for K8s-direct model
- [ ] All existing tests pass (unit, integration, E2E)
- [ ] No regressions in pipeline execution

## Out of Scope

- Rewriting `fly hijack` to use `kubectl exec` (functional change)
- Changing `fly workers` output format (user-facing change)
- DB schema migrations to remove historical columns (schema is already clean)
- Removing the `runtime.Worker` interface itself (useful for test doubles)
- Changing the worker registration model (Registrar/Reaper)
