# Implementation Plan: Legacy cleanup: dead code, stale config, and broken references

> **Closed 2026-06-07 as a VOID stub — no work performed.** This track was created
> 2026-03-29 but never scoped: the spec was all placeholders and the plan held only
> `<define your first task>` + a generic verification step. On review the premise was
> already satisfied, so there was nothing concrete to implement:
>
> - The legacy subsystems the title targets — Garden, Baggageclaim, TSA, beacon,
>   `gardenruntime` — are fully removed (0 references in active code; the
>   `atc/worker/gardenruntime` dir no longer exists).
> - 0 stale CLI flags / config referencing removed features.
> - The "legacy cleanup" theme is covered by **5 completed tracks**:
>   `legacy-cleanup-20260210`, `deprecate_old_code_paths_20260305`,
>   `dead_code_cleanup_20260310`, `deprecate_old_agent_paths_and_update_tests_20260327`,
>   `deprecate-produces-registry-image`. Remaining backend cleanup (PVC/SPDY) has its
>   own dedicated track `deprecate_pvc_and_spdy_artifact_backends_20260327`.
> - What remained was ~32 ordinary TODO/FIXME markers — routine maintenance, not a
>   coherent deliverable.
>
> Closed rather than reconciled because no work was ever done here and none is needed.

## Phase 1: Implementation

- [x] ~~Task: define scope~~ — closed as a void stub (premise already covered; see note above)
- [x] ~~Task: Phase 1 Manual Verification~~ — N/A (no work performed)

---
