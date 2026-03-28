# Implementation Plan: Bug in empty artifact resolution with DaemonSet

## Phase 1: Fix handle mismatch (Option A)

- [x] Task: In `recordOutputLocations` (`process.go`), replace `ArtifactKey(vol.Handle())` with a lookup that maps each output mount path → DB volume handle via `p.container.dbContainer`. The DB container's volumes know their mount paths and handles. Use `ArtifactKey(dbVolumeHandle)` so it matches what `LookupVolume` → `DaemonSetVolume` will use downstream. d2340a4
- [x] Task: Verify the DB container interface (`db.CreatedContainer` or similar) exposes volume handles with mount paths. If not, add a method or use existing volume metadata to perform the mapping. d2340a4
- [ ] Task: Phase 1 Manual Verification — confirm with a unit test that recording and lookup now use the same key.

## Phase 2: Fail-fast defense in depth

- [x] Task: In `buildArtifactInitContainers` (`container.go:548`), return an error when `artifactLocate` returns `hasLoc = false` in DaemonSet mode. Error message: `"artifact location unknown for key %s: producing step may not have recorded its output"`. c8f4415
- [x] Task: In `daemonSetFetchCommand` (`container.go:611`), if `sourceHostDir` is empty, generate a script that prints a diagnostic and exits 1 instead of `cp -a /. dest/`. c8f4415
- [x] Task: Update callers of `buildArtifactInitContainers` to handle the new error return value. c8f4415
- [ ] Task: Phase 2 Manual Verification

## Phase 3: Tests

- [x] Task: Add unit test — `recordOutputLocations` records using DB volume handle; subsequent `artifactLocate` with the same handle returns the correct `HostDir` and `NodeName`. d2340a4
- [x] Task: Add unit test — `buildArtifactInitContainers` in DaemonSet mode with located artifact produces correct init container (correct `sourceHostDir` in command, `SOURCE_NODE` env var set). c8f4415
- [x] Task: Add unit test — `buildArtifactInitContainers` in DaemonSet mode with missing artifact key returns error. c8f4415
- [x] Task: Add unit test — `daemonSetFetchCommand("")` generates exit-1 script, not `cp -a /.`. c8f4415
- [x] Task: Run `make test-unit` to confirm no regressions. d2340a4
- [ ] Task: Phase 3 Manual Verification

---
